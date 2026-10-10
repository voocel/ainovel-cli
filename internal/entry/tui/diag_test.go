package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/app/diag"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

func diagnosticModel(t *testing.T) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book", 1)
	updated, _ := m.openDiagnostics()
	return updated.(model)
}

func TestDiagnosticsBusyAndStalePage(t *testing.T) {
	m := diagnosticModel(t)
	epoch := m.bench.diag.epoch
	ctx := m.bench.diag.ctx
	for _, key := range []string{"d", "r", "n", "e"} {
		_, cmd := m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd != nil {
			t.Fatalf("busy accepted %s", key)
		}
	}
	updated, _ := m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if ctx.Err() != context.Canceled {
		t.Fatal("leaving diagnostic page did not cancel read context")
	}
	updated, _ = m.openDiagnostics()
	m = updated.(model)
	updated, _ = m.applyDiagnostics(diagnosticsMsg{gen: 1, epoch: epoch, err: errors.New("old error")})
	m = updated.(model)
	if !m.bench.diag.busy || m.bench.diag.err != "" {
		t.Fatal("old page response affected reopened diagnosis")
	}
	updated, _ = m.applyDiagnosticsShared(diagnosticsSharedMsg{gen: 1, epoch: epoch, path: "old.json"})
	m = updated.(model)
	if m.bench.diag.notice != "" {
		t.Fatal("old export response affected reopened diagnosis")
	}
}

func TestDiagnosticsPaginationDrillAndCoverage(t *testing.T) {
	m := diagnosticModel(t)
	report := diag.Report{Header: diag.Header{Scope: diag.Request{ProjectID: "book", RunID: "run"}}, Summary: diag.Summary{State: "waiting_user"}, Operations: []diag.Operation{{ID: "op-1", Error: "原始失败信息"}, {ID: "op-2"}}, NextOperationID: "op-2", Coverage: []diag.Coverage{{Source: "usage", Status: "missing", Detail: "用量缺失，不等于零"}},
		Metrics: diag.Metrics{Usage: domainmodel.Usage{Input: 120000, Output: 9000, CacheRead: 60000, Cost: 0.12}}}
	updated, _ := m.applyDiagnostics(diagnosticsMsg{gen: 1, epoch: m.bench.diag.epoch, report: report})
	m = updated.(model)
	// 所选范围的用量合计常驻摘要，与右栏同一写法；未定价不显示 $0。
	if !strings.Contains(m.bench.diag.text, "原始失败信息") || !strings.Contains(m.bench.diag.text, "不等于零") ||
		!strings.Contains(m.bench.diag.text, "用量  ↑120K ↓9.0K · 缓存 50% · $0.12") {
		t.Fatal(m.bench.diag.text)
	}
	updated, cmd := m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(model)
	if cmd == nil || m.bench.diag.request.After != "op-2" {
		t.Fatal("missing task pagination cursor")
	}
	// A new page replaces the old page instead of accumulating all task records.
	report.Operations = []diag.Operation{{ID: "op-3"}}
	report.NextOperationID = ""
	updated, _ = m.applyDiagnostics(diagnosticsMsg{gen: 1, epoch: m.bench.diag.epoch, report: report})
	m = updated.(model)
	if strings.Contains(m.bench.diag.text, "op-1") {
		t.Fatal("old task page retained")
	}
	updated, cmd = m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || m.bench.diag.request.OperationID != "op-3" {
		t.Fatal("task not selected")
	}
	report.Header.Scope.OperationID = "op-3"
	report.Events = []diag.Event{{Sequence: 1, Text: "本地敏感事件", PayloadTruncated: true, Usage: domainmodel.Usage{Input: 3000, Output: 200}}}
	report.NextEventSequence = 100
	updated, _ = m.applyDiagnostics(diagnosticsMsg{gen: 1, epoch: m.bench.diag.epoch, report: report})
	m = updated.(model)
	if !strings.Contains(m.bench.diag.text, "已截断") || !strings.Contains(m.bench.diag.text, "用量 ↑3.0K ↓200") {
		t.Fatalf("missing truncation coverage or event usage:\n%s", m.bench.diag.text)
	}
	updated, cmd = m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(model)
	if cmd == nil || m.bench.diag.request.EventAfter != 100 {
		t.Fatal("missing event pagination cursor")
	}
}

func TestDiagnosticsMouseAndShareInput(t *testing.T) {
	m := diagnosticModel(t)
	report := diag.Report{Header: diag.Header{Scope: diag.Request{ProjectID: "book"}}, Operations: []diag.Operation{{ID: "op"}}}
	updated, _ := m.applyDiagnostics(diagnosticsMsg{gen: 1, epoch: m.bench.diag.epoch, report: report})
	m = updated.(model)
	line := m.bench.diag.operationLines[0]
	updated, cmd := m.handleDiagnosticsMouse(tea.MouseMsg{Y: line + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(model)
	if cmd == nil || m.bench.diag.request.OperationID != "op" {
		t.Fatal("mouse did not drill into task")
	}
	m.bench.diag.busy = false
	updated, _ = m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(model)
	if m.bench.diag.input == nil {
		t.Fatal("missing share path input")
	}
	updated, cmd = m.handleDiagnosticsKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd != nil || m.bench.diag.err == "" {
		t.Fatal("empty path was accepted")
	}
	for _, width := range []int{32, 80, 120} {
		m.width = width
		m.layoutDiagnostics()
		if m.viewDiagnostics() == "" {
			t.Fatal("empty view")
		}
	}
}

func TestDiagnosticEventDisplayBounded(t *testing.T) {
	text := strings.Repeat("汉", 100000)
	shown := diagnosticEventText(text)
	if len(shown) > 2200 || !strings.Contains(shown, "2 KiB") {
		t.Fatal("large event display is not bounded")
	}
	if !strings.Contains(diagnosticStateLabel("waiting_user"), "正常等待") {
		t.Fatal("waiting was presented as a fault")
	}
}
