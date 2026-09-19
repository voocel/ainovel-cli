package tui

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// benchPane 是可滚动区的焦点：大纲、正文、现场（Tab 轮换）。
type benchPane int

const (
	benchPaneOutline benchPane = iota
	benchPaneFeed
	benchPaneMain
)

type workbenchState struct {
	projectID string
	// gen 是本次打开的代际号：全部异步消息带着它出生，不匹配即丢弃。
	gen           int
	refresh       *benchRefresh
	rowsCache     *outlineCache
	proseCache    *wrappedText
	wordCache     *wordCache
	search        string
	content       benchContent
	splitPercent  int
	outputCache   *outputLayoutCache
	outputHeld    []activity.OutputBlock
	outputFrozen  bool
	outputOffset  int
	outputDropped uint64
	reviewOffset  int
	snap          workbench.WorkbenchSnapshot
	loaded        bool
	cursor        int // 大纲可见行索引（含卷/弧头行；快照刷新后按身份重新锚定）
	// collapsed 是折叠的卷/弧节点（键 PlanNode.ID）；快照每轮整体替换，
	// 折叠状态只能活在这里，跨刷新存活。
	collapsed map[string]bool
	pane      benchPane
	// pinned 表示创作中用户手动选章、主区固定在选中内容上（§2）；
	// Esc 分层返回：先回活动流，再回欢迎页。
	pinned bool
	// previewOffset 是正文视图的显示行滚动偏移，随选章归零。
	previewOffset int
	// feedOffset 是活动流历史偏移：0=跟随最新（§2 自动滚动跟随），>0=用户上翻
	// 暂停跟随；新活动到达时按增量补偿，窗口锚定不动，↓ 到底恢复跟随。
	feedOffset int
	diag       *diagnosticsState
	diagEpoch  int
	reading    bool
	body       viewport.Model
	bodyText   string
	writing    bool
	exporting  bool
	spin       int // 创作中动画帧，随轮询节拍推进
	decision   *decisionState
	// input 是常驻聚焦的底栏输入框（页面设计 §2）：文字是要求或修改意见，
	// `/` 是命令，`y` 回车批准；空回车不提交，回以确认指引。
	input         textinput.Model
	inputScope    string
	inputLabel    string
	inputDecision string
	notice        string
	err           string
	// activity 是实时活动快照（页面设计 §3/§4）：被唤醒后整读，不逐条回放；
	// activityOff 只退订，绝不取消创作。
	activity    activity.Snapshot
	activityCh  <-chan struct{}
	activityOff func()
}

