package services

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	"github.com/ogen-go/ogen/ogenerrors"
	"go.uber.org/zap"

	ht "github.com/ogen-go/ogen/http"
	"github.com/tgdrive/teldrive/internal/api"
	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/internal/cache"
	"github.com/tgdrive/teldrive/internal/config"
	"github.com/tgdrive/teldrive/internal/events"
	"github.com/tgdrive/teldrive/internal/logging"
	"github.com/tgdrive/teldrive/internal/tgc"
	"github.com/tgdrive/teldrive/internal/utils"
	"github.com/tgdrive/teldrive/internal/version"
	"github.com/tgdrive/teldrive/pkg/models"
	"gorm.io/gorm"
)

type apiService struct {
	db             *gorm.DB
	cnf            *config.ServerCmdConfig
	cache          cache.Cacher
	botSelector    tgc.BotSelector
	events         events.EventBroadcaster
	channelManager *tgc.ChannelManager

	// gate 是 TGPan 自有登录闸门（管理密码 + TG 主凭证）。
	// 可能为 nil（初始化失败时降级为原版 TG 扫码登录）。
	gate *gateService
}

func (a *apiService) newMiddlewares(ctx context.Context, retries int) []telegram.Middleware {
	return tgc.NewMiddleware(&a.cnf.TG,
		tgc.WithFloodWait(),
		tgc.WithRecovery(ctx),
		tgc.WithRetry(retries),
		tgc.WithRateLimit(),
	)
}

func (a *apiService) VersionVersion(ctx context.Context) (*api.ApiVersion, error) {
	return version.GetVersionInfo(), nil
}

func (a *apiService) EventsGetEvents(ctx context.Context) ([]api.Event, error) {
	//Get latest events within 5 minutes
	userId := auth.GetUser(ctx)
	res := []models.Event{}
	a.db.Model(&models.Event{}).Where("created_at > ?", time.Now().UTC().Add(-10*time.Minute).Format(time.RFC3339)).
		Where("user_id = ?", userId).Order("created_at desc").Find(&res)
	return utils.Map(res, func(item models.Event) api.Event {
		return api.Event{
			ID:        item.ID,
			Type:      item.Type,
			CreatedAt: item.CreatedAt,
			Source: api.Source{
				ID:           item.Source.Data().ID,
				Type:         api.SourceType(item.Source.Data().Type),
				Name:         item.Source.Data().Name,
				ParentId:     item.Source.Data().ParentID,
				DestParentId: api.NewOptString(item.Source.Data().DestParentID),
			},
		}
	}), nil
}

func (a *apiService) EventsEventsStream(ctx context.Context, params api.EventsEventsStreamParams) (*api.EventsEventsStreamOKHeaders, error) {
	return nil, nil
}

func (e *extendedService) EventsEventsStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		http.Error(w, "missing token or authash", http.StatusUnauthorized)
		return
	}
	user, err := auth.VerifyUser(r.Context(), e.api.db, e.api.cache, e.api.cnf.JWT.Secret, cookie.Value)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	userId, _ := strconv.ParseInt(user.Subject, 10, 64)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	eventChan := e.api.events.Subscribe(userId)
	defer e.api.events.Unsubscribe(userId, eventChan)
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-eventChan:
			if !ok {
				return
			}
			src := event.Source.Data()
			if src == nil {
				continue
			}
			eventData := api.Event{
				ID:        event.ID,
				Type:      event.Type,
				CreatedAt: event.CreatedAt,
				Source: api.Source{
					ID:           src.ID,
					Type:         api.SourceType(src.Type),
					Name:         src.Name,
					ParentId:     src.ParentID,
					DestParentId: api.NewOptString(src.DestParentID),
				},
			}

			jsonData, _ := eventData.MarshalJSON()
			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()

		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (a *apiService) NewError(ctx context.Context, err error) *api.ErrorStatusCode {
	var (
		code     = http.StatusInternalServerError
		message  = http.StatusText(code)
		ogenErr  ogenerrors.Error
		apiError *apiError
	)
	switch {
	case errors.Is(err, ht.ErrNotImplemented):
		code = http.StatusNotImplemented
		message = http.StatusText(code)
	case errors.As(err, &ogenErr):
		code = ogenErr.Code()
		message = ogenErr.Error()
	case errors.As(err, &apiError):
		if apiError.code == 0 {
			code = http.StatusInternalServerError
			message = http.StatusText(code)
		} else {
			code = apiError.code
			message = apiError.Error()
		}
		logger := logging.Component("API")
		logger.Error("request.failed", zap.Error(apiError.err))
	}
	return &api.ErrorStatusCode{StatusCode: code, Response: api.Error{Code: code, Message: message}}
}

