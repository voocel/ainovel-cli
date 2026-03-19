package tui

import (
	"io"
	"log"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

// Run 启动 TUI 模式。
func Run(cfg bootstrap.Config, bundle assets.Bundle) error {
	rt, err := orchestrator.NewRuntime(cfg, bundle)
	if err != nil {
		return err
	}
	bridge := newAskUserBridge()
	rt.AskUser().SetHandler(bridge.handler)
	restoreLog := redirectLogger(rt.Dir())
	defer restoreLog()
	defer rt.Close()

	m := NewModel(rt, bridge)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}

// redirectLogger 将标准日志重定向到文件，避免破坏 TUI 画面。
func redirectLogger(outputDir string) func() {
	prev := log.Writer()
	logPath := filepath.Join(outputDir, "meta", "tui.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		log.SetOutput(io.Discard)
		return func() { log.SetOutput(prev) }
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.SetOutput(io.Discard)
		return func() { log.SetOutput(prev) }
	}

	log.SetOutput(f)
	return func() {
		log.SetOutput(prev)
		_ = f.Close()
	}
}
