package tui

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// benchPane 是左右栏的焦点；详情是主区标签，不再占用第三栏。
type benchPane int

const (
	benchPaneOutline benchPane = iota
	benchPaneMain
)

type workbenchState struct {
	projectID string
	// gen 是本次打开的代际号：全部异步消息带着它出生，不匹配即丢弃。
	gen          int
	refresh      *benchRefresh
	rowsCache    *outlineCache
	proseCache   *wrappedText
	thoughtCache *wrappedText
	search       string
	snap         workbench.WorkbenchSnapshot
	loaded       bool
	view         int // 窄屏：0 主区，1 目录
	tab          benchTab
	thoughtOpen  bool
	liveHeld     bool
	liveText     string
	liveOffset   int
	cursor       int // 大纲可见行索引（含卷/弧头行；快照刷新后按身份重新锚定）
	// collapsed 是折叠的卷/弧节点（键 PlanNode.ID）；快照每轮整体替换，
	// 折叠状态只能活在这里，跨刷新存活。
	collapsed map[string]bool
	pane      benchPane
	// pinned 表示创作中用户手动选章、主区固定在选中内容上（§2）；
	// Esc 分层返回：先回活动流，再回欢迎页。
	pinned bool
	// previewOffset/detailOffset 是正文与详情视图的显示行
	// 滚动偏移，随选章归零。
	previewOffset int
	detailOffset  int
	// feedOffset 是活动流历史偏移：0=跟随最新（§2 自动滚动跟随），>0=用户上翻
	// 暂停跟随；新活动到达时按增量补偿，窗口锚定不动，↓ 到底恢复跟随。
	feedOffset int
	reading    bool
	body       viewport.Model
	bodyText   string
	writing    bool
	exporting  bool
	spin       int // 创作中动画帧，随轮询节拍推进
	decision   *decisionState
	prompt     *promptState
	// input 是常驻底部输入框：待裁决时 y 明确通过、n 聚焦写修改意见；
	// 空回车不提交，回以确认指引（页面设计 §2）。
	input  textinput.Model
	notice string
	err    string
	// activity 是实时活动快照（页面设计 §3/§4）：被唤醒后整读，不逐条回放；
	// activityOff 只退订，绝不取消创作。
	activity    activity.Snapshot
	activityCh  <-chan struct{}
	activityOff func()
}

// close 退订活动并释放本次工作台的作品与排版缓存；在途查询完成后自行释放旧 Reader。
func (b *workbenchState) close() {
	if b.activityOff != nil {
		b.activityOff()
	}
	*b = newWorkbenchState(b.projectID, b.gen)
}

func (b *workbenchState) hasRun() bool { return b.snap.Run != nil }

func (b *workbenchState) run() domainmodel.CreationRun {
	if b.snap.Run == nil {
		return domainmodel.CreationRun{}
	}
	return *b.snap.Run
}

// decisionState 是决定卡：等待原因 + 可选的待裁决稿件。
// continueAfter 标记裁决后是否自动续跑（创作运行的稿件续跑，导入草案不续）；
// stale 表示稿件基线已过期（直接通过会撞版本冲突），只留重写路径。
type decisionState struct {
	reason        string
	proposal      domainmodel.Proposal
	hasProposal   bool
	continueAfter bool
	stale         bool
}

// presentDecision 呈现（或清除）决定卡：批准必须由 y 明确确认，
// 输入框不再自动聚焦（按 n 进入修改意见输入）。
func (b *workbenchState) presentDecision(decision *decisionState) {
	b.decision = decision
	b.input.SetValue("")
	b.input.Blur()
}

// promptState 是工作台的单值输入态：调整目标章数、自动修订预算、提出创作要求或接受审阅发现。
type promptState struct {
	purpose string // "target" | "budget" | "directive" | "adjudicate"
	label   string
	scope   string // directive 的作用域，按大纲选中行决定
	finding string // adjudicate 要接受的发现 ID
	input   textinput.Model
}

type quickParams struct {
	projectID string
	premise   string
	chapters  int
	approval  domainmodel.ApprovalPolicy
	// intent 非空时（完善设定入口）先以完整 Intent 初始化作品。
	intent *domainmodel.Intent
}

type quickDoneMsg struct {
	gen    int
	result novel.QuickWriteResult
	err    error
}

type benchRefreshedMsg struct {
	gen  int
	snap workbench.WorkbenchSnapshot
	err  error
}

type decisionDoneMsg struct {
	gen           int
	continueAfter bool
	err           error
}

// runControlMsg 是运行控制（暂停/取消/调预算）的统一回执。
type runControlMsg struct {
	gen  int
	err  error
	next string // "continue" | "refresh"
	note string
}

type diagnosticsMsg struct {
	gen  int
	text string
	err  error
}

type pollMsg struct{ gen int }

// activityMsg 是活动唤醒回执：open=false 表示订阅已取消，不再重挂监听。
type activityMsg struct {
	gen  int
	open bool
}

func newWorkbenchState(projectID string, gen int) workbenchState {
	return workbenchState{
		projectID: projectID, gen: gen, input: newInput(""),
		collapsed: make(map[string]bool), thoughtOpen: true,
		refresh: &benchRefresh{}, rowsCache: &outlineCache{}, proseCache: &wrappedText{}, thoughtCache: &wrappedText{},
	}
}

func pollTick(gen int) tea.Cmd {
	return tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg { return pollMsg{gen: gen} })
}

// watchActivityCmd 等待下一次活动唤醒（合并信号）；醒来后消费侧整读快照。
func (m model) watchActivityCmd() tea.Cmd {
	wake, gen, ctx := m.bench.activityCh, m.bench.gen, m.ctx
	if wake == nil {
		return nil
	}
	return func() tea.Msg {
		_, open := <-wake
		if open {
			// Coalesce provider deltas into at most 20 UI updates per second.
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return activityMsg{gen: gen}
			}
		}
		return activityMsg{gen: gen, open: open}
	}
}

// startQuickWriteCmd 异步驱动创作：QuickWrite 会一直推进到完成或需要等待。
func (m model) startQuickWriteCmd(params quickParams) tea.Cmd {
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	gen := m.bench.gen
	return func() tea.Msg {
		now := time.Now().UTC()
		if params.intent != nil {
			if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
				ProjectID: params.projectID, ChangeID: params.projectID + ":create",
				UserID: user, Reason: "完善设定后创建作品",
				Draft:     projectdoc.ProjectDraft{Intent: *params.intent, Approval: params.approval},
				CreatedAt: now,
			}); err != nil {
				return quickDoneMsg{gen: gen, err: err}
			}
		}
		result, err := api.Novels.QuickWrite(ctx, novel.QuickWriteCommand{
			ProjectID: params.projectID, UserID: user,
			Premise: params.premise, Chapters: params.chapters, Approval: params.approval,
			WorkerID: "tui-" + user, LeaseDuration: time.Minute, CreatedAt: now,
		})
		return quickDoneMsg{gen: gen, result: result, err: err}
	}
}

