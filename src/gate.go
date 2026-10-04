package services

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/pkg/types"
)

// ---------------------------------------------------------------------------
//  TGPan 闸门（Gate）
//
//  目标：把「TG 配对」和「日常进入」解耦。
//
//    第一次部署 → 用户自己设一个管理密码 → 扫码配对 TG 一次
//    以后       → 内网入口免密直进；对外域名只要输管理密码，永不再扫码
//
//  数据文件 /data/tgpan-gate.json 同时保存：
//    - 管理密码的 bcrypt 哈希
//    - TG 主凭证（session + 用户信息）
//
//  放在 /data 下 = 容器重启 / 重建 / 升级镜像都不丢。
// ---------------------------------------------------------------------------

const (
	gateFileName   = "tgpan-gate.json"
	gateCookieName = "tgpan_gate"

	// 状态值
	gateStateInit      = "init"       // 没设密码也没凭证：需要「设置初始密码」
	gateStateNeedPair  = "need_pair"  // 有密码没凭证：需要扫码配对 TG
	gateStateReady     = "ready"      // 都齐了
	gateStateNeedLogin = "need_login" // 访问当前 Host 需要密码，但还没登录
	gateStateOK        = "ok"         // 放行
)

// gateData 是落盘的数据结构。所有字段都可 JSON 序列化。
type gateData struct {
	// 管理密码（bcrypt 哈希的 hex）。空 = 还没设置。
	PasswordHash string `json:"passwordHash,omitempty"`

	// TG 主凭证。
	MasterSession string `json:"masterSession,omitempty"` // TG session（StringSession 编码）
	MasterUserID  int64  `json:"masterUserId,omitempty"`
	MasterName    string `json:"masterName,omitempty"`
	MasterUser    string `json:"masterUserName,omitempty"`
	MasterHash    string `json:"masterHash,omitempty"` // 对应的 sessions 表 hash
	PairedAt      string `json:"pairedAt,omitempty"`

	// 闸门通行证签名密钥。首次写入时随机生成，之后固定。
	GateSecret string `json:"gateSecret,omitempty"`
}

// gateService 闸门服务。所有公开方法都是并发安全的。
type gateService struct {
	cfg  *gateConfigView
	mu   sync.RWMutex
	data gateData
}

// gateConfigView 是 gate 服务需要的配置子集，
// 这样不用把整个 ServerCmdConfig 拖进来。
type gateConfigView struct {
	DataFile          string
	RequireLoginHosts []string
	SessionTTL        time.Duration
	Disable           bool
}

// newGateService 载入 / 初始化闸门数据。
func newGateService(dataFile string, requireHosts []string, ttl time.Duration, disable bool) (*gateService, error) {
	if dataFile == "" {
		dataFile = "/data/" + gateFileName
	}
	g := &gateService{cfg: &gateConfigView{
		DataFile:          dataFile,
		RequireLoginHosts: requireHosts,
		SessionTTL:        ttl,
		Disable:           disable,
	}}
	if err := g.load(); err != nil {
		return nil, err
	}
	return g, nil
}

// load 从磁盘读取数据文件。文件不存在不算错误（全新部署）。
func (g *gateService) load() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	b, err := os.ReadFile(g.cfg.DataFile)
	if err != nil {
		if os.IsNotExist(err) {
			// 全新部署：生成一个随机的闸门签名密钥
			g.data = gateData{GateSecret: randomToken(32)}
			return nil
		}
		return fmt.Errorf("read gate file: %w", err)
	}
	if len(b) == 0 {
		g.data = gateData{GateSecret: randomToken(32)}
		return nil
	}
	var d gateData
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("parse gate file: %w", err)
	}
	if d.GateSecret == "" {
		d.GateSecret = randomToken(32)
	}
	g.data = d
	return nil
}

// save 原子写入磁盘（先写临时文件再 rename，避免断电写坏）。
// 调用方必须已持有写锁，或者通过 saveLocked 调用。
func (g *gateService) saveLocked() error {
	dir := filepath.Dir(g.cfg.DataFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir gate dir: %w", err)
	}
	b, err := json.MarshalIndent(g.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := g.cfg.DataFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write gate tmp: %w", err)
	}
	if err := os.Rename(tmp, g.cfg.DataFile); err != nil {
		return fmt.Errorf("rename gate file: %w", err)
	}
	return nil
}

func (g *gateService) save() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.saveLocked()
}

// ---------------------------------------------------------------------------
//  状态判定
// ---------------------------------------------------------------------------

type gateStatus struct {
	// State: init / need_pair / need_login / ok
	State string `json:"state"`
	// HasPassword 是否已设置管理密码
	HasPassword bool `json:"hasPassword"`
	// Paired 是否已配对 TG
	Paired bool `json:"paired"`
	// Bypass 当前 Host 是否免密
	Bypass bool `json:"bypass"`
	// Host 当前请求的 Host（便于排查）
	Host string `json:"host,omitempty"`
	// User 已配对 TG 的用户名（展示用，不含敏感信息）
	User string `json:"user,omitempty"`
	// Name 已配对 TG 的昵称
	Name string `json:"name,omitempty"`
}

