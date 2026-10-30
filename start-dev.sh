#!/usr/bin/env bash
set -euo pipefail

APP_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$APP_ROOT"
if [[ ! -f .env.local ]]; then
  echo '缺少 .env.local。请复制 .env.local.example，设置 JWT_SECRET 和可用的本地代理。'
  exit 1
fi
set -a
source .env.local
set +a

JWT_SECRET="${JWT_SECRET:-}"
if [[ ${#JWT_SECRET} -lt 16 ]]; then
  echo 'JWT_SECRET 未设置或过短。请将 openssl rand -hex 32 的结果保存到 .env.local。'
  exit 1
fi
for cmd in go node curl; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "缺少运行依赖：$cmd"
    exit 1
  fi
done
if [[ ! -d frontend/node_modules ]]; then
  echo '缺少前端依赖，请先执行：cd frontend && npm ci'
  exit 1
fi

export SERVER_HOST="${SERVER_HOST:-127.0.0.1}"
export SERVER_PORT="${SERVER_PORT:-8080}"
export SERVER_MODE="${SERVER_MODE:-debug}"
export DB_PATH="${DB_PATH:-$APP_ROOT/data/cdk_recharge.db}"
export INSTALL_MODE="${INSTALL_MODE:-wizard}"
export NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}"
export VITE_API_TARGET="http://127.0.0.1:$SERVER_PORT"
FRONTEND_PORT="${FRONTEND_PORT:-5173}"
mkdir -p "$(dirname -- "$DB_PATH")" "$APP_ROOT/tmp"

BACKEND_PID=''
FRONTEND_PID=''
cleanup() {
  for pid in "$BACKEND_PID" "$FRONTEND_PID"; do
    if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; fi
  done
  for pid in "$BACKEND_PID" "$FRONTEND_PID"; do
    if [[ -n "$pid" ]]; then wait "$pid" 2>/dev/null || true; fi
  done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

(cd "$APP_ROOT/backend" && go build -o "$APP_ROOT/tmp/cdk-dev" ./cmd/server)
"$APP_ROOT/tmp/cdk-dev" &
BACKEND_PID=$!
READY=0
for ((i=0; i<60; i++)); do
  if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
    echo '后端启动失败，请查看上方日志。'
    exit 1
  fi
  if curl --noproxy '*' --silent --fail "$VITE_API_TARGET/health" >/dev/null; then
    READY=1
    break
  fi
  sleep 0.5
done
if [[ "$READY" != 1 ]]; then
  echo '后端未在预期时间内就绪。'
  exit 1
fi

(cd "$APP_ROOT/frontend" && exec node node_modules/vite/bin/vite.js --host 127.0.0.1 --port "$FRONTEND_PORT" --strictPort) &
FRONTEND_PID=$!
FRONTEND_READY=0
for ((i=0; i<60; i++)); do
  if ! kill -0 "$FRONTEND_PID" 2>/dev/null; then
    echo '前端启动失败，请查看上方日志。'
    exit 1
  fi
  if curl --noproxy '*' --silent --fail "http://127.0.0.1:$FRONTEND_PORT/" >/dev/null; then
    FRONTEND_READY=1
    break
  fi
  sleep 0.5
done
if [[ "$FRONTEND_READY" != 1 ]]; then
  echo '前端未在预期时间内就绪。'
  exit 1
fi
echo "前端：http://127.0.0.1:$FRONTEND_PORT/recharge"
echo "后端：${VITE_API_TARGET}（SQLite）"
echo '按 Ctrl+C 停止服务。'
while kill -0 "$BACKEND_PID" 2>/dev/null && kill -0 "$FRONTEND_PID" 2>/dev/null; do
  sleep 1
done
echo '服务已退出，正在停止另一进程。'
exit 1