// refreshBenchCmd 拉取统一工作台快照：权威投影、大纲状态、最近一轮落点
// （含终态）、待决定事项与下一步引导全部来自同一查询模型（页面设计 §4）。
func (m model) refreshBenchCmd() tea.Cmd {
	gate := m.bench.refresh
	if gate.busy {
		gate.pending = true
		return nil
	}
	gate.busy = true
	if gate.reader == nil {
		gate.reader = m.api.Workbench.OpenReader(m.bench.projectID)
	}
	ctx, gen := m.ctx, m.bench.gen
	return func() tea.Msg {
		snap, err := gate.reader.Snapshot(ctx)
		return benchRefreshedMsg{gen: gen, snap: snap, err: err}
	}
}

func (m model) loadDiagnosticsCmd() tea.Cmd {
	api, ctx := m.api, m.ctx
	gen, run := m.bench.gen, m.bench.run()
	return func() tea.Msg {
		events, err := api.Runs.CreationRunEvents(ctx, run.ID)
		if err != nil {
			return diagnosticsMsg{gen: gen, err: err}
		}
		var text strings.Builder
		text.WriteString(fmt.Sprintf("运行诊断 · %s\n状态：%s", run.ID, runStateLabel(run.State)))
		if run.StateReason != "" {
			text.WriteString(" · " + run.StateReason)
		}
		text.WriteString("\n\n")
		for _, event := range events {
			text.WriteString(fmt.Sprintf("#%d %s @ %s\n", event.Sequence, event.Kind, event.CreatedAt.Format("01-02 15:04:05")))
			if len(event.Payload) != 0 {
				text.WriteString("  " + string(event.Payload) + "\n")
			}
		}
		// 下钻各 Operation（M3 错误诊断）：run 事件只有编排流水，真实失败原因
		// 在 Operation.Error 与 operation_events——失败理由里"可展开事件记录"的
		// 承诺在这里兑现。
		operations, err := api.Runs.RunOperations(ctx, run.ID)
		if err != nil {
			return diagnosticsMsg{gen: gen, err: err}
		}
		for _, operation := range operations {
			text.WriteString(fmt.Sprintf("\n任务 %s · %s · %s\n", operation.ID, operation.Kind, operation.State))
			if operation.Error != "" {
				text.WriteString("  错误：" + operation.Error + "\n")
			}
			// 成功任务自纠过的工具报错也要可见：从已持久化消息确定性提取，
			// 不然"曾经出错又纠回来"的过程在诊断里是黑箱。
			issues, err := api.Tasks.OperationToolIssues(ctx, operation.ID)
			if err != nil {
				return diagnosticsMsg{gen: gen, err: err}
			}
			for _, issue := range issues {
				label := issue.Tool
				if label == "" {
					label = "工具"
				}
				// 错误本体不截断：诊断页是可滚动阅读视图，错误必须完整可见。
				text.WriteString(fmt.Sprintf("  工具报错 %s：%s\n", label, issue.Err))
			}
			if operation.State != domainmodel.OperationFailed {
				continue
			}
			operationEvents, err := api.Tasks.OperationEvents(ctx, operation.ID)
			if err != nil {
				return diagnosticsMsg{gen: gen, err: err}
			}
			for _, operationEvent := range operationEvents {
				// 全量消息记录是恢复语义且体量大，不进诊断面板。
				if operationEvent.Kind == "agent.message_committed" {
					continue
				}
				text.WriteString(fmt.Sprintf("  #%d %s %s\n",
					operationEvent.Sequence, operationEvent.Kind, clipDiagnostics(string(operationEvent.Payload))))
			}
		}
		return diagnosticsMsg{gen: gen, text: text.String()}
	}
}

