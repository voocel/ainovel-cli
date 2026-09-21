package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// benchPane 是可滚动区的焦点：主区（正文/活动）或目录（Tab 切换）。
type benchPane int

const (
	benchPaneMain benchPane = iota
	benchPaneOutline
)

// benchView 是主区视图：正文，或本轮创作过程。
type benchView int

const (
	viewProse benchView = iota
	viewActivity
)

type workbenchState struct {
	projectID string
	// gen 是本次打开的代际号：全部异步消息带着它出生，不匹配即丢弃。
	gen                  int
	refresh              *benchRefresh
	rowsCache            *outlineCache
	proseCache           *wrappedText
	sceneCache           *wrappedText // 现场条尾部所看的输出块，整块排版按文本/宽度缓存
	wordCache            *wordCache
	outputCache          *outputLayoutCache
	search               string
	directoryInitialized bool
	view                 benchView
	snap                 workbench.WorkbenchSnapshot
	loaded               bool
	cursor               int // 目录可见行索引（含卷/弧头行；快照刷新后按身份重新锚定）
	// collapsed 是折叠的卷/弧节点（键 PlanNode.ID）；快照每轮整体替换，折叠状态只能活在这里。
	collapsed map[string]bool
	pane      benchPane
	// pinned 表示创作中用户手动选章、正文固定在选中章上，不被创作进度抢走。
	pinned bool
	// proseOffset 是正文视图的行偏移；proseHold 表示直播中用户上滚、暂停尾部跟随。
	proseOffset int
	proseHold   bool
	// activityHeld 是活动视图上滚时持有的快照（nil = 跟随最新）；activityOffset 是其行偏移。
	activityHeld   *activity.Snapshot
	activityOffset int
	diag           *diagnosticsState
	diagEpoch      int
	modelPanel     *modelPanel // /model 面板打开时非 nil，独占方向键与回车
	reading        bool
	body           viewport.Model
	bodyText       string
	writing        bool
	exporting      bool
	spin           int // 创作中动画帧，随活动与轮询节拍推进
	decision       *decisionState
	// input 是常驻聚焦的底栏输入框：文字是要求或修改意见，`/` 是命令，`y` 回车通过。
	input         textinput.Model
	inputScope    string
	inputLabel    string
	inputDecision string
	notice        string
	err           string
	// activity 是实时活动快照：被唤醒后整读，不逐条回放；activityOff 只退订，绝不取消创作。
	activity    activity.Snapshot
	activityCh  <-chan struct{}
	activityOff func()
}

// close 退订活动并释放本次工作台的作品与排版缓存。
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

// benchSituation 是工作台的处境。顶栏徽章、现场条、主操作与总览引导都只按它分支，
// 不再各自重算 writing 与 run.State 的组合。
type benchSituation int

const (
	situationNoRun benchSituation = iota
	situationWriting
	situationPausing    // 用户已暂停，当前任务还在收尾
	situationCancelling // 本轮已取消，当前任务还在收尾
	situationDecidingProposal
	situationDeciding
	situationCompleted
	situationPaused
	situationFailed
	situationCancelled
	situationRunning
)

// situation 的优先级：进程内是否正在跑（直播语义绑进程态）> 是否等用户裁决 >
// 有没有运行过 > 持久化的运行状态。
func (b *workbenchState) situation() benchSituation {
	if b.writing {
		if b.hasRun() {
			switch b.run().State {
			case domainmodel.RunPaused:
				return situationPausing
			case domainmodel.RunCancelled:
				return situationCancelling
			}
		}
		return situationWriting
	}
	if b.decision != nil {
		if b.decision.hasProposal {
			return situationDecidingProposal
		}
		return situationDeciding
	}
	if !b.hasRun() {
		return situationNoRun
	}
	switch b.run().State {
	case domainmodel.RunCompleted:
		return situationCompleted
	case domainmodel.RunPaused:
		return situationPaused
	case domainmodel.RunFailed:
		return situationFailed
	case domainmodel.RunCancelled:
		return situationCancelled
	}
	return situationRunning
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

// runControlMsg 是运行控制（暂停/取消/调预算/记要求）的统一回执。
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
		refresh:   &benchRefresh{}, rowsCache: &outlineCache{}, proseCache: &wrappedText{}, sceneCache: &wrappedText{},
		wordCache: &wordCache{}, outputCache: &outputLayoutCache{},
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
			// 把模型增量合并为每秒最多 20 帧。
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
	return m.inflight.track(func() tea.Msg {
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
			WorkerID: "tui-" + user, CreatedAt: now,
		})
		return quickDoneMsg{gen: gen, result: result, err: err}
	})
}

type benchRefresh struct {
	busy, pending bool
	reader        *workbench.Reader
}

// refreshBenchCmd 拉取统一工作台快照：权威投影、目录状态、最近一轮落点、待决定事项与下一步引导。
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

