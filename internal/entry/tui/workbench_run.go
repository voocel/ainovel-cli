package tui

import (
	"fmt"
	"strings"
	"time"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// 右栏「本轮运行」只放随创作实时变化的运行数据：用过的模型与各自用量、
// 正在跑的代理与它的轮次/调用/重试、本轮合计。章节依据走 /view，不常驻。

// kindRole 由执行的 Operation Kind 定角色，不从模型文字猜身份。
func kindRole(kind string) string {
	switch domainmodel.OperationKind(kind) {
	case domainmodel.OperationInitializeProject, domainmodel.OperationDevelopPlan, domainmodel.OperationRevisePlan:
		return "策划"
	case domainmodel.OperationWriteChapter, domainmodel.OperationRewriteChapter, domainmodel.OperationRewriteAffected:
		return "作者"
	case domainmodel.OperationReviewRange:
		return "编辑"
	case domainmodel.OperationReviseCanon:
		return "设定整理"
	case domainmodel.OperationGenerateAsset, domainmodel.OperationInspectAsset:
		return "素材"
	default:
		return "代理"
	}
}

// taskName 「角色 · 对象」：对象来自任务输入里的章节范围，未知种类退回任务名。
func taskName(task activity.Task) string {
	chapter := fmt.Sprintf("第 %d 章", task.Scope.ChapterNumber)
	chapters := fmt.Sprintf("%d 章", len(task.Scope.ChapterIDs))
	var object string
	switch domainmodel.OperationKind(task.Kind) {
	case domainmodel.OperationInitializeProject:
		object = "创作设定"
	case domainmodel.OperationDevelopPlan:
		object = "故事蓝图"
	case domainmodel.OperationRevisePlan:
		object = "调整规划"
	case domainmodel.OperationWriteChapter:
		object = chapter
	case domainmodel.OperationRewriteChapter:
		object = "重写" + chapter
	case domainmodel.OperationRewriteAffected:
		object = "修订 " + chapters
	case domainmodel.OperationReviewRange:
		object = "审阅 " + chapters
	case domainmodel.OperationReviseCanon:
		object = "核验事实"
	case domainmodel.OperationGenerateAsset:
		object = "生成素材"
	case domainmodel.OperationInspectAsset:
		object = "检查素材"
	default:
		object = task.Label
	}
	return kindRole(task.Kind) + " · " + object
}

// tokensLine 「↑输入 ↓输出 · 缓存 命中率」；费用另起，最窄的右栏（30 列）也放得下。
func tokensLine(usage activity.UsageTotals) string {
	line := fmt.Sprintf("↑%s ↓%s", formatTokens(usage.Input), formatTokens(usage.Output))
	if usage.CacheRead > 0 && usage.Input > 0 {
		line += fmt.Sprintf(" · 缓存 %.0f%%", 100*float64(usage.CacheRead)/float64(usage.Input))
	}
	return line
}

// workedTime 本轮实际执行时长：已结束任务之和 + 进行中任务到现在。
func workedTime(feed activity.Snapshot) time.Duration {
	worked := feed.Worked
	for _, task := range feed.Tasks {
		if !task.Done && !task.StartedAt.IsZero() {
			worked += time.Since(task.StartedAt)
		}
	}
	return worked
}

func railSection(title, note string, width int) string {
	return alignRight(benchTheme.Muted.Render(title), benchTheme.Muted.Render(note), width)
}

// runColumn 右栏：标题、模型、代理、本轮合计；底部是命令入口。
func (m model) runColumn(l benchLayout) []string {
	width := l.railWidth - 2
	lines := []string{benchTheme.Muted.Render("本轮运行"), ""}
	feed, ok := m.activityFeed()
	if !ok {
		lines = append(lines, benchTheme.Muted.Render(m.idleSceneText()))
	} else {
		lines = append(lines, m.modelLines(feed, width)...)
		lines = append(lines, "")
		lines = append(lines, m.agentLines(feed, width)...)
		lines = append(lines, "")
		lines = append(lines, m.totalLines(feed, width)...)
	}
	footer := benchTheme.Muted.Render("/view 本章详情 · /diag 诊断")
	for len(lines) < l.bodyHeight-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:l.bodyHeight-1], footer)
	for i := range lines {
		lines[i] = " " + fitLine(lines[i], width)
	}
	return fitBlock(strings.Join(lines, "\n"), l.railWidth, l.bodyHeight)
}

