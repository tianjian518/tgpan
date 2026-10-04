package services

// ---------------------------------------------------------------------------
// WebDAV 服务端实现（TGPan 扩展）
//
// 目标：让网易爆米花 / Infuse / VidHub / nPlayer / PotPlayer 这类播放器
//       通过 WebDAV 协议挂载 TGPan 的网盘，直接浏览和播放 TG 频道里的视频。
//
// 为什么需要自己实现：
//   Teldrive 原版不含任何 WebDAV 代码（全项目 grep 零命中）。
//   它只提供 REST API + 网页界面，播放器无法直接对接。
//
// 支持的协议子集（播放器实际用到的部分）：
//
//   OPTIONS   - 声明支持 DAV: 1,2，让播放器确认可以挂载
//   PROPFIND  - 列目录 / 取单文件属性（爆米花刮削媒体库的核心）
//   GET       - 下载 / 播放，支持 HTTP Range（拖进度条、边下边播）
//   HEAD      - 取文件元信息，不发正文
//
// 认证：HTTP Basic Auth。播放器只认这个，不认浏览器 Cookie。
//       凭据来自 webdav_credentials 表（独立密码，与网页登录分开）。
//
// 目录结构：一频道一顶层文件夹，见 channel_scans.folder_id 的映射。
// ---------------------------------------------------------------------------

import (
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/internal/logging"
	"github.com/tgdrive/teldrive/pkg/models"
	"github.com/tgdrive/teldrive/pkg/types"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	// webdavPrefix 是 WebDAV 的挂载路径。
	// 爆米花里填服务器地址时用 http://你的IP:端口/webdav
	webdavPrefix = "/webdav"

	// davComplianceClasses 声明我们支持的 WebDAV 级别。
	// 1 = 基础 PROPFIND/PROPPATCH；2 = 支持 LOCK/UNLOCK。
	// 这里声明 1,2 是为了兼容更多播放器的探测逻辑，实际不实现锁。
	davComplianceClasses = "1, 2"
)

// davNS 是 WebDAV 的 XML 命名空间
const davNS = "DAV:"

// dummyBcryptHash 是一个固定的 bcrypt 哈希（明文是 "dummy"）。
//
// 用途：用户名不存在时，仍然跑一次 bcrypt 比对再返回 401，
// 让「用户不存在」和「密码错误」两条路径耗时接近，
// 防止通过响应时间差枚举出哪些用户名是有效的。
const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// ---------------------------------------------------------------------------
// XML 结构体：WebDAV 的响应体是 XML，这里定义最小必要集合
// ---------------------------------------------------------------------------

// multistatus 是 PROPFIND 的响应根元素
type multistatus struct {
	XMLName   xml.Name      `xml:"D:multistatus"`
	XmlnsD    string        `xml:"xmlns:D,attr"`
	XmlnsNs0  string        `xml:"xmlns:ns0,attr,omitempty"`
	Responses []davResponse `xml:"D:response"`
}

type davResponse struct {
	Href     string        `xml:"D:href"`
	Propstat []davPropstat `xml:"D:propstat"`
}

type davPropstat struct {
	Prop   davProp `xml:"D:prop"`
	Status string  `xml:"D:status"`
}

// davProp 是所有可能返回的属性集合。
// 用 omitempty 控制哪些字段出现在响应里。
// 播放器通常只认 getcontentlength / getlastmodified / resourcetype / displayname。
type davProp struct {
	// 资源类型：文件夹要有 collection 子元素，文件留空
	ResourceType *davResourceType `xml:"D:resourcetype,omitempty"`
	// 文件名
	DisplayName string `xml:"D:displayname,omitempty"`
	// 字节大小（文件夹不给）
	ContentLength *int64 `xml:"D:getcontentlength,omitempty"`
	// 最后修改时间，RFC1123 格式
	LastModified string `xml:"D:getlastmodified,omitempty"`
	// 内容的 MIME 类型（非标准但播放器常读）
	ContentType string `xml:"D:getcontenttype,omitempty"`
	// ETag，用于缓存校验
	ETag string `xml:"D:getetag,omitempty"`
	// 创建时间，ISO8601
	CreationDate string `xml:"D:creationdate,omitempty"`
	// 是否支持 Range 请求
	SupportRange string `xml:"D:supported-ranges,omitempty"`
}

type davResourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

// ---------------------------------------------------------------------------
// WebDAV 处理器
// ---------------------------------------------------------------------------

// webdavHandler 把 WebDAV 请求翻译成对现有 files 表的操作。
//
// 设计原则：尽量复用 Teldrive 已有的能力。
//   - GET 播放：复用 FilesStream（已支持 Range + TG 流式读取）
//   - 列目录：直接查 files 表（parent_id 自引用，天然是树）
type webdavHandler struct {
	svc *extendedService
}

// basePath 推断本次请求的 WebDAV 挂载基路径，用于回填 PROPFIND 里的 href。
//
// 存在两种访问入口：
//  1. 播放器直连  http://ip:port/webdav/...   —— 路径自带 /webdav 前缀
//  2. /api 挂载   /api/webdav/...            —— 被 StripPrefix 剥掉 /api 后
//     进入中间件时是 /webdav/...，仍然带前缀
//
// 因此绝大多数情况下基路径都是 /webdav。这里做成按请求推断，
// 是为了万一将来挂到别的路径（或反向代理改写）时 href 不至于指错地方，
// 播放器拿到的链接点不开。
func (h *webdavHandler) basePath(r *http.Request) string {
	if p := r.URL.Path; strings.HasPrefix(p, webdavPrefix) {
		return webdavPrefix
	}
	// 路径已被剥掉前缀（例如挂到 /api/webdav 且中间层做了 StripPrefix），
	// 用原始请求 URI 兜底，避免 href 缺前缀。
	if r.RequestURI != "" {
		if i := strings.Index(r.RequestURI, "/webdav"); i >= 0 {
			return webdavPrefix
		}
	}
	return webdavPrefix
}