// close 退订活动并释放本次工作台的作品与排版缓存；在途查询完成后自行释放旧 Reader。
func (b *workbenchState) close() {
	if b.diag != nil {
		b.diag.cancel()
	}
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

// presentDecision 呈现（或清除）决定卡；用户可能正在打字，不动输入框。
func (b *workbenchState) presentDecision(decision *decisionState) {
	b.decision = decision
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

type pollMsg struct{ gen int }

// activityMsg 是活动唤醒回执：open=false 表示订阅已取消，不再重挂监听。
type activityMsg struct {
	gen  int
	open bool
}

func newWorkbenchState(projectID string, gen int) workbenchState {
	return workbenchState{
		projectID: projectID, gen: gen, input: newBenchInput(),
		collapsed: make(map[string]bool),
		refresh:   &benchRefresh{}, rowsCache: &outlineCache{}, proseCache: &wrappedText{}, wordCache: &wordCache{},
		outputCache: &outputLayoutCache{}, splitPercent: 25,
	}
}

// newBenchInput 是常驻输入框：静态光标不挂闪烁定时器，焦点永不离开。
func newBenchInput() textinput.Model {
	input := newInput("")
	input.Prompt = "› "
	input.Cursor.SetMode(cursor.CursorStatic)
	input.Focus()
	return input
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
						bench.notice = "所读活动已超出实时保留范围，已定位到最早保留条目；输入 /diag 查看历史"
					}
				}
			}
			bench.activity = feed
		}
		// 动画随活动密度转：流式时快、等待时回落到轮询节拍。
		bench.spin++
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
		if !bench.pinned {
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
		return m.applyDiagnostics(message)
	case diagnosticsSharedMsg:
		return m.applyDiagnosticsShared(message)
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
	if m.bench.diag != nil {
		return m.handleDiagnosticsKey(key)
	}
	bench := &m.bench
	if bench.reading {
		if key.Type == tea.KeyEsc {
			bench.reading = false
			return m, nil
		}
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(key)
		return m, cmd
	}
	// 输入框常驻聚焦（页面设计 §2）：导航键归面板，其余一律进文本框，没有单键快捷操作。
	switch key.Type {
	case tea.KeyEsc:
		// 分层返回（§5）：先清空输入，再解除固定回到跟随，最后回欢迎页。
		if bench.input.Value() != "" {
			bench.input.SetValue("")
			bench.inputScope, bench.inputLabel, bench.inputDecision = "", "", ""
			return m, nil
		}
		if bench.content != contentOutput || bench.outputFrozen || bench.pinned {
			bench.pinned = false
			bench.content, bench.outputFrozen, bench.outputHeld = contentOutput, false, nil
			return m, nil
		}
		bench.close()
		m.page = pageHome
		m.home = newHomeState()
		m.home.lastOpened = bench.projectID
		return m, m.loadLibraryCmd()
	case tea.KeyEnter:
		return m.submitInput()
	case tea.KeyF1:
		m.switchContent(contentOutput)
		return m, nil
	case tea.KeyF2:
		m.switchContent(contentManuscript)
		return m, nil
	case tea.KeyF3:
		m.switchContent(contentReview)
		return m, nil
	case tea.KeyTab:
		bench.pane = benchPane((int(bench.pane) + 1) % 3)
		return m, nil
	case tea.KeyShiftTab:
		bench.pane = benchPane((int(bench.pane) + 2) % 3)
		return m, nil
	case tea.KeyUp, tea.KeyDown:
		delta := 1
		if key.Type == tea.KeyUp {
			delta = -1
		}
		return m.scrollBenchPane(bench.pane, delta), nil
	case tea.KeyPgUp, tea.KeyPgDown:
		return m.navigateOutline(key.Type), nil
	case tea.KeyHome, tea.KeyEnd:
		if bench.input.Value() == "" {
			return m.navigateOutline(key.Type), nil
		}
	}
	var cmd tea.Cmd
	wasEmpty := bench.input.Value() == ""
	bench.input, cmd = bench.input.Update(key)
	if wasEmpty && bench.input.Value() != "" {
		bench.inputScope, bench.inputLabel = m.directiveScope()
		bench.inputDecision = ""
		if bench.content == contentReview && bench.decision != nil && bench.decision.hasProposal && !bench.writing {
			bench.inputDecision = bench.decision.proposal.ID
		}
	}
	if bench.input.Value() == "" {
		bench.inputScope, bench.inputLabel, bench.inputDecision = "", "", ""
	}
	return m, cmd
}

// submitInput 是回车的唯一分派：空回车走面板动作，`/` 是命令，`y` 批准，
// 其余文字在等你决定时是修改意见、否则是带作用域的要求（§4.9、D26）。
// 校验失败保留输入并显示 err，其余情况清空输入。
func (m model) submitInput() (tea.Model, tea.Cmd) {
	bench := &m.bench
	text := strings.TrimSpace(bench.input.Value())
	bench.err, bench.notice = "", ""
	pending := bench.decision != nil && bench.decision.hasProposal && !bench.writing && m.composingReview()
	var (
		next tea.Model
		cmd  tea.Cmd
	)
	switch {
	case text == "":
		return m.enterPane()
	case strings.HasPrefix(text, "/"):
		next, cmd = m.runCommand(strings.TrimSpace(text[1:]))
	case text == "y":
		next, cmd = m.approve()
	case text == "n":
		bench.notice = "写下修改意见后回车"
		if !pending {
			bench.notice = "现在没有等你决定的稿件"
		}
		next = m
	case pending:
		if bench.inputScope != "" && bench.inputDecision != bench.decision.proposal.ID {
			bench.err = "新的稿件正在等待审阅；这段文字仍是创作要求，请用 /note 提交，或 Esc 清空后审稿"
			return m, nil
		}
		next, cmd = m.decideCmd(false, text)
	default:
		if bench.inputDecision != "" {
			bench.err = "这份稿件已不再等待审阅；修改意见已保留，请 Esc 清空后重新选择操作"
			return m, nil
		}
		scope, _ := m.composerScope()
		next, cmd = m, m.addDirectiveCmd(scope, text)
	}
	updated := next.(model)
	if updated.bench.err == "" {
		updated.bench.input.SetValue("")
		updated.bench.inputScope, updated.bench.inputLabel, updated.bench.inputDecision = "", "", ""
	}
	return updated, cmd
}

