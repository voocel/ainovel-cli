package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/store"
)

// buildStoryStateSummary 组装一段精简的故事现状Tóm tắt，供阶段共创助手了解"已经写了什么"。
// 复用 store 访问点，只取规划方向所需的高层事实（Tiến độ / 罗盘 / 最近卷 / 主要Nhân vật / 活跃伏笔）；
// 不拉Chính văn、不喂 novel_context 的全量 JSON——共创是对话，要的是可读Tổng quan，不是ViếtNgữ cảnh。
// 任一项缺失都Bỏ qua（best-effort），Quay lạiRỗng串表示尚Không có可用Tiến độ。
func buildStoryStateSummary(s *store.Store) string {
	if s == nil {
		return ""
	}
	var b strings.Builder

	if progress, _ := s.Progress.Load(); progress != nil {
		if name := strings.TrimSpace(progress.NovelName); name != "" {
			fmt.Fprintf(&b, "- Tên sách：《%s》\n", name)
		}
		fmt.Fprintf(&b, "- Tiến độ：Đã hoàn thành %d 章", len(progress.CompletedChapters))
		if progress.TotalChapters > 0 {
			fmt.Fprintf(&b, " / 规划 %d 章", progress.TotalChapters)
		}
		fmt.Fprintf(&b, "，约 %d 字，下一章为第 %d 章\n", progress.TotalWordCount, progress.NextChapter())
		if progress.Layered && progress.CurrentVolume > 0 {
			fmt.Fprintf(&b, "- Hiện tại位置：第 %d 卷 第 %d 弧\n", progress.CurrentVolume, progress.CurrentArc)
		}
	}

	if compass, _ := s.Outline.LoadCompass(); compass != nil {
		if dir := strings.TrimSpace(compass.EndingDirection); dir != "" {
			fmt.Fprintf(&b, "- 终局方向：%s\n", dir)
		}
		if compass.EstimatedScale != "" {
			fmt.Fprintf(&b, "- 预估规模：%s\n", compass.EstimatedScale)
		}
		if len(compass.OpenThreads) > 0 {
			fmt.Fprintf(&b, "- 活跃长线：%s\n", strings.Join(compass.OpenThreads, "；"))
		}
	}

	// 最近一卷Tóm tắt，让助手知道故事刚走到哪
	if vols, _ := s.Summaries.LoadAllVolumeSummaries(); len(vols) > 0 {
		last := vols[len(vols)-1]
		fmt.Fprintf(&b, "- 最近《%s》：%s\n", last.Title, truncate(last.Summary, 200))
	}

	// 主要Nhân vật（core/important），最多 8 个
	if chars, _ := s.Characters.Load(); len(chars) > 0 {
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
			fmt.Fprintf(&b, "- 主要Nhân vật：%s\n", strings.Join(names, "、"))
		}
	}

	// 未收伏笔，最多 6 条
	if fs, _ := s.World.LoadActiveForeshadow(); len(fs) > 0 {
		var items []string
		for _, f := range fs {
			items = append(items, truncate(f.Description, 40))
			if len(items) >= 6 {
				break
			}
		}
		fmt.Fprintf(&b, "- 未收伏笔：%s\n", strings.Join(items, "；"))
	}

	return strings.TrimSpace(b.String())
}

// stageSystemPrompt 组装阶段共创的完整系统提示：阶段 prompt + Hiện tại故事Trạng tháiTóm tắt。
// Tóm tắt作为数据附录挂在末尾（用分隔线与格式规范隔开），呼应 prompt 里"Tiến độ见下方"的指引。
func stageSystemPrompt(s *store.Store) string {
	prompt := stageCoCreateSystemPrompt
	if summary := buildStoryStateSummary(s); summary != "" {
		prompt += "\n\n---\n## Hiện tại故事Trạng thái\n（以下是已写内容的客观Tóm tắt，供你规划后续时参照，不要在 <draft> 里照抄原文）\n" + summary
	}
	return prompt
}
