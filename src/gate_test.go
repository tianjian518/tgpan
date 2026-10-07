package services

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/pkg/types"
)

// authDecodeForTest 是 auth.Decode 的测试包装。
func authDecodeForTest(secret, tok string) (*types.JWTClaims, error) {
	return auth.Decode(secret, tok)
}

// newTestGate 建一个临时目录里的闸门实例。
func newTestGate(t *testing.T) *gateService {
	t.Helper()
	dir := t.TempDir()
	g, err := newGateService(
		filepath.Join(dir, "tgpan-gate.json"),
		nil,
		30*24*time.Hour,
		false,
	)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	return g
}

// TestGateStateMachine 覆盖状态流转：need_login → ok。
//
// 【核心设计】只有两个状态，只看服务端有没有 TG 凭证：
//   · 没配对 → need_login（前端展示扫码 / 验证码登录）
//   · 已配对 → ok（放行进主界面）
//
// 不看 Host、不看密码、不看内外网 —— 以前那三层分叉全砍掉了。
func TestGateStateMachine(t *testing.T) {
	g := newTestGate(t)

	// 1) 全新：没凭证 → need_login（不管什么 Host 都一样）
	for _, host := range []string{"pan.example.com", "10.0.0.119:30141", "localhost:8080"} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.Host = host
		st := g.Status(r)
		if st.State != gateStateNeedLogin {
			t.Fatalf("fresh state(host=%s) = %q, want %q", host, st.State, gateStateNeedLogin)
		}
		if st.Paired {
			t.Fatalf("fresh state(host=%s) Paired should be false", host)
		}
	}

	// 2) 配对 TG 后，**但没带门票** → 仍然 need_login
	//
	// 【这一条是关键安全断言】服务端有凭证 ≠ 这个浏览器能进。
	// 否则任何人拿到网址、用任意浏览器打开都会被直接放进去。
	if err := g.StoreMaster("FAKESESSION123", 42, "hash42", "alice", "Alice"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	for _, host := range []string{"pan.example.com", "10.0.0.119:30141", "localhost:8080"} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.Host = host
		st := g.Status(r)
		if st.State != gateStateNeedLogin {
			t.Fatalf("paired-but-no-ticket state(host=%s) = %q, want %q（没门票不能放行）",
				host, st.State, gateStateNeedLogin)
		}
		if !st.Paired {
			t.Fatalf("paired state(host=%s) Paired should be true", host)
		}
		if st.Authed {
			t.Fatalf("paired state(host=%s) Authed should be false（没带门票）", host)
		}
	}

	// 3) 配对 + 带有效门票 → ok（同样不管 Host）
	for _, host := range []string{"pan.example.com", "10.0.0.119:30141", "localhost:8080"} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.Host = host
		r.AddCookie(&http.Cookie{Name: gateCookieName, Value: g.sign(time.Now().Add(time.Hour).Unix())})
		st := g.Status(r)
		if st.State != gateStateOK {
			t.Fatalf("paired+ticket state(host=%s) = %q, want %q", host, st.State, gateStateOK)
		}
		if !st.Paired || !st.Authed {
			t.Fatalf("paired+ticket state(host=%s) Paired=%v Authed=%v", host, st.Paired, st.Authed)
		}
		if st.User != "alice" || st.Name != "Alice" {
			t.Fatalf("paired state(host=%s) user=%q name=%q", host, st.User, st.Name)
		}
	}
}

// TestGateNoPasswordConcept 钉住「没有管理密码」这个不变量。
//
// 旧版本有一整套管理密码（SetPassword / CheckPassword / bcrypt）。
// 它带来的问题：用户以为设了密码就万事大吉，结果 TG 没配对 → 功能全 401。
// 现在登录 == 扫码登 TG，不再有第二套凭据。
//
// 这条测试确保：
//   1. 没有密码时也是 need_login（而不是某个 init 状态）
//   2. 不存在「有密码但没凭证就放行」这种情况
func TestGateNoPasswordConcept(t *testing.T) {
	g := newTestGate(t)

	// 只有 Cookie（伪造的"已登录"标记）也不能进 —— 判定依据是服务端凭证
	r := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r.Host = "pan.example.com"
	r.AddCookie(&http.Cookie{
		Name:  gateCookieName,
		Value: g.sign(time.Now().Add(time.Hour).Unix()),
	})
	if st := g.Status(r); st.State != gateStateNeedLogin {
		t.Fatalf("cookie-only state = %q, want %q（光有门票、服务端没凭证也不行）",
			st.State, gateStateNeedLogin)
	}
}

