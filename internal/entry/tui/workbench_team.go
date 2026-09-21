package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// Rendering and mouse handling consume the same frame, including variable-height cards.
type inspectorHit struct {
	start, end int
	task       activity.Task
	review     bool
	x, width   int // Nonzero width restricts a text action to its visible label.
	detail     string
}

type inspectorFrame struct {
	lines []string
	hits  []inspectorHit
}

func taskRole(task activity.Task) string {
	switch domainmodel.OperationKind(task.Kind) {
	case domainmodel.OperationInitializeProject, domainmodel.OperationDevelopPlan, domainmodel.OperationRevisePlan:
		return "策划"
	case domainmodel.OperationWriteChapter, domainmodel.OperationRewriteChapter, domainmodel.OperationRewriteAffected:
		return "作者"
	case domainmodel.OperationReviewRange:
		return "编辑"
	case domainmodel.OperationReviseCanon:
		return "设定整理"
	default:
		return "创作助手"
	}
}

func taskStage(task activity.Task) string {
	switch task.Phase {
	case activity.TurnStart:
		return "等待模型响应"
	case activity.Thinking:
		return "思考中"
	case activity.Text:
		return "正在输出说明"
	case activity.Prose:
		return "正在写正文"
	case activity.ToolStart, activity.ToolDelta:
		return toolActivityLabel(task.Tool)
	case activity.Retry:
		return "正在重试"
	default:
		return "准备中"
	}
}

func (m model) teamFrame(width, height int) inspectorFrame {
	f := inspectorFrame{}
	add := func(text string) { f.lines = append(f.lines, fitLine(text, width)) }
	if d := m.bench.decision; d != nil {
		start := len(f.lines)
		add(benchTheme.Warning.Render("需要你决定"))
		add(m.reviewTarget())
		f.lines = append(f.lines, inspectorLimit(contextLines("", d.reason, width), 3)...)
		if d.hasProposal {
			add(benchTheme.Accent.Render("点击审阅候选稿 →"))
			f.hits = append(f.hits, inspectorHit{start: start, end: len(f.lines), review: true})
		} else {
			add(benchTheme.Muted.Render("/continue 继续 · /budget 调整预算"))
		}
		add("")
	}
	feed, _ := m.activityFeed()
	var active, finished []activity.Task
	for i := len(feed.Tasks) - 1; i >= 0; i-- {
		task := feed.Tasks[i]
		if task.Done {
			finished = append(finished, task)
		} else {
			active = append(active, task)
		}
	}
	teamTitle := "创作团队"
	if len(active) > 0 {
		teamTitle += fmt.Sprintf(" · %d 项执行中", len(active))
	}
	add(benchTheme.Accent.Bold(true).Render(teamTitle))
	add("")
	if len(active) == 0 {
		status := "暂无实时任务"
		if m.bench.hasRun() {
			status = runStateLabel(m.bench.run().State) + " · 暂无实时任务"
		}
		add(benchTheme.Muted.Render("  " + status))
		add("")
	}
	// Reserve space for chapter context even when several executions are active.
	capacity := max(1, (height-len(f.lines)-10)/5)
	for i, task := range active {
		if i >= capacity {
			add(benchTheme.Muted.Render(fmt.Sprintf("另有 %d 个执行中任务 · /diag 查看", len(active)-i)))
			break
		}
		start := len(f.lines)
		title := spinnerFrames[m.bench.spin%len(spinnerFrames)] + " " + taskRole(task)
		if task.Scope.ChapterNumber > 0 {
			title += fmt.Sprintf(" · 第 %d 章", task.Scope.ChapterNumber)
		} else if len(task.Scope.ChapterIDs) > 0 {
			title += fmt.Sprintf(" · %d 章", len(task.Scope.ChapterIDs))
		}
		add(benchTheme.Accent.Bold(true).Render("▎ " + title))
		add("  " + task.Label)
		elapsed := ""
		if !task.StartedAt.IsZero() {
			elapsed = " · " + formatDuration(max(time.Duration(0), time.Since(task.StartedAt)))
		}
		add(benchTheme.Muted.Render("  " + taskStage(task) + elapsed))
		prose := 0
		for _, block := range feed.Output {
			if block.OperationID == task.OperationID && block.Kind == activity.Prose {
				prose += utf8.RuneCount(block.Text)
			}
		}
		hint := "点击查看任务输出 →"
		if prose > 0 {
			hint = fmt.Sprintf("已保留正文预览 %s 字 →", groupDigits(prose))
		} else if task.Attempt > 1 {
			hint = fmt.Sprintf("第 %d 次执行 · 点击查看 →", task.Attempt)
		}
		add(benchTheme.Muted.Render("  " + hint))
		f.hits = append(f.hits, inspectorHit{start: start, end: len(f.lines), task: task})
		add("")
	}
	if len(finished) > 0 && height-len(f.lines) >= 12 {
		add(benchTheme.Muted.Render("最近动态"))
		slices.SortStableFunc(finished, func(a, b activity.Task) int { return b.EndedAt.Compare(a.EndedAt) })
		for _, task := range finished[:min(2, len(finished))] {
			start := len(f.lines)
			label := "✓ " + taskRole(task) + " · 执行完成"
			if task.Err != "" {
				label = "! " + taskRole(task) + " · 执行失败"
				label = benchTheme.Warning.Render(label)
			}
			add("  " + label)
			add(benchTheme.Muted.Render("    " + task.Label))
			f.hits = append(f.hits, inspectorHit{start: start, end: len(f.lines), task: task})
		}
		add("")
	}
	remaining := height - len(f.lines) - 2
	if remaining > 0 {
		start := len(f.lines)
		detail := m.detailFrame(width, remaining)
		f.lines = append(f.lines, detail.lines...)
		for _, hit := range detail.hits {
			hit.start, hit.end = hit.start+start, hit.end+start
			f.hits = append(f.hits, hit)
		}
	}
	for len(f.lines) < height-1 {
		add("")
	}
	add(benchTheme.Muted.Render(approvalLabel(m.bench.snap.Approval) + " · /view 完整上下文"))
	f.lines = fitBlock(strings.Join(f.lines, "\n"), width, height)
	return f
}
