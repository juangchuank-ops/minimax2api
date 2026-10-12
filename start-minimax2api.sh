#!/bin/bash
# ===========================================================
#  MiniMax2API - macOS / Linux 启动脚本（仅本机）
#
#  用法：
#     ./start-minimax2api.sh                    # 默认端口 8080
#     ./start-minimax2api.sh 9000               # 指定端口
#     ./start-minimax2api.sh 9000 你的密码      # 指定端口 + 初始管理员密码
#     ./start-minimax2api.sh --rebuild          # 先重新编译前后端再启动
#
#  只监听 127.0.0.1，同网段的手机 / 其他电脑访问不到。
#  需要局域网访问请用 start-minimax2api-lan.sh。
# ===========================================================

set -u

usage() {
  cat <<'EOF'
用法: ./start-minimax2api.sh [选项] [端口] [初始管理员密码]

选项:
  -b, --rebuild   启动前重新构建前端与后端
  -h, --help      显示本帮助

说明:
  端口默认 8080，只监听 127.0.0.1。
  初始管理员密码只在首次启动（data/app.json 不存在）时生效；
  之后密码以 data/app.json 里保存的为准，密码忘了就删掉该文件重新初始化。
  不传密码时首次启动会随机生成一个，并打印在下方日志里。
EOF
}

REBUILD=0
POSITIONAL=()
for arg in "$@"; do
  case "$arg" in
    -b|--rebuild) REBUILD=1 ;;
    -h|--help) usage; exit 0 ;;
    --) ;;
    *) POSITIONAL+=("$arg") ;;
  esac
done

PORT="${POSITIONAL[0]:-8080}"
PASSWORD="${POSITIONAL[1]:-}"

case "$PORT" in
  ''|*[!0-9]*)
    echo "[错误] 端口不合法：$PORT"
    echo "       用法: ./start-minimax2api.sh [端口] [初始管理员密码]"
    exit 1
    ;;
esac
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
  echo "[错误] 端口超出范围（1-65535）：$PORT"
  exit 1
fi

