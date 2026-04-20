package host

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// buildResumePrompt 基于事实生成 Resume 用的简短 prompt 与 UI 标签。
//
// 重构说明（2026-04-20）：所有"具体下一步应该做什么"的决策已下沉到 Host Flow Router。
// 本函数不再替 Coordinator 规划动作，只做三件事：
//  1. 判断是否需要恢复（Phase=Complete 或无 Progress → 返回空 label 表示新建）
//  2. 生成适合在 UI 展示的 label（"恢复：弧末评审待处理（V2 A3）" 之类）
//  3. 把用户停机期间留下的 PendingSteer 显式传给 Coordinator
//
// 返回 (prompt, label, error)。label 为空表示无可恢复状态（应走新建）。
func buildResumePrompt(store *storepkg.Store) (string, string, error) {
	progress, err := store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	if progress == nil || progress.Phase == domain.PhaseComplete {
		return "", "", nil
	}

	label := describeResume(store, progress)

	var b strings.Builder
	title := progress.NovelName
	if title == "" {
		title = "当前小说"
	}
	b.WriteString(fmt.Sprintf("[恢复] 本书「%s」", title))
	if n := len(progress.CompletedChapters); n > 0 {
		b.WriteString(fmt.Sprintf("已完成 %d 章", n))
		if progress.TotalChapters > 0 {
			b.WriteString(fmt.Sprintf("（共 %d 章）", progress.TotalChapters))
		}
		b.WriteString(fmt.Sprintf("，共 %d 字", progress.TotalWordCount))
	}
	b.WriteString("。\n")
	b.WriteString("Host 将根据当前事实下达下一步 `[Host 下达指令]` 消息。收到后立即执行，不要先调 novel_context 推理。\n")

	if meta, _ := store.RunMeta.Load(); meta != nil && meta.PendingSteer != "" {
		b.WriteString("\n用户在停机期间留下了一条干预意见：\n「")
		b.WriteString(meta.PendingSteer)
		b.WriteString("」\n请先按 coordinator.md 的用户干预规则评估处理。")
	}

	return b.String(), label, nil
}

// describeResume 生成人类可读的恢复标签；不影响 Coordinator 的行为。
// 所有执行路由由 Flow Router 按事实推导；这里仅面向 UI 的 "恢复：xxx"。
func describeResume(store *storepkg.Store, progress *domain.Progress) string {
	switch progress.Phase {
	case domain.PhasePremise, domain.PhaseOutline:
		return fmt.Sprintf("恢复：规划阶段（%s）", progress.Phase)
	case domain.PhaseWriting:
		// 优先级与 Router 的决策优先级对齐，让 label 与即将派发的指令一致。
		if pending, _ := store.Signals.LoadPendingCommit(); pending != nil {
			return fmt.Sprintf("恢复：第 %d 章提交中断", pending.Chapter)
		}
		if len(progress.PendingRewrites) > 0 {
			verb := "重写"
			if progress.Flow == domain.FlowPolishing {
				verb = "打磨"
			}
			return fmt.Sprintf("%s恢复：%d 章待处理", verb, len(progress.PendingRewrites))
		}
		if progress.Flow == domain.FlowReviewing {
			return "恢复：审阅中断"
		}
		if progress.InProgressChapter > 0 {
			return fmt.Sprintf("恢复：第 %d 章进行中", progress.InProgressChapter)
		}
		if label := describeArcEndLabel(store, progress); label != "" {
			return label
		}
		return fmt.Sprintf("恢复：从第 %d 章继续", progress.NextChapter())
	}
	return "恢复"
}

// describeArcEndLabel 为弧末/卷末的多种中间状态生成贴合 UI 的标签。
// 与 flow.Route 的弧末分支保持同序，保证 label 与 Router 首条指令对齐。
func describeArcEndLabel(store *storepkg.Store, progress *domain.Progress) string {
	if !progress.Layered || len(progress.CompletedChapters) == 0 {
		return ""
	}
	lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
	boundary, err := store.Outline.CheckArcBoundary(lastCh)
	if err != nil || boundary == nil || !boundary.IsArcEnd {
		return ""
	}
	vol, arc := boundary.Volume, boundary.Arc
	switch {
	case !store.World.HasArcReview(lastCh):
		return fmt.Sprintf("恢复：弧末评审待处理（V%d A%d）", vol, arc)
	case !store.Summaries.HasArcSummary(vol, arc):
		return fmt.Sprintf("恢复：弧摘要待生成（V%d A%d）", vol, arc)
	case boundary.IsVolumeEnd && !store.Summaries.HasVolumeSummary(vol):
		return fmt.Sprintf("恢复：卷摘要待生成（V%d）", vol)
	case boundary.NeedsExpansion && boundary.NextArc > 0:
		return fmt.Sprintf("恢复：待展开下一弧（V%d A%d）", boundary.NextVolume, boundary.NextArc)
	case boundary.NeedsNewVolume:
		return fmt.Sprintf("恢复：待决策下一卷（V%d 末）", vol)
	}
	return ""
}
