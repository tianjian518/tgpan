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

  var STYLE = [
    '#tgpan-fab{position:fixed;right:24px;bottom:24px;z-index:99998;',
    'background:' + ACCENT + ';color:#fff;border:none;',
    'border-radius:8px;padding:13px 20px;font-size:15px;font-weight:500;cursor:pointer;',
    'box-shadow:0 4px 14px rgba(43,125,233,.4);display:flex;align-items:center;gap:8px;',
    'transition:transform .15s,box-shadow .15s;font-family:inherit}',
    '#tgpan-fab:hover{transform:translateY(-2px);box-shadow:0 8px 22px rgba(43,125,233,.5)}',

    '#tgpan-mask{position:fixed;inset:0;background:rgba(0,0,0,.5);z-index:99999;',
    'display:none;align-items:center;justify-content:center;padding:16px}',
    '#tgpan-mask.show{display:flex}',
    '#tgpan-box{background:var(--color-surface-container,#fff);',
    'color:var(--color-on-surface,#111);',
    'border-radius:12px;width:100%;max-width:680px;',
    'max-height:88vh;overflow:hidden;display:flex;flex-direction:column;',
    'box-shadow:0 20px 60px rgba(0,0,0,.28);',
    'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}',

    '#tgpan-head{padding:20px 24px 0;flex-shrink:0}',
    '#tgpan-head h3{margin:0 0 4px;font-size:19px;font-weight:600;display:flex;align-items:center;gap:8px}',
    '#tgpan-head .sub{color:var(--color-on-surface-variant,#888);font-size:13px;line-height:1.6}',

    '#tgpan-tabs{display:flex;gap:4px;margin:16px 0 0;border-bottom:1px solid var(--color-outline-variant,#eee);',
    'padding:0 24px;flex-shrink:0;overflow-x:auto}',
    '.tgpan-tab{padding:9px 14px;font-size:14px;cursor:pointer;border:none;background:none;',
    'color:var(--color-on-surface-variant,#666);font-family:inherit;white-space:nowrap;',
    'border-bottom:2px solid transparent;margin-bottom:-1px;transition:color .15s}',
    '.tgpan-tab:hover{color:' + ACCENT + '}',
    '.tgpan-tab.active{color:' + ACCENT + ';border-bottom-color:' + ACCENT + ';font-weight:600}',

    '#tgpan-body{padding:20px 24px;overflow-y:auto;flex:1}',
    '.tgpan-pane{display:none}',
    '.tgpan-pane.active{display:block}',

    '.tgpan-field{margin-bottom:15px}',
    '.tgpan-field label{display:block;font-size:13px;font-weight:600;margin-bottom:6px}',
    '.tgpan-field .hint{font-weight:400;color:var(--color-on-surface-variant,#999);font-size:12px}',
    '.tgpan-field input{width:100%;box-sizing:border-box;padding:10px 13px;',
    'border:1.5px solid var(--color-outline,#e0e0e0);border-radius:6px;font-size:14px;',
    'outline:none;transition:border-color .15s;font-family:inherit;',
    'background:var(--color-surface,#fff);color:var(--color-on-surface,#111)}',
    '.tgpan-field input:focus{border-color:' + ACCENT + '}',

    '.tgpan-switch{display:inline-flex;align-items:center;gap:8px;cursor:pointer;user-select:none}',
    '.tgpan-switch input{display:none}',
    '.tgpan-switch .track{width:38px;height:21px;border-radius:11px;background:#ccc;',
    'position:relative;transition:background .2s;flex:0 0 auto}',
    '.tgpan-switch .track::after{content:"";position:absolute;top:2px;left:2px;width:17px;height:17px;',
    'border-radius:50%;background:#fff;transition:transform .2s;box-shadow:0 1px 3px rgba(0,0,0,.2)}',
    '.tgpan-switch input:checked+.track{background:' + ACCENT + '}',
    '.tgpan-switch input:checked+.track::after{transform:translateX(17px)}',

    '.tgpan-btn{padding:10px 18px;border-radius:6px;border:none;font-size:14px;font-weight:500;',
    'cursor:pointer;font-family:inherit;transition:opacity .15s}',
    '.tgpan-btn:hover{opacity:.86}',
    '.tgpan-btn:disabled{opacity:.5;cursor:not-allowed}',
    '.tgpan-btn.primary{background:' + ACCENT + ';color:#fff}',
    '.tgpan-btn.ghost{background:var(--color-surface-container-high,#f0f0f0);',
    'color:var(--color-on-surface,#444)}',
    '.tgpan-btn.danger{background:#e64545;color:#fff}',
    '.tgpan-btn.sm{padding:7px 13px;font-size:13px}',
    '#tgpan-actions{display:flex;gap:10px;margin-top:20px}',
    '#tgpan-actions .tgpan-btn{flex:1}',

    '.tgpan-result{margin-top:16px;padding:13px;border-radius:8px;font-size:13px;',
    'line-height:1.75;display:none;white-space:pre-wrap;word-break:break-word}',
    '.tgpan-result.ok{background:#eafaf0;color:#0a7a3d;border:1px solid #b7e6c9;display:block}',
    '.tgpan-result.err{background:#fdecec;color:#b3261e;border:1px solid #f5c2c0;display:block}',
    '.tgpan-result.load{background:#eef7fe;color:#1a73b5;border:1px solid #c4e2f7;display:block}',

    '.tgpan-help{margin-top:14px;padding:12px 14px;background:var(--color-surface-container-low,#f8f9fa);',
    'border-radius:8px;font-size:12.5px;color:var(--color-on-surface-variant,#666);line-height:1.85}',
    '.tgpan-help b{color:var(--color-on-surface,#333)}',
    '.tgpan-help code{background:var(--color-surface-container-high,#e8eaed);padding:1px 6px;',
    'border-radius:4px;font-size:11.5px;font-family:ui-monospace,Menlo,Consolas,monospace}',

    '.tgpan-ch{display:flex;align-items:center;gap:12px;padding:13px 14px;border-radius:8px;',
    'border:1px solid var(--color-outline-variant,#eaeaea);margin-bottom:9px;',
    'background:var(--color-surface-container-low,#fafbfc)}',
    '.tgpan-ch .info{flex:1;min-width:0}',
    '.tgpan-ch .name{font-size:14px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    '.tgpan-ch .meta{font-size:12px;color:var(--color-on-surface-variant,#888);margin-top:3px;line-height:1.6}',
    '.tgpan-ch .err{font-size:12px;color:#b3261e;margin-top:3px;line-height:1.5}',
    '.tgpan-ch .ops{display:flex;gap:6px;flex:0 0 auto}',

    '.tgpan-cred{display:flex;align-items:center;gap:12px;padding:13px 14px;border-radius:8px;',
    'border:1px solid var(--color-outline-variant,#eaeaea);margin-bottom:9px;',
    'background:var(--color-surface-container-low,#fafbfc)}',
    '.tgpan-cred .info{flex:1;min-width:0}',
    '.tgpan-cred .uname{font-size:14px;font-weight:600;font-family:ui-monospace,Menlo,Consolas,monospace}',
    '.tgpan-cred .meta{font-size:12px;color:var(--color-on-surface-variant,#888);margin-top:3px}',

    '.tgpan-pwbox{margin-top:14px;padding:14px;border-radius:8px;background:#fff8e6;',
    'border:1.5px dashed #ffc107}',
    '.tgpan-pwbox .t{font-size:13px;font-weight:600;color:#8a6100;margin-bottom:10px}',
    '.tgpan-pwbox .v{display:flex;gap:8px;align-items:center;margin-bottom:8px}',
    '.tgpan-pwbox .v code{flex:1;background:#fff;padding:9px 11px;border-radius:6px;',
    'font-family:ui-monospace,Menlo,Consolas,monospace;font-size:13px;',
    'border:1px solid #ffd75e;word-break:break-all;color:#333}',
    '.tgpan-pwbox .n{font-size:12px;color:#8a6100;line-height:1.6}',

    '.tgpan-mount{margin-top:12px;border:1px solid var(--color-outline-variant,#eaeaea);border-radius:8px;overflow:hidden}',
    '.tgpan-mount .r{display:flex;border-bottom:1px solid var(--color-outline-variant,#eaeaea)}',
    '.tgpan-mount .r:last-child{border-bottom:none}',
    '.tgpan-mount .k{flex:0 0 96px;padding:10px 13px;font-size:13px;font-weight:600;',
    'background:var(--color-surface-container-low,#fafbfc);color:var(--color-on-surface-variant,#666)}',
    '.tgpan-mount .v{flex:1;padding:10px 13px;font-size:13px;',
    'font-family:ui-monospace,Menlo,Consolas,monospace;word-break:break-all;display:flex;align-items:center}',

    '#tgpan-empty{text-align:center;padding:36px 16px;font-size:13.5px;',
    'color:var(--color-on-surface-variant,#999);line-height:1.9}',

    '@media(max-width:600px){',
    '#tgpan-fab{right:14px;bottom:14px;padding:11px 16px;font-size:14px}',
    '#tgpan-head,#tgpan-tabs{padding-left:16px;padding-right:16px}',
    '#tgpan-body{padding:16px}',
    '.tgpan-mount .k{flex:0 0 74px}',
    '.tgpan-ch{flex-wrap:wrap}',
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
      '    <div id="tgpan-tabs">',
      '      <button class="tgpan-tab active" data-tab="scan">📡 扫描频道</button>',
      '      <button class="tgpan-tab" data-tab="auto">🔄 自动扫描</button>',
      '      <button class="tgpan-tab" data-tab="webdav">🔗 WebDAV</button>',
      '      <button class="tgpan-tab" data-tab="help">💡 帮助</button>',
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
      '      <div id="tgpan-actions">',
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
    for (var i = 0; i < tabs.length; i++) {
      tabs[i].addEventListener('click', function () {
        var name = this.getAttribute('data-tab');
        for (var j = 0; j < tabs.length; j++) tabs[j].classList.remove('active');
        this.classList.add('active');
        var panes = mask.querySelectorAll('.tgpan-pane');
        for (var k = 0; k < panes.length; k++) {
          panes[k].classList.toggle('active', panes[k].getAttribute('data-pane') === name);
        }
        if (name === 'auto') loadChannels();
        if (name === 'webdav') loadCredentials();
      });
    }

    $('#tgpan-close', mask).onclick = closeModal;
    $('#tgpan-start', mask).onclick = doScan;
    $('#tgpan-auto-refresh', mask).onclick = function () { loadChannels(true); };
    $('#tgpan-cred-new', mask).onclick = createCredential;
    $('#tgpan-cred-refresh', mask).onclick = function () { loadCredentials(true); };
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
    var channelId = parseInt(chanRaw.replace(/[^0-9-]/g, ''), 10);
    if (!channelId || isNaN(channelId)) {
      setResult(res, 'err', '⚠️ 频道 ID 格式不对，应该是纯数字（可带负号）');
      return;
    }

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
  function loadChannels(force) {
    var list = document.getElementById('tgpan-ch-list');
    if (!list) return;
    if (!force && list.getAttribute('data-loaded') === '1') return;

    list.innerHTML = '<div id="tgpan-empty">正在加载…</div>';

    api('GET', '/scan/channels').then(function (r) {
      if (!r.ok) {
        list.innerHTML = '<div id="tgpan-empty">加载失败：' +
          esc((r.data && r.data.message) || ('HTTP ' + r.status)) + '</div>';
        return;
      }
      var d = r.data || {};
      var chs = d.channels || [];
      list.setAttribute('data-loaded', '1');

      if (!chs.length) {
        list.innerHTML = '<div id="tgpan-empty">' +
          '还没有登记任何频道。<br>去「📡 扫描频道」页签扫一个，' +
          '并勾选「加入自动扫描」。</div>';
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
          '<div class="tgpan-ch" data-cid="' + c.channelId + '">' +
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
      list.innerHTML = '<div id="tgpan-empty">请求出错：' + esc(String(e)) + '</div>';
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
      var nowEnabled = btn.textContent.trim() === '暂停';
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
   *  功能三：WebDAV 凭据管理
   * ==================================================================== */
  function loadCredentials(force) {
    var list = document.getElementById('tgpan-cred-list');
    if (!list) return;
    if (!force && list.getAttribute('data-loaded') === '1') return;

    list.innerHTML = '<div id="tgpan-empty">正在加载…</div>';

    api('GET', '/webdav/credentials').then(function (r) {
      if (!r.ok) {
        list.innerHTML = '<div id="tgpan-empty">加载失败：' +
          esc((r.data && r.data.message) || ('HTTP ' + r.status)) + '</div>';
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
        list.innerHTML = '<div id="tgpan-empty">' +
          '还没有生成过密码。<br>点上面的「生成 WebDAV 密码」按钮创建一个。</div>';
        return;
      }

      var html = [];
      creds.forEach(function (c) {
        html.push(
          '<div class="tgpan-cred" data-id="' + c.id + '">' +
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
      list.innerHTML = '<div id="tgpan-empty">请求出错：' + esc(String(e)) + '</div>';
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