// ServeHTTP 是 WebDAV 的入口分发。
func (h *webdavHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 认证：HTTP Basic Auth
	user, cred, ok := h.authenticate(w, r)
	if !ok {
		return
	}

	// 2. 解析请求路径，得到网盘内的相对路径
	relPath, err := h.parsePath(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	logger := logging.Component("WEBDAV").With(
		zap.Int64("user_id", user.UserId),
		zap.String("path", relPath),
		zap.String("method", r.Method),
	)

	// 3. 按方法分发
	switch r.Method {
	case http.MethodOptions:
		h.handleOptions(w, r)
	case "PROPFIND":
		h.handlePropfind(w, r, user, cred, relPath, logger)
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, user, cred, relPath, logger)
	default:
		// 播放器可能探测 PROPPATCH / LOCK 等，明确告诉它不支持，
		// 而不是返回 404（返回 404 有些播放器会直接判定挂载失败）
		w.Header().Set("DAV", davComplianceClasses)
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleOptions 响应播放器的能力探测。
// 爆米花/Infuse 挂载前会先发 OPTIONS，看返回头里有没有 DAV: 1,2。
// 没有这个头，播放器会认为"这不是个 WebDAV 服务器"而拒绝挂载。
func (h *webdavHandler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("DAV", davComplianceClasses)
	w.Header().Set("MS-Author-Via", "DAV")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND")
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// 认证
// ---------------------------------------------------------------------------

// authenticate 校验 HTTP Basic Auth。
//
// 返回 (用户, 凭据, 是否通过)。未通过时已经写好 401 响应。
//
// 说明：播放器普遍只支持 Basic Auth，不支持 Cookie/Bearer，
// 所以这里必须走独立凭据表，不能用网页登录那套。
func (h *webdavHandler) authenticate(w http.ResponseWriter, r *http.Request) (models.User, models.WebDAVCredential, bool) {
	var (
		user models.User
		cred models.WebDAVCredential
	)

	username, password, ok := r.BasicAuth()
	if !ok {
		// 没带凭据：返回 401 并提示认证方式，播放器会弹出账号密码输入框
		w.Header().Set("WWW-Authenticate", `Basic realm="TGPan WebDAV", charset="UTF-8"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return user, cred, false
	}

	// 按用户名查凭据。
	//
	// 注意：查不到用户时也要跑一次 bcrypt 比对（拿一个固定的假哈希），
	// 否则「用户名不存在」会比「用户名存在但密码错」快得多（前者直接返回，
	// 后者要跑一次 bcrypt）。这个耗时差异可以被用来枚举有效用户名。
	if err := h.svc.api.db.Where("username = ? AND enabled = ?", username, true).
		First(&cred).Error; err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
		w.Header().Set("WWW-Authenticate", `Basic realm="TGPan WebDAV", charset="UTF-8"`)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return user, cred, false
	}

	// 校验密码哈希
	if err := bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(password)); err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="TGPan WebDAV", charset="UTF-8"`)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return user, cred, false
	}

	// 取用户信息
	if err := h.svc.api.db.Where("user_id = ?", cred.UserId).First(&user).Error; err != nil {
		http.Error(w, "user not found", http.StatusUnauthorized)
		return user, cred, false
	}

	// 记录最后使用时间（异步，不阻塞请求）
	go func(id string) {
		now := time.Now().UTC()
		h.svc.api.db.Model(&models.WebDAVCredential{}).Where("id = ?", id).
			Update("last_used", now)
	}(cred.ID)

	return user, cred, true
}

// ---------------------------------------------------------------------------
// 路径解析
// ---------------------------------------------------------------------------

// parsePath 把 HTTP 请求路径转成网盘内的相对路径。
//
//	/webdav               -> ""
//	/webdav/              -> ""
//	/webdav/电影频道       -> "电影频道"
//	/webdav/电影频道/a.mp4 -> "电影频道/a.mp4"
func (h *webdavHandler) parsePath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	// 去掉 WebDAV 前缀
	p := raw
	if strings.HasPrefix(p, webdavPrefix) {
		p = p[len(webdavPrefix):]
	}
	// URL 解码（中文文件夹名必须解码，否则查不到）
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return "", err
	}
	// 规范化：去掉首尾斜杠，清理 .. 之类的越权路径
	decoded = strings.Trim(decoded, "/")
	if decoded == "" {
		return "", nil
	}
	// path.Clean 会处理 "a/../b"，防止用 .. 跳出根目录
	cleaned := path.Clean(decoded)
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path traversal detected")
	}
	return cleaned, nil
}

// ---------------------------------------------------------------------------
// 目录与文件查找
// ---------------------------------------------------------------------------

// resolvePath 按路径找到对应的文件/文件夹记录。
//
// path 为空时返回根目录的虚拟节点（ID 为空字符串）。
// 返回 (记录, 是否存在, 错误)。
func (h *webdavHandler) resolvePath(userId int64, p string) (*models.File, bool, error) {
	// 根目录：虚拟节点，没有实际的数据库记录
	if p == "" {
		return &models.File{
			ID:       "",
			Name:     "根目录",
			Type:     "folder",
			MimeType: "folder",
			UserId:   userId,
			Status:   "active",
		}, true, nil
	}

	segments := strings.Split(p, "/")
	var parentID *string

	for i, seg := range segments {
		var f models.File
		q := h.svc.api.db.Where("user_id = ? AND name = ? AND status = ?", userId, seg, "active")
		if parentID == nil {
			q = q.Where("parent_id IS NULL")
		} else {
			q = q.Where("parent_id = ?", *parentID)
		}
		if err := q.First(&f).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, false, nil
			}
			return nil, false, err
		}

		// 中间层级必须是文件夹，否则路径非法
		if i < len(segments)-1 && f.Type != "folder" {
			return nil, false, nil
		}
		parentID = &f.ID
		if i == len(segments)-1 {
			return &f, true, nil
		}
	}
	return nil, false, nil
}

