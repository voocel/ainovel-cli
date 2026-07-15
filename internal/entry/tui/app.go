package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/logger"
)

// Run 启动 TUI。
// 启动模式分层约定：
// 1. 快速模式、共创模式属于“启动编排”；
// 2. 正式创作会话进入 host.Host；
// 3. 未来若新增“续写已有小说”等共享模式，统一落到 internal/entry/startup。
func Run(cfg bootstrap.Config, bundle assets.Bundle, version, configPath string) error {
	rt, err := host.NewWithOptions(cfg, bundle, host.Options{ConfigPath: configPath})
	if err != nil {
		return err
	}
	bridge := newAskUserBridge()
	rt.AskUser().SetHandler(bridge.handler)
	cleanup := logger.SetupFile(rt.Dir(), "tui.log", false)
	defer cleanup()
	defer rt.Close()

	m := NewModel(rt, bridge, version)
	// 不在启动时全局开启鼠标上报：欢迎页用不到鼠标，关闭上报可保留终端原生
	// 拖拽选中复制。进入创作工作台（modeRunning）时再由 enterRunning 打开上报，
	// 以支持点击切面板 / 滚轮 / 拖拽侧边栏。
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
