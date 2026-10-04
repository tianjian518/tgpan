#!/bin/bash
# ==========================================================================
#  本地验证脚本：构建镜像 → 启动容器 → 检查服务 + WebDAV 是否正常
#  用法：bash scripts/verify.sh
#
#  检查项：
#    1. 镜像能否构建成功
#    2. 容器能否启动、服务是否就绪
#    3. 网页界面是否可访问
#    4. WebDAV 端点是否响应（OPTIONS 应返回 DAV: 1, 2 头）
#    5. 新增 API 路由是否挂载（未登录应返回 401）
# ==========================================================================
set -e

IMAGE="${IMAGE:-tgpan:test}"
CONTAINER="${CONTAINER:-tgpan-test}"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:$PORT"

echo "==> 1. 构建镜像 $IMAGE"
docker build --build-arg TELDRIVE_VERSION=1.8.3 -t "$IMAGE" .

echo "==> 2. 清理旧容器"
docker rm -f "$CONTAINER" 2>/dev/null || true

echo "==> 3. 启动容器（端口 $PORT）"
docker run -d --name "$CONTAINER" -p "$PORT:8080" "$IMAGE"

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
echo "==> 5. 验证结果"

echo "    /api/version →"
curl -s "$BASE/api/version" || true
echo
echo

echo "    网页界面 / →"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "$BASE/"

echo "    前端皮肤 /tgpan-skin.js →"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "$BASE/tgpan-skin.js"

echo "    前端控制台 /tgpan-scan.js →"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "$BASE/tgpan-scan.js"

echo
echo "    WebDAV OPTIONS /webdav（DAV 头是爆米花挂载的关键）→"
curl -s -i -X OPTIONS "$BASE/webdav" 2>/dev/null | grep -iE '^(HTTP|DAV|MS-Author-Via)' | sed 's/^/    /'

echo
echo "    WebDAV PROPFIND（未带凭据，期望 401 而非 405）→"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" -X PROPFIND "$BASE/webdav" -H 'Depth: 1'

echo
echo "    新增 API /api/webdav/credentials（未登录期望 401 JSON）→"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "$BASE/api/webdav/credentials"

echo "    新增 API /api/scan/channels（未登录期望 401 JSON）→"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "$BASE/api/scan/channels"

echo "    剧集列表 /api/scan/series（未登录期望 401 JSON）→"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "$BASE/api/scan/series"

echo "    剧集改名 /api/scan/series/rename（未登录期望 401 JSON）→"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" -X POST \
  -H 'Content-Type: application/json' -d '{}' "$BASE/api/scan/series/rename"

echo
echo "    剧集页签是否已嵌入前端 →"
if curl -s "$BASE/tgpan-scan.js" | grep -q 'data-tab="series"'; then
  echo "    ✅ 找到「🎬 剧集」页签"
else
  echo "    ❌ 前端控制台里没有剧集页签，检查 ui/dist/tgpan-scan.js 是否已嵌入"
  exit 1
fi

echo
echo "==> 5.1 v2.5.0 安全与前端修复自检"

echo "    [H1] 越权读取 /api/files/stream（未登录期望 401，绝不能 200）→"
code_stream=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/files/stream/abcdef")
echo "    HTTP $code_stream"
if [ "$code_stream" = "200" ]; then
  echo "    ❌ 未登录竟能读取文件流，越权漏洞未修复！"
  exit 1
fi

echo "    [H2] alg=none 伪造 token（期望 401）→"
code_none=$(curl -s -o /dev/null -w "%{http_code}" \
  -H 'Cookie: access_token=eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ1c2VyX2lkIjo5OTl9.' \
  "$BASE/api/files/stream/abcdef")
echo "    HTTP $code_none"
if [ "$code_none" = "200" ]; then
  echo "    ❌ alg=none 伪造 token 竟然通过！"
  exit 1
fi

echo "    [F2/F3] 前端皮肤修复是否生效 →"
skin=$(curl -s "$BASE/tgpan-skin.js")
f3_ok=1
f2_ok=1
# F3：不得再出现裸的全局输入框选择器
if echo "$skin" | grep -qE "^\s*'input, textarea, select\{',"; then
  echo "    ❌ F3 未修复：仍有全局 input/textarea/select 选择器（会污染原版界面）"
  f3_ok=0
fi
# F2：不得再出现 #tgpan-help（该 id 不存在，是死代码）
if echo "$skin" | grep -qE "^\s*'\.dark #tgpan-help"; then
  echo "    ❌ F2 未修复：仍有失效的 #tgpan-help 选择器"
  f2_ok=0
fi
if [ "$f3_ok" = "1" ] && echo "$skin" | grep -q '#tgpan-mask input'; then
  echo "    ✅ F3 已修复（输入框样式已限定在 #tgpan-mask 内）"
