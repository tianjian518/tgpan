package services

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	tgauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/tgdrive/teldrive/internal/tgc"
)

// ---------------------------------------------------------------------------
// 登录自检页面
//
//  背景：用户在手机上点登录，前端只会显示「Please Wait...」，
//        收不到验证码时完全无法判断卡在哪一步，容器里也没有终端可查日志。
//
//  本页面的做法：直接在服务端跑一次真实的「请求验证码」调用，
//        把每一步的结果（耗时、TG 返回值、错误原文）摊在网页上。
//
//  注意：本页面会向 Telegram 真实发送一次验证码请求，
//        因此带 60 秒节流，避免被 TG 限流。
// ---------------------------------------------------------------------------

type loginStep struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Elapsed string `json:"elapsed"`
}

var (
	loginProbeMu   sync.Mutex
	loginProbeLast time.Time
)

// FilesLoginCheckHTTP 处理 /logincheck：跑一次真实的发码流程
func (e *extendedService) FilesLoginCheckHTTP(w http.ResponseWriter, r *http.Request) {
	phone := strings.TrimSpace(r.URL.Query().Get("phone"))
	if phone == "" {
		phone = "+8613800138000"
	}

	loginProbeMu.Lock()
	since := time.Since(loginProbeLast)
	if since < 60*time.Second && !loginProbeLast.IsZero() {
		loginProbeMu.Unlock()
		writeLoginCheckPage(w, phone, nil, fmt.Sprintf(
			"距上次检测仅 %.0f 秒，为避免 TG 限流，请等 %d 秒后再刷新。",
			since.Seconds(), int((60*time.Second-since).Seconds())+1))
		return
	}
	loginProbeLast = time.Now()
	loginProbeMu.Unlock()

	steps := e.runLoginProbe(r.Context(), phone)
	writeLoginCheckPage(w, phone, steps, "")
}

// runLoginProbe 按顺序执行发码流程的每一步
func (e *extendedService) runLoginProbe(ctx context.Context, phone string) []loginStep {
	var out []loginStep

	// ---- 第 1 步：创建 TG 客户端 ----
	t0 := time.Now()
	client, err := tgc.NoAuthClient(ctx, &e.api.cnf.TG, nil, nil)
	if err != nil {
		out = append(out, loginStep{"创建 TG 客户端", false,
			"失败：" + short(err.Error()), ms(t0)})
		return out
	}
	out = append(out, loginStep{"创建 TG 客户端", true,
		fmt.Sprintf("成功（app-id=%d）", e.api.cnf.TG.AppId), ms(t0)})

	// ---- 第 2 / 3 步：连接 TG，然后真实请求验证码 ----
	//
	// 【2026-10 修复】超时从 40s 放宽到 150s。
	//
	// 这个自检页要在一次请求里跑完「连接 + 可能的 DC 迁移 + 发码」，
	// 而通过代理连 TG 本身就要花十几到二十秒（实测首次握手 18.6s，
	// DC 迁移更久）。原来的 40s 会在中途把 ctx 掐断，
	// 页面于是显示 "migrate to dc: context deadline exceeded" ——
	// 看起来像功能坏了，其实只是自检页自己设的闹钟太短。
	//
	// 真实登录走的是 WebSocket 长连接（没有这个限制），
	// 所以自检页的超时必须比真实路径更宽松，否则会误报。
	cctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()

	var sendErr error
	var codeHash string
	var codeType string
	connected := false

	tRun := time.Now()
	runErr := client.Run(cctx, func(ctx context.Context) error {
		connected = true
		out = append(out, loginStep{"连接 Telegram 服务器", true,
			"MTProto 握手成功，已进入会话", ms(tRun)})

		t1 := time.Now()
		res, err := client.Auth().SendCode(ctx, phone, tgauth.SendCodeOptions{})
		sendErr = err
		if err != nil {
			out = append(out, loginStep{"请求发送验证码", false,
				"TG 返回错误：" + short(err.Error()), ms(t1)})
			return nil
		}
		if sent, ok := res.(*tg.AuthSentCode); ok {
			codeHash = sent.PhoneCodeHash
			codeType = fmt.Sprintf("%T", sent.Type)
			out = append(out, loginStep{"请求发送验证码", true,
				"TG 已受理，验证码应已发出（type=" + codeType + "）", ms(t1)})
		} else {
			out = append(out, loginStep{"请求发送验证码", true,
				fmt.Sprintf("TG 已受理（返回类型 %T）", res), ms(t1)})
		}
		return nil
	})

	if !connected {
		msg := "未能进入 TG 会话"
		if runErr != nil {
			msg = short(runErr.Error())
		}
		out = append(out, loginStep{"连接 Telegram 服务器", false,
			"失败：" + msg, ms(tRun)})
		out = append(out, loginStep{"请求发送验证码", false,
			"未执行（上一步未通过）", "-"})
		return out
	}

	// ---- 第 4 步：会话结果 ----
	if runErr != nil && sendErr == nil {
		out = append(out, loginStep{"会话结束", false, short(runErr.Error()), "-"})
	}
	if sendErr == nil && codeHash != "" {
		out = append(out, loginStep{"取得 phoneCodeHash", true,
			"正常（长度 " + fmt.Sprint(len(codeHash)) + "）—— 前端可用于提交验证码", "-"})
	}

	return out
}

