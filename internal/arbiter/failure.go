package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// FailureFacts 是 worker_failure / deadlock 两个场景共用的事实包:
// Engine 已做过确定性分类(重试/参数错等不到这里),送到 Arbiter 的都是
// "确定性代码给不出出路"的残余。
type FailureFacts struct {
	Kind          string   `json:"kind"` // worker_failure | deadlock
	Agent         string   `json:"agent,omitempty"`
	Task          string   `json:"task,omitempty"`
	Error         string   `json:"error,omitempty"` // worker_failure:错误文本
	ErrorKind     string   `json:"error_kind,omitempty"`
	Repeats       int      `json:"repeats,omitempty"` // deadlock:同指令已派次数
	Phase         string   `json:"phase,omitempty"`
	NextChapter   int      `json:"next_chapter,omitempty"`
	PendingQueue  []int    `json:"pending_rewrites,omitempty"`
	FoundationGap []string `json:"foundation_missing,omitempty"`
	FactWarnings  []string `json:"fact_warnings,omitempty"`
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

// failureContract 紧邻 FailureDecision:action 封闭枚举,dispatch 可空对象
// (仅 reroute 时非 null);跨字段组合仍由 ValidateAgainst 按事实校验。
var failureContract = llmcontract.Contract{
	Name:        "arbiter_failure",
	Description: "失败/僵局裁定:给出出路",
	Schema: schema.Object(
		schema.Property("action", schema.Enum("出路", "retry", "reroute", "abort")).Required(),
		schema.Property("dispatch", dispatchSchema("派单目标(仅 reroute 时给出,否则为 null)")).Required(),
		schema.Property("reason", schema.String("裁定理由")).Required(),
	),
}

// DecideFailure 失败/僵局咨询。失败语义:返回 error → Engine 按最保守路径处理
// (暂停 + notify),绝不无限咨询。
func DecideFailure(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts FailureFacts) (FailureDecision, error) {
	payload, err := marshalPayload(facts)
	if err != nil {
		return FailureDecision{}, err
	}
	return decide(ctx, model, failureContract, systemPrompt, payload, func(d *FailureDecision) error {
		return d.ValidateAgainst(facts)
	})
}
