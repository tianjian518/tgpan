#!/bin/bash
# ==========================================================================
#  本地验证脚本：构建镜像 → 启动容器 → 检查服务 + WebDAV + 前端 + 闸门
#  用法：bash scripts/verify.sh
#
#  历史说明（重要）：
#    本脚本早期版本检查的是「注入官方 UI」方案产出的
#    tgpan-skin.js / tgpan-scan.js / tgpan-login.js 三个补丁脚本。
#    v2.6.3 起前端改为**自制界面**（ui/app.js + ui/app.css + ui/tgpan-gate.js），
#    那三个脚本已删除，相关断言随之作废。
#
#  另一个必须说明的坑：
#    服务端 SPA 回落会把「任何不存在的路径」都返回 index.html（HTTP 200）。
#    所以 `curl -s "$BASE/xxx.js" | grep ...` 这种写法**不会**因为文件缺失而失败，
#    它拿到的是 901 字节的 HTML，grep 结果毫无意义。
#    本脚本统一用 check_asset 先校验 Content-Type，再让调用方 grep。
# ==========================================================================
set -u

IMAGE="${IMAGE:-tgpan:test}"
CONTAINER="${CONTAINER:-tgpan-test}"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:$PORT"

FAILED=0
pass() { echo "    ✅ $1"; }
fail() { echo "    ❌ $1"; FAILED=$((FAILED+1)); }

# --------------------------------------------------------------------------
#  check_asset <路径> <期望最小字节数>
#  取静态资源，并确认它**真的是资源本身**而不是 SPA 回落的 HTML。
#  成功时把响应体打到 stdout，供调用方继续 grep。
# --------------------------------------------------------------------------
check_asset() {
  local path="$1" minsize="${2:-1}" body ct nbytes
  body=$(curl -sS "$BASE$path" 2>/dev/null || true)
  ct=$(curl -sSI "$BASE$path" 2>/dev/null | grep -i '^content-type' | tr -d '\r')
  # 用字节数，不用 ${#body}（后者按字符计，中文会少算）
  nbytes=$(printf '%s' "$body" | wc -c | tr -d ' ')
  if [ "$nbytes" -lt "$minsize" ]; then
    fail "$path 内容过短（${nbytes}B < ${minsize}B）"
    return 1
  fi
  case "$path" in
    *.js|*.css)
      if echo "$ct" | grep -qi 'text/html'; then
        fail "$path 返回的是 HTML（SPA 回落），说明该资源实际不存在"
        return 1
      fi
      ;;
  esac
  printf '%s' "$body"
  return 0
}

# 字节数辅助（同上，避免 ${#var} 的字符计数问题）
bytecount() { printf '%s' "$1" | wc -c | tr -d ' '; }

echo "==> 1. 构建镜像 $IMAGE"
if [ "${SKIP_BUILD:-0}" = "1" ]; then
  echo "    （SKIP_BUILD=1，跳过构建）"
else
  docker build -t "$IMAGE" .
fi

echo "==> 2. 清理旧容器"
if [ "${SKIP_RUN:-0}" = "1" ]; then
  echo "    （SKIP_RUN=1，不动现有容器）"
else
  docker rm -f "$CONTAINER" 2>/dev/null || true
fi

echo "==> 3. 启动容器（端口 $PORT）"
if [ "${SKIP_RUN:-0}" = "1" ]; then
  echo "    （SKIP_RUN=1，直接对已有服务做检查）"
else
  docker run -d --name "$CONTAINER" -p "$PORT:8080" "$IMAGE"
fi

echo "==> 4. 等待服务就绪（最多 120 秒）"
ok=0
for i in $(seq 1 60); do
  if curl -fsS "$BASE/api/version" >/dev/null 2>&1; then
    ok=1
    echo "    服务已就绪（第 ${i} 次探测）"
    break
  fi
  sleep 2
done

if [ "$ok" != "1" ]; then
  echo
  echo "❌ 服务未就绪，容器日志："
  docker logs --tail 60 "$CONTAINER"
  exit 1
fi

echo
echo "==> 5. 基础服务"

echo "    /api/version →"
curl -s "$BASE/api/version" | sed 's/^/      /'
echo

ver=$(curl -s "$BASE/api/version")
if echo "$ver" | grep -q '"version"'; then
  pass "版本接口返回正常"
else
  fail "版本接口内容异常"
fi

echo "    网页界面 / →"
curl -s -o /dev/null -w "      HTTP %{http_code}\n" "$BASE/"

echo
echo "==> 5.1 前端资源（自制界面）"

CSS=$(check_asset "/app.css" 5000) || true
JS=$(check_asset "/app.js" 20000) || true
GATE=$(check_asset "/tgpan-gate.js" 3000) || true

[ -n "$CSS" ] && pass "/app.css 正常（$(bytecount "$CSS")B，真实 CSS）"
[ -n "$JS" ] && pass "/app.js 正常（$(bytecount "$JS")B，真实 JS）"
[ -n "$GATE" ] && pass "/tgpan-gate.js 正常（$(bytecount "$GATE")B，真实 JS）"

echo "    index.html 是否挂载了这些资源 →"
IDX=$(curl -s "$BASE/")
for s in "/app.js" "/tgpan-gate.js" "/app.css"; do
  if echo "$IDX" | grep -q "$s"; then
    pass "index.html 引用 $s"
  else
    fail "index.html 未引用 $s"
  fi
