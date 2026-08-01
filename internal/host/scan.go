package host

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/scan"
)

// ScanRanks 启动一次榜单扫描：fetch → parse（含清洗质量门）→ analyze → topic。
//
// 与拆文同属只读分析管线：不写 Store、没有 publish、不改任何创作状态，产物落独立
// 扫榜库（扫榜库/{平台}_{榜单}_{日期}/）。因此用通用的 superviseExclusive 即可。
//
// 与 Engine 运行互斥：扫榜和创作抢同一个钱包与速率配额。
// 返回的事件通道由 scan.Run 关闭，调用方负责消费。
func (h *Host) ScanRanks(ctx context.Context, opts scan.Options) (<-chan scan.Event, error) {
	// 与导入/拆文同一纪律：扫榜是全流程模型调用，预算已超时不得启动。
	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := h.acquireExclusive("扫榜"); err != nil {
		return nil, err
	}
	// 登记取消函数：预算硬停/手动暂停经 abortWithEvent 取消扫榜自己的 context
	//（否则哨兵只会去暂停并未运行的 Engine，扫榜继续烧钱）。
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	deps := scan.Deps{
		Parse:   h.scanCaller("parse"),
		Analyze: h.scanCaller("analyze"),
		Prompts: scan.Prompts{
			Parse:   h.bundle.Prompts.ScanParse,
			Analyze: h.bundle.Prompts.ScanAnalyze,
			Topic:   h.bundle.Prompts.ScanTopic,
		},
	}
	ch, err := scan.Run(ctx, deps, opts)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// ScanLibraryPath 回显一次扫榜在扫榜库中的目录，供 UI 展示与恢复检测。
// libraryDir 为空时落 <cwd>/扫榜库；scanDate 为空时取今天。
func (h *Host) ScanLibraryPath(libraryDir, platform, rankName, scanDate string) (string, error) {
	return scan.LibraryPath(libraryDir, platform, rankName, scanDate)
}

// ScanResumeHint 返回某次扫榜未完成的一行提示（无未完成扫榜则空串）。
// 内部会重算各工件的 InputDigest，只适合按需调用，不要放进快照轮询。
func (h *Host) ScanResumeHint(libraryDir, platform, rankName, scanDate string) string {
	dir, err := scan.LibraryPath(libraryDir, platform, rankName, scanDate)
	if err != nil {
		return ""
	}
	return scan.ResumeSummary(dir)
}

// scanCaller 解析一个扫榜语义函数的模型档位：roles 配置存在 scan_<fn> 则用该档位
//（用量也记该角色的账），否则落 architect。这是调用配置，不改任何语义契约。
//
// 档位划分按「机械性」分层：parse 是逐份数据源的摘录劳动，可指到便宜档位；
// analyze 是全局裁定，值得留强模型。选题跟 analyze 同档（同属全局裁定）。
func (h *Host) scanCaller(fn string) scan.Caller {
	role := "scan_" + fn
	if _, _, explicit := h.models.CurrentSelection(role); !explicit {
		role = "architect"
	}
	model := h.models.ForRoleWithFailover(role, func(ev bootstrap.FailoverEvent) {
		slog.Warn("扫榜 provider 切换", "module", "scan", "role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err)
	})
	model = newUsageTrackedModel(model, role, h.usage.Record)
	return scan.Caller{Model: model, Runtime: h.scanModelRuntime(role, model)}
}

// scanModelRuntime 探测所选档位的调用能力，供 scan 预算 / thinking 自适应使用。
// 探测逻辑与导入完全相同，故直接复用 importModelRuntime 转形：
// 能力探测只应有一处实现，两处会各自漂移。
func (h *Host) scanModelRuntime(role string, model agentcore.ChatModel) scan.ModelRuntime {
	rt := h.importModelRuntime(role, model)
	return scan.ModelRuntime{
		ContextTokens:   rt.ContextTokens,
		MaxOutputTokens: rt.MaxOutputTokens,
		Thinking:        rt.Thinking,
	}
}
