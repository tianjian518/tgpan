package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/tgdrive/teldrive/internal/tgc"
	"github.com/tgdrive/teldrive/internal/utils"
	"golang.org/x/net/proxy"
)

// ---------------------------------------------------------------------------
//  诊断页面：不依赖原版前端，直接检测服务器到 Telegram 的连通性
//
//  背景：Teldrive 原版登录界面在连不上 TG 时完全没有提示，
//        用户只能看到 "Please Wait..." 无限转圈，无从判断问题。
//        本接口提供一个纯 HTML 页面，直接把检测结果摊开给用户看。
// ---------------------------------------------------------------------------

type diagCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Elapsed string `json:"elapsed"`
}

// FilesDiagHTTP 处理 /diag 请求，返回一个自包含的诊断页面
func (e *extendedService) FilesDiagHTTP(w http.ResponseWriter, r *http.Request) {
	// ?cookie=1：原样回显服务端看到的 Cookie 头与解析结果。
	// 排查「前端明明带了 Cookie 却 401」这类问题的第一步 —— 先确认
	// 服务端到底收没收到，别在后端代码里瞎猜。
	if r.URL.Query().Get("cookie") != "" {
		hdr := r.Header.Get("Cookie")
		ck, err := r.Cookie(authCookieName)
		val, e2 := "", ""
		if err == nil {
			val = ck.Value
			if len(val) > 60 {
				val = val[:60] + "..."
			}
		} else {
			e2 = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"rawCookieHeader": hdr,
			"found":           err == nil,
			"valuePreview":    val,
			"parseError":      e2,
			"allHeaders":      r.Header,
			"urlPath":         r.URL.Path,
			"requestURI":      r.RequestURI,
			"method":          r.Method,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	checks := e.runDiagnostics(r.Context())

	// 统计
	passed := 0
	for _, c := range checks {
		if c.OK {
			passed++
		}
	}
	allOK := passed == len(checks)

	var rows string
	for _, c := range checks {
		icon := "❌"
		color := "#d32f2f"
		if c.OK {
			icon = "✅"
			color = "#2e7d32"
		}
		rows += fmt.Sprintf(`
    <div class="item">
      <div class="head"><span class="icon">%s</span><span class="name">%s</span><span class="time">%s</span></div>
      <div class="detail" style="color:%s">%s</div>
    </div>`, icon, esc(c.Name), esc(c.Elapsed), color, esc(c.Detail))
	}

	banner := `<div class="banner fail">检测未全部通过 —— 请把本页截图发给开发者</div>`
	if allOK {
		banner = `<div class="banner ok">全部通过 —— 服务正常，可以去登录了</div>`
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>TGPan 诊断</title>
<style>
  *{box-sizing:border-box}
  body{margin:0;padding:16px;font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif;background:#f5f6f8;color:#1f2329}
  .wrap{max-width:640px;margin:0 auto}
  h1{font-size:20px;margin:8px 0 4px}
  .sub{color:#8a9099;font-size:13px;margin-bottom:16px}
  .banner{padding:12px 14px;border-radius:10px;font-weight:600;margin-bottom:16px}
  .banner.ok{background:#e8f5e9;color:#2e7d32;border:1px solid #a5d6a7}
  .banner.fail{background:#ffebee;color:#c62828;border:1px solid #ef9a9a}
  .card{background:#fff;border-radius:12px;padding:6px 14px;box-shadow:0 1px 3px rgba(0,0,0,.06);margin-bottom:16px}
  .item{padding:12px 0;border-bottom:1px solid #eef0f3}
  .item:last-child{border-bottom:none}
  .head{display:flex;align-items:center;gap:8px}
  .icon{font-size:16px}
  .name{flex:1;font-weight:600}
  .time{color:#9aa0a6;font-size:12px;font-variant-numeric:tabular-nums}
  .detail{font-size:13px;margin-top:4px;padding-left:24px;word-break:break-all}
  .foot{color:#9aa0a6;font-size:12px;text-align:center;padding:8px 0 24px}
  .btn{display:block;text-align:center;background:#2f7cf6;color:#fff;text-decoration:none;padding:12px;border-radius:10px;font-weight:600}
</style></head><body><div class="wrap">
<h1>TGPan 连接诊断</h1>
<div class="sub">检测服务器能否连上 Telegram。登录问题多半出在这一步。</div>
%s
<div class="card">%s</div>
<a class="btn" href="/">返回登录</a>
<div class="foot">检测时间 %s</div>
</div></body></html>`, banner, rows, time.Now().Format("2006-01-02 15:04:05"))

	_, _ = w.Write([]byte(html))
}

func esc(s string) string {
	r := make([]rune, 0, len(s))
	for _, c := range s {
		switch c {
		case '<':
			r = append(r, []rune("&lt;")...)
		case '>':
			r = append(r, []rune("&gt;")...)
		case '&':
			r = append(r, []rune("&amp;")...)
		default:
			r = append(r, c)
		}
	}
	return string(r)
}

// runDiagnostics 依次执行各项检测
func (e *extendedService) runDiagnostics(ctx context.Context) []diagCheck {
	var out []diagCheck

	// ---- 1. 配置检查 ----
	{
		t0 := time.Now()
		cfg := e.api.cnf.TG
		ok := cfg.AppId != 0 && cfg.AppHash != ""
		detail := fmt.Sprintf("app-id=%d，device=%s", cfg.AppId, cfg.DeviceModel)
		if !ok {
			detail = "app-id 或 app-hash 未配置（会导致 TG 拒绝连接）"
		}
		out = append(out, diagCheck{"TG 应用凭据", ok, detail, ms(t0)})
	}

	// ---- 2. DNS 解析 ----
	{
		t0 := time.Now()
		ips, err := net.LookupHost("api.telegram.org")
		if err != nil {
			out = append(out, diagCheck{"DNS 解析 api.telegram.org", false,
				"解析失败：" + err.Error() + "（DNS 被劫持或网络不通）", ms(t0)})
		} else {
			out = append(out, diagCheck{"DNS 解析 api.telegram.org", true,
				fmt.Sprintf("解析到 %v", ips), ms(t0)})
		}
	}

	// ---- 2.5 对照组：普通 HTTPS 网站能否访问 ----
	// 作用：把「整台机器没网」和「只有 TG 连不上」区分开。
	// 如果这一项 ✅ 而 TG 相关项 ❌，说明网络本身是好的，是 TG 这条路被单独挡了。
	{
		t0 := time.Now()
		cli := &http.Client{Timeout: 8 * time.Second}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://core.telegram.org/", nil)
		resp, err := cli.Do(req)
		if err != nil {
			out = append(out, diagCheck{"对照：访问 core.telegram.org 网页", false,
				"失败：" + short(err.Error()) + "（说明服务器整体网络不通，不只是 TG 协议问题）", ms(t0)})
		} else {
			_ = resp.Body.Close()
			out = append(out, diagCheck{"对照：访问 core.telegram.org 网页", true,
				fmt.Sprintf("HTTP %d（普通网页能打开，说明网络本身正常）", resp.StatusCode), ms(t0)})
		}
	}

	// ---- 3. TCP 直连 Telegram 数据中心 ----
	//
	// 【2026-10 修复】配了代理时，这一项改用「经代理拨号」并明确标注。
	//
	// 先前无论是否配置代理，这里都走 net.DialTimeout 裸连 TG 的 443 端口。
	// 在国内/被墙环境下裸连必然全 ❌，页面于是长期显示
	// 「全部不通说明网络层面被阻断」，把用户的注意力引向「网络坏了」，
	// 而实际上流量是走代理的、完全正常。这是纯粹的误导。
	//
	// 现在：能拿到代理配置就经代理拨，并在文案里说明走的是代理。
	dcsToTry := []struct{ name, addr string }{
		{"DC1 (149.154.175.50)", "149.154.175.50:443"},
		{"DC2 (149.154.167.51)", "149.154.167.51:443"},
		{"DC4 (149.154.167.91)", "149.154.167.91:443"},
		{"DC5 (91.108.56.130)", "91.108.56.130:443"},
	}
	anyDC := false
	var dcDetail string
	viaProxy := strings.TrimSpace(e.api.cnf.TG.Proxy) != ""
	{
		t0 := time.Now()
		var dialer dcs.DialFunc = proxy.Direct.DialContext
		if viaProxy {
			if d, err := utils.Proxy.GetDial(e.api.cnf.TG.Proxy); err == nil {
				dialer = d.DialContext
			}
		}
		for _, dc := range dcsToTry {
			cctx, ccancel := context.WithTimeout(ctx, 12*time.Second)
			conn, err := dialer(cctx, "tcp", dc.addr)
			if err == nil {
				_ = conn.Close()
				anyDC = true
				dcDetail += "✅ " + dc.name + "  "
			} else {
				dcDetail += "❌ " + dc.name + "  "
			}
			ccancel()
		}
		how := "直连"
		if viaProxy {
			how = "经代理 " + e.api.cnf.TG.Proxy
		}
		out = append(out, diagCheck{"TCP 连接 TG 数据中心（" + how + "）", anyDC,
			dcDetail + "（全部不通说明这条链路有问题）", ms(t0)})
	}

	// ---- 4. 真实 MTProto 握手（最关键的检测）----
	{
		t0 := time.Now()
		ok, detail := e.testMTProto(ctx)
		out = append(out, diagCheck{"MTProto 实际握手", ok, detail, ms(t0)})
	}

	return out
}

// testMTProto 用真实客户端做一次连接 + 调用，最能反映实际可用性
func (e *extendedService) testMTProto(ctx context.Context) (bool, string) {
	// 【2026-10 修复】超时从 20s 放宽到 90s。
	// 通过代理连 TG 实测要 10-19 秒，20s 会踩在临界点上，
	// 表现为「有时通过、有时超时」的抖动，误导排查。
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	client, err := tgc.NoAuthClient(cctx, &e.api.cnf.TG, nil, nil)
	if err != nil {
		return false, "创建客户端失败：" + err.Error()
	}

	var callErr error

	runErr := client.Run(cctx, func(ctx context.Context) error {
		// 调用一个无需登录的接口，验证通道确实可用
		_, callErr = client.API().HelpGetConfig(ctx)
		return nil
	})

	if runErr != nil {
		return false, "连接失败：" + short(runErr.Error()) +
			"（这是登录卡住/验证码收不到的直接原因）"
	}
	if callErr != nil {
		// 只有「通道确实建立、只是没登录」造成的错误才算通过。
		// gotd 在真正握手成功但未授权时返回的错误包含这些关键字。
		msg := callErr.Error()
		if strings.Contains(msg, "AUTH_KEY") ||
			strings.Contains(msg, "not authorized") ||
			strings.Contains(msg, "AUTH_KEY_UNREGISTERED") ||
			strings.Contains(msg, "not logged in") {
			return true, "通道已建立（接口返回未授权，属正常现象）"
		}
		return false, "调用失败：" + short(msg)
	}
	return true, "连接正常，TG 接口调用成功"
}

func short(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

func ms(t time.Time) string {
	return fmt.Sprintf("%dms", time.Since(t).Milliseconds())
}

// FilesDiagJSON 返回 JSON 格式，方便脚本调用
func (e *extendedService) FilesDiagJSON(w http.ResponseWriter, r *http.Request) {
	checks := e.runDiagnostics(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks})
}

var _ = dcs.Plain
var _ = telegram.NewClient
