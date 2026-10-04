/* ============================================================================
 * TGPan 登录增强：给手机号登录补上「改用短信接收」按钮
 * ============================================================================
 *
 * 【为什么需要这个文件】
 *
 * Telegram 的登录流程是这样的：
 *
 *   1. 客户端请求验证码 → TG 默认把码发到「你已登录的官方 App」里
 *   2. 界面上会出现一个倒计时，以及「改用短信接收」的选项
 *   3. 用户主动要求切换 → TG 才会把码通过短信通道发出来
 *
 * 问题在于：Teldrive 原版登录页**只有 Next 和 QR Login 两个按钮**，
 * 没有第 2 步那个「改用短信」的入口。而后端其实早就实现了这个能力
 * （pkg/services/auth.go 里的 `resendcode` 分支），只是前端从没调用过。
 *
 * 结果就是：如果 TG 把码发到了 App、而用户又拿不到 App，
 * 这条路就彻底走死了 —— 界面上根本没有按钮去触发短信通道。
 *
 * 这个脚本就是补上那个缺失的按钮。
 *
 * 【怎么实现的】
 *
 * 不去改压缩过的 login.lazy-*.js（改那个太脆，一升级就废），
 * 而是以「旁挂」的方式工作：
 *
 *   - 监听页面上的 WebSocket 帧，拿到后端回的 phoneCodeHash
 *     （浏览器自带的 WebSocket 构造函数，包一层就能截到消息，
 *       不需要碰 React 内部状态）
 *   - 在手机号输入框下面插一个自己的按钮
 *   - 点按钮时，往同一个 WebSocket 发 {authType:"phone", message:"resendcode", ...}
 *   - 后端收到后会调 TG 的 AuthResendCode 换一种方式发码
 *
 * 【能保证什么 / 不能保证什么】
 *
 *   能：把「要求 TG 改用短信发码」这个动作做出来（原版做不到）
 *   不能：保证 TG 真的会发短信。
 *         TG 可能仍然拒绝（比如判定客户端非官方、号码被风控、
 *         或要求先付 SMS Fee）。那些是 TG 侧的策略，客户端无能为力。
 *         本脚本会把 TG 返回的真实类型显示出来，让用户知道卡在哪。
 *
 * ========================================================================== */

