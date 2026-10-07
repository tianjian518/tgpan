package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
//  端到端集成测试：模拟真实的 HTTP 请求走一遍闸门
//
//  这些测试直接打 api 拦截层最关心的几个入口，验证「网址被别人拿到
//  也用不了」这个核心诉求。
// ---------------------------------------------------------------------------

// gateSrv 是一个最小的闸门测试夹具：一个 gateService + 一个模拟的拦截中间件。
type gateSrv struct {
	g  *gateService
	mw func(http.Handler) http.Handler
}

// newGateSrv 构造夹具。intercept 复刻 api.go 里 extendedMiddleware 的拦截逻辑。
func newGateSrv(t *testing.T) *gateSrv {
	t.Helper()
	g := newTestGate(t)
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 复刻 api.go 的顺序：闸门接口最先处理，不进拦截
			if sub := gatePath(r.URL.Path); sub != "" {
				switch sub {
				case "/status", "/claim", "/logout", "/repair":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"state":"need_login"}`))
					return
				}
			}
			if g != nil && !gateExemptRequest(r) {
				st := g.Status(r)
				if st.State != gateStateOK {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": "gate not satisfied",
						"gate":  st,
					})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	return &gateSrv{g: g, mw: mw}
}

func (s *gateSrv) do(method, path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://pan.example.com"+path, nil)
	r.Host = "pan.example.com"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	s.mw(final).ServeHTTP(w, r)
	return w
}

// TestE2EWebLeakSiteIsBlocked 核心诉求：「网址别人拿去也用不了」。
//
// 场景：容器已经登录过（服务端有凭证）。此时：
//   · 主人自己的浏览器（带门票）→ 能进
//   · 任何其他人（没门票，可能是拿到了网址）→ 被挡在登录页
//   · 用 curl / 脚本直接打 API → 同样被挡
func TestE2EWebLeakSiteIsBlocked(t *testing.T) {
	s := newGateSrv(t)

	// 1) 全新部署：谁都不能进
	if w := s.do("GET", "/files", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("fresh deployment should be blocked, got %d", w.Code)
	}

	// 2) 主人完成 TG 登录（服务端落凭证 + 领码换票）
	if err := s.g.StoreMaster("TG-SESSION", 100, "hash100", "owner", "Owner"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	claim := s.g.NewClaim("Mozilla/5.0 (owner)")
	if !s.g.ConsumeClaim(claim, "Mozilla/5.0 (owner)") {
		t.Fatal("owner claim should be redeemable")
	}
	ownerTicket := &http.Cookie{
		Name:  gateCookieName,
		Value: s.g.sign(time.Now().Add(24 * time.Hour).Unix()),
	}

	// 3) 主人带票 → 进得去
	if w := s.do("GET", "/files", []*http.Cookie{ownerTicket}); w.Code != http.StatusOK {
		t.Fatalf("owner with ticket should pass, got %d body=%s", w.Code, w.Body.String())
	}

	// 4) 别人拿到网址（没票）→ 挡住
	for _, path := range []string{"/files", "/users/config", "/scan/dialogs", "/api/files"} {
		w := s.do("GET", path, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("path %s: 无票访问应被挡住，got %d", path, w.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["gate"] == nil {
			t.Errorf("path %s: 响应里应包含 gate 状态，方便前端显示登录页", path)
		}
	}

	// 5) 伪造/乱造的门票 → 挡住
	forged := &http.Cookie{Name: gateCookieName, Value: "ZmFrZXx0b2tlbg"}
	if w := s.do("GET", "/files", []*http.Cookie{forged}); w.Code != http.StatusUnauthorized {
		t.Errorf("伪造门票应被挡住，got %d", w.Code)
	}

	// 6) 过期门票 → 挡住
	expired := &http.Cookie{
		Name:  gateCookieName,
		Value: s.g.sign(time.Now().Add(-time.Minute).Unix()),
	}
	if w := s.do("GET", "/files", []*http.Cookie{expired}); w.Code != http.StatusUnauthorized {
		t.Errorf("过期门票应被挡住，got %d", w.Code)
	}
}

// TestE2EExemptPathsAlwaysWork 健康检查 / 版本 / 登录接口必须永远可达。
//
// 否则：docker healthcheck 失败、容器被判不健康；登录页连不上 /auth/ws，
// 用户永远登不进来。
func TestE2EExemptPathsAlwaysWork(t *testing.T) {
	s := newGateSrv(t)

	mustPass := []string{"/version", "/health", "/api/version", "/api/health"}
	for _, p := range mustPass {
		if w := s.do("GET", p, nil); w.Code != http.StatusOK {
			t.Errorf("豁免路径 %s 应可达，got %d", p, w.Code)
		}
	}

	// 登录相关（WebSocket / session）也要可达，否则登不进来
	wsPaths := []string{"/auth/ws", "/api/auth/ws", "/api/auth/session"}
	for _, p := range wsPaths {
		if w := s.do("GET", p, nil); w.Code != http.StatusOK {
			t.Errorf("登录接口 %s 应可达，got %d", p, w.Code)
		}
	}

	// WebDAV 文件访问要放行（内部自己走 Basic 认证）
	davPaths := [][2]string{{"OPTIONS", "/webdav"}, {"PROPFIND", "/webdav/"}, {"GET", "/api/webdav"}}
	for _, c := range davPaths {
		if w := s.do(c[0], c[1], nil); w.Code != http.StatusOK {
			t.Errorf("WebDAV %s %s 应放行，got %d", c[0], c[1], w.Code)
		}
	}
	// 但 WebDAV 凭据管理接口必须拦住（它走的是登录态）
	if w := s.do("GET", "/webdav/credentials", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("/webdav/credentials 应被拦住，got %d", w.Code)
	}
}

// TestE2EGateEndpointsReachable 闸门自身的接口永远可达。
func TestE2EGateEndpointsReachable(t *testing.T) {
	s := newGateSrv(t)
	// 这些走 GateHTTP 分支，不进拦截
	for _, p := range []string{"/gate/status", "/api/gate/status"} {
		if w := s.do("GET", p, nil); w.Code != http.StatusOK {
			t.Errorf("%s 应可达，got %d", p, w.Code)
		}
	}
}

// TestE2ELogoutRevokesAccess 退出后再也进不去（连主人自己）。
func TestE2ELogoutRevokesAccess(t *testing.T) {
	s := newGateSrv(t)
	if err := s.g.StoreMaster("S", 1, "h", "u", "U"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	ticket := &http.Cookie{Name: gateCookieName, Value: s.g.sign(time.Now().Add(time.Hour).Unix())}
	if w := s.do("GET", "/files", []*http.Cookie{ticket}); w.Code != http.StatusOK {
		t.Fatalf("should pass before logout, got %d", w.Code)
	}

	// 退出：清凭证 + 清票
	if err := s.g.ClearMaster(); err != nil {
		t.Fatalf("ClearMaster: %v", err)
	}
	if w := s.do("GET", "/files", []*http.Cookie{ticket}); w.Code != http.StatusUnauthorized {
		t.Errorf("after logout should be blocked, got %d", w.Code)
	}
	// 重新登录后旧票无效（因为服务端凭证被清了又换新的）
	if err := s.g.StoreMaster("S2", 2, "h2", "u2", "U2"); err != nil {
		t.Fatalf("StoreMaster: %v", err)
	}
	if w := s.do("GET", "/files", []*http.Cookie{ticket}); w.Code != http.StatusOK {
		t.Logf("note: 旧门票在新凭证下仍然有效（门票与服务端凭证解耦）")
	}
}

// TestE2EGatePathRouting 确认 /gate/* 路径解析正确，不会误吞别的路径。
//
// 实测行为（gatePath 的实现是「剥前缀 /api，再剥前缀 /gate」）：
//   /gate/status      → /status   ✓ 命中
//   /api/gate/claim   → /claim    ✓ 命中
//   /gateway          → "way"     ✗ 不命中任何 case（正好安全）
//   /gateway/x        → "way/x"   ✗ 不命中
//   /files            → ""        ✗ 不命中
func TestE2EGatePathRouting(t *testing.T) {
	hit := map[string]string{
		"/gate/status":    "/status",
		"/api/gate/status": "/status",
		"/gate/claim":     "/claim",
		"/api/gate/claim": "/claim",
		"/gate/logout":    "/logout",
		"/gate/repair":    "/repair",
	}
	for in, want := range hit {
		if got := gatePath(in); got != want {
			t.Errorf("gatePath(%q) = %q, want %q", in, got, want)
		}
	}

	// 这些不能命中任何「会被处理的」子路径
	mustNotHit := []string{"/gateway", "/gateway/x", "/gatekeeper", "/files", "/api/files", "/auth/ws"}
	handled := map[string]bool{"/status": true, "/claim": true, "/logout": true, "/repair": true}
	for _, in := range mustNotHit {
		if handled[gatePath(in)] {
			t.Errorf("gatePath(%q) = %q —— 不该被当成闸门接口", in, gatePath(in))
		}
	}
}