// enterPane 是空回车：批准必须由 y 明确确认（页面设计 §2），空回车只回以指引。
func (m model) enterPane() (tea.Model, tea.Cmd) {
	bench := &m.bench
	if bench.decision != nil && bench.decision.hasProposal && !bench.writing && len(m.outlineRows()) == 0 {
		bench.notice = "输入 y 通过，或写下修改意见后回车"
		return m, nil
	}
	if bench.pane == benchPaneMain {
		switch bench.content {
		case contentReview:
			return m.openReview(), nil
		case contentOutput:
			return m.openBody(strings.Join(m.outputLines(max(1, m.width-4)), "\n")), nil
		}
	}
	// 大纲焦点下卷/弧头行：回车折叠/展开；其余回车阅读选中章（§5）。
	rows := m.outlineRows()
	if bench.pane == benchPaneOutline && bench.cursor >= 0 && bench.cursor < len(rows) && rows[bench.cursor].header() {
		bench.pinned = true
		bench.notice = m.toggleFold(rows[bench.cursor].node.Node.ID)
		return m, nil
	}
	return m.openChapter()
}

func (m model) approve() (tea.Model, tea.Cmd) {
	bench := &m.bench
	switch {
	case bench.decision == nil || !bench.decision.hasProposal:
		bench.notice = "现在没有等你决定的稿件"
	case bench.writing:
		bench.notice = "创作进行中，稿件写完后再裁决"
	case !m.composingReview():
		bench.notice = "请先按 F3 或输入 /review 审阅稿件，再输入 y 通过"
	case bench.inputScope != "" && bench.inputDecision != bench.decision.proposal.ID:
		bench.err = "待审稿件已变化，请 Esc 清空后重新确认"
	case bench.decision.stale:
		// 过期稿件不能直接通过（会撞版本冲突）：只留重写路径。
		bench.notice = "这份稿件完成后书又有了新变化，不能直接通过；写下修改意见后回车，让它基于最新内容重写"
	default:
		return m.decideCmd(true, "")
	}
	return m, nil
}

// benchCommand 是底栏 `/` 命令：唯一前缀匹配，命令面板与帮助共用同一张表。
// idleOnly 的命令在创作进行中屏蔽，以免并发驱动；quiet 的不进 `/` 总览（一行放不下）。
type benchCommand struct {
	name, usage, label string
	idleOnly, quiet    bool
	run                func(m model, arg string) (tea.Model, tea.Cmd)
}