// modelLines 用过的模型按首次使用排序；当前模型标 ●，换过的模型注明提供方与何时起用。
func (m model) modelLines(feed activity.Snapshot, width int) []string {
	note := fmt.Sprintf("%d 个", len(feed.Models))
	if len(feed.Models) > 1 {
		note += fmt.Sprintf(" · 切换 %d 次", len(feed.Models)-1)
	}
	lines := []string{railSection("模型", note, width)}
	if len(feed.Models) == 0 {
		return append(lines, benchTheme.Muted.Render(m.bindingLabel()+" · 尚无用量"))
	}
	for _, entry := range feed.Models {
		name := entry.Model
		if name == "" {
			name = m.api.Models.Current("").Model
		}
		mark, tag := benchTheme.Muted.Render("·"), entry.Provider
		if entry.Model == feed.ActiveModel {
			mark, tag = benchTheme.Accent.Render("●"), "当前"
		}
		lines = append(lines,
			alignRight(mark+" "+benchTheme.Text.Render(name), benchTheme.Muted.Render(tag), width),
			"  "+benchTheme.Muted.Render(tokensLine(entry.Usage)),
			"  "+benchTheme.Muted.Render(fmt.Sprintf("%s · %d 条消息 · %s 起", formatCost(entry.Usage.Cost), entry.Messages, entry.FirstAt.Local().Format("15:04"))),
		)
	}
	return lines
}

// agentLines 正在跑的代理（轮次、调用、重试、时长、本任务费用）与本轮已完成的任务。
func (m model) agentLines(feed activity.Snapshot, width int) []string {
	active := -1
	for i := len(feed.Tasks) - 1; i >= 0; i-- {
		if !feed.Tasks[i].Done {
			active = i
			break
		}
	}
	note := ""
	if active >= 0 {
		note = fmt.Sprintf("第 %d 轮", feed.Tasks[active].Turns)
	}
	lines := []string{railSection("代理", note, width)}
	if active >= 0 {
		task := feed.Tasks[active]
		lines = append(lines,
			styleFocus.Render("◉ "+taskName(task)),
			"  "+benchTheme.Muted.Render(fmt.Sprintf("工具调用 %d · 重试 %d", task.Calls, task.Retries)),
			"  "+benchTheme.Muted.Render(fmt.Sprintf("已用 %s · 本任务 %s", formatDuration(time.Since(task.StartedAt)), formatCost(task.Usage.Cost))),
		)
	}
	shown := 0
	for i := len(feed.Tasks) - 1; i >= 0 && shown < 5; i-- {
		task := feed.Tasks[i]
		if !task.Done {
			continue
		}
		mark := styleNotice.Render("✓")
		if task.Err != "" {
			mark = benchTheme.Warning.Render("!")
		}
		stat := formatDuration(task.EndedAt.Sub(task.StartedAt))
		if task.Usage.Cost > 0 {
			stat += " · " + formatCost(task.Usage.Cost)
		}
		lines = append(lines, mark+" "+benchTheme.Muted.Render(taskName(task)), "  "+benchTheme.Muted.Render(stat))
		shown++
	}
	if active < 0 && shown == 0 {
		lines = append(lines, benchTheme.Muted.Render("本轮还没有任务"))
	}
	return lines
}

func (m model) totalLines(feed activity.Snapshot, width int) []string {
	calls, retries := 0, 0
	for _, task := range feed.Tasks {
		calls += task.Calls
		retries += task.Retries
	}
	return []string{
		railSection("本轮合计", fmt.Sprintf("%d 个任务", len(feed.Tasks)), width),
		"  " + benchTheme.Text.Render(tokensLine(feed.Usage)),
		"  " + benchTheme.Muted.Render(fmt.Sprintf("%s · 已用 %s", formatCost(feed.Usage.Cost), formatDuration(workedTime(feed)))),
		"  " + benchTheme.Muted.Render(fmt.Sprintf("工具调用 %d · 重试 %d", calls, retries)),
	}
}