func NewApiService(db *gorm.DB,
	cnf *config.ServerCmdConfig,
	cache cache.Cacher,
	botSelector tgc.BotSelector,
	events events.EventBroadcaster) *apiService {

	svc := &apiService{
		db:             db,
		cnf:            cnf,
		cache:          cache,
		botSelector:    botSelector,
		events:         events,
		channelManager: tgc.NewChannelManager(db, cache, &cnf.TG),
	}

	// 初始化 TGPan 自有登录闸门。
	// 失败不致命：降级为原版「只有 TG 扫码」的登录方式，
	// 只是用户没法用管理密码进入而已。
	g, err := newGateService(
		cnf.Gate.DataFile,
		cnf.Gate.RequireLoginHosts,
		cnf.Gate.SessionTTL,
		cnf.Gate.Disable,
	)
	if err != nil {
		logging.Component("GATE").Error("gate.init.failed", zap.Error(err))
	} else {
		svc.gate = g
		logging.Component("GATE").Info("gate.init.completed",
			zap.String("dataFile", cnf.Gate.DataFile),
			zap.Strings("requireLoginHosts", cnf.Gate.RequireLoginHosts),
		)
	}

	return svc
}

type extendedService struct {
	api *apiService

	// webdavOnce/webdavHandler 保证 WebDAV 处理器只构造一次
	webdavOnce    sync.Once
	webdavHandler *webdavHandler

	// scanOnce/scanTicker 保证自动扫描器只启动一次
	scanOnce sync.Once

	// dialogOnce 保证「已关注频道」同步器只启动一次
	dialogOnce sync.Once
}

func NewExtendedService(api *apiService) *extendedService {
	return &extendedService{api: api}
}

// webdav 返回 WebDAV 处理器（懒加载单例）
func (e *extendedService) webdav() *webdavHandler {
	e.webdavOnce.Do(func() {
		e.webdavHandler = &webdavHandler{svc: e}
	})
	return e.webdavHandler
}

type extendedMiddleware struct {
	next *api.Server
	srv  *extendedService
}

