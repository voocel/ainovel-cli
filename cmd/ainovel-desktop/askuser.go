package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/voocel/ainovel-cli/internal/tools"
)

// askQuestion / askOption 是投影给前端的提问形状（tools.Question 已带 json tag，
// 这里保持同形以便前端直接渲染）。
type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	Options     []askOption `json:"options"`
	MultiSelect bool        `json:"multiSelect"`
}

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type askRequest struct {
	ID        string        `json:"id"`
	Questions []askQuestion `json:"questions"`
}

type pendingAsk struct {
	ch        chan *tools.AskUserResponse
	questions []askQuestion
}

// askBridge 把引擎中途的结构化提问（阻塞式）桥接到异步前端弹窗。
//
// 引擎在 tools.AskUserTool.Execute 里同步调用 handler 并阻塞，直到用户回答。桥的做法：
// 每次提问生成唯一 id，建一个一次性 channel 存入 pending，发 engine:askuser 事件；
// 然后 select 等前端经 AnswerAskUser(id,...) 写回，或 ctx 取消（Close/Abort）时解除阻塞——
// 否则未回答的弹窗会永久挂死引擎 goroutine。
type askBridge struct {
	mu      sync.Mutex
	pending map[string]pendingAsk
	seq     atomic.Uint64
	emit    func(id string, qs []askQuestion)
}

func newAskBridge(emit func(id string, qs []askQuestion)) *askBridge {
	return &askBridge{
		pending: make(map[string]pendingAsk),
		emit:    emit,
	}
}

// handler 满足 tools.AskUserHandler：阻塞等待前端回答。
func (b *askBridge) handler(ctx context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
	id := fmt.Sprintf("ask-%d", b.seq.Add(1))
	ch := make(chan *tools.AskUserResponse, 1)

	projected := toAskQuestions(questions)
	b.mu.Lock()
	b.pending[id] = pendingAsk{ch: ch, questions: projected}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	b.emit(id, projected)

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		// Close()/Abort 取消引擎 ctx：解除阻塞，交回引擎按“未回答”兜底处理。
		return nil, ctx.Err()
	}
}

// answer 由绑定方法 AnswerAskUser 调用，把前端回答写回对应 pending。
func (b *askBridge) answer(id string, answers, notes map[string]string) error {
	b.mu.Lock()
	pending, ok := b.pending[id]
	if ok {
		// 回答是一次性的。先摘出 pending，避免双击/重复 IPC 把第二份答案
		// 塞进无人再读取的 channel，或让调用方误以为重复回答也已生效。
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("提问 %s 已失效或已回答", id)
	}
	if answers == nil {
		answers = map[string]string{}
	}
	if notes == nil {
		notes = map[string]string{}
	}
	pending.ch <- &tools.AskUserResponse{Answers: answers, Notes: notes}
	return nil
}

func (b *askBridge) pendingRequest() *askRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, pending := range b.pending {
		return &askRequest{ID: id, Questions: append([]askQuestion(nil), pending.questions...)}
	}
	return nil
}

func toAskQuestions(questions []tools.Question) []askQuestion {
	out := make([]askQuestion, 0, len(questions))
	for _, q := range questions {
		opts := make([]askOption, 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, askOption{Label: o.Label, Description: o.Description})
		}
		out = append(out, askQuestion{
			Question:    q.Question,
			Header:      q.Header,
			Options:     opts,
			MultiSelect: q.MultiSelect,
		})
	}
	return out
}

// AnswerAskUser 是绑定方法：前端提交某次提问的回答。answers/notes 以问题全文为 key
// （与 tools.formatAnswers 一致）。
func (a *App) AnswerAskUser(id string, answers, notes map[string]string) error {
	a.mu.Lock()
	bridge := a.ask
	a.mu.Unlock()
	if bridge == nil {
		return fmt.Errorf("当前没有进行中的创作")
	}
	return bridge.answer(id, answers, notes)
}

// GetPendingAskUser 补取当前仍在等待的提问。前端即使在设置页或页面切换期间错过
// 一次性事件，回到工作台后也能恢复弹窗并解除引擎阻塞。
func (a *App) GetPendingAskUser() map[string]any {
	a.mu.Lock()
	bridge := a.ask
	a.mu.Unlock()
	if bridge == nil {
		return nil
	}
	pending := bridge.pendingRequest()
	if pending == nil {
		return nil
	}
	return map[string]any{"id": pending.ID, "questions": pending.Questions}
}
