package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/logger"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

// Run 启动 TUI。
// 启动模式分层约定：
// 1. 快速模式、共创模式属于“启动编排”；
// 2. 正式创作会话进入 orchestrator.Engine；
// 3. 未来若新增“续写已有小说”等共享模式，统一落到 internal/entry/startup。
func Run(cfg bootstrap.Config, bundle assets.Bundle) error {
	rt, err := orchestrator.NewRuntime(cfg, bundle)
	if err != nil {
		return err
	}
	bridge := newAskUserBridge()
	rt.AskUser().SetHandler(bridge.handler)
	cleanup := logger.SetupFile(rt.Dir(), "tui.log", false)
	defer cleanup()
	defer rt.Close()

	m := NewModel(rt, bridge)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}
