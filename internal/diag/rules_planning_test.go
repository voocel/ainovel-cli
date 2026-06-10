package diag

import (
	"strings"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

// TestForeshadowOverdue 验证 deadline 逾期检测。
func TestForeshadowOverdue(t *testing.T) {
	snap := &Snapshot{
		Progress: &domain.Progress{CompletedChapters: []int{1, 2, 3, 4, 5}},
		Foreshadow: []domain.ForeshadowEntry{
			{ID: "f1", Status: "planted", PlantedAt: 1, Deadline: 4},
			{ID: "f2", Status: "advanced", PlantedAt: 1, Deadline: 9},
			{ID: "f3", Status: "resolved", PlantedAt: 1, Deadline: 2},
		},
	}
	got := ForeshadowOverdue(snap)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Rule != "ForeshadowOverdue" || !strings.Contains(got[0].Evidence, "f1") {
		t.Fatalf("finding = %+v", got[0])
	}
	snap.Foreshadow = []domain.ForeshadowEntry{{ID: "x", Status: "planted"}}
	if got := ForeshadowOverdue(snap); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}
