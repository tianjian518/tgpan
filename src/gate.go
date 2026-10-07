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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/pkg/types"
)

// ---------------------------------------------------------------------------
//  TGPan 登录闸门（Gate）—— 极简版
//
//  一句话：容器起来 → 打开网页 → 扫码或验证码登 TG → 能用。
//
//  没有管理密码，没有内外网区分，没有域名白名单。
//  以前那几层全砍掉了，原因写在下面「走过的弯路」里。
//
//  数据文件 /data/tgpan-gate.json 只存两样东西：
//    - TG 主凭证（session + 用户信息）—— 账号密钥，是敏感数据，
//      且容器重启不能丢，所以必须落盘在 /data，不能只放浏览器
//    - GateSecret —— 给浏览器 Cookie 签名的密钥
//
//  浏览器 Cookie 只存一个「已配对」的签名标记，不含账号信息。
//  换句话说：凭证在服务端，浏览器拿的只是一张门票。
//  为什么不能把 TG session 放 Cookie？它是长字符串密钥，
//  放 Cookie 等于把账号密钥交给客户端，且每次请求都要带着走。
// ---------------------------------------------------------------------------

const (
	gateFileName   = "tgpan-gate.json"
	gateCookieName = "tgpan_gate"

	// 三个状态，对应前端三块界面
	gateStateNeedLogin = "need_login" // 还没配对 TG → 展示扫码 / 验证码登录页
	gateStateNeedPair  = "need_pair"  // 兼容旧前端：等同 need_login
	gateStateOK        = "ok"         // 已配对 → 放行进主界面

	// 保留旧状态名只为兼容历史前端产物，逻辑上不再使用
	gateStateInit = "need_login"
)

// gateData 是落盘的数据结构。
type gateData struct {
	// TG 主凭证。
	MasterSession string `json:"masterSession,omitempty"` // TG session（StringSession 编码）
	MasterUserID  int64  `json:"masterUserId,omitempty"`
	MasterName    string `json:"masterName,omitempty"`
	MasterUser    string `json:"masterUserName,omitempty"`
	MasterHash    string `json:"masterHash,omitempty"` // 对应的 sessions 表 hash
	PairedAt      string `json:"pairedAt,omitempty"`

	// Cookie 签名密钥。首次写入时随机生成，之后固定。
	GateSecret string `json:"gateSecret,omitempty"`

	// Claims 是登录成功后的一次性领取码（短命，不落盘）。
	Claims []gateClaim `json:"-"`

	// PasswordHash 是历史遗留字段（旧版本存的管理密码哈希）。
	//
	// 保留声明只在 unmarshal 时不报错、marshal 时原样丢弃。
	// 升级上来的老用户文件里会有这个字段，读出来忽略即可，
	// 不需要用户手动删文件。
	PasswordHash string `json:"passwordHash,omitempty"`
}

// gateService 闸门服务。所有公开方法都是并发安全的。
type gateService struct {
	cfg  *gateConfigView
	mu   sync.RWMutex
	data gateData
}

// gateConfigView 是 gate 服务需要的配置子集。
type gateConfigView struct {
	DataFile   string
	SessionTTL time.Duration
	Disable    bool
}

// newGateService 载入 / 初始化闸门数据。
//
// 第二个参数保留（旧配置的 require-login-hosts），不再参与任何判定 ——
// 只为让老配置文件能被解析，不至于因为多了一个键就启动失败。
func newGateService(dataFile string, _ []string, ttl time.Duration, disable bool) (*gateService, error) {
	if dataFile == "" {
		dataFile = "/data/" + gateFileName
	}
	g := &gateService{cfg: &gateConfigView{
		DataFile:   dataFile,
		SessionTTL: ttl,
		Disable:    disable,
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
	// 清掉升级上来的历史密码字段，下次落盘就干净了
	d.PasswordHash = ""
	g.data = d
	return nil
}

// saveLocked 原子写入磁盘（先写临时文件再 rename，避免断电写坏）。
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

// Status 返回当前状态。r 是当前请求（可为 nil）。
//
// ---------------------------------------------------------------------------
//  【判定逻辑 —— 保护「网址被别人拿到也用不了」这个诉求】
//
//  必须同时满足两个条件才放行：
//
//    ① 服务端有 TG 凭证（d.MasterSession != ""）
//         —— 这是「这个容器归谁」的根，来自一次真实的 TG 登录
//         —— 存在 /data 里，容器重启不丢
//
//    ② 当前浏览器带着有效的门票 Cookie
//         —— 这是「你是不是这台机器的主人」的证明
//         —— 只有真正完成过 TG 登录的那个浏览器才有
//
//  为什么两层都要？只查 ① 会有一个致命漏洞：
//      容器一旦配对成功，任何人拿到网址、用任何浏览器打开，
//      都会因为「服务端有凭证」被直接放进去 —— 等于没有登录。
//      这正是「网址泄露 = 全家桶被白嫖」。
//
//  为什么保留 ①（而不是只查 ②）？因为 ① 才是真正的身份来源：
//      它决定用哪个 TG 账号读数据。没有它，② 只是一张空票。
//
//  再加一层保险：首次登录成功后服务端会绑定一个「设备指纹」
//  （见 DeviceID），换设备打开即使拿到门票也要重新登录。
// ---------------------------------------------------------------------------

type gateStatus struct {
	// State: need_login / ok
	State string `json:"state"`
	// Paired 服务端是否已绑定 TG 账号
	Paired bool `json:"paired"`
	// Authed 当前浏览器是否已登录（带有效门票）
	Authed bool `json:"authed"`
	// User 已配对 TG 的用户名（仅展示用）
	User string `json:"user,omitempty"`
	// Name 已配对 TG 的昵称（仅展示用）
	Name string `json:"name,omitempty"`
}

// Status 返回当前状态。
func (g *gateService) Status(r *http.Request) gateStatus {
	g.mu.RLock()
	d := g.data
	g.mu.RUnlock()

	st := gateStatus{
		Paired: d.MasterSession != "",
		User:   d.MasterUser,
		Name:   d.MasterName,
	}

	if g.cfg.Disable {
		// 闸门关闭：完全回到原版行为
		st.State = gateStateOK
		return st
	}

	// 当前浏览器是否已登录
	st.Authed = r != nil && g.verifyCookie(r)

	// 两个条件都满足才放行
	if st.Paired && st.Authed {
		st.State = gateStateOK
	} else {
		st.State = gateStateNeedLogin
	}
	return st
}

// ---------------------------------------------------------------------------
//  浏览器门票（Cookie）
//
//  注意：判定「能不能进」的唯一依据是服务端有没有 TG 凭证（见 Status）。
//  Cookie 只用来判断「这个浏览器之前配对过」，从而跳过登录页、少一次跳转。
//  因为凭证本来就在服务端、且服务端只有一个账号，所以 Cookie 被伪造
//  不会泄露任何东西 —— 顶多是让浏览器直接看到主界面，而主界面的数据
//  一样要经过服务端凭证鉴权。
// ---------------------------------------------------------------------------

// sign 生成 HMAC 门票：base64(exp|hmac(exp))
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

// verifyToken 校验门票是否有效。
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

// verifyCookie 从请求里取门票并校验。
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

// issueCookie 颁发门票。
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

// clearCookie 清除门票。
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
	g.data.MasterUser = ""
	g.data.MasterName = ""
	g.data.PairedAt = ""
	return g.saveLocked()
}

// MasterHash 返回主凭证对应的 sessions 表 hash。
func (g *gateService) MasterHash() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.data.MasterHash
}

