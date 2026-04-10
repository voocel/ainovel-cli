package host

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// buildResumePrompt 从持久化事实生成恢复指令。
// 返回 (prompt, label, error)。label 为空表示无可恢复状态(应走新建)。
func buildResumePrompt(store *storepkg.Store) (string, string, error) {
	progress, err := store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	if progress == nil {
		return "", "", nil // 新建
	}

	// 已完成
	if progress.Phase == domain.PhaseComplete {
		return "", "", nil
	}

	var b strings.Builder
	b.WriteString("[恢复指令]\n\n")

	// 构建进度描述
	title := progress.NovelName
	if title == "" {
		title = "当前小说"
	}
	b.WriteString(fmt.Sprintf("本书「%s」", title))

	completedCount := len(progress.CompletedChapters)
	if completedCount > 0 {
		b.WriteString(fmt.Sprintf("已完成 %d 章", completedCount))
		if progress.TotalChapters > 0 {
			b.WriteString(fmt.Sprintf("（共 %d 章）", progress.TotalChapters))
		}
		b.WriteString(fmt.Sprintf("，共 %d 字。", progress.TotalWordCount))
	}

	// 当前阶段
	label := "恢复"
	switch progress.Phase {
	case domain.PhasePremise, domain.PhaseOutline:
		b.WriteString("\n上次在规划阶段中断。请调用 novel_context 检查当前基础设定状态，补全缺失项，然后开始写作。")
		label = fmt.Sprintf("恢复：规划阶段（%s）", progress.Phase)

	case domain.PhaseWriting:
		// 用 checkpoint 确定精确位置
		latest := store.Checkpoints.LatestGlobal()

		// 待处理的提交中断
		if pending, _ := store.Signals.LoadPendingCommit(); pending != nil {
			b.WriteString(fmt.Sprintf("\n第 %d 章提交中途中断（阶段：%s）。请调用 writer 重新提交该章。", pending.Chapter, pending.Stage))
			label = fmt.Sprintf("恢复：第 %d 章提交中断", pending.Chapter)
			break
		}

		// 待重写
		if len(progress.PendingRewrites) > 0 {
			verb := "重写"
			if progress.Flow == domain.FlowPolishing {
				verb = "打磨"
			}
			b.WriteString(fmt.Sprintf("\n有 %d 章待%s：%v。原因：%s。", len(progress.PendingRewrites), verb, progress.PendingRewrites, progress.RewriteReason))
			b.WriteString(fmt.Sprintf("\n请逐章调用 writer 执行%s，全部完成后继续写新章节。", verb))
			label = fmt.Sprintf("%s恢复：%d 章待处理", verb, len(progress.PendingRewrites))
			break
		}

		// 审阅中断
		if progress.Flow == domain.FlowReviewing {
			b.WriteString("\n上次审阅中断。请调用 editor 对已完成章节进行审阅。")
			label = "恢复：审阅中断"
			break
		}

		// 章节进行中 — 用 checkpoint 精确到 step
		if progress.InProgressChapter > 0 {
			ch := progress.InProgressChapter
			step := ""
			if latest != nil && latest.Scope.Kind == domain.ScopeChapter && latest.Scope.Chapter == ch {
				step = latest.Step
			}
			switch step {
			case "plan":
				b.WriteString(fmt.Sprintf("\n第 %d 章计划已完成，请调用 writer 继续写草稿（draft_chapter）。", ch))
			case "draft":
				b.WriteString(fmt.Sprintf("\n第 %d 章草稿已落盘。请调用 writer 继续一致性检查（read_chapter + check_consistency），然后 commit。", ch))
			case "consistency_check":
				b.WriteString(fmt.Sprintf("\n第 %d 章已完成一致性检查。请调用 writer 提交（commit_chapter）。", ch))
			default:
				b.WriteString(fmt.Sprintf("\n第 %d 章正在进行中。请调用 writer 继续完成该章（可用 read_chapter 读取已有草稿）。", ch))
			}
			label = fmt.Sprintf("恢复：第 %d 章进行中（%s）", ch, step)
			break
		}

		// 正常续写
		nextCh := progress.NextChapter()
		b.WriteString(fmt.Sprintf("\n请从第 %d 章继续写作。", nextCh))
		if progress.TotalChapters > 0 {
			b.WriteString(fmt.Sprintf("总共需要写 %d 章。", progress.TotalChapters))
		}
		label = fmt.Sprintf("恢复：从第 %d 章继续", nextCh)
	}

	// 待处理的用户干预
	if meta, _ := store.RunMeta.Load(); meta != nil && meta.PendingSteer != "" {
		b.WriteString(fmt.Sprintf("\n\n用户在停机期间留下了一条干预意见：\n「%s」\n请先评估影响范围，必要时调规划师微调并逐章重写受影响章节。", meta.PendingSteer))
	}

	b.WriteString("\n\n如需了解详情请调用 novel_context。")
	return b.String(), label, nil
}