# --- 解析脚本自身所在目录（双击、./ 执行、软链都能work）--------------
SOURCE="${BASH_SOURCE[0]}"
while [ -L "$SOURCE" ]; do
  DIR="$(cd -P "$(dirname "$SOURCE")" && pwd)"
  SOURCE="$(readlink "$SOURCE")"
  case "$SOURCE" in
    /*) ;;
    *) SOURCE="$DIR/$SOURCE" ;;
  esac
done
HERE="$(cd -P "$(dirname "$SOURCE")" && pwd)"

BIN="$HERE/minimax2api"
STATIC="$HERE/frontend/dist"
# 与后端 config.Load 一致：环境变量优先，其次才是相对仓库根目录的 data/。
# 相对路径后端按工作目录解析，这里统一成绝对路径再展示，避免歧义。
DATA="${MINIMAX2API_DATA:-$HERE/data}"
case "$DATA" in
  /*) ;;
  *) DATA="$HERE/$DATA" ;;
esac

# --- 找 Go（只在需要编译时用到）-------------------------------------
find_go() {
  candidate=""
  for candidate in "$HOME/.local/opt/go/bin/go" /usr/local/go/bin/go \
                   /usr/lib/go/bin/go /opt/homebrew/bin/go go; do
    if [ -x "$candidate" ] || command -v "$candidate" >/dev/null 2>&1; then
      if "$candidate" version >/dev/null 2>&1; then
        echo "$candidate"
        return 0
      fi
    fi
  done
  return 1
}

# --- 找 node（用于浏览器登录桥，可选）--------------------------------
find_node() {
  candidate=""
  for candidate in node "$HOME/.local/node/bin/node" /usr/local/bin/node \
                   /opt/homebrew/bin/node; do
    if [ -x "$candidate" ] || command -v "$candidate" >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

# --- 浏览器登录桥（可选，缺 node / chrome 时优雅降级）------------------
# 桥驱动一个真实 Chrome 抓 MiniMax 登录后的 _token，支撑管理台的
# 「浏览器登录」。桥起不来不影响主服务，只是那个菜单项会报「服务不可用」。
BRIDGE_PORT="${BRIDGE_PORT:-8766}"
BRIDGE_PID=""

find_chrome() {
  candidate=""
  for candidate in "${CHROME_BIN:-}" \
                   "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
                   "/Applications/Chromium.app/Contents/MacOS/Chromium" \
                   "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge" \
                   /usr/bin/google-chrome /usr/bin/chromium /snap/bin/chromium \
                   google-chrome google-chrome-stable \
                   chromium chromium-browser; do
    [ -n "$candidate" ] || continue
    if [ -x "$candidate" ] || command -v "$candidate" >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

start_bridge() {
  local nodeexe chromexe
  nodeexe="$(find_node)" || {
    echo "[提示] 未找到 Node，管理台「浏览器登录」将不可用（需 Node 22+）"
    return 0
  }
  chromexe="$(find_chrome)" || {
    echo "[提示] 未找到 Chrome，管理台「浏览器登录」将不可用（可设 CHROME_BIN 指定路径）"
    return 0
  }
  CHROME_BIN="$chromexe" BRIDGE_PORT="$BRIDGE_PORT" \
    "$nodeexe" "$HERE/tools/browser-login-bridge.mjs" --port "$BRIDGE_PORT" \
    >/dev/null 2>&1 &
  BRIDGE_PID=$!
  echo "[提示] 浏览器登录桥已启动（127.0.0.1:$BRIDGE_PORT，Chrome: $chromexe）"
  return 0
}

stop_bridge() {
  if [ -n "$BRIDGE_PID" ] && kill -0 "$BRIDGE_PID" 2>/dev/null; then
    kill "$BRIDGE_PID" 2>/dev/null
    wait "$BRIDGE_PID" 2>/dev/null
  fi
}

# --- 找 npm（只在需要构建前端时用到）--------------------------------
find_npm() {
  candidate=""
  for candidate in npm "$HOME/.local/node/bin/npm" /usr/local/bin/npm \
                   /opt/homebrew/bin/npm; do
    if [ -x "$candidate" ] || command -v "$candidate" >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

build_backend() {
  GOEXE="$(find_go)" || {
    echo "[错误] 没找到 Go 工具链，无法编译后端。"
    echo "       需要 Go 1.24+。装好后重跑本脚本，或手动编译："
    echo "         cd backend && go build -o ../minimax2api ./cmd/minimax2api"
    return 1
  }
  echo "[构建] 后端 -> $BIN"
  ( cd "$HERE/backend" && "$GOEXE" build -o "$BIN" ./cmd/minimax2api ) || {
    echo "[错误] 后端编译失败。"
    return 1
  }
  return 0
}

build_frontend() {
  NPMEXE="$(find_npm)" || {
    echo "[警告] 没找到 npm，跳过前端构建，服务将只提供 API。"
    return 1
  }
  echo "[构建] 前端 -> $STATIC"
  (
    cd "$HERE/frontend" || exit 1
    if [ ! -d node_modules ]; then
      echo "[构建] 首次运行，安装前端依赖..."
      "$NPMEXE" install --no-audit --no-fund || exit 1
    fi
    "$NPMEXE" run build || exit 1
  ) || {
    echo "[警告] 前端构建失败，界面可能不是最新的。"
    return 1
  }
  return 0
}

if [ "$REBUILD" = 1 ]; then
  build_frontend
  build_backend || exit 1
else
  if [ ! -x "$BIN" ]; then
    echo "[提示] 未找到可执行文件，先编译一次。"
    build_backend || exit 1
  fi
  if [ ! -f "$STATIC/index.html" ]; then
    echo "[提示] 未找到前端产物，先构建一次。"
    build_frontend
  fi
fi

if [ ! -x "$BIN" ]; then
  echo "[错误] 可执行文件仍不存在：$BIN"
  exit 1
fi

# 数据目录与静态资源都用相对路径解析，所以必须先切到仓库根目录。
cd "$HERE" || exit 1

echo "==========================================================="
echo "  MiniMax2API"
echo
echo "  管理台    : http://127.0.0.1:$PORT/"
echo "  API       : http://127.0.0.1:$PORT/v1"
echo "  数据目录  : $DATA"
echo
if [ -f "$DATA/app.json" ]; then
  echo "  管理员密码: 沿用 data/app.json 中已有的设置"
elif [ -n "$PASSWORD" ]; then
  echo "  管理员密码: 使用你传入的密码（用户名 ${MINIMAX2API_ADMIN_USER:-admin}）"
else
  echo "  管理员密码: 首次启动随机生成，见下方日志"
fi
if [ ! -f "$STATIC/index.html" ]; then
  echo
  echo "  ⚠ 没有前端产物，本次只提供 API（无法打开管理台）"
fi
echo
echo "  关掉这个窗口服务就会停止，按 Ctrl+C 退出。"
echo "==========================================================="
echo

# 主服务 Ctrl+C 退出时，一并停掉浏览器登录桥。
trap stop_bridge EXIT

start_bridge

if [ -n "$PASSWORD" ]; then
  MINIMAX2API_ADMIN_PASSWORD="$PASSWORD" \
  MINIMAX2API_BROWSER_LOGIN_BRIDGE="http://127.0.0.1:$BRIDGE_PORT" \
    "$BIN" -addr "127.0.0.1:$PORT"
else
  MINIMAX2API_BROWSER_LOGIN_BRIDGE="http://127.0.0.1:$BRIDGE_PORT" \
    "$BIN" -addr "127.0.0.1:$PORT"
fi
STATUS=$?

echo
echo "[服务已退出]"
exit $STATUS