fi
if [ "$f2_ok" = "1" ] && echo "$skin" | grep -q '\.tgpan-help'; then
  echo "    ✅ F2 已修复（深色模式帮助块用 .tgpan-help）"
fi

echo "    [F1/F5/F6] 前端控制台修复是否生效 →"
scan=$(curl -s "$BASE/tgpan-scan.js")
for needle in "aria-selected" "activateTab" "openEpisodeEditor" "cssEsc" "emptyState"; do
  if echo "$scan" | grep -q "$needle"; then
    echo "    ✅ 含 $needle"
  else
    echo "    ❌ 缺少 $needle"
    exit 1
  fi
done

echo
echo "==> 5.2 v2.6.0 专项检查"

echo "    [Bot 页签] 前端是否含「🤖 Bot 加速」→"
if curl -s "$BASE/tgpan-scan.js" | grep -q 'data-tab="bots"'; then
  echo "    ✅ 找到 Bot 页签"
else
  echo "    ❌ 前端缺 Bot 页签"
  exit 1
fi

echo "    [Bot 接口] 三个接口必须存在（未登录期望 401，不能是 404）→"
for spec in "GET:/users/config" "POST:/users/bots" "DELETE:/users/bots"; do
  m="${spec%%:*}"; p="${spec#*:}"
  code=$(curl -s -o /dev/null -w "%{http_code}" -X "$m" \
    -H 'Content-Type: application/json' -d '{"bots":["x"]}' "$BASE/api$p")
  if [ "$code" = "404" ]; then
    echo "    ❌ $m $p 返回 404 —— 路由不存在，Bot 功能不可用"
    exit 1
  fi
  echo "    ✅ $m $p → HTTP $code"
done

echo "    [Bot 路径正确性] 前端不能引用不存在的 /users/stats →"
if curl -s "$BASE/tgpan-scan.js" | grep -q "'/users/stats'"; then
  echo "    ❌ 前端仍在调用不存在的 /users/stats（正确路径是 /users/config）"
  exit 1
fi
echo "    ✅ 前端使用的是 /users/config"

echo "    [Token 格式校验] 前端必须含格式校验正则 →"
if curl -s "$BASE/tgpan-scan.js" | grep -q 'BOT_TOKEN_RE'; then
  echo "    ✅ 含 Token 格式校验"
else
  echo "    ❌ 缺 Token 格式校验，用户可能提交无效 Token"
  exit 1
fi

echo "    [WebDAV 能力声明] 未认证的 OPTIONS 也必须带 DAV 头 →"
# 播放器（爆米花/Infuse/WinSCP/Windows 资源管理器）挂载前会先发不带凭据的
# OPTIONS，靠响应里的 DAV 头判断「对面是不是 WebDAV 服务器」。
# 如果 401 响应里没有 DAV 头，部分客户端会直接判挂载失败，连密码框都不弹。
# 正确姿势：401 + WWW-Authenticate（标准质询） + DAV/Allow/Accept-Ranges（能力声明）
opt_headers=$(curl -s -i -X OPTIONS "$BASE/webdav" 2>/dev/null)
opt_dav=$(echo "$opt_headers" | grep -ciE '^DAV:')
opt_auth=$(echo "$opt_headers" | grep -ciE '^WWW-Authenticate:')
opt_ranges=$(echo "$opt_headers" | grep -ciE '^Accept-Ranges:')
if [ "$opt_dav" -eq 0 ]; then
  echo "    ❌ 未认证 OPTIONS 缺 DAV 头 —— 播放器会判定「不是 WebDAV 服务器」而挂载失败"
  exit 1
fi
if [ "$opt_auth" -eq 0 ]; then
  echo "    ❌ 未认证 OPTIONS 缺 WWW-Authenticate 头 —— 客户端不知道该带 Basic 凭据重试"
  exit 1
fi
if [ "$opt_ranges" -eq 0 ]; then
  echo "    ❌ 未认证 OPTIONS 缺 Accept-Ranges: bytes —— 播放器可能不支持拖进度条"
  exit 1
fi
echo "    ✅ DAV / WWW-Authenticate / Accept-Ranges 三个头齐全（爆米花可正常探测）"

echo
echo "==> 6. 剧集识别引擎自测（不依赖网络）"
echo "    在容器内跑单元测试…"
if docker exec "$CONTAINER" sh -c 'command -v go >/dev/null 2>&1' 2>/dev/null; then
  docker exec "$CONTAINER" sh -c 'cd /app && go test ./pkg/services/ -run "Episode|Resolve|Caption|ChineseNum" 2>&1' | sed 's/^/    /'
else
  echo "    （运行镜像里没有 Go 工具链，跳过 —— 属正常，源码目录已附独立测试）"
fi

echo
echo "✅ 验证完成！浏览器访问 $BASE"
echo "   WebDAV 挂载地址：$BASE/webdav"
