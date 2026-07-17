package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
)

// FailureFacts 是 worker_failure / deadlock 两个场景共用的事实包:
// Engine 已做过确定性分类(重试/参数错等不到这里),送到 Arbiter 的都是
// "确定性代码给不出出路"的残余。
type FailureFacts struct {
	Kind          string   `json:"kind"` // worker_failure | deadlock
	Agent         string   `json:"agent,omitempty"`
	Task          string   `json:"task,omitempty"`
	Error         string   `json:"error,omitempty"`   // worker_failure:错误文本
	Repeats       int      `json:"repeats,omitempty"` // deadlock:同指令已派次数
	Phase         string   `json:"phase,omitempty"`
	NextChapter   int      `json:"next_chapter,omitempty"`
	PendingQueue  []int    `json:"pending_rewrites,omitempty"`
	FoundationGap []string `json:"foundation_missing,omitempty"`
}

// FailureDecision 失败/僵局裁定。
type FailureDecision struct {
	Action   string      `json:"action"` // retry | reroute | abort
	Dispatch *DispatchOp `json:"dispatch,omitempty"`
	Reason   string      `json:"reason"`
}

func (d *FailureDecision) ValidateAgainst(f FailureFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason 不能为空")
	}
	switch d.Action {
	case "retry", "abort":
		return nil
	case "reroute":
		if d.Dispatch == nil {
			return fmt.Errorf("reroute 必须附 dispatch")
		}
		if err := d.Dispatch.validate(); err != nil {
			return err
		}
		return validateDispatchAgainst(d.Dispatch, f.Phase)
	default:
		return fmt.Errorf("action 非法: %q（可选 retry / reroute / abort）", d.Action)
	}
}

// DecideFailure 失败/僵局咨询。失败语义:返回 error → Engine 按最保守路径处理
// (暂停 + notify),绝不无限咨询。
func DecideFailure(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts FailureFacts) (FailureDecision, error) {
	return decide(ctx, model, systemPrompt, marshalPayload(facts), func(d *FailureDecision) error {
		return d.ValidateAgainst(facts)
	})
}
