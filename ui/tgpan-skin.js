/* ==========================================================================
 *  TGPan 百度网盘风格皮肤
 *
 *  做法：不改动 React 产物，纯 CSS 覆盖 + 轻量 JS 增强。
 *
 *  为什么这样做：
 *    Teldrive 前端是编译压缩后的 React 产物，源码不在手上。
 *    直接改压缩 JS 做布局重排风险极高（改错一个字符整页白屏）。
 *    CSS 覆盖 + 独立 JS 注入是安全、可逆、易维护的方式。
 *
 *  百度网盘视觉特征（本皮肤还原的重点）：
 *    1. 主色是蓝色（#06a7ff / #2b7de9），不是 Material 的紫色系
 *    2. 顶部有导航栏，左侧有分类树
 *    3. 文件列表是表格：复选框 + 图标 + 文件名 + 大小 + 修改时间 + 操作
 *    4. 列表行有斑马纹/悬浮高亮，表头吸顶
 *    5. 整体更紧凑，圆角更小（百度网盘偏方正）
 * ========================================================================== */

(function () {
  'use strict';

  if (window.__tgpanSkinInjected) return;
  window.__tgpanSkinInjected = true;

  /* ------------------------------------------------------------------------
   *  一、配色：把 Material 3 的色板重定义为「百度蓝」
   *
   *  这些是 Tailwind 通过 CSS 变量注入的色值（--color-xxx）。
   *  直接覆盖变量即可全局换色，不需要针对每个组件写规则。
   * ---------------------------------------------------------------------- */
  var SKIN_CSS = [
    /* ---- 亮色主题：百度蓝 ---- */
    ':root{',
    '  --color-primary:#2b7de9;',
    '  --color-on-primary:#ffffff;',
    '  --color-primary-container:#e1f0ff;',
    '  --color-on-primary-container:#0b4da2;',
    '  --color-secondary:#5b8def;',
    '  --color-on-secondary:#ffffff;',
    '  --color-secondary-container:#eaf2ff;',
    '  --color-on-secondary-container:#1a4f9c;',
    '  --color-surface:#ffffff;',
    '  --color-on-surface:#1a1a1a;',
    '  --color-surface-variant:#f5f7fa;',
    '  --color-on-surface-variant:#4a5568;',
    '  --color-surface-container:#ffffff;',
    '  --color-surface-container-low:#fafbfc;',
    '  --color-surface-container-high:#f0f2f5;',
    '  --color-surface-container-highest:#e8eaed;',
    '  --color-outline:#d0d7de;',
    '  --color-outline-variant:#e4e8ec;',
    '  --color-background:#f2f4f7;',
    '  --color-on-background:#1a1a1a;',
    '  --color-inverse-surface:#2c3138;',
    '  --color-inverse-on-surface:#f0f2f5;',
    '  --color-inverse-primary:#8bb8f5;',
    '}',

    /* ---- 深色主题 ---- */
    '.dark, [data-theme="dark"]{',
    '  --color-primary:#4a9eff;',
    '  --color-on-primary:#00203d;',
    '  --color-primary-container:#0a3d78;',
    '  --color-on-primary-container:#cfe4ff;',
    '  --color-secondary:#7fb2ff;',
    '  --color-secondary-container:#1a3a63;',
    '  --color-on-secondary-container:#cfe0ff;',
    '  --color-surface:#1c1f24;',
    '  --color-on-surface:#e8eaed;',
    '  --color-surface-variant:#262a30;',
    '  --color-on-surface-variant:#a8b0ba;',
    '  --color-surface-container:#1c1f24;',
    '  --color-surface-container-low:#212429;',
    '  --color-surface-container-high:#2a2e35;',
    '  --color-surface-container-highest:#33383f;',
    '  --color-outline:#3d434b;',
    '  --color-outline-variant:#2f343a;',
    '  --color-background:#14161a;',
    '  --color-on-background:#e8eaed;',
    '  --color-inverse-surface:#e8eaed;',
    '  --color-inverse-on-surface:#1c1f24;',
    '}',

    /* ---- 二、整体观感：更方正、更紧凑 ---- */
    'body{',
    '  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",',
    '    "Hiragino Sans GB","Microsoft YaHei",Roboto,sans-serif !important;',
    '  background:var(--color-background) !important;',
    '}',
    /* 百度网盘圆角偏小 */
    '.rounded-xl{border-radius:8px !important}',
    '.rounded-2xl{border-radius:10px !important}',
    '.rounded-lg{border-radius:7px !important}',

    /* ---- 三、侧边导航：百度网盘那种蓝色高亮 ---- */
    'nav a[data-hover="true"], nav button[data-hover="true"]{',
    '  background:var(--color-primary-container) !important;',
    '}',
    /* 选中态：左侧蓝色竖条（百度网盘的标志性设计） */
    'a[aria-current="page"]{',
    '  position:relative;',
    '  background:var(--color-primary-container) !important;',
    '  color:var(--color-on-primary-container) !important;',
    '  font-weight:600 !important;',
    '}',
    'a[aria-current="page"]::before{',
    '  content:"";',
    '  position:absolute;',
    '  left:0; top:20%; bottom:20%;',
    '  width:3px;',
    '  background:var(--color-primary);',
    '  border-radius:0 3px 3px 0;',
    '}',

    /* ---- 四、文件列表：表格化（百度网盘的核心观感） ---- */
    /* 表头吸顶 + 灰底 */
    'thead, [role="rowgroup"]:first-child [role="row"],',
    'div[data-tgpan-thead]{',
    '  position:sticky; top:0; z-index:5;',
    '  background:var(--color-surface-container-low);',
    '  border-bottom:1px solid var(--color-outline-variant);',
    '  font-weight:600;',
    '}',

    /* 行悬浮高亮 —— 百度网盘最明显的手感 */
    '[role="row"]:hover, tr:hover,',
    'div[data-tgpan-row]:hover{',
    '  background:var(--color-primary-container) !important;',
    '  transition:background .12s ease;',
    '}',
    /* 表格行分隔线，更像传统网盘 */
    '[role="row"], tr{',
    '  border-bottom:1px solid var(--color-outline-variant);',
    '}',

    /* ---- 五、按钮：百度网盘的主按钮是实心蓝、圆角小 ---- */
    'button.bg-primary, button[data-primary="true"]{',
    '  border-radius:6px !important;',
    '  font-weight:500 !important;',
    '}',

    /* ---- 六、输入框：更方正 ---- */
    'input, textarea, select{',
    '  border-radius:6px !important;',
    '}',

    /* ---- 七、滚动条：细一些，接近系统观感 ---- */
    '::-webkit-scrollbar{width:10px;height:10px}',
    '::-webkit-scrollbar-track{background:transparent}',
    '::-webkit-scrollbar-thumb{',
    '  background:var(--color-outline);',
    '  border-radius:5px;',
    '  border:2px solid transparent;',
    '  background-clip:content-box;',
    '}',
    '::-webkit-scrollbar-thumb:hover{background:var(--color-on-surface-variant);background-clip:content-box}',

    /* ---- 八、TGPan 自建控件的统一主题色 ---- */
    '#tgpan-fab{',
    '  background:var(--color-primary) !important;',
    '  border-radius:8px !important;',
    '  box-shadow:0 4px 14px rgba(43,125,233,.35) !important;',
    '}',
    '#tgpan-fab:hover{',
    '  box-shadow:0 8px 22px rgba(43,125,233,.45) !important;',
    '}',
    '.tgpan-btn.primary{background:var(--color-primary) !important;border-radius:6px !important}',
    '.tgpan-btn.ghost{background:var(--color-surface-container-high) !important;',
    '  color:var(--color-on-surface) !important;border-radius:6px !important}',

    /* 深色模式下自建弹窗也要跟着变 */
    '.dark #tgpan-box, [data-theme="dark"] #tgpan-box{',
    '  background:var(--color-surface-container-high) !important;',
    '  color:var(--color-on-surface) !important;',
    '}',
    '.dark #tgpan-box h3, [data-theme="dark"] #tgpan-box h3{color:var(--color-on-surface) !important}',
    '.dark .tgpan-field label, [data-theme="dark"] .tgpan-field label{color:var(--color-on-surface) !important}',
    '.dark .tgpan-field input, [data-theme="dark"] .tgpan-field input{',
    '  background:var(--color-surface-container) !important;',
    '  color:var(--color-on-surface) !important;',
    '  border-color:var(--color-outline) !important;',
    '}',
    '.dark #tgpan-help, [data-theme="dark"] #tgpan-help{',
    '  background:var(--color-surface-container) !important;',
    '  color:var(--color-on-surface-variant) !important;',
    '}',
    '.dark #tgpan-help b, [data-theme="dark"] #tgpan-help b{color:var(--color-on-surface) !important}',
    '.dark #tgpan-help code, [data-theme="dark"] #tgpan-help code{',
    '  background:var(--color-surface-container-highest) !important;',
    '  color:var(--color-on-surface) !important;',
    '}',
  ].join('\n');

  /* ------------------------------------------------------------------------
   *  二、标签页标题汉化（浏览器标签）
   * ---------------------------------------------------------------------- */
  function fixTitle() {
    // 原版标题是 "Teldrive"，我们已在 index.html 改成 TGPan
    // 这里再把 og:title 之类也统一
    var meta = document.querySelector('meta[property="og:title"]');
    if (meta) meta.setAttribute('content', 'TGPan 云盘');
  }

  /* ------------------------------------------------------------------------
   *  三、给文件列表打标记，方便 CSS 精准命中
   *
   *  原版用 role="row" 的表格结构，这里加个属性便于选择器定位，
   *  同时不影响原有逻辑。
   * ---------------------------------------------------------------------- */
  function tagRows() {
    var rows = document.querySelectorAll('[role="row"], tr');
    for (var i = 0; i < rows.length; i++) {
      if (!rows[i].hasAttribute('data-tgpan-row')) {
        rows[i].setAttribute('data-tgpan-row', '1');
      }
    }
  }

  /* ------------------------------------------------------------------------
   *  四、启动
   * ---------------------------------------------------------------------- */
  function injectStyle() {
    var s = document.createElement('style');
    s.id = 'tgpan-skin-style';
    s.textContent = SKIN_CSS;
    document.head.appendChild(s);
  }

  function boot() {
    injectStyle();
    fixTitle();

    // 列表是异步渲染的，用 MutationObserver 持续标记
    var root = document.getElementById('root') || document.body;
    var obs = new MutationObserver(function () {
      tagRows();
    });
    obs.observe(root, { childList: true, subtree: true });
    tagRows();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
