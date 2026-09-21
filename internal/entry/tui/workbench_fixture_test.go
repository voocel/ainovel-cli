package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// studioModel 是正在写第 4 章的作品：三章已入稿、第 4 章正文直播中、两条要求、有用量。
func studioModel(t *testing.T, width, height int) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page, m.width, m.height = pageWorkbench, width, height
	m.bench = newWorkbenchState("letters", 1)
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "letters", Intent: domainmodel.Intent{Premise: "亡者来信", TargetChapters: 8}, TargetChapters: 8,
		Run:          &domainmodel.CreationRun{ID: "run", State: domainmodel.RunRunning},
		CurrentPhase: "撰写第 4 章",
		Directives: []domainmodel.Directive{
			{ID: "d1", Scope: "project", Text: "保持悬疑氛围，不要过早揭示门后人的身份", Status: domainmodel.DirectiveActive},
			{ID: "d2", Scope: "chapter_range:4-4", Text: "用声音与一个具体动作承接悬念", Status: domainmodel.DirectiveActive},
		},
		Outline: []workbench.OutlineNode{{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "第一卷 · 未寄出的信"}}},
	}
	texts := []string{
		"镇上最后一间邮局，在黄昏六点关门。\n\n陈渡整理完柜台上的退信，发现最底下多了一封没有邮戳的信。信封潮湿，像刚从河里捞起来。\n\n收件人的名字，他在墓园里见过。",
		"来客没有撑伞。雨从他的帽檐滴下来，却没有在地板上留下水迹。\n\n“有我的信吗？”\n\n陈渡看向柜台后的旧挂钟。秒针停在同一个地方，已经整整三分钟。",
		"那封回信只有一行字。\n\n别在天亮前敲门。\n\n陈渡把信翻过来，背面是自己的笔迹。他记不起写过这句话，正如他记不起，为什么口袋里会有一把陌生的钥匙。",
	}
	for i, title := range []string{"无人签收", "雨夜来客", "回信", "门后的声音"} {
		state := workbench.ChapterConfirmed
		if i == 3 {
			state = workbench.ChapterInProgress
		}
		m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{
			Node: domainmodel.PlanNode{ID: fmt.Sprint("c", i+1), Kind: domainmodel.PlanChapter, ParentID: "v1", Title: title}, Number: i + 1, State: state,
		})
		if i < 3 {
			m.bench.snap.Manuscript = append(m.bench.snap.Manuscript, domainmodel.ManuscriptChapter{
				ID: fmt.Sprint("ch-", i+1), PlanNodeID: fmt.Sprint("c", i+1), Number: i + 1, Title: title,
				Blocks: []domainmodel.ManuscriptBlock{{ID: "b", Text: strings.Repeat(texts[i]+"\n\n", 3)}},
			})
		}
	}
	m.bench.cursor = 4 // 行 0 是卷头，第 4 章在行 4
	at := time.Now().Add(-12 * time.Second)
	thinking := "保留上一章的雨声作为过渡。门后的人可以认出他的脚步，但暂时不解释身份。用声音与一个具体动作承接悬念，让读者先感到异常。"
	prose := "雨停了，屋檐却还在滴水。\n\n陈渡站在门前，把那封没有邮戳的信翻了过来。收件人的名字已经被雨水洇开，只有最后一个“渡”字，还像一枚钉子，牢牢留在纸上。\n\n他没有敲门。\n\n门后却传来一个声音：“你今天来晚了。”"
	m.bench.activity = activity.Snapshot{
		Seq: 1, ProjectID: "letters", RunID: "run", OperationID: "writing", Thinking: true, ThinkingSeen: true,
		Entries: []activity.Entry{
			{ID: 1, OperationID: "writing", Kind: activity.ToolStart, Tool: "authority_read", Done: true, At: at, DoneAt: at.Add(2 * time.Second)},
			{ID: 2, OperationID: "writing", Kind: activity.ToolStart, Tool: "workspace_put_chapter", Bytes: 2048, At: at.Add(10 * time.Second)},
		},
		Tasks: []activity.Task{
			{
				OperationID: "planning", Label: "全书规划", Kind: string(domainmodel.OperationDevelopPlan), Done: true,
				StartedAt: at.Add(-10 * time.Minute), EndedAt: at.Add(-6 * time.Minute), Turns: 4, Calls: 3,
				Usage: activity.UsageTotals{Input: 41000, Output: 9000, CacheRead: 4900, Cost: 0.31},
			},
			{
				OperationID: "writing", Label: "第 4 章写作", Kind: string(domainmodel.OperationWriteChapter),
				Phase: activity.Prose, StartedAt: at, Scope: activity.Scope{ChapterNumber: 4}, Turns: 3, Calls: 10, Retries: 1,
				Usage: activity.UsageTotals{Input: 192000, Output: 49000, CacheRead: 181500, Cost: 0.11},
			},
		},
		Models: []activity.ModelUsage{
			{Model: "deepseek-v4-pro", Provider: "deepseek", Messages: 3, FirstKind: string(domainmodel.OperationDevelopPlan),
				FirstAt: at.Add(-10 * time.Minute), LastAt: at.Add(-6 * time.Minute),
				Usage: activity.UsageTotals{Input: 41000, Output: 9000, CacheRead: 4900, Cost: 0.31}},
			{Model: "deepseek-v4-flash", Provider: "deepseek", Messages: 7, FirstKind: string(domainmodel.OperationWriteChapter),
				FirstAt: at.Add(-5 * time.Minute), LastAt: at,
				Usage: activity.UsageTotals{Input: 192000, Output: 49000, CacheRead: 181500, Cost: 0.11}},
		},
		ActiveModel: "deepseek-v4-flash", Worked: 4 * time.Minute,
		Output: []activity.OutputBlock{
			{ID: 1, Version: 1, OperationID: "writing", TaskLabel: "第 4 章写作", Kind: activity.Thinking, At: at.Add(time.Second), Scope: activity.Scope{ChapterNumber: 4}, Text: []byte(thinking)},
			{ID: 2, Version: 1, OperationID: "writing", TaskLabel: "第 4 章写作", Kind: activity.Prose, At: at.Add(10 * time.Second), Scope: activity.Scope{ChapterNumber: 4}, Text: []byte(prose)},
		},
		Usage: activity.UsageTotals{Input: 233000, Output: 58000, CacheRead: 186400, Cost: 0.42},
	}
	return m
}

