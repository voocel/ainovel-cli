#!/bin/sh
# ainovel-cli 一键安装脚本
#
#   curl -fsSL https://raw.githubusercontent.com/voocel/ainovel-cli/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/voocel/ainovel-cli/v1.0.0/scripts/install.sh | sh -s -- v1.0.0
#
# 自定义安装目录： AINOVEL_INSTALL_DIR=~/.local/bin curl -fsSL ... | sh
# 指定版本：AINOVEL_VERSION=v1.0.0 curl -fsSL ... | sh
set -e

REPO="voocel/ainovel-cli"
BIN="ainovel-cli"
DEST="${AINOVEL_INSTALL_DIR:-/usr/local/bin}"
VERSION="${AINOVEL_VERSION:-${1:-latest}}"

for cmd in curl tar; do
	command -v "$cmd" >/dev/null 2>&1 || { echo "需要 $cmd，请先安装后重试"; exit 1; }
done

if command -v sha256sum >/dev/null 2>&1; then
	sha256_file() { sha256sum "$1" | awk '{print tolower($1)}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_file() { shasum -a 256 "$1" | awk '{print tolower($1)}'; }
else
	echo "需要 sha256sum 或 shasum，请先安装后重试"
	exit 1
fi

curl_https() {
	curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 "$@"
}

case "$(uname -s)" in
	Darwin) OS="Darwin" ;;
	Linux)  OS="Linux" ;;
	*) echo "不支持的系统 $(uname -s)；Windows 请到 https://github.com/$REPO/releases 手动下载"; exit 1 ;;
esac

case "$(uname -m)" in
	x86_64|amd64)  ARCH="x86_64" ;;
	arm64|aarch64) ARCH="arm64" ;;
	*) echo "不支持的架构 $(uname -m)"; exit 1 ;;
esac

if [ "$VERSION" = "latest" ] || [ -z "$VERSION" ]; then
	API="https://api.github.com/repos/$REPO/releases/latest"
	echo "查询最新版本..."
else
	case "$VERSION" in
		v*) TAG="$VERSION" ;;
		*) TAG="v$VERSION" ;;
	esac
	API="https://api.github.com/repos/$REPO/releases/tags/$TAG"
	echo "查询版本 $TAG..."
fi

RELEASE=$(curl_https "$API")
TAG=$(printf '%s\n' "$RELEASE" | grep '"tag_name"' | head -1 | cut -d '"' -f 4)
[ -n "$TAG" ] || { echo "release 缺少 tag_name，拒绝安装"; exit 1; }

RELEASE_VERSION=${TAG#v}
ASSET="${BIN}_${RELEASE_VERSION}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="${BIN}_checksums.txt"
BASE_URL="https://github.com/$REPO/releases/download/$TAG"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "下载 $ASSET"
curl_https -o "$TMP/$ASSET" "$BASE_URL/$ASSET"
curl_https -o "$TMP/$CHECKSUMS" "$BASE_URL/$CHECKSUMS"

EXPECTED=$(awk -v asset="$ASSET" '
	NF == 2 {
		name = $2
		sub(/^\*/, "", name)
		if (name == asset) {
			count++
			sum = tolower($1)
		}
	}
	END {
		if (count != 1) exit 1
		print sum
	}
' "$TMP/$CHECKSUMS") || { echo "checksum 清单中未唯一找到 $ASSET，拒绝安装"; exit 1; }

case "$EXPECTED" in
	*[!0-9a-f]*|'') echo "$ASSET 的 SHA256 格式非法，拒绝安装"; exit 1 ;;
esac
[ "${#EXPECTED}" -eq 64 ] || { echo "$ASSET 的 SHA256 长度非法，拒绝安装"; exit 1; }

ACTUAL=$(sha256_file "$TMP/$ASSET")
[ "$ACTUAL" = "$EXPECTED" ] || {
	echo "$ASSET SHA256 校验失败，拒绝安装"
	exit 1
}
echo "SHA256 校验通过"

CONTENTS=$(tar -tzf "$TMP/$ASSET") || { echo "无法读取安装包，拒绝安装"; exit 1; }
BIN_COUNT=$(printf '%s\n' "$CONTENTS" | awk -v bin="$BIN" '$0 == bin { count++ } END { print count + 0 }')
[ "$BIN_COUNT" -eq 1 ] || { echo "安装包中未唯一找到 $BIN，拒绝安装"; exit 1; }

mkdir "$TMP/extract"
tar -xOzf "$TMP/$ASSET" "$BIN" > "$TMP/extract/$BIN" || {
	echo "无法从安装包提取 $BIN，拒绝安装"
	exit 1
}
[ -s "$TMP/extract/$BIN" ] || { echo "安装包中的 $BIN 为空，拒绝安装"; exit 1; }
chmod 0755 "$TMP/extract/$BIN"

echo "安装到 $DEST"
[ -d "$DEST" ] || mkdir -p "$DEST" 2>/dev/null || sudo mkdir -p "$DEST"
if [ -w "$DEST" ]; then
	mv "$TMP/extract/$BIN" "$DEST/$BIN"
else
	echo "需要管理员权限写入 $DEST"
	sudo mv "$TMP/extract/$BIN" "$DEST/$BIN"
fi

echo "✓ 安装完成：$DEST/$BIN"
[ -n "$TAG" ] && echo "版本：$TAG"
command -v "$BIN" >/dev/null 2>&1 || echo "提示：$DEST 不在 PATH 中，请将其加入 PATH"
echo "运行 $BIN 开始使用"