// hostNeedsLogin 判断某个 Host 是否需要密码。
//
// 采用「反向白名单」：RequireLoginHosts 里列出的才需要密码，
// 其余（内网 IP、localhost、飞牛入口…）一律免密。
// 这样用户不必事先知道内网地址是什么。
func (g *gateService) hostNeedsLogin(host string) bool {
	if len(g.cfg.RequireLoginHosts) == 0 {
		return false
	}
	h := strings.ToLower(strings.TrimSpace(host))
	// 去掉端口
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	h = strings.TrimSuffix(h, ".")
	for _, want := range g.cfg.RequireLoginHosts {
		w := strings.ToLower(strings.TrimSpace(want))
		if w == "" {
			continue
		}
		if wp, _, err := net.SplitHostPort(w); err == nil {
			w = wp
		}
		w = strings.TrimSuffix(w, ".")
		if w == "" {
			continue
		}
		if h == w {
			return true
		}
		// 支持 *.example.com 通配前缀
		if strings.HasPrefix(w, "*.") && strings.HasSuffix(h, w[1:]) {
			return true
		}
	}
	return false
}

// Status 返回当前状态。needAuthCookie 是浏览器带来的闸门 Cookie（可能为空）。
func (g *gateService) Status(r *http.Request) gateStatus {
	g.mu.RLock()
	d := g.data
	g.mu.RUnlock()

	st := gateStatus{
		HasPassword: d.PasswordHash != "",
		Paired:      d.MasterSession != "",
		User:        d.MasterUser,
		Name:        d.MasterName,
	}
	if r != nil {
		st.Host = r.Host
	}
	if g.cfg.Disable {
		// 闸门关闭：完全回到原版行为，前端直接走 TG 登录
		st.State = gateStateOK
		st.Bypass = true
		return st
	}
	// 判定顺序很重要，两条原则：
	//   1. 「已通过管理密码」优先于「还没配对 TG」——否则用户输完密码
	//      会被永远卡在配对页，密码登录形同虚设。
	//   2. 「内网免密」优先于前面的所有检查——内网入口本就信任，
	//      只需在首次部署时引导设密码，设完即可直进。
	authed := r != nil && g.verifyCookie(r)
	trustedHost := !g.hostNeedsLogin(st.Host)

	switch {
	case !st.HasPassword:
		// 全新部署：先引导设置管理密码（内网/外网都一样，只做一次）
		st.State = gateStateInit
	case trustedHost || authed:
		// 内网免密入口，或已通过管理密码验证
		if !st.Paired {
			// 还没配对 TG：放行进入配对页（这一步仍需要 TG 扫码，但只需一次）
			st.State = gateStateNeedPair
		} else {
			st.State = gateStateOK
			st.Bypass = trustedHost
		}
	default:
		// 外网域名且未通过密码验证
		st.State = gateStateNeedLogin
	}
	return st
}

// ---------------------------------------------------------------------------
//  管理密码
// ---------------------------------------------------------------------------

// SetPassword 设置（或首次设置）管理密码。
// 已设置过密码时，必须提供正确的旧密码（oldPwd）才能修改，
// 防止未登录状态下被任意改密。
func (g *gateService) SetPassword(oldPwd, newPwd string) error {
	if len(strings.TrimSpace(newPwd)) < 4 {
		return errors.New("密码至少 4 位")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.data.PasswordHash != "" {
		if !compareBcryptHex(g.data.PasswordHash, oldPwd) {
			return errors.New("原密码不正确")
		}
	}
	h, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	g.data.PasswordHash = hex.EncodeToString(h)
	if g.data.GateSecret == "" {
		g.data.GateSecret = randomToken(32)
	}
	return g.saveLocked()
}

// CheckPassword 校验管理密码，成功返回 true。
func (g *gateService) CheckPassword(pwd string) bool {
	g.mu.RLock()
	h := g.data.PasswordHash
	g.mu.RUnlock()
	if h == "" {
		return false
	}
	return compareBcryptHex(h, pwd)
}

func compareBcryptHex(hashHex, pwd string) bool {
	raw, err := hex.DecodeString(hashHex)
	if err != nil || len(raw) == 0 {
		// 仍跑一次 bcrypt，避免时序差异暴露「哈希是否合法」
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(pwd))
		return false
	}
	return bcrypt.CompareHashAndPassword(raw, []byte(pwd)) == nil
}

// ---------------------------------------------------------------------------
//  闸门通行证（Cookie）
// ---------------------------------------------------------------------------

