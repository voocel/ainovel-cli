package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/voocel/ainovel-cli/internal/host"
)

// ── 共创（冷启动 + 阶段） ──
//
// 两种共创共用一套四段式协议（reply / draft / ready / suggestions）与流式回调：
//   - 冷启动（CoCreate）：从零澄清需求，产出整本书的创作指令，Ready 后 StartFromCoCreate 起书。
//   - 阶段（StageCoCreate）：已写一部分时暂停规划后续方向，Ready 后 ResumeFromCoCreate 注入并续跑。
//
// 前端必须把每轮回复的 Raw 原样写回 history（作为 assistant 消息），
// 否则模型看不到自己上一轮的 <draft>，会每轮重新归纳而不是累积更新。

// CoCreateTurn 是一轮共创的返回值（对应 host.CoCreateReply）。
type CoCreateTurn struct {
	Message     string   `json:"message"`
	Prompt      string   `json:"prompt"`
	Ready       bool     `json:"ready"`
	Suggestions []string `json:"suggestions"`
	Raw         string   `json:"raw"`
}

// CoCreateMsg 是前端传入的历史消息。
type CoCreateMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func toHostHistory(history []CoCreateMsg) []host.CoCreateMessage {
	out := make([]host.CoCreateMessage, 0, len(history))
	for _, m := range history {
		out = append(out, host.CoCreateMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

func toTurn(reply host.CoCreateReply) CoCreateTurn {
	return CoCreateTurn{
		Message:     reply.Message,
		Prompt:      reply.Prompt,
		Ready:       reply.Ready,
		Suggestions: reply.Suggestions,
		Raw:         reply.Raw,
	}
}

// coCreateJobs 记录在途共创的取消函数，供 CancelCoCreateTurn 中断当轮生成。
type coCreateJobs struct {
	mu       sync.Mutex
	seq      uint64
	activeID uint64
	cancel   context.CancelFunc
}

func (j *coCreateJobs) begin() (context.Context, uint64) {
	ctx, cancel := context.WithCancel(context.Background())
	j.mu.Lock()
	j.seq++
	id := j.seq
	if j.cancel != nil {
		j.cancel() // 上一轮还在跑：新一轮取代它
	}
	j.activeID = id
	j.cancel = cancel
	j.mu.Unlock()
	return ctx, id
}

func (j *coCreateJobs) end(id uint64) {
	j.mu.Lock()
	if j.activeID == id {
		j.activeID = 0
		j.cancel = nil
	}
	j.mu.Unlock()
}

// abort 取消在途生成（用户关闭共创面板时调用）。
func (j *coCreateJobs) abort() {
	j.mu.Lock()
	cancel := j.cancel
	j.activeID = 0
	j.cancel = nil
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (j *coCreateJobs) active() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancel != nil
}

// CoCreate 冷启动共创的一轮对话。流式过程经 cocreate:progress 事件推送。
func (a *App) CoCreate(history []CoCreateMsg) (CoCreateTurn, error) {
	return a.coCreateTurn(history, false)
}

// StageCoCreate 阶段共创的一轮对话（系统提示会带上当前故事状态摘要）。
func (a *App) StageCoCreate(history []CoCreateMsg) (CoCreateTurn, error) {
	return a.coCreateTurn(history, true)
}

func (a *App) coCreateTurn(history []CoCreateMsg, stage bool) (CoCreateTurn, error) {
	h, err := a.reqHost()
	if err != nil {
		return CoCreateTurn{}, err
	}
	if len(history) == 0 {
		return CoCreateTurn{}, fmt.Errorf("共创历史为空")
	}

	ctx, jobID := a.cocreate.begin()
	defer a.cocreate.end(jobID)

	onProgress := func(kind, text string) {
		a.emit("cocreate:progress", map[string]string{"kind": kind, "text": text})
	}

	var reply host.CoCreateReply
	if stage {
		reply, err = h.StageCoCreateStream(ctx, toHostHistory(history), onProgress)
	} else {
		reply, err = h.CoCreateStream(ctx, toHostHistory(history), onProgress)
	}
	if err != nil {
		return CoCreateTurn{}, err
	}
	return toTurn(reply), nil
}

// CancelCoCreateTurn 中断在途的共创生成（前端关闭面板时调用）。
func (a *App) CancelCoCreateTurn() {
	a.cocreate.abort()
}

// StartFromCoCreate 用共创产出的创作指令起新书（等价于快速模式，但 prompt 来自共创草稿）。
func (a *App) StartFromCoCreate(draft string, reviewFirst bool, genre string) error {
	return a.StartQuick(draft, reviewFirst, genre)
}

// ── 阶段共创的生命周期（已在创作中） ──

// PauseForCoCreate 进入阶段共创：占用共创窗口，运行中则一并暂停引擎。
// 返回 false 表示当前不能进入（全书已完成或已在共创中）。
func (a *App) PauseForCoCreate() (bool, error) {
	h, err := a.reqHost()
	if err != nil {
		return false, err
	}
	return h.PauseForCoCreate(), nil
}

// ResumeFromCoCreate 结束阶段共创：把后续方向作为干预注入并恢复创作。
func (a *App) ResumeFromCoCreate(draft string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	return h.ResumeFromCoCreate(draft)
}

// CancelCoCreate 放弃阶段共创，保持暂停态。
func (a *App) CancelCoCreate() error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	a.cocreate.abort()
	h.CancelCoCreate()
	return nil
}
