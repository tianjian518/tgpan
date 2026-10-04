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

// authDecodeForTest 是 auth.Decode 的测试包装，避免测试文件里反复写包名。
func authDecodeForTest(secret, tok string) (*types.JWTClaims, error) {
	return auth.Decode(secret, tok)
}

// newTestGate 建一个临时目录里的闸门实例。
func newTestGate(t *testing.T, requireHosts []string) *gateService {
	t.Helper()
	dir := t.TempDir()
	g, err := newGateService(
		filepath.Join(dir, "tgpan-gate.json"),
		requireHosts,
		30*24*time.Hour,
		false,
	)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	return g
}

// TestGateStateMachine 覆盖三种状态的流转：
// init → need_pair → need_login（对外域名）
func TestGateStateMachine(t *testing.T) {
	g := newTestGate(t, []string{"pan.2016.de5.net"})

	// 1) 全新：没密码没凭证 → init
	r := httptest.NewRequest("GET", "http://pan.2016.de5.net/", nil)
	r.Host = "pan.2016.de5.net"
	if st := g.Status(r); st.State != gateStateInit {
		t.Fatalf("fresh state = %q, want %q", st.State, gateStateInit)
	}

	// 2) 设了密码、没凭证 → need_pair
	if err := g.SetPassword("", "test1234"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if st := g.Status(r); st.State != gateStateNeedPair {
		t.Fatalf("after password state = %q, want %q", st.State, gateStateNeedPair)
	}

	// 3) 配对 TG 后，对外域名 → need_login
	if err := g.StoreMaster("FAKESESSION123", 42, "hash42", "alice", "Alice"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	if st := g.Status(r); st.State != gateStateNeedLogin {
		t.Fatalf("domain unauthenticated state = %q, want %q", st.State, gateStateNeedLogin)
	}

	// 4) 内网 Host → 直接放行（免密）
	rLAN := httptest.NewRequest("GET", "http://10.0.0.119:30141/", nil)
	rLAN.Host = "10.0.0.119:30141"
	if st := g.Status(rLAN); st.State != gateStateOK || !st.Bypass {
		t.Fatalf("LAN state = %q bypass=%v, want ok/true", st.State, st.Bypass)
	}
}

// TestGatePasswordVerify 校验密码正确/错误、以及改密需要旧密码。
func TestGatePasswordVerify(t *testing.T) {
	g := newTestGate(t, nil)

	if g.CheckPassword("whatever") {
		t.Fatal("password check on empty gate should fail")
	}
	if err := g.SetPassword("", "abcd"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if !g.CheckPassword("abcd") {
		t.Fatal("correct password rejected")
	}
	if g.CheckPassword("abce") {
		t.Fatal("wrong password accepted")
	}

	// 旧密码错误 → 拒绝改密
	if err := g.SetPassword("wrong", "newpass"); err == nil {
		t.Fatal("change password with wrong old password should fail")
	}
	if !g.CheckPassword("abcd") {
		t.Fatal("password should be unchanged after failed change")
	}

	// 旧密码正确 → 改密成功
	if err := g.SetPassword("abcd", "newpass"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if !g.CheckPassword("newpass") {
		t.Fatal("new password not effective")
	}
}

// TestGateTooShortPassword 密码长度下限。
func TestGateTooShortPassword(t *testing.T) {
	g := newTestGate(t, nil)
	if err := g.SetPassword("", "123"); err == nil {
		t.Fatal("3-char password should be rejected")
	}
}

// TestGateHostMatching 覆盖 Host 判定的边界：端口、大小写、通配。
func TestGateHostMatching(t *testing.T) {
	g := newTestGate(t, []string{"pan.2016.de5.net", "*.example.com", "UPPER.CASE"})

	cases := []struct {
		host string
		want bool
	}{
		{"pan.2016.de5.net", true},
		{"PAN.2016.DE5.NET", true},
		{"pan.2016.de5.net:443", true},
		{"10.0.0.119:30141", false},
		{"localhost:8080", false},
		{"a.example.com", true},
		{"deep.a.example.com", true},
		{"upper.case", true},
		{"notexample.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := g.hostNeedsLogin(c.host); got != c.want {
			t.Errorf("hostNeedsLogin(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestGateEmptyRequireHosts 反向白名单为空 → 所有 Host 都免密。
func TestGateEmptyRequireHosts(t *testing.T) {
	g := newTestGate(t, nil)
	if g.hostNeedsLogin("pan.2016.de5.net") {
		t.Fatal("with empty require-hosts, nothing should require login")
	}
	if g.hostNeedsLogin("anything.example.com") {
		t.Fatal("with empty require-hosts, nothing should require login")
	}
}

// TestGateTokenRoundTrip 闸门通行证签发 / 校验 / 过期。
func TestGateTokenRoundTrip(t *testing.T) {
	g := newTestGate(t, []string{"pan.2016.de5.net"})

	// 未签发的空 token
	if g.verifyToken("") {
		t.Fatal("empty token should not verify")
	}
	if g.verifyToken("garbage!!") {
		t.Fatal("garbage token should not verify")
	}

	// 正常签发
	tok := g.sign(time.Now().Add(time.Hour).Unix())
	if !g.verifyToken(tok) {
		t.Fatal("valid token rejected")
	}

	// 过期
	old := g.sign(time.Now().Add(-time.Hour).Unix())
	if g.verifyToken(old) {
		t.Fatal("expired token accepted")
	}

	// 篡改签名
	if g.verifyToken(tok + "x") {
		t.Fatal("tampered token accepted")
	}
}

// TestGatePersistence 落盘后重新载入，密码与主凭证都还在。
func TestGatePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgpan-gate.json")

	g1, err := newGateService(path, []string{"pan.2016.de5.net"}, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	if err := g1.SetPassword("", "persist123"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if err := g1.StoreMaster("SESSION-XYZ", 777, "hash777", "bob", "Bob"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}

	// 重新载入（模拟容器重启）
	g2, err := newGateService(path, []string{"pan.2016.de5.net"}, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !g2.CheckPassword("persist123") {
		t.Fatal("password lost after reload")
	}
	sess, uid, ok := g2.MasterCredentials()
	if !ok || sess != "SESSION-XYZ" || uid != 777 {
		t.Fatalf("master creds lost after reload: ok=%v sess=%q uid=%d", ok, sess, uid)
	}
	if g2.MasterHash() != "hash777" {
		t.Fatalf("master hash lost: %q", g2.MasterHash())
	}
}

// TestGateClearMaster 清除主凭证后回到 need_pair。
func TestGateClearMaster(t *testing.T) {
	g := newTestGate(t, nil)
	if err := g.SetPassword("", "abcd"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
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
	r := httptest.NewRequest("GET", "http://x/", nil)
	r.Host = "x"
	if st := g.Status(r); st.State != gateStateNeedPair {
		t.Fatalf("state after clear = %q, want %q", st.State, gateStateNeedPair)
	}
}

// TestGateDisable 闸门关闭时一律放行。
func TestGateDisable(t *testing.T) {
	dir := t.TempDir()
	g, err := newGateService(filepath.Join(dir, "g.json"), []string{"pan.2016.de5.net"}, time.Hour, true)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	r := httptest.NewRequest("GET", "http://pan.2016.de5.net/", nil)
	r.Host = "pan.2016.de5.net"
	st := g.Status(r)
	if st.State != gateStateOK || !st.Bypass {
		t.Fatalf("disabled gate state = %q bypass=%v, want ok/true", st.State, st.Bypass)
	}
	// 关闭时即使没密码也不该卡在 init
	if st.HasPassword {
		t.Fatal("unexpected password on fresh disabled gate")
	}
}

// TestGateFilePermissions 数据文件权限不过宽（含密码哈希与 TG 凭证）。
func TestGateFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgpan-gate.json")
	g, err := newGateService(path, nil, time.Hour, false)
	if err != nil {
		t.Fatalf("newGateService: %v", err)
	}
	if err := g.SetPassword("", "secret99"); err != nil {
		t.Fatalf("SetPassword: %v", err)
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
	g := newTestGate(t, nil)
	const secret = "unit-test-secret-abcdefghijklmnop"

	// 没配对时不能合成
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

	// 合成的 token 必须能被正常校验（说明它是一张"合法通行证"）
	claims, err := authDecodeForTest(secret, tok)
	if err != nil {
		t.Fatalf("decode synthesized token: %v", err)
	}
	if claims.Subject != "1234" {
		t.Fatalf("subject = %q, want 1234", claims.Subject)
	}
	if claims.Hash != "master-hash" {
		t.Fatalf("hash = %q", claims.Hash)
	}
	if claims.TgSession != "MASTER-SESS" {
		t.Fatalf("tgSession = %q", claims.TgSession)
	}
}

// TestGateExemptPaths 健康检查/版本接口必须豁免闸门拦截，
// 否则 docker healthcheck 会失败、前端连版本都读不到。
func TestGateExemptPaths(t *testing.T) {
	exempt := []string{"/version", "/api/version", "/health", "/api/health"}
	for _, p := range exempt {
		if !gateExemptPath(p) {
			t.Errorf("gateExemptPath(%q) = false, want true", p)
		}
	}
	// 这些必须照常拦截
	protected := []string{"/files", "/api/files", "/users/config", "/api/uploads"}
	for _, p := range protected {
		if gateExemptPath(p) {
			t.Errorf("gateExemptPath(%q) = true, want false", p)
		}
	}
}

// TestGateExemptRequest WebDAV 的 OPTIONS 探测必须豁免（否则爆米花挂不上）。
func TestGateExemptRequest(t *testing.T) {
	mk := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, "http://example.com"+path, nil)
		r.Host = "pan.2016.de5.net"
		return r
	}

	// 必须豁免
	yes := [][2]string{
		{"GET", "/version"},
		{"GET", "/api/version"},
		{"OPTIONS", "/webdav"},
		{"OPTIONS", "/webdav/"},
		{"OPTIONS", "/api/webdav"},
	}
	for _, c := range yes {
		if !gateExemptRequest(mk(c[0], c[1])) {
			t.Errorf("gateExemptRequest(%s %s) = false, want true", c[0], c[1])
		}
	}

	// 必须拦截（非 OPTIONS 的 WebDAV 请求照常走认证）
	no := [][2]string{
		{"GET", "/files"},
		{"GET", "/api/users/config"},
		{"PROPFIND", "/webdav"},
		{"GET", "/webdav"},
	}
	for _, c := range no {
		if gateExemptRequest(mk(c[0], c[1])) {
			t.Errorf("gateExemptRequest(%s %s) = true, want false", c[0], c[1])
		}
	}
}