var benchCommands = []benchCommand{
	{name: "stream", label: "实时输出", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.switchContent(contentOutput)
		return m, nil
	}},
	{name: "body", label: "阅读正文", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.switchContent(contentManuscript)
		return m, nil
	}},
	{name: "split", usage: "<20–50>", label: "事件区高度百分比", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		percent, err := strconv.Atoi(arg)
		if err != nil || percent < 20 || percent > 50 {
			m.bench.err = "用法：/split 20–50（事件区占中栏高度的百分比）"
			return m, nil
		}
		m.bench.splitPercent = percent
		return m, nil
	}},
	{name: "note", usage: "<要求>", label: "提出创作要求（不裁决稿件）", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		if arg == "" {
			m.bench.err = "用法：/note 要求内容；作用范围见输入框上方"
			return m, nil
		}
		scope, _ := m.composerScope()
		return m, m.addDirectiveCmd(scope, arg)
	}},
	{name: "review", label: "阅读待确认稿件", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.switchContent(contentReview)
		return m, nil
	}},
	{name: "pause", label: "暂停推进", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if b := &m.bench; b.hasRun() && (b.run().State == domainmodel.RunRunning || b.run().State == domainmodel.RunWaitingUser) {
			return m, m.pauseRunCmd()
		}
		m.bench.notice = "现在没有在推进的创作"
		return m, nil
	}},
	{name: "continue", label: "继续创作", idleOnly: true, run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.continueRun()
	}},
	{name: "export", usage: "<路径>", label: "导出", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		return m.submitExport(arg)
	}},
	{name: "follow", label: "回到最新", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		b := &m.bench
		b.pinned = false
		b.feedOffset, b.previewOffset = 0, 0
		b.content, b.outputFrozen, b.outputHeld = contentOutput, false, nil
		b.pane = benchPaneMain
		return m, nil
	}},
	{name: "think", label: "思考原文", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if text := m.thinkingContent(); text != "" {
			return m.openBody(text), nil
		}
		m.bench.notice = "模型未提供思考文本"
		return m, nil
	}},
	{name: "view", label: "完整详情", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openBody(m.detailReport(max(1, m.width-4))), nil
	}},
	{name: "goal", usage: "<章数>", label: "目标章数", idleOnly: true, run: func(m model, arg string) (tea.Model, tea.Cmd) {
		chapters, err := strconv.Atoi(arg)
		if err != nil || chapters <= 0 {
			m.bench.err = fmt.Sprintf("用法：/goal 章数（当前目标 %d 章）", m.currentTarget())
			return m, nil
		}
		return m.continueRunWith(chapters)
	}},
	{name: "budget", usage: "<次数>", label: "修订预算", idleOnly: true, run: func(m model, arg string) (tea.Model, tea.Cmd) {
		b := &m.bench
		if b.decision == nil || b.decision.hasProposal || !b.hasRun() {
			b.notice = "只有等待修订预算时才能调整"
			return m, nil
		}
		budget, err := strconv.Atoi(arg)
		if err != nil || budget <= 0 {
			b.err = fmt.Sprintf("用法：/budget 次数（当前 %d 次）", b.run().Strategy.AutoRepairBudget)
			return m, nil
		}
		return m, m.applyBudgetCmd(budget)
	}},
	{name: "accept", usage: "<理由>", label: "接受发现", idleOnly: true, run: func(m model, arg string) (tea.Model, tea.Cmd) {
		finding, ok := m.firstBlockingFinding()
		if !ok {
			m.bench.notice = "选中的章节没有待处理的阻塞发现"
			return m, nil
		}
		if arg == "" {
			m.bench.err = "用法：/accept 理由 · 接受发现「" + truncate(finding.Note, 40) + "」"
			return m, nil
		}
		return m, m.adjudicateCmd(finding.ID, arg)
	}},
	{name: "stop", label: "结束本轮", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if b := &m.bench; b.hasRun() && b.run().State != domainmodel.RunCompleted &&
			b.run().State != domainmodel.RunFailed && b.run().State != domainmodel.RunCancelled {
			return m, m.cancelRunCmd()
		}
		m.bench.notice = "没有可以结束的创作"
		return m, nil
	}},
	{name: "diag", label: "诊断", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openDiagnostics()
	}},
	{name: "next", label: "下一个匹配", quiet: true, run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if m.bench.search == "" || !m.findChapter(m.bench.search, true) {
			m.bench.notice = "先用 /章号 或 /标题 搜索"
		}
		return m, nil
	}},
	{name: "help", label: "帮助", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openBody(benchHelp), nil
	}},
}

// matchCommand 按完整名称或唯一前缀取命令；有歧义时需继续输入。
func matchCommand(name string) (benchCommand, bool) {
	var found *benchCommand
	for i := range benchCommands {
		command := &benchCommands[i]
		if command.name == name {
			return *command, true
		}
		if strings.HasPrefix(command.name, name) {
			if found != nil {
				return benchCommand{}, false
			}
			found = command
		}
	}
	if found == nil {
		return benchCommand{}, false
	}
	return *found, true
}

