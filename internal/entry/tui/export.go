package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/app/novel"
)

type exportDoneMsg struct {
	gen    int
	result novel.ExportResult
	err    error
}

func (m model) submitExport() (tea.Model, tea.Cmd) {
	path := strings.TrimSpace(m.bench.prompt.input.Value())
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if format != "txt" && format != "epub" {
		m.bench.err = "请输入以 .txt 或 .epub 结尾的文件路径"
		return m, nil
	}
	api, ctx, gen := m.api, m.ctx, m.bench.gen
	command := novel.ExportCommand{ProjectID: m.bench.projectID, Revision: m.bench.snap.Revision, Path: path, Format: format}
	m.bench.prompt = nil
	m.bench.exporting = true
	m.bench.err = ""
	m.bench.notice = fmt.Sprintf("正在导出 %s…", strings.ToUpper(format))
	return m, func() tea.Msg {
		result, err := api.Novels.Export(ctx, command)
		return exportDoneMsg{gen: gen, result: result, err: err}
	}
}
