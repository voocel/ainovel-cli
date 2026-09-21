package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestComposerKeepsTargetWhileNavigating(t *testing.T) {
	m := studioModel(t, 150, 40)
	m = typeText(t, m, "加强结尾")
	want, label := m.composerScope()
	m.selectOutline(0)
	if scope, got := m.composerScope(); scope != want || got != label {
		t.Fatal("navigation changed the in-progress requirement target")
	}
	m, _ = press(t, m, tea.KeyEsc)
	if scope, _ := m.composerScope(); scope == want {
		t.Fatal("clearing the draft must release its target")
	}
}

func TestNewDecisionDoesNotConsumeExistingRequirement(t *testing.T) {
	m := studioModel(t, 150, 40)
	m = typeText(t, m, "加强结尾")
	m.bench.writing = false
	m.bench.presentDecision(&decisionState{hasProposal: true, proposal: domainmodel.Proposal{ID: "new"}})
	next, cmd := press(t, m, tea.KeyEnter)
	if cmd == nil || next.bench.input.Value() != "" || next.bench.err != "" || next.bench.decision == nil {
		t.Fatal("an arriving candidate consumed an existing requirement as rejection")
	}
}

func TestReviewReadsItsOwnCandidateAndLeavesNavigationAvailable(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.writing = false
	chapter := domainmodel.ManuscriptChapter{Number: 4, Title: "门后的声音", Blocks: []domainmodel.ManuscriptBlock{{Text: "第四章的候选正文"}}}
	content, err := json.Marshal(chapter)
	if err != nil {
		t.Fatal(err)
	}
	m.bench.presentDecision(&decisionState{hasProposal: true, proposal: domainmodel.Proposal{ID: "candidate", Patches: []domainmodel.Patch{{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "chapter-4"}, Operation: domainmodel.PatchPut, Content: content}}}})
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{Number: 1, Title: "无人签收", Blocks: []domainmodel.ManuscriptBlock{{Text: "第一章的正式正文"}}}}
	m.selectOutline(0)
	reading, cmd := press(t, m, tea.KeyEnter)
	if cmd != nil || !reading.bench.reading || !strings.Contains(reading.bench.bodyText, "第一章的正式正文") || reading.bench.decision == nil {
		t.Fatal("pending review blocked ordinary chapter reading")
	}
	review, _ := submit(t, m, "/review")
	if !strings.Contains(review.View(), "第四章的候选正文") || strings.Contains(review.View(), "第一章的正式正文") {
		t.Fatal("review followed reading selection instead of the proposal")
	}
	note := typeText(t, m, "/note 调整开头")
	if !strings.Contains(ansi.Strip(note.View()), "不裁决待确认稿件") {
		t.Fatal("independent requirement must expose its semantics")
	}
}

func TestFeedbackDoesNotHideActionsAndUsage(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.config.Model = "test-model"
	m.bench.err = "导出失败，请检查路径"
	view := strings.Join(strings.Fields(ansi.Strip(m.View())), " ")
	for _, want := range []string{"导出失败", "/p 暂停推进", "test-model", "/? 更多"} {
		if !strings.Contains(view, want) {
			t.Fatalf("feedback hid %q", want)
		}
	}
}

func TestFooterContainsActionsAndInspectorKeepsContext(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.config.Model = "test-model"
	m.bench.err = "导出失败"
	l := m.benchLayout()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	footer := strings.Join(lines[l.footerY:], "\n")
	if len(lines[l.footerY:]) != benchFooterRows || !strings.Contains(footer, "创作要求") || !strings.Contains(footer, "导出失败") {
		t.Fatal("composer or feedback missing")
	}
	for _, text := range []string{"Tab 切换区域"} {
		if strings.Contains(footer, text) {
			t.Fatalf("footer still contains %q", text)
		}
	}
	if !strings.Contains(lines[l.footerY+3], "test-model") || strings.Contains(lines[1], "test-model") {
		t.Fatal("model must be at the right of the footer hints")
	}
	if !strings.Contains(lines[l.footerY+1], "创作要求") || !strings.Contains(lines[l.footerY+2], "导出失败") {
		t.Fatal("context must sit inline with input and feedback within the lower rule")
	}
	actions := ""
	for _, line := range lines[l.bodyY:l.footerY] {
		actions += ansi.Cut(line, l.inspectorX, l.width) + "\n"
	}
	for _, text := range []string{"操作", "暂停推进", "思考原文", "更多"} {
		if strings.Contains(actions, text) {
			t.Fatalf("inspector still contains action %q", text)
		}
	}
}

func TestFooterEmptyFeedbackKeepsRuleContinuousAndBottomBlank(t *testing.T) {
	m := studioModel(t, 150, 40)
	for _, notice := range []string{"", "   "} {
		m.bench.notice = notice
		lines := m.benchFooter(m.benchLayout())
		if ansi.Strip(lines[2]) != strings.Repeat("─", m.width) {
			t.Fatal("empty feedback left a gap in the separator")
		}
		view := strings.Split(ansi.Strip(m.View()), "\n")
		if len(view) != m.height || strings.TrimSpace(view[len(view)-1]) != "" {
			t.Fatal("footer must end with a blank row without growing the frame")
		}
	}
}

func TestCompletedWorkbenchOffersGoalCommandWithoutCapturingLetters(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.writing = false
	run := *m.bench.snap.Run
	run.State = domainmodel.RunCompleted
	m.bench.snap.Run = &run
	if action := m.benchActions()[0]; action.key != "/goal" {
		t.Fatalf("completed run offers wrong action: %+v", action)
	}
	l := m.benchLayout()
	clicked := clickBench(m, 2, l.footerY+3)
	if clicked.bench.input.Value() != "/goal " || clicked.bench.writing {
		t.Fatal("goal action must prepare input without starting a run")
	}
	typed := typeText(t, m, "g")
	if typed.bench.input.Value() != "g" || typed.bench.writing {
		t.Fatal("ordinary letter must stay in the composer")
	}
}

func TestFooterActionClickPreservesUnsubmittedInput(t *testing.T) {
	m := studioModel(t, 150, 40)
	l := m.benchLayout()
	line := ansi.Strip(m.benchFooter(l)[3])
	if !strings.Contains(line, "/p 暂停推进") || !strings.Contains(line, "/? 更多") {
		t.Fatalf("actions missing below composer: %s", line)
	}
	m = clickBench(m, 2, l.footerY+3)
	if m.bench.input.Value() != "/p" {
		t.Fatal("footer click did not fill command")
	}
	m.bench.input.SetValue("保留这段要求")
	m = clickBench(m, 2, l.footerY+3)
	if m.bench.input.Value() != "保留这段要求" {
		t.Fatal("footer action overwrote unsubmitted text")
	}
	m.bench.err = strings.Repeat("非常长的错误信息", 100)
	if len(strings.Split(ansi.Strip(m.View()), "\n")) != 40 {
		t.Fatal("long feedback changed frame height")
	}
}
