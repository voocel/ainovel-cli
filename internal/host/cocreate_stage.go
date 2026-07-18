package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/store"
)

// buildStoryStateSummary 组装一段精简的故事现状摘要，供阶段共创助手了解"已经写了什么"。
// 复用 store 访问点，只取规划方向所需的高层事实（进度 / 罗盘 / 最近卷 / 主要人物 / 活跃伏笔）；
// 不拉正文、不喂 novel_context 的全量 JSON——共创是对话，要的是可读概览，不是写作上下文。
// 任一项缺失都跳过（best-effort），返回空串表示尚无可用进度。
func buildStoryStateSummary(s *store.Store) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	var warnings []string
	warn := func(scope string, err error) {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s 读取失败: %v", scope, err))
		}
	}

	if progress, err := s.Progress.Load(); progress != nil {
		if name := strings.TrimSpace(progress.NovelName); name != "" {
			fmt.Fprintf(&b, "- 书名：《%s》\n", name)
		}
		fmt.Fprintf(&b, "- 进度：已完成 %d 章", len(progress.CompletedChapters))
		if progress.TotalChapters > 0 {
			fmt.Fprintf(&b, " / 规划 %d 章", progress.TotalChapters)
		}
		fmt.Fprintf(&b, "，约 %d 字，下一章为第 %d 章\n", progress.TotalWordCount, progress.NextChapter())
		if progress.Layered && progress.CurrentVolume > 0 {
			fmt.Fprintf(&b, "- 当前位置：第 %d 卷 第 %d 弧\n", progress.CurrentVolume, progress.CurrentArc)
		}
	} else {
		warn("progress", err)
	}

	if compass, err := s.Outline.LoadCompass(); compass != nil {
		if dir := strings.TrimSpace(compass.EndingDirection); dir != "" {
			fmt.Fprintf(&b, "- 终局方向：%s\n", dir)
		}
		if compass.EstimatedScale != "" {
			fmt.Fprintf(&b, "- 预估规模：%s\n", compass.EstimatedScale)
		}
		if len(compass.OpenThreads) > 0 {
			fmt.Fprintf(&b, "- 活跃长线：%s\n", strings.Join(compass.OpenThreads, "；"))
		}
	} else {
		warn("story_compass", err)
	}

	// 最近一卷摘要，让助手知道故事刚走到哪
	if vols, err := s.Summaries.LoadAllVolumeSummaries(); len(vols) > 0 {
		last := vols[len(vols)-1]
		fmt.Fprintf(&b, "- 最近《%s》：%s\n", last.Title, truncate(last.Summary, 200))
	} else {
		warn("volume_summaries", err)
	}

	// 主要人物（core/important），最多 8 个
	if chars, err := s.Characters.Load(); len(chars) > 0 {
		var names []string
		for _, c := range chars {
			if c.Tier == "secondary" || c.Tier == "decorative" {
				continue
			}
			line := c.Name
			if role := strings.TrimSpace(c.Role); role != "" {
				line += "（" + role + "）"
			}
			names = append(names, line)
			if len(names) >= 8 {
				break
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "- 主要人物：%s\n", strings.Join(names, "、"))
		}
	} else {
		warn("characters", err)
	}

	// 未收伏笔，最多 6 条
	if fs, err := s.World.LoadActiveForeshadow(); len(fs) > 0 {
		var items []string
		for _, f := range fs {
			items = append(items, truncate(f.Description, 40))
			if len(items) >= 6 {
				break
			}
		}
		fmt.Fprintf(&b, "- 未收伏笔：%s\n", strings.Join(items, "；"))
	} else {
		warn("foreshadow", err)
	}

	if len(warnings) > 0 {
		fmt.Fprintf(&b, "- 数据告警：%s\n", strings.Join(warnings, "；"))
	}

	return strings.TrimSpace(b.String())
}

// stageSystemPrompt 组装阶段共创的完整系统提示：阶段 prompt + 当前故事状态摘要。
// 摘要作为数据附录挂在末尾（用分隔线与格式规范隔开），呼应 prompt 里"进度见下方"的指引。
func stageSystemPrompt(s *store.Store) string {
	prompt := stageCoCreateSystemPrompt
	if summary := buildStoryStateSummary(s); summary != "" {
		prompt += "\n\n---\n## 当前故事状态\n（以下是已写内容的客观摘要，供你规划后续时参照，不要在 <draft> 里照抄原文）\n" + summary
	}
	return prompt
}
