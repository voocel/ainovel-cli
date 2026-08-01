// Command ainovel-desktop 是 ainovel 的桌面应用入口（Wails v2）。
//
// 它是 host.Host 的第三个消费者，与终端 TUI（internal/entry/tui）和 headless
// （internal/entry/headless）并列。引擎、存储、断点恢复等核心一律复用，桌面层只做
// “动作绑定 + 事件投影”：把前端调用翻译成 Host 方法，把 Host 的 Events/Stream/Done
// 三条 channel 投影成 Wails 运行时事件。见 docs 计划与 app.go。
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// 版本信息由构建期 ldflags 注入（-X main.version=...），与 CLI 同源；About 弹窗复用。
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

//go:embed all:frontend/dist
var frontendAssets embed.FS

func main() {
	app := NewApp(versionInfo())

	err := wails.Run(&options.App{
		Title:     "AINovel Studio · AI 小说创作",
		Width:     1280,
		Height:    860,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: frontendAssets,
		},
		OnStartup:     app.OnStartup,
		OnShutdown:    app.OnShutdown,
		OnBeforeClose: app.OnBeforeClose,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		println("fatal:", err.Error())
	}
}