// clipDiagnostics 限制单条原始事件 payload 的呈现长度（任意 JSON 可能很大，
// 完整内容始终在库里）；错误本体（Operation.Error、工具报错）不经此截断。
func clipDiagnostics(text string) string {
	const limit = 2000
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func (m model) applyWorkbench(message tea.Msg) (tea.Model, tea.Cmd) {
	bench := &m.bench
	switch message := message.(type) {
	case exportDoneMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		bench.exporting = false
		if message.err != nil {
			bench.err = message.err.Error()
			return m, nil
		}
		bench.err = ""
		bench.notice = fmt.Sprintf("已导出 %d 章已确认正文 → %s", message.result.Chapters, message.result.Path)
		return m, nil
	case pollMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if bench.writing {
			bench.spin++
			return m, tea.Batch(m.refreshBenchCmd(), pollTick(bench.gen))
		}
		return m, nil
	case activityMsg:
		if message.gen != bench.gen || !message.open {
			return m, nil
		}
		if feed, ok := m.api.Workbench.RunActivity(bench.projectID); ok {
			if bench.feedOffset > 0 {
				end := len(bench.activity.Entries) - bench.feedOffset
				if end > 0 {
					anchor := bench.activity.Entries[end-1].ID
					found := false
					for i, entry := range feed.Entries {
						if entry.ID == anchor {
							bench.feedOffset = len(feed.Entries) - i - 1
							found = true
							break
						}
					}
					if !found {
						bench.feedOffset = max(0, len(feed.Entries)-1)
						bench.notice = "所读活动已超出实时保留范围，已定位到最早保留条目；按 d 查看历史"
					}
				}
			}
			bench.activity = feed
		}
		return m, m.watchActivityCmd()
	case benchRefreshedMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if message.err != nil {
			// 唯一可宽容的瞬态：建书起步、作品还没落库（未加载 + 创作中 +
			// 确为"不存在"）；其余错误（如库损坏）创作中也必须呈现。
			if !bench.loaded {
				if bench.writing && projectdoc.IsNotFound(message.err) {
					return m, nil
				}
				if bench.writing {
					bench.err = message.err.Error()
					return m, nil
				}
				bench.close()
				m.page = pageHome
				m.home = newHomeState()
				m.home.err = "打不开这部作品：" + message.err.Error()
				return m, m.loadLibraryCmd()
			}
			bench.err = message.err.Error()
			return m, nil
		}
		// 快照整体替换会改变大纲行索引：先记下选中行身份，替换后重新锚定
		// （首次加载落在第一个章行）。
		anchorID, anchorChapter := m.selectedRowIdentity()
		bench.snap, bench.loaded = message.snap, true
		bench.cursor = anchorOutlineCursor(m.outlineRows(), anchorID, anchorChapter)
		if !bench.pinned && !bench.liveHeld {
			for i, row := range m.outlineRows() {
				if row.isChapter() && (row.node.State == workbench.ChapterInProgress || row.node.State == workbench.ChapterPending) {
					bench.cursor = i
					break
				}
			}
		}
		// 恢复落点（页面设计 §4）：快照自带待决定事项，直接重建决定卡。
		if !bench.writing && bench.decision == nil && message.snap.Decision != nil {
			bench.presentDecision(&decisionState{
				reason: message.snap.Decision.Reason, proposal: message.snap.Decision.Proposal,
				hasProposal: message.snap.Decision.HasProposal, continueAfter: true,
				stale: message.snap.Decision.Stale,
			})
		}
		return m, nil
	case quickDoneMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		return m.handleQuickDone(message)
	case decisionDoneMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if message.err != nil {
			// 裁决失败不能弄丢决定卡：立即重拉快照从权威恢复（含基线漂移后的
			// Stale 标记），用户看着错误就能直接重试。
			bench.err = message.err.Error()
			return m, m.refreshBenchCmd()
		}
		if message.continueAfter {
			return m.continueRun()
		}
		bench.notice = "决定已生效"
		return m, m.refreshBenchCmd()
	case runControlMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if message.err != nil {
			bench.err = message.err.Error()
			return m, nil
		}
		bench.presentDecision(nil)
		if message.note != "" {
			bench.notice = message.note
		}
		if message.next == "continue" {
			return m.continueRun()
		}
		return m, m.refreshBenchCmd()
	case diagnosticsMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if message.err != nil {
			bench.err = message.err.Error()
			return m, nil
		}
		return m.openBody(message.text), nil
	case tea.MouseMsg:
		return m.handleBenchMouse(message)
	case tea.KeyMsg:
		return m.handleBenchKey(message)
	}
	return m, nil
}

func (m model) handleQuickDone(message quickDoneMsg) (tea.Model, tea.Cmd) {
	bench := &m.bench
	bench.writing = false
	bench.pinned = false
	bench.feedOffset = 0
	bench.presentDecision(nil)
	bench.err = ""
	if message.err != nil {
		bench.err = message.err.Error()
	}
	result := message.result
	switch result.RunState {
	case domainmodel.RunCompleted:
		bench.notice = fmt.Sprintf("全书完成：%d 章都写好了，在大纲里选章阅读吧", len(result.Chapters))
	case domainmodel.RunWaitingUser:
		// 待批稿件由下一次快照刷新带回并重建决定卡；无稿件等待先显示原因。
		if result.WaitingOperationID == "" {
			bench.presentDecision(&decisionState{reason: result.RunReason})
		}
	case domainmodel.RunFailed:
		// 失败理由已是创作语言（D26），比原始错误链更有用。
		bench.err = result.RunReason
	}
	return m, m.refreshBenchCmd()
}

func (m model) handleBenchKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	bench := &m.bench
	// 单值输入态（目标/预算/要求）优先。
	if bench.prompt != nil {
		switch key.Type {
		case tea.KeyEsc:
			bench.prompt = nil
			return m, nil
		case tea.KeyEnter:
			if bench.prompt.purpose == "export" {
				return m.submitExport()
			}
			if bench.prompt.purpose == "search" {
				query := strings.TrimSpace(bench.prompt.input.Value())
				if m.findChapter(query, false) {
					bench.prompt = nil
					bench.err = ""
				} else {
					bench.err = "没有找到匹配的章节"
				}
				return m, nil
			}
			if bench.prompt.purpose == "directive" {
				text := strings.TrimSpace(bench.prompt.input.Value())
				if text == "" {
					bench.notice = "写下你的创作要求后回车，Esc 取消"
					return m, nil
				}
				scope := bench.prompt.scope
				bench.prompt, bench.err = nil, ""
				return m, m.addDirectiveCmd(scope, text)
			}
			if bench.prompt.purpose == "adjudicate" {
				reason := strings.TrimSpace(bench.prompt.input.Value())
				if reason == "" {
					bench.notice = "写下接受这条发现的理由后回车，Esc 取消"
					return m, nil
				}
				finding := bench.prompt.finding
				bench.prompt, bench.err = nil, ""
				return m, m.adjudicateCmd(finding, reason)
			}
			value, err := strconv.Atoi(strings.TrimSpace(bench.prompt.input.Value()))
			if err != nil || value <= 0 {
				bench.err = "请输入一个正整数"
				return m, nil
			}
			purpose := bench.prompt.purpose
			bench.prompt = nil
			bench.err = ""
			if purpose == "budget" {
				return m, m.applyBudgetCmd(value)
			}
			return m.continueRunWith(value)
		}
		var cmd tea.Cmd
		bench.prompt.input, cmd = bench.prompt.input.Update(key)
		return m, cmd
	}
	// 修改意见输入态（按 n 进入）：文字回车=按意见重写；空回车回以指引。
	if bench.input.Focused() && bench.decision != nil && bench.decision.hasProposal && !bench.writing {
		switch key.Type {
		case tea.KeyEsc:
			bench.input.Blur()
			return m, nil
		case tea.KeyEnter:
			if reason := strings.TrimSpace(bench.input.Value()); reason != "" {
				return m.decideCmd(false, reason)
			}
			bench.notice = "按 y 确认通过，或写下修改意见后回车"
			return m, nil
		}
		var cmd tea.Cmd
		bench.input, cmd = bench.input.Update(key)
		return m, cmd
	}
	if bench.reading {
		switch key.Type {
		case tea.KeyEsc:
			bench.reading = false
			return m, nil
		}
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(key)
		return m, cmd
	}
	switch key.Type {
	case tea.KeyEsc:
		// 分层返回（§5）：创作中固定了主区时先回到活动流，再回欢迎页。
		if bench.pinned || bench.liveHeld {
			bench.pinned, bench.liveHeld = false, false
			return m, nil
		}
		bench.close()
		m.page = pageHome
		m.home = newHomeState()
		m.home.lastOpened = bench.projectID
		return m, m.loadLibraryCmd()
	case tea.KeyTab:
		if m.multiPane() {
			bench.pane = benchPane((int(bench.pane) + 1) % 2)
		} else {
			bench.view = (bench.view + 1) % 2
		}
		return m, nil
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
		return m.navigateOutline(key.Type), nil
	case tea.KeyUp, tea.KeyDown:
		delta := 1
		if key.Type == tea.KeyUp {
			delta = -1
		}
		// 目标栏 = 焦点栏；单栏形态总览视图作用于主区（活动流），正文视图作用于选章。
		target := benchPaneOutline
		if m.multiPane() {
			target = bench.pane
		} else if bench.view == 0 {
			target = benchPaneMain
		}
		return m.scrollBenchPane(target, delta), nil
	case tea.KeyEnter:
		// 批准必须由 y 明确确认（页面设计 §2）：空回车回以指引，不提交。
		if bench.decision != nil && bench.decision.hasProposal && !bench.writing {
			bench.notice = "按 y 确认通过，或按 n 输入修改意见"
			return m, nil
		}
		if bench.pane == benchPaneMain && bench.view == 0 && bench.tab == benchTabProse {
			if text, live := m.liveProse(); live {
				return m.openBody("正文预览 · 最终以确认稿为准\n\n" + text), nil
			}
		}
		if m.multiPane() || bench.view == 1 {
			// 卷/弧头行：回车折叠/展开；章行：回车阅读（§5）。
			rows := m.outlineRows()
			if bench.cursor >= 0 && bench.cursor < len(rows) && rows[bench.cursor].header() && (bench.pane == benchPaneOutline || !m.multiPane() && bench.view == 1) {
				bench.pinned = true
				bench.notice = m.toggleFold(rows[bench.cursor].node.Node.ID)
				return m, nil
			}
			return m.openChapter()
		}
		return m, nil
	}
	switch key.String() {
	case "1", "2", "3":
		bench.tab = benchTab(key.String()[0] - '1')
		bench.view, bench.pane = 0, benchPaneMain
		return m, nil
	case "o":
		if !m.multiPane() {
			bench.view = 1 - bench.view
		}
		bench.pane = benchPaneOutline
		return m, nil
	case "t":
		visible := false
		frame := m.workbenchFrame()
		for _, hit := range frame.hits {
			if hit.key == "t" && hit.y < 2+frame.bodyHeight {
				visible = true
			}
		}
		if visible {
			bench.thoughtOpen = !bench.thoughtOpen
		} else {
			bench.thoughtOpen, bench.tab, bench.view, bench.pane = true, benchTabActivity, 0, benchPaneMain
		}
		return m, nil
	case "T":
		if feed, ok := m.activityFeed(); ok && feed.ThinkingNote != "" {
			return m.openBody("思考片段 · 模型提供的原文尾部\n\n" + feed.ThinkingNote), nil
		}
		return m, nil
	case "f":
		bench.pinned, bench.liveHeld = false, false
		bench.feedOffset, bench.previewOffset = 0, 0
		bench.view, bench.tab, bench.pane = 0, benchTabProse, benchPaneMain
		return m, nil
	case "/":
		return m.openPrompt("search", "跳章 / 搜索 · 输入章节号或标题", ""), nil
	case "N":
		m.findChapter(bench.search, true)
		return m, nil
	case "e":
		if bench.exporting {
			return m, nil
		}
		if len(bench.snap.Manuscript) == 0 {
			bench.err = "暂无已确认正文，先批准稿件后再导出"
			return m, nil
		}
		return m.openPrompt("export", "导出已确认正文 · 输入 .txt 或 .epub 文件路径", ""), nil
	case "?":
		return m.openBody(benchHelp), nil
	case "i":
		if bench.loaded {
			scope, label := m.directiveScope()
			m = m.openPrompt("directive", label, "")
			m.bench.prompt.scope = scope
			return m, nil
		}
		return m, nil
	}
	// p/x/d 在创作进行中同样可用：暂停/取消走乐观转换（驱动循环在当前步骤
	// 完成后收束），诊断只读；其余键位在 writing 态屏蔽以免并发驱动。
	switch key.String() {
	case "p":
		if bench.hasRun() && (bench.run().State == domainmodel.RunRunning || bench.run().State == domainmodel.RunWaitingUser) {
			return m, m.pauseRunCmd()
		}
		return m, nil
	case "x":
		if bench.hasRun() && bench.run().State != domainmodel.RunCompleted &&
			bench.run().State != domainmodel.RunFailed && bench.run().State != domainmodel.RunCancelled {
			return m, m.cancelRunCmd()
		}
		return m, nil
	case "d":
		if bench.hasRun() {
			return m, m.loadDiagnosticsCmd()
		}
		return m, nil
	}
	if bench.writing {
		return m, nil
	}
	switch key.String() {
	case "y":
		if bench.decision != nil && bench.decision.hasProposal {
			// 过期稿件不能直接通过（会撞版本冲突）：只留重写路径。
			if bench.decision.stale {
				bench.notice = "这份稿件完成后书又有了新变化，不能直接通过；按 n 写修改意见让它基于最新内容重写"
				return m, nil
			}
			return m.decideCmd(true, "")
		}
	case "n":
		if bench.decision != nil && bench.decision.hasProposal {
			bench.input.Focus()
			return m, nil
		}
	case "c":
		return m.continueRun()
	case "g":
		if bench.loaded {
			return m.openPrompt("target", "新的目标章数", strconv.Itoa(m.currentTarget())), nil
		}
	case "b":
		if bench.decision != nil && !bench.decision.hasProposal && bench.hasRun() {
			return m.openPrompt("budget", "新的自动修订预算", strconv.Itoa(bench.run().Strategy.AutoRepairBudget)), nil
		}
	case "a":
		if finding, ok := m.firstBlockingFinding(); ok {
			m = m.openPrompt("adjudicate", "接受发现「"+truncate(finding.Note, 40)+"」的理由", "")
			m.bench.prompt.finding = finding.ID
			return m, nil
		}
		bench.notice = "选中的章节没有待处理的阻塞发现"
	}
	return m, nil
}

