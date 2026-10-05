package services

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/pkg/types"
)

// ---------------------------------------------------------------------------
//  TGPan 闸门 HTTP 接口
//
//  路径（带不带 /api 前缀都认）：
//    GET  /gate/status    当前状态（前端据此决定显示哪个界面）
//    POST /gate/setup     首次设置管理密码 {password}
//    POST /gate/login     登录              {password}
//    POST /gate/logout    登出
//    POST /gate/password  修改密码          {oldPassword, newPassword}
//    POST /gate/repair    清除 TG 凭证，重新配对
//
//  设计原则：这些接口本身不做闸门拦截（否则无法登录），
//  但「改密码 / 清凭证」要求当前请求通过闸门或已设置过密码。
// ---------------------------------------------------------------------------

// gateExemptPath 判断某个请求路径是否豁免闸门拦截。
//
// 这些是「存活探测」类接口，只返回极少量非敏感信息，
// 但被拦住会导致容器健康检查失败、前端连版本都读不到。
// 注意：豁免的路径绝不能返回任何用户数据。
func gateExemptPath(p string) bool {
	switch p {
	case "/version", "/api/version", "/health", "/api/health":
		return true
	}
	return false
}

// gatePairPath 判断是否是「配对 TG」所需的认证接口。
// 这些接口只在闸门已放行（need_pair / ok）时才豁免，
// 不能无条件放行——否则外网未登录的人也能直接调登录接口绕过闸门。
func gatePairPath(p string) bool {
	switch p {
	case "/api/auth/ws", "/auth/ws",
		"/api/auth/session", "/auth/session",
		"/api/auth/login", "/auth/login",
		"/api/auth/logout", "/auth/logout":
		return true
	}
	return false
}

// gateExemptRequest 判断整个请求是否豁免闸门拦截。
//
// 除路径豁免外，还要放行 WebDAV 的**文件访问**请求。
//
// 为什么是整体而不是只放行 OPTIONS：
//   WebDAV 有自己的认证体系（HTTP Basic，凭据存 webdav_credentials 表，
//   bcrypt 哈希）。播放器挂载后会立刻发 PROPFIND 列举目录，这个请求
//   带的是 Basic 凭据而不是闸门 cookie。如果被闸门拦成 401，
//   客户端只会认为"密码错了"，而不是"该去浏览器输管理密码"，
//   表现就是挂载死活连不上。
//
//   放行整条 WebDAV 路径不会降低安全性：真正校验身份的是
//   webdav.go 里的 r.BasicAuth()，没凭据一样返回 401
//   （带 WWW-Authenticate，正好是客户端要的）。
//
// 注意这里**不能**把 /webdav/credentials 也算进豁免。
// 那是给网页界面用的凭据管理接口，走的是闸门 cookie 认证，
// 必须经过下面的闸门块 —— 否则拿不到合成凭证，必然 401。
func gateExemptRequest(r *http.Request) bool {
	if gateExemptPath(r.URL.Path) {
		return true
	}
	p := strings.TrimPrefix(r.URL.Path, "/api")
	if isWebDAVCredentialPath(p) {
		return false
	}
	// WebDAV 全程放行，由 webdav.go 自己做 Basic 认证
	if strings.HasPrefix(p, webdavPrefix) {
		return true
	}
	return false
}

// isWebDAVCredentialPath 判断是否是「WebDAV 凭据管理」接口。
//
// 这类接口虽然长在 /webdav 前缀下，但它们的身份校验走的是闸门 cookie
// （网页里点"生成账号"），跟 WebDAV 文件访问的 Basic 认证是两套东西。
// 必须在豁免判断里把它们区分出来，否则会被当成 WebDAV 文件请求放行，
// 结果拿不到合成凭证 → 一律 401。
func isWebDAVCredentialPath(p string) bool {
	return p == "/webdav/credentials" ||
		strings.HasPrefix(p, "/webdav/credentials/")
}

