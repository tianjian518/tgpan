/* ==========================================================================
 *  TGPan 控制台插件
 *
 *  在 Teldrive 网盘界面右下角加一个「TGPan 控制台」按钮，点开后有四个页签：
 *
 *    1. 扫描频道   —— 手动扫一个频道，把视频变成网盘文件夹
 *    2. 自动扫描   —— 管理定时扫描的频道（默认每 2 分钟）
 *    3. WebDAV     —— 生成密码，把网盘挂到网易爆米花 / Infuse / VidHub
 *    4. 帮助       —— 使用说明
 *
 *  原理：调用后端自定义接口，不改动原版前端代码。
 * ========================================================================== */
(function () {
  'use strict';

  if (window.__tgpanConsoleInjected) return;
  window.__tgpanConsoleInjected = true;

  var ACCENT = '#2b7de9';
  // 语义色收进变量，暗色模式统一切换，避免满屏硬编码
  var OK_BG = 'var(--tgpan-ok-bg,#eafaf0)', OK_FG = 'var(--tgpan-ok-fg,#0a7a3d)', OK_BD = 'var(--tgpan-ok-bd,#b7e6c9)';
  var ER_BG = 'var(--tgpan-er-bg,#fdecec)', ER_FG = 'var(--tgpan-er-fg,#b3261e)', ER_BD = 'var(--tgpan-er-bd,#f5c2c0)';
  var LD_BG = 'var(--tgpan-ld-bg,#eef7fe)', LD_FG = 'var(--tgpan-ld-fg,#1a73b5)', LD_BD = 'var(--tgpan-ld-bd,#c4e2f7)';

  var STYLE = [
    /* --- 浮标 --- */
    '#tgpan-fab{position:fixed;right:24px;bottom:24px;z-index:99998;',
    'background:' + ACCENT + ';color:#fff;border:none;',
    'border-radius:10px;padding:13px 20px;font-size:15px;font-weight:500;cursor:pointer;',
    'box-shadow:0 4px 14px rgba(43,125,233,.38);display:flex;align-items:center;gap:8px;',
    'transition:transform .16s cubic-bezier(.4,0,.2,1),box-shadow .16s;font-family:inherit}',
    '#tgpan-fab:hover{transform:translateY(-2px);box-shadow:0 10px 24px rgba(43,125,233,.46)}',
    '#tgpan-fab:focus-visible{outline:2px solid #fff;outline-offset:-4px}',

    /* --- 遮罩与容器 --- */
    '#tgpan-mask{position:fixed;inset:0;background:rgba(15,20,28,.52);z-index:99999;',
    'display:none;align-items:center;justify-content:center;padding:16px;',
    'backdrop-filter:blur(2px)}',
    '#tgpan-mask.show{display:flex;animation:tgpan-fade .18s ease-out}',
    '@keyframes tgpan-fade{from{opacity:0}to{opacity:1}}',
    '#tgpan-box{background:var(--color-surface-container,#fff);',
    'color:var(--color-on-surface,#111);',
    'border-radius:14px;width:100%;max-width:680px;',
    'max-height:88vh;overflow:hidden;display:flex;flex-direction:column;',
    'box-shadow:0 24px 64px rgba(0,0,0,.3);',
    'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}',
    '#tgpan-mask.show #tgpan-box{animation:tgpan-pop .2s cubic-bezier(.34,1.3,.64,1)}',
    '@keyframes tgpan-pop{from{transform:scale(.97);opacity:.6}to{transform:scale(1);opacity:1}}',

    /* --- 头部：字号收敛到 20 / 16 / 14 / 12 四档 --- */
    '#tgpan-head{padding:20px 24px 0;flex-shrink:0}',
    '#tgpan-head h3{margin:0 0 4px;font-size:20px;font-weight:600;letter-spacing:-.2px;',
    'display:flex;align-items:center;gap:8px}',
    '#tgpan-head .sub{color:var(--color-on-surface-variant,#7a7f87);font-size:13px;line-height:1.6}',

    /* --- 页签 --- */
    '#tgpan-tabs{display:flex;gap:2px;margin:16px 0 0;',
    'border-bottom:1px solid var(--color-outline-variant,#eceef1);',
    'padding:0 24px;flex-shrink:0;overflow-x:auto;scrollbar-width:none}',
    '#tgpan-tabs::-webkit-scrollbar{display:none}',
    '.tgpan-tab{padding:10px 14px;font-size:14px;cursor:pointer;border:none;background:none;',
    'color:var(--color-on-surface-variant,#6b7280);font-family:inherit;white-space:nowrap;',
    'border-bottom:2px solid transparent;margin-bottom:-1px;transition:color .15s,border-color .15s}',
    '.tgpan-tab:hover{color:' + ACCENT + '}',
    '.tgpan-tab:focus-visible{outline:2px solid ' + ACCENT + ';outline-offset:-2px;border-radius:6px 6px 0 0}',
    '.tgpan-tab.active{color:' + ACCENT + ';border-bottom-color:' + ACCENT + ';font-weight:600}',

    /* --- 主体 --- */
    '#tgpan-body{padding:20px 24px;overflow-y:auto;flex:1}',
    '.tgpan-pane{display:none}',
    '.tgpan-pane.active{display:block;animation:tgpan-slide .18s ease-out}',
    '@keyframes tgpan-slide{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:none}}',

    /* --- 表单 --- */
    '.tgpan-field{margin-bottom:16px}',
    '.tgpan-field label{display:block;font-size:14px;font-weight:600;margin-bottom:6px}',
    '.tgpan-field .hint{font-weight:400;color:var(--color-on-surface-variant,#9096a0);font-size:12px}',
    '.tgpan-field input{width:100%;box-sizing:border-box;padding:10px 13px;',
    'border:1.5px solid var(--color-outline,#e2e5e9);border-radius:8px;font-size:14px;',
    'outline:none;transition:border-color .15s,box-shadow .15s;font-family:inherit;',
    'background:var(--color-surface,#fff);color:var(--color-on-surface,#111)}',
    '.tgpan-field input:focus{border-color:' + ACCENT + ';box-shadow:0 0 0 3px rgba(43,125,233,.12)}',

    /* --- 开关 --- */
    '.tgpan-switch{display:inline-flex;align-items:center;gap:8px;cursor:pointer;user-select:none}',
    '.tgpan-switch input{display:none}',
    '.tgpan-switch .track{width:38px;height:21px;border-radius:11px;background:#cfd3d8;',
    'position:relative;transition:background .2s;flex:0 0 auto}',
    '.tgpan-switch .track::after{content:"";position:absolute;top:2px;left:2px;width:17px;height:17px;',
    'border-radius:50%;background:#fff;transition:transform .2s;box-shadow:0 1px 3px rgba(0,0,0,.22)}',
    '.tgpan-switch input:checked+.track{background:' + ACCENT + '}',
    '.tgpan-switch input:checked+.track::after{transform:translateX(17px)}',
    '.tgpan-switch input:focus-visible+.track{outline:2px solid ' + ACCENT + ';outline-offset:2px}',

    /* --- 按钮：统一圆角 8、补 focus-visible --- */
    '.tgpan-btn{padding:10px 18px;border-radius:8px;border:1px solid transparent;font-size:14px;',
    'font-weight:500;cursor:pointer;font-family:inherit;',
    'transition:background .15s,border-color .15s,opacity .15s}',
    '.tgpan-btn:hover{opacity:.9}',
    '.tgpan-btn:focus-visible{outline:2px solid ' + ACCENT + ';outline-offset:2px}',
    '.tgpan-btn:disabled{opacity:.45;cursor:not-allowed}',
    '.tgpan-btn.primary{background:' + ACCENT + ';color:#fff}',
    '.tgpan-btn.primary:hover{background:#1f6fd6}',
    '.tgpan-btn.ghost{background:var(--color-surface-container-high,#f4f5f7);',
    'border-color:var(--color-outline-variant,#dfe3e8);',
    'color:var(--color-on-surface,#3d4450)}',
    '.tgpan-btn.ghost:hover{background:var(--color-surface-container-highest,#e9ecf1)}',
    '.tgpan-btn.danger{background:#e64545;color:#fff}',
    '.tgpan-btn.danger:hover{background:#d33838}',
    '.tgpan-btn.sm{padding:7px 13px;font-size:13px;border-radius:7px}',
    '.tgpan-actions{display:flex;gap:10px;margin-top:20px}',
    '.tgpan-actions .tgpan-btn{flex:1}',

    /* --- 结果提示 --- */
    '.tgpan-result{margin-top:16px;padding:13px 15px;border-radius:9px;font-size:13px;',
    'line-height:1.75;display:none;white-space:pre-wrap;word-break:break-word;',
    'border-left:3px solid transparent}',
    '.tgpan-result.ok{background:' + OK_BG + ';color:' + OK_FG + ';border-color:' + OK_BD + ';display:block}',
    '.tgpan-result.err{background:' + ER_BG + ';color:' + ER_FG + ';border-color:' + ER_BD + ';display:block}',
    '.tgpan-result.load{background:' + LD_BG + ';color:' + LD_FG + ';border-color:' + LD_BD + ';display:block}',
    '.tgpan-result.load::before{content:"";display:inline-block;width:11px;height:11px;',
    'margin-right:7px;vertical-align:-1px;border:2px solid currentColor;border-right-color:transparent;',
    'border-radius:50%;animation:tgpan-spin .7s linear infinite}',
    '@keyframes tgpan-spin{to{transform:rotate(360deg)}}',

    /* --- 帮助块 --- */
    '.tgpan-help{margin-top:14px;padding:13px 15px;background:var(--color-surface-container-low,#f7f8fa);',
    'border-radius:9px;font-size:13px;color:var(--color-on-surface-variant,#5c636e);line-height:1.85}',
    '.tgpan-help b{color:var(--color-on-surface,#2b313b)}',
    '.tgpan-help code{background:var(--color-surface-container-high,#e8eaed);padding:1px 6px;',
    'border-radius:4px;font-size:12px;font-family:ui-monospace,Menlo,Consolas,monospace}',

    /* --- 频道卡片 --- */
    '.tgpan-ch{display:flex;align-items:center;gap:12px;padding:13px 15px;border-radius:10px;',
    'border:1px solid var(--color-outline-variant,#e8ebef);margin-bottom:10px;',
    'background:var(--color-surface-container-low,#fafbfc);transition:border-color .15s}',
    '.tgpan-ch:hover{border-color:var(--color-outline,#d6dae0)}',
    '.tgpan-ch .info{flex:1;min-width:0}',
    '.tgpan-ch .name{font-size:14px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    '.tgpan-ch .meta{font-size:12px;color:var(--color-on-surface-variant,#838a95);margin-top:3px;line-height:1.6}',
    '.tgpan-ch .err{font-size:12px;color:#b3261e;margin-top:3px;line-height:1.5}',
    '.tgpan-ch .ops{display:flex;gap:6px;flex:0 0 auto}',

    /* --- 凭据卡片 --- */
    '.tgpan-cred{display:flex;align-items:center;gap:12px;padding:13px 15px;border-radius:10px;',
    'border:1px solid var(--color-outline-variant,#e8ebef);margin-bottom:10px;',
    'background:var(--color-surface-container-low,#fafbfc)}',
    '.tgpan-cred .info{flex:1;min-width:0}',
    '.tgpan-cred .uname{font-size:14px;font-weight:600;font-family:ui-monospace,Menlo,Consolas,monospace}',
    '.tgpan-cred .meta{font-size:12px;color:var(--color-on-surface-variant,#838a95);margin-top:3px}',

    /* --- 密码框 --- */
    '.tgpan-pwbox{margin-top:14px;padding:15px;border-radius:10px;background:var(--tgpan-warn-bg,#fff8e6);',
    'border:1.5px dashed var(--tgpan-warn-bd,#ffc107)}',
    '.tgpan-pwbox .t{font-size:13px;font-weight:600;color:var(--tgpan-warn-fg,#8a6100);margin-bottom:10px}',
    '.tgpan-pwbox .v{display:flex;gap:8px;align-items:center;margin-bottom:8px}',
    '.tgpan-pwbox .v code{flex:1;background:var(--color-surface,#fff);padding:9px 11px;border-radius:7px;',
    'font-family:ui-monospace,Menlo,Consolas,monospace;font-size:13px;',
    'border:1px solid var(--tgpan-warn-bd,#ffd75e);word-break:break-all;color:var(--color-on-surface,#333)}',
    '.tgpan-pwbox .n{font-size:12px;color:var(--tgpan-warn-fg,#8a6100);line-height:1.6}',

    /* --- 挂载信息表 --- */
    '.tgpan-mount{margin-top:12px;border:1px solid var(--color-outline-variant,#e8ebef);border-radius:10px;overflow:hidden}',
    '.tgpan-mount .r{display:flex;border-bottom:1px solid var(--color-outline-variant,#e8ebef)}',
    '.tgpan-mount .r:last-child{border-bottom:none}',
    '.tgpan-mount .k{flex:0 0 96px;padding:10px 13px;font-size:13px;font-weight:600;',
    'background:var(--color-surface-container-low,#fafbfc);color:var(--color-on-surface-variant,#5c636e)}',
    '.tgpan-mount .v{flex:1;padding:10px 13px;font-size:13px;min-width:0;',
    'font-family:ui-monospace,Menlo,Consolas,monospace;word-break:break-all;display:flex;align-items:center}',

    /* --- 空 / 加载状态：class 与 id 都支持（原先只写了 id，剧集页签用的是 class，样式全丢） --- */
    '#tgpan-empty,.tgpan-empty{text-align:center;padding:40px 16px;font-size:14px;',
    'color:var(--color-on-surface-variant,#969ba4);line-height:1.9}',
    '.tgpan-empty .ico{display:block;font-size:30px;margin-bottom:10px;opacity:.55}',
    '.tgpan-empty .cta{margin-top:14px}',
    '.tgpan-skeleton{display:flex;flex-direction:column;gap:10px;padding:6px 0}',
    '.tgpan-skel-row{height:58px;border-radius:10px;',
    'background:linear-gradient(90deg,#f0f1f4 25%,#e6e8ec 37%,#f0f1f4 63%);',
    'background-size:400% 100%;animation:tgpan-shimmer 1.3s ease-in-out infinite}',
    '@keyframes tgpan-shimmer{0%{background-position:100% 50%}100%{background-position:0 50%}}',

    /* --- 剧集卡片 --- */
    '.tgpan-series-grid{display:grid;gap:10px}',
    '.tgpan-series-card{border:1px solid var(--color-outline-variant,#e8ebef);',
    'border-radius:11px;padding:14px 16px;background:var(--color-surface,#fff);',
    'box-shadow:0 1px 2px rgba(16,24,40,.04)}',
    '.tgpan-series-name{font-size:16px;font-weight:650;margin-bottom:5px;letter-spacing:-.1px}',
    '.tgpan-series-meta{font-size:13px;color:' + ACCENT + ';margin-bottom:10px;font-weight:500}',
    '.tgpan-series-detail{display:flex;gap:8px;font-size:12px;margin-bottom:8px}',
    '.tgpan-series-detail .k{flex:0 0 68px;color:var(--color-on-surface-variant,#838a95)}',
    '.tgpan-series-detail .v{flex:1;word-break:break-all;min-width:0}',
    '.tgpan-series-samples{display:flex;flex-wrap:wrap;gap:5px;margin-bottom:10px}',
    '.tgpan-chip{font-size:12px;padding:3px 8px;border-radius:6px;',
    'background:var(--color-surface-container-low,#f2f4f7);',
    'color:var(--color-on-surface-variant,#5c636e);',
    'font-family:ui-monospace,Menlo,Consolas,monospace}',
    '.tgpan-series-ops{display:flex;gap:8px;flex-wrap:wrap}',

    /* --- 剧集文件列表（点选改名） --- */
    '.tgpan-ep-list{margin-top:10px;border-top:1px solid var(--color-outline-variant,#eceef1);padding-top:10px;',
    'max-height:280px;overflow-y:auto}',
    '.tgpan-ep-row{display:flex;align-items:center;gap:9px;padding:7px 9px;border-radius:8px;',
    'cursor:pointer;font-size:13px;transition:background .13s}',
    '.tgpan-ep-row:hover{background:var(--color-surface-container-low,#f4f6f9)}',
    '.tgpan-ep-row .ep{flex:0 0 42px;text-align:center;font-size:11px;font-weight:700;',
    'color:' + ACCENT + ';background:var(--tgpan-ep-bg,#eaf2fd);border-radius:5px;padding:2px 0}',
    '.tgpan-ep-row .nm{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    '.tgpan-ep-row .sz{flex:0 0 auto;font-size:11px;color:var(--color-on-surface-variant,#969ba4)}',
    '.tgpan-ep-row .go{flex:0 0 auto;font-size:12px;color:' + ACCENT + ';font-weight:600}',

    /* --- 轻量编辑表单 --- */
    '.tgpan-edit{margin-top:12px;padding:14px;border-radius:10px;',
    'background:var(--color-surface-container-low,#f7f8fa);',
    'border:1px solid var(--color-outline-variant,#e4e7ec)}',
    '.tgpan-edit .row{margin-bottom:11px}',
    '.tgpan-edit label{display:block;font-size:13px;font-weight:600;margin-bottom:5px}',
    '.tgpan-edit input{width:100%;box-sizing:border-box;padding:9px 12px;border-radius:8px;',
    'border:1.5px solid var(--color-outline,#e2e5e9);font-size:13.5px;font-family:inherit;',
    'background:var(--color-surface,#fff);color:var(--color-on-surface,#111);outline:none}',
    '.tgpan-edit input:focus{border-color:' + ACCENT + ';box-shadow:0 0 0 3px rgba(43,125,233,.12)}',
    '.tgpan-edit .ops{display:flex;gap:8px;margin-top:13px}',

    /* --- 暗色模式：补齐 result / pwbox / help / 卡片 --- */
    '.dark #tgpan-box,[data-theme="dark"] #tgpan-box{',
    '--tgpan-ok-bg:#12291d;--tgpan-ok-fg:#68d391;--tgpan-ok-bd:#22543a;',
    '--tgpan-er-bg:#2d1618;--tgpan-er-fg:#fc8181;--tgpan-er-bd:#5c2a2e;',
    '--tgpan-ld-bg:#12202e;--tgpan-ld-fg:#63b3ed;--tgpan-ld-bd:#23405c;',
    '--tgpan-warn-bg:#2b2413;--tgpan-warn-bd:#7a6420;--tgpan-warn-fg:#ecc94b;',
    '--tgpan-ep-bg:#16283d}',
    '.dark .tgpan-help,[data-theme="dark"] .tgpan-help{',
    'background:var(--color-surface-container-low,#20242b) !important}',
    '.dark .tgpan-help b,[data-theme="dark"] .tgpan-help b{',
    'color:var(--color-on-surface,#e6e8eb) !important}',
    '.dark .tgpan-help code,[data-theme="dark"] .tgpan-help code{',
    'background:var(--color-surface-container-high,#2c313a) !important}',
    '.dark .tgpan-skel-row,[data-theme="dark"] .tgpan-skel-row{',
    'background:linear-gradient(90deg,#23272e 25%,#2b3038 37%,#23272e 63%);',
    'background-size:400% 100%}',

    /* --- 移动端 --- */
    '@media(max-width:600px){',
    '#tgpan-fab{right:14px;bottom:14px;padding:11px 16px;font-size:14px}',
    '#tgpan-head{padding:18px 16px 0}',
    '#tgpan-tabs{padding:0 16px}',
    '#tgpan-body{padding:16px}',
    '#tgpan-box{max-height:92vh;border-radius:12px}',
    '.tgpan-mount .k{flex:0 0 74px;font-size:12px}',
    '.tgpan-mount .v{font-size:12px}',
    '.tgpan-ch{flex-wrap:wrap}',
    '.tgpan-ch .ops{width:100%;justify-content:flex-end}',
    '.tgpan-actions{flex-direction:column-reverse}',
    '.tgpan-series-detail .k{flex:0 0 56px}',
    '}'
  ].join('');


  /* ======================================================================
   *  工具函数
   * ==================================================================== */

  function h(html) {
    var d = document.createElement('div');
    d.innerHTML = html;
    return d.firstElementChild;
  }

  function $(sel, root) {
    return (root || document).querySelector(sel);
  }

  function humanSize(n) {
    n = Number(n) || 0;
    if (n <= 0) return '0 B';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(i === 0 ? 0 : 2) + ' ' + u[i];
  }

  function fmtTime(iso) {
    if (!iso) return '从未';
    try {
      var d = new Date(iso);
      var p = function (x) { return x < 10 ? '0' + x : '' + x; };
      return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) +
        ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
    } catch (e) { return iso; }
  }

  function api(method, path, body) {
    var opt = {
      method: method,
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include'
    };
    if (body !== undefined) opt.body = JSON.stringify(body);
    return fetch('/api' + path, opt).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = JSON.parse(t); } catch (e) { j = { message: t }; }
        return { ok: r.ok, status: r.status, data: j };
      });
    });
  }

  function setResult(el, kind, text) {
    if (!el) return;
    el.className = 'tgpan-result ' + kind;
    el.textContent = text;
  }

  function esc(s) {
    return String(s === undefined || s === null ? '' : s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function copyText(text, btn) {
    var done = function () {
      if (!btn) return;
      var old = btn.textContent;
      btn.textContent = '✓ 已复制';
      setTimeout(function () { btn.textContent = old; }, 1600);
    };
    var fallback = function () {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand('copy'); done(); } catch (e) { }
      document.body.removeChild(ta);
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(done).catch(fallback);
    } else {
      fallback();
    }
  }

  /* ======================================================================
   *  构建界面
   * ==================================================================== */

  function buildUI() {
    var fab = document.createElement('button');
    fab.id = 'tgpan-fab';
    fab.innerHTML = '<span style="font-size:17px">🗂</span><span>TGPan 控制台</span>';
    fab.onclick = openModal;
    document.body.appendChild(fab);

    var mask = h([
      '<div id="tgpan-mask">',
      '<div id="tgpan-box">',
      '  <div id="tgpan-head">',
      '    <h3>🗂 TGPan 控制台</h3>',
      '    <div class="sub">把 Telegram 频道变成网盘，直接在线看、挂播放器。</div>',
      '    <div id="tgpan-tabs" role="tablist" aria-label="TGPan 功能页签">',
      '      <button class="tgpan-tab active" role="tab" aria-selected="true" tabindex="0" data-tab="scan">📡 扫描频道</button>',
      '      <button class="tgpan-tab" role="tab" aria-selected="false" tabindex="-1" data-tab="auto">🔄 自动扫描</button>',
      '      <button class="tgpan-tab" role="tab" aria-selected="false" tabindex="-1" data-tab="webdav">🔗 WebDAV</button>',
      '      <button class="tgpan-tab" role="tab" aria-selected="false" tabindex="-1" data-tab="series">🎬 剧集</button>',
      '      <button class="tgpan-tab" role="tab" aria-selected="false" tabindex="-1" data-tab="bots">🤖 Bot 加速</button>',
      '      <button class="tgpan-tab" role="tab" aria-selected="false" tabindex="-1" data-tab="help">💡 帮助</button>',
      '    </div>',
      '  </div>',
      '  <div id="tgpan-body">',

      '    <div class="tgpan-pane active" data-pane="scan">',
      '      <div class="tgpan-field">',
      '        <label>频道 ID <span class="hint">（必填）</span></label>',
      '        <input id="tgpan-channel" placeholder="例如 -1001234567890 或 1234567890" autocomplete="off">',
      '      </div>',
      '      <div class="tgpan-field">',
      '        <label>网盘文件夹名 <span class="hint">（选填，默认用频道标题）</span></label>',
      '        <input id="tgpan-folder" placeholder="例如 我的电影" autocomplete="off">',
      '      </div>',
      '      <div class="tgpan-field">',
      '        <label class="tgpan-switch">',
      '          <input type="checkbox" id="tgpan-auto">',
      '          <span class="track"></span>',
      '          <span style="font-size:13px;font-weight:600">加入自动扫描</span>',
      '          <span class="hint">每 2 分钟自动检查新视频</span>',
      '        </label>',
      '      </div>',
      '      <div id="tgpan-scan-result" class="tgpan-result"></div>',
      '      <div class="tgpan-actions">',
      '        <button class="tgpan-btn ghost" id="tgpan-close">关闭</button>',
      '        <button class="tgpan-btn primary" id="tgpan-start">开始扫描</button>',
      '      </div>',
      '    </div>',

      '    <div class="tgpan-pane" data-pane="auto">',
      '      <div class="tgpan-help" style="margin-top:0">',
      '        <b>自动扫描</b>：登记频道后，系统每隔一段时间自动检查有没有新视频，',
      '        发现就自动加进网盘。默认 <b>2 分钟</b>一次，只拉新消息（增量），很省资源。',
      '      </div>',
      '      <div id="tgpan-ch-list" style="margin-top:16px"></div>',
      '      <div id="tgpan-auto-result" class="tgpan-result"></div>',
      '      <div style="margin-top:16px">',
      '        <button class="tgpan-btn ghost sm" id="tgpan-auto-refresh">刷新列表</button>',
      '      </div>',
      '    </div>',

      '    <div class="tgpan-pane" data-pane="webdav">',
      '      <div class="tgpan-help" style="margin-top:0">',
      '        <b>把网盘挂到播放器</b>：生成一个专用密码，填进网易爆米花 / Infuse / VidHub，',
      '        就能像本地硬盘一样浏览和播放频道里的视频。',
      '      </div>',
      '      <div id="tgpan-mount-info" style="margin-top:16px"></div>',
      '      <div class="tgpan-field" style="margin-top:16px">',
      '        <label>备注名 <span class="hint">（选填，方便区分是哪台设备）</span></label>',
      '        <input id="tgpan-cred-label" placeholder="例如 客厅电视" autocomplete="off">',
      '      </div>',
      '      <div style="display:flex;gap:10px">',
      '        <button class="tgpan-btn primary" id="tgpan-cred-new">生成 WebDAV 密码</button>',
      '        <button class="tgpan-btn ghost" id="tgpan-cred-refresh">刷新</button>',
      '      </div>',
      '      <div id="tgpan-cred-list" style="margin-top:16px"></div>',
      '      <div id="tgpan-cred-result" class="tgpan-result"></div>',
      '    </div>',

      '    <div class="tgpan-pane" data-pane="series">',
      '      <div class="tgpan-help" style="margin-top:0">',
      '        <b>剧集自动归档</b>：扫描时如果认出文件名（或消息配文）里的「第几集」，',
      '        会自动把这一集改名成 <code>剧名 S01E05.mp4</code>，',
      '        并收进频道文件夹下面的「剧名」子文件夹。认不出来的就平铺放着，不动它。',
      '      </div>',
      '      <div id="tgpan-series-list" style="margin-top:16px"></div>',
      '      <div id="tgpan-series-result" class="tgpan-result"></div>',
      '      <div style="margin-top:16px">',
      '        <button class="tgpan-btn ghost sm" id="tgpan-series-refresh">刷新列表</button>',
      '      </div>',
      '    </div>',

      '    <div class="tgpan-pane" data-pane="bots">',
      '      <div class="tgpan-help" style="margin-top:0">',
      '        <b>🤖 为什么要配 Bot？</b><br>',
      '        默认情况下，拉视频用的是<b>你自己的 TG 账号</b>，每次拖进度条都要重建一次账号连接，',
      '        所以会卡十几秒。配上 Bot 之后改用 Bot 连接，<b>建连快得多</b>，拖进度能明显变顺。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>⚠️ 只能加速「你自己的频道」</b><br>',
      '        Bot 需要被设为频道管理员才能取文件。所以：<br>',
      '        · <b>你自己建的频道</b>（含从别处转发进来的）→ ✅ 能自动加速<br>',
      '        · <b>别人的频道</b>（你只是订阅）→ ⚠️ 加不进去，维持原样，但仍可正常播放<br>',
      '        保存后下方会逐个列出结果，失败的不会影响使用。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>🔑 怎么拿 Bot Token？</b><br>',
      '        在 TG 里找 <code>@BotFather</code> → 发 <code>/newbot</code> → 起个名字 → 它给你一串',
      '        <code>数字:字母</code> 形式的字符串，那就是 Token。<br>',
      '        <b>可以填多个</b>（一行一个），系统会轮流使用来分摊限流，更稳。',
      '      </div>',
      '      <div class="tgpan-field" style="margin-top:16px">',
      '        <label>Bot Token <span class="hint">（一行一个，留空则清除）</span></label>',
      '        <textarea id="tgpan-bots" rows="4" spellcheck="false"',
      '          placeholder="1234567890:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw&#10;9876543210:BBJkLmNoPqRsTuVwXyZ0123456789abcde"',
      '          style="width:100%;font-family:ui-monospace,Menlo,Consolas,monospace;',
      '          font-size:12.5px;line-height:1.6;resize:vertical"></textarea>',
      '      </div>',
      '      <div id="tgpan-bots-status" style="margin-top:4px"></div>',
      '      <div id="tgpan-bots-result" class="tgpan-result"></div>',
      '      <div class="tgpan-actions">',
      '        <button class="tgpan-btn ghost" id="tgpan-bots-clear">清除</button>',
      '        <button class="tgpan-btn primary" id="tgpan-bots-save">保存并加速</button>',
      '      </div>',
      '    </div>',

      '    <div class="tgpan-pane" data-pane="help">',
      '      <div class="tgpan-help" style="margin-top:0">',
      '        <b>💡 怎么获取频道 ID？</b><br>',
      '        方法一：在 Telegram 里打开频道，随便转发一条消息到 <code>@userinfobot</code>，它会告诉你 ID。<br>',
      '        方法二：网页版频道链接形如 <code>t.me/c/1234567890/1</code>，中间那串数字就是。<br>',
      '        注意：ID 前面通常要加 <code>-100</code>，例如 <code>-1001234567890</code>。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>📂 文件夹是怎么组织的？</b><br>',
      '        一个频道对应一个顶层文件夹，文件夹名就是频道名。',
      '        之后无论扫多少次，都会放进同一个文件夹里。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>🔄 自动扫描为什么是 2 分钟？</b><br>',
      '        Telegram 对接口调用有频率限制，扫太勤会被临时限制甚至封号。',
      '        2 分钟是个平衡点：新片发现够快，又不会触发限流。',
      '        而且系统只拉「新消息」，不是每次全量重扫，所以很轻。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>🔗 挂到网易爆米花</b><br>',
      '        在爆米花的「添加媒体库 → WebDAV」里填：<br>',
      '        地址：<code>http://你的服务器IP:端口/webdav</code><br>',
      '        账号 / 密码：用「WebDAV」页签里生成的那对。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>🐢 拖进度条很慢怎么办？</b><br>',
      '        默认用你的 TG 账号拉流，每次拖进度都要重建一次账号连接，会卡十几秒。<br>',
      '        去「🤖 Bot 加速」页签填上 Bot Token，改用 Bot 连接，拖进度会明显变顺。<br>',
      '        注意：只能加速<b>你自己建的频道</b>；别人的频道加不进去，但不影响正常播放。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>🎬 电视剧会怎么整理？</b><br>',
      '        如果一个视频的文件名（或消息配文）里写着「第05集」「S01E05」「EP05」这类信息，',
      '        系统会自动把它识别成剧集，做两件事：<br>',
      '        ① 改名成统一格式 <code>剧名 S01E05.mp4</code>，播放器刮削更容易认出；<br>',
      '        ② 收进频道文件夹下面的「剧名」子文件夹，一集一集自动归堆。<br>',
      '        认不出来的（比如电影、名字里没有集数的）就原样平铺放着，不会被乱动。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>⚠️ 识别错了怎么办？</b><br>',
      '        去「🎬 剧集」页签，点「改名 / 移动文件」就能手工调。',
      '        原则是<b>宁可漏判，不要错判</b> —— 认不出来宁可不动，',
      '        也不瞎归类。所以「狂飙 05.mp4」这种只有数字的不会被当成第 5 集。',
      '      </div>',
      '      <div class="tgpan-help">',
      '        <b>⚠️ 温馨提示</b><br>',
      '        建议用小号登录。TG 官方警告：滥用 API 会封号，大量囤积文件会导致频道被清空。',
      '        正常看片、少量上传没问题。',
      '      </div>',
      '    </div>',

      '  </div>',
      '</div>',
      '</div>'
    ].join('\n'));

    document.body.appendChild(mask);

    mask.addEventListener('click', function (e) {
      if (e.target === mask) closeModal();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && mask.classList.contains('show')) closeModal();
    });

    var tabs = mask.querySelectorAll('.tgpan-tab');
    function activateTab(name) {
      for (var j = 0; j < tabs.length; j++) {
        var on = tabs[j].getAttribute('data-tab') === name;
        tabs[j].classList.toggle('active', on);
        tabs[j].setAttribute('aria-selected', on ? 'true' : 'false');
        tabs[j].setAttribute('tabindex', on ? '0' : '-1');
      }
      var panes = mask.querySelectorAll('.tgpan-pane');
      for (var k = 0; k < panes.length; k++) {
        panes[k].classList.toggle('active', panes[k].getAttribute('data-pane') === name);
      }
      if (name === 'auto') loadChannels();
      if (name === 'webdav') loadCredentials();
      if (name === 'series') loadSeries();
      if (name === 'bots') loadBots();
    }

    for (var i = 0; i < tabs.length; i++) {
      tabs[i].addEventListener('click', function () {
        activateTab(this.getAttribute('data-tab'));
      });
      // 键盘左右键在页签间移动（可访问性）
      tabs[i].addEventListener('keydown', function (e) {
        if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
        var idx = Array.prototype.indexOf.call(tabs, this);
        var next = e.key === 'ArrowRight' ? idx + 1 : idx - 1;
        if (next < 0) next = tabs.length - 1;
        if (next >= tabs.length) next = 0;
        tabs[next].focus();
        activateTab(tabs[next].getAttribute('data-tab'));
        e.preventDefault();
      });
    }
    // 供空状态里的 CTA 按钮跨页签跳转
    window.__tgpanSwitchTab = activateTab;

    $('#tgpan-close', mask).onclick = closeModal;
    $('#tgpan-start', mask).onclick = doScan;
    $('#tgpan-auto-refresh', mask).onclick = function () { loadChannels(true); };
    $('#tgpan-cred-new', mask).onclick = createCredential;
    $('#tgpan-cred-refresh', mask).onclick = function () { loadCredentials(true); };
    $('#tgpan-series-refresh', mask).onclick = function () { loadSeries(true); };
    $('#tgpan-bots-save', mask).onclick = saveBots;
    $('#tgpan-bots-clear', mask).onclick = clearBots;
  }

  function openModal() {
    var m = document.getElementById('tgpan-mask');
    if (m) m.classList.add('show');
  }

  function closeModal() {
    var m = document.getElementById('tgpan-mask');
    if (m) m.classList.remove('show');
  }

  /* ======================================================================
   *  功能一：手动扫描频道
   * ==================================================================== */
  function doScan() {
    var btn = document.getElementById('tgpan-start');
    var res = document.getElementById('tgpan-scan-result');
    var chanRaw = (document.getElementById('tgpan-channel').value || '').trim();
    var folder = (document.getElementById('tgpan-folder').value || '').trim();
    var auto = document.getElementById('tgpan-auto').checked;

    if (!chanRaw) {
      setResult(res, 'err', '⚠️ 请先填入频道 ID');
      return;
    }
    // 频道 ID 只保留数字，负号单独判断。
    // 之前用 replace(/[^0-9-]/g,'') 会把负号留在任意位置，
    // 「1-00」这种会被拼成「-100」，属于误放行。
    var negative = chanRaw.charAt(0) === '-';
    var digits = chanRaw.replace(/[^0-9]/g, '');
    if (!digits) {
      setResult(res, 'err', '⚠️ 频道 ID 格式不对，应该是数字（可用 -100 开头）');
      return;
    }
    var channelId = parseInt(digits, 10);
    if (!channelId || isNaN(channelId)) {
      setResult(res, 'err', '⚠️ 频道 ID 格式不对，应该是数字（可用 -100 开头）');
      return;
    }
    if (negative) channelId = -channelId;

    btn.disabled = true;
    btn.textContent = '扫描中…';
    setResult(res, 'load',
      '⏳ 正在读取频道消息，请稍候…\n（消息多的话可能要一两分钟，别关这个窗口）');

    var body = { channelId: channelId, limit: 2000 };
    if (folder) body.folderName = folder;
    if (auto) { body.autoRegister = true; body.intervalSeconds = 120; }

    api('POST', '/scan/channel', body)
      .then(function (r) {
        var d = r.data || {};
        if (!r.ok) {
          setResult(res, 'err', '❌ 扫描失败\n' + (d.message || ('HTTP ' + r.status)));
          return;
        }
        var lines = [
          '✅ ' + (d.message || '扫描完成'),
          '',
          '📁 频道：' + (d.channelName || '-'),
          '📂 文件夹：' + (d.folderName || '-'),
          '🔍 扫描消息：' + (d.scanned || 0) + ' 条',
          '🎬 新导入：' + (d.imported || 0) + ' 个视频',
          '💾 总大小：' + humanSize(d.totalSize),
          '⏭️ 跳过：' + (d.skipped || 0) + ' 条'
        ];
        if (d.autoScan) {
          lines.push('');
          lines.push('🔄 已加入自动扫描，每 2 分钟自动检查新视频');
        }
        if (d.imported > 0) {
          lines.push('');
          lines.push('👉 刷新页面（F5）就能看到新文件夹了');
        }
        setResult(res, 'ok', lines.join('\n'));
      })
      .catch(function (err) {
        setResult(res, 'err', '❌ 请求出错：' + (err && err.message ? err.message : String(err)));
      })
      .finally(function () {
        btn.disabled = false;
        btn.textContent = '开始扫描';
      });
  }

  /* ======================================================================
   *  功能二：自动扫描管理
   * ==================================================================== */
  // 请求序号：防止快速切页签时旧响应覆盖新结果
  var channelsSeq = 0;

  function loadChannels(force) {
    var list = document.getElementById('tgpan-ch-list');
    if (!list) return;
    if (!force && list.getAttribute('data-loaded') === '1') return;

    var seq = ++channelsSeq;
    list.innerHTML = skeleton(2);

    api('GET', '/scan/channels').then(function (r) {
      if (seq !== channelsSeq) return;
      if (!r.ok) {
        list.innerHTML = emptyState('⚠️', '加载失败：' +
          esc((r.data && r.data.message) || ('HTTP ' + r.status)));
        bindGoto(list);
        return;
      }
      var d = r.data || {};
      var chs = d.channels || [];
      list.setAttribute('data-loaded', '1');

      if (!chs.length) {
        list.innerHTML = emptyState('📡',
          '还没有登记任何频道。<br>去「扫描频道」页签扫一个，并勾选「加入自动扫描」。',
          '去扫描频道', 'scan');
        bindGoto(list);
        return;
      }

      var html = [];
      chs.forEach(function (c) {
        var status = c.enabled ? '🟢 自动扫描中' : '⚪ 已暂停';
        var meta = status + ' · 每 ' + c.intervalSec + ' 秒' +
          ' · 已导入 ' + c.totalImported + ' 个' +
          ' · 上次 ' + fmtTime(c.lastScanAt);
        if (c.folderName) meta += ' · 文件夹「' + c.folderName + '」';
        html.push(
          '<div class="tgpan-ch" data-cid="' + esc(String(c.channelId)) + '"' +
          ' data-enabled="' + (c.enabled ? '1' : '0') + '">' +
          '<div class="info">' +
          '<div class="name">' + esc(c.channelName || ('频道 ' + c.channelId)) + '</div>' +
          '<div class="meta">' + esc(meta) + '</div>' +
          (c.lastError ? '<div class="err">⚠️ ' + esc(c.lastError) + '</div>' : '') +
          '</div>' +
          '<div class="ops">' +
          '<button class="tgpan-btn ghost sm" data-act="run">立即扫</button>' +
          '<button class="tgpan-btn ' + (c.enabled ? 'ghost' : 'primary') + ' sm" data-act="toggle">' +
          (c.enabled ? '暂停' : '启用') + '</button>' +
          '<button class="tgpan-btn danger sm" data-act="del">移除</button>' +
          '</div>' +
          '</div>'
        );
      });
      list.innerHTML = html.join('');

      var btns = list.querySelectorAll('button[data-act]');
      for (var i = 0; i < btns.length; i++) {
        btns[i].addEventListener('click', onChannelAction);
      }
    }).catch(function (e) {
      if (seq !== channelsSeq) return;
      list.innerHTML = emptyState('⚠️', '请求出错：' +
        esc(String((e && e.message) || e)));
      bindGoto(list);
    });
  }

  function onChannelAction() {
    var card = this.closest('.tgpan-ch');
    var cid = card.getAttribute('data-cid');
    var act = this.getAttribute('data-act');
    var res = document.getElementById('tgpan-auto-result');
    var btn = this;

    if (act === 'run') {
      btn.disabled = true;
      btn.textContent = '扫描中…';
      setResult(res, 'load', '⏳ 正在扫描频道 ' + cid + '…');
      api('POST', '/scan/channels/' + cid + '/run')
        .then(function (r) {
          var d = r.data || {};
          if (!r.ok) {
            setResult(res, 'err', '❌ ' + (d.message || ('HTTP ' + r.status)));
          } else if (d.imported > 0) {
            setResult(res, 'ok', '✅ 新导入 ' + d.imported + ' 个视频（' +
              humanSize(d.totalSize) + '）\n👉 刷新页面即可看到');
          } else {
            setResult(res, 'ok', '✅ 扫描完成，没有新视频（扫了 ' +
              (d.scanned || 0) + ' 条消息）');
          }
        })
        .catch(function (e) { setResult(res, 'err', '❌ ' + e); })
        .finally(function () {
          btn.disabled = false;
          btn.textContent = '立即扫';
          loadChannels(true);
        });
      return;
    }

    if (act === 'toggle') {
      // 用 data-enabled 属性判断当前状态，而不是读按钮文字。
      // 按钮文字随时可能被改（比如点了变成「切换中…」），靠文字判断会错乱。
      var nowEnabled = card.getAttribute('data-enabled') === '1';
      btn.disabled = true;
      api('POST', '/scan/channels', {
        channelId: parseInt(cid, 10),
        enabled: !nowEnabled,
        scanNow: false
      }).then(function (r) {
        var d = r.data || {};
        if (!r.ok) {
          setResult(res, 'err', '❌ ' + (d.message || ('HTTP ' + r.status)));
        } else {
          setResult(res, 'ok', nowEnabled ? '已暂停该频道的自动扫描' : '已启用自动扫描');
        }
      }).catch(function (e) { setResult(res, 'err', '❌ ' + e); })
        .finally(function () {
          btn.disabled = false;
          loadChannels(true);
        });
      return;
    }

    if (act === 'del') {
      if (!confirm('确定移除这个频道的自动扫描吗？\n（已导入的视频不会被删除）')) return;
      btn.disabled = true;
      api('DELETE', '/scan/channels/' + cid)
        .then(function (r) {
          var d = r.data || {};
          if (!r.ok) {
            setResult(res, 'err', '❌ ' + (d.message || ('HTTP ' + r.status)));
          } else {
            setResult(res, 'ok', '已移除，不再自动扫描该频道');
          }
        })
        .catch(function (e) { setResult(res, 'err', '❌ ' + e); })
        .finally(function () {
          btn.disabled = false;
          loadChannels(true);
        });
    }
  }

  /* ======================================================================
   *  功能四：剧集归档（自动识别的结果 + 手动纠正）
   * ==================================================================== */

  var seriesCache = null;
  // 请求序号：快速切换页签时，旧响应不能覆盖新结果
  var seriesSeq = 0;

  // 骨架屏，替代纯文字「正在读取…」
  function skeleton(rows) {
    var out = ['<div class="tgpan-skeleton">'];
    for (var i = 0; i < (rows || 3); i++) out.push('<div class="tgpan-skel-row"></div>');
    out.push('</div>');
    return out.join('');
  }

  // 带图标和引导按钮的空状态
  function emptyState(icon, text, ctaText, ctaTab) {
    var s = '<div class="tgpan-empty"><span class="ico">' + icon + '</span>' + text;
    if (ctaText) {
      s += '<div class="cta"><button class="tgpan-btn primary sm" data-goto="' +
        esc(ctaTab) + '">' + esc(ctaText) + '</button></div>';
    }
    return s + '</div>';
  }

  // 绑定空状态里的「跳到某页签」按钮
  function bindGoto(root) {
    var gs = root.querySelectorAll('[data-goto]');
    for (var i = 0; i < gs.length; i++) {
      gs[i].onclick = function () {
        var tab = this.getAttribute('data-goto');
        if (typeof window.__tgpanSwitchTab === 'function') window.__tgpanSwitchTab(tab);
      };
    }
  }

  function loadSeries(force) {
    var list = document.getElementById('tgpan-series-list');
    var res = document.getElementById('tgpan-series-result');
    if (!list) return;

    if (seriesCache && !force) { renderSeries(seriesCache); return; }
    var seq = ++seriesSeq;
    list.innerHTML = skeleton(3);
    setResult(res, '', '');

    api('GET', '/scan/series').then(function (r) {
      if (seq !== seriesSeq) return; // 已有更新的请求发出，丢弃本次结果
      if (!r.ok) {
        list.innerHTML = emptyState('⚠️', '加载失败：' +
          esc((r.data && r.data.message) || ('HTTP ' + r.status)));
        bindGoto(list);
        return;
      }
      seriesCache = r.data;
      renderSeries(r.data);
    }).catch(function (e) {
      if (seq !== seriesSeq) return;
      list.innerHTML = emptyState('⚠️', '加载失败：' + esc(String((e && e.message) || e)));
      bindGoto(list);
    });
  }

  function renderSeries(data) {
    var list = document.getElementById('tgpan-series-list');
    if (!list) return;
    var arr = (data && data.series) || [];
    if (!arr.length) {
      list.innerHTML = emptyState('🎬',
        '还没有识别出剧集。<br>扫描一个电视剧频道试试 —— ' +
        '文件名里带「第01集」「S01E05」的会自动归到剧名文件夹里。',
        '去扫描频道', 'scan');
      bindGoto(list);
      return;
    }

    var out = ['<div style="font-size:13px;color:var(--color-on-surface-variant,#6b7280);' +
      'margin-bottom:12px">共 <b>' + arr.length + '</b> 部剧。' +
      '展开后点任意一集即可改名或移动。</div>'];
    out.push('<div class="tgpan-series-grid">');
    for (var i = 0; i < arr.length; i++) {
      var s = arr[i];
      var range = '';
      if (s.episodeCount > 0) {
        range = '第 ' + s.firstEpisode + '-' + s.lastEpisode + ' 集';
        if (s.episodeCount !== (s.lastEpisode - s.firstEpisode + 1)) {
          range += '（已收录 ' + s.episodeCount + ' 集，有缺集）';
        }
      }
      out.push(
        '<div class="tgpan-series-card" data-sid="' + esc(s.id) + '">',
        '  <div class="tgpan-series-name">🎬 ' + esc(s.title) + '</div>',
        '  <div class="tgpan-series-meta">' + esc(range || (s.fileCount + ' 个文件')) + '</div>',
        '  <div class="tgpan-series-samples">' + (s.samples || []).map(function (n) {
          return '<span class="tgpan-chip">' + esc(n) + '</span>';
        }).join('') + '</div>',
        '  <div class="tgpan-series-ops">',
        '    <button class="tgpan-btn ghost sm" data-op="expand">📂 展开文件（' +
        (s.files ? s.files.length : 0) + '）</button>',
        '  </div>',
        '  <div class="tgpan-ep-list" hidden></div>',
        '</div>');
    }
    out.push('</div>');
    list.innerHTML = out.join('\n');

    var cards = list.querySelectorAll('.tgpan-series-card');
    for (var k = 0; k < cards.length; k++) {
      var card = cards[k];
      var sid = card.getAttribute('data-sid');
      var found = null;
      for (var m = 0; m < arr.length; m++) if (arr[m].id === sid) found = arr[m];
      if (!found) continue;
      bindSeriesCard(card, found);
    }
  }

  // bindSeriesCard 绑定单个剧集卡片的展开 / 点选逻辑
  function bindSeriesCard(card, series) {
    var btn = card.querySelector('[data-op="expand"]');
    var pane = card.querySelector('.tgpan-ep-list');
    if (!btn || !pane) return;

    btn.onclick = function () {
      if (!pane.hasAttribute('hidden')) {
        pane.setAttribute('hidden', '');
        btn.innerHTML = '📂 展开文件（' + (series.files ? series.files.length : 0) + '）';
        return;
      }
      renderEpisodeList(pane, series);
      pane.removeAttribute('hidden');
      btn.innerHTML = '📁 收起文件';
    };
  }

  // renderEpisodeList 渲染某一部剧的文件清单，每行可点击去改名
  function renderEpisodeList(pane, series) {
    var files = series.files || [];
    if (!files.length) {
      pane.innerHTML = '<div style="font-size:12.5px;color:#9096a0;padding:8px 4px">' +
        '这部剧下还没有可展示的文件。</div>';
      return;
    }

    var out = [];
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      out.push(
        '<div class="tgpan-ep-row" data-fid="' + esc(f.id) + '" data-fname="' + esc(f.name) + '">',
        '  <span class="ep">' + (f.episode > 0 ? ('E' + f.episode) : '—') + '</span>',
        '  <span class="nm" title="' + esc(f.name) + '">' + esc(f.name) + '</span>',
        '  <span class="sz">' + humanSize(f.size) + '</span>',
        '  <span class="go">改名</span>',
        '</div>');
    }
    pane.innerHTML = out.join('\n');

    // 点某一行 -> 在该卡片内展开轻量编辑表单（不再用 window.prompt 让用户手输 ID）
    var rows = pane.querySelectorAll('.tgpan-ep-row');
    for (var j = 0; j < rows.length; j++) {
      rows[j].onclick = function () {
        var fid = this.getAttribute('data-fid');
        var fname = this.getAttribute('data-fname');
        var file = null;
        for (var t = 0; t < files.length; t++) if (files[t].id === fid) file = files[t];
        if (file) openEpisodeEditor(pane, series, file, fname);
      };
    }
  }

  // openEpisodeEditor 在卡片内展开编辑表单：改名字 + 可选移动到别的文件夹
  function openEpisodeEditor(pane, series, file, oldName) {
    var res = document.getElementById('tgpan-series-result');

    // 默认把新名字预填成规范格式，用户只想改集号时不用全打一遍
    var ext = '.mp4';
    var dot = oldName.lastIndexOf('.');
    if (dot > 0) ext = oldName.slice(dot);
    var suggested = series.title + ' S01E' +
      (file.episode > 0 ? String(file.episode).padStart(2, '0') : '01') + ext;

    var box = document.createElement('div');
    box.className = 'tgpan-edit';
    box.innerHTML = [
      '<div class="row"><label>新文件名</label>',
      '<input type="text" class="ed-name" value="' + esc(suggested) + '"></div>',
      '<div class="row"><label>移动到目录（选填）</label>',
      '<input type="text" class="ed-parent" placeholder="留空=不动；填 root=移到根目录；或填目标文件夹 ID"></div>',
      '<div style="font-size:12px;color:#9096a0;line-height:1.6">',
      '当前文件：<code>' + esc(oldName) + '</code></div>',
      '<div class="ops">',
      '  <button class="tgpan-btn primary sm ed-save">保存</button>',
      '  <button class="tgpan-btn ghost sm ed-cancel">取消</button>',
      '</div>'
    ].join('\n');

    // 同一时刻只保留一个编辑框
    var old = pane.querySelector('.tgpan-edit');
    if (old) old.remove();

    var row = pane.querySelector('.tgpan-ep-row[data-fid="' + cssEsc(file.id) + '"]');
    if (row && row.parentNode) row.parentNode.insertBefore(box, row.nextSibling);
    else pane.appendChild(box);

    var nameInput = box.querySelector('.ed-name');
    if (nameInput) { nameInput.focus(); nameInput.select(); }

    box.querySelector('.ed-cancel').onclick = function () { box.remove(); };
    box.querySelector('.ed-save').onclick = function () {
      var newName = (box.querySelector('.ed-name').value || '').trim();
      var parentRaw = (box.querySelector('.ed-parent').value || '').trim();

      var body = { fileId: file.id };
      if (newName && newName !== oldName) body.name = newName;
      if (parentRaw) {
        body.parentId = (parentRaw === 'root' || parentRaw === '0') ? 'root' : parentRaw;
      }
      if (!body.name && !body.parentId) {
        setResult(res, 'err', '没有改动，已取消。');
        box.remove();
        return;
      }

      setResult(res, 'load', '正在提交…');
      api('POST', '/scan/series/rename', body).then(function (r) {
        if (r.ok) {
          setResult(res, 'ok', '✅ 已保存');
          seriesCache = null;
          loadSeries(true);
        } else {
          setResult(res, 'err', '❌ ' +
            ((r.data && r.data.message) || ('HTTP ' + r.status)));
        }
      }).catch(function (e) {
        setResult(res, 'err', '❌ ' + ((e && e.message) || e));
      });
    };
  }

  // cssEsc 转义属性选择器里要用的值
  function cssEsc(s) {
    return String(s).replace(/["\\]/g, '\\$&');
  }

  /* ======================================================================
   *  功能三：WebDAV 凭据管理
   * ==================================================================== */
  var credentialsSeq = 0;

  function loadCredentials(force) {
    var list = document.getElementById('tgpan-cred-list');
    if (!list) return;
    if (!force && list.getAttribute('data-loaded') === '1') return;

    var seq = ++credentialsSeq;
    list.innerHTML = skeleton(2);

    api('GET', '/webdav/credentials').then(function (r) {
      if (seq !== credentialsSeq) return;
      if (!r.ok) {
        list.innerHTML = emptyState('⚠️', '加载失败：' +
          esc((r.data && r.data.message) || ('HTTP ' + r.status)));
        bindGoto(list);
        return;
      }
      var d = r.data || {};
      var creds = d.credentials || [];
      var mountPath = d.mountPath || '/webdav';
      list.setAttribute('data-loaded', '1');

      var origin = window.location.origin;
      var info = document.getElementById('tgpan-mount-info');
      if (info) {
        info.innerHTML = [
          '<div class="tgpan-mount">',
          '  <div class="r"><div class="k">挂载地址</div><div class="v">',
          '    <span id="tgpan-mount-url">' + esc(origin + mountPath) + '</span>',
          '    <button class="tgpan-btn ghost sm" id="tgpan-copy-url" style="margin-left:8px">复制</button>',
          '  </div></div>',
          '  <div class="r"><div class="k">协议</div><div class="v">WebDAV（HTTP Basic Auth）</div></div>',
          '  <div class="r"><div class="k">账号密码</div><div class="v">用下方生成的凭据</div></div>',
          '</div>'
        ].join('');
        var cp = document.getElementById('tgpan-copy-url');
        if (cp) {
          cp.onclick = function () { copyText(origin + mountPath, this); };
        }
      }

      if (!creds.length) {
        list.innerHTML = emptyState('🔗',
          '还没有生成过密码。<br>点上面的「生成 WebDAV 密码」创建一个，' +
          '填进播放器就能挂载。');
        bindGoto(list);
        return;
      }

      var html = [];
      creds.forEach(function (c) {
        html.push(
          '<div class="tgpan-cred" data-id="' + esc(String(c.id)) + '">' +
          '<div class="info">' +
          '<div class="uname">' + esc(c.username) + '</div>' +
          '<div class="meta">' +
          (c.label ? esc(c.label) + ' · ' : '') +
          '创建于 ' + fmtTime(c.createdAt) +
          ' · 最近使用 ' + fmtTime(c.lastUsed) +
          '</div>' +
          '</div>' +
          '<div class="ops">' +
          '<button class="tgpan-btn danger sm" data-cred-del>删除</button>' +
          '</div>' +
          '</div>'
        );
      });
      list.innerHTML = html.join('');

      var delBtns = list.querySelectorAll('[data-cred-del]');
      for (var i = 0; i < delBtns.length; i++) {
        delBtns[i].addEventListener('click', function () {
          var card = this.closest('.tgpan-cred');
          var id = card.getAttribute('data-id');
          if (!confirm('确定删除这个 WebDAV 凭据吗？\n删除后用它挂载的播放器会立刻失效。')) return;
          var res = document.getElementById('tgpan-cred-result');
          var b = this;
          b.disabled = true;
          api('DELETE', '/webdav/credentials/' + id)
            .then(function (rr) {
              if (!rr.ok) {
                setResult(res, 'err', '❌ ' + ((rr.data && rr.data.message) || ('HTTP ' + rr.status)));
              } else {
                setResult(res, 'ok', '已删除该凭据');
              }
            })
            .catch(function (e) { setResult(res, 'err', '❌ ' + e); })
            .finally(function () { loadCredentials(true); });
        });
      }
    }).catch(function (e) {
      if (seq !== credentialsSeq) return;
      list.innerHTML = emptyState('⚠️', '请求出错：' + esc(String((e && e.message) || e)));
      bindGoto(list);
    });
  }

  function createCredential() {
    var btn = document.getElementById('tgpan-cred-new');
    var res = document.getElementById('tgpan-cred-result');
    var label = (document.getElementById('tgpan-cred-label').value || '').trim();

    btn.disabled = true;
    btn.textContent = '生成中…';
    setResult(res, 'load', '⏳ 正在生成密码…');

    api('POST', '/webdav/credentials', { label: label })
      .then(function (r) {
        var d = r.data || {};
        if (!r.ok) {
          setResult(res, 'err', '❌ 生成失败：' + (d.message || ('HTTP ' + r.status)));
          return;
        }
        var box = h([
          '<div class="tgpan-pwbox">',
          '  <div class="t">⚠️ 密码只显示这一次，请立刻复制保存！</div>',
          '  <div class="v"><span style="font-size:12px;width:52px">账号</span>',
          '    <code>' + esc(d.username || '') + '</code>',
          '    <button class="tgpan-btn ghost sm" data-cp="user">复制</button></div>',
          '  <div class="v"><span style="font-size:12px;width:52px">密码</span>',
          '    <code>' + esc(d.password || '') + '</code>',
          '    <button class="tgpan-btn primary sm" data-cp="pw">复制</button></div>',
          '  <div class="n">关闭这个窗口后就再也看不到密码了。<br>',
          '  如果没存下来，删掉重新生成一个即可。</div>',
          '</div>'
        ].join(''));

        res.className = 'tgpan-result ok';
        res.textContent = '✅ 密码生成成功，请复制保存';
        res.appendChild(box);

        var userBtn = box.querySelector('[data-cp="user"]');
        var pwBtn = box.querySelector('[data-cp="pw"]');
        if (userBtn) userBtn.onclick = function () { copyText(d.username || '', this); };
        if (pwBtn) pwBtn.onclick = function () { copyText(d.password || '', this); };

        document.getElementById('tgpan-cred-label').value = '';
        loadCredentials(true);
      })
      .catch(function (e) {
        setResult(res, 'err', '❌ 请求出错：' + e);
      })
      .finally(function () {
        btn.disabled = false;
        btn.textContent = '生成 WebDAV 密码';
      });
  }

  /* ======================================================================
   *  功能五：Bot 加速
   *
   *  默认拉流用的是用户自己的 TG 账号，每次拖进度条都要重建一次账号连接，
   *  所以会卡十几秒。配上 Bot 之后改走 Bot 连接，建连快得多。
   *
   *  用的全是原版 Teldrive 已有的接口，没有新写后端：
   *    GET    /users/config  → 看当前存了哪些 bot
   *    POST   /users/bots    → 存 bot（后端会自动把 bot 设为各频道管理员）
   *    DELETE /users/bots    → 清空
   * ==================================================================== */
  var botsSeq = 0;

  // 只做格式校验，不做网络校验 —— 真正的有效性由后端调用 TG 时判定。
  // TG Bot Token 形如： 数字:35位左右的字母数字
  var BOT_TOKEN_RE = /^\d{6,}:[A-Za-z0-9_-]{30,}$/;

  function parseBotTokens(raw) {
    var out = [];
    var seen = {};
    var lines = String(raw || '').split('\n');
    for (var i = 0; i < lines.length; i++) {
      var t = lines[i].trim();
      if (!t) continue;
      if (seen[t]) continue;      // 顺手去重，重复填不会造成后端重复处理
      seen[t] = 1;
      out.push(t);
    }
    return out;
  }

  function loadBots(force) {
    var box = document.getElementById('tgpan-bots-status');
    var ta = document.getElementById('tgpan-bots');
    if (!box || !ta) return;
    if (!force && ta.getAttribute('data-loaded') === '1') return;

    var seq = ++botsSeq;
    box.innerHTML = skeleton(1);

    api('GET', '/users/config').then(function (r) {
      if (seq !== botsSeq) return;
      if (!r.ok) {
        // 401 是「没登录」，属于常见情况，给用户看得懂的话；
        // 其它错误也尽量别把后端英文原文直接甩出来。
        if (r.status === 401) {
          box.innerHTML = emptyState('🔒',
            '请先登录网盘账号，登录后回到这里配置 Bot。');
        } else {
          box.innerHTML = emptyState('⚠️',
            '暂时读不到 Bot 状态，请稍后刷新页面重试。');
        }
        bindGoto(box);
        return;
      }
      var bots = (r.data && r.data.bots) || [];
      ta.setAttribute('data-loaded', '1');

      if (!bots.length) {
        box.innerHTML =
          '<div class="tgpan-help" style="margin:0">' +
          '<b>当前未配置 Bot</b> —— 正在用你的 TG 账号拉流，拖进度条会比较慢。' +
          '把 Token 填到上面，点「保存并加速」即可。</div>';
        return;
      }

      var html = ['<div class="tgpan-help" style="margin:0">',
        '<b>已配置 ' + bots.length + ' 个 Bot</b>：<br>'];
      for (var i = 0; i < bots.length; i++) {
        // Token 只显示前后各一小段，避免完整泄露在界面上
        var t = String(bots[i]);
        var masked = t.length > 18
          ? esc(t.slice(0, 12)) + '…' + esc(t.slice(-4))
          : esc(t);
        html.push('· <code>' + masked + '</code><br>');
      }
      html.push('</div>');
      box.innerHTML = html.join('');
      // 回填到输入框，方便用户增删
      ta.value = bots.join('\n');
    });
  }

  function saveBots() {
    var ta = document.getElementById('tgpan-bots');
    var res = document.getElementById('tgpan-bots-result');
    var btn = document.getElementById('tgpan-bots-save');
    if (!ta) return;

    var tokens = parseBotTokens(ta.value);

    if (!tokens.length) {
      setResult(res, 'error', '请先填入至少一个 Bot Token。');
      return;
    }

    var bad = [];
    for (var i = 0; i < tokens.length; i++) {
      if (!BOT_TOKEN_RE.test(tokens[i])) bad.push(tokens[i]);
    }
    if (bad.length) {
      setResult(res, 'error',
        '有 ' + bad.length + ' 个 Token 格式不对。正确格式形如 ' +
        '1234567890:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw（数字:字母数字）。');
      return;
    }

    if (btn) { btn.disabled = true; btn.textContent = '保存中…'; }
    setResult(res, 'loading',
      '正在保存并将 Bot 加入频道，请稍候…（频道较多时可能需要一两分钟，' +
      '期间请不要关闭窗口）');

    var seq = ++botsSeq;
    api('POST', '/users/bots', { bots: tokens }).then(function (r) {
      if (seq !== botsSeq) return;
      if (btn) { btn.disabled = false; btn.textContent = '保存并加速'; }

      if (!r.ok) {
        // 同样：不把后端英文原文直接给用户看
        if (r.status === 401) {
          setResult(res, 'error', '登录已失效，请刷新页面重新登录后再试。');
        } else {
          setResult(res, 'error',
            '保存失败，请稍后重试。若反复失败，请检查 Token 是否正确、' +
            '以及网络是否能访问 Telegram。');
        }
        return;
      }

      setResult(res, 'ok',
        '已保存 ' + tokens.length + ' 个 Bot。系统正在把它们加入你的频道，' +
        '稍等片刻后播放时就会自动启用。');

      // 重新拉一次状态，把最新列表显示出来
      var ta2 = document.getElementById('tgpan-bots');
      if (ta2) ta2.setAttribute('data-loaded', '0');
      loadBots(true);
    });
  }

  function clearBots() {
    var res = document.getElementById('tgpan-bots-result');
    var btn = document.getElementById('tgpan-bots-clear');
    if (btn) { btn.disabled = true; btn.textContent = '清除中…'; }

    var seq = ++botsSeq;
    api('DELETE', '/users/bots').then(function (r) {
      if (seq !== botsSeq) return;
      if (btn) { btn.disabled = false; btn.textContent = '清除'; }

      if (!r.ok) {
        setResult(res, 'error', r.status === 401
          ? '登录已失效，请刷新页面重新登录后再试。'
          : '清除失败，请稍后重试。');
        return;
      }

      var ta = document.getElementById('tgpan-bots');
      if (ta) { ta.value = ''; ta.setAttribute('data-loaded', '0'); }
      setResult(res, 'ok', '已清除全部 Bot，恢复使用你的 TG 账号拉流。');
      loadBots(true);
    });
  }

  /* ======================================================================
   *  启动
   * ==================================================================== */
  function injectStyle() {
    var s = document.createElement('style');
    s.textContent = STYLE;
    document.head.appendChild(s);
  }

  function boot() {
    if (!document.body) { setTimeout(boot, 100); return; }
    injectStyle();
    buildUI();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