// firstBlockingFinding 取选中章节第一条尚未被接受的阻塞发现（D43 的 a 键一次只裁一条）。
func (m model) firstBlockingFinding() (workbench.WorkbenchFinding, bool) {
	chapterID := m.selectedChapterID()
	for _, finding := range m.bench.snap.Findings {
		if chapterID != "" && finding.ChapterID == chapterID && finding.Severity == domainmodel.FindingBlocking {
			return finding, true
		}
	}
	return workbench.WorkbenchFinding{}, false
}

// directiveScope 按大纲选中行定要求的作用域（§4.9）：章行只管该章，卷/弧行
// 管整个子树，占位行或尚无大纲时从下一章起生效。
func (m model) directiveScope() (scope, label string) {
	rows := m.outlineRows()
	if cursor := m.bench.cursor; cursor >= 0 && cursor < len(rows) {
		row := rows[cursor]
		switch {
		case row.placeholder:
			return fmt.Sprintf("from_chapter:%d", row.chapter), fmt.Sprintf("对第 %d 章起的要求", row.chapter)
		case row.isChapter():
			return domainmodel.DirectiveScopePlanNode(row.node.Node.ID), fmt.Sprintf("对第 %d 章的要求", row.chapter)
		default:
			return domainmodel.DirectiveScopePlanNode(row.node.Node.ID), fmt.Sprintf("对「%s」的要求", row.node.Node.Title)
		}
	}
	next := len(m.bench.snap.Manuscript) + 1
	return fmt.Sprintf("from_chapter:%d", next), fmt.Sprintf("对第 %d 章起的要求", next)
}

// scrollBenchPane 按当前标签分派滚动，不因运行状态改变用户正在看的内容。
func (m model) scrollBenchPane(pane benchPane, delta int) tea.Model {
	bench := &m.bench
	switch pane {
	case benchPaneMain:
		switch bench.tab {
		case benchTabActivity:
			bench.feedOffset = min(max(0, bench.feedOffset-delta), max(0, len(bench.activity.Entries)-1))
		case benchTabDetail:
			bench.detailOffset = min(max(0, bench.detailOffset+delta), m.workbenchFrame().detailMaxOffset)
		default:
			return m.scrollProse(delta)
		}
	default:
		rows := m.outlineRows()
		next := nextSelectable(rows, bench.cursor, delta, false)
		if next != bench.cursor {
			bench.cursor = next
			bench.previewOffset, bench.detailOffset = 0, 0
			bench.liveHeld = false
			// 创作中手动选章：固定主区在选中内容上，不被运行态覆盖（§2）。
			bench.pinned = true
		}
	}
	return m
}