// updateWorkbench 把查询在途期间的刷新请求合并成一次后续查询，不重叠。
func (m model) updateWorkbench(message tea.Msg) (tea.Model, tea.Cmd) {
	refreshed, ok := message.(benchRefreshedMsg)
	if !ok || refreshed.gen != m.bench.gen {
		return m.applyWorkbench(message)
	}
	gate := m.bench.refresh
	pending := gate.pending
	gate.busy, gate.pending = false, false
	updated, cmd := m.applyWorkbench(message)
	next := updated.(model)
	if pending && next.page == pageWorkbench {
		cmd = tea.Batch(cmd, next.refreshBenchCmd())
	}
	return next, cmd
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
			// 唯一可宽容的瞬态：建书起步、作品还没落库（未加载 + 创作中 + 确为"不存在"）。
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
		// 快照整体替换会改变目录行索引：先记下选中行身份，替换后重新锚定。
		anchorID, anchorChapter := m.selectedRowIdentity()
		bench.snap, bench.loaded = message.snap, true
		m.initializeDirectory()
		bench.cursor = anchorOutlineCursor(m.outlineRows(), anchorID, anchorChapter)
		if !bench.pinned {
			if number := m.currentChapter(); number > 0 {
				m.revealCurrent(number)
			}
		}
		// 恢复落点：快照自带待决定事项，直接重建决定卡。
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
			// 裁决失败不能弄丢决定卡：立即重拉快照从权威恢复（含基线漂移后的 Stale）。
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
	bench.presentDecision(nil)
	bench.err = ""
	switch {
	case errors.Is(message.err, domainmodel.ErrOperationHeld):
		// 正常退出会释放任务；还被占着说明上一个进程崩溃后租约未到期，或另一个窗口在写。
		bench.err = "这项任务还在上一个进程的租约里（最多 1 分钟），稍后再 /c；如果另一个窗口正在写这本书，请先关掉它"
	case message.err != nil:
		bench.err = message.err.Error()
	}
	result := message.result
	switch result.RunState {
	case domainmodel.RunCompleted:
		bench.notice = fmt.Sprintf("全书完成：%d 章都写好了，在目录里选章阅读吧", len(result.Chapters))
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

// addDirectiveCmd 把要求原话按作用域入账；之后的任务按当前快照装配到它。
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

// adjudicateCmd 把"接受当前版本"入账为用户裁决（D43）：续写时不再因这条发现重写。
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

// continueRun 是统一续跑入口：同一命令从落点继续，目标章数沿用当前目标。
func (m model) continueRun() (tea.Model, tea.Cmd) { return m.continueRunWith(0) }

// continueRunWith 续跑并把目标调整为 chapters 章；0 表示沿用当前目标（由 app/novel 解析）。
func (m model) continueRunWith(chapters int) (tea.Model, tea.Cmd) {
	bench := &m.bench
	if !bench.loaded {
		return m, m.refreshBenchCmd()
	}
	bench.writing = true
	bench.presentDecision(nil)
	bench.notice, bench.err = "", ""
	// 新一轮驱动开始：上一轮的活动快照不再呈现，等新事件流入。
	bench.activity = activity.Snapshot{}
	bench.activityHeld, bench.activityOffset = nil, 0
	return m, tea.Batch(
		m.startQuickWriteCmd(quickParams{
			projectID: bench.projectID,
			premise:   bench.snap.Intent.Premise,
			chapters:  chapters,
		}),
		pollTick(bench.gen),
	)
}

// openBody 用整屏视口展示长文本，Esc 关闭。
func (m model) openBody(content string) model {
	body := viewport.New(max(1, m.width-4), max(1, m.height-1))
	body.SetContent(strings.Join(readingLines(content, body.Width), "\n"))
	m.bench.body = body
	m.bench.bodyText = content
	m.bench.reading = true
	return m
}

type wrappedText struct {
	text  string
	width int
	lines []string
}

func (c *wrappedText) wrap(text string, width int) []string {
	if c == nil {
		return readingLines(text, width)
	}
	if c.lines == nil || c.text != text || c.width != width {
		c.text, c.width, c.lines = text, width, readingLines(text, width)
	}
	return c.lines
}

type outputWrappedBlock struct {
	version uint64
	width   int
	lines   []string
}

type outputLayoutCache struct{ blocks map[uint64]outputWrappedBlock }

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

// benchStateBadge 顶栏运行状态。
func (m model) benchStateBadge() string {
	bench := m.bench
	switch bench.situation() {
	case situationPausing:
		return styleWarn.Render("Ⅱ 已暂停推进 · 当前任务收尾中")
	case situationCancelling:
		return styleWarn.Render("已结束本轮 · 当前任务收尾中")
	case situationWriting:
		label := "◉ 正在创作"
		if number := m.currentChapter(); number > 0 {
			label += fmt.Sprintf(" · 第 %d 章", number)
		}
		return styleFocus.Render(label + " " + spinnerFrames[bench.spin%len(spinnerFrames)])
	case situationDecidingProposal, situationDeciding:
		return styleWarn.Render("◇ 等你决定")
	case situationCompleted:
		return styleNotice.Render("✓ " + runStateLabel(bench.run().State))
	case situationFailed:
		return styleErr.Render("! " + runStateLabel(bench.run().State))
	case situationNoRun:
		return styleHint.Render("尚未开始")
	default:
		return styleHint.Render(runStateLabel(bench.run().State))
	}
}
