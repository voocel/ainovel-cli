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

// 结构化产出一条一行：同一调用同一栏目合成一块，换栏目另起一块；参数收齐时
// ToolStart 把作用对象补进由首个 delta 建立的条目。
func TestItemsGroupBySectionAndToolStartCarriesDetail(t *testing.T) {
	hub := NewHub()
	item := func(section, text string) {
		e := toolEvent(Item, "proposal_submit")
		e.CallID, e.Section, e.Text = "call-1", section, text
		hub.Publish(e)
	}
	delta := toolEvent(ToolDelta, "proposal_submit")
	delta.CallID, delta.Bytes = "call-1", 64
	hub.Publish(delta)
	item("大纲", "第 1 卷 · 尘缘")
	item("大纲", "第 1 章 · 药铺学徒")
	item("设定", "韩立（人物）")
	item("大纲", "故事罗盘 · 终局：飞升")
	start := toolEvent(ToolStart, "proposal_submit")
	start.CallID, start.Detail = "call-1", "首次规划"
	hub.Publish(start)

	snapshot, _ := hub.Snapshot("book-1")
	want := []struct{ section, text string }{
		{"大纲", "第 1 卷 · 尘缘\n第 1 章 · 药铺学徒"}, {"设定", "韩立（人物）"}, {"大纲", "故事罗盘 · 终局：飞升"},
	}
	if len(snapshot.Output) != len(want) {
		t.Fatalf("blocks = %#v", snapshot.Output)
	}
	for i, w := range want {
		if block := snapshot.Output[i]; block.Kind != Item || block.Section != w.section || string(block.Text) != w.text {
			t.Fatalf("block %d = %s %q %q", i, block.Kind, block.Section, block.Text)
		}
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Detail != "首次规划" || snapshot.Entries[0].Bytes != 64 {
		t.Fatalf("entry = %#v", snapshot.Entries)
	}
	if snapshot.Thinking {
		t.Fatal("an item is model output and must end the thinking state")
	}
}

// 旁白是时间线上的一行，不是模型输出：不打断等待模型的计时，换任务时照常归属新任务。
func TestNoticeIsATimelineEntry(t *testing.T) {
	hub := NewHub()
	notice := func(op string, tone Tone, text string) {
		e := toolEvent(Notice, "")
		e.OperationID, e.Tone, e.Text = op, tone, text
		hub.Publish(e)
	}
	hub.Publish(toolEvent(TurnStart, ""))
	notice("op-1", ToneDone, "第 4 章《门后的声音》已入稿 · 3412 字")
	snapshot, _ := hub.Snapshot("book-1")
	if !snapshot.Waiting {
		t.Fatal("a notice must not end the wait for the model")
	}
	notice("op-2", ToneStep, "审阅第 1–5 章")
	snapshot, _ = hub.Snapshot("book-1")
	if len(snapshot.Entries) != 2 || snapshot.Entries[1].Kind != Notice || snapshot.Entries[1].Tone != ToneStep ||
		snapshot.Entries[1].Text != "审阅第 1–5 章" || !snapshot.Entries[1].Done || snapshot.OperationID != "op-2" {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
}
