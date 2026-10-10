/*!
 * TGPan 登录引导（Gate）前端
 * ---------------------------------------------------------------------------
 *  v2.8.0 重写。
 *
 *  只有一件事：把「登录 TG」这一步做到能用。
 *
 *    没配对 → 直接展示登录页（扫码 / 手机验证码 双通道）
 *    已配对 → 撤掉遮罩，进主界面
 *
 *  没有管理密码，没有内外网判断，没有域名白名单 —— 全砍了。
 *  登录 == 登 TG，一步到位。
 *
 *  服务端返回的 state 只有两个：need_login / ok
 *
 *  ---------------------------------------------------------------------------
 *  【走过的弯路，写在这里免得有人再改回去】
 *
 *  弯路一：管理密码 + 内外网免密 + 域名白名单。
 *    想法是"内网方便、外网安全"，实际结果：
 *      · 配置里硬编码了作者自己的域名，别人部署时匹配不上
 *        → 所有人免密直进，设密码形同虚设；
 *      · 内网判断依赖 Host，而反代会改写 Host，Docker 网桥 / VPN
 *        场景下"内网"边界很模糊，判断根本不可靠；
 *      · 用户设完密码直接进主界面，TG 没配对 → 功能全 401
 *        → 界面上还找不到配对入口，彻底卡死。
 *    根因：把「登录」和「配对」拆成了两件事。其实它们就是一件事。
 *
 *  弯路二：自写 QR 编码器。
 *    用 jsQR 交叉验证，20 个用例全挂。逐层排查修了格式信息位序、
 *    纠错等级编码值、RS 生成多项式，还是不对。最后换成成熟库
 *    （qrcode-generator，MIT，被无数项目验证过），20/20 通过。
 *    结论：编码器这种东西不要自己写。
 *
 *  实现方式：旁挂式。不改压缩后的 SPA 产物，先盖一层遮罩，
 *  由 /gate/status 决定显示哪块，状态满足后把遮罩撤掉。
 * ---------------------------------------------------------------------------
 */
