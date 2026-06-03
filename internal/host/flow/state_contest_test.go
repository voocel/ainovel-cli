package flow

import (
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	storepkg "github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

// TestLoadStateWithContest_ConcurrencyAndAbandoned 验证 concurrency 开关与弃权名单进入 State。
func TestLoadStateWithContest_ConcurrencyAndAbandoned(t *testing.T) {
	store := storepkg.NewStore(t.TempDir())
	// 进入写作阶段，目标章=1（空 CompletedChapters → NextChapter()=1）
	if err := store.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	// 把 wuzei 顶到弃权（阈值 1）
	if _, err := store.Contest.RecordAttempts(1, []string{"wuzei"}, 1); err != nil {
		t.Fatalf("record: %v", err)
	}
	s := LoadStateWithContest(store, ContestConfig{Personas: []string{"wuzei", "tudou"}, Concurrency: true})
	if !s.ContestConcurrent {
		t.Fatal("ContestConcurrent 应为 true")
	}
	if !s.Abandoned["wuzei"] {
		t.Fatalf("wuzei 应在 Abandoned 中, got %v", s.Abandoned)
	}
}
