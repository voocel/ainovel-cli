package host

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/rip"
)

// Deconstruct 启动一次对标小说拆解：ingest → bound →（长篇）黄金三章 → 逐章摘要 →
// 聚合 → 角色设定 → 报告 → 文风。
//
// 与导入的本质差别：拆解是只读的——不写 Store、没有 publish、不改任何创作状态，
// 产物落独立拆文库（拆文库/{书名}/）。因此它不需要 superviseImport 那套接力逻辑，
// 用通用的 superviseExclusive 即可（与 SimulateFrom 同形）。
//
// 与 Engine 运行互斥：拆一本 500 章的书是几百次模型调用，和创作抢同一个钱包与速率配额。
// 返回的事件通道由 rip.Run 关闭，调用方负责消费。
func (h *Host) Deconstruct(ctx context.Context, opts rip.Options) (<-chan rip.Event, error) {
	// 与导入同一纪律：拆解是全流程模型调用，预算已超时不得启动。
	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := h.acquireExclusive("拆文"); err != nil {
		return nil, err
	}
	// 登记取消函数：预算硬停/手动暂停经 abortWithEvent 取消拆解自己的 context
	//（否则哨兵只会去暂停并未运行的 Engine，拆解继续烧钱）。
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	deps := rip.Deps{
		Bound:     h.ripCaller("bound"),
		Summary:   h.ripCaller("summary"),
		Aggregate: h.ripCaller("aggregate"),
		Report:    h.ripCaller("report"),
		Prompts: rip.Prompts{
			Bound:     h.bundle.Prompts.RipBound,
			Preview:   h.bundle.Prompts.RipPreview,
			Summary:   h.bundle.Prompts.RipSummary,
			Aggregate: h.bundle.Prompts.RipAggregate,
			Profile:   h.bundle.Prompts.RipProfile,
			Report:    h.bundle.Prompts.RipReport,
			Style:     h.bundle.Prompts.RipStyle,
		},
	}
	ch, err := rip.Run(ctx, deps, opts)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// DeconstructLibraryPath 回显某本书在拆文库中的目录，供 UI 展示与恢复检测。
// libraryDir 为空时落 <cwd>/拆文库。
func (h *Host) DeconstructLibraryPath(libraryDir, bookName string) (string, error) {
	return rip.LibraryPath(libraryDir, bookName)
}

// DeconstructResumeHint 返回某本书未完成拆解的一行提示（无未完成拆解则空串）。
// 内部会重算各工件的 InputDigest，只适合按需调用，不要放进快照轮询。
func (h *Host) DeconstructResumeHint(libraryDir, bookName string) string {
	dir, err := rip.LibraryPath(libraryDir, bookName)
	if err != nil {
		return ""
	}
	return rip.ResumeSummary(dir)
}

// ripCaller 解析一个拆解语义函数的模型档位：roles 配置存在 rip_<fn> 则用该档位
//（用量也记该角色的账），否则落 architect。这是调用配置，不改任何语义契约。
//
// 档位划分按"机械性"分层：bound / summary 是逐块逐章的重复劳动，可指到便宜档位；
// aggregate / report 是全书级裁定，值得留强模型。黄金三章跟 summary 同档（读的是章正文），
// 文风跟 report 同档（同属全书级裁定）。
func (h *Host) ripCaller(fn string) rip.Caller {
	role := "rip_" + fn
	if _, _, explicit := h.models.CurrentSelection(role); !explicit {
		role = "architect"
	}
	model := h.models.ForRoleWithFailover(role, func(ev bootstrap.FailoverEvent) {
		slog.Warn("拆文 provider 切换", "module", "rip", "role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err)
	})
	model = newUsageTrackedModel(model, role, h.usage.Record)
	return rip.Caller{Model: model, Runtime: h.ripModelRuntime(role, model)}
}

// ripModelRuntime 探测所选档位的调用能力，供 rip 双预算 / thinking 自适应使用。
// 探测逻辑与导入完全相同（同样是 registry + 角色 reasoning effort），故直接复用
// importModelRuntime 转形：能力探测只应有一处实现，两处会各自漂移。
func (h *Host) ripModelRuntime(role string, model agentcore.ChatModel) rip.ModelRuntime {
	rt := h.importModelRuntime(role, model)
	return rip.ModelRuntime{
		ContextTokens:   rt.ContextTokens,
		MaxOutputTokens: rt.MaxOutputTokens,
		Thinking:        rt.Thinking,
	}
}
