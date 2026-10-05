/*!
 * TGPan 闸门（Gate）前端
 * ---------------------------------------------------------------------------
 *  v2.6.2 新增。
 *
 *  目标：让「进入 TGPan」不再依赖 TG 扫码 / 验证码。
 *
 *  三个状态对应三块界面：
 *    1. init        首次部署 → 让用户设置管理密码
 *    2. need_pair   已有密码、未配对 TG → 引导扫码一次
 *    3. need_login  对外域名访问 → 输入管理密码
 *
 *  免密入口（内网 / 飞牛 OS）后端直接放行，前端什么都不用做。
 *
 *  v2.7.0 改动：配对这一步不再是「拦路虎」。
 *  以前 need_pair 会盖住整个界面，不配对就什么都干不了（连扫描按钮都被藏了）。
 *  现在 need_pair 只作为一个「可跳过的提示条」显示，点一下就进主界面。
 *  用户要的是「只留管理密码」—— TG 配对交给频道扫描时再按需完成。
 *
 *  实现方式：旁挂式。不改压缩后的 SPA 产物，而是先盖一层遮罩，
 *  由后端 /gate/status 决定显示哪块。状态满足后才把遮罩撤掉。
 * ---------------------------------------------------------------------------
 */
(function () {
  'use strict';

  var API = '/api';
  var OVERLAY_ID = 'tgpan-gate-overlay';
  var state = { status: null, busy: false, pollTimer: null };

  // ---------------------------------------------------------------------
  //  样式
  // ---------------------------------------------------------------------
  function injectStyle() {
    if (document.getElementById('tgpan-gate-style')) return;
    var css = ''
      + '#' + OVERLAY_ID + '{position:fixed;inset:0;z-index:2147483000;display:flex;'
      + 'align-items:center;justify-content:center;padding:20px;'
      + 'background:linear-gradient(150deg,#0f172a 0%,#1e293b 55%,#0b1220 100%);'
      + 'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;'
      + 'color:#e2e8f0;-webkit-font-smoothing:antialiased;}'
      + '.tgpan-gate-card{width:100%;max-width:400px;background:rgba(30,41,59,.86);'
      + 'border:1px solid rgba(148,163,184,.22);border-radius:18px;padding:30px 28px 26px;'
      + 'box-shadow:0 24px 60px rgba(0,0,0,.5);backdrop-filter:blur(14px);}'
      + '.tgpan-gate-logo{width:52px;height:52px;border-radius:14px;margin:0 auto 16px;'
      + 'display:flex;align-items:center;justify-content:center;font-size:26px;'
      + 'background:linear-gradient(135deg,#2563eb,#7c3aed);box-shadow:0 8px 22px rgba(37,99,235,.4);}'
      + '.tgpan-gate-title{font-size:19px;font-weight:600;text-align:center;margin:0 0 6px;color:#f1f5f9;}'
      + '.tgpan-gate-sub{font-size:13px;color:#94a3b8;text-align:center;line-height:1.65;margin:0 0 20px;}'
      + '.tgpan-gate-field{margin-bottom:13px;}'
      + '.tgpan-gate-field label{display:block;font-size:12.5px;color:#94a3b8;margin-bottom:6px;}'
      + '.tgpan-gate-field input{width:100%;box-sizing:border-box;padding:11px 13px;font-size:14.5px;'
      + 'color:#f1f5f9;background:rgba(15,23,42,.85);border:1px solid rgba(148,163,184,.3);'
      + 'border-radius:10px;outline:none;transition:border-color .18s,box-shadow .18s;}'
      + '.tgpan-gate-field input:focus{border-color:#3b82f6;box-shadow:0 0 0 3px rgba(59,130,246,.18);}'
      + '.tgpan-gate-btn{width:100%;padding:12px;font-size:15px;font-weight:600;color:#fff;cursor:pointer;'
      + 'border:none;border-radius:10px;background:linear-gradient(135deg,#2563eb,#4f46e5);'
      + 'transition:transform .12s,box-shadow .18s;margin-top:6px;}'
      + '.tgpan-gate-btn:hover:not(:disabled){transform:translateY(-1px);box-shadow:0 10px 24px rgba(37,99,235,.36);}'
      + '.tgpan-gate-btn:disabled{opacity:.55;cursor:not-allowed;}'
      + '.tgpan-gate-btn.ghost{background:transparent;border:1px solid rgba(148,163,184,.35);'
      + 'color:#cbd5e1;font-weight:500;margin-top:10px;}'
      + '.tgpan-gate-msg{font-size:13px;text-align:center;margin-top:12px;min-height:19px;color:#f87171;}'
      + '.tgpan-gate-msg.ok{color:#4ade80;}'
      + '.tgpan-gate-tip{margin-top:16px;padding:11px 13px;font-size:12.5px;line-height:1.7;'
      + 'color:#94a3b8;background:rgba(15,23,42,.6);border-left:3px solid #3b82f6;border-radius:0 8px 8px 0;}'
      + '.tgpan-gate-steps{margin:16px 0 0;padding:0;list-style:none;font-size:13px;color:#cbd5e1;}'
      + '.tgpan-gate-steps li{padding:8px 0 8px 26px;position:relative;line-height:1.6;'
      + 'border-bottom:1px solid rgba(148,163,184,.13);}'
      + '.tgpan-gate-steps li:last-child{border-bottom:none;}'
      + '.tgpan-gate-steps li:before{content:"✓";position:absolute;left:4px;color:#4ade80;font-weight:700;}'
      + '.tgpan-gate-code{display:inline-block;padding:1px 6px;margin:0 2px;font-size:12.5px;'
      + 'background:rgba(59,130,246,.16);color:#93c5fd;border-radius:5px;font-family:ui-monospace,Menlo,monospace;}';
    var el = document.createElement('style');
    el.id = 'tgpan-gate-style';
    el.textContent = css;
    document.head.appendChild(el);
  }

  // ---------------------------------------------------------------------
  //  请求辅助
  // ---------------------------------------------------------------------
  function req(path, opts) {
    opts = opts || {};
    return fetch(API + path, {
      method: opts.method || 'GET',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: opts.body ? JSON.stringify(opts.body) : undefined
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) {
          var e = new Error(j.error || ('HTTP ' + r.status));
          e.status = r.status;
          throw e;
        }
        return j;
      });
    });
  }

  // ---------------------------------------------------------------------
  //  遮罩
  // ---------------------------------------------------------------------
  function overlay() {
    var el = document.getElementById(OVERLAY_ID);
    if (!el) {
      el = document.createElement('div');
      el.id = OVERLAY_ID;
      // 挂到 body 之前先看 body 有没有；没有就等 DOM
      (document.body || document.documentElement).appendChild(el);
    }
    return el;
  }

  function removeOverlay() {
    var el = document.getElementById(OVERLAY_ID);
    if (el && el.parentNode) el.parentNode.removeChild(el);
  }

  function render(html) {
    overlay().innerHTML = '<div class="tgpan-gate-card">' + html + '</div>';
  }

  // escHtml 转义要拼进 innerHTML 的动态文本。
  //
  // 这里的 msg 大多来自服务端返回的 message 字段。虽然目前后端消息是自己
  // 写的，但「把外部字符串直接拼进 innerHTML」本身就是个洞 —— 哪天后端
  // 把用户输入（比如密码错误提示里带上输入值）原样回显，就变成注入点了。
  // 所以统一收口：凡是拼进 HTML 的动态文本，一律先过这个函数。
  function escHtml(s) {
    return String(s === null || s === undefined ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  // ---------------------------------------------------------------------
  //  各状态界面
  // ---------------------------------------------------------------------

  // 首次部署：设置初始密码
  function renderSetup(msg, isError) {
    render(''
      + '<div class="tgpan-gate-logo"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="10" width="16" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/><circle cx="12" cy="15.5" r="1.2" fill="#fff" stroke="none"/></svg></div>'
      + '<h1 class="tgpan-gate-title">欢迎使用 TGPan</h1>'
      + '<p class="tgpan-gate-sub">第一次使用，请先给 TGPan 设一个管理密码。<br>'
      + '以后不管在手机还是电脑上打开，输这个密码就能进，<b>不用再扫码</b>。</p>'
      + '<div class="tgpan-gate-field"><label>设置密码</label>'
      + '<input type="password" id="tgpan-gate-p1" placeholder="至少 4 位" autocomplete="new-password"></div>'
      + '<div class="tgpan-gate-field"><label>再输一次确认</label>'
      + '<input type="password" id="tgpan-gate-p2" placeholder="再输一次" autocomplete="new-password"></div>'
      + '<button class="tgpan-gate-btn" id="tgpan-gate-go">确定</button>'
      + '<div class="tgpan-gate-msg' + (isError ? '' : ' ok') + '" id="tgpan-gate-msg">' + escHtml(msg || '') + '</div>'
      + '<div class="tgpan-gate-tip"><b>提示</b> · 这个密码存在服务器上，容器重启也不会丢。'
      + '忘了的话，删掉数据目录里的 <span class="tgpan-gate-code">tgpan-gate.json</span> 就能重来。</div>'
    );
    var go = function () {
      if (state.busy) return;
      var a = (document.getElementById('tgpan-gate-p1') || {}).value || '';
      var b = (document.getElementById('tgpan-gate-p2') || {}).value || '';
      if (a.length < 4) { setMsg('密码至少 4 位', false); return; }
      if (a !== b) { setMsg('两次输入不一致', false); return; }
      state.busy = true;
      req('/gate/setup', { method: 'POST', body: { password: a } })
        .then(function (st) { state.busy = false; applyStatus(st, '管理密码已设置'); })
        .catch(function (e) { state.busy = false; setMsg(e.message, false); });
    };
    var btn = document.getElementById('tgpan-gate-go');
    if (btn) btn.onclick = go;
    var p2 = document.getElementById('tgpan-gate-p2');
    if (p2) p2.addEventListener('keydown', function (e) { if (e.key === 'Enter') go(); });
    var p1 = document.getElementById('tgpan-gate-p1');
    if (p1) p1.focus();
  }

  // 已有密码、未配对 TG：**不拦路**，只在角落挂一条可关闭的提示。
  //
  // v2.7.0 以前这里会盖满整屏，导致「不配对 → 什么都进不去」。
  // 现在改成非阻塞提示：管理密码已经能证明身份，TG 配对是后续扫描时才需要。
  function renderNeedPair(msg) {
    // 先把可能存在的整屏遮罩撤掉
    removeOverlay();

    var bar = document.getElementById('tgpan-pair-bar');
    if (bar && bar.parentNode) bar.parentNode.removeChild(bar);

    bar = document.createElement('div');
    bar.id = 'tgpan-pair-bar';
    bar.style.cssText = 'position:fixed;left:50%;transform:translateX(-50%);bottom:18px;'
      + 'z-index:2147482000;max-width:92vw;display:flex;align-items:center;gap:12px;'
      + 'padding:11px 14px;border-radius:10px;background:#1e293b;color:#e2e8f0;'
      + 'box-shadow:0 10px 30px rgba(0,0,0,.32);font-size:13.5px;'
      + 'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif;';
    bar.innerHTML = '<span>TGPan 还没和 Telegram 配对。去「系统设置 → 扫描频道」扫描时如果提示未登录，再来配一次即可。</span>'
      + '<button id="tgpan-pair-go" style="flex:none;padding:6px 14px;border:none;border-radius:7px;'
      + 'background:#2563eb;color:#fff;font-size:13px;cursor:pointer;">去配对</button>'
      + '<button id="tgpan-pair-x" style="flex:none;background:none;border:none;color:#94a3b8;'
      + 'font-size:17px;cursor:pointer;line-height:1;">×</button>';
    (document.body || document.documentElement).appendChild(bar);

    var go = document.getElementById('tgpan-pair-go');
    if (go) go.onclick = function () { window.open('/api/auth/ws', '_blank'); };
    var x = document.getElementById('tgpan-pair-x');
    if (x) x.onclick = function () { if (bar.parentNode) bar.parentNode.removeChild(bar); };

    // 关键：仍然标记为 ok，让主界面的导航/按钮全部可用
    try { document.documentElement.setAttribute('data-tgpan-gate', 'ok'); } catch (e) {}
  }

  // 对外域名：输入管理密码
  function renderLogin(msg) {
    render(''
      + '<div class="tgpan-gate-logo"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6l7-3z"/><path d="M9 12l2 2 4-4"/></svg></div>'
      + '<h1 class="tgpan-gate-title">TGPan</h1>'
      + '<p class="tgpan-gate-sub">你正在通过外网域名访问，需要输入管理密码。</p>'
      + '<div class="tgpan-gate-field"><label>管理密码</label>'
      + '<input type="password" id="tgpan-gate-pwd" placeholder="请输入管理密码" autocomplete="current-password"></div>'
      + '<button class="tgpan-gate-btn" id="tgpan-gate-go">进入</button>'
      + '<div class="tgpan-gate-msg" id="tgpan-gate-msg">' + escHtml(msg || '') + '</div>'
      + '<div class="tgpan-gate-tip"><b>提示</b> · 在飞牛 OS 里打开 TGPan 可以免密码直接进。'
      + '这个密码在外网域名上才需要输。</div>'
    );
    var go = function () {
      if (state.busy) return;
      var v = (document.getElementById('tgpan-gate-pwd') || {}).value || '';
      if (!v) { setMsg('请输入密码', false); return; }
      state.busy = true;
      req('/gate/login', { method: 'POST', body: { password: v } })
        .then(function (st) { state.busy = false; applyStatus(st, ''); })
        .catch(function (e) { state.busy = false; setMsg(e.message, false); });
    };
    var btn = document.getElementById('tgpan-gate-go');
    if (btn) btn.onclick = go;
    var inp = document.getElementById('tgpan-gate-pwd');
    if (inp) { inp.focus(); inp.addEventListener('keydown', function (e) { if (e.key === 'Enter') go(); }); }
  }

  function setMsg(text, ok) {
    var el = document.getElementById('tgpan-gate-msg');
    if (!el) return;
    el.textContent = text;
    el.className = 'tgpan-gate-msg' + (ok ? ' ok' : '');
  }

  // ---------------------------------------------------------------------
  //  状态应用
  // ---------------------------------------------------------------------
  function applyStatus(st, okMsg) {
    state.status = st;
    if (!st) return;
    if (st.state === 'ok') {
      removeOverlay();
      // 通知其他 TGPan 脚本：已登录，可以显示控制台
      try {
        // 除了发事件，还在 window 上留一个标记。
        //
        // 原因：万一闸门这一次 fetch 命中了缓存、快得离谱，事件可能在
        // app.js 注册监听器**之前**就 dispatch 了 —— 那一发就白发了，
        // 主界面会一直等在「等闸门放行」的空状态里。留个标记，后加载的
        // 脚本可以先查一眼，不用赌事件有没有错过。
        window.__tgpanGateOk = true;
        window.dispatchEvent(new CustomEvent('tgpan:gate-ok', { detail: st }));
        document.documentElement.setAttribute('data-tgpan-gate', 'ok');
      } catch (e) {}
      return;
    }
    try { document.documentElement.setAttribute('data-tgpan-gate', st.state); } catch (e) {}
    if (st.state === 'init') renderSetup(okMsg || '', false);
    else if (st.state === 'need_pair') renderNeedPair(okMsg || '');
    else if (st.state === 'need_login') renderLogin(okMsg || '');
  }

  function refresh(okMsg) {
    req('/gate/status')
      .then(function (st) { applyStatus(st, okMsg); })
      .catch(function () { /* 状态接口不可用 → 不拦，交给原登录流程 */ removeOverlay(); });
  }

  // ---------------------------------------------------------------------
  //  启动
  // ---------------------------------------------------------------------
  function boot() {
    injectStyle();
    refresh();
    // 轮询：配对成功后自动撤除遮罩（用户此时在原登录页扫码）
    if (!state.pollTimer) {
      state.pollTimer = setInterval(function () {
        if (!state.status || state.status.state === 'ok') return;
        req('/gate/status').then(function (st) {
          if (st && st.state === 'ok') { applyStatus(st, ''); }
          else if (st && state.status && st.state !== state.status.state) { applyStatus(st, ''); }
        }).catch(function () {});
      }, 3000);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }

  window.TGPanGate = { refresh: refresh, applyStatus: applyStatus };
})();
