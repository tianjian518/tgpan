/*!
 * TGPan 主界面 — v2.7.0 重写版
 * ---------------------------------------------------------------------------
 *  为什么重写：原版前端是 React 编译产物（1MB，压缩混淆，无源码），
 *  只能整体替换。本文件是全新实现，纯原生 JS，零依赖、零构建。
 *
 *  只做两件事：
 *    1. 我的网盘 —— 一个个文件夹，每个文件夹 = 一个 TG 频道；点进去浏览/播放
 *    2. 系统设置 —— 文字菜单：扫描频道 / 自动扫描 / WebDAV / 剧集归档 / 关于
 *
 *  后端沿用原版 API（/api/files 等），播放流走 /api/files/{id}/{name}。
 * ---------------------------------------------------------------------------
 */
(function () {
  'use strict';

  var API = '/api';

  // MAX_LIMIT 后端 /files 接口的 limit 硬上限。
  // 传超过这个值 ogen 会直接 400（"value N greater than 1000"），
  // 所以这里必须卡住，不能想当然写 2000。
  var MAX_LIMIT = 1000;

  // =====================================================================
  //  工具
  // =====================================================================

  function el(tag, attrs, children) {
    var n = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        if (k === 'class') n.className = attrs[k];
        else if (k === 'text') n.textContent = attrs[k];
        else if (k === 'html') n.innerHTML = attrs[k];
        else if (k.indexOf('on') === 0) n.addEventListener(k.slice(2), attrs[k]);
        else if (attrs[k] !== null && attrs[k] !== undefined) n.setAttribute(k, attrs[k]);
      });
    }
    if (children) {
      (Array.isArray(children) ? children : [children]).forEach(function (c) {
        if (c === null || c === undefined || c === false) return;
        n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
      });
    }
    return n;
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function esc(s) {
    return String(s === null || s === undefined ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function fmtSize(bytes) {
    var b = Number(bytes);
    if (!b || b < 0) return '—';
    if (b < 1024) return b + ' B';
    var units = ['KB', 'MB', 'GB', 'TB'];
    var i = -1;
    do { b /= 1024; i++; } while (b >= 1024 && i < units.length - 1);
    return (b >= 100 ? b.toFixed(0) : b.toFixed(1)) + ' ' + units[i];
  }

  function fmtTime(iso) {
    if (!iso) return '—';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '—';
    var now = Date.now();
    var diff = (now - d.getTime()) / 1000;
    if (diff < 60) return '刚刚';
    if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前';
    if (diff < 86400) return Math.floor(diff / 3600) + ' 小时前';
    if (diff < 86400 * 7) return Math.floor(diff / 86400) + ' 天前';
    function p(n) { return n < 10 ? '0' + n : '' + n; }
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
  }

  function isVideo(name) {
    return /\.(mp4|mkv|avi|mov|wmv|flv|webm|m4v|ts|rmvb|rm|mpg|mpeg|3gp)$/i.test(name || '');
  }
  function isImage(name) {
    return /\.(jpg|jpeg|png|gif|webp|bmp|svg|heic)$/i.test(name || '');
  }
  function isAudio(name) {
    return /\.(mp3|flac|wav|aac|m4a|ogg|wma|ape)$/i.test(name || '');
  }

  // 内联 SVG 图标。
  //
  // 为什么不用 emoji：某些系统（尤其精简版 Linux 容器 / 部分安卓 WebView）
  // 没装彩色 emoji 字体，emoji 会渲染成"豆腐块"方框，看着像坏了。
  // SVG 走的是矢量描边，任何环境都一致。
  function svgIcon(path, size) {
    var s = size || 34;
    return '<svg width="' + s + '" height="' + s + '" viewBox="0 0 24 24" fill="none" '
      + 'stroke="currentColor" stroke-width="1.5" stroke-linecap="round" '
      + 'stroke-linejoin="round">' + path + '</svg>';
  }

  var ICONS = {
    folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"/>',
    key: '<circle cx="8" cy="15" r="4"/><path d="M10.8 12.2 20 3"/><path d="M17 6l3 3"/>',
    film: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 4v16M17 4v16M3 9h4M3 15h4M17 9h4M17 15h4"/>',
    file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5z"/><path d="M14 3v5h5"/>',
    image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="M3 17l5-5 4 4 3-3 6 6"/>',
    music: '<path d="M9 18V6l10-2v12"/><circle cx="6" cy="18" r="3"/><circle cx="16" cy="16" r="3"/>',
  };

  function iconSvg(kind, size) {
    return svgIcon(ICONS[kind] || ICONS.file, size);
  }

  function fileKind(name) {
    if (isVideo(name)) return 'video';
    if (isImage(name)) return 'image';
    if (isAudio(name)) return 'audio';
    return 'other';
  }

  // ---- 请求 ----
  function req(path, opts) {
    opts = opts || {};
    var url = API + path;
    return fetch(url, {
      method: opts.method || 'GET',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: opts.body ? JSON.stringify(opts.body) : undefined
    }).then(function (r) {
      return r.text().then(function (t) {
        var j = {};
        try { j = t ? JSON.parse(t) : {}; } catch (e) { j = { _raw: t }; }
        if (!r.ok) {
          var msg = j.message || j.error || ('HTTP ' + r.status);
          var err = new Error(msg);
          err.status = r.status;
          err.body = j;
          throw err;
        }
        return j;
      });
    });
  }

  // ---- 提示 ----
  //
  // 分级超时：一条"已保存"不需要霸屏 2.6 秒，而一条"扫描失败：TG 限流"
  // 一闪而过用户根本来不及看。所以按级别给不同停留时间。
  //
  //   默认/成功  2.4s   操作已生效，扫一眼就够
  //   警告       4s     需要留意但不用操作
  //   错误       7s     要看清楚说了什么，可能还要照着做
  //
  // 另外两个细节：
  //   1. 鼠标悬上去就暂停倒计时 —— 用户正在读，不能被抽走
  //   2. 同一条消息连点多次只重置计时，不叠加一堆 toast
  //      （连点"移出"按钮曾经会叠出七八个提示）
  var TOAST_MS = { info: 2400, ok: 2400, warn: 4000, err: 7000 };

  // 计时器挂在**节点自己身上**，不用全局变量。
  //
  // 为什么：全局单例会有下面这个竞态 ——
  //   toast('A') -> 悬停 -> 移开（起 1.2s 销毁计时）-> 立刻 toast('B')
  //   -> 1.2s 后 A 的计时触发 dismissToast(A)，里面清掉了"全局计时器"
  //   -> 而那个全局计时器其实已经被 B 用了 -> B 永远不会消失，卡在屏幕上。
  //
  // 挂在自己身上，A 的销毁只可能动 A 的计时，A 和 B 互不干扰。
  var toastHover = false;

  function toast(msg, ms, level) {
    var lv = level || 'info';
    if (!ms) ms = TOAST_MS[lv] || 2600;

    var old = document.getElementById('tp-toast');
    if (old) {
      // 同一条消息：只重置计时，不重建节点（避免闪烁）
      if (old.getAttribute('data-msg') === String(msg)) {
        if (old._tpTimer) clearTimeout(old._tpTimer);
        if (!toastHover) scheduleToastDismiss(old, ms);
        return;
      }
      // 换了一条消息：把旧节点连同它的计时一起清掉
      if (old._tpTimer) { clearTimeout(old._tpTimer); old._tpTimer = null; }
      if (old.parentNode) old.parentNode.removeChild(old);
    }

    var t = el('div', {
      class: 'tp-toast tp-toast-' + lv,
      id: 'tp-toast',
      text: msg
    });
    t.setAttribute('data-msg', String(msg));
    document.body.appendChild(t);

    // 悬停读的时候别抽走
    t.onmouseenter = function () {
      toastHover = true;
      if (t._tpTimer) { clearTimeout(t._tpTimer); t._tpTimer = null; }
    };
    t.onmouseleave = function () {
      toastHover = false;
      scheduleToastDismiss(t, 1200); // 移开后留 1.2 秒收尾
    };
    t.onclick = function () { dismissToast(t); };

    scheduleToastDismiss(t, ms);
  }

  function scheduleToastDismiss(node, ms) {
    if (!node) return;
    if (node._tpTimer) clearTimeout(node._tpTimer);
    node._tpTimer = setTimeout(function () {
      node._tpTimer = null;
      dismissToast(node);
    }, ms);
  }

  function dismissToast(node) {
    if (!node) return;
    if (node._tpTimer) { clearTimeout(node._tpTimer); node._tpTimer = null; }
    if (node.parentNode) {
      node.classList.add('tp-toast-out');
      // 记下节点引用，避免这 160ms 里用户又弹了新 toast、
      // 而这里的闭包把新节点误删（按 id 查会查到新的那个）。
      setTimeout(function () {
        if (node.parentNode) node.parentNode.removeChild(node);
      }, 160);
    }
  }

  // 语义化包装，调用点读起来更清楚，也顺手保证级别传对
  function toastOk(msg, ms) { toast(msg, ms, 'ok'); }
  function toastWarn(msg, ms) { toast(msg, ms, 'warn'); }
  function toastErr(msg, ms) { toast(msg, ms, 'err'); }

  function copyText(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text).then(function () {
        toastOk('已复制');
      }).catch(function () { fallbackCopy(text); });
    }
    fallbackCopy(text);
  }

  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); toastOk('已复制'); }
    catch (e) { toastErr('复制失败，请手动选中'); }
    document.body.removeChild(ta);
  }

  // =====================================================================
  //  自动刷新 —— 但不能打断用户
  // =====================================================================
  //
  // 后台一直在跑自动扫描，网盘内容随时可能变多。理想情况下列表应该自己更新。
  // 但"自己更新"做不好就是灾难：
  //
  //   · 用户正在输网址/搜索，重绘一次输入框就清空了
  //   · 用户正在看某个文件的详情，列表一重排就不知道刚才点到哪
  //   · 用户正要拖动滚动条，内容高度突然变了
  //
  // 所以规则是：**只要用户最近动过，就不刷新**。
  // 宁可晚几秒更新，也不能把用户正在做的事打断。
  var lastActive = Date.now();
  var IDLE_MS = 30000;   // 30 秒内有操作就算"正在用"
  var AUTO_MS = 60000;   // 每分钟检查一次要不要刷

  function markActive() { lastActive = Date.now(); }

  // 用捕获阶段监听，任何输入/点击/滚动/按键都算"用户还在"
  ['mousedown', 'keydown', 'touchstart', 'scroll', 'input', 'wheel'].forEach(function (ev) {
    document.addEventListener(ev, markActive, true);
  });

  function isUserIdle() {
    if (document.hidden) return false;   // 页面在后台，不动它（省请求）
    return Date.now() - lastActive >= IDLE_MS;
  }

  // 页签切回前台时，如果数据已经"陈旧"就补一次刷新。
  //
  // 为什么盯着 visibilitychange：用户切出去看别的、回来时，
  // 内容可能已经变了。这时候刷新是符合预期的（他刚回来，没有正在进行的操作）。
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) return;
    if (Date.now() - lastActive > IDLE_MS) {
      lastActive = Date.now();
      silentRefresh();
    }
  });

  setInterval(function () {
    if (!isUserIdle()) return;
    silentRefresh();
  }, AUTO_MS);

  // silentRefresh 不显示 loading、不动滚动位置，只在数据真的变了时重绘。
  function silentRefresh() {
    if (state.tab !== 'drive') return;
    if (state.loading) return;         // 正有一次加载在飞，别叠加
    if (isModalOpen()) return;         // 有弹窗/播放器开着，绝不能动 DOM
    doLoad(state.cwd.length ? state.cwd[state.cwd.length - 1].id : null, true);
  }

  function isModalOpen() {
    // 播放器（.tp-player-wrap/.tp-player）和确认框（.tp-modal）一旦开着，
    // 底下那张列表就绝对不能重绘 —— 用户正看着呢。
    return !!document.querySelector('.tp-player-wrap, .tp-player, .tp-modal, .tp-overlay, video');
  }

  // =====================================================================
  //  状态
  // =====================================================================
  var state = {
    tab: 'drive',          // drive | settings
    cwd: [],               // 面包屑栈：[{id,name}]，空 = 根目录
    files: [],
    loading: false,
    settingsPane: 'scan',
    channels: null,        // 频道扫描登记列表（缓存）
    series: null,          // 剧集列表（缓存）
    creds: null,           // WebDAV 凭据（缓存）
    dialogs: null,         // 「已关注频道」列表（扫描页用，来自 tg_dialogs 缓存表）
    dialogsSyncedAt: 0,    // 上次拿到 dialogs 的时刻（用于判断要不要重新拉）
    rootParentId: null     // 根目录的 parentId（用于「我的网盘」只显示频道文件夹）
  };

  var app = document.getElementById('app');

  // =====================================================================
  //  顶栏
  // =====================================================================
  function renderHeader() {
    var nav = el('nav', { class: 'tp-nav' });
    [
      ['drive', '我的网盘'],
      ['settings', '系统设置']
    ].forEach(function (item) {
      nav.appendChild(el('button', {
        class: 'tp-nav-item' + (state.tab === item[0] ? ' is-active' : ''),
        text: item[1],
        onclick: function () { go(item[0]); }
      }));
    });

    var right = el('div', { class: 'tp-header-right' }, [
      el('span', { class: 'tp-user', id: 'tp-user', text: '' }),
      el('button', {
        class: 'tp-btn sm', text: '退出',
        onclick: function () { doLogout(); }
      })
    ]);

    return el('header', { class: 'tp-header' }, [
      el('div', { class: 'tp-logo' }, [
        el('span', { class: 'tp-logo-mark', text: 'T' }),
        el('span', { class: 'tp-logo-text', text: 'TGPan' })
      ]),
      nav,
      el('div', { class: 'tp-nav-flex' }),
      right
    ]);
  }

  function loadUser() {
    // 用户名从闸门状态里取（/gate/status 已带 name / user）。
    //
    // 为什么不调 /users/profile/{name}：那个接口要先知道自己的用户名，
    // 而「我是谁」这件事本来就得先问一次。闸门状态里现成有，省一次请求。
    req('/gate/status').then(function (st) {
      var name = (st && (st.name || st.user)) || '';
      var u = document.getElementById('tp-user');
      if (u && name) u.textContent = name;
    }).catch(function () { /* 拿不到就不显示，不打扰用户 */ });
  }

  function doLogout() {
    // 退出 = 清掉服务端的 TG 凭证 + 浏览器门票，回到登录页。
    //
    // 注意 /auth/logout 和 /gate/logout 是两套：
    //   /auth/logout 清的是 TG 用户 cookie（access_token）
    //   /gate/logout 清的是 TGPan 的登录态（TG 凭证 + tgpan_gate 门票）
    // 这里要的是后者 —— 真正让「这个人」出去。
    req('/gate/logout', { method: 'POST' }).then(function () {
      location.reload();
    }).catch(function () {
      location.reload();
    });
  }

  // =====================================================================
  //  路由
  // =====================================================================
  function go(tab) {
    state.tab = tab;
    if (tab === 'drive') {
      state.cwd = [];
      state.files = [];
    }
    render();
    if (tab === 'drive') loadRoot();
    if (tab === 'settings') loadSettingsPane();
  }

  function render() {
    clear(app);
    app.appendChild(renderHeader());
    var main = el('main', { class: 'tp-main' });
    if (state.tab === 'drive') renderDrive(main);
    else renderSettings(main);
    app.appendChild(main);
  }

  // =====================================================================
  //  我的网盘
  // =====================================================================
  function renderDrive(main) {
    // 面包屑
    var crumbs = el('div', { class: 'tp-crumbs' });
    crumbs.appendChild(el('span', {
      class: 'tp-crumb' + (state.cwd.length === 0 ? ' is-current' : ''),
      text: '我的网盘',
      onclick: function () { if (state.cwd.length) { state.cwd = []; loadRoot(); } }
    }));
    state.cwd.forEach(function (c, i) {
      crumbs.appendChild(el('span', { class: 'tp-crumb-sep', text: '/' }));
      crumbs.appendChild(el('span', {
        class: 'tp-crumb' + (i === state.cwd.length - 1 ? ' is-current' : ''),
        text: c.name,
        onclick: function () {
          if (i === state.cwd.length - 1) return;
          state.cwd = state.cwd.slice(0, i + 1);
          loadDir();
        }
      }));
    });
    main.appendChild(crumbs);

    // 工具行
    var head = el('div', { class: 'tp-page-head' }, [
      el('h1', {
        class: 'tp-page-title',
        text: state.cwd.length ? state.cwd[state.cwd.length - 1].name : '全部频道'
      }),
      el('span', { class: 'tp-page-sub', id: 'tp-count', text: '' }),
      el('div', { class: 'tp-spacer' }),
      el('button', {
        class: 'tp-btn sm', text: '刷新',
        onclick: function () { if (state.cwd.length) loadDir(); else loadRoot(); }
      })
    ]);
    main.appendChild(head);

    var body = el('div', { id: 'tp-drive-body' });
    main.appendChild(body);

    if (state.loading) renderLoading(body);
    else renderDriveBody(body);
  }

  function renderLoading(host) {
    clear(host);
    host.appendChild(el('div', { class: 'tp-loading' }, [
      el('span', { class: 'tp-spin' }),
      document.createTextNode('加载中…')
    ]));
  }

  function renderDriveBody(host) {
    clear(host);
    var folders = state.files.filter(function (f) { return f.type === 'folder'; });
    var files = state.files.filter(function (f) { return f.type !== 'folder'; });

    // 根目录：频道文件夹用网格卡片展示
    if (state.cwd.length === 0 && folders.length) {
      var grid = el('div', { class: 'tp-folders' });
      folders.forEach(function (f) {
        grid.appendChild(el('div', {
          class: 'tp-folder',
          onclick: function () { enterFolder(f); }
        }, [
          el('div', { class: 'tp-folder-icon', html: iconSvg('folder', 19) }),
          el('div', { class: 'tp-folder-meta' }, [
            el('div', { class: 'tp-folder-name', text: f.name }),
            el('div', { class: 'tp-folder-info', text: folderInfo(f) })
          ])
        ]));
      });
      host.appendChild(grid);
      if (files.length) {
        host.appendChild(el('div', { class: 'tp-page-head', attrs: {} }, [
          el('h2', { class: 'tp-card-title', text: '根目录文件' })
        ]));
        host.appendChild(renderFileList(files));
      }
    } else if (folders.length || files.length) {
      host.appendChild(renderFileList(folders.concat(files)));
    } else {
      host.appendChild(el('div', { class: 'tp-empty' }, [
        el('div', { class: 'tp-empty-big', html: iconSvg('folder', 34) }),
        el('div', {
          text: state.cwd.length === 0
            ? '还没有频道文件夹。去「系统设置 → 扫描频道」把 TG 频道扫进来。'
            : '这个文件夹是空的。'
        })
      ]));
    }
  }

  function folderInfo(f) {
    var n = Number(f.size || 0);
    if (n > 0) return fmtSize(n);
    return '文件夹';
  }

  function renderFileList(items) {
    var wrap = el('div', { class: 'tp-card tp-files' });
    items.forEach(function (f) {
      wrap.appendChild(renderRow(f));
    });
    return wrap;
  }

  function renderRow(f) {
    var isFolder = f.type === 'folder';
    var kind = isFolder ? 'folder' : fileKind(f.name);
    var iconChar = isFolder ? iconSvg('folder', 16)
      : kind === 'video' ? iconSvg('film', 16)
        : kind === 'image' ? iconSvg('image', 16)
          : kind === 'audio' ? iconSvg('music', 16) : iconSvg('file', 16);

    var actions = el('div', { class: 'tp-row-actions' });
    if (isFolder) {
      actions.appendChild(el('button', {
        class: 'tp-linkbtn', text: '打开',
        onclick: function (e) { e.stopPropagation(); enterFolder(f); }
      }));
    } else {
      if (kind === 'video') {
        actions.appendChild(el('button', {
          class: 'tp-linkbtn', text: '播放',
          onclick: function (e) { e.stopPropagation(); playVideo(f); }
        }));
      }
      actions.appendChild(el('button', {
        class: 'tp-linkbtn', text: '下载',
        onclick: function (e) { e.stopPropagation(); downloadFile(f); }
      }));
      actions.appendChild(el('button', {
        class: 'tp-linkbtn', text: '复制直链',
        onclick: function (e) { e.stopPropagation(); copyDirectLink(f); }
      }));
    }

    var row = el('div', {
      class: 'tp-row is-clickable',
      onclick: function () {
        if (isFolder) enterFolder(f);
        else if (kind === 'video') playVideo(f);
        else downloadFile(f);
      }
    }, [
      el('div', { class: 'tp-row-icon ' + kind, html: iconChar }),
      el('div', { class: 'tp-row-name', text: f.name, title: f.name }),
      el('div', { class: 'tp-row-size', text: isFolder ? '' : fmtSize(f.size) }),
      actions
    ]);
    return row;
  }

  function enterFolder(f) {
    state.cwd.push({ id: f.id, name: f.name });
    loadDir();
  }

  function loadRoot() {
    doLoad(null, false);
  }

  function loadDir() {
    var cur = state.cwd[state.cwd.length - 1];
    if (!cur) { doLoad(null, false); return; }
    doLoad(cur.id, false);
  }

  // doLoad 是「列目录」的唯一实现，三个入口共用：
  //
  //   loadRoot()       根目录，显示频道文件夹
  //   loadDir()        进入某个文件夹
  //   silentRefresh()  后台静默刷新（silent=true）
  //
  // silent=true 时：
  //   · 不显示 loading（否则用户会看到列表突然变成"加载中"）
  //   · 不因为"数据没变"而无谓重绘（避免打断滚动位置和选中状态）
  function doLoad(parentId, silent) {
    if (!silent) {
      state.loading = true;
      render();
    }

    var isRoot = !parentId;

    if (!isRoot) {
      listFiles({ parentId: parentId, limit: MAX_LIMIT }).then(function (r) {
        var next = filesOf(r);
        applyFiles(next, silent);
      }).catch(function (e) {
        state.loading = false;
        if (silent) return;   // 静默刷新失败就当没发生，别弹错误吓人
        render();
        var body = document.getElementById('tp-drive-body');
        if (body) {
          clear(body);
          body.appendChild(el('div', { class: 'tp-alert err', text: '读取失败：' + e.message }));
        }
      });
      return;
    }

    // 根目录：「我的网盘」= 频道文件夹。优先用 channel_scans 里登记的 folderId，
    // 这样即使根目录里混杂了别的文件夹也不会显示错。
    req('/scan/channels').then(function (res) {
      var list = (res && res.channels) || [];
      state.channels = list;

      var want = {};
      list.forEach(function (c) {
        if (c.folderId) want[c.folderId] = c;
      });

      return listFiles({ limit: MAX_LIMIT, foldersOnly: true }).then(function (r) {
        var all = filesOf(r);

        // 注意：不传 parentId 时后端会递归返回**所有层级**的文件。
        // 所以要按 parentId 过滤出真正在根目录（parentId 为空）的项。
        var roots = all.filter(function (f) {
          var pid = f.parentId;
          return !pid || pid === '' || pid === 'root';
        });

        var picks;
        if (Object.keys(want).length) {
          // 有频道登记：只显示这些频道文件夹（并保持登记顺序）
          picks = roots.filter(function (f) {
            return f.type === 'folder' && want[f.id];
          });
          picks.sort(function (a, b) {
            var ia = list.findIndex(function (c) { return c.folderId === a.id; });
            var ib = list.findIndex(function (c) { return c.folderId === b.id; });
            return ia - ib;
          });
        } else {
          // 没有任何频道登记：直接显示根目录里的东西
          picks = roots;
        }

        applyFiles(picks, silent);
      });
    }).catch(function () {
      // 拿频道登记失败（最常见：还没配对 TG，接口 401）。
      // 这不该让整页报错 —— 退回直接列根目录，用户至少能看到已有文件。
      return listFiles({ limit: MAX_LIMIT, foldersOnly: true }).then(function (r) {
        var all = filesOf(r);
        var picks = all.filter(function (f) {
          var pid = f.parentId;
          return !pid || pid === '' || pid === 'root';
        });
        applyFiles(picks, silent);
      }).catch(function (e2) {
        state.loading = false;
        if (silent) return;
        render();
        var body = document.getElementById('tp-drive-body');
        if (body) {
          clear(body);
          body.appendChild(el('div', { class: 'tp-alert err', text: '读取网盘失败：' + e2.message }));
        }
      });
    });
  }

  // applyFiles 落数据 + 决定要不要重绘。
  //
  // 静默刷新时如果内容一模一样就**不重绘** —— 重绘会重建所有 DOM 节点，
  // 用户正在滚动的位置、刚聚焦的元素全丢。这是"自动刷新不打断"的关键一步。
  function applyFiles(next, silent) {
    if (silent && sameFiles(state.files, next)) {
      state.loading = false;
      return;
    }
    state.files = next;
    state.loading = false;
    render();
  }

  // sameFiles 比较两个文件列表是不是"同一批"。
  // 只比 id + name + size —— 这三个变了就说明真变了。
  function sameFiles(a, b) {
    a = a || []; b = b || [];
    if (a.length !== b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i].id !== b[i].id) return false;
      if (a[i].name !== b[i].name) return false;
      if (a[i].size !== b[i].size) return false;
    }
    return true;
  }

  function listFiles(params) {
    var q = [];
    // 根目录：**不要**传 parentId。后端把 parent_id 当 UUID 解析，
    // 传 "root" 会直接 SQLSTATE 22P02（invalid input syntax for type uuid）。
    //
    // 【2026-10 修复】不传 parentId 时后端返回的是「全部层级」的文件，
    // 再靠 limit 截断取前 N 条。文件一多（本库 1965 个），
    // 根目录的频道文件夹就被挤到 1000 条之外，界面上只剩两三个频道。
    // 修法：根目录查询加 category=folder，只取文件夹（几十个，永不截断）。
    if (params.foldersOnly) q.push('category=folder');
    if (params.parentId) q.push('parentId=' + encodeURIComponent(params.parentId));
    q.push('limit=' + (params.limit || MAX_LIMIT));
    q.push('sort=name');
    q.push('order=asc');
    return req('/files?' + q.join('&'));
  }

  // filesOf 从接口响应里取出文件数组。
  //
  // 后端把结果放在 items 字段下（不是 files）。这里做一层兼容，
  // 避免以后字段名再变时整个界面白屏。
  function filesOf(res) {
    if (!res) return [];
    if (Array.isArray(res)) return res;
    return res.items || res.files || [];
  }

  // ---- 播放 ----
  function playVideo(f) {
    var src = streamUrl(f);
    var wrap = el('div', { class: 'tp-player-wrap', id: 'tp-player' });
    var bar = el('div', { class: 'tp-player-bar' }, [
      el('span', { class: 'tp-player-title', text: f.name, title: f.name }),
      el('span', { class: 'tp-page-sub', text: fmtSize(f.size) }),
      el('button', { class: 'tp-player-close', text: '×', onclick: closePlayer })
    ]);
    var stage = el('div', { class: 'tp-player-stage' });
    var v = document.createElement('video');
    v.src = src;
    v.controls = true;
    v.autoplay = true;
    v.playsInline = true;
    v.preload = 'metadata';
    stage.appendChild(v);

    // 播放失败得有人吱一声。
    //
    // 视频流是从 Telegram 现取的：会话过期、原文件被删、频道没权限，
    // 都会让 <video> 直接黑屏 —— 而黑屏和「还在加载」长得一模一样，
    // 用户只会以为网卡了，一直干等。
    var tip = el('div', { class: 'tp-player-err', style: 'display:none' });
    stage.appendChild(tip);
    function fail(msg) {
      tip.style.display = '';
      tip.textContent = msg;
    }
    v.addEventListener('error', function () {
      // video.error.code 的取值含义差别很大，别一句话糊弄过去：
      //   1 ABORTED / 2 NETWORK → 流根本没拿到（服务端 500、网络断、会话过期）
      //   3 DECODE             → 拿到了但解不开（格式不支持）
      //   4 SRC_NOT_SUPPORTED  → 浏览器压根不认这个源
      var code = v.error && v.error.code;
      if (code === 3 || code === 4) {
        fail('这个格式浏览器放不了。点「下载」用本地播放器看吧。');
      } else {
        fail('视频流没取到。可能是 Telegram 那边暂时不可用，或者登录已过期 —— 刷新页面试试。');
      }
    });
    // autoplay 被浏览器拦下来（无用户手势/静音策略）不算错误，
    // 但要给个提示，否则用户看着黑屏不动会以为坏了。
    v.addEventListener('play', function () { tip.style.display = 'none'; });
    var apTimer = setTimeout(function () {
      if (v.paused && !v.ended && v.readyState < 2) {
        fail('还没开始播放？点一下画面中间的播放键，或先下载到本地看。');
      }
    }, 6000);

    wrap.appendChild(bar);
    wrap.appendChild(stage);
    document.body.appendChild(wrap);

    document.addEventListener('keydown', escClose);
    function escClose(e) { if (e.key === 'Escape') closePlayer(); }
    function closePlayer() {
      clearTimeout(apTimer);
      document.removeEventListener('keydown', escClose);
      try { v.pause(); v.src = ''; } catch (e) { }
      if (wrap.parentNode) wrap.parentNode.removeChild(wrap);
    }
    wrap._close = closePlayer;
  }

  function streamUrl(f) {
    return API + '/files/' + encodeURIComponent(f.id) + '/' + encodeURIComponent(f.name);
  }

  function downloadFile(f) {
    var a = document.createElement('a');
    a.href = streamUrl(f);
    a.download = f.name;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  function copyDirectLink(f) {
    copyText(location.origin + streamUrl(f));
  }

  // =====================================================================
  //  系统设置
  // =====================================================================
  var SETTINGS_PANES = [
    ['scan', '扫描频道', '把 TG 频道里的视频扫进网盘，自动建文件夹、自动归类'],
    ['auto', '自动扫描', '让频道定时自动扫，间隔自己定。默认不开，开了才跑'],
    ['webdav', 'WebDAV 挂载', '生成账号密码，挂到网易爆米花 / Infuse / VidHub'],
    ['series', '剧集归档', '看看哪些剧被识别出来了，认错的可以手动改'],
    ['about', '关于与诊断', '版本信息、服务状态']
  ];

  function renderSettings(main) {
    var head = el('div', { class: 'tp-page-head' }, [
      el('h1', { class: 'tp-page-title', text: '系统设置' }),
      el('span', { class: 'tp-page-sub', text: '所有设置都在这里' })
    ]);
    main.appendChild(head);

    var nav = el('div', { class: 'tp-settings-nav' });
    SETTINGS_PANES.forEach(function (p) {
      nav.appendChild(el('button', {
        class: 'tp-settings-item' + (state.settingsPane === p[0] ? ' is-active' : ''),
        text: p[1],
        onclick: function () {
          state.settingsPane = p[0];
          render();
          loadSettingsPane();
        }
      }));
    });

    var body = el('div', { class: 'tp-settings-body', id: 'tp-settings-body' });
    main.appendChild(el('div', { class: 'tp-card tp-settings' }, [nav, body]));
    renderSettingsPane(body);
  }

  function renderSettingsPane(host) {
    clear(host);
    var pane = state.settingsPane;
    var meta = SETTINGS_PANES.filter(function (p) { return p[0] === pane; })[0] || [];
    host.appendChild(el('h2', { class: 'tp-settings-title', text: meta[1] || '' }));
    host.appendChild(el('p', { class: 'tp-settings-desc', text: meta[2] || '' }));

    var box = el('div', { id: 'tp-pane-content' });
    host.appendChild(box);

    if (pane === 'scan') paneScan(box);
    else if (pane === 'auto') paneAuto(box);
    else if (pane === 'webdav') paneWebDAV(box);
    else if (pane === 'series') paneSeries(box);
    else if (pane === 'about') paneAbout(box);
  }

  function loadSettingsPane() {
    var box = document.getElementById('tp-pane-content');
    if (!box) return;
    var pane = state.settingsPane;
    if (pane === 'auto') loadAutoPane(box);
    else if (pane === 'webdav') loadWebDAVPane(box);
    else if (pane === 'series') loadSeriesPane(box);
    else if (pane === 'about') loadAboutPane(box);
  }

  // ---------- 扫描频道 ----------
  function paneScan(host) {
    host.appendChild(el('div', { class: 'tp-alert info' }, [
      el('b', { text: '怎么用' }),
      el('span', { text: '：从下面的列表里挑一个频道点「扫描」就行。它会把这个频道里的新视频拉进网盘，自动建一个同名文件夹，并按「电影 / 电视剧 / 动漫」归好类。' })
    ]));

    var listBox = el('div', { id: 'tp-scan-list' }, [el('div', { class: 'tp-loading', text: '正在读取你关注的频道…' })]);
    var out = el('div', { id: 'tp-scan-out' });

    host.appendChild(listBox);
    host.appendChild(el('div', { style: 'margin-top:16px' }, [out]));

    // ⚠️ 必须把 listBox 的**元素引用**传进去，不能让它自己 getElementById 找。
    //
    // 踩过的坑：调用链是 render() → renderSettings(main) → renderSettingsPane()
    // → paneScan()，而 render() 是**先把 main 建好、渲染完、最后才
    // app.appendChild(main)**。也就是说执行到这里时，整棵树还没挂进
    // document —— document.getElementById('tp-scan-list') 必然返回 null，
    // loadScanChannelList 第一行就 return 了。
    //
    // 表现：扫描页永远停在"正在读取你关注的频道…"，且**一个网络请求都没有**
    // （连报错都没有，因为压根没发请求），排查时非常误导人。
    loadScanChannelList(false, listBox);
  }

  // 扫描页的频道列表：全部来自「你 TG 账号已关注的频道」，
  // 由后端定时从 Telegram 同步过来，这里只管展示。
  //
  // 为什么不让人手填 ID：绝大多数人根本不知道自己的频道 ID 是多少，
  // 得先点进消息链接里数那串数字，还要判断加不加 -100 前缀。
  // 既然账号本来就关注了这些频道，直接列出来点一下最省事。
  //
  // force 参数很关键：
  //   loadScanChannelList()       读后端缓存表（毫秒级）
  //   loadScanChannelList(true)   让后端**立刻去 TG 拉一次**再返回（几秒）
  //
  // boxEl 参数：直接传容器元素。**不要靠 getElementById 自己找** ——
  // 首次渲染时整棵树还没挂进 document，getElementById 必然返回 null，
  // 函数会静默 return，表现为"页面永远停在加载中，且一个请求都不发"。
  // （详见 paneScan 里的说明）
  //
  // 只有用户点「刷新」时才传 force=true。开页面时用 false ——
  // 否则每次切到扫描页都要等几秒，而且白耗 TG 配额。
  function loadScanChannelList(force, boxEl) {
    var box = boxEl || document.getElementById('tp-scan-list');
    if (!box) return;

    var url = force ? '/scan/dialogs?refresh=1' : '/scan/dialogs';

    // 手动刷新期间给个明确的"正在去 TG 拉"反馈 ——
    // 这一步要几秒，没有任何提示的话用户会以为按钮坏了而狂点
    if (force) {
      var btn0 = box.querySelector('#tp-scan-refresh');
      if (btn0) {
        btn0.disabled = true;
        btn0.textContent = '正在同步…';
      }
    }

    req(url).then(function (r) {
      state.dialogs = (r && r.dialogs) || [];
      state.dialogsSyncedAt = Date.now();
      clear(box);
      if (!state.dialogs.length) {
        box.appendChild(el('div', { class: 'tp-empty' }, [
          el('div', { text: '还没读到你的频道。' }),
          el('div', { class: 'tp-hint', text: '后台正在从 Telegram 同步，稍等一会儿点「刷新」再看看。' })
        ]));
        box.appendChild(el('div', { style: 'margin-top:10px' }, [refreshBtn(box)]));
        return;
      }
      var head = el('div', { style: 'display:flex;align-items:center;gap:10px;margin-bottom:10px' }, [
        el('div', { style: 'flex:1;font-size:13.5px;color:#5c6370', text: '共 ' + state.dialogs.length + ' 个频道' }),
        refreshBtn(box)
      ]);
      box.appendChild(head);
      var card = el('div', { class: 'tp-card' });
      state.dialogs.forEach(function (d) { card.appendChild(renderDialogRow(d)); });
      box.appendChild(card);

      // 后端说"这次同步失败了，但返回的是上次的数据"→ 告诉用户一声，
      // 别让他以为看到的是最新的
      if (r && r.syncError) {
        toastWarn('同步失败，显示的是上次的数据：' + r.syncError);
      }
    }).catch(function (e) {
      clear(box);
      box.appendChild(el('div', { class: 'tp-alert err' }, [
        el('b', { text: '读取频道失败' }),
        el('div', { text: e.message || '未知错误' }),
        el('div', { class: 'tp-hint', text: '如果一直失败，去「关于与诊断」看看 Telegram 配对情况。' })
      ]));
      box.appendChild(el('div', { style: 'margin-top:10px' }, [refreshBtn(box)]));
    });
  }

  // 刷新按钮。每次重建，保证 disabled / 文案状态跟随当前这一次刷新。
  //
  // 闭包捕获 box：点击时直接把容器传回去，不依赖 getElementById。
  function refreshBtn(box) {
    var b = el('button', {
      class: 'tp-btn', id: 'tp-scan-refresh', text: '刷新',
      title: '立刻去 Telegram 重新拉一次你关注的频道'
    });
    b.onclick = function () { loadScanChannelList(true, box); };
    return b;
  }

  function renderDialogRow(d) {
    var scanning = false;
    var deepRunning = false;
    var btn = el('button', { class: 'tp-btn primary', text: d.scanned ? '扫新视频' : '扫描' });
    var info = el('div', { class: 'tp-hint', text: d.scanned
      ? ('上次扫过 · 已导入 ' + (d.imported || 0) + ' 个')
      : '还没扫过' });

    // ---------- 翻老片（深度扫描）----------
    //
    // 普通「扫描」只看新视频；「翻老片」是从当前位置往回翻频道历史，
    // 把更早发布的老片也收进网盘。一次点击自动翻很多批（每批 2000 条
    // 消息，批与批之间歇 1.5 秒防 TG 限流），直到翻完整个频道历史，
    // 或单轮翻满 20 批（4 万条）先收工 —— 频道特别大时再点一次接着翻。
    // 中途断了也没关系：进度存在数据库里，再点一次从断点继续。
    var deepBtn = el('button', {
      class: 'tp-btn', text: '翻老片',
      title: '往回翻频道历史，把更早的老片也扫进网盘'
    });
    deepBtn.onclick = function () {
      if (deepRunning || scanning) return;
      deepRunning = true;
      deepBtn.disabled = true;
      btn.disabled = true;

      var tScanned = 0, tImported = 0, rounds = 0, lastMore = false;
      var t0 = Date.now();
      deepBtn.textContent = '翻老片 0条';

      function round() {
        rounds++;
        req('/scan/channel', { method: 'POST', body: { channelId: d.channelId, deep: true, batch: 2000 } })
          .then(function (r) {
            tScanned += (r.deepScanned || 0);
            tImported += (r.imported || 0);
            lastMore = !!r.hasMore;
            deepBtn.textContent = '翻老片 ' + tScanned + '条';

            // 还有更早的且没翻满 20 批 → 歇 1.5 秒继续下一批
            if (lastMore && rounds < 20) {
              setTimeout(round, 1500);
              return;
            }

            deepRunning = false;
            deepBtn.disabled = false;
            btn.disabled = false;
            deepBtn.textContent = lastMore ? '继续翻老片' : '翻老片';
            var secs = Math.round((Date.now() - t0) / 1000);
            clear(out);
            out.appendChild(el('div', { class: 'tp-alert ' + (tImported > 0 ? 'ok' : 'info') }, [
              el('b', { text: lastMore ? '这轮翻完了，频道历史还没到头' : '老片翻完了' }),
              el('div', { text: (r.channelName || d.title) + '：往回翻了 ' + tScanned + ' 条消息，新进 ' + tImported + ' 个视频' }),
              el('div', { class: 'tp-hint', text: lastMore
                ? '这个频道历史很长，歇一会儿再点「继续翻老片」接着往回翻。'
                : '整个频道的历史已经全部翻完，用时 ' + secs + ' 秒。' })
            ]));
            if (tImported > 0) {
              toastOk('翻到 ' + tImported + ' 个老片，用时 ' + secs + ' 秒');
              state.channels = null;
              if (state.tab === 'drive') {
                doLoad(state.cwd.length ? state.cwd[state.cwd.length - 1].id : null, true);
              }
            }
          })
          .catch(function (e) {
            deepRunning = false;
            deepBtn.disabled = false;
            btn.disabled = false;
            deepBtn.textContent = '继续翻老片';
            clear(out);
            var msg = e.message || '';
            var extra = /FLOOD|限流/i.test(msg) ? 'Telegram 在限流，歇几分钟再点一次就好。' : '';
            out.appendChild(el('div', { class: 'tp-alert err' }, [
              el('b', { text: '翻老片中断' }),
              el('div', { text: msg + '（已往回翻 ' + tScanned + ' 条，进度已保存，再点一次接着翻）' }),
              extra ? el('div', { class: 'tp-hint', text: extra }) : null
            ]));
            toastErr('翻老片中断：' + (msg || '未知错误'));
          });
      }
      round();
    };

    btn.onclick = function () {
      if (scanning || deepRunning) return;
      scanning = true;
      btn.disabled = true;

      // 扫描是同步阻塞的，大频道可能要跑几分钟。挂个秒表，
      // 让「它还在动」这件事看得见，不然用户以为卡死了会刷新页面。
      var t0 = Date.now();
      var tick = setInterval(function () {
        var s = Math.floor((Date.now() - t0) / 1000);
        btn.textContent = '扫描中 ' + s + 's';
      }, 1000);
      btn.textContent = '扫描中 0s';

      // 注意 incremental:true —— 后端本来就支持增量扫描（只拉游标之后
      // 的新消息），但以前前端从来没传过这个字段，等于每次都把整个频道
      // 从头翻一遍。频道一大就慢，还容易吃 Telegram 的限流。
      var body = { channelId: d.channelId, incremental: true, limit: 2000 };

      req('/scan/channel', { method: 'POST', body: body }).then(function (r) {
        clearInterval(tick);
        var secs = Math.round((Date.now() - t0) / 1000);
        scanning = false;
        btn.disabled = false;
        btn.textContent = '扫新视频';

        var n = r.imported || 0;
        info.textContent = '上次扫过 · 已导入 ' + (r.totalImported || d.imported || 0) + ' 个';

        clear(out);
        if (n > 0) {
          out.appendChild(el('div', { class: 'tp-alert ok' }, [
            el('b', { text: '扫描完成' }),
            el('div', { text: (r.channelName || d.title) + '：新导入 ' + n + ' 个视频' }),
            el('div', { class: 'tp-hint', text: (r.message || '') + ' 用时 ' + secs + ' 秒。去「我的网盘」就能看到。' })
          ]));
        } else {
          out.appendChild(el('div', { class: 'tp-alert info' }, [
            el('b', { text: '没有新视频' }),
            el('div', { text: (r.channelName || d.title) + '：' + (r.message || ('扫了 ' + (r.scanned || 0) + ' 条消息，都已在网盘里。')) }),
            el('div', { class: 'tp-hint', text: '用时 ' + secs + ' 秒。等频道更新了再点一次就行。' })
          ]));
        }
        toastOk('扫描完成，用时 ' + secs + ' 秒');
        state.channels = null;
        // 如果用户此刻就在网盘页，顺手把当前目录刷一下 ——
        // 否则他切回去看到的还是扫描前的旧列表（以为是扫描没生效）。
        // 只在自己这个页签上刷，不去打扰别的页面。
        if (state.tab === 'drive') {
          doLoad(state.cwd.length ? state.cwd[state.cwd.length - 1].id : null, true);
        }
      }).catch(function (e) {
        clearInterval(tick);
        scanning = false;
        btn.disabled = false;
        btn.textContent = d.scanned ? '扫新视频' : '扫描';
        clear(out);
        var msg = e.message || '';
        var extra = '';
        if (/unauthor|login|session/i.test(msg)) {
          extra = '看起来是 Telegram 登录状态的问题，去「关于与诊断」看看配对情况。';
        } else if (/FLOOD|限流/i.test(msg)) {
          extra = 'Telegram 在限流，等几分钟再点一次就好。';
        }
        out.appendChild(el('div', { class: 'tp-alert err' }, [
          el('b', { text: '扫描失败' }),
          el('div', { text: msg }),
          extra ? el('div', { class: 'tp-hint', text: extra }) : null
        ]));
        toastErr('扫描失败：' + (msg || '未知错误'));
      });
    };

    return el('div', { class: 'tp-row', style: 'gap:10px' }, [
      el('div', { style: 'flex:1;min-width:150px' }, [
        el('div', { style: 'font-size:14.5px;font-weight:500' }, [
          el('span', { class: 'tp-dot ' + (d.scanned ? 'on' : 'off') }),
          document.createTextNode(d.title || ('频道 ' + d.channelId))
        ]),
        info
      ]),
      deepBtn,
      btn
    ]);
  }

  // ---------- 自动扫描 ----------
  function paneAuto(host) {
    host.appendChild(el('div', { class: 'tp-alert info', text: '下面列出的频道会按你设的间隔自动扫。默认「关」，只有你打开的才会跑。' }));
    var box = el('div', { id: 'tp-auto-list' }, [el('div', { class: 'tp-loading', text: '加载中…' })]);
    host.appendChild(box);

    host.appendChild(el('div', { class: 'tp-field', style: 'margin-top:18px' }, [
      el('label', { text: '把频道加进自动扫描' }),
      el('div', { style: 'display:flex;gap:8px' }, [
        el('input', { class: 'tp-input', id: 'tp-auto-chid', placeholder: 'TG 频道 ID' }),
        el('button', {
          class: 'tp-btn', text: '添加',
          onclick: function () { addAutoChannel(); }
        })
      ]),
      el('div', { class: 'tp-hint', text: '频道得先扫过一次（有网盘文件夹）才能加进来' })
    ]));
  }

  function loadAutoPane(box) {
    var list = (box || document).querySelector('#tp-auto-list');
    if (!list) return;
    req('/scan/channels').then(function (r) {
      state.channels = (r && r.channels) || [];
      clear(list);
      if (!state.channels.length) {
        list.appendChild(el('div', { class: 'tp-empty' }, [
          el('div', { text: '还没登记任何频道。' }),
          el('div', { class: 'tp-hint', text: '先去「扫描频道」扫一个进来。' })
        ]));
        return;
      }
      var card = el('div', { class: 'tp-card' });
      state.channels.forEach(function (c) {
        card.appendChild(renderChannelRow(c));
      });
      list.appendChild(card);
    }).catch(function (e) {
      clear(list);
      list.appendChild(el('div', { class: 'tp-alert err', text: '读取失败：' + e.message }));
    });
  }

  function renderChannelRow(c) {
    var enabled = !!c.enabled;
    var secs = c.intervalSeconds || 120;

    var toggle = el('input', { type: 'checkbox' });
    toggle.checked = enabled;
    toggle.onchange = function () {
      saveChannel(c, toggle.checked, null, this);
    };

    var sel = el('select', { class: 'tp-select', style: 'width:auto;min-width:96px' });
    [
      [60, '1 分钟'], [120, '2 分钟'], [300, '5 分钟'],
      [600, '10 分钟'], [1800, '30 分钟'], [3600, '1 小时'], [21600, '6 小时']
    ].forEach(function (o) {
      var op = el('option', { value: o[0], text: o[1] });
      if (o[0] === secs) op.selected = true;
      sel.appendChild(op);
    });
    sel.onchange = function () {
      saveChannel(c, null, Number(sel.value), this);
    };

    // 已停用的频道：间隔下拉没意义（不会按它扫），直接禁掉，别让用户
    // 白改一通还纳闷儿为什么没效果。要改先「重新开启」。
    if (!enabled) {
      sel.disabled = true;
      sel.title = '已停用，先点「重新开启」再改间隔';
    }

    var runBtn = el('button', {
      class: 'tp-linkbtn', text: '立即扫一次',
      onclick: function () {
        runBtn.textContent = '扫描中…';
        req('/scan/channels/' + encodeURIComponent(String(c.channelId)) + '/run', { method: 'POST' })
          .then(function () { toast('已开始扫描'); runBtn.textContent = '立即扫一次'; setTimeout(function () { loadAutoPane(); }, 1200); })
          .catch(function (e) { toastErr('失败：' + e.message); runBtn.textContent = '立即扫一次'; });
      }
    });

    // v16 全量重扫：删掉该频道已导入的文件，从头重新扫一遍，
    // 老文件的脏名字（时间戳/水印/段号）会按最新规则重新命名。
    var fullBtn = el('button', {
      class: 'tp-linkbtn', text: '全量重扫',
      onclick: function () {
        if (!confirm('全量重扫会先删除「' + (c.channelName || '该频道') + '」已导入的全部文件，再从头扫描一遍重新入库。\n\nTG 里的片子本体不受影响，扫完会全部回来（名字更干净）。\n\n确定要重扫吗？')) return;
        fullBtn.textContent = '重扫中…';
        req('/scan/channels/' + encodeURIComponent(String(c.channelId)) + '/rescanfull', { method: 'POST' })
          .then(function (r) {
            var msg = '已重扫：清掉 ' + ((r && r.purged) || 0) + ' 个旧记录，新导入 ' + ((r && r.imported) || 0) + ' 个';
            if (r && r.duplicates) msg += '，拦下重复 ' + r.duplicates + ' 个';
            toast(msg);
            fullBtn.textContent = '全量重扫';
            setTimeout(function () { loadAutoPane(); }, 1200);
          })
          .catch(function (e) { toastErr('重扫失败：' + e.message); fullBtn.textContent = '全量重扫'; });
      }
    });

    // 「移出」在后端是软删除：只把 enabled 置 false，**不删**已导入的文件，
    // 也保留扫描游标（下次再开启能接着扫，不会重复导入）。
    //
    // 问题在于列表接口会把这类「已停用」的记录照样返回。如果按钮永远写
    // 「移出」，用户点完发现那一行没消失，会以为按钮坏了，于是反复点。
    // 所以这里让按钮文案跟着状态走 —— 已停用的显示「重新开启」，
    // 用户一眼能看出「刚才那下确实生效了，只是这行不会被抹掉」。
    var disabled = !enabled;
    var delBtn = el('button', {
      class: 'tp-linkbtn' + (disabled ? '' : ' danger'),
      text: disabled ? '重新开启' : '移出',
      onclick: function () {
        if (disabled) {
          // 重新开启：等价于把开关打开
          saveChannel(c, true, null, delBtn);
          return;
        }
        req('/scan/channels/' + encodeURIComponent(String(c.channelId)), { method: 'DELETE' })
          .then(function () {
            toast('已停止自动扫描（文件保留）');
            loadAutoPane();
          })
          .catch(function (e) { toastErr('失败：' + e.message); });
      }
    });

    // v16 彻底删除：文件树 + 文件夹 + 扫描记录一起删（TG 里的片子本体不动，
    // 但不重新扫描的话网盘里就没了）。和上面的「移出/重新开启」完全两回事。
    var purgeBtn = el('button', {
      class: 'tp-linkbtn danger', text: '彻底删除',
      onclick: function () {
        if (!confirm('确定要彻底删除「' + (c.channelName || '该频道') + '」吗？\n\n会删掉：已导入的全部文件 + 频道文件夹 + 扫描记录。\nTG 频道里的片子本体不受影响，但网盘里将看不到它们。\n\n此操作不可撤销，确定吗？')) return;
        purgeBtn.textContent = '删除中…';
        req('/scan/channels/' + encodeURIComponent(String(c.channelId)) + '/purge', { method: 'DELETE' })
          .then(function (r) {
            toast('已彻底删除（清掉 ' + ((r && r.deleted) || 0) + ' 条记录）');
            loadAutoPane();
          })
          .catch(function (e) { toastErr('删除失败：' + e.message); purgeBtn.textContent = '彻底删除'; });
      }
    });

    return el('div', { class: 'tp-row' + (disabled ? ' is-off' : ''), style: 'flex-wrap:wrap;gap:10px' }, [
      el('div', { style: 'flex:1;min-width:150px' }, [
        el('div', { style: 'font-size:14.5px;font-weight:500' }, [
          el('span', { class: 'tp-dot ' + (c.lastError ? 'err' : enabled ? 'on' : 'off') }),
          document.createTextNode(c.channelName || ('频道 ' + c.channelId)),
          disabled ? el('span', { class: 'tp-tag', text: '已停用' }) : null
        ]),
        el('div', { class: 'tp-hint', text: '文件夹：' + (c.folderName || '—') + ' · 已导入 ' + (c.totalImported || 0) + ' 个 · 上次 ' + fmtTime(c.lastScanAt) })
      ]),
      el('label', { style: 'display:flex;align-items:center;gap:6px;font-size:13px;color:#5c6370' }, [
        toggle, el('span', { text: '开启' })
      ]),
      sel,
      runBtn,
      fullBtn,
      delBtn,
      purgeBtn
    ]);
  }

  function saveChannel(c, enabled, interval, ctrl) {
    var body = {
      channelId: c.channelId,
      folderName: c.folderName || '',
      enabled: enabled === null ? !!c.enabled : enabled,
      intervalSeconds: interval === null ? (c.intervalSeconds || 120) : interval
    };
    if (ctrl) ctrl.disabled = true;
    req('/scan/channels', { method: 'POST', body: body }).then(function () {
      toast('已保存');
      loadAutoPane();
    }).catch(function (e) {
      toastErr('保存失败：' + e.message);
      if (ctrl) ctrl.disabled = false;
    });
  }

  function addAutoChannel() {
    var input = document.getElementById('tp-auto-chid');
    var v = (input.value || '').trim();
    if (!v) { toastWarn('请填频道 ID'); return; }
    var idNum = Number(v);
    if (!idNum) { toastWarn('频道 ID 必须是数字'); return; }
    req('/scan/channels', {
      method: 'POST',
      body: { channelId: idNum, enabled: true, intervalSeconds: 120 }
    }).then(function () {
      toast('已添加');
      input.value = '';
      loadAutoPane();
    }).catch(function (e) { toastErr('添加失败：' + e.message); });
  }

  // ---------- WebDAV ----------
  function paneWebDAV(host) {
    host.appendChild(el('div', { class: 'tp-alert info' }, [
      el('b', { text: '挂载地址' }),
      el('div', {
        class: 'tp-kv-val',
        style: 'margin-top:6px',
        id: 'tp-dav-addr',
        text: location.origin + '/webdav/'
      }),
      el('div', { class: 'tp-hint', text: '注意结尾有个斜杠，别漏。爆米花里填这个地址。' })
    ]));

    host.appendChild(el('div', { style: 'display:flex;align-items:center;gap:10px;margin-bottom:12px' }, [
      el('button', { class: 'tp-btn primary', text: '生成新账号', onclick: createCred }),
      el('button', { class: 'tp-btn', text: '复制地址', onclick: function () { copyText(location.origin + '/webdav/'); } })
    ]));

    host.appendChild(el('div', { id: 'tp-dav-list' }, [el('div', { class: 'tp-loading', text: '加载中…' })]));
  }

  function loadWebDAVPane(box) {
    var list = (box || document).querySelector('#tp-dav-list');
    if (!list) return;
    req('/webdav/credentials').then(function (r) {
      var creds = (r && r.credentials) || [];
      state.creds = creds;
      clear(list);
      if (!creds.length) {
        list.appendChild(el('div', { class: 'tp-empty' }, [
          el('div', { class: 'tp-empty-big', html: iconSvg('key', 34) }),
          el('div', { text: '还没有账号。点上面「生成新账号」。' }),
          el('div', { class: 'tp-hint', text: '密码只显示这一次，生成后请立刻复制保存。' })
        ]));
        return;
      }
      var card = el('div', { class: 'tp-card' });
      creds.forEach(function (c) {
        card.appendChild(el('div', { class: 'tp-row', style: 'flex-wrap:wrap;gap:10px' }, [
          el('div', { style: 'flex:1;min-width:150px' }, [
            el('div', { style: 'font-size:14.5px;font-weight:500' }, [
              el('span', { class: 'tp-dot ' + (c.enabled ? 'on' : 'off') }),
              document.createTextNode(c.username)
            ]),
            el('div', { class: 'tp-hint', text: (c.label || '未命名') + ' · 创建于 ' + fmtTime(c.createdAt) + (c.lastUsed ? ' · 上次使用 ' + fmtTime(c.lastUsed) : '') })
          ]),
          el('button', {
            class: 'tp-linkbtn danger', text: '删除',
            onclick: function () {
              req('/webdav/credentials/' + encodeURIComponent(c.id), { method: 'DELETE' })
                .then(function () { toast('已删除'); loadWebDAVPane(); })
                .catch(function (e) { toastErr('失败：' + e.message); });
            }
          })
        ]));
      });
      list.appendChild(card);
      list.appendChild(el('div', { class: 'tp-hint', style: 'margin-top:10px', text: '密码只在生成时显示一次。如果忘了，删掉重新生成一个。' }));
    }).catch(function (e) {
      clear(list);
      list.appendChild(el('div', { class: 'tp-alert err', text: '读取失败：' + e.message }));
    });
  }

  function createCred() {
    req('/webdav/credentials', { method: 'POST', body: { label: '爆米花' } }).then(function (c) {
      // 弹窗展示，因为这些参数只出现这一次
      showCredModal(c);
      loadWebDAVPane();
    }).catch(function (e) { toastErr('生成失败：' + e.message); });
  }

  function showCredModal(c) {
    var addr = (c.mountPath ? location.origin + c.mountPath + '/' : location.origin + '/webdav/');

    function kv(key, val) {
      return el('div', { class: 'tp-kv' }, [
        el('div', { class: 'tp-kv-key', text: key }),
        el('div', { class: 'tp-kv-val', text: val })
      ]);
    }

    var modal = el('div', { class: 'tp-modal' }, [
      el('div', { class: 'tp-modal-head' }, [
        el('h3', { class: 'tp-modal-title', text: '账号已生成' }),
        el('button', { class: 'tp-modal-close', text: '×', onclick: close })
      ]),
      el('div', { class: 'tp-alert warn', text: '密码只显示这一次，关掉就看不到了。请现在复制保存。' }),
      kv('地址', addr),
      kv('账号', c.username || '—'),
      kv('密码', c.password || '—'),
      el('div', { style: 'display:flex;gap:8px;margin-top:16px' }, [
        el('button', {
          class: 'tp-btn primary block',
          text: '复制全部',
          onclick: function () {
            copyText('地址：' + addr + '\n账号：' + (c.username || '') + '\n密码：' + (c.password || ''));
          }
        }),
        el('button', { class: 'tp-btn block', text: '关闭', onclick: close })
      ]),
      el('div', { class: 'tp-hint', style: 'margin-top:12px', text: '在网易爆米花里选「WebDAV」，把这四项填进去就能挂载。' })
    ]);

    var mask = el('div', { class: 'tp-mask' }, [modal]);
    mask.addEventListener('click', function (e) { if (e.target === mask) close(); });
    document.body.appendChild(mask);

    function close() { if (mask.parentNode) mask.parentNode.removeChild(mask); }
  }

  // ---------- 剧集归档 ----------
  function paneSeries(host) {
    host.appendChild(el('div', { class: 'tp-alert info', text: '下面是扫描时识别出来的剧集（文件名已经规范成「剧名 S01E05」这种）。爆米花这类播放器就是靠这个刮削的。' }));
    host.appendChild(el('div', { id: 'tp-series-list' }, [el('div', { class: 'tp-loading', text: '加载中…' })]));
  }

  function loadSeriesPane(box) {
    var list = (box || document).querySelector('#tp-series-list');
    if (!list) return;
    req('/scan/series').then(function (r) {
      var series = (r && r.series) || (Array.isArray(r) ? r : []);
      state.series = series;
      clear(list);
      if (!series.length) {
        list.appendChild(el('div', { class: 'tp-empty' }, [
          el('div', { class: 'tp-empty-big', html: iconSvg('film', 34) }),
          el('div', { text: '还没有识别出剧集。' }),
          el('div', { class: 'tp-hint', text: '去扫一个频道试试。' })
        ]));
        return;
      }
      var card = el('div', { class: 'tp-card' });
      series.forEach(function (s) {
        var range = '';
        if (s.firstEpisode || s.lastEpisode) {
          range = s.firstEpisode === s.lastEpisode
            ? '第 ' + s.firstEpisode + ' 集'
            : '第 ' + s.firstEpisode + '–' + s.lastEpisode + ' 集';
        }
        card.appendChild(el('div', { class: 'tp-row' }, [
          el('div', { class: 'tp-row-icon video', html: iconSvg('film', 15) }),
          el('div', { style: 'flex:1;min-width:0' }, [
            el('div', { class: 'tp-row-name', text: s.title || '(未命名)' }),
            el('div', { class: 'tp-hint', text: [range, (s.fileCount || 0) + ' 个文件', (s.episodeCount || 0) + ' 集已规范'].filter(Boolean).join(' · ') })
          ])
        ]));
      });
      list.appendChild(card);
    }).catch(function (e) {
      clear(list);
      list.appendChild(el('div', { class: 'tp-alert err', text: '读取失败：' + e.message }));
    });
  }

  // ---------- 关于 ----------
  function paneAbout(host) {
    host.appendChild(el('div', { id: 'tp-about' }, [el('div', { class: 'tp-loading', text: '加载中…' })]));
  }

  function loadAboutPane(box) {
    var host = (box || document).querySelector('#tp-about');
    if (!host) return;
    req('/version').then(function (v) {
      clear(host);
      // 字段名以服务端实际返回为准：{"version","commitSHA","goVersion","os","arch"}。
      // 注意是 commitSHA（小写 c 开头），早先按 commit / CommitSHA 取值永远取不到，
      // 版本号后面那段 SHA 就一直空着。
      var ver = v.version || '—';
      var sha = v.commitSHA || '';
      host.appendChild(el('div', { class: 'tp-card tp-card-body' }, [
        kvRow('版本', ver + (sha && sha !== 'unknown' ? '（' + String(sha).slice(0, 7) + '）' : '')),
        kvRow('界面', 'TGPan 重写版 v2.7.0'),
        kvRow('地址', location.origin),
        kvRow('WebDAV', location.origin + '/webdav/'),
        (v.goVersion || v.os) ? kvRow('运行环境', [v.goVersion, v.os, v.arch].filter(Boolean).join(' · ')) : null
      ]));
    }).catch(function () {
      clear(host);
      host.appendChild(el('div', { class: 'tp-alert warn', text: '拿不到版本信息，服务可能是旧版。' }));
    });
  }

  function kvRow(k, v) {
    return el('div', { class: 'tp-kv' }, [
      el('div', { class: 'tp-kv-key', text: k }),
      el('div', { class: 'tp-kv-val', text: v })
    ]);
  }

  // =====================================================================
  //  启动
  // =====================================================================
  function boot() {
    if (!app) return;

    // 先画骨架，但**先别发业务请求**。
    //
    // 原因：闸门（扫码 / 验证码登 TG）会盖一层遮罩，而它的
    // 状态是异步问出来的。如果这里上来就 loadRoot()，在闸门还没放行的那
    // 一瞬间 /api/files 会 401，界面先渲染一个「读取网盘失败」的红框，
    // 紧接着才被遮罩盖住 —— 用户能看到报错闪一下，以为坏了。
    //
    // 所以顺序反过来：等闸门说 ok 了再加载数据。
    render();

    // 闸门放行只需要处理**一次**。
    //
    // 早先这里踩过坑：外层「放行前先拉一次 + 放行后再刷新一次」的写法，
    // 两处都调了 loadRoot()，结果开局就并发打两个 /api/scan/channels；
    // 事件再多来一次就翻倍。所以这里只留一个 done 开关，谁先到谁负责。
    var done = false;
    function startOnce() {
      if (done) return;
      done = true;
      loadUser();
      if (state.tab === 'drive') loadRoot();
      else loadSettingsPane();
    }

    // 闸门已经放行（已配对 TG）→ 立刻加载。
    // 两种信号都认：window 标记（防事件早于监听器发出）和 html 属性。
    if (window.__tgpanGateOk ||
        document.documentElement.getAttribute('data-tgpan-gate') === 'ok') {
      startOnce();
      return;
    }

    // 否则等闸门放行的事件
    window.addEventListener('tgpan:gate-ok', startOnce);

    // 兜底：闸门脚本没加载 / 闸门接口挂了，2 秒后自己来，
    // 别让用户对着空白界面干等。
    setTimeout(startOnce, 2000);
  }

  // ---------------------------------------------------------------------
  //  调试出口
  //
  //  整个 app.js 包在一个 IIFE 里（'use strict' + 不污染全局），
  //  代价是内部函数从外面够不着 —— 排查线上问题时没法在控制台里
  //  手工调一次 silentRefresh()、看一眼 state，或者手动弹个 toast 试样式。
  //
  //  所以在这里挑一批"排查时真正用得上"的挂到 window.__tgpan 上。
  //  只读为主，不做成 API —— 它没有稳定性承诺，纯给调试用。
  //  ---------------------------------------------------------------------
  window.__tgpan = {
    state: state,
    toast: toast,
    toastOk: toastOk,
    toastWarn: toastWarn,
    toastErr: toastErr,
    TOAST_MS: TOAST_MS,
    sameFiles: sameFiles,
    doLoad: doLoad,
    loadRoot: loadRoot,
    loadDir: loadDir,
    silentRefresh: silentRefresh,
    isUserIdle: isUserIdle,
    markActive: markActive,
    loadScanChannelList: loadScanChannelList,
    isModalOpen: isModalOpen,
    version: '2.7.0',
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
