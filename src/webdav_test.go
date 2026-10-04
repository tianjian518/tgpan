package services

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ===========================================================================
//  WebDAV 协议能力声明的回归测试
//
//  守护的是：无论请求有没有通过认证，响应里都必须带 DAV 头。
//
//  为什么这条重要：
//    爆米花 / Infuse / nPlayer / WinSCP / Windows 资源管理器在挂载前，
//    会先发一个**不带凭据**的 OPTIONS 请求，靠响应里的 DAV 头判断
//    「对面到底是不是一个 WebDAV 服务器」。
//
//    曾经的做法是先认证、认证不过就直接 401 返回，DAV 头一个都不给。
//    结果是部分客户端看到 401 且没有 DAV 头，就认为这压根不是 WebDAV 服务，
//    直接判挂载失败，连账号密码框都不弹 —— 用户表现就是"挂不上，且没地方输密码"。
//
//    正确做法（RFC 7235 + 各家服务端实践）：
//      401 + WWW-Authenticate: Basic realm="..."  ← 标准质询，让客户端知道要鉴权
//      同时带上 DAV / Allow / Accept-Ranges      ← 声明协议能力
//    客户端拿到后会带着 Authorization 重试，这次就通过了。
//
//  安全性说明：
//    这里只是在未认证响应里多回了几个能力声明头，不涉及任何网盘数据。
//    未认证请求依旧一个字节的文件内容都拿不到（下面 TestWebDAVUnauthenticated
//    DoesNotLeakContent 一并钉住这一点）。
// ===========================================================================

// TestOptionsAdvertisesDAVBeforeAuth 验证未认证的 OPTIONS 也会带上 DAV 能力头。
//
// 复现的是播放器挂载前的探测请求：不带任何 Authorization。
func TestOptionsAdvertisesDAVBeforeAuth(t *testing.T) {
	// 只需要一个能跑到 ServeHTTP 开头即可。
	// handler 内部的 svc 为 nil，但因为不带凭据、认证必然在查库前就返回 401，
	// 不会走到解除 svc 引用的分支。
	h := &webdavHandler{}

	req := httptest.NewRequest(http.MethodOptions, "/webdav/", nil)
	rec := httptest.NewRecorder()

	// ServeHTTP 是入口，会先挂能力头再做认证
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("DAV"); got != davComplianceClasses {
		t.Errorf("未认证 OPTIONS 缺少 DAV 头（期望 %q，实际 %q）——\n"+
			"部分播放器会因此判定「不是 WebDAV 服务器」而挂载失败", davComplianceClasses, got)
	}

	if got := rec.Header().Get("Allow"); got == "" {
		t.Error("未认证 OPTIONS 缺少 Allow 头，客户端无法知道支持哪些方法")
	}

	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("未认证 OPTIONS 缺少 Accept-Ranges: bytes（实际 %q）——\n"+
			"播放器可能因此认为不支持拖进度条", got)
	}

	if got := rec.Header().Get("MS-Author-Via"); got != "DAV" {
		t.Errorf("未认证 OPTIONS 缺少 MS-Author-Via: DAV（实际 %q）——\n"+
			"Windows 资源管理器靠这个头才能挂载", got)
	}

	// 同时必须仍然是 401 + 标准质询头，否则就是把自己改成匿名可访问了
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未认证 OPTIONS 期望 401（标准质询），实际 %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("未认证 OPTIONS 缺少 WWW-Authenticate 头 —— 客户端不会知道要带 Basic 凭据重试")
	}
}

// TestWebDAVUnauthenticatedDoesNotLeakContent 验证未认证请求不会漏出任何文件内容。
//
// 这是上一条测试的安全边界：能力头可以给，内容一个字节都不能给。
func TestWebDAVUnauthenticatedDoesNotLeakContent(t *testing.T) {
	h := &webdavHandler{}

	// 各种会读数据的方法，全都不带凭据
	for _, method := range []string{"GET", "HEAD", "PROPFIND", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/webdav/some-file.mkv", nil)
			if method == "PROPFIND" {
				req.Header.Set("Depth", "1")
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			// OPTIONS 在认证前就返回 401（因为认证在能力头之后、分发之前），
			// 所以这四种方法未认证时都应该是 401
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("未认证 %s 期望 401，实际 %d —— 可能存在未授权访问", method, rec.Code)
			}

			// 响应体里不能出现任何像文件内容/元数据的东西
			body := rec.Body.String()
			if len(body) > 0 {
				// 允许的只有框架自带的纯文本错误提示
				allowed := map[string]bool{
					"authentication required\n": true,
					"invalid credentials\n":     true,
				}
				if !allowed[body] {
					t.Errorf("未认证 %s 的响应体出现了非预期内容，可能泄露信息：%q", method, body)
				}
			}
		})
	}
}

// TestDAVComplianceDeclaredAsOneAndTwo 把「声明支持 DAV 1,2 级」这个约定钉住。
//
// 说明：我们实际不实现 LOCK/UNLOCK（没有锁的需求），
// 声明 2 级是为了兼容更多播放器的探测逻辑 —— 有些客户端看到只有 1 级就直接放弃。
// 这条测试的价值是：万一以后有人把 davComplianceClasses 改了，
// 能立刻意识到这会影响到播放器的挂载判定。
func TestDAVComplianceDeclaredAsOneAndTwo(t *testing.T) {
	if davComplianceClasses != "1, 2" {
		t.Errorf("DAV 能力声明被改成了 %q，原为 \"1, 2\"；\n"+
			"改动前请确认不会影响播放器的挂载探测", davComplianceClasses)
	}
}