// runCommand 分派 `/` 后的文本：命令按表执行，其余按章节号或标题跳转（原 / 键）。
func (m model) runCommand(text string) (tea.Model, tea.Cmd) {
	name, arg, _ := strings.Cut(text, " ")
	if name == "" || name == "?" {
		name = "help"
	}
	if command, ok := matchCommand(name); ok {
		if command.idleOnly && m.bench.writing {
			m.bench.notice = "创作进行中，先 /pause 或等它写完"
			return m, nil
		}
		return command.run(m, strings.TrimSpace(arg))
	}
	var matches []string
	for _, command := range benchCommands {
		if strings.HasPrefix(command.name, name) {
			matches = append(matches, "/"+command.name)
		}
	}
	if len(matches) > 1 {
		m.bench.err = "请继续输入以选择命令：" + strings.Join(matches, " 或 ")
		return m, nil
	}
	if m.findChapter(text, false) {
		if _, err := strconv.Atoi(text); err != nil {
			m.bench.notice = "/next 下一个匹配"
		}
		return m, nil
	}
	m.bench.err = "没有找到匹配的章节；/? 查看命令"
	return m, nil
}

// firstBlockingFinding 取选中章节第一条尚未被接受的阻塞发现（D43 的 /accept 一次只裁一条）。
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

