package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// PlanStartDecision 启动裁定:选规划师并产出(必要时扩充过的)任务文本。
type PlanStartDecision struct {
	Planner string `json:"planner"` // architect_long | architect_short
	Task    string `json:"task"`    // 交给规划师的完整任务(含扩充后的需求)
	Reason  string `json:"reason"`
}

func (d *PlanStartDecision) Validate() error {
	if d.Planner != "architect_long" && d.Planner != "architect_short" {
		return fmt.Errorf("planner 非法: %q（可选 architect_long / architect_short）", d.Planner)
	}
	if strings.TrimSpace(d.Task) == "" {
		return fmt.Errorf("task 不能为空")
	}
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason 不能为空")
	}
	return nil
}

// planStartContract 紧邻 PlanStartDecision:字段全 required,planner 是封闭枚举。
var planStartContract = llmcontract.Contract{
	Name:        "arbiter_plan_start",
	Description: "启动裁定:选规划师并产出完整任务文本",
	Schema: schema.Object(
		schema.Property("planner", schema.Enum("规划师", "architect_long", "architect_short")).Required(),
		schema.Property("task", schema.String("交给规划师的完整任务(含扩充后的需求)")).Required(),
		schema.Property("reason", schema.String("选择理由")).Required(),
	),
}

// planStartPayload 是 plan_start 的用户负载(事实即输入,无 store 状态——新书)。
type planStartPayload struct {
	Requirement string `json:"requirement"`
	Style       string `json:"style,omitempty"`
}

// DecidePlanStart 启动裁定:根据用户需求选规划师;需求过短(<20 字)时在 task 里
// 自主补充差异化方向、目标读者与核心消费点、至少一个非常规钩子。
// 失败语义:返回 error → 调用方显式报错中止启动(启动期用户在场,报错优于猜测)。
func DecidePlanStart(ctx context.Context, model agentcore.ChatModel, systemPrompt, requirement, style string) (PlanStartDecision, error) {
	payload, err := marshalPayload(planStartPayload{Requirement: requirement, Style: style})
	if err != nil {
		return PlanStartDecision{}, err
	}
	return decide(ctx, model, planStartContract, systemPrompt, payload, (*PlanStartDecision).Validate)
}