// writeJSONError 统一错误响应格式：{"error": "..."}
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// gatePath 判断并返回闸门的子路径，未命中返回空串。
func gatePath(p string) string {
	p = strings.TrimPrefix(p, "/api")
	if !strings.HasPrefix(p, "/gate") {
		return ""
	}
	return strings.TrimPrefix(p, "/gate")
}

// GateHTTP 是闸门接口的统一入口。
func (e *extendedService) GateHTTP(w http.ResponseWriter, r *http.Request, sub string) {
	g := e.gate()
	if g == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "闸门未启用")
		return
	}
	switch sub {
	case "/status", "":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st := g.Status(r)
		writeJSON(w, http.StatusOK, st)

	case "/setup":
		e.gateSetup(w, r)
	case "/login":
		e.gateLogin(w, r)
	case "/logout":
		g.clearCookie(w)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/password":
		e.gateChangePassword(w, r)
	case "/repair":
		e.gateRepair(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown gate endpoint")
	}
}

// gateSetup 首次设置管理密码。
// 仅在「尚未设置密码」时允许匿名调用；已设置过则要求旧密码。
func (e *extendedService) gateSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Password    string `json:"password"`
		OldPassword string `json:"oldPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	g := e.gate()
	if err := g.SetPassword(body.OldPassword, body.Password); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 设置成功即视为登录，直接发通行证
	g.issueCookie(w)
	st := g.Status(r)
	st.State = gateStateNeedPair
	if st.Paired {
		st.State = gateStateOK
	}
	writeJSON(w, http.StatusOK, st)
}

// gateLogin 用管理密码登录。
func (e *extendedService) gateLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	g := e.gate()
	if !g.CheckPassword(body.Password) {
		writeJSONError(w, http.StatusUnauthorized, "密码不正确")
		return
	}
	g.issueCookie(w)
	st := g.Status(r)
	st.State = gateStateOK
	writeJSON(w, http.StatusOK, st)
}

// gateChangePassword 修改管理密码（需要旧密码）。
func (e *extendedService) gateChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	g := e.gate()
	// 已通过闸门（Cookie 有效）时允许免旧密码改密
	if !g.verifyCookie(r) && !g.CheckPassword(body.OldPassword) {
		writeJSONError(w, http.StatusUnauthorized, "原密码不正确")
		return
	}
	if err := g.SetPassword(body.OldPassword, body.NewPassword); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	g.issueCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// gateRepair 清除 TG 主凭证，让用户重新扫码配对。
func (e *extendedService) gateRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	g := e.gate()
	st0 := g.Status(r)
	// 需要先过闸门（或本来就免密）才能重新配对
	if st0.State == gateStateNeedLogin {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if err := g.ClearMaster(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "清除凭证失败："+err.Error())
		return
	}
	st := g.Status(r)
	st.State = gateStateNeedPair
	writeJSON(w, http.StatusOK, st)
}

// gateMasterClaims 用主凭证构造一份 JWTClaims，
// 让「内网免密 / 闸门已登录」的请求可以直接当作已登录用户处理。
//
// 返回 nil 表示没有可用主凭证。
func (e *extendedService) gateMasterClaims() *types.JWTClaims {
	g := e.gate()
	if g == nil {
		return nil
	}
	session, userID, ok := g.MasterCredentials()
	if !ok || session == "" || userID == 0 {
		return nil
	}
	now := time.Now().UTC()
	return &types.JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
		},
		Name:      g.MasterName(),
		UserName:  g.MasterUserName(),
		Hash:      g.MasterHash(),
		TgSession: session,
	}
}

// gate 返回闸门服务；未初始化时返回 nil。
func (e *extendedService) gate() *gateService {
	if e == nil || e.api == nil {
		return nil
	}
	return e.api.gate
}

// 确保 auth 包被引用（WithUser 用于把主凭证注入 context）
var _ = auth.WithUser

// buildMasterClaims 用主凭证构造 JWTClaims。
func buildMasterClaims(session string, userID int64, hash string) *types.JWTClaims {
	now := time.Now().UTC()
	return &types.JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
		},
		Hash:      hash,
		TgSession: session,
	}
}
