package diag

import (
	"strings"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

// TestDeadCharacterAppears 验证回溯检测：死亡后章节摘要仍出场。
func TestDeadCharacterAppears(t *testing.T) {
	snap := &Snapshot{
		StateChanges: []domain.StateChange{
			{Chapter: 2, Entity: "王老五", Field: "status", NewValue: "战死"},
		},
		Summaries: map[int]*domain.ChapterSummary{
			1: {Chapter: 1, Characters: []string{"王老五"}}, // 死前出场 → 正常
			4: {Chapter: 4, Characters: []string{"王老五"}}, // 死后出场 → 违规
		},
	}
	got := DeadCharacterAppears(snap)
	if len(got) != 1 || got[0].Rule != "DeadCharacterAppears" {
		t.Fatalf("findings = %+v, want 1 条", got)
	}
	if !strings.Contains(got[0].Evidence, "ch4") {
		t.Fatalf("evidence = %q, 应指向 ch4", got[0].Evidence)
	}
}

// TestDeadCharacterAppears_AliasDeath 验证别名双向归一化：
// state_changes 死亡记录用别名「五爷」，摘要出场用正名「王老五」，仍应命中。
func TestDeadCharacterAppears_AliasDeath(t *testing.T) {
	snap := &Snapshot{
		Characters: []domain.Character{
			{Name: "王老五", Aliases: []string{"五爷"}},
		},
		StateChanges: []domain.StateChange{
			{Chapter: 2, Entity: "五爷", Field: "status", NewValue: "战死"}, // 死亡记录用别名
		},
		Summaries: map[int]*domain.ChapterSummary{
			4: {Chapter: 4, Characters: []string{"王老五"}}, // 出场用正名 → 应命中
		},
	}
	got := DeadCharacterAppears(snap)
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want 1 条（别名死亡记录应归一到正名）", got)
	}
	if !strings.Contains(got[0].Evidence, "王老五") || !strings.Contains(got[0].Evidence, "ch4") {
		t.Fatalf("evidence = %q, 应含正名 王老五 与 ch4", got[0].Evidence)
	}
}