// scrollBenchPane 按焦点区分派滚动，不因运行状态改变用户正在看的内容。
func (m model) scrollBenchPane(pane benchPane, delta int) tea.Model {
	bench := &m.bench
	switch pane {
	case benchPaneFeed:
		bench.feedOffset = min(max(0, bench.feedOffset-delta), max(0, len(bench.activity.Entries)-1))
	case benchPaneMain:
		return m.scrollContent(delta)
	default:
		rows := m.outlineRows()
		if next := nextSelectable(rows, bench.cursor, delta, false); next != bench.cursor {
			// 创作中手动选章：固定主区在选中内容上，不被运行态覆盖（§2）。
			m.selectOutline(next)
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

// handleBenchMouse 鼠标只做两件事：滚轮滚动指针所在栏、点击大纲行选章或折叠；
// 命中坐标全部来自固定布局。阅读态转发给正文视口。
func (m model) handleBenchMouse(mouse tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.bench.diag != nil {
		return m.handleDiagnosticsMouse(mouse)
	}
	bench := &m.bench
	if bench.reading {
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(mouse)
		return m, cmd
	}
	l := m.benchLayout()
	switch {
	case mouse.Button == tea.MouseButtonWheelUp || mouse.Button == tea.MouseButtonWheelDown:
		if !l.inBody(mouse.Y) || mouse.X >= l.inspectorX-1 {
			return m, nil
		}
		delta := 1
		if mouse.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		return m.scrollBenchPane(l.paneAt(mouse.X, mouse.Y), delta), nil
	case mouse.Action == tea.MouseActionPress && mouse.Button == tea.MouseButtonLeft:
		if mouse.X >= l.mainX && mouse.X < l.inspectorX-1 && l.inBody(mouse.Y) {
			bench.pane = l.paneAt(mouse.X, mouse.Y)
			if mouse.Y == l.contentY && mouse.X >= l.mainX+1 {
				index := (mouse.X - l.mainX - 1) / contentTabWidth
				if index < len(contentLabels) {
					m.switchContent(benchContent(index))
				}
			}
			return m, nil
		}
		if mouse.X >= l.inspectorX && l.inBody(mouse.Y) {
			start := l.bodyY + 11
			tasks := m.visibleTasks()
			if mouse.Y >= start && mouse.Y < start+2*len(tasks) {
				m.inspectTask(tasks[(mouse.Y-start)/2])
				return m, nil
			}
			if mouse.Y >= l.bodyY+4 && mouse.Y < l.bodyY+8 && bench.decision != nil {
				m.switchContent(contentReview)
				return m, nil
			}
		}
		if mouse.Y == l.footerY+3 && mouse.X < l.width-1 {
			if action, ok := m.benchActionAt(mouse.X); ok {
				if bench.input.Value() != "" {
					bench.notice = "输入框中有未提交内容，请先提交或清空"
					return m, nil
				}
				bench.input.SetValue(action.key)
				bench.inputScope, bench.inputLabel = m.directiveScope()
				return m, nil
			}
		}
		if index, ok := m.outlineRowAt(l, mouse.X, mouse.Y); ok {
			m.clickOutlineRow(index)
		}
	}
	return m, nil
}

// clickOutlineRow 与键盘选章同一入口：章行选中并固定主区，头行折叠/展开，占位行无动作。
func (m *model) clickOutlineRow(index int) {
	row := m.outlineRows()[index]
	switch {
	case row.placeholder:
	case row.header():
		m.bench.cursor, m.bench.pane, m.bench.pinned = index, benchPaneOutline, true
		m.bench.notice = m.toggleFold(row.node.Node.ID)
	default:
		m.bench.pane = benchPaneOutline
		m.selectOutline(index)
	}
}

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
		return runControlMsg{gen: gen, err: err, next: "refresh", note: "要求已记录 · 对应范围的后续任务会采用并核验；现有正文尚未修改"}
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
	note := "已暂停，/continue 随时继续"
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
	note := "本轮创作已取消；已写内容保留，/continue 会开启新一轮继续"
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
// 装不下时窗口随选中行居中；截断指示行由布局恒定预留，不占用窗口容量。
func outlineWindow(total, capacity, selected int) (start, end int) {
	if total <= capacity {
		return 0, total
	}
	capacity = max(1, capacity)
	selected = min(max(0, selected), total-1)
	start = min(max(0, selected-capacity/2), total-capacity)
	return start, start + capacity
}

// viewOutlinePane 渲染大纲树：卷/弧层级（回车折叠/展开）+ 章节徽标（§2 左栏）。
// 行结构固定：标题、上截断指示（或空行）、outlineRows 行、下截断指示（或空行）。
func (m model) viewOutlinePane(l benchLayout) []string {
	rows := m.outlineRows()
	width := l.leftWidth - 1
	title := "大纲"
	if m.bench.cursor >= 0 && m.bench.cursor < len(rows) && rows[m.bench.cursor].context != "" {
		title = "大纲 · " + truncate(rows[m.bench.cursor].context, width-8)
	}
	lines := []string{sectionTitle(title, width, m.bench.pane == benchPaneOutline), ""}
	start, end := outlineWindow(len(rows), l.outlineRows, m.bench.cursor)
	if start > 0 {
		lines[1] = styleHint.Render(fmt.Sprintf("  ↑ 前面还有 %d 行", start))
	}
	for index := start; index < end; index++ {
		lines = append(lines, m.outlineRowLine(rows[index], index == m.bench.cursor, width))
	}
	for len(lines) < 2+l.outlineRows {
		lines = append(lines, "")
	}
	// 下截断指示行恒占位，本章摘要的起始行才是常量。
	bottom := ""
	if end < len(rows) {
		bottom = styleHint.Render(fmt.Sprintf("  ↓ 后面还有 %d 行", len(rows)-end))
	}
	return append(lines, bottom)
}

// outlineRowLine 渲染一个大纲行；头行带折叠指示（▾ 展开 / ▸ 折叠 + 摘要）。
func (m model) outlineRowLine(row outlineRow, selected bool, width int) string {
	switch {
	case row.placeholder:
		if row.endChapter > row.chapter {
			return styleSubtitle.Render(fmt.Sprintf("  ○ %02d–%02d 未规划", row.chapter, row.endChapter))
		}
		return styleSubtitle.Render(fmt.Sprintf("  ○ %02d 未规划", row.chapter))
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
		text := indent + fold + truncate(row.node.Node.Title, max(4, width-10))
		summary, pending := "", ""
		if row.collapsed {
			summary = fmt.Sprintf(" · %d 章", row.chapters)
			if row.pending > 0 {
				pending = " ◐"
			}
		}
		if selected {
			return benchTheme.Selected.Render(fitLine("▎ "+text+summary+pending, width))
		}
		return "  " + style.Render(text) + styleHint.Render(summary) + styleWarn.Render(pending)
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
	line := fmt.Sprintf("%s %02d  %s", badge, entry.Number, truncate(entry.Node.Title, max(4, width-9)))
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
	"semantic_compliance":     "核对语义合规",
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

// activityLines 渲染事件级活动流（页面设计 §3）：一行一事件，带时钟与耗时；
// 工具参数流只显示已接收数据量，不冒充正文字数；出错与重试可见，让"模型在自纠"
// 可见而不可怕。跟随滚动（§2）：feedOffset=0 渲染尾部；用户上翻后窗口后移、暂停
// 跟随，并提示底下还有多少新活动。指示行计入 limit，活动条高度恒定。
func (m model) activityLines(width, limit int) []string {
	feed, ok := m.activityFeed()
	if !ok {
		return nil
	}
	offset := min(m.bench.feedOffset, max(0, len(feed.Entries)-1))
	end := len(feed.Entries) - offset
	reserve := 0
	if offset > 0 {
		reserve++
	}
	if end > limit-reserve {
		reserve++
	}
	start := max(0, end-max(1, limit-reserve))
	var lines []string
	if start > 0 {
		lines = append(lines, styleHint.Render(fmt.Sprintf("↑ 更早还有 %d 条", start)))
	}
	for _, entry := range feed.Entries[start:end] {
		lines = append(lines, m.activityLine(entry, width))
	}
	if offset > 0 {
		lines = append(lines, styleWarn.Render(fmt.Sprintf("⏸ 已暂停跟随 · 下方还有 %d 条新活动 · ↓ 回到最新", offset)))
	}
	return lines
}

// activityLine：时钟 图标 标签 [· 已接收 N]，右端是耗时（完成态取起止差，进行中实时计）。
func (m model) activityLine(entry activity.Entry, width int) string {
	var clock string
	if !entry.At.IsZero() {
		clock = benchTheme.Muted.Render(entry.At.Local().Format("15:04:05")) + " "
	}
	label := toolActivityLabel(entry.Tool)
	var body, elapsed string
	switch {
	case entry.Kind == activity.Retry:
		body = styleWarn.Render(fmt.Sprintf("↻ 连接不稳定，正在第 %d 次重试", entry.Attempt))
	case entry.Kind == activity.ProseStall:
		// 直播中断显式告知（不吞错）：预览停更 ≠ 模型停写，成稿不受影响。
		body = styleWarn.Render("⚠ 正文直播中断（仅预览受影响），最终以确认稿为准")
	case entry.Err != "":
		// 错误原因随行可见："模型在自纠"可见而不可怕；完整诊断 /diag 下钻。
		body = styleWarn.Render("! " + label + "遇到问题，模型正在自纠 · " + oneLine(entry.Err))
		elapsed = entryElapsed(entry)
	case entry.Done:
		body = styleNotice.Render("✓ ") + label
		elapsed = entryElapsed(entry)
	default:
		line := spinnerFrames[m.bench.spin%len(spinnerFrames)] + " " + label + "……"
		if entry.Bytes > 0 {
			line += " 已接收 " + formatDataSize(entry.Bytes)
		}
		body = styleFocus.Render(line)
		elapsed = entryElapsed(entry)
	}
	return alignRight(clock+body, benchTheme.Muted.Render(elapsed), width)
}

func entryElapsed(entry activity.Entry) string {
	switch {
	case entry.At.IsZero():
		return ""
	case entry.Done && entry.DoneAt.IsZero():
		return ""
	case entry.Done:
		return formatDuration(entry.DoneAt.Sub(entry.At))
	default:
		return formatDuration(time.Since(entry.At))
	}
}

func formatDuration(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// alignRight 把 right 靠右放在同一行；左边放不下时截断左边，两者至少隔一格。
func alignRight(left, right string, width int) string {
	if right == "" {
		return fitLine(left, width)
	}
	room := width - lipgloss.Width(right) - 1
	left = fitLine(left, max(0, room))
	return left + strings.Repeat(" ", max(1, room-lipgloss.Width(left)+1)) + right
}

// tailLine 保留文本尾部并以 … 开头（思考片段只有尾部有意义）。
func tailLine(text string, width int) string {
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	cut := len(runes)
	for used := 1; cut > 0; cut-- {
		if w := lipgloss.Width(string(runes[cut-1])); used+w > width {
			break
		} else {
			used += w
		}
	}
	return "…" + string(runes[cut:])
}

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dK", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatCost(usd float64) string {
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// groupDigits 千分位分组："9800" → "9,800"。
func groupDigits(n int) string {
	digits := fmt.Sprintf("%d", n)
	var out []byte
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// wordCache 按快照版本缓存全书字数（几百章时不逐帧重算）。
type wordCache struct {
	revision domainmodel.Revision
	chapters int
	count    int
}

func (m model) wordCount() int {
	snap := m.bench.snap
	cache := m.bench.wordCache
	if cache != nil && cache.count > 0 && cache.revision == snap.Revision && cache.chapters == len(snap.Manuscript) {
		return cache.count
	}
	count := 0
	for _, chapter := range snap.Manuscript {
		for _, block := range chapter.Blocks {
			count += utf8.RuneCountInString(block.Text)
		}
	}
	if cache != nil {
		*cache = wordCache{revision: snap.Revision, chapters: len(snap.Manuscript), count: count}
	}
	return count
}

// benchPhaseLabel 概览卡"阶段"在快照没有环节文案时的兜底：按运行状态说话。
func (m model) benchPhaseLabel() string {
	bench := m.bench
	switch {
	case bench.writing:
		return "创作中"
	case bench.decision != nil:
		return "等你决定"
	case bench.hasRun():
		return runStateLabel(bench.run().State)
	default:
		return "未开始"
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

// detailReport 完整详情（v 全屏）：选中章的已确认事实、审阅发现、创作意图与锁定摘要。
func (m model) detailReport(width int) string {
	bench := m.bench
	title := "详情"
	if number := m.selectedChapterNumber(); number > 0 {
		title = fmt.Sprintf("第 %d 章详情", number)
	}
	var view strings.Builder
	view.WriteString(styleTitle.Render(title) + "\n")
	for _, entry := range bench.snap.Outline {
		if entry.Number == m.selectedChapterNumber() && entry.Node.Kind == domainmodel.PlanChapter && entry.Node.Summary != "" {
			view.WriteString("\n" + styleTitle.Render("本章规划") + "\n" + entry.Node.Summary + "\n")
			break
		}
	}

	chapterID := m.selectedChapterID()
	var facts []string
	for _, fact := range bench.snap.Canon {
		if chapterID == "" || fact.SourceChapterID != chapterID {
			continue
		}
		label := factLabel(fact.Value)
		if slices.Contains(bench.snap.PendingCanon, fact.ID) {
			label = styleWarn.Render("待核验 ") + factLabel(fact.Value)
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
		view.WriteString(styleHint.Render(marker) + finding.Note + "\n")
		findings++
	}

	intent := bench.snap.Intent
	view.WriteString("\n" + styleTitle.Render("创作意图") + "\n")
	if len(intent.Required) > 0 {
		view.WriteString(styleHint.Render("必须 ") + strings.Join(intent.Required, "、") + "\n")
	}
	if len(intent.Forbidden) > 0 {
		view.WriteString(styleHint.Render("禁止 ") + strings.Join(intent.Forbidden, "、") + "\n")
	}
	if intent.EndingDirection != "" {
		view.WriteString(styleHint.Render("结局 ") + intent.EndingDirection + "\n")
	}
	if len(bench.snap.Ownership) > 0 {
		view.WriteString(styleHint.Render(fmt.Sprintf("锁定 %d 处", len(bench.snap.Ownership))) + "\n")
	}
	if len(bench.snap.Directives) > 0 {
		view.WriteString("\n" + styleTitle.Render("创作要求") + "\n")
		for _, directive := range bench.snap.Directives {
			view.WriteString(styleHint.Render("· ") + directive.Text + "（" + m.directiveScopeLabel(directive.Scope) + "）" + "\n")
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
		label := runStateLabel(bench.run().State)
		switch bench.run().State {
		case domainmodel.RunCompleted:
			return styleNotice.Render(label)
		case domainmodel.RunFailed:
			return styleErr.Render(label)
		default:
			return styleHint.Render(label)
		}
	default:
		return styleHint.Render("空闲")
	}
}

func benchRule(width int) string {
	return benchTheme.Border.Render(strings.Repeat("─", max(0, width)))
}