// toggleFold 折叠/展开一个卷/弧节点，并把偏好随手写进用户级 state.json
// （布局偏好记忆，M3）；collapsed 是 map 引用，值接收者上的修改对外生效。
// 返回保存失败的提示（""=成功）：折叠本身已生效，丢的只是重启后的偏好，
// 但丢失必须让用户知道而非静默。
func (m model) toggleFold(id string) string {
	bench := m.bench
	if bench.collapsed[id] {
		delete(bench.collapsed, id)
	} else {
		bench.collapsed[id] = true
	}
	state, err := appconfig.LoadState(m.deps.ConfigDir)
	if err != nil {
		// 读不出来就不回写：用空状态读改写会抹掉其他作品的偏好。
		return "折叠已生效，但偏好文件异常未保存：" + err.Error()
	}
	if state.Collapsed == nil {
		state.Collapsed = make(map[string][]string)
	}
	ids := make([]string, 0, len(bench.collapsed))
	for nodeID := range bench.collapsed {
		ids = append(ids, nodeID)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		delete(state.Collapsed, bench.projectID)
	} else {
		state.Collapsed[bench.projectID] = ids
	}
	if err := appconfig.SaveState(m.deps.ConfigDir, state); err != nil {
		return "折叠已生效，但布局偏好保存失败：" + err.Error()
	}
	return ""
}

// handleBenchMouse 鼠标热区（M3）：滚轮=指针所在栏的 ↑/↓ 语义；点击选章/
// 折叠/切栏焦点；单栏点击标签行切视图；阅读态转发给正文视口。
func (m model) handleBenchMouse(mouse tea.MouseMsg) (tea.Model, tea.Cmd) {
	bench := &m.bench
	if bench.reading {
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(mouse)
		return m, cmd
	}
	// 输入态（修改意见/目标章数）不响应鼠标，避免焦点被点飞。
	if bench.prompt != nil || bench.input.Focused() {
		return m, nil
	}
	if mouse.Button == tea.MouseButtonWheelUp || mouse.Button == tea.MouseButtonWheelDown {
		if mouse.Y < 2 || mouse.Y >= 2+m.workbenchFrame().bodyHeight {
			return m, nil
		}
	}
	switch {
	case mouse.Button == tea.MouseButtonWheelUp:
		return m.wheelBench(mouse.X, -1), nil
	case mouse.Button == tea.MouseButtonWheelDown:
		return m.wheelBench(mouse.X, 1), nil
	case mouse.Action == tea.MouseActionPress && mouse.Button == tea.MouseButtonLeft:
		return m.clickBench(mouse.X, mouse.Y)
	}
	return m, nil
}

func (m model) wheelBench(x, delta int) tea.Model {
	if !m.multiPane() && m.bench.view == 1 {
		return m.scrollBenchPane(benchPaneOutline, delta)
	}
	return m.scrollBenchPane(m.benchPaneAt(x), delta)
}

// multiPane 报告当前终端宽度是否启用多栏布局（页面设计 §2 宽度降级）。
func (m model) multiPane() bool { return m.width >= 90 }

// chapterCount 是大纲可选章节数。
func chapterCount(snap workbench.WorkbenchSnapshot) int {
	count := 0
	for _, entry := range snap.Outline {
		if entry.Node.Kind == domainmodel.PlanChapter {
			count++
		}
	}
	return count
}

func (m model) openPrompt(purpose, label, initial string) model {
	input := newInput(initial)
	input.Prompt = ""
	input.Focus()
	m.bench.prompt = &promptState{purpose: purpose, label: label, input: input}
	return m
}

// addDirectiveCmd 把要求原话按作用域入账；刷新后右栏可见，之后的任务按当前
// 快照装配到它，在途稿件因基线推进走后继链（S12）。
func (m model) addDirectiveCmd(scope, text string) tea.Cmd {
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	gen, projectID := m.bench.gen, m.bench.projectID
	return func() tea.Msg {
		now := time.Now().UTC()
		_, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: projectID, ChangeID: projectdoc.NewID("directive", now), UserID: user,
			Scope: scope, Text: text, Reason: "工作台提出创作要求", CreatedAt: now,
		})
		return runControlMsg{gen: gen, err: err, next: "refresh", note: "要求已记录：之后的创作照此执行，审阅逐条核验"}
	}
}

// adjudicateCmd 把"接受当前版本"入账为用户裁决（D43）：裁定生效形态随之改变，
// 续写时不再因这条发现重写。
func (m model) adjudicateCmd(finding, reason string) tea.Cmd {
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	gen, projectID := m.bench.gen, m.bench.projectID
	return func() tea.Msg {
		now := time.Now().UTC()
		_, err := api.Reviews.AddAdjudication(ctx, novel.AddAdjudicationCommand{
			ProjectID: projectID, ChangeID: projectdoc.NewID("adjudication", now), UserID: user,
			Finding: finding, Reason: reason, CreatedAt: now,
		})
		return runControlMsg{gen: gen, err: err, next: "refresh", note: "已接受这条发现：续写时不再为它重写；相关内容再变化时裁决自动失效"}
	}
}

// currentTarget 取当前目标章数：小说目标优先，其余回退到 Intent。
func (m model) currentTarget() int {
	if m.bench.hasRun() {
		if goal, err := domainmodel.DecodeNovelGoal(m.bench.run().Goal); err == nil {
			return goal.TargetChapters
		}
	}
	return m.bench.snap.Intent.TargetChapters
}

// decideCmd 批准或带理由拒绝当前待裁决稿件；创作运行的稿件裁决后自动续跑。
func (m model) decideCmd(approve bool, reason string) (tea.Model, tea.Cmd) {
	bench := &m.bench
	proposal := bench.decision.proposal
	continueAfter := bench.decision.continueAfter
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	gen := bench.gen
	bench.presentDecision(nil)
	return m, func() tea.Msg {
		var err error
		if approve {
			_, err = api.Decisions.Approve(ctx, proposal.ID, user, time.Now().UTC())
		} else {
			_, err = api.Decisions.Reject(ctx, proposal.ID, user, reason, time.Now().UTC())
		}
		return decisionDoneMsg{gen: gen, continueAfter: continueAfter, err: err}
	}
}

func (m model) pauseRunCmd() tea.Cmd {
	api, ctx := m.api, m.ctx
	gen, runID := m.bench.gen, m.bench.run().ID
	note := "已暂停，按 c 随时继续"
	if m.bench.writing {
		note = "暂停指令已发出：当前这一步完成后就会停下"
	}
	return func() tea.Msg {
		_, err := api.Runs.PauseCreationRun(ctx, runID, time.Now().UTC())
		return runControlMsg{gen: gen, err: err, next: "refresh", note: note}
	}
}

func (m model) cancelRunCmd() tea.Cmd {
	api, ctx := m.api, m.ctx
	gen, runID := m.bench.gen, m.bench.run().ID
	note := "本轮创作已取消；已写内容保留，按 c 会开启新一轮继续"
	if m.bench.writing {
		note = "取消指令已发出：当前这一步完成后停下；已写内容保留"
	}
	return func() tea.Msg {
		_, err := api.Runs.CancelCreationRun(ctx, runID, time.Now().UTC())
		return runControlMsg{gen: gen, err: err, next: "refresh", note: note}
	}
}

