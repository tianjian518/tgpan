#!/bin/bash
# ==========================================================================
#  本地验证脚本：构建镜像 → 启动容器 → 检查服务是否正常
#  用法：bash scripts/verify.sh
# ==========================================================================
set -e

IMAGE="${IMAGE:-tgpan:test}"
CONTAINER="${CONTAINER:-tgpan-test}"
PORT="${PORT:-18080}"

echo "==> 1. 构建镜像 $IMAGE"
docker build --build-arg TELDRIVE_VERSION=1.8.3 -t "$IMAGE" .

echo "==> 2. 清理旧容器"
docker rm -f "$CONTAINER" 2>/dev/null || true

echo "==> 3. 启动容器（端口 $PORT）"
docker run -d --name "$CONTAINER" -p "$PORT:8080" "$IMAGE"

echo "==> 4. 等待服务就绪（最多 120 秒）"
ok=0
for i in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$PORT/api/version" >/dev/null 2>&1; then
    ok=1
    echo "    服务已就绪（第 ${i} 次探测）"
    break
  fi
  sleep 2
done

echo "==> 5. 验证结果"
echo "    /api/version →"
curl -s "http://127.0.0.1:$PORT/api/version" || true
echo
echo "    Web UI →"
curl -s -o /dev/null -w "    HTTP %{http_code}\n" "http://127.0.0.1:$PORT/" || true

if [ "$ok" = "1" ]; then
  echo
  echo "✅ 验证通过！浏览器访问 http://127.0.0.1:$PORT"
else
  echo
  echo "❌ 服务未就绪，容器日志："
  docker logs --tail 50 "$CONTAINER"
  exit 1
fi
