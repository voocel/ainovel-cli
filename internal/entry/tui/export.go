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

// submitExport 导出已确认正文（/export <路径>）：格式由扩展名决定，已有文件不覆盖。
func (m model) submitExport(path string) (tea.Model, tea.Cmd) {
	if m.bench.exporting {
		m.bench.notice = "正在导出，请稍候"
		return m, nil
	}
	if len(m.bench.snap.Manuscript) == 0 {
		m.bench.notice = "暂无已确认正文，先批准稿件后再导出"
		return m, nil
	}
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if format != "txt" && format != "epub" {
		m.bench.err = "用法：/export 文件路径，以 .txt 或 .epub 结尾"
		return m, nil
	}
	api, ctx, gen := m.api, m.ctx, m.bench.gen
	command := novel.ExportCommand{ProjectID: m.bench.projectID, Revision: m.bench.snap.Revision, Path: path, Format: format}
	m.bench.exporting = true
	m.bench.err = ""
	m.bench.notice = fmt.Sprintf("正在导出 %s…", strings.ToUpper(format))
	return m, func() tea.Msg {
		result, err := api.Novels.Export(ctx, command)
		return exportDoneMsg{gen: gen, result: result, err: err}
	}
}
