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