(function () {
  'use strict';

  /* 只在登录页生效：路径是 /_auth/login，或者页面上有那个「QR Login」按钮 */
  function isLoginPage() {
    if (location.pathname.indexOf('_auth') >= 0) return true;
    var btns = document.querySelectorAll('button');
    for (var i = 0; i < btns.length; i++) {
      if ((btns[i].textContent || '').trim() === 'QR Login') return true;
    }
    return false;
  }

  /* ------------------------------------------------------------------------
   *  第一部分：截获 WebSocket，拿到 phoneCodeHash
   *
   *  为什么要截 WebSocket：
   *    登录是走 /api/auth/ws 这条长连接的，phoneCodeHash 由后端推回来。
   *    我们需要它才能发 resendcode（TG 要求带上这个 hash）。
   *
   *    这些状态都在 React 组件内部，从外面读不到。
   *    但它一定会经过 WebSocket —— 所以从网络层截最稳，
   *    不依赖任何组件内部实现，React 代码怎么改都不影响。
   * ---------------------------------------------------------------------- */

  var lastPhone = '';       /* 最近一次请求发码用的手机号 */
  var lastHash = '';        /* 后端回的 phoneCodeHash */
  var lastSentType = '';    /* TG 首次发码用的方式（App / SMS / Call...） */
  var wsRefs = [];          /* 当前活着的 websocket，用于回发消息 */

  var NativeWS = window.WebSocket;

  if (NativeWS && !NativeWS.__tgpanPatched) {
    var PatchedWS = function (url, protocols) {
      var ws = protocols === undefined ? new NativeWS(url) : new NativeWS(url, protocols);

      /* 只关心登录那条通道 */
      if (String(url).indexOf('/api/auth/ws') >= 0) {
        wsRefs.push(ws);

        ws.addEventListener('message', function (ev) {
          var msg;
          try { msg = JSON.parse(ev.data); } catch (e) { return; }
          if (!msg || typeof msg !== 'object') return;

          var payload = msg.payload || {};

          /* 后端回 phoneCodeHash —— 说明码已发出（首次发送或重发） */
          if (payload.phoneCodeHash) {
            lastHash = payload.phoneCodeHash;
            if (payload.resent === '1') {
              lastSentType = payload.resentType || '';
              notify('已请求改用其它方式发送，TG 返回：' + friendlyType(lastSentType));
            } else {
              notify('验证码已发送。如果收不到，点下面的按钮改用短信。');
            }
            renderPanel();
          }

          /* 后端明确报错 —— 原文照实显示，这是判断问题的唯一线索 */
          if (msg.type === 'error' && msg.message) {
            notify('TG 返回：' + msg.message, true);
          }

          /* 从「已连接到 Telegram」这类状态里嗅探手机号不太好使，
             改从前端发出的 sendcode 里拿 —— 见下面 send 的包装 */
        });

        ws.addEventListener('close', function () {
          var i = wsRefs.indexOf(ws);
          if (i >= 0) wsRefs.splice(i, 1);
        });
      }

      return ws;
    };

    /* 复制静态属性（CONNECTING / OPEN / CLOSING / CLOSED），
       有些库会读 WebSocket.OPEN 来判断状态 */
    ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED'].forEach(function (k) {
      if (k in NativeWS) PatchedWS[k] = NativeWS[k];
    });
    PatchedWS.prototype = NativeWS.prototype;

    PatchedWS.__tgpanPatched = true;
    window.WebSocket = PatchedWS;
  }

  /* ------------------------------------------------------------------------
   *  第二部分：截获「发出去的」消息，拿到手机号
   *
   *  前端调 sendcode 时会发 {authType:"phone", message:"sendcode", phoneNo:"+8613..."}。
   *  我们在 prototype 上包一层 send，把它记下来。
   * ---------------------------------------------------------------------- */

  if (NativeWS && NativeWS.prototype && !NativeWS.prototype.__tgpanSendPatched) {
    var nativeSend = NativeWS.prototype.send;
    NativeWS.prototype.send = function (data) {
      try {
        var msg = JSON.parse(data);
        if (msg && msg.authType === 'phone') {
          if (msg.phoneNo) lastPhone = msg.phoneNo;
          /* 手动触发一次重发时，前端也会带 phoneCodeHash，同步一下 */
          if (msg.phoneCodeHash) lastHash = msg.phoneCodeHash;
        }
      } catch (e) { /* 不是 JSON 就忽略 */ }
      return nativeSend.apply(this, arguments);
    };
    NativeWS.prototype.__tgpanSendPatched = true;
  }

  /* 把 TG 返回的内部类型名翻译成人话 */
  function friendlyType(t) {
    if (!t) return '未知';
    if (t.indexOf('SentCodeTypeApp') >= 0) return 'App（TG 客户端内）';
    if (t.indexOf('SentCodeTypeSms') >= 0) return '短信';
    if (t.indexOf('SentCodeTypeCall') >= 0) return '语音电话';
    if (t.indexOf('SentCodeTypeFlashCall') >= 0) return '闪信';
    if (t.indexOf('SentCodeTypeMissedCall') >= 0) return '未接来电';
    return t.replace(/^\*?tg\./, '');
  }

  /* ------------------------------------------------------------------------
   *  第三部分：往登录通道回发 resendcode
   * ---------------------------------------------------------------------- */

  function requestSms() {
    if (!lastPhone) {
      notify('请先填手机号并点 Next。', true);
      return;
    }
    if (!lastHash) {
      notify('还没拿到验证码会话，请先点 Next 发送验证码。', true);
      return;
    }
    var ws = wsRefs[wsRefs.length - 1];
    if (!ws || ws.readyState !== 1) {
      notify('与服务器的连接已断开，请刷新页面重试。', true);
      return;
    }

    ws.send(JSON.stringify({
      authType: 'phone',
      message: 'resendcode',
      phoneNo: lastPhone,
      phoneCodeHash: lastHash
    }));
    notify('已请求改用短信发送，等待 TG 回应…');
  }

  /* ------------------------------------------------------------------------
   *  第四部分：界面
   * ---------------------------------------------------------------------- */

  var PANEL_ID = 'tgpan-sms-helper';
  var noteEl = null;

  function notify(text, isError) {
    if (!noteEl || !noteEl.parentNode) return;
    noteEl.textContent = text;
    noteEl.style.color = isError ? '#c0392b' : '#4a5568';
  }

  /* 找到手机号输入框（第一步），在它下面插我们的面板 */
  function findPhoneInput() {
    var inputs = document.querySelectorAll('input');
    for (var i = 0; i < inputs.length; i++) {
      var inp = inputs[i];
      if (inp.type === 'password' || inp.type === 'checkbox') continue;
      /* 手机号那一步：placeholder 或附近有「手机号」字样 */
      var box = inp.closest('div');
      var ctx = '';
      for (var up = 0; up < 4 && box; up++) { ctx += (box.textContent || ''); box = box.parentElement; }
      if (ctx.indexOf('手机号') >= 0 && ctx.indexOf('验证码') < 0) return inp;
    }
    return null;
  }

  function buildPanel() {
    if (document.getElementById(PANEL_ID)) return;
    if (!isLoginPage()) return;

    var phoneInput = findPhoneInput();
    if (!phoneInput) return;

    /* 往上找到包裹输入框的那层，插在它后面 */
    var anchor = phoneInput.closest('div');
    for (var up = 0; up < 3 && anchor && anchor.parentNode; up++) {
      if (anchor.querySelector('button')) break;   /* 到按钮那一层就停 */
      anchor = anchor.parentNode;
    }
    if (!anchor || !anchor.parentNode) return;

    var wrap = document.createElement('div');
    wrap.id = PANEL_ID;
    wrap.style.cssText = 'margin-top:12px;max-width:320px;width:100%;';

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = '收不到验证码？改用短信接收';
    btn.style.cssText = [
      'width:100%', 'padding:10px 14px',
      'border:1px solid #2b7de9', 'border-radius:8px',
      'background:#fff', 'color:#2b7de9',
      'font-size:13px', 'cursor:pointer', 'line-height:1.5'
    ].join(';');
    btn.onclick = requestSms;

    noteEl = document.createElement('div');
    noteEl.style.cssText = 'margin-top:8px;font-size:12px;line-height:1.6;color:#4a5568;';
    noteEl.textContent = '提示：Telegram 默认把验证码发到「已登录的 TG 客户端」。' +
                         '如果那边收不到，点上面的按钮改为短信发送。';

    wrap.appendChild(btn);
    wrap.appendChild(noteEl);
    anchor.parentNode.insertBefore(wrap, anchor.nextSibling);
  }

  function renderPanel() {
    try { buildPanel(); } catch (e) { /* 静默：登录页结构变了也不该报错刷屏 */ }
  }

  /* 登录页是 SPA，切步骤时 DOM 会重建，定时检查一下 */
  function watch() {
    if (!document.body) { setTimeout(watch, 200); return; }
    renderPanel();
    setInterval(renderPanel, 1200);
  }

  watch();
})();