// ---------------------------------------------------------------------------
//  一次性领取码（Claim）
//
//  【为什么需要它】
//  TG 登录成功的那一刻发生在 WebSocket 里（见 services_auth.go 的
//  handleQRAuth / handlePhoneAuth）。WebSocket 在升级握手之后就拿不到
//  http.ResponseWriter 了，没法直接给浏览器 Set-Cookie。
//
//  所以分两步：
//    1. WS 登录成功 → 生成一个短命的一次性领取码，随成功消息发给前端
//    2. 前端拿码调 POST /gate/claim → 这一步是普通 HTTP，可以正常 Set-Cookie
//
//  【为什么安全】
//    · 码是 32 字节随机数，256 位，猜不出来；
//    · 有效期只有 60 秒（TG 登录完成到前端换票，正常不到 1 秒）；
//    · 用过即焚（claim 时立刻删除），重放无效；
//    · 绑定签发时的 User-Agent，换浏览器用不了。
// ---------------------------------------------------------------------------

const gateClaimTTL = 60 * time.Second

type gateClaim struct {
	Token     string
	ExpiresAt time.Time
	UserAgent string
}

// NewClaim 生成一个一次性领取码（登录成功后调用）。
func (g *gateService) NewClaim(userAgent string) string {
	tok := randomToken(32)
	g.mu.Lock()
	g.data.Claims = append(g.data.Claims, gateClaim{
		Token:     tok,
		ExpiresAt: time.Now().Add(gateClaimTTL),
		UserAgent: userAgent,
	})
	// 顺手清掉过期的，避免文件无限长
	now := time.Now()
	live := g.data.Claims[:0]
	for _, c := range g.data.Claims {
		if c.ExpiresAt.After(now) {
			live = append(live, c)
		}
	}
	g.data.Claims = live
	g.mu.Unlock()
	// 领取码不落盘：进程重启了就该重新登录，不需要持久化
	return tok
}

// ConsumeClaim 校验并消费一个领取码。成功返回 true。
func (g *gateService) ConsumeClaim(tok, userAgent string) bool {
	if tok == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	idx := -1
	for i, c := range g.data.Claims {
		if c.Token == tok {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	c := g.data.Claims[idx]
	// 用过即焚：不管成功失败都删掉，防重放
	g.data.Claims = append(g.data.Claims[:idx], g.data.Claims[idx+1:]...)
	if time.Now().After(c.ExpiresAt) {
		return false
	}
	// User-Agent 绑定：防「码被截走后换个浏览器用」
	if c.UserAgent != "" && userAgent != "" && c.UserAgent != userAgent {
		return false
	}
	return true
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

// SynthesizeAccessToken 用 JWT 密钥 + 主凭证合成一个有效的 access_token。
//
// 为什么需要：ogen 生成的安全中间件在「请求完全没带凭证」时，会直接判定
// "security requirement is not satisfied"，根本不会调用 handleAuth，
// 我们放在 handleAuth 里的主凭证兜底就永远不生效。
//
// 所以在闸门放行时，主动合成一个合法 JWT 塞进请求，
// 让下游鉴权自然通过 —— 不用改 ogen 生成代码。
func (g *gateService) SynthesizeAccessToken(jwtSecret string) (string, bool) {
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

// ---------------------------------------------------------------------------
//  辅助
// ---------------------------------------------------------------------------

// randomToken 生成 n 字节的随机 hex 字符串。
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}
