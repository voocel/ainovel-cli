#!/usr/bin/env bash
# 构建 ainovel 桌面应用（Windows）。
#
#   scripts/build-desktop.sh            # 出 .exe
#   scripts/build-desktop.sh --nsis     # 额外出 NSIS 安装包（需先装 nsis）
#
# 版本信息与 CLI 同源：走 ldflags 注入 main.version/commit/date，
# 应用内「关于」读 internal/version 解析后的结果。
set -euo pipefail

cd "$(dirname "$0")/.."
source scripts/dev-env.sh

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

echo "==> 版本 ${VERSION} (${COMMIT})"

# CLI 必须不受桌面构建影响，先验一遍。
echo "==> 校验 CLI 未受影响"
go build -o /dev/null ./cmd/ainovel-cli

echo "==> 构建桌面应用"
cd cmd/ainovel-desktop
wails build -clean -ldflags "${LDFLAGS}" "$@"

echo "==> 完成：cmd/ainovel-desktop/build/bin/"
ls -la build/bin/ 2>/dev/null || true
