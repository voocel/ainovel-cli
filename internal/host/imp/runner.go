package imp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Deps 把 runner 需要的可插拔依赖一次性传入，方便测试 mock。
type Deps struct {
	Store      *store.Store
	CommitTool *tools.CommitChapterTool
	LLM        LLMChat // 同一模型即可，foundation/analyzer 都是结构化反推
	Prompts    Prompts
}

// Prompts 是 imp 流程使用的两段提示词。
type Prompts struct {
	Foundation string // 反推 foundation
	Analyzer   string // 反推单章
}

// Run 执行完整 import 流程：split → foundation → chapter loop。
// 在自己的 goroutine 中跑；Events 通道由本函数关闭。
//
// 设计取舍：
//   - 完整流程是阻塞执行（CLI 长任务），调用方负责开 goroutine 监听通道；
//   - 任意一步失败都直接结束，发 StageError 事件；
//   - chapter 阶段对已完成章节静默跳过（commit_chapter 的幂等是兜底，但跳过 LLM 更省 token）。
func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.CommitTool == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if strings.TrimSpace(opts.SourcePath) == "" {
		return nil, fmt.Errorf("source path is required")
	}

	events := make(chan Event, 32)

	go func() {
		defer close(events)
		emit := func(stage Stage, current, total int, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}

		// ── 1. 切分 ──
		emit(StageSplitting, 0, 0, "切分章节...", nil)
		chapters, err := SplitFile(opts.SourcePath, opts.CustomRegex)
		if err != nil {
			emit(StageError, 0, 0, "切分失败", err)
			return
		}
		total := len(chapters)
		if total == 0 {
			emit(StageError, 0, 0, "未识别到任何章节", fmt.Errorf("no chapters matched"))
			return
		}
		emit(StageSplitting, 0, total, fmt.Sprintf("切分完成：%d 章", total), nil)

		// ── 2. Foundation 反推（已完整时跳过）──
		if needsFoundation(deps.Store, opts) {
			emit(StageFoundation, 0, total, "反推 Foundation 中（一次 LLM 调用）...", nil)
			fr, err := ReverseFoundation(ctx, deps.LLM, deps.Prompts.Foundation, chapters)
			if err != nil {
				emit(StageError, 0, total, "Foundation 反推失败", err)
				return
			}
			scale := pickScale(total)
			if err := PersistFoundation(ctx, deps.Store, scale, fr); err != nil {
				emit(StageError, 0, total, "Foundation 落盘失败", err)
				return
			}
			emit(StageFoundation, 0, total,
				fmt.Sprintf("Foundation 就绪：%d 角色 / %d 规则 / %d 章大纲",
					len(fr.Characters), len(fr.WorldRules), len(fr.Outline)),
				nil)
		} else {
			emit(StageFoundation, 0, total, "Foundation 已存在，跳过反推", nil)
		}

		// ── 3. 章节循环 ──
		premise, _ := deps.Store.Outline.LoadPremise()
		charactersBlock := loadCharactersBlock(deps.Store)

		startIdx := 0
		if opts.ResumeFrom > 1 {
			startIdx = opts.ResumeFrom - 1
		}
		for i := startIdx; i < total; i++ {
			if err := ctx.Err(); err != nil {
				emit(StageError, i+1, total, "用户取消", err)
				return
			}
			chNum := i + 1
			ch := chapters[i]

			// 已完成 → 跳过 LLM
			if deps.Store.Progress.IsChapterCompleted(chNum) {
				emit(StageChapter, chNum, total, fmt.Sprintf("第 %d 章已完成，跳过", chNum), nil)
				continue
			}

			emit(StageChapter, chNum, total, fmt.Sprintf("分析第 %d/%d 章：%s", chNum, total, ch.Title), nil)

			activeHooks, _ := deps.Store.World.LoadActiveForeshadow()
			analysis, err := AnalyzeChapter(ctx, deps.LLM, deps.Prompts.Analyzer,
				chNum, ch.Title, ch.Content, premise, charactersBlock, activeHooks)
			if err != nil {
				emit(StageError, chNum, total, fmt.Sprintf("第 %d 章分析失败", chNum), err)
				return
			}

			if err := PersistChapter(ctx, deps.Store, deps.CommitTool, chNum, ch.Title, ch.Content, analysis); err != nil {
				emit(StageError, chNum, total, fmt.Sprintf("第 %d 章落盘失败", chNum), err)
				return
			}
			emit(StageChapter, chNum, total, fmt.Sprintf("第 %d 章导入完成", chNum), nil)
		}

		emit(StageDone, total, total, fmt.Sprintf("导入完成：%d 章", total), nil)
	}()

	return events, nil
}

// needsFoundation 判断是否需要重新反推 foundation。
// 用户显式 ResumeFrom > 1 视为"接着导入"，跳过反推；否则按 Store 状态判断。
func needsFoundation(st *store.Store, opts Options) bool {
	if opts.ResumeFrom > 1 {
		return false
	}
	return len(st.FoundationMissing()) > 0
}

// pickScale 根据章数给规划级别一个合理的初值；short ≤25, mid ≤80, 否则 long。
// 不影响 import 本身，只影响后续续写时 Coordinator 选择 architect 提示词。
func pickScale(total int) domain.PlanningTier {
	switch {
	case total <= 25:
		return domain.PlanningTierShort
	case total <= 80:
		return domain.PlanningTierMid
	default:
		return domain.PlanningTierLong
	}
}

// loadCharactersBlock 把角色档案渲染成简短文本块（name/role + 一句描述），
// 仅供 LLM 上下文参考，不需要严格结构。
func loadCharactersBlock(st *store.Store) string {
	chars, err := st.Characters.Load()
	if err != nil || len(chars) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, c := range chars {
		fmt.Fprintf(&sb, "- **%s**（%s）：%s\n", c.Name, c.Role, oneLine(c.Description))
	}
	return sb.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
