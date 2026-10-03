/* ==========================================================================
 *  TGPan 频道扫描插件（前端注入）
 *
 *  作用：在 Teldrive 网盘界面右下角加一个「扫描 TG 频道」按钮。
 *        点开后填入频道 ID，就能把频道里已有的视频导入网盘。
 *
 *  原理：调用后端自定义接口 POST /api/scan/channel
 * ========================================================================== */
(function () {
  'use strict';

  // 避免重复注入
  if (window.__tgpanScanInjected) return;
  window.__tgpanScanInjected = true;

  var STYLE = [
    '#tgpan-fab{position:fixed;right:24px;bottom:24px;z-index:99998;',
    'background:linear-gradient(135deg,#2AABEE,#229ED9);color:#fff;border:none;',
    'border-radius:50px;padding:14px 22px;font-size:15px;font-weight:600;cursor:pointer;',
    'box-shadow:0 6px 20px rgba(42,171,238,.45);display:flex;align-items:center;gap:8px;',
    'transition:transform .2s,box-shadow .2s;font-family:inherit}',
    '#tgpan-fab:hover{transform:translateY(-2px);box-shadow:0 10px 26px rgba(42,171,238,.55)}',
    '#tgpan-mask{position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:99999;',
    'display:none;align-items:center;justify-content:center;padding:16px}',
    '#tgpan-mask.show{display:flex}',
    '#tgpan-box{background:#fff;border-radius:16px;width:100%;max-width:520px;',
    'max-height:90vh;overflow:auto;padding:26px;box-shadow:0 20px 60px rgba(0,0,0,.3);',
    'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif}',
    '#tgpan-box h3{margin:0 0 6px;font-size:20px;color:#111}',
    '#tgpan-box .sub{color:#888;font-size:13px;margin-bottom:20px;line-height:1.6}',
    '.tgpan-field{margin-bottom:16px}',
    '.tgpan-field label{display:block;font-size:13px;font-weight:600;color:#333;margin-bottom:6px}',
    '.tgpan-field .hint{font-weight:400;color:#999;font-size:12px}',
    '.tgpan-field input{width:100%;box-sizing:border-box;padding:11px 14px;border:1.5px solid #e0e0e0;',
    'border-radius:9px;font-size:14px;outline:none;transition:border-color .2s;font-family:inherit}',
    '.tgpan-field input:focus{border-color:#2AABEE}',
    '#tgpan-actions{display:flex;gap:10px;margin-top:22px}',
    '.tgpan-btn{flex:1;padding:12px;border-radius:9px;border:none;font-size:15px;font-weight:600;',
    'cursor:pointer;font-family:inherit;transition:opacity .2s}',
    '.tgpan-btn:hover{opacity:.88}',
    '.tgpan-btn.primary{background:#2AABEE;color:#fff}',
    '.tgpan-btn.ghost{background:#f0f0f0;color:#444}',
    '.tgpan-btn:disabled{opacity:.5;cursor:not-allowed}',
    '#tgpan-result{margin-top:18px;padding:14px;border-radius:10px;font-size:13px;',
    'line-height:1.7;display:none;white-space:pre-wrap;word-break:break-word}',
    '#tgpan-result.ok{background:#eafaf0;color:#0a7a3d;border:1px solid #b7e6c9;display:block}',
    '#tgpan-result.err{background:#fdecec;color:#b3261e;border:1px solid #f5c2c0;display:block}',
    '#tgpan-result.load{background:#eef7fe;color:#1a73b5;border:1px solid #c4e2f7;display:block}',
    '#tgpan-help{margin-top:14px;padding:12px;background:#f8f9fa;border-radius:9px;',
    'font-size:12px;color:#666;line-height:1.8}',
    '#tgpan-help b{color:#333}',
    '#tgpan-help code{background:#e8eaed;padding:1px 6px;border-radius:4px;font-size:11px}',
    '@media(max-width:600px){#tgpan-fab{right:14px;bottom:14px;padding:12px 18px;font-size:14px}}'
  ].join('');

  function injectStyle() {
    var s = document.createElement('style');
    s.textContent = STYLE;
    document.head.appendChild(s);
  }

  function buildUI() {
    // 悬浮按钮
    var fab = document.createElement('button');
    fab.id = 'tgpan-fab';
    fab.innerHTML = '<span style="font-size:18px">📡</span><span>扫描 TG 频道</span>';
    fab.onclick = function () { openModal(); };
    document.body.appendChild(fab);

    // 弹窗
    var mask = document.createElement('div');
    mask.id = 'tgpan-mask';
    mask.innerHTML = [
      '<div id="tgpan-box">',
      '  <h3>📡 扫描 TG 频道</h3>',
      '  <div class="sub">把一个 Telegram 频道里的视频，直接变成网盘里的文件夹。<br>不复制文件、不占空间、不吃流量。</div>',
      '  <div class="tgpan-field">',
      '    <label>频道 ID <span class="hint">（必填）</span></label>',
      '    <input id="tgpan-channel" placeholder="例如 -1001234567890 或 1234567890" autocomplete="off">',
      '  </div>',
      '  <div class="tgpan-field">',
      '    <label>网盘文件夹名 <span class="hint">（选填，默认用频道标题）</span></label>',
      '    <input id="tgpan-folder" placeholder="例如 我的电影" autocomplete="off">',
      '  </div>',
      '  <div class="tgpan-field">',
      '    <label>最多扫描消息数 <span class="hint">（选填，默认 2000）</span></label>',
      '    <input id="tgpan-limit" type="number" placeholder="2000" value="2000">',
      '  </div>',
      '  <div id="tgpan-help">',
      '    <b>💡 怎么获取频道 ID？</b><br>',
      '    方法一：在 Telegram 里打开频道，随便转发一条消息到 <code>@userinfobot</code>，它会告诉你 ID。<br>',
      '    方法二：网页版频道链接形如 <code>t.me/c/1234567890/1</code>，中间那串数字就是。',
      '  </div>',
      '  <div id="tgpan-result"></div>',
      '  <div id="tgpan-actions">',
      '    <button class="tgpan-btn ghost" id="tgpan-cancel">取消</button>',
      '    <button class="tgpan-btn primary" id="tgpan-start">开始扫描</button>',
      '  </div>',
      '</div>'
    ].join('\n');
    document.body.appendChild(mask);

    mask.addEventListener('click', function (e) {
      if (e.target === mask) closeModal();
    });
    document.getElementById('tgpan-cancel').onclick = closeModal;
    document.getElementById('tgpan-start').onclick = doScan;

    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && mask.classList.contains('show')) closeModal();
    });
  }

  function openModal() {
    var mask = document.getElementById('tgpan-mask');
    if (mask) mask.classList.add('show');
  }

  function closeModal() {
    var mask = document.getElementById('tgpan-mask');
    if (mask) mask.classList.remove('show');
  }

  function setResult(kind, text) {
    var el = document.getElementById('tgpan-result');
    if (!el) return;
    el.className = kind;
    el.textContent = text;
  }

  function humanSize(n) {
    if (!n) return '0 B';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(i === 0 ? 0 : 2) + ' ' + u[i];
  }

  function doScan() {
    var btn = document.getElementById('tgpan-start');
    var channelRaw = (document.getElementById('tgpan-channel').value || '').trim();
    var folder = (document.getElementById('tgpan-folder').value || '').trim();
    var limit = parseInt(document.getElementById('tgpan-limit').value, 10) || 2000;

    if (!channelRaw) {
      setResult('err', '⚠️ 请先填入频道 ID');
      return;
    }
    var channelId = parseInt(channelRaw.replace(/[^0-9-]/g, ''), 10);
    if (!channelId || isNaN(channelId)) {
      setResult('err', '⚠️ 频道 ID 格式不对，应该是纯数字（可带负号）');
      return;
    }

    btn.disabled = true;
    btn.textContent = '扫描中…';
    setResult('load', '⏳ 正在读取频道消息，请稍候…\n（消息多的话可能要一两分钟，别关这个窗口）');

    var body = { channelId: channelId, limit: limit };
    if (folder) body.folderName = folder;

    fetch('/api/scan/channel', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(body)
    })
      .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, status: r.status, data: j }; }); })
      .then(function (res) {
        var d = res.data || {};
        if (!res.ok) {
          setResult('err', '❌ 扫描失败\n' + (d.message || ('HTTP ' + res.status)));
          return;
        }
        var lines = [
          '✅ ' + (d.message || '扫描完成'),
          '',
          '📁 频道：' + (d.channelName || '-'),
          '📂 文件夹：' + (d.folderName || '-'),
          '🔍 扫描消息：' + (d.scanned || 0) + ' 条',
          '🎬 新导入：' + (d.imported || 0) + ' 个视频',
          '💾 总大小：' + humanSize(d.totalSize || 0),
          '⏭️ 跳过：' + (d.skipped || 0) + ' 条'
        ];
        if (d.imported > 0) {
          lines.push('');
          lines.push('👉 刷新页面（F5）就能看到新文件夹了');
        }
        setResult('ok', lines.join('\n'));
      })
      .catch(function (err) {
        setResult('err', '❌ 请求出错：' + (err && err.message ? err.message : String(err)));
      })
      .finally(function () {
        btn.disabled = false;
        btn.textContent = '开始扫描';
      });
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