// TestGateAnyHostSameBehavior 任何 Host 行为必须一致。
//
// 踩过的坑：曾经按 Host 分叉（内网免密 / 外网要密码 / 反向白名单），
// 结果用户在不同设备上看到的行为不一致，而且反代会改写 Host，
// 判断根本不可靠。现在一律同一条路径。
func TestGateAnyHostSameBehavior(t *testing.T) {
	g := newTestGate(t)

	hosts := []string{
		"pan.example.com", "example.com", "www.example.com:443",
		"127.0.0.1", "127.0.0.1:8080", "localhost", "localhost:30141",
		"10.0.0.119:30141", "192.168.1.1", "172.16.0.5", "169.254.1.1",
		"[::1]:8080", "nas", "tgpanserver", "mybox.lan", "host.local",
		"weird.example", "x",
	}
	for _, h := range hosts {
		r := httptest.NewRequest("GET", "http://example.com/", nil)
		r.Host = h
		if st := g.Status(r); st.State != gateStateNeedLogin {
			t.Errorf("host=%q state = %q, want %q（所有 Host 行为必须一致）",
				h, st.State, gateStateNeedLogin)
		}
	}

	// 配对 + 带门票后同样全部一致
	if err := g.StoreMaster("S", 1, "h", "u", "U"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	tok := g.sign(time.Now().Add(time.Hour).Unix())
	for _, h := range hosts {
		r := httptest.NewRequest("GET", "http://example.com/", nil)
		r.Host = h
		r.AddCookie(&http.Cookie{Name: gateCookieName, Value: tok})
		if st := g.Status(r); st.State != gateStateOK {
			t.Errorf("paired+ticket host=%q state = %q, want %q", h, st.State, gateStateOK)
		}
	}
}

// TestGateTokenRoundTrip 门票签发 / 校验 / 过期 / 篡改。
func TestGateTokenRoundTrip(t *testing.T) {
	g := newTestGate(t)

	if g.verifyToken("") {
		t.Fatal("empty token should not verify")
	}
	if g.verifyToken("garbage!!") {
		t.Fatal("garbage token should not verify")
	}

	tok := g.sign(time.Now().Add(time.Hour).Unix())
	if !g.verifyToken(tok) {
		t.Fatal("valid token rejected")
	}

	old := g.sign(time.Now().Add(-time.Hour).Unix())
	if g.verifyToken(old) {
		t.Fatal("expired token accepted")
	}

	if g.verifyToken(tok + "x") {
		t.Fatal("tampered token accepted")
	}
}

// TestGatePersistence 落盘后重新载入，主凭证还在。
//
// 【为什么要测这个】TG session 是账号密钥，容器重启 / 升级镜像后
// 必须还在，否则用户每次重启都要重新扫码 —— 这体验不能接受。
func TestGatePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgpan-gate.json")

	g1, err := newGateService(path, nil, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	if err := g1.StoreMaster("SESSION-XYZ", 777, "hash777", "bob", "Bob"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	secret1 := g1.data.GateSecret

	// 重新载入（模拟容器重启）
	g2, err := newGateService(path, nil, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	sess, uid, ok := g2.MasterCredentials()
	if !ok || sess != "SESSION-XYZ" || uid != 777 {
		t.Fatalf("master creds lost after reload: ok=%v sess=%q uid=%d", ok, sess, uid)
	}
	if g2.MasterHash() != "hash777" {
		t.Fatalf("master hash lost: %q", g2.MasterHash())
	}
	if g2.MasterName() != "Bob" || g2.MasterUserName() != "bob" {
		t.Fatalf("name lost: %q / %q", g2.MasterName(), g2.MasterUserName())
	}
	// 签名密钥必须稳定，否则重启后所有浏览器门票失效
	if g2.data.GateSecret != secret1 {
		t.Fatal("gate secret changed after reload —— 重启后门票会全部失效")
	}
}

// TestGateUpgradeFromPasswordVersion 升级兼容：
// 老版本的数据文件带 passwordHash 字段，新版本要能正常读、忽略它、不报错。
//
// 这是真实场景 —— 已经部署过的用户（比如跑 2.7.x 的）直接换新镜像，
// /data 里的 tgpan-gate.json 是旧的。如果解析失败，闸门会整个起不来。
func TestGateUpgradeFromPasswordVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgpan-gate.json")

	// 模拟 2.7.x 的老文件：有 passwordHash、有凭证
	oldJSON := `{
  "passwordHash": "5f4dcc3b5aa765d61d8327deb882cf99",
  "masterSession": "OLD-SESSION",
  "masterUserId": 555,
  "masterName": "OldUser",
  "masterUserName": "olduser",
  "masterHash": "oldhash",
  "pairedAt": "2026-01-01T00:00:00Z",
  "gateSecret": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
}`
	if err := os.WriteFile(path, []byte(oldJSON), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	g, err := newGateService(path, []string{"pan.2016.de5.net"}, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("load old-format gate file: %v", err)
	}
	if _, _, ok := g.MasterCredentials(); !ok {
		t.Fatal("old master credentials should still load")
	}
	if g.MasterName() != "OldUser" {
		t.Fatalf("name = %q", g.MasterName())
	}
	if g.data.PasswordHash != "" {
		t.Fatal("legacy passwordHash should be dropped in memory")
	}
	// 老用户升级上来：服务端有凭证，但浏览器还没有新门票 → 需要重新登录一次
	// （这是有意为之：门票是 v2.8 新引入的，老 Cookie 不认，
	//   用户重新扫码一次即可，之后长期有效）
	r := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r.Host = "pan.example.com"
	if st := g.Status(r); st.State != gateStateNeedLogin {
		t.Fatalf("state after upgrade (no ticket) = %q, want %q", st.State, gateStateNeedLogin)
	}
	// 带上门票后就 ok
	r2 := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r2.Host = "pan.example.com"
	r2.AddCookie(&http.Cookie{Name: gateCookieName, Value: g.sign(time.Now().Add(time.Hour).Unix())})
	if st := g.Status(r2); st.State != gateStateOK {
		t.Fatalf("state after upgrade (with ticket) = %q, want %q", st.State, gateStateOK)
	}

	// 触发一次落盘，确认写出来的文件里不再有 passwordHash
	if err := g.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(b); contains(got, "passwordHash") {
		t.Fatalf("passwordHash should be gone after save, got:\n%s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestGateClearMaster 清除主凭证后回到 need_login。
func TestGateClearMaster(t *testing.T) {
	g := newTestGate(t)
	if err := g.StoreMaster("S", 1, "h", "u", "U"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	if _, _, ok := g.MasterCredentials(); !ok {
		t.Fatal("master should exist")
	}
	if err := g.ClearMaster(); err != nil {
		t.Fatalf("ClearMaster: %v", err)
	}
	if _, _, ok := g.MasterCredentials(); ok {
		t.Fatal("master should be cleared")
	}
	if g.MasterHash() != "" || g.MasterName() != "" || g.MasterUserName() != "" {
		t.Fatal("all master fields should be cleared")
	}
	r := httptest.NewRequest("GET", "http://x/", nil)
	r.Host = "x"
	if st := g.Status(r); st.State != gateStateNeedLogin {
		t.Fatalf("state after clear = %q, want %q", st.State, gateStateNeedLogin)
	}
}

// TestGateDisable 闸门关闭时一律放行。
func TestGateDisable(t *testing.T) {
	dir := t.TempDir()
	g, err := newGateService(filepath.Join(dir, "g.json"), nil, time.Hour, true)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	r := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r.Host = "pan.example.com"
	if st := g.Status(r); st.State != gateStateOK {
		t.Fatalf("disabled gate state = %q, want %q", st.State, gateStateOK)
	}
}

// TestGateFilePermissions 数据文件权限不过宽（含 TG 凭证）。
func TestGateFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgpan-gate.json")
	g, err := newGateService(path, nil, time.Hour, false)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	if err := g.StoreMaster("S", 1, "h", "u", "U"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("gate file mode = %o, want 600", mode)
	}
}

// TestBuildMasterClaims 主凭证能正确转成 JWTClaims。
func TestBuildMasterClaims(t *testing.T) {
	c := buildMasterClaims("SESS", 99, "HASH99")
	if c == nil {
		t.Fatal("nil claims")
	}
	if c.Subject != "99" {
		t.Fatalf("subject = %q, want 99", c.Subject)
	}
	if c.TgSession != "SESS" {
		t.Fatalf("tgSession = %q", c.TgSession)
	}
	if c.Hash != "HASH99" {
		t.Fatalf("hash = %q", c.Hash)
	}
	if c.ExpiresAt == nil || c.ExpiresAt.Time.Before(time.Now()) {
		t.Fatal("claims should expire in the future")
	}
}

// TestSynthesizeAccessToken 主凭证能合成出可被校验的 JWT。
func TestSynthesizeAccessToken(t *testing.T) {
	g := newTestGate(t)
	const secret = "unit-test-secret-abcdefghijklmnop"

	if _, ok := g.SynthesizeAccessToken(secret); ok {
		t.Fatal("should not synthesize without master credentials")
	}

	if err := g.StoreMaster("MASTER-SESS", 1234, "master-hash", "carol", "Carol"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	tok, ok := g.SynthesizeAccessToken(secret)
	if !ok || tok == "" {
		t.Fatal("synthesize failed")
	}
	claims, err := authDecodeForTest(secret, tok)
	if err != nil {
		t.Fatalf("decode synthesized token: %v", err)
	}
	if claims.Subject != "1234" || claims.Hash != "master-hash" || claims.TgSession != "MASTER-SESS" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
}

// TestGateExemptPaths 健康检查/版本接口必须豁免闸门拦截。
func TestGateExemptPaths(t *testing.T) {
	exempt := []string{"/version", "/api/version", "/health", "/api/health"}
	for _, p := range exempt {
		if !gateExemptPath(p) {
			t.Errorf("gateExemptPath(%q) = false, want true", p)
		}
	}
	protected := []string{"/files", "/api/files", "/users/config", "/api/uploads"}
	for _, p := range protected {
		if gateExemptPath(p) {
			t.Errorf("gateExemptPath(%q) = true, want false", p)
		}
	}
}

// TestGatePairPaths 登录 TG 的接口必须豁免闸门 ——
// 否则未配对时前端连不上 /auth/ws，永远登不上去。
func TestGatePairPaths(t *testing.T) {
	yes := []string{"/auth/ws", "/api/auth/ws", "/auth/session", "/api/auth/login"}
	for _, p := range yes {
		if !gatePairPath(p) {
			t.Errorf("gatePairPath(%q) = false, want true", p)
		}
	}
	no := []string{"/files", "/api/users/config", "/gate/status", "/scan/dialogs"}
	for _, p := range no {
		if gatePairPath(p) {
			t.Errorf("gatePairPath(%q) = true, want false", p)
		}
	}
}

// TestGateExemptRequest WebDAV 请求必须整体豁免闸门。
func TestGateExemptRequest(t *testing.T) {
	mk := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, "http://example.com"+path, nil)
		r.Host = "pan.2016.de5.net"
		return r
	}

	yes := [][2]string{
		{"GET", "/version"},
		{"GET", "/api/version"},
		{"POST", "/api/auth/ws"},
		{"GET", "/api/auth/session"},
		{"OPTIONS", "/webdav"},
		{"OPTIONS", "/webdav/"},
		{"OPTIONS", "/api/webdav"},
		{"PROPFIND", "/webdav"},
		{"PROPFIND", "/webdav/"},
		{"GET", "/webdav"},
		{"GET", "/api/webdav/"},
		{"OPTIONS", "/api/webdav/"},
	}
	for _, c := range yes {
		if !gateExemptRequest(mk(c[0], c[1])) {
			t.Errorf("gateExemptRequest(%s %s) = false, want true", c[0], c[1])
		}
	}

	no := [][2]string{
		{"GET", "/files"},
		{"GET", "/api/users/config"},
		{"GET", "/api/uploads"},
	}
	for _, c := range no {
		if gateExemptRequest(mk(c[0], c[1])) {
			t.Errorf("gateExemptRequest(%s %s) = true, want false", c[0], c[1])
		}
	}
}

// TestGateWebDAVCredentialsNotExempt WebDAV 凭据管理接口不能豁免。
//
// 它长在 /webdav 前缀下，但走的是闸门身份校验（网页里点"生成账号"），
// 跟文件访问的 Basic 认证是两套。若被当成文件请求放行，
// 就拿不到合成凭证 → 一律 401。
func TestGateWebDAVCredentialsNotExempt(t *testing.T) {
	for _, p := range []string{"/webdav/credentials", "/webdav/credentials/", "/api/webdav/credentials"} {
		r := httptest.NewRequest("GET", "http://example.com"+p, nil)
		if gateExemptRequest(r) {
			t.Errorf("gateExemptRequest(%q) = true, want false", p)
		}
	}
}

// ---------------------------------------------------------------------------
//  一次性领取码（Claim）测试
//
//  这是 v2.8 新增的安全关键路径：TG 登录在 WebSocket 里完成，
//  那里拿不到 http.ResponseWriter、没法 Set-Cookie。所以用「一次性码」
//  过渡 —— 前端拿码打 POST /gate/claim 换浏览器门票。
//
//  这几条测试钉住它的安全属性：随机、短命、用过即焚、绑定 UA。
// ---------------------------------------------------------------------------

// TestClaimBasic 正常流程：签发 → 兑换成功 → 再兑一次失败（用过即焚）。
func TestClaimBasic(t *testing.T) {
	g := newTestGate(t)
	const ua = "Mozilla/5.0 (test-browser)"

	tok := g.NewClaim(ua)
	if tok == "" {
		t.Fatal("NewClaim returned empty token")
	}
	if len(tok) < 32 {
		t.Fatalf("claim token too short: %d chars（必须是高熵随机串）", len(tok))
	}

	// 第一次兑换成功
	if !g.ConsumeClaim(tok, ua) {
		t.Fatal("first claim should succeed")
	}
	// 第二次兑换必须失败 —— 用过即焚
	if g.ConsumeClaim(tok, ua) {
		t.Fatal("claim token must be single-use（重放必须失败）")
	}
}

// TestClaimUnknownToken 未签发的码不能兑换。
func TestClaimUnknownToken(t *testing.T) {
	g := newTestGate(t)
	if g.ConsumeClaim("", "ua") {
		t.Fatal("empty token must fail")
	}
	if g.ConsumeClaim("deadbeefdeadbeefdeadbeefdeadbeef", "ua") {
		t.Fatal("forged token must fail")
	}
	// 签发一个真的，然后用另一个值去兑
	_ = g.NewClaim("ua")
	if g.ConsumeClaim("00000000000000000000000000000000", "ua") {
		t.Fatal("non-issued token must fail")
	}
}

// TestClaimUserAgentBinding 码绑定签发时的浏览器，换 UA 兑换必须失败。
//
// 防的是：码在传输过程中被截走（比如日志、中间人），
// 攻击者在自己的浏览器里兑换。
func TestClaimUserAgentBinding(t *testing.T) {
	g := newTestGate(t)
	tok := g.NewClaim("Mozilla/5.0 (owner)")
	if g.ConsumeClaim(tok, "Mozilla/5.0 (attacker)") {
		t.Fatal("claim must not be redeemable from a different User-Agent")
	}
}

// TestClaimExpiry 过期的码不能兑换。
func TestClaimExpiry(t *testing.T) {
	g := newTestGate(t)
	tok := g.NewClaim("ua")

	// 手工把它改成已过期
	g.mu.Lock()
	for i := range g.data.Claims {
		if g.data.Claims[i].Token == tok {
			g.data.Claims[i].ExpiresAt = time.Now().Add(-time.Second)
		}
	}
	g.mu.Unlock()

	if g.ConsumeClaim(tok, "ua") {
		t.Fatal("expired claim must fail")
	}
}

// TestClaimMultipleIndependent 多个码互不干扰（多标签页 / 多设备同时登录）。
func TestClaimMultipleIndependent(t *testing.T) {
	g := newTestGate(t)
	a := g.NewClaim("ua-a")
	b := g.NewClaim("ua-b")
	if a == b {
		t.Fatal("两次签发的码不能相同")
	}
	if !g.ConsumeClaim(b, "ua-b") {
		t.Fatal("token b should be valid")
	}
	// 兑了 b 之后，a 依然可用
	if !g.ConsumeClaim(a, "ua-a") {
		t.Fatal("token a should still be valid after b was consumed")
	}
}

// TestClaimNotPersisted 领取码不落盘（进程重启即失效）。
//
// 理由：码是「本次登录会话」的临时凭据，没必要持久化；
// 不落盘也让数据文件保持干净（不含短命数据）。
func TestClaimNotPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgpan-gate.json")

	g1, err := newGateService(path, nil, time.Hour, false)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	// 先落一次盘，确保文件存在
	if err := g1.StoreMaster("S", 1, "h", "u", "U"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	tok := g1.NewClaim("ua")

	// 重新载入（模拟重启）
	g2, err := newGateService(path, nil, time.Hour, false)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if g2.ConsumeClaim(tok, "ua") {
		t.Fatal("claim token must not survive a restart（不该落盘）")
	}
}

// TestClaimThenTicketIntegration 端到端：登录 → 领码 → 换票 → 状态 ok。
//
// 这是用户实际会走的路径，必须整条通。
func TestClaimThenTicketIntegration(t *testing.T) {
	g := newTestGate(t)
	const ua = "Mozilla/5.0 (real-flow)"

	// 1) 还没登录 → need_login
	r0 := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r0.Host = "pan.example.com"
	if st := g.Status(r0); st.State != gateStateNeedLogin {
		t.Fatalf("step1: state = %q, want %q", st.State, gateStateNeedLogin)
	}

	// 2) 模拟 TG 登录成功：落盘凭证 + 签发领取码
	if err := g.StoreMaster("TG-SESSION", 7, "hash7", "owner", "Owner"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	claim := g.NewClaim(ua)

	// 3) 换票前：服务端有凭证但浏览器没票 → 还是 need_login
	if st := g.Status(r0); st.State != gateStateNeedLogin {
		t.Fatalf("step3: state = %q, want %q（没票不能进）", st.State, gateStateNeedLogin)
	}

	// 4) 兑换领取码 → 拿到门票
	if !g.ConsumeClaim(claim, ua) {
		t.Fatal("step4: claim should be redeemable")
	}
	ticket := g.sign(time.Now().Add(time.Hour).Unix())

	// 5) 带票访问 → ok
	r1 := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r1.Host = "pan.example.com"
	r1.AddCookie(&http.Cookie{Name: gateCookieName, Value: ticket})
	st := g.Status(r1)
	if st.State != gateStateOK {
		t.Fatalf("step5: state = %q, want %q", st.State, gateStateOK)
	}
	if !st.Paired || !st.Authed {
		t.Fatalf("step5: Paired=%v Authed=%v, both should be true", st.Paired, st.Authed)
	}
	if st.User != "owner" || st.Name != "Owner" {
		t.Fatalf("step5: user=%q name=%q", st.User, st.Name)
	}

	// 6) 另一个浏览器（无票）访问 → 仍被挡住
	r2 := httptest.NewRequest("GET", "http://pan.example.com/", nil)
	r2.Host = "pan.example.com"
	if st := g.Status(r2); st.State != gateStateNeedLogin {
		t.Fatalf("step6: 另一个浏览器 state = %q, want %q（网址泄露也进不来）",
			st.State, gateStateNeedLogin)
	}
}
