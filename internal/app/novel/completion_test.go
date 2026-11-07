package novel

import (
	"testing"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 内容齐全要以全书章数已知为前提（D29/D63）；蓝图多于固定篇幅由推导器先停下等用户。
func TestNothingLeftToWriteNeedsAKnownFinal(t *testing.T) {
	project := projectdoc.Snapshot{
		Plan: []model.PlanNode{
			{ID: "chapter-1", Kind: model.PlanChapter, Turn: "转折", Order: 1, Title: "第一章", Summary: "开端"},
			{ID: "chapter-2", Kind: model.PlanChapter, Turn: "转折", Order: 2, Title: "第二章", Summary: "推进"},
		},
		Manuscript: []model.ManuscriptChapter{
			{ID: "manuscript-1", PlanNodeID: "chapter-1", Number: 1, Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "正文"}}},
			{ID: "manuscript-2", PlanNodeID: "chapter-2", Number: 2, Blocks: []model.ManuscriptBlock{{ID: "block-2", Text: "正文"}}},
		},
	}
	// 篇幅交给 AI 且尚未收官：内容再齐也要接着扩写蓝图。
	if next, ok := nextUpcoming(project, Length{Compass: &model.Compass{Ending: "收束"}}); !ok || next.write != nil || next.covered != 2 {
		t.Fatalf("open-phase next = %+v, %v", next, ok)
	}
	committed := project
	committed.Compass = &model.Compass{Ending: "收束", Final: 2}
	if next, ok := nextUpcoming(committed, lengthOf(committed, 0)); ok {
		t.Fatalf("committed finale still has work: %+v", next)
	}
}
