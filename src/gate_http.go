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
//  TGPan 闸门 HTTP 接口（极简版）
//
//  路径（带不带 /api 前缀都认）：
//    GET  /gate/status   当前状态（前端据此决定显示登录页还是主界面）
//    POST /gate/claim    用一次性领取码换取浏览器门票（登录成功后）
//    POST /gate/logout   退出登录（清除 TG 凭证 + 门票）
//    POST /gate/repair   换一个 TG 账号（等同退出）
//
//  没有 setup / login / password 了 —— 登录就是「扫码或验证码登 TG」本身。
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

// gatePairPath 判断是否是「登录 TG」所需的认证接口。
//
// 未配对时前端必须能连 /auth/ws 扫码，所以这类接口在闸门未放行时
// 也要放行 —— 它们自身就是登录逻辑，不挂安全要求。
func gatePairPath(p string) bool {
	p = strings.TrimPrefix(p, "/api")
	switch p {
	case "/auth/ws",
		"/auth/session",
		"/auth/login",
		"/auth/logout":
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
//   客户端只会认为"密码错了"，而不是"该去浏览器登录"，
//   表现就是挂载死活连不上。
//
//   放行整条 WebDAV 路径不会降低安全性：真正校验身份的是
//   webdav.go 里的 r.BasicAuth()，没凭据一样返回 401
//   （带 WWW-Authenticate，正好是客户端要的）。
//
// 注意这里**不能**把 /webdav/credentials 也算进豁免。
// 那是给网页界面用的凭据管理接口，走的是闸门认证，
// 必须经过下面的闸门块 —— 否则拿不到合成凭证，必然 401。
func gateExemptRequest(r *http.Request) bool {
	if gateExemptPath(r.URL.Path) {
		return true
	}
	// 登录 TG 本身的接口
	if gatePairPath(r.URL.Path) {
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
// 这类接口虽然长在 /webdav 前缀下，但它们的身份校验走的是闸门身份，
// 跟 WebDAV 文件访问的 Basic 认证是两套东西。
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
		writeJSON(w, http.StatusOK, g.Status(r))

	case "/claim":
		e.gateClaim(w, r)

	case "/logout":
		// 退出 = 清掉服务端 TG 凭证 + 浏览器门票，回到登录页。
		if err := g.ClearMaster(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "退出失败："+err.Error())
			return
		}
		g.clearCookie(w)
		writeJSON(w, http.StatusOK, g.Status(r))

	case "/repair":
		// 换一个 TG 账号：等同退出后重新登录
		if err := g.ClearMaster(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "清除凭证失败："+err.Error())
			return
		}
		g.clearCookie(w)
		writeJSON(w, http.StatusOK, g.Status(r))

	default:
		writeJSONError(w, http.StatusNotFound, "unknown gate endpoint")
	}
}

// gateClaim 用一次性领取码换取浏览器门票。
//
// 为什么要有这一步：TG 登录成功发生在 WebSocket 里，升级握手后
// 拿不到 http.ResponseWriter，没法 Set-Cookie。所以由前端拿码
// 回来打这个普通 HTTP 接口 —— 这里能正常写 Cookie。
//
// 【安全设计】见 gate.go 里 NewClaim/ConsumeClaim 的注释：
// 256 位随机、60 秒有效、用过即焚、绑定 User-Agent。
func (e *extendedService) gateClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	g := e.gate()
	if !g.ConsumeClaim(body.Token, r.UserAgent()) {
		writeJSONError(w, http.StatusUnauthorized, "登录凭据无效或已过期，请重新登录")
		return
	}
	// 服务端必须已经有 TG 凭证，否则这张票没意义
	if _, _, ok := g.MasterCredentials(); !ok {
		writeJSONError(w, http.StatusConflict, "服务端还没有 TG 凭证，请重新登录")
		return
	}
	g.issueCookie(w)
	writeJSON(w, http.StatusOK, g.Status(r))
}

// gateMasterClaims 用主凭证构造一份 JWTClaims。
func (e *extendedService) gateMasterClaims() *types.JWTClaims {
	g := e.gate()
	if g == nil {
		return nil
	}
	session, userID, ok := g.MasterCredentials()
	if !ok || session == "" || userID == 0 {
		return nil
	}
	return buildMasterClaims(session, userID, g.MasterHash())
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