// sign 生成 HMAC 通行证：base64(exp|hmac(exp))
func (g *gateService) sign(exp int64) string {
	g.mu.RLock()
	secret := g.data.GateSecret
	g.mu.RUnlock()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "gate|%d", exp)
	sig := hex.EncodeToString(mac.Sum(nil))
	payload := fmt.Sprintf("%d|%s", exp, sig)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// verifyToken 校验通行证是否有效。
func (g *gateService) verifyToken(tok string) bool {
	if tok == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[0], "%d", &exp); err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	g.mu.RLock()
	secret := g.data.GateSecret
	g.mu.RUnlock()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "gate|%d", exp)
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) == 1
}

// verifyCookie 从请求里取闸门 Cookie 并校验。
func (g *gateService) verifyCookie(r *http.Request) bool {
	if r == nil {
		return false
	}
	c, err := r.Cookie(gateCookieName)
	if err != nil {
		return false
	}
	return g.verifyToken(c.Value)
}

// issueCookie 登录成功后颁发通行证。
func (g *gateService) issueCookie(w http.ResponseWriter) string {
	ttl := g.cfg.SessionTTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	exp := time.Now().Add(ttl).Unix()
	tok := g.sign(exp)
	http.SetCookie(w, &http.Cookie{
		Name:     gateCookieName,
		Value:    tok,
		Path:     "/",
		Expires:  time.Unix(exp, 0),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// 不设 Secure：内网 http 访问时设了会导致 Cookie 完全不被保存。
	})
	return tok
}

// clearCookie 让通行证失效（登出）。
func (g *gateService) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     gateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ---------------------------------------------------------------------------
//  TG 主凭证
// ---------------------------------------------------------------------------

// MasterCredentials 返回已配对的主凭证（无凭证时 ok=false）。
func (g *gateService) MasterCredentials() (session string, userID int64, ok bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.data.MasterSession == "" {
		return "", 0, false
	}
	return g.data.MasterSession, g.data.MasterUserID, true
}

// StoreMaster 保存 TG 主凭证（扫码 / 验证码配对成功后调用）。
func (g *gateService) StoreMaster(session string, userID int64, hash, userName, name string) error {
	if session == "" {
		return errors.New("empty master session")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.data.MasterSession = session
	g.data.MasterUserID = userID
	g.data.MasterHash = hash
	g.data.MasterUser = userName
	g.data.MasterName = name
	g.data.PairedAt = time.Now().UTC().Format(time.RFC3339)
	return g.saveLocked()
}

// ClearMaster 清除 TG 主凭证（用于「重新配对 TG」）。
func (g *gateService) ClearMaster() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.data.MasterSession = ""
	g.data.MasterUserID = 0
	g.data.MasterHash = ""
	g.data.PairedAt = ""
	return g.saveLocked()
}

// MasterHash 返回主凭证对应的 sessions 表 hash。
func (g *gateService) MasterHash() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.data.MasterHash
}

// SynthesizeAccessToken 用 JWT 密钥 + 主凭证合成一个有效的 access_token。
//
// 为什么需要：ogen 生成的安全中间件在「请求完全没带凭证」时，会直接判定
// "security requirement is not satisfied"，根本不会调用 handleAuth，
// 我们放在 handleAuth 里的主凭证兜底就永远不生效。
//
// 所以在闸门放行（内网免密 / 已输密码）时，主动合成一个合法 JWT
// 塞进请求，让下游鉴权自然通过 —— 不用改 ogen 生成代码。
//
// jwtSecret 为 JWT 签名密钥；claims 由调用方用主凭证构造。
func (g *gateService) synthesizeCookieValue(jwtSecret string) (string, bool) {
	g.mu.RLock()
	d := g.data
	g.mu.RUnlock()

	if d.MasterSession == "" || d.MasterUserID == 0 {
		return "", false
	}
	claims := &types.JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(d.MasterUserID, 10),
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(24 * time.Hour)),
		},
		Name:      d.MasterName,
		UserName:  d.MasterUser,
		Hash:      d.MasterHash,
		TgSession: d.MasterSession,
	}
	tok, err := auth.Encode(jwtSecret, claims)
	if err != nil {
		return "", false
	}
	return tok, true
}

// SynthesizeAccessToken 对外暴露（供中间件调用）。
func (g *gateService) SynthesizeAccessToken(jwtSecret string) (string, bool) {
	return g.synthesizeCookieValue(jwtSecret)
}

// MasterName 返回已配对 TG 账号的昵称。
func (g *gateService) MasterName() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.data.MasterName
}

// MasterUserName 返回已配对 TG 账号的用户名。
func (g *gateService) MasterUserName() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.data.MasterUser
}

// ---------------------------------------------------------------------------
//  辅助
// ---------------------------------------------------------------------------

// randomToken 生成 n 字节的随机 hex 字符串。
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属于极端情况，退化为时间种子
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}