func (m model) applyBudgetCmd(budget int) tea.Cmd {
	api, ctx := m.api, m.ctx
	gen, run := m.bench.gen, m.bench.run()
	return func() tea.Msg {
		strategy := run.Strategy
		strategy.AutoRepairBudget = budget
		_, err := api.Runs.UpdateCreationRunStrategy(ctx, run.ID, strategy, time.Now().UTC())
		return runControlMsg{gen: gen, err: err, next: "continue"}
	}
}

// continueRun 是统一续跑入口：同一命令从落点继续（§6.3）；
// chapters>0 时同时把目标章数调整为该值（走 QuickWrite 的既有 goal 更新路径）。
func (m model) continueRun() (tea.Model, tea.Cmd) { return m.continueRunWith(0) }

func (m model) continueRunWith(chapters int) (tea.Model, tea.Cmd) {
	bench := &m.bench
	if !bench.loaded {
		return m, m.refreshBenchCmd()
	}
	if chapters <= 0 {
		chapters = m.currentTarget()
		if chapters <= 0 {
			chapters = max(1, len(bench.snap.Manuscript))
		}
	}
	bench.writing = true
	bench.presentDecision(nil)
	bench.notice = ""
	bench.err = ""
	// 新一轮驱动开始：上一轮的活动快照不再呈现，等新事件流入。
	bench.activity = activity.Snapshot{}
	bench.feedOffset = 0
	return m, tea.Batch(
		m.startQuickWriteCmd(quickParams{
			projectID: bench.projectID,
			premise:   bench.snap.Intent.Premise,
			chapters:  chapters,
		}),
		pollTick(bench.gen),
	)
}

// selectedChapter 取选中章的呈现版本。候选稿优先（三层校正链，页面设计 §3）：
// 重写场景下旧权威正文与新候选并存，等你裁决的是候选，不能被旧稿遮住。
func (m model) selectedChapter(number int) (domainmodel.ManuscriptChapter, string, bool) {
	for _, candidate := range m.bench.snap.Candidates {
		if candidate.Chapter.Number == number {
			return candidate.Chapter, "候选稿 · 等你验收，尚未写入正式书稿", true
		}
	}
	if chapter, ok := chapterByNumber(m.bench.snap.Manuscript, number); ok {
		return chapter, "", true
	}
	return domainmodel.ManuscriptChapter{}, "", false
}

// openChapter 全屏阅读选中章。
func (m model) openChapter() (tea.Model, tea.Cmd) {
	chapter, badge, ok := m.selectedChapter(m.selectedChapterNumber())
	if !ok {
		return m, nil
	}
	return m.openBody(renderChapterBody(chapter, badge)), nil
}

func (m model) selectedChapterID() string {
	if chapter, ok := chapterByNumber(m.bench.snap.Manuscript, m.selectedChapterNumber()); ok {
		return chapter.ID
	}
	return ""
}

func chapterByNumber(chapters []domainmodel.ManuscriptChapter, number int) (domainmodel.ManuscriptChapter, bool) {
	for _, chapter := range chapters {
		if chapter.Number == number {
			return chapter, true
		}
	}
	return domainmodel.ManuscriptChapter{}, false
}

func renderChapterBody(chapter domainmodel.ManuscriptChapter, badge string) string {
	var text strings.Builder
	text.WriteString(fmt.Sprintf("第%d章 %s\n", chapter.Number, chapter.Title))
	if badge != "" {
		text.WriteString(styleWarn.Render(badge) + "\n")
	}
	text.WriteString("\n")
	for _, block := range chapter.Blocks {
		text.WriteString(block.Text)
		text.WriteString("\n\n")
	}
	return text.String()
}

// openBody 用整屏视口展示长文本（章节正文或运行诊断），Esc 关闭。
func (m model) openBody(content string) tea.Model {
	body := viewport.New(max(1, m.width-4), max(1, m.height-1))
	body.SetContent(strings.Join(readingLines(content, body.Width), "\n"))
	m.bench.body = body
	m.bench.bodyText = content
	m.bench.reading = true
	return m
}

// outlineRow 是折叠过滤后的大纲可见行：卷/弧头行（回车折叠/展开）、
// 章行（回车阅读）或未规划占位行（不可选）。
type outlineRow struct {
	context     string
	node        workbench.OutlineNode
	endChapter  int
	chapter     int // 章行与占位行的章节号
	placeholder bool
	collapsed   bool // 头行且已折叠
	chapters    int  // 折叠头行统计：子树章节数
	pending     int  // 子树待确认（◐）章数
}

func (r outlineRow) isChapter() bool { return r.node.Node.Kind == domainmodel.PlanChapter }
func (r outlineRow) header() bool {
	return r.node.Node.Kind == domainmodel.PlanVolume || r.node.Node.Kind == domainmodel.PlanArc
}

// outlineDepth 卷→弧→章的层级深度；beat 是章内节拍，不入大纲。
func outlineDepth(kind domainmodel.PlanNodeKind) (int, bool) {
	switch kind {
	case domainmodel.PlanVolume:
		return 0, true
	case domainmodel.PlanArc:
		return 1, true
	case domainmodel.PlanChapter:
		return 2, true
	default:
		return 0, false
	}
}

type outlineNodeStats struct{ chapters, pending int }

// outlineStats 统计每个卷/弧子树的章节数与待确认数（折叠头行的摘要）。
func outlineStats(outline []workbench.OutlineNode) map[string]outlineNodeStats {
	stats := make(map[string]outlineNodeStats)
	var ancestors []string
	for _, entry := range outline {
		depth, ok := outlineDepth(entry.Node.Kind)
		if !ok {
			continue
		}
		if entry.Node.Kind == domainmodel.PlanChapter {
			for _, id := range ancestors {
				s := stats[id]
				s.chapters++
				if entry.State == workbench.ChapterPending {
					s.pending++
				}
				stats[id] = s
			}
			continue
		}
		ancestors = append(ancestors[:min(depth, len(ancestors))], entry.Node.ID)
	}
	return stats
}

// outlineRows 是大纲的可见行序列：先序快照按折叠状态过滤（折叠头行吞掉
// 整棵子树），末尾补未规划占位。行结构与宽度无关，导航/锚定/渲染共用。
func (m model) buildOutlineRows() []outlineRow {
	bench := m.bench
	stats := outlineStats(bench.snap.Outline)
	var rows []outlineRow
	skipDepth := -1
	ancestors := make(map[int]string)
	for _, entry := range bench.snap.Outline {
		depth, ok := outlineDepth(entry.Node.Kind)
		if !ok {
			continue
		}
		if skipDepth >= 0 {
			if depth > skipDepth {
				continue
			}
			skipDepth = -1
		}
		for d := range ancestors {
			if d >= depth {
				delete(ancestors, d)
			}
		}
		var context []string
		for d := 0; d < depth; d++ {
			if title := ancestors[d]; title != "" {
				context = append(context, title)
			}
		}
		row := outlineRow{node: entry, chapter: entry.Number, context: strings.Join(context, " / ")}
		if depth < 2 {
			ancestors[depth] = entry.Node.Title
		}
		if entry.Node.Kind != domainmodel.PlanChapter && bench.collapsed[entry.Node.ID] {
			row.collapsed = true
			row.chapters, row.pending = stats[entry.Node.ID].chapters, stats[entry.Node.ID].pending
			skipDepth = depth
		}
		rows = append(rows, row)
	}
	target := m.currentTarget()
	if number := chapterCount(bench.snap) + 1; number <= target {
		rows = append(rows, outlineRow{chapter: number, endChapter: target, placeholder: true})
	}
	return rows
}

