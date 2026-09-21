package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestInspectorReviewFollowsCandidateAndScopesRequirements(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.selectOutline(0)
	m.bench.snap.Directives = []domainmodel.Directive{
		{Scope: "project", Text: "保持悬疑", Status: domainmodel.DirectiveActive},
		{Scope: "chapter_range:4-4", Text: "门后声音", Status: domainmodel.DirectiveActive},
		{Scope: "chapter_range:1-1", Text: "第一章专属", Status: domainmodel.DirectiveActive},
	}
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{ID: "ch-4", Number: 4, Title: "旧标题", Blocks: []domainmodel.ManuscriptBlock{{Text: "旧正文"}}}}
	chapter := domainmodel.ManuscriptChapter{ID: "ch-4", Number: 4, Title: "新标题", Blocks: []domainmodel.ManuscriptBlock{{Text: "新的候选正文"}}}
	payload, err := json.Marshal(chapter)
	if err != nil {
		t.Fatal(err)
	}
	m.bench.decision = &decisionState{hasProposal: true, proposal: domainmodel.Proposal{ID: "candidate-4", Patches: []domainmodel.Patch{{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "ch-4"}, Operation: domainmodel.PatchPut, Content: payload}}}}
	m.switchContent(contentReview)
	view := ansi.Strip(strings.Join(m.detailSummary(40, 40), "\n"))
	for _, want := range []string{"第 4 章 · 候选待确认", "candidate-4", "正文 3 → 6 字", "标题已调整", "[全书] 保持悬疑", "门后声音"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "第一章专属") {
		t.Fatal("review inspector followed outline selection")
	}
	m.bench.decision.stale = true
	if !strings.Contains(strings.Join(m.detailSummary(40, 40), "\n"), "基线已过期") {
		t.Fatal("stale candidate looked approvable")
	}
	m.bench.decision.proposal.Patches[0].Content = []byte("invalid")
	if !strings.Contains(strings.Join(m.detailSummary(40, 40), "\n"), "读取异常") {
		t.Fatal("malformed candidate was hidden")
	}
}

func TestInspectorLimitsLongContext(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.snap.Directives = []domainmodel.Directive{{Scope: "project", Text: strings.Repeat("必须保留人物动机。", 100), Status: domainmodel.DirectiveActive}}
	lines := m.detailSummary(30, 15)
	if len(lines) > 15 || !strings.Contains(strings.Join(lines, "\n"), "…") {
		t.Fatal("long context must stay bounded with a visible omission hint")
	}
}