// longWorkbench 是 count 章、每 50 章一卷、正文直播中的长篇夹具（不依赖服务）。
func longWorkbench(tb testing.TB, count int) model {
	deps, _ := newTestDeps(tb, true)
	m := newModel(context.Background(), deps)
	m.width, m.height, m.page = 150, 40, pageWorkbench
	m.bench = newWorkbenchState("long", 1)
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap.Intent.TargetChapters = count + 500
	m.bench.snap.TargetChapters = count + 500
	m.bench.snap.Run = &domainmodel.CreationRun{ID: "run", State: domainmodel.RunRunning}
	for i := 1; i <= count; i++ {
		if i%50 == 1 {
			m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprintf("v%d", i), Kind: domainmodel.PlanVolume, Title: fmt.Sprintf("第%d卷", i/50+1)}})
		}
		m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprint(i), Kind: domainmodel.PlanChapter, Title: fmt.Sprintf("雨夜来信 %d", i)}, Number: i, State: workbench.ChapterConfirmed})
		m.bench.snap.Manuscript = append(m.bench.snap.Manuscript, domainmodel.ManuscriptChapter{ID: fmt.Sprint(i), Number: i, Title: "雨夜来信", Blocks: []domainmodel.ManuscriptBlock{{Text: strings.Repeat("雨停了，屋檐却还在滴水。", 250)}}})
	}
	m.bench.activity = activity.Snapshot{Seq: 1, RunID: "run"}
	m.bench.activity.Output = []activity.OutputBlock{
		{ID: 1, Version: 1, Kind: activity.Thinking, Text: []byte(strings.Repeat("保留雨声作为过渡。", 450))},
		{ID: 2, Version: 1, Kind: activity.Prose, Text: []byte(strings.Repeat("雨停了，屋檐却还在滴水。", 700))},
	}
	return m
}

func clickBench(m model, x, y int) model {
	updated, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return updated.(model)
}

func wheelBench(m model, x, y int, up bool) model {
	button := tea.MouseButtonWheelDown
	if up {
		button = tea.MouseButtonWheelUp
	}
	updated, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: button})
	return updated.(model)
}