(function () {
  'use strict';

  var API = '/api';
  var OVERLAY_ID = 'tgpan-gate-overlay';
  var WS_URL = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + API + '/auth/ws';

  var state = {
    status: null,
    busy: false,
    pollTimer: null,
    ws: null,
    // 登录方式：'qr' | 'phone'
    mode: 'qr',
    // 验证码流程的阶段：'idle' | 'code' | '2fa'
    phoneStage: 'idle',
    phoneNo: '',
    phoneCodeHash: '',
    qrTimer: null,
    started: false
  };

  // ---------------------------------------------------------------------
  //  样式
  // ---------------------------------------------------------------------
  function injectStyle() {
    if (document.getElementById('tgpan-gate-style')) return;
    var css = ''
      + '#' + OVERLAY_ID + '{position:fixed;inset:0;z-index:2147483000;display:flex;'
      + 'align-items:center;justify-content:center;padding:20px;overflow:auto;'
      + 'background:linear-gradient(150deg,#0f172a 0%,#1e293b 55%,#0b1220 100%);'
      + 'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;'
      + 'color:#e2e8f0;-webkit-font-smoothing:antialiased;}'
      + '.tgpan-gate-card{width:100%;max-width:420px;background:rgba(30,41,59,.9);'
      + 'border:1px solid rgba(148,163,184,.22);border-radius:18px;padding:28px 26px 24px;'
      + 'box-shadow:0 24px 60px rgba(0,0,0,.5);backdrop-filter:blur(14px);}'
      + '.tgpan-gate-logo{width:52px;height:52px;border-radius:14px;margin:0 auto 14px;'
      + 'display:flex;align-items:center;justify-content:center;font-size:26px;'
      + 'background:linear-gradient(135deg,#2563eb,#7c3aed);box-shadow:0 8px 22px rgba(37,99,235,.4);}'
      + '.tgpan-gate-title{font-size:19px;font-weight:600;text-align:center;margin:0 0 6px;color:#f1f5f9;}'
      + '.tgpan-gate-sub{font-size:13px;color:#94a3b8;text-align:center;line-height:1.65;margin:0 0 18px;}'
      + '.tgpan-gate-tabs{display:flex;gap:6px;background:rgba(15,23,42,.7);padding:4px;'
      + 'border-radius:10px;margin-bottom:18px;}'
      + '.tgpan-gate-tab{flex:1;padding:8px;text-align:center;font-size:13.5px;cursor:pointer;'
      + 'border-radius:7px;color:#94a3b8;transition:background .18s,color .18s;user-select:none;}'
      + '.tgpan-gate-tab.on{background:#2563eb;color:#fff;font-weight:600;}'
      + '.tgpan-gate-qrbox{display:flex;flex-direction:column;align-items:center;gap:12px;}'
      + '.tgpan-gate-qr{width:228px;height:228px;background:#fff;border-radius:12px;padding:10px;'
      + 'box-sizing:content-box;box-shadow:0 6px 20px rgba(0,0,0,.35);}'
      + '.tgpan-gate-qr canvas{display:block;width:228px;height:228px;}'
      + '.tgpan-gate-qrhint{font-size:12.5px;color:#94a3b8;text-align:center;line-height:1.7;}'
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
      + '.tgpan-gate-msg.info{color:#93c5fd;}'
      + '.tgpan-gate-status{display:flex;align-items:center;justify-content:center;gap:7px;'
      + 'font-size:12.5px;color:#94a3b8;margin-top:10px;min-height:18px;}'
      + '.tgpan-gate-dot{width:7px;height:7px;border-radius:50%;background:#f59e0b;flex:none;'
      + 'animation:tgpan-pulse 1.4s ease-in-out infinite;}'
      + '.tgpan-gate-dot.ok{background:#4ade80;animation:none;}'
      + '.tgpan-gate-dot.err{background:#f87171;animation:none;}'
      + '@keyframes tgpan-pulse{0%,100%{opacity:.35}50%{opacity:1}}'
      + '.tgpan-gate-tip{margin-top:15px;padding:11px 13px;font-size:12.5px;line-height:1.7;'
      + 'color:#94a3b8;background:rgba(15,23,42,.6);border-left:3px solid #3b82f6;border-radius:0 8px 8px 0;}'
      + '.tgpan-gate-tip.warn{border-left-color:#f59e0b;}'
      + '.tgpan-gate-code{display:inline-block;padding:1px 6px;margin:0 2px;font-size:12.5px;'
      + 'background:rgba(59,130,246,.16);color:#93c5fd;border-radius:5px;font-family:ui-monospace,Menlo,monospace;}'
      // 主界面角落的「换账号」浮标
      + '#tgpan-account-chip{position:fixed;right:14px;bottom:14px;z-index:2147482000;'
      + 'display:flex;align-items:center;gap:8px;padding:7px 12px;border-radius:20px;'
      + 'background:rgba(30,41,59,.92);border:1px solid rgba(148,163,184,.25);color:#cbd5e1;'
      + 'font-size:12.5px;cursor:pointer;box-shadow:0 6px 18px rgba(0,0,0,.3);'
      + 'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif;'
      + 'opacity:.72;transition:opacity .18s;}'
      + '#tgpan-account-chip:hover{opacity:1;}';
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
          e.body = j;
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
  // 把用户输入（比如 TG 返回的错误里带上手机号）原样回显，就变成注入点了。
  // 所以统一收口：凡是拼进 HTML 的动态文本，一律先过这个函数。
  function escHtml(s) {
    return String(s === null || s === undefined ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  // ---------------------------------------------------------------------
  //  登录页（need_login）
  // ---------------------------------------------------------------------

  // renderLogin 画出完整的登录界面：扫码 / 验证码 两个 tab + 状态区。
  function renderLogin(msg, isError) {
    render(''
      + '<div class="tgpan-gate-logo">'
      + '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">'
      + '<path d="M21.5 2.5 2.5 10.2l7.6 2.7 2.7 7.6 8.7-18z"/><path d="M10.1 12.9l4.4-4.4"/></svg>'
      + '</div>'
      + '<h1 class="tgpan-gate-title">登录 TGPan</h1>'
      + '<p class="tgpan-gate-sub">用你的 Telegram 账号登录，只需一次。<br>登录后凭证保存在服务器上，重启也不会掉。</p>'
      + '<div class="tgpan-gate-tabs">'
      + '<div class="tgpan-gate-tab' + (state.mode === 'qr' ? ' on' : '') + '" id="tgpan-tab-qr">扫码登录</div>'
      + '<div class="tgpan-gate-tab' + (state.mode === 'phone' ? ' on' : '') + '" id="tgpan-tab-phone">手机号登录</div>'
      + '</div>'
      + '<div id="tgpan-gate-panel"></div>'
      + '<div class="tgpan-gate-status" id="tgpan-gate-status">'
      + '<span class="tgpan-gate-dot" id="tgpan-gate-dot"></span>'
      + '<span id="tgpan-gate-statustext">正在连接 Telegram…</span></div>'
      + '<div class="tgpan-gate-msg' + (isError ? '' : ' info') + '" id="tgpan-gate-msg">' + escHtml(msg || '') + '</div>'
    );

    var tabQr = document.getElementById('tgpan-tab-qr');
    var tabPhone = document.getElementById('tgpan-tab-phone');
    if (tabQr) tabQr.onclick = function () { switchMode('qr'); };
    if (tabPhone) tabPhone.onclick = function () { switchMode('phone'); };

    renderPanel();
    connectWS();
  }

  // renderPanel 根据当前模式画下半部分。
  function renderPanel() {
    var box = document.getElementById('tgpan-gate-panel');
    if (!box) return;

    if (state.mode === 'qr') {
      box.innerHTML = ''
        + '<div class="tgpan-gate-qrbox">'
        + '<div class="tgpan-gate-qr"><canvas id="tgpan-gate-qrcanvas" width="228" height="228"></canvas></div>'
        + '<div class="tgpan-gate-qrhint">用手机 Telegram 扫这个二维码<br>'
        + '<b>设置 → 设备 → 添加设备 → 扫描二维码</b><br>'
        + '扫完手机上点「确认」，这里会自动进</div>'
        + '</div>';
      drawQR(state.lastToken || '');
      return;
    }

    if (state.mode === 'phone') {
      var codeStage = state.phoneStage === 'code';
      var tfaStage = state.phoneStage === '2fa';
      box.innerHTML = ''
        + (tfaStage
          ? '<div class="tgpan-gate-field"><label>两步验证密码</label>'
            + '<input type="password" id="tgpan-gate-2fa" placeholder="你的两步验证密码" autocomplete="current-password"></div>'
            + '<button class="tgpan-gate-btn" id="tgpan-gate-go2fa">提交</button>'
          : '<div class="tgpan-gate-field"><label>手机号（含国家码）</label>'
            + '<input type="tel" id="tgpan-gate-phone" placeholder="+8613800138000" autocomplete="tel"'
            + (codeStage ? ' disabled' : '') + ' value="' + escHtml(state.phoneNo) + '"></div>'
            + (codeStage
              ? '<div class="tgpan-gate-field"><label>验证码</label>'
                + '<input type="text" inputmode="numeric" id="tgpan-gate-code" placeholder="Telegram 发来的 5 位数字" autocomplete="one-time-code"></div>'
                + '<button class="tgpan-gate-btn" id="tgpan-gate-go">登录</button>'
                + '<button class="tgpan-gate-btn ghost" id="tgpan-gate-back">换个手机号</button>'
              : '<button class="tgpan-gate-btn" id="tgpan-gate-go">发送验证码</button>')
        )
        + '<div class="tgpan-gate-tip"><b>收不到验证码？</b> · 验证码会发到你 Telegram App 里（不是短信）。'
        + '如果你手机没装 TG，请改用左边的「扫码登录」。</div>';

      wirePhoneHandlers();
      return;
    }
  }

  function wirePhoneHandlers() {
    var back = document.getElementById('tgpan-gate-back');
    if (back) back.onclick = function () {
      state.phoneStage = 'idle';
      state.phoneCodeHash = '';
      renderPanel();
      setStatus('info', '可以换一个手机号');
    };

    var go2fa = document.getElementById('tgpan-gate-go2fa');
    if (go2fa) {
      var do2fa = function () {
        var v = (document.getElementById('tgpan-gate-2fa') || {}).value || '';
        if (!v) { setMsg('请输入两步验证密码', false); return; }
        sendWS({ authType: '2fa', password: v });
        setStatus('wait', '正在验证…');
        setMsg('', true);
      };
      go2fa.onclick = do2fa;
      var i2 = document.getElementById('tgpan-gate-2fa');
      if (i2) { i2.focus(); i2.addEventListener('keydown', function (e) { if (e.key === 'Enter') do2fa(); }); }
      return;
    }

    var go = document.getElementById('tgpan-gate-go');
    if (!go) return;

    if (state.phoneStage === 'code') {
      // 【2026-10 修正】提交验证码期间「锁死按钮」，防连点。
      //
      // 为什么必须锁：TG 的登录验证码是**一次性的**。日志里能看到
      //   07:28:23 / 07:28:26 / 07:28:28 三次 signin 全报 PHONE_CODE_EXPIRED ——
      // 第一次就把码用掉了，后面几次必然过期。用户看到的现象就是
      // 「闪过一行字然后出错」，其实是自己连点把码点废了。
      //
      // 另外：每点一次，前端也会把 WebSocket 重建一遍，连带后端重新跟 TG
      // 握手（11~13 秒），越点越慢、越点越不可能成功。所以这里锁住，
      // 一次只允许一个在途请求，直到服务端给答复才解锁。
      var submitting = false;
      var doLogin = function () {
        if (submitting) return;              // 在途 → 直接吞掉重复点击
        var code = (document.getElementById('tgpan-gate-code') || {}).value || '';
        if (!code) { setMsg('请输入验证码', false); return; }
        submitting = true;
        if (go) { go.disabled = true; go.style.opacity = '0.6'; go.style.cursor = 'not-allowed'; }
        sendWS({
          authType: 'phone',
          message: 'signin',
          phoneCode: code.trim(),
          // 【2026-10-10 修复】必须带上 phoneNo！
          //
          // 服务端 handlePhoneAuth 的 signin 分支是：
          //     tgClient.Auth().SignIn(ctx, message.PhoneNo, message.PhoneCode, message.PhoneCodeHash)
          // 三项缺一不可。此前这里漏传 phoneNo，服务端拿到空手机号，
          // TG 校验哈希时直接回 PHONE_CODE_EXPIRED —— 用户看到的就是
          // 「验证码收到了却提示已过期」，且与网络/节点无关，百分百复现。
          phoneNo: state.phoneNo,
          phoneCodeHash: state.phoneCodeHash
        });
        setStatus('wait', '正在验证验证码，请稍候（约 10~15 秒）…');
        setMsg('已提交，请耐心等待，不要重复点击', true);
      };
      go.onclick = doLogin;
      // 把「解锁」句柄挂到 state 上，handleWS 收到答复时调用
      state.unlockLogin = function () {
        submitting = false;
        var g = document.getElementById('tgpan-gate-go');
        if (g) { g.disabled = false; g.style.opacity = ''; g.style.cursor = ''; }
      };
      var ic = document.getElementById('tgpan-gate-code');
      if (ic) { ic.focus(); ic.addEventListener('keydown', function (e) { if (e.key === 'Enter') doLogin(); }); }
      return;
    }

    var doSend = function () {
      var phone = ((document.getElementById('tgpan-gate-phone') || {}).value || '').trim();
      if (!phone) { setMsg('请输入手机号', false); return; }
      if (phone.charAt(0) !== '+') { setMsg('手机号要以 + 开头，比如 +8613800138000', false); return; }
      state.phoneNo = phone;
      state._lastPhoneNo = phone;   // 断线自动续接时重新发码用
      sendWS({ authType: 'phone', message: 'sendcode', phoneNo: phone });
      setStatus('wait', '正在请求验证码…');
      setMsg('', true);
    };
    go.onclick = doSend;
    var ip = document.getElementById('tgpan-gate-phone');
    if (ip) { ip.focus(); ip.addEventListener('keydown', function (e) { if (e.key === 'Enter') doSend(); }); }
  }

  // drawQR 用内置的成熟库（qrcode-generator）画二维码。
  //
  // 【为什么用库而不是自己写】见文件头的「弯路二」。
  // 自写版本用 jsQR 交叉验证 20/20 全挂；换成这个库后 20/20 通过。
  function drawQR(text) {
    var canvas = document.getElementById('tgpan-gate-qrcanvas');
    if (!canvas) return;
    var ctx = canvas.getContext('2d');
    var size = canvas.width;

    if (!text) {
      ctx.fillStyle = '#fff';
      ctx.fillRect(0, 0, size, size);
      return;
    }

    try {
      if (typeof qrcode !== 'function') {
        throw new Error('QR 库未加载（vendor-qrcode.js 缺失）');
      }
      var q = qrcode(0, 'M');
      q.addData(text);
      q.make();

      var n = q.getModuleCount();
      var quiet = 4;
      var total = n + quiet * 2;
      var cell = size / total;

      ctx.fillStyle = '#fff';
      ctx.fillRect(0, 0, size, size);
      ctx.fillStyle = '#000';

      for (var r = 0; r < n; r++) {
        for (var c = 0; c < n; c++) {
          if (!q.isDark(r, c)) continue;
          var x = Math.floor((c + quiet) * cell);
          var y = Math.floor((r + quiet) * cell);
          var w = Math.floor((c + quiet + 1) * cell) - x;
          var h = Math.floor((r + quiet + 1) * cell) - y;
          ctx.fillRect(x, y, w, h);
        }
      }
    } catch (e) {
      ctx.fillStyle = '#fff';
      ctx.fillRect(0, 0, size, size);
      ctx.fillStyle = '#b91c1c';
      ctx.font = '13px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('二维码生成失败', size / 2, size / 2 - 8);
      ctx.fillText('请改用手机号登录', size / 2, size / 2 + 12);
      setMsg('二维码渲染出错：' + e.message, false);
    }
  }

  function switchMode(mode) {
    if (state.mode === mode) return;
    state.mode = mode;
    state.phoneStage = 'idle';
    // 重建整页，保证 tab 高亮和面板都同步
    renderLogin('', true);
    if (mode === 'phone') {
      setStatus('info', '请输入手机号');
      var ip = document.getElementById('tgpan-gate-phone');
      if (ip) ip.focus();
    } else {
      setStatus('info', '请用手机 Telegram 扫码');
      if (state.lastToken) drawQR(state.lastToken);
    }
  }

  function setMsg(text, ok) {
    var el = document.getElementById('tgpan-gate-msg');
    if (!el) return;
    el.textContent = text;
    el.className = 'tgpan-gate-msg' + (ok ? ' info' : '');
  }

  function setStatus(kind, text) {
    var dot = document.getElementById('tgpan-gate-dot');
    var t = document.getElementById('tgpan-gate-statustext');
    if (t) t.textContent = text;
    if (dot) dot.className = 'tgpan-gate-dot' + (kind === 'ok' ? ' ok' : kind === 'err' ? ' err' : '');
  }

  // ---------------------------------------------------------------------
  //  WebSocket（TG 登录通道）
  // ---------------------------------------------------------------------

  function connectWS() {
    if (state.ws && (state.ws.readyState === 0 || state.ws.readyState === 1)) return;

    var ws;
    try {
      ws = new WebSocket(WS_URL);
    } catch (e) {
      setStatus('err', '无法建立连接');
      setMsg('连接登录服务失败：' + e.message, false);
      return;
    }
    state.ws = ws;

    ws.onopen = function () {
      state._reconnTries = 0;   // 连上了 → 退避计数清零
      setStatus('wait', '正在连接 Telegram…');
      // 默认走扫码；如果用户已经切到手机号，就不要发 qr 请求
      // （发 qr 会让后端重新握手一次，手机号流程里纯属添乱）
      if (state.mode === 'qr') {
        ws.send(JSON.stringify({ authType: 'qr' }));
      }
      // 【v14】断线自动续接：等验证码的途中通道断了（手机切后台、
      // 路由器掐空闲连接），重连后自动重新发一次验证码 —— 旧连接里的
      // phoneCodeHash 已随旧通道作废，必须重新发码才有新 hash。
      // 用户只需要输入「最新一条」验证码，不用刷新页面。
      else if (state.mode === 'phone' && state.phoneStage === 'code' && state._autoResume) {
        state._autoResume = false;
        var pn = state._lastPhoneNo || '';
        if (pn) {
          setStatus('wait', '连接已恢复，正在重新发送验证码…');
          ws.send(JSON.stringify({ authType: 'phone', message: 'sendcode', phoneNo: pn }));
          setMsg('刚才的通道断了，验证码已重新发送 —— 请输入【最新收到】的验证码。', true);
        }
      }
    };

    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      handleWS(msg);
    };

    ws.onerror = function () {
      setStatus('err', '连接出错');
    };

    ws.onclose = function () {
      state.ws = null;
      // 已配对 → 不用重连；否则过几秒重来一次
      if (state.status && state.status.state === 'ok') return;

      // 【2026-10 修正】重连要「减速退避」，不能再每 2.5 秒猛敲。
      //
      // 原来固定 2.5 秒重连一次，而每次 onopen 只要还停在扫码模式就会
      // 补发一条 {authType:'qr'} —— 后端收到就**新建一个 TG 客户端重新握手**，
      // 一次要 11~13 秒。结果就是：连接老是断、重连又老是打断握手，
      // 后端反复处在「还没连上 TG」的状态（日志里 tg_ready=false 满地都是），
      // 用户操作就总是踩空。
      //
      // 改成 3s → 6s → 12s → 24s 逐步退避，并且一旦用户已经进入
      // 手机号流程（sendcode 之后），就不再自动重连 —— 那条 WebSocket
      // 里握着 phoneCodeHash 的会话，断了就让它断，让用户自己刷新重来，
      // 总比重连把它冲掉强。
      state._reconnTries = (state._reconnTries || 0) + 1;
      if (state.mode === 'phone' && state.phoneStage !== 'idle') {
        // 【v14】手机流程中断线：不再让用户手动刷新 —— 自动重连，
        // 连上后自动重新发验证码（见 onopen 的 _autoResume 分支）。
        // 后端已加心跳保活，正常情况下通道不会再死；这里只兜底。
        // 最多自动恢复 3 次，超过就老实提示刷新（防死循环消耗发码次数）。
        if ((state._reconnTries - 1) < 3 && state.phoneStage === 'code') {
          state._autoResume = true;
          setStatus('wait', '连接断开，正在自动恢复…');
          var dResume = Math.min(2000 * Math.pow(2, state._reconnTries - 1), 8000);
          setTimeout(function () {
            if (state.status && state.status.state === 'ok') return;
            connectWS();
          }, dResume);
          return;
        }
        setStatus('err', '连接已断开');
        setMsg('登录通道多次断开，请刷新页面后重试。', false);
        return;
      }
      var delay = Math.min(3000 * Math.pow(2, state._reconnTries - 1), 24000);
      setStatus('err', '连接已断开，' + Math.round(delay / 1000) + ' 秒后重连…');
      setTimeout(function () {
        if (state.status && state.status.state === 'ok') return;
        connectWS();
      }, delay);
    };
  }

  // handleWS 处理服务端推来的消息。
  //
  // 协议（后端 pkg/services/services_auth.go）：
  //   → {"authType":"qr"}
  //   ← {"type":"auth","payload":{"token":"tg://login?token=..."}}
  //   ← {"type":"status","message":"已连接到 Telegram"}
  //   ← {"type":"error","message":"..."}
  //   → {"authType":"phone","message":"sendcode","phoneNo":"+86..."}
  //   ← {"type":"auth","payload":{"phoneCodeHash":"..."}}
  //   → {"authType":"phone","message":"signin","phoneCode":"...","phoneCodeHash":"..."}
  //   ← {"type":"auth","message":"2FA required"} → 前端弹两步验证
  //   ← {"type":"auth","payload":{session...},"message":"success"} → 成功
  function handleWS(msg) {
    if (!msg || !msg.type) return;

    if (msg.type === 'status') {
      setStatus('wait', msg.message || '正在连接 Telegram…');
      return;
    }

    if (msg.type === 'error') {
      if (state.unlockLogin) state.unlockLogin();
      setStatus('err', '出错了');
      setMsg(msg.message || '未知错误', false);
      // 手机号发码失败 → 退回可重输状态，否则按钮永远点不动
      if (state.mode === 'phone' && state.phoneStage === 'idle') {
        renderPanel();
      }
      return;
    }

    if (msg.type !== 'auth') return;

    // 【2026-10 修正】后端把验证码类错误转成了短码（见 pkg/services/auth.go），
    // 这里翻译成中文，并明确告诉用户「下一步该干嘛」——重新发一个新码，
    // 而不是让人一脸懵地对着英文报错反复点「登录」。
    var CODE_HINT = {
      PHONE_CODE_INVALID: '验证码不正确。请核对后重新输入；若多次不对，请返回上一步重新获取。',
      PHONE_CODE_EXPIRED: '验证码已过期（很可能已经被用过一次）。请点「重新获取验证码」，用**最新的那个码**，并且只点一次「登录」。',
      PHONE_CODE_EMPTY:   '验证码是空的。请填写 Telegram 发来的验证码。',
      PHONE_CODE_HASH_EMPTY: '验证码会话失效了，请返回上一步重新获取验证码。',
      CODE_HASH_INVALID:  '验证码会话已失效（通常是因为中途重连了）。请返回上一步重新获取验证码。'
    };
    if (CODE_HINT[msg.message]) {
      if (state.unlockLogin) state.unlockLogin();
      // 码废了 → 退回「输入手机号」这一步，逼用户重新发码，避免继续用旧码
      state.phoneStage = 'idle';
      state.phoneCodeHash = '';
      setStatus('err', '验证码已失效');
      setMsg(CODE_HINT[msg.message], false);
      renderPanel();
      return;
    }

    var payload = msg.payload || {};

    // 1) 扫码 token
    if (payload.token) {
      state.lastToken = payload.token;
      if (state.mode === 'qr') {
        drawQR(payload.token);
        setStatus('ok', '请用手机 Telegram 扫码');
      }
      return;
    }

    // 2) 手机号发码成功 → 进入输入验证码阶段
    if (payload.phoneCodeHash) {
      if (state.unlockLogin) state.unlockLogin();
      state.phoneCodeHash = payload.phoneCodeHash;
      state.phoneStage = 'code';
      setStatus('info', '验证码已发出，请查看 Telegram');
      setMsg('验证码已发送到你的 Telegram。填好后点「登录」只需一次，请耐心等它转完。', true);
      if (state.mode !== 'phone') switchMode('phone'); else renderPanel();
      return;
    }

    // 3) 需要两步验证
    if (msg.message === '2FA required') {
      if (state.unlockLogin) state.unlockLogin();
      state.phoneStage = '2fa';
      setStatus('info', '需要两步验证密码');
      setMsg('该账号开启了两步验证，请输入密码', true);
      renderPanel();
      return;
    }

    // 4) 登录成功
    //
    // 【关键】拿到 claim 之后必须先兑换浏览器门票，再进主界面。
    // 不兑换的话，服务端虽然有 TG 凭证，但你这个浏览器没票，
    // 下一次请求（甚至下一次刷新）照样被挡回登录页 —— 看起来就是
    // "登录了但马上又退出来了"。
    if (msg.message === 'success' || payload.session) {
      if (msg.claim) {
        setStatus('ok', '登录成功，正在进入…');
        setMsg('登录成功', true);
        claimTicket(msg.claim);
      } else {
        // 后端没给 claim（理论上不该发生）→ 直接用现有 Cookie 试一次
        setStatus('info', '登录成功，正在确认…');
        onPaired();
      }
      return;
    }
  }

  // claimTicket 用一次性领取码换取浏览器门票，然后进主界面。
  function claimTicket(claim) {
    req('/gate/claim', { method: 'POST', body: { token: claim } })
      .then(function (st) { applyStatus(st, ''); })
      .catch(function (e) {
        setStatus('err', '登录凭据换取失败');
        setMsg('登录成功了，但换取访问凭据失败：' + e.message + '。请刷新页面重试。', false);
      });
  }

  function sendWS(obj) {
    if (!state.ws || state.ws.readyState !== 1) {
      // 还没连上 → 等一下就绪再发（最多重试 20 次，约 10 秒）
      var tries = (state._wsRetry || 0);
      if (tries > 20) {
        setMsg('连接登录服务超时，请刷新页面重试', false);
        return;
      }
      state._wsRetry = tries + 1;
      setTimeout(function () { sendWS(obj); }, 500);
      return;
    }
    state._wsRetry = 0;
    state.ws.send(JSON.stringify(obj));
  }

  // ---------------------------------------------------------------------
  //  状态应用
  // ---------------------------------------------------------------------

  function applyStatus(st, okMsg) {
    state.status = st;
    if (!st) return;

    if (st.state === 'ok') {
      removeOverlay();
      closeWS();
      renderAccountChip(st);
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

    // 未登录 → 展示登录页
    try { document.documentElement.setAttribute('data-tgpan-gate', 'need_login'); } catch (e) {}
    removeAccountChip();
    renderLogin(okMsg || '', true);
  }

  // renderAccountChip 在主界面角落挂一个「换账号」浮标。
  //
  // 为什么需要：登录后凭证存在服务端，用户可能想换成另一个 TG 账号
  // （比如换成有更多空间的号）。没这个入口就只能去删容器里的文件。
  function renderAccountChip(st) {
    removeAccountChip();
    var chip = document.createElement('div');
    chip.id = 'tgpan-account-chip';
    var who = st.name || st.user || '';
    chip.title = who ? ('已登录：' + who + '（点击可换账号）') : '点击可换账号';
    chip.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" '
      + 'stroke-width="2" stroke-linecap="round" stroke-linejoin="round">'
      + '<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>'
      + '<span>' + escHtml(who ? ('换账号 · ' + who) : '换账号') + '</span>';

    chip.onclick = function () {
      if (!window.confirm('要退出当前 Telegram 账号，换一个账号登录吗？\n\n（需要重新扫码或输验证码）')) return;
      chip.style.opacity = '.4';
      chip.style.pointerEvents = 'none';
      req('/gate/repair', { method: 'POST' })
        .then(function (st2) { applyStatus(st2, '已退出，请重新登录'); })
        .catch(function (e) {
          chip.style.opacity = '';
          chip.style.pointerEvents = '';
          window.alert('退出失败：' + e.message);
        });
    };
    (document.body || document.documentElement).appendChild(chip);
  }

  function removeAccountChip() {
    var el = document.getElementById('tgpan-account-chip');
    if (el && el.parentNode) el.parentNode.removeChild(el);
  }

  // onPaired 兜底路径：没拿到 claim 码时，直接查一次状态。
  //
  // 正常情况下走 claimTicket（拿码换票）。这条只是防御：
  // 万一后端版本不匹配、或者 claim 字段丢了，至少不会卡死在登录页。
  function onPaired() {
    if (state.qrTimer) { clearInterval(state.qrTimer); state.qrTimer = null; }
    var tries = 0;
    var check = function () {
      req('/gate/status')
        .then(function (st) {
          if (st && st.state === 'ok') { applyStatus(st, ''); }
          else if (tries++ < 8) { setTimeout(check, 400); }
          else {
            setStatus('err', '登录成功但服务端未就绪');
            setMsg('登录成功了，但服务端还没准备好。请刷新页面。', false);
          }
        })
        .catch(function () { if (tries++ < 8) setTimeout(check, 400); });
    };
    setTimeout(check, 300);
  }

  function closeWS() {
    if (state.qrTimer) { clearInterval(state.qrTimer); state.qrTimer = null; }
    if (state.ws) {
      try { state.ws.onclose = null; state.ws.close(); } catch (e) {}
      state.ws = null;
    }
  }

  // ---------------------------------------------------------------------
  //  启动
  // ---------------------------------------------------------------------

  function refresh(okMsg) {
    req('/gate/status')
      .then(function (st) { applyStatus(st, okMsg); })
      .catch(function () {
        // 状态接口不可用（后端还没起来 / 网络抖动）→ 不拦，交给原流程，
        // 但要提示一下，否则用户看到的是白屏
        removeOverlay();
        setStatus('err', '无法读取登录状态');
      });
  }

  function boot() {
    injectStyle();
    refresh();
    // 轮询：用户可能在另一个标签页/设备上完成了登录，
    // 这里也要跟着进去。只在未登录时轮询。
    if (!state.pollTimer) {
      state.pollTimer = setInterval(function () {
        if (state.status && state.status.state === 'ok') return;
        req('/gate/status').then(function (st) {
          if (st && st.state === 'ok') applyStatus(st, '');
        }).catch(function () {});
      }, 5000);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }

  window.TGPanGate = { refresh: refresh, applyStatus: applyStatus };
})();
