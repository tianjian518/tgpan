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
