package main

import buildversion "github.com/voocel/ainovel-cli/internal/version"

// versionInfo 解析构建期 ldflags 注入的版本信息（与 CLI 同源）。
func versionInfo() buildversion.Info {
	return buildversion.Resolve(buildversion.Info{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
}