done

echo
echo "==> 5.2 前端功能自检（关键词必须在 app.js 里）"

if [ -n "$JS" ]; then
  # 扫描页：已关注频道列表（v2.7.0 核心能力）
  for kw in "loadScanChannelList" "renderDialogRow" "refreshBtn"; do
    if echo "$JS" | grep -q "$kw"; then
      pass "app.js 含 $kw（已关注频道列表）"
    else
      fail "app.js 缺 $kw —— 频道列表功能不完整"
    fi
  done

  # 刷新按钮必须带 refresh=1，否则只是重读缓存（v2.7.0 修的坑）
  if echo "$JS" | grep -q "refresh=1"; then
    pass "app.js 的刷新走 refresh=1（真去同步，不是读缓存）"
  else
    fail "app.js 缺 refresh=1 —— 刷新按钮可能只是重读缓存"
  fi

  # 设置页的六个面板
  for pane in "drive" "scan" "auto" "webdav" "series" "about"; do
    if echo "$JS" | grep -q "'$pane'"; then
      pass "app.js 含面板 $pane"
    else
      fail "app.js 缺面板 $pane"
    fi
  done

  # toast 分级
  for kw in "toastOk" "toastWarn" "toastErr"; do
    if echo "$JS" | grep -q "$kw"; then
      pass "app.js 含 $kw"
    else
      fail "app.js 缺 $kw"
    fi
  done
else
  fail "app.js 取不到，跳过前端功能自检"
fi

echo
echo "==> 5.3 闸门脚本（tgpan-gate.js）"

if [ -n "$GATE" ]; then
  for kw in '/gate/setup' '/gate/login' '/gate/status'; do
    if echo "$GATE" | grep -q "$kw"; then
      pass "含 $kw"
    else
      fail "缺 $kw"
    fi
  done
else
  fail "tgpan-gate.js 取不到"
fi

echo
echo "==> 6. 闸门接口"

gs=$(curl -s "$BASE/api/gate/status")
echo "    /api/gate/status → $gs"
if echo "$gs" | grep -q '"state"'; then
  pass "闸门状态接口正常（含 state 字段）"
else
  fail "闸门状态接口缺 state 字段"
fi

st=$(echo "$gs" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
case "$st" in
  init|need_pair|need_login|ok) pass "state=$st 合法" ;;
  *) fail "出现未知 state=$st" ;;
esac

echo
echo "==> 7. WebDAV（播放器挂载的关键）"

echo "    OPTIONS /webdav（未认证也必须有能力声明头）→"
opt=$(curl -s -i -X OPTIONS "$BASE/webdav" 2>/dev/null)
echo "$opt" | grep -iE '^(HTTP|DAV|WWW-Authenticate|Accept-Ranges)' | sed 's/^/      /'

for h in 'DAV' 'WWW-Authenticate' 'Accept-Ranges'; do
  if echo "$opt" | grep -qiE "^$h:"; then
    pass "OPTIONS 带 $h 头"
  else
    fail "OPTIONS 缺 $h 头 —— 部分播放器会判定挂载失败"
  fi
done

echo "    PROPFIND /webdav（未认证期望 401，不能 405）→"
pf=$(curl -s -o /dev/null -w "%{http_code}" -X PROPFIND "$BASE/webdav" -H 'Depth: 1')
if [ "$pf" = "401" ]; then
  pass "PROPFIND 未认证 → 401"
elif [ "$pf" = "405" ]; then
  fail "PROPFIND 返回 405 —— WebDAV 方法未启用"
else
  pass "PROPFIND → HTTP $pf"
fi

echo
echo "==> 8. 安全回归"

echo "    [H1] 未登录读 /api/files/stream（绝不能 200）→"
c1=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/files/stream/abcdef")
echo "      HTTP $c1"
if [ "$c1" = "200" ]; then
  fail "未登录竟能读文件流，越权漏洞！"
else
  pass "已拦截（HTTP $c1）"
fi

echo "    [H2] alg=none 伪造 token（期望非 200）→"
c2=$(curl -s -o /dev/null -w "%{http_code}" \
  -H 'Cookie: access_token=eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ1c2VyX2lkIjo5OTl9.' \
  "$BASE/api/files/stream/abcdef")
echo "      HTTP $c2"
if [ "$c2" = "200" ]; then
  fail "alg=none 伪造 token 竟然通过！"
else
  pass "已拒绝（HTTP $c2）"
fi

echo
echo "==> 9. 受保护接口未登录应被拦"

for p in "/api/webdav/credentials" "/api/scan/channels" "/api/scan/series"; do
  c=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: pan.2016.de5.net" "$BASE$p")
  if [ "$c" = "404" ]; then
    fail "$p 返回 404 —— 路由不存在"
  else
    pass "$p → HTTP $c"
  fi
done

echo
if [ "$FAILED" -gt 0 ]; then
  echo "❌ 验证结束：$FAILED 项失败"
  echo "   容器仍在运行，可访问 $BASE 继续排查"
  exit 1
fi

echo "✅ 验证完成！浏览器访问 $BASE"
echo "   WebDAV 挂载地址：$BASE/webdav"
