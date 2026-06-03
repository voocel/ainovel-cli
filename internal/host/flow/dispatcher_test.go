package flow

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// TestDedupeKey_BatchVsSingle 验证批量指令与单派指令、以及不同批次之间的去重键行为。
func TestDedupeKey_BatchVsSingle(t *testing.T) {
	single := &Instruction{Agent: "writer_wuzei", Task: "写第 1 章候选稿"}
	batchA := &Instruction{Batch: []SubTask{
		{Agent: "writer_wuzei", Task: "写第 1 章候选稿"},
		{Agent: "writer_tudou", Task: "写第 1 章候选稿"},
	}}
	batchB := &Instruction{Batch: []SubTask{ // 少了 tudou
		{Agent: "writer_wuzei", Task: "写第 1 章候选稿"},
	}}
	if dedupeKey(single) == dedupeKey(batchA) {
		t.Fatal("单派与批量键不应相同")
	}
	if dedupeKey(batchA) == dedupeKey(batchB) {
		t.Fatal("不同 pending 集合的批量键应不同（更小批不能被误杀）")
	}
	if dedupeKey(batchA) != dedupeKey(&Instruction{Batch: batchA.Batch}) {
		t.Fatal("相同批量键应相等（重复派发应去重）")
	}
}

// TestFailedFromLastBatch 验证"上一批派出但仍缺候选且未弃权者计为失败"的逻辑。
func TestFailedFromLastBatch(t *testing.T) {
	state := State{
		CandidatesReady: map[string]bool{"wuzei": true, "tudou": false, "maibao": false},
		Abandoned:       map[string]bool{"maibao": true},
	}
	got := failedFromLastBatch([]string{"wuzei", "tudou", "maibao"}, state)
	// wuzei 已就绪→排除；maibao 已弃权→排除；只剩 tudou。
	if len(got) != 1 || got[0] != "tudou" {
		t.Fatalf("应只判定 tudou 失败, got %v", got)
	}
}

// fakeCoord 捕获 FollowUp 文本，供多轮收敛测试断言指令序列。
type fakeCoord struct{ msgs []string }

func (f *fakeCoord) Subscribe(fn func(agentcore.Event)) func() { return func() {} }
func (f *fakeCoord) FollowUp(msg agentcore.AgentMessage)       { f.msgs = append(f.msgs, msg.TextContent()) }

// TestDispatch_ConcurrentFailureConvergence 验证并发批连续失败时，dispatcher 确定性地
// 重发候选批（不被 dedupe 静默截断），累计到阈值后弃权、全弃权降级单 writer。
func TestDispatch_ConcurrentFailureConvergence(t *testing.T) {
	store := storepkg.NewStore(t.TempDir())
	if err := store.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	fc := &fakeCoord{}
	d := &Dispatcher{coordinator: fc, store: store}
	d.SetContest(ContestConfig{Personas: []string{"wuzei", "tudou"}, Concurrency: true})

	// 候选始终不落盘（模拟全失败）。多轮 Dispatch，期望最终降级单 writer。
	for i := 0; i < 6; i++ {
		d.Dispatch()
	}

	if len(fc.msgs) == 0 {
		t.Fatal("不应因 dedupe 全程静默——至少应下发候选批")
	}
	// 多次重发候选批 = 确定性重试（非静默卡死）。
	batchCount := 0
	for _, m := range fc.msgs {
		if strings.Contains(m, "tasks=[") {
			batchCount++
		}
	}
	if batchCount < 2 {
		t.Fatalf("应多次重发候选批，got batchCount=%d msgs=%v", batchCount, fc.msgs)
	}
	// 末条应是降级单 writer 续写（含"写第 1 章"，不含候选/批量标记）。
	last := fc.msgs[len(fc.msgs)-1]
	if !strings.Contains(last, "写第 1 章") || strings.Contains(last, "候选稿") || strings.Contains(last, "tasks=[") {
		t.Fatalf("末条应为降级单 writer 指令, got %q", last)
	}
	ab, _ := store.Contest.AbandonedPersonas(1)
	if !ab["wuzei"] || !ab["tudou"] {
		t.Fatalf("两 persona 应均弃权, got %v", ab)
	}
}
