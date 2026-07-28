# ainovel 桌面开发环境。
#
# 工具路径由调用者的 PATH 与 `go env GOPATH` 决定，不写死本机用户名、盘符或
# winget 包目录。这样脚本可直接用于其他开发机和 CI。

# Wails 在 Windows 用 CGO 绑定 WebView2；允许调用方显式覆盖。
export CGO_ENABLED="${CGO_ENABLED:-1}"

# wails 通常安装在 GOPATH/bin。若它尚未进入 PATH，在 Go 可用时自动补上。
if command -v go >/dev/null 2>&1; then
  GOPATH_VALUE="$(go env GOPATH 2>/dev/null || true)"
  if [[ -n "$GOPATH_VALUE" ]]; then
    if command -v cygpath >/dev/null 2>&1; then
      GOPATH_VALUE="$(cygpath -u "$GOPATH_VALUE")"
    fi
    export PATH="$GOPATH_VALUE/bin:$PATH"
  fi
fi

for required in go gcc wails npm; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "缺少桌面构建依赖：$required（请先安装并加入 PATH）" >&2
    return 1 2>/dev/null || exit 1
  fi
done
