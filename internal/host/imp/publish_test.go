package imp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// spyCommitter 记录 Execute 调用次数，供发布幂等/恢复路径测试。
type spyCommitter struct{ calls int }

func (s *spyCommitter) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	s.calls++
	return json.RawMessage(`{}`), nil
}

// TestPublishChapterHandlesStalePendingCommit 守护发布崩溃窗口的恢复：崩溃落在
// MarkChapterComplete 与 ClearPendingCommit 之间会残留指向本章的 pending_commit。
// 已完成章若直接跳过会绕开 commit 工具的清理分支，下一章 Execute 以 ErrToolConflict
// 拒绝，导入每次重跑死在同一处——命中残留时必须仍走一次工具幂等路径。
func TestPublishChapterHandlesStalePendingCommit(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 100, "mystery", "quest"); err != nil {
		t.Fatal(err)
	}
	f := ImportedChapterFacts{Chapter: 1, Summary: "s", CoreEvent: "c", HookType: "mystery", DominantStrand: "quest"}

	// 无残留：已完成章零成本跳过，不触发 commit。
	spy := &spyCommitter{}
	if err := publishChapter(context.Background(), st, spy, 1, "正文", f); err != nil {
		t.Fatalf("已完成章应幂等跳过：%v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("无残留不应调用 commit，得 %d 次", spy.calls)
	}

	// 残留指向本章：必须走一次 commit 幂等路径完成清理。
	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1}); err != nil {
		t.Fatal(err)
	}
	if err := publishChapter(context.Background(), st, spy, 1, "正文", f); err != nil {
		t.Fatalf("残留清理路径不应失败：%v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("命中残留应恰好调用 commit 一次，得 %d 次", spy.calls)
	}
}