// nextSelectable 从 from 沿 delta 找下一个可选行；单栏正文视图只在章行间
// 移动（该形态没有大纲头行可看）。没有更多可选行时原地不动。
func nextSelectable(rows []outlineRow, from, delta int, chapterOnly bool) int {
	for next := from + delta; next >= 0 && next < len(rows); next += delta {
		if rows[next].placeholder {
			continue
		}
		if chapterOnly && !rows[next].isChapter() {
			continue
		}
		return next
	}
	return from
}

// anchorOutlineCursor 在新的可见行里找回选中行：先按节点 ID，再按章节号，
// 都找不到（首次加载/结构变化）回落到第一个章行。
func anchorOutlineCursor(rows []outlineRow, nodeID string, chapter int) int {
	if nodeID != "" {
		for index, row := range rows {
			if row.node.Node.ID == nodeID {
				return index
			}
		}
	}
	if chapter > 0 {
		for index, row := range rows {
			if row.isChapter() && row.chapter == chapter {
				return index
			}
		}
	}
	for index, row := range rows {
		if row.isChapter() {
			return index
		}
	}
	for index, row := range rows {
		if !row.placeholder {
			return index
		}
	}
	return 0
}

// selectedRowIdentity 记录当前选中行的身份，供快照刷新后重新锚定。
func (m model) selectedRowIdentity() (string, int) {
	rows := m.outlineRows()
	if m.bench.cursor < 0 || m.bench.cursor >= len(rows) {
		return "", 0
	}
	return rows[m.bench.cursor].node.Node.ID, rows[m.bench.cursor].chapter
}

// selectedChapterNumber 取光标所在章行的章节号；头行/占位行返回 0（无选中章）。
func (m model) selectedChapterNumber() int {
	rows := m.outlineRows()
	if m.bench.cursor >= 0 && m.bench.cursor < len(rows) && rows[m.bench.cursor].isChapter() {
		return rows[m.bench.cursor].chapter
	}
	return 0
}

// outlineWindow 计算大纲视口窗口（视图与鼠标命中共用，防两处漂移）：
// 装不下时窗口随选中行居中，并预留上下截断指示行。
func outlineWindow(total, capacity, selected int) (start, end int, clipped bool) {
	if total <= capacity {
		return 0, total, false
	}
	capacity = max(1, capacity-2)
	selected = min(max(0, selected), total-1)
	start = min(max(0, selected-capacity/2), total-capacity)
	return start, start + capacity, true
}

// viewOutlinePane 渲染大纲树：卷/弧层级（回车折叠/展开）+ 章节徽标（§2 左栏）；
// 长书按视口窗口化，窗口随选中行移动，截断处给出方向指示。
func (m model) viewOutlinePane(width, height int) string {
	rows := m.outlineRows()
	var view strings.Builder
	title := "大纲 / 搜索"
	if m.bench.cursor >= 0 && m.bench.cursor < len(rows) && rows[m.bench.cursor].context != "" {
		title = "大纲 · " + truncate(rows[m.bench.cursor].context, width-11) + " /"
	}
	view.WriteString(paneTitle(title, m.multiPane() && m.bench.pane == benchPaneOutline) + "\n")

	start, end, clipped := outlineWindow(len(rows), max(1, height-1), m.bench.cursor)
	if clipped && start > 0 {
		view.WriteString(styleHint.Render(fmt.Sprintf("  ↑ 前面还有 %d 行", start)) + "\n")
	}
	for index := start; index < end; index++ {
		view.WriteString(m.outlineRowLine(rows[index], index == m.bench.cursor, width) + "\n")
	}
	if clipped && end < len(rows) {
		view.WriteString(styleHint.Render(fmt.Sprintf("  ↓ 后面还有 %d 行", len(rows)-end)) + "\n")
	}
	return view.String()
}

// outlineRowLine 渲染一个大纲行；头行带折叠指示（▾ 展开 / ▸ 折叠 + 摘要）。
func (m model) outlineRowLine(row outlineRow, selected bool, width int) string {
	switch {
	case row.placeholder:
		if row.endChapter > row.chapter {
			return styleSubtitle.Render(fmt.Sprintf("  ○ %d–%d 未规划", row.chapter, row.endChapter))
		}
		return styleSubtitle.Render(fmt.Sprintf("  ○ %d 未规划", row.chapter))
	case row.isChapter():
		return m.outlineChapterLine(row.node, selected, width)
	default:
		fold, style, indent := "▾ ", styleTitle, ""
		if row.collapsed {
			fold = "▸ "
		}
		if row.node.Node.Kind == domainmodel.PlanArc {
			style, indent = styleSubtitle, " "
		}
		line := style.Render(indent + fold + truncate(row.node.Node.Title, max(4, width-10)))
		if row.collapsed {
			line += styleHint.Render(fmt.Sprintf(" · %d 章", row.chapters))
			if row.pending > 0 {
				line += styleWarn.Render(" ◐")
			}
		}
		return marker(selected, line)
	}
}

// paneTitle 栏标题：当前焦点栏高亮（§5 Tab 栏间焦点）。
func paneTitle(text string, focused bool) string {
	if focused {
		return styleFocus.Render("▎" + text)
	}
	return styleHint.Render(text)
}

func (m model) outlineChapterLine(entry workbench.OutlineNode, selected bool, width int) string {
	badge := "○"
	style := styleHint
	switch entry.State {
	case workbench.ChapterConfirmed:
		badge, style = "●", styleNotice
	case workbench.ChapterPending:
		badge, style = "◐", styleWarn
	case workbench.ChapterInProgress:
		badge, style = "▸", styleFocus
	}
	line := fmt.Sprintf("%s %d %s", badge, entry.Number, truncate(entry.Node.Title, max(4, width-8)))
	if selected {
		return benchTheme.Selected.Render(fitLine("▎ "+line, width))
	}
	return "  " + style.Render(fitLine(line, max(1, width-2)))
}

// toolActivityLabels 是工具名到创作语言的唯一翻译表（页面设计 §3）。
var toolActivityLabels = map[string]string{
	"authority_read":          "查阅设定与前情",
	"workspace_list":          "清点工作区",
	"workspace_read":          "翻看工作稿",
	"workspace_put_chapter":   "落笔章节工作稿",
	"workspace_replace_block": "修改段落",
	"workspace_put_candidate": "整理结构候选",
	"workspace_put_review":    "记录审阅意见",
	"proposal_submit":         "提交候选稿",
	"verdict_submit":          "给出审阅结论",
}

func toolActivityLabel(tool string) string {
	if label, ok := toolActivityLabels[tool]; ok {
		return label
	}
	return "推进创作步骤"
}

