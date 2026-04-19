package reminder

import (
	"context"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// queueGuardGen 在 rewriting/polishing 队列未空时强制 Coordinator 先清队列。
// 这一条是为了根除 Phase 0 之前暴露过的典型 bug：editor verdict=polish 后，
// Coordinator 忽视 affected_chapters，先去 expand_arc 派新章节。
type queueGuardGen struct{}

func (queueGuardGen) Source() string { return "queue_guard" }

func (queueGuardGen) Generate(_ context.Context, state State) string {
	p := state.Progress
	if p == nil {
		return ""
	}
	if p.Flow != domain.FlowRewriting && p.Flow != domain.FlowPolishing {
		return ""
	}
	if len(p.PendingRewrites) == 0 {
		return ""
	}
	verb := "重写"
	if p.Flow == domain.FlowPolishing {
		verb = "打磨"
	}
	return fmt.Sprintf(
		"当前 flow=%s，待处理队列：%v。请立即调 writer 逐章%s，直到 commit_chapter 返回 queue_drained=true。"+
			"在此之前：禁止调 architect 展开新弧、禁止调 writer 写队列外的新章节、禁止调 editor 生成摘要。",
		p.Flow, p.PendingRewrites, verb)
}