func (m *extendedMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ---- TGPan 闸门接口（设置密码 / 登录 / 状态查询）----
	// 必须放在最前面：这些接口本身不能要求已登录，否则无法进入。
	if sub := gatePath(r.URL.Path); sub != "" {
		// 只处理明确命中的子路径，避免把 /gateway 之类也吞掉
		switch sub {
		case "/status", "/setup", "/login", "/logout", "/password", "/repair":
			m.srv.GateHTTP(w, r, sub)
			return
		}
	}

	// ---- 闸门拦截 ----
	// 未初始化 / 未配对 / 当前 Host 需要登录且未登录时，
	// 所有 API 请求一律拒绝，并告知前端该显示哪个界面。
	//
	// 例外（不拦，否则会造成可见故障）：
	//   · /version、/health —— 容器健康检查与前端探测，只暴露版本号
	//   · WebDAV 的 OPTIONS —— 播放器（爆米花/Infuse）挂载前会先发不带凭据的
	//     OPTIONS 探测能力，靠响应里的 DAV 头判断"对面是不是 WebDAV 服务器"。
	//     被闸门拦掉会丢 DAV 头 → 播放器直接判挂载失败，连密码框都不弹。
	if g := m.srv.gate(); g != nil && !gateExemptRequest(r) {
		st := g.Status(r)
		// need_pair 属于「已放行、但还没配对 TG」——必须让请求通过，
		// 否则配对页自己会被拦掉，用户永远配不上。
		if st.State == gateStateInit || st.State == gateStateNeedLogin {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "gate not satisfied",
				"gate":  st,
			})
			return
		}

		// 配对通道（/api/auth/*）需要特殊处理：
		//
		// · need_pair —— 用户已放行但还没配对 TG，此刻系统里**没有任何
		//   可用凭证**，SynthesizeAccessToken 也合成不出来。而配对页必须
		//   连上 /auth/ws 扫码。这类请求跳过「合成凭证」这一步，
		//   继续往下交给 ogen（这些接口自身就是登录逻辑，不挂安全要求）。
		// · ok —— 已配对且已放行，同理放行让原逻辑工作（换 TG 账号等）。
		//
		// 注意：只在闸门已放行（need_pair / ok）时才豁免；init 和
		// need_login 在上面就被拦掉了，外网未登录的人无法借道这里绕过闸门。
		//
		// 这里**不能 return**：return 会直接丢掉请求，什么响应都不回。
		if !gatePairPath(r.URL.Path) && m.srv.api != nil && m.srv.api.cnf != nil {
			if tok, ok := g.SynthesizeAccessToken(m.srv.api.cnf.JWT.Secret); ok {
				if _, err := r.Cookie(authCookieName); err != nil {
					r.AddCookie(&http.Cookie{Name: authCookieName, Value: tok})
				}
			}
		}
	}

	// 频道扫描接口（自定义扩展，不走 ogen 路由表）
	// 注意：外层 mux.Mount("/api/", http.StripPrefix("/api", ...)) 已剥掉 /api 前缀，
	//      所以这里看到的路径是 /scan/channel；两种都兼容以防路由配置变化
	if r.Method == http.MethodPost &&
		(r.URL.Path == "/scan/channel" || r.URL.Path == "/api/scan/channel") {
		m.srv.FilesScanChannelHTTP(w, r)
		return
	}

	// ---- 频道自动扫描管理 ----
	// 已关注频道列表（扫描页的频道来源）。
	// 必须放在 /scan/channels 前缀判断**之前** —— 否则会被下面
	// HasPrefix("/scan/channels/") 那条吞掉，parts[0] 变成 "dialogs"，
	// 走成「操作频道 dialogs」然后 404。
	if r.Method == http.MethodGet &&
		(r.URL.Path == "/scan/dialogs" || r.URL.Path == "/api/scan/dialogs") {
		m.srv.DialogsListHTTP(w, r)
		return
	}

	if r.URL.Path == "/scan/channels" || r.URL.Path == "/api/scan/channels" {
		switch r.Method {
		case http.MethodGet:
			m.srv.ChannelsScanListHTTP(w, r)
			return
		case http.MethodPost:
			m.srv.ChannelScanRegisterHTTP(w, r)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/scan/channels/") ||
		strings.HasPrefix(r.URL.Path, "/api/scan/channels/") {
		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api"), "/scan/channels/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) >= 1 && parts[0] != "" {
			if len(parts) == 1 && r.Method == http.MethodDelete {
				m.srv.ChannelScanDeleteHTTP(w, r, parts[0])
				return
			}
			if len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost {
				m.srv.ChannelScanRunNowHTTP(w, r, parts[0])
				return
			}
		}
	}

	// ---- 剧集归档（自动识别的结果 + 手动纠正入口）----
	if r.URL.Path == "/scan/series" || r.URL.Path == "/api/scan/series" {
		if r.Method == http.MethodGet {
			m.srv.SeriesListHTTP(w, r)
			return
		}
	}
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/scan/series/rename", "/api/scan/series/rename":
			m.srv.SeriesRenameHTTP(w, r)
			return
		case "/scan/series/merge", "/api/scan/series/merge":
			m.srv.SeriesMergeHTTP(w, r)
			return
		}
	}

	// 诊断接口（自己处理，不依赖原版前端）
	if r.URL.Path == "/diag" || r.URL.Path == "/api/diag" {
		m.srv.FilesDiagHTTP(w, r)
		return
	}
	if r.URL.Path == "/diag.json" || r.URL.Path == "/api/diag.json" {
		m.srv.FilesDiagJSON(w, r)
		return
	}
	// 登录链路自检：真实跑一次「请求验证码」，把每步结果摊在网页上
	if r.URL.Path == "/logincheck" || r.URL.Path == "/api/logincheck" {
		m.srv.FilesLoginCheckHTTP(w, r)
		return
	}
	// 登录事件日志：记录后端收到的消息与 TG 的返回
	if r.URL.Path == "/authlog" || r.URL.Path == "/api/authlog" {
		m.srv.FilesAuthLogHTTP(w, r)
		return
	}

	// ---- WebDAV 凭据管理接口（网页界面里生成/查看/删除密码用）----
	// 注意顺序：必须先于下面的 webdavPrefix 前缀判断。
	// 否则 /api/webdav/credentials 被 StripPrefix 剥成 /webdav/credentials 后，
	// 会被 WebDAV 处理器当成一个 DAV 路径抢走，返回 Basic 401 而不是正常的登录校验。
	if r.URL.Path == "/webdav/credentials" || r.URL.Path == "/api/webdav/credentials" {
		switch r.Method {
		case http.MethodGet:
			m.srv.WebDAVCredentialsListHTTP(w, r)
			return
		case http.MethodPost:
			m.srv.WebDAVCredentialCreateHTTP(w, r)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/webdav/credentials/") ||
		strings.HasPrefix(r.URL.Path, "/api/webdav/credentials/") {
		id := path.Base(r.URL.Path)
		switch r.Method {
		case http.MethodDelete:
			m.srv.WebDAVCredentialDeleteHTTP(w, r, id)
			return
		}
	}

	// ---- WebDAV：给播放器（网易爆米花 / Infuse / VidHub）挂载用 ----
	// 必须放在 SPA 兜底之前，否则会被前端路由吃掉。
	if strings.HasPrefix(r.URL.Path, webdavPrefix) {
		m.srv.webdav().ServeHTTP(w, r)
		return
	}

	// 注意：不要在这里"补 /api 前缀"！
	// ogen 的路由表内部已按 servers.url = "{url}/api" 处理，直接传 r.URL.Path 即可。
	// 曾尝试补前缀，结果 FindRoute 虽然命中，但 ogen 内部据路径解析参数时出错，
	// 导致 WebSocket 返回的 101 缺少 Sec-WebSocket-Accept 头（假升级，浏览器拒绝）。
	route, ok := m.next.FindRoute(r.Method, r.URL.Path)
	if !ok {
		m.next.ServeHTTP(w, r)
		return
	}
	switch route.Name() {
	case api.AuthWsOperation:
		m.srv.AuthWs(w, r)
		return
	case api.FilesStreamOperation:
		args := route.Args()
		m.srv.FilesStream(w, r, args[0], 0)
		return
	case api.SharesStreamOperation:
		args := route.Args()
		m.srv.SharesStream(w, r, args[0], args[1])
		return

	case api.EventsEventsStreamOperation:
		m.srv.EventsEventsStream(w, r)
		return
	}
	m.next.ServeHTTP(w, r)
}

func NewExtendedMiddleware(next *api.Server, srv *extendedService) *extendedMiddleware {
	return &extendedMiddleware{next: next, srv: srv}
}

type apiError struct {
	err  error
	code int
}

func (a apiError) Error() string {
	return a.err.Error()
}

func (a *apiError) Unwrap() error {
	return a.err
}

var (
	_ api.Handler = (*apiService)(nil)
	_ error       = apiError{}
)

// StartScanSchedulerFor 供 cmd 包在服务启动时调用，
// 启动频道自动扫描调度器。
//
// 参数是 NewExtendedMiddleware 的返回值。这里用 any 接收是为了
// 不必把中间件类型导出（该类型只在包内使用）。
// 内部会做类型断言，取不到就静默跳过 —— 调度器是增强功能，
// 即使没启动也不该影响服务本身正常提供。
func StartScanSchedulerFor(mw any) {
	if m, ok := mw.(*extendedMiddleware); ok && m != nil && m.srv != nil {
		m.srv.StartScanScheduler()
		// 顺带启动「已关注频道」同步器：扫描页的频道列表靠它刷新。
		// 两个调度器互相独立：一个挂了不影响另一个。
		m.srv.StartDialogScheduler()
	}
}