func writeLoginCheckPage(w http.ResponseWriter, phone string, steps []loginStep, notice string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	allOK := len(steps) > 0
	var rows string
	for _, s := range steps {
		icon, color := "❌", "#d32f2f"
		if s.OK {
			icon, color = "✅", "#2e7d32"
		} else {
			allOK = false
		}
		rows += fmt.Sprintf(`
    <div class="item">
      <div class="head"><span>%s</span><span class="name">%s</span><span class="time">%s</span></div>
      <div class="detail" style="color:%s">%s</div>
    </div>`, icon, esc(s.Step), esc(s.Elapsed), color, esc(s.Detail))
	}

	banner := `<div class="banner fail">登录链路存在 ❌ —— 请看下面哪一步卡住</div>`
	if allOK {
		banner = `<div class="banner ok">全部通过 —— 服务器能正常向 TG 发出验证码</div>`
	}
	if notice != "" {
		banner = `<div class="banner warn">` + esc(notice) + `</div>`
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>TGPan 登录自检</title>
<style>
  *{box-sizing:border-box}
  body{margin:0;padding:16px;font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif;background:#f5f6f8;color:#1f2329}
  .wrap{max-width:640px;margin:0 auto}
  h1{font-size:20px;margin:8px 0 4px}
  .sub{color:#8a9099;font-size:13px;margin-bottom:16px}
  .banner{padding:12px 14px;border-radius:10px;font-weight:600;margin-bottom:16px}
  .banner.ok{background:#e8f5e9;color:#2e7d32;border:1px solid #a5d6a7}
  .banner.fail{background:#ffebee;color:#c62828;border:1px solid #ef9a9a}
  .banner.warn{background:#fff8e1;color:#ef6c00;border:1px solid #ffcc80}
  .card{background:#fff;border-radius:12px;padding:6px 14px;box-shadow:0 1px 3px rgba(0,0,0,.06);margin-bottom:16px}
  .item{padding:12px 0;border-bottom:1px solid #eef0f3}
  .item:last-child{border-bottom:none}
  .head{display:flex;align-items:center;gap:8px}
  .name{flex:1;font-weight:600}
  .time{color:#9aa0a6;font-size:12px}
  .detail{font-size:13px;margin-top:4px;padding-left:24px;word-break:break-all}
  .foot{color:#9aa0a6;font-size:12px;text-align:center;padding:8px 0 24px}
  .btn{display:block;text-align:center;background:#2f7cf6;color:#fff;text-decoration:none;padding:12px;border-radius:10px;font-weight:600;margin-bottom:10px}
  .note{background:#fff;border-radius:10px;padding:12px 14px;font-size:13px;color:#555;margin-bottom:16px;line-height:1.7}
  code{background:#f0f2f5;padding:1px 5px;border-radius:4px;font-size:12px}
</style></head><body><div class="wrap">
<h1>登录链路自检</h1>
<div class="sub">在服务器上真实跑一次「请求验证码」，看卡在哪一步。手机号：%s</div>
%s
<div class="note">
本页会向 Telegram 真实发送一次验证码请求，因此有 <b>60 秒节流</b>。<br>
想用真实手机号检测，在网址后加参数：<br>
<code>/logincheck?phone=+86138xxxxxxxx</code>
</div>
<div class="card">%s</div>
<a class="btn" href="/diag">← 连接诊断</a>
<a class="btn" href="/" style="background:#8a9099">返回登录</a>
<div class="foot">检测时间 %s</div>
</div></body></html>`, esc(phone), banner, rows, time.Now().Format("2006-01-02 15:04:05"))

	_, _ = w.Write([]byte(html))
}
