package activity

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputPreservesChannelsTurnsAndTaskHistory(t *testing.T) {
	hub := NewHub()
	publish := func(op string, kind Kind, call, text string) {
		e := toolEvent(kind, "")
		e.OperationID, e.CallID, e.Text, e.TaskLabel = op, call, text, "章节写作"
		hub.Publish(e)
	}
	publish("a", TurnStart, "", "")
	publish("a", Thinking, "", "先考虑")
	publish("a", Thinking, "", "人物动机")
	publish("a", Text, "", "准备写作")
	publish("a", Prose, "one", "甲")
	publish("a", Prose, "two", "乙")
	publish("a", Prose, "one", "丙")
	publish("a", TurnStart, "", "")
	publish("a", Thinking, "", "再检查")
	publish("b", Text, "", "开始审阅")
	snap, _ := hub.Snapshot("book-1")
	want := []string{"先考虑人物动机", "准备写作", "甲丙", "乙", "再检查", "开始审阅"}
	if len(snap.Output) != len(want) {
		t.Fatalf("blocks=%d", len(snap.Output))
	}
	for i, text := range want {
		if string(snap.Output[i].Text) != text {
			t.Fatalf("block %d: %q", i, snap.Output[i].Text)
		}
		if i > 0 && snap.Output[i].ID <= snap.Output[i-1].ID {
			t.Fatal("unstable block identity")
		}
	}
	// Readers own snapshots: mutations in either direction must not leak.
	snap.Output[0].Text[0] = 'x'
	again, _ := hub.Snapshot("book-1")
	if string(again.Output[0].Text) != want[0] {
		t.Fatal("snapshot shares mutable output")
	}
	publish("b", Text, "", "完成")
	if string(again.Output[5].Text) != "开始审阅" {
		t.Fatal("publisher mutated a captured snapshot")
	}
	newRun := toolEvent(Text, "")
	newRun.RunID, newRun.Text = "new-run", "新一轮"
	hub.Publish(newRun)
	last, _ := hub.Snapshot("book-1")
	if len(last.Output) != 1 || last.OutputDropped != 0 {
		t.Fatal("previous run leaked")
	}
}

func TestOutputBoundsAreVisibleAndUTF8Safe(t *testing.T) {
	hub := NewHub()
	for i := 0; i < maxOutputBlocks+20; i++ {
		e := toolEvent(Prose, "workspace_put_chapter")
		e.CallID, e.Text = fmt.Sprint(i), strings.Repeat("中文🙂", 6000)
		hub.Publish(e)
	}
	snap, _ := hub.Snapshot("book-1")
	total := 0
	for _, b := range snap.Output {
		total += len(b.Text)
		if !b.Truncated || !utf8.Valid(b.Text) || len(b.Text) > maxOutputBlockBytes+outputSlack {
			t.Fatal("invalid truncated block")
		}
	}
	if total > maxOutputBytes || len(snap.Output) > maxOutputBlocks || snap.OutputDropped == 0 {
		t.Fatal("output history was not visibly bounded")
	}
}