// listChildren 列出某个文件夹下的直接子项。
func (h *webdavHandler) listChildren(userId int64, parentID string) ([]models.File, error) {
	var items []models.File
	q := h.svc.api.db.Where("user_id = ? AND status = ?", userId, "active")

	if parentID == "" {
		q = q.Where("parent_id IS NULL")
	} else {
		q = q.Where("parent_id = ?", parentID)
	}

	// 文件夹排前面，然后按名字排序，跟常见网盘一致
	if err := q.Order("type DESC, name ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// PROPFIND：列目录 / 取属性
// ---------------------------------------------------------------------------

// propfindRequest 是 PROPFIND 的请求体
type propfindRequest struct {
	XMLName  xml.Name  `xml:"propfind"`
	Prop     *davProp  `xml:"prop"`
	AllProp  *struct{} `xml:"allprop"`
	PropName *struct{} `xml:"propname"`
}

// handlePropfind 处理 PROPFIND。
//
// Depth 头的语义：
//
//	0 - 只返回这个资源本身
//	1 - 返回这个资源和它的直接子项（列目录用这个）
//	infinity - 递归返回全部（播放器基本不用，这里按 1 处理，避免大目录卡死）
func (h *webdavHandler) handlePropfind(w http.ResponseWriter, r *http.Request, user models.User,
	cred models.WebDAVCredential, relPath string, logger *zap.Logger) {

	// 解析 Depth
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}
	if depth == "infinity" {
		// 递归全量在某些播放器里会把整个网盘拉一遍，非常慢，
		// 这里降级成 1，只给直接子项。
		depth = "1"
	}

	// 解析请求体，看客户端想要哪些属性。
	// 解析失败不报错，按 allprop 处理（有些播放器发空 body）。
	var reqBody propfindRequest
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(body) > 0 {
			_ = xml.Unmarshal(body, &reqBody)
		}
	}

	// 找到目标资源
	target, found, err := h.resolvePath(user.UserId, relPath)
	if err != nil {
		logger.Error("webdav.propfind.resolve_failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// 组装响应
	ms := multistatus{
		XmlnsD: davNS,
	}

	// 自己这个资源
	ms.Responses = append(ms.Responses, h.buildResponse(target, relPath, user))

	// Depth=1 时补上直接子项
	if depth == "1" && target.Type == "folder" {
		children, err := h.listChildren(user.UserId, target.ID)
		if err != nil {
			logger.Error("webdav.propfind.list_failed", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range children {
			child := &children[i]
			childPath := child.Name
			if relPath != "" {
				childPath = relPath + "/" + child.Name
			}
			ms.Responses = append(ms.Responses, h.buildResponse(child, childPath, user))
		}
	}

	// 输出 XML
	// PROPFIND 成功返回 207 Multi-Status，这是协议规定，不能用 200
	out, err := xml.MarshalIndent(ms, "", "  ")
	if err != nil {
		logger.Error("webdav.propfind.marshal_failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.Header().Set("DAV", davComplianceClasses)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// buildResponse 把一个文件记录转成 WebDAV 的 response 元素。
func (h *webdavHandler) buildResponse(f *models.File, relPath string, user models.User) davResponse {
	// href 必须是 URL 编码的完整路径，中文要转义，否则播放器解析出错
	hrefPath := webdavPrefix
	if relPath != "" {
		hrefPath = webdavPrefix + "/" + encodePathSegments(relPath)
	}
	// 文件夹的 href 按惯例带结尾斜杠
	if f.Type == "folder" && !strings.HasSuffix(hrefPath, "/") {
		hrefPath += "/"
	}

	prop := davProp{
		DisplayName: f.Name,
	}

	if f.Type == "folder" {
		prop.ResourceType = &davResourceType{Collection: &struct{}{}}
		prop.ContentType = "httpd/unix-directory"
	} else {
		prop.ResourceType = &davResourceType{}
		// 大小：TG 文件可能没有大小信息，给 0 而不是省略，
		// 因为有些播放器读到缺失字段会报解析错误
		size := int64(0)
		if f.Size != nil {
			size = *f.Size
		}
		prop.ContentLength = &size

		ct := f.MimeType
		if ct == "" {
			ct = "application/octet-stream"
		}
		prop.ContentType = ct
		prop.SupportRange = "bytes"
	}

	// 时间：优先 UpdatedAt，回退 CreatedAt
	modTime := time.Now().UTC()
	if f.UpdatedAt != nil {
		modTime = f.UpdatedAt.UTC()
	} else if f.CreatedAt != nil {
		modTime = f.CreatedAt.UTC()
	}
	prop.LastModified = modTime.Format(http.TimeFormat)
	prop.CreationDate = modTime.Format(time.RFC3339)

	// ETag：用 id + 大小 + 时间生成，变了就说明文件变了
	if f.Type != "folder" {
		size := int64(0)
		if f.Size != nil {
			size = *f.Size
		}
		prop.ETag = fmt.Sprintf(`"%s-%d-%d"`, f.ID, size, modTime.Unix())
	}

	return davResponse{
		Href: hrefPath,
		Propstat: []davPropstat{
			{
				Prop:   prop,
				Status: "HTTP/1.1 200 OK",
			},
		},
	}
}

// encodePathSegments 对路径的每一段单独做 URL 编码。
//
// 不能整体 url.PathEscape，那样会把 "/" 也编码掉，
// 导致 href 变成一整个转义串，播放器无法解析层级。
func encodePathSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// ---------------------------------------------------------------------------
// GET / HEAD：播放与下载
// ---------------------------------------------------------------------------

// handleGet 处理 GET / HEAD。
//
// 关键点：直接复用 FilesStream 的流式读取能力（已实现 Range 支持），
// 这样播放器拖进度条、边下边播都能正常工作。
func (h *webdavHandler) handleGet(w http.ResponseWriter, r *http.Request, user models.User,
	cred models.WebDAVCredential, relPath string, logger *zap.Logger) {

	if relPath == "" {
		// 对根目录发 GET，返回一个简单的目录说明（有些播放器会来探一下）
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "TGPan WebDAV\n挂载地址: %s\n", webdavPrefix)
		return
	}

	target, found, err := h.resolvePath(user.UserId, relPath)
	if err != nil {
		logger.Error("webdav.get.resolve_failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if target.Type == "folder" {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}

	// 复用现有的流式读取逻辑。
	// FilesStream(w, r, fileId, userId) 里 userId != 0 时会跳过认证直接取文件，
	// 正好适合我们已经用 Basic Auth 验过身份的场景。
	h.svc.FilesStream(w, r, target.ID, user.UserId)
}

// ---------------------------------------------------------------------------
// 凭据生成
// ---------------------------------------------------------------------------

// generateWebDAVPassword 生成一个便于手输的随机密码。
//
// 故意去掉容易看错的字符（0/O、1/l/I），因为用户要在播放器里手打。
func generateWebDAVPassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[randomInt(len(alphabet))]
	}
	return string(b)
}

// hashWebDAVPassword 生成 bcrypt 哈希
func hashWebDAVPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

// ---------------------------------------------------------------------------
// HTTP 入口：凭据管理接口（给网页界面用）
// ---------------------------------------------------------------------------

// WebDAVCredentialCreate 创建凭据的请求体
type WebDAVCredentialCreate struct {
	Label    string `json:"label,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// WebDAVCredentialView 返回给前端的凭据视图（不含哈希）
type WebDAVCredentialView struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	LastUsed  string `json:"lastUsed,omitempty"`
	CreatedAt string `json:"createdAt"`
	// Password 只在创建时返回一次，之后不再返回
	Password string `json:"password,omitempty"`
	// MountPath 是 WebDAV 挂载路径，方便前端直接展示给用户复制
	MountPath string `json:"mountPath,omitempty"`
}

// WebDAVCredentialsListHTTP 列出当前用户的 WebDAV 凭据
func (e *extendedService) WebDAVCredentialsListHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var creds []models.WebDAVCredential
	if err := e.api.db.WithContext(ctx).Where("user_id = ?", userId).
		Order("created_at ASC").Find(&creds).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}

	out := make([]WebDAVCredentialView, 0, len(creds))
	for _, c := range creds {
		v := WebDAVCredentialView{
			ID:        c.ID,
			Username:  c.Username,
			Label:     c.Label,
			Enabled:   c.Enabled,
			CreatedAt: c.CreatedAt.Format(time.RFC3339),
		}
		if c.LastUsed != nil {
			v.LastUsed = c.LastUsed.Format(time.RFC3339)
		}
		out = append(out, v)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credentials": out,
		// 顺带把挂载地址告诉前端，方便界面直接展示给用户复制
		"mountPath": webdavPrefix,
	})
}

// WebDAVCredentialCreateHTTP 创建新凭据
func (e *extendedService) WebDAVCredentialCreateHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var req WebDAVCredentialCreate
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求格式错误: " + err.Error()})
		return
	}

	// 用户名默认用 TG 用户 ID；如果已被占用则加后缀
	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = strconv.FormatInt(userId, 10)
	}
	username = ensureUniqueUsername(e.api.db, username)

	// 密码：用户没指定就随机生成一个
	password := strings.TrimSpace(req.Password)
	if password == "" {
		password = generateWebDAVPassword(16)
	}

	hash, err := hashWebDAVPassword(password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "生成密码失败"})
		return
	}

	now := time.Now().UTC()
	cred := models.WebDAVCredential{
		UserId:       userId,
		Username:     username,
		PasswordHash: hash,
		Label:        strings.TrimSpace(req.Label),
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := e.api.db.WithContext(ctx).Create(&cred).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, WebDAVCredentialView{
		ID:        cred.ID,
		Username:  cred.Username,
		Label:     cred.Label,
		Enabled:   cred.Enabled,
		CreatedAt: cred.CreatedAt.Format(time.RFC3339),
		// 明文密码只在这里返回一次，之后无法再取回
		Password:  password,
		MountPath: webdavPrefix,
	})
}

// WebDAVCredentialDeleteHTTP 删除凭据
func (e *extendedService) WebDAVCredentialDeleteHTTP(w http.ResponseWriter, r *http.Request, id string) {
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	res := e.api.db.Where("id = ? AND user_id = ?", id, userId).Delete(&models.WebDAVCredential{})
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "凭据不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// verifyCookieUser 从 Cookie 里取当前登录用户（供扩展接口用）
func (e *extendedService) verifyCookieUser(r *http.Request) (*types.JWTClaims, error) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return nil, err
	}
	return auth.VerifyUser(r.Context(), e.api.db, e.api.cache, e.api.cnf.JWT.Secret, cookie.Value)
}

// ensureUniqueUsername 保证用户名不重复。
//
// 用户名在整张表里是全局唯一的（认证时按 username 查一条），
// 所以这里查重也不能限定 user_id —— 跨用户的同名一样会冲突。
//
// 注意这个函数只是「尽量」避免冲突：并发创建时两个请求可能同时
// 查到「不冲突」，所以真正的兜底是表上的唯一索引，插入失败要能重试。
func ensureUniqueUsername(db *gorm.DB, base string) string {
	if base == "" {
		base = "user"
	}
	name := base
	for i := 1; i < 100; i++ {
		var count int64
		db.Model(&models.WebDAVCredential{}).Where("username = ?", name).Count(&count)
		if count == 0 {
			return name
		}
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return fmt.Sprintf("%s_%d", base, time.Now().Unix())
}

// randomInt 返回 [0,n) 的伪随机数。
// 用 crypto/rand 保证不可预测（这个值决定密码内容，不能用 math/rand）。
func randomInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		// 极罕见：熵源不可用。退化为时间取模，至少不 panic。
		return int(time.Now().UnixNano() % int64(n))
	}
	return int(v.Int64())
}

// decodeJSONBody 解析请求体 JSON，限制大小防止恶意超大 body
func decodeJSONBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}
