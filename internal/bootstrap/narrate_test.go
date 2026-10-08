package bootstrap_test

import (
	"context"
	"strings"
	"testing"
	"time"

	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

type noticeRecorder struct{ notices []activity.Event }

func (r *noticeRecorder) Publish(event activity.Event) { r.notices = append(r.notices, event) }

// 一轮快速创作把每一步说给现场：开工做什么、为什么是现在，做成落下了什么。
func TestQuickWriteNarratesEveryStepToTheActivityFeed(t *testing.T) {
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	recorder := &noticeRecorder{}
	api.Tasks.SetActivitySink(recorder)
	command := novelapp.QuickWriteCommand{
		ProjectID: "narrated-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if _, err := api.Novels.QuickWrite(context.Background(), command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	var lines []string
	for _, event := range recorder.notices {
		if event.Kind != activity.Notice || event.ProjectID != command.ProjectID || event.RunID == "" || event.OperationID == "" {
			t.Fatalf("notice without attribution: %#v", event)
		}
		lines = append(lines, string(event.Tone)+" "+event.Text)
	}
	want := []string{
		"step 规划故事蓝图", "done 故事蓝图已生效",
		"step 写第 1 章《", "done 第 1 章《", "step 写第 2 章《", "done 第 2 章《", "step 写第 3 章《", "done 第 3 章《",
		"step 审阅第 1–3 章：先审后写", "done 第 1–3 章审阅完成：通过",
	}
	if len(lines) != len(want) {
		t.Fatalf("notices:\n%s", strings.Join(lines, "\n"))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatalf("notice %d = %q, want prefix %q\n%s", i, lines[i], prefix, strings.Join(lines, "\n"))
		}
	}
	if !strings.Contains(lines[3], "》已入稿 · ") || !strings.HasSuffix(lines[3], " 字") {
		t.Fatalf("a committed chapter must report its length: %q", lines[3])
	}
}

// 逐章确认时，稿件停下等用户：现场说清在等什么，用推导规则给的同一句话。
func TestManualApprovalNarratesTheWait(t *testing.T) {
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	recorder := &noticeRecorder{}
	api.Tasks.SetActivitySink(recorder)
	command := novelapp.QuickWriteCommand{
		ProjectID: "waiting-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, Approval: model.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if _, err := api.Novels.QuickWrite(context.Background(), command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if len(recorder.notices) != 2 || recorder.notices[1].Tone != activity.ToneWait || recorder.notices[1].Text != "故事蓝图已拟好，等你确认后继续" {
		t.Fatalf("notices = %#v", recorder.notices)
	}
}