// activityFeed 取当前这轮创作的活动快照；上一轮的活动不冒充当前进展
// （三层校正链的易失层），没有当前活动时 ok=false。
func (m model) activityFeed() (activity.Snapshot, bool) {
	feed := m.bench.activity
	if feed.Seq == 0 || feed.RunID == "" {
		return activity.Snapshot{}, false
	}
	if m.bench.hasRun() && feed.RunID != m.bench.run().ID {
		return activity.Snapshot{}, false
	}
	return feed, true
}

// viewActivity 渲染事件级活动流（页面设计 §3）：一行一事件；工具参数流只显示
// 已接收数据量，不冒充正文字数；出错与重试可见，让"模型在自纠"可见而不可怕。
func (m model) viewActivity(width, limit int) string {
	feed, ok := m.activityFeed()
	if !ok {
		return ""
	}
	var view strings.Builder
	// 跟随滚动（§2）：feedOffset=0 渲染尾部即自动跟随；用户上翻后窗口后移、
	// 暂停跟随，并提示底下还有多少新活动。
	offset := min(m.bench.feedOffset, max(0, len(feed.Entries)-1))
	end := len(feed.Entries) - offset
	start := max(0, end-limit)
	if start > 0 {
		view.WriteString(styleHint.Render(fmt.Sprintf("  ↑ 更早还有 %d 条", start)) + "\n")
	}
	for _, entry := range feed.Entries[start:end] {
		view.WriteString(m.activityLine(entry, width) + "\n")
	}
	if offset > 0 {
		view.WriteString(styleWarn.Render(fmt.Sprintf("⏸ 已暂停跟随 · 下方还有 %d 条新活动 · ↓ 回到最新", offset)) + "\n")
		return view.String()
	}
	if feed.Note != "" {
		view.WriteString(styleHint.Render(truncate("· "+feed.Note, max(10, width))) + "\n")
	}
	return view.String()
}

func (m model) activityLine(entry activity.Entry, width int) string {
	if entry.Kind == activity.Retry {
		return styleWarn.Render(fmt.Sprintf("↻ 连接不稳定，正在第 %d 次重试", entry.Attempt))
	}
	if entry.Kind == activity.ProseStall {
		// 直播中断显式告知（不吞错）：预览停更 ≠ 模型停写，成稿不受影响。
		return styleWarn.Render("⚠ 正文直播中断（仅预览受影响），最终以确认稿为准")
	}
	label := toolActivityLabel(entry.Tool)
	switch {
	case entry.Err != "":
		// 错误原因随行可见（M3）："模型在自纠"可见而不可怕；完整诊断按 d 下钻。
		line := styleWarn.Render("! " + label + "遇到问题，模型正在自纠")
		return line + "\n" + styleHint.Render(truncate("  └ "+oneLine(entry.Err), max(10, width)))
	case entry.Done:
		return styleNotice.Render("✓ ") + label
	default:
		line := spinnerFrames[m.bench.spin%len(spinnerFrames)] + " " + label + "……"
		if entry.Bytes > 0 {
			line += " 已接收 " + formatDataSize(entry.Bytes)
		}
		return styleFocus.Render(line)
	}
}

// oneLine 把多行文本压成一行（摘要行不允许撑破布局）。
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func formatDataSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d 字节", n)
	}
	return fmt.Sprintf("%.1fK 数据", float64(n)/1024)
}

// viewDetailPane 详情标签的只读投影：选中章的已确认事实、审阅发现、
// 创作意图与锁定摘要。
func (m model) viewDetailPane(width int) string {
	bench := m.bench
	title := "详情"
	if number := m.selectedChapterNumber(); number > 0 {
		title = fmt.Sprintf("第 %d 章详情", number)
	}
	var view strings.Builder
	view.WriteString(paneTitle(title, bench.tab == benchTabDetail) + "\n")

	chapterID := m.selectedChapterID()
	var facts []string
	for _, fact := range bench.snap.Canon {
		if chapterID == "" || fact.SourceChapterID != chapterID {
			continue
		}
		label := truncate(string(fact.Value), width-3)
		if slices.Contains(bench.snap.PendingCanon, fact.ID) {
			label = styleWarn.Render("待核验 ") + truncate(string(fact.Value), width-8)
		}
		facts = append(facts, label)
	}
	if len(facts) > 0 {
		view.WriteString("\n" + styleTitle.Render("已确认事实") + "\n")
		start := 0
		for index := start; index < len(facts); index++ {
			view.WriteString(styleHint.Render("· ") + facts[index] + "\n")
		}
		if len(facts) > 8 {
			view.WriteString(styleHint.Render(fmt.Sprintf("  共 %d 条 · ↑/↓ 翻看", len(facts))) + "\n")
		}
	}

	findings := 0
	for _, finding := range bench.snap.Findings {
		if finding.ChapterID != chapterID || chapterID == "" {
			continue
		}
		if findings == 0 {
			view.WriteString("\n" + styleTitle.Render("审阅发现") + "\n")
		}
		marker := "· "
		if finding.Severity == domainmodel.FindingBlocking {
			marker = "! "
		}
		view.WriteString(styleHint.Render(marker) + truncate(finding.Note, width-3) + "\n")
		findings++
	}

	intent := bench.snap.Intent
	view.WriteString("\n" + styleTitle.Render("创作意图") + "\n")
	if len(intent.Required) > 0 {
		view.WriteString(styleHint.Render("必须 ") + truncate(strings.Join(intent.Required, "、"), width-4) + "\n")
	}
	if len(intent.Forbidden) > 0 {
		view.WriteString(styleHint.Render("禁止 ") + truncate(strings.Join(intent.Forbidden, "、"), width-4) + "\n")
	}
	if intent.EndingDirection != "" {
		view.WriteString(styleHint.Render("结局 ") + truncate(intent.EndingDirection, width-4) + "\n")
	}
	if len(bench.snap.Ownership) > 0 {
		view.WriteString(styleHint.Render(fmt.Sprintf("锁定 %d 处", len(bench.snap.Ownership))) + "\n")
	}
	if len(bench.snap.Directives) > 0 {
		view.WriteString("\n" + styleTitle.Render("创作要求") + "\n")
		for _, directive := range bench.snap.Directives {
			view.WriteString(styleHint.Render("· ") + truncate(directive.Text, width-3) + "\n")
		}
	}
	return view.String()
}

// benchStateBadge 当前运行状态徽章（顶栏与底栏共用）。
func (m model) benchStateBadge() string {
	bench := m.bench
	switch {
	case bench.writing && bench.hasRun() && bench.run().State == domainmodel.RunPaused:
		return styleWarn.Render("已暂停推进 · 当前任务收尾中")
	case bench.writing && bench.hasRun() && bench.run().State == domainmodel.RunCancelled:
		return styleWarn.Render("已结束本轮 · 当前任务收尾中")
	case bench.writing:
		return styleFocus.Render("创作中 " + spinnerFrames[bench.spin%len(spinnerFrames)])
	case bench.decision != nil:
		return styleWarn.Render("等你决定")
	case bench.hasRun():
		return stateBadge(runStateLabel(bench.run().State))
	default:
		return styleHint.Render("空闲")
	}
}

func benchRule(width int) string {
	return benchTheme.Border.Render(strings.Repeat("─", max(10, width)))
}
