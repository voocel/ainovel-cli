package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/voocel/ainovel-cli/internal/app/chief"
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
	viewTalk
)

type workbenchState struct {
	projectID string
	// gen 是本次打开的代际号：全部异步消息带着它出生，不匹配即丢弃。
	gen                  int
	refresh              *benchRefresh
	rowsCache            *outlineCache
	proseCache           *wrappedText
	sceneCache           *outputLayoutCache // 现场条里输出块的紧凑排版，按块 ID/版本/宽度缓存
	talkCache            *talkCache         // 对话各条排好的行，条目与宽度不变就沿用
	outputCache          *outputLayoutCache
	search               string
	directoryInitialized bool
	view                 benchView
	snap                 workbench.WorkbenchSnapshot
	loaded               bool
	cursor               int // 目录可见行索引（含卷/弧头行；快照刷新后按身份重新锚定）
	// outlineTop 是滚轮翻到的目录窗口起点，outlineScrolled 为真时生效：翻目录只挪窗口，不换选中章。
	outlineScrolled bool
	outlineTop      int
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
	// session 是这本书的主编会话：对话与创作推进都在它那里，界面只读快照、调方法。
	// driveSeen 是已处理过落点的推进次数；changeSeen 是已跟上的主编改动回执数；thread 是
	// 对话的最新快照。
	session    *chief.Session
	driveSeen  int
	changeSeen int
	thread     chief.Thread
	// talkHeld 表示对话视图上滚、暂停跟随；talkOffset 是其行偏移。
	talkHeld   bool
	talkOffset int
	// resumeOnLoad 表示刚打开作品：首份快照里创作还标着进行中（上次没停就退出），就自动接着写。
	resumeOnLoad bool
	exporting    bool
	pulse        pulseState // 创作与主编回话时的动画节拍
	decision     *decisionState
	// input 是常驻聚焦的底栏输入框：文字交给主编，`/` 是命令，`y` 回车通过。
	input  textinput.Model
	notice string
	err    string
	// activity 是实时活动快照：被唤醒后整读，不逐条回放；activityOff 只退订，绝不取消创作。
	activity    activity.Snapshot
	activityCh  <-chan struct{}
	activityOff func()
	// echoes 是用户自己的操作在本轮时间线上的回响（"› 你通过了……"）：交互状态归入口，
	// 不经过活动通道；只属于 echoRun 这一轮，换轮即清。
	echoRun string
	echoes  []activity.Entry
}

// close 退订活动、合上主编会话（打断回合与推进，任务放回队列），释放本次工作台的作品与
// 排版缓存。
func (b *workbenchState) close() {
	if b.diag != nil {
		b.diag.cancel()
	}
	if b.activityOff != nil {
		b.activityOff()
	}
	if b.session != nil {
		b.session.Close()
	}
	*b = newWorkbenchState(b.projectID, b.gen, nil)
}

func (b *workbenchState) hasRun() bool { return b.snap.Run != nil }

// addEcho 把一次已生效的用户操作记到当前这轮的时间线上；还没有创作运行时无处可记。
func (b *workbenchState) addEcho(text string) {
	if !b.hasRun() {
		return
	}
	if id := b.run().ID; b.echoRun != id {
		b.echoRun, b.echoes = id, nil
	}
	b.echoes = append(b.echoes, activity.Entry{Kind: activity.Notice, Tone: toneUser, Text: text, Done: true, At: time.Now()})
}

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
	situationCancelling // 已停止创作，当前任务还在收尾
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

// resumable 报告创作停着、可以接着写：回车就继续。正在写、有稿件等你裁决、某章没做到你的
// 要求（照原样续跑还会停在那里）、完本（续写要跟主编说篇幅）都不算。
func (b *workbenchState) resumable() bool {
	switch b.situation() {
	case situationNoRun, situationPaused, situationFailed, situationCancelled, situationRunning:
		return true
	case situationDeciding:
		return b.decision.held == ""
	}
	return false
}

// decisionState 是决定卡：等待原因 + 可选的待裁决稿件及其故事语言描述。
// continueAfter 标记裁决后是否自动续跑（创作任务的稿件续跑，用户自己的草案不续）；
// stale 表示稿件基线已过期（直接通过会撞版本冲突），只留重写路径；held 是自动修订
// 用尽仍没做到你的要求、等你接受现状或跟主编说怎么改的那一章（章节 ID）。
type decisionState struct {
	reason        string
	proposal      domainmodel.Proposal
	view          workbench.ProposalView
	hasProposal   bool
	continueAfter bool
	stale         bool
	held          string
}

// presentDecision 呈现（或清除）决定卡；用户可能正在打字，不动输入框。
func (b *workbenchState) presentDecision(decision *decisionState) {
	b.decision = decision
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
	echo          string
}

// runControlMsg 是运行控制（暂停）的回执；echo 是成功后留在时间线上的那句"你做了什么"。
type runControlMsg struct {
	gen  int
	err  error
	next string // "continue" | "refresh"
	note string
	echo string
}

type pollMsg struct{ gen int }

// activityMsg 是活动唤醒回执：open=false 表示订阅已取消，不再重挂监听。
type activityMsg struct {
	gen  int
	open bool
}

func newWorkbenchState(projectID string, gen int, session *chief.Session) workbenchState {
	return workbenchState{
		projectID: projectID, gen: gen, session: session, input: newBenchInput(),
		collapsed: make(map[string]bool),
		refresh:   &benchRefresh{}, rowsCache: &outlineCache{}, proseCache: &wrappedText{}, sceneCache: &outputLayoutCache{}, talkCache: &talkCache{},
		outputCache: &outputLayoutCache{},
	}
}

// newBenchInput 是常驻输入框：静态光标不挂闪烁定时器，焦点永不离开；灰底只跟着文字走。
func newBenchInput() textinput.Model {
	input := newInput("")
	input.Prompt = "› "
	input.SetStyles(inputStyles(benchTheme.Text.Background(benchColors.InputBackground),
		lipgloss.NewStyle().Foreground(benchColors.Faint).Background(benchColors.InputBackground), false))
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

// write 让会话开始推进（直到完成或需要等待），界面跟上直播；落点经会话唤醒送回。
func (m *model) write(params chief.WriteParams) tea.Cmd {
	m.bench.session.Write(params)
	m.startedWriting()
	return tea.Batch(pollTick(m.bench.gen), m.bench.pulse.start(m.bench.gen))
}

// startedWriting 新一轮推进开始：清掉决定卡与上一轮的活动快照，等新事件流入。
func (m *model) startedWriting() {
	bench := &m.bench
	bench.writing = true
	bench.presentDecision(nil)
	bench.notice, bench.err = "", ""
	bench.activity = activity.Snapshot{}
	bench.activityHeld, bench.activityOffset = nil, 0
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
			return m, tea.Batch(m.refreshBenchCmd(), pollTick(bench.gen))
		}
		return m, nil
	case pulseMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if !bench.writing && !bench.thread.Busy {
			bench.pulse.running = false
			return m, nil
		}
		bench.pulse.advance(time.Now())
		return m, pulseTick(bench.gen)
	case activityMsg:
		if message.gen != bench.gen || !message.open {
			return m, nil
		}
		if feed, ok := m.api.Workbench.RunActivity(bench.projectID); ok {
			bench.activity = feed
		}
		bench.pulse.heard = time.Now()
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
				m.home = newHomeState(m.width)
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
		// 恢复落点：快照自带待决定事项，直接重建决定卡。
		if !bench.writing && bench.decision == nil && message.snap.Decision != nil {
			bench.presentDecision(&decisionState{
				reason: message.snap.Decision.Reason, proposal: message.snap.Decision.Proposal,
				view: message.snap.Decision.View, hasProposal: message.snap.Decision.HasProposal,
				continueAfter: message.snap.Decision.Proposal.OperationID != "",
				stale:         message.snap.Decision.Stale, held: message.snap.Decision.Held,
			})
		}
		if !bench.pinned {
			if number := m.currentChapter(); number > 0 {
				m.revealCurrent(number)
			}
		}
		// 暂停过的不自动开：那是用户明确停下的，打开来看看、导出都不该开始花钱。
		if bench.resumeOnLoad {
			bench.resumeOnLoad = false
			if bench.situation() == situationRunning {
				return m.continueRun()
			}
		}
		return m, nil
	case sessionMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		return m.applySession()
	case chiefSentMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if message.err != nil {
			// 话没送出去：放回输入框，别让用户重打。
			bench.err = message.err.Error()
			if bench.input.Value() == "" {
				bench.input.SetValue(message.text)
				bench.input.CursorEnd()
			}
		}
		return m, nil
	case decisionDoneMsg:
		if message.gen != bench.gen {
			return m, nil
		}
		if message.err != nil {
			// 裁决失败不能弄丢决定卡：立即重拉快照从权威恢复（含基线漂移后的 Stale）。
			bench.err = message.err.Error()
			return m, m.refreshBenchCmd()
		}
		bench.addEcho(message.echo)
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
		bench.addEcho(message.echo)
		if message.note != "" {
			bench.notice = message.note
		}
		if message.next == "continue" {
			return m.continueRun()
		}
		bench.presentDecision(nil)
		return m, m.refreshBenchCmd()
	case diagnosticsMsg:
		return m.applyDiagnostics(message)
	case diagnosticsSharedMsg:
		return m.applyDiagnosticsShared(message)
	case tea.MouseMsg:
		return m.handleBenchMouse(message)
	case tea.PasteMsg:
		return m.handleBenchPaste(message)
	case tea.KeyPressMsg:
		return m.handleBenchKey(message)
	}
	return m, nil
}

// handleDriveEnd 处理一次推进的落点：完成、等待、失败，或被暂停打断。
func (m model) handleDriveEnd(message chief.DriveEnd) (tea.Model, tea.Cmd) {
	bench := &m.bench
	bench.writing = false
	bench.pinned = false
	bench.presentDecision(nil)
	bench.err = ""
	switch {
	case errors.Is(message.Err, context.Canceled):
		// 暂停打断了在途任务：引擎已把它放回队列，不是错误。
	case errors.Is(message.Err, domainmodel.ErrOperationHeld):
		// 正常退出会释放任务；还被占着说明上一个进程崩溃后租约未到期，或另一个窗口在写。
		bench.err = "这项任务还在上一个进程的租约里（最多 1 分钟），稍后回车继续；如果另一个窗口正在写这本书，请先关掉它"
	case message.Err != nil:
		bench.err = message.Err.Error()
	}
	result := message.Result
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

// acceptHeld 接受卡住那一章的现状，接着写。
func (m model) acceptHeld() (tea.Model, tea.Cmd) {
	findings, number := m.heldFindings(), m.heldNumber()
	m.bench.presentDecision(nil)
	return m, m.acceptCmd(findings, number, true)
}

// acceptCmd 接受一章的现状：它尚未被接受的阻塞意见一次入账为用户裁决（D43），之后不再为
// 它们重写，标记随之消失（这一章再变化时裁决自动失效）。continueAfter 表示之后接着写。
func (m model) acceptCmd(findings []workbench.WorkbenchFinding, number int, continueAfter bool) tea.Cmd {
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	gen, projectID := m.bench.gen, m.bench.projectID
	ids := make([]string, len(findings))
	for i, finding := range findings {
		ids[i] = finding.ID
	}
	return func() tea.Msg {
		now := time.Now().UTC()
		if _, err := api.Reviews.AddAdjudication(ctx, novel.AddAdjudicationCommand{
			ProjectID: projectID, ChangeID: projectdoc.NewID("adjudication", now), UserID: user,
			Findings: ids, Reason: "接受当前版本", CreatedAt: now,
		}); err != nil {
			return decisionDoneMsg{gen: gen, err: err}
		}
		return decisionDoneMsg{gen: gen, continueAfter: continueAfter, echo: fmt.Sprintf("你接受了第 %d 章的现状", number)}
	}
}

// approveCmd 通过当前待裁决稿件；创作运行的稿件通过后自动续跑。退稿跟主编说。
func (m model) approveCmd() (tea.Model, tea.Cmd) {
	bench := &m.bench
	proposal := bench.decision.proposal
	continueAfter := bench.decision.continueAfter
	echo := "你通过了" + m.reviewTarget()
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	gen := bench.gen
	bench.presentDecision(nil)
	return m, func() tea.Msg {
		_, err := api.Decisions.Approve(ctx, proposal.ID, user, time.Now().UTC())
		return decisionDoneMsg{gen: gen, continueAfter: continueAfter, err: err, echo: echo}
	}
}

// pauseRunCmd 暂停创作：会话先落盘暂停，再打断在途的模型调用、任务放回队列，继续时接着
// 已有的对话与草稿写，不等这一步写完。暂停后跟主编说句话，说完就接着写。
func (m model) pauseRunCmd() tea.Cmd {
	session, ctx, gen := m.bench.session, m.ctx, m.bench.gen
	return func() tea.Msg {
		err := session.Pause(ctx)
		return runControlMsg{gen: gen, err: err, next: "refresh", note: "已暂停 · 回车继续，或跟主编说说想法", echo: "你暂停了创作"}
	}
}

// continueRun 是统一续跑入口：同一推进从落点继续，篇幅沿用上一轮设定。
func (m model) continueRun() (tea.Model, tea.Cmd) {
	return m.continueRunWith(chief.WriteParams{Chapters: novel.KeepChapters})
}

// continueRunWith 按篇幅设定续跑：Chapters 正数固定章数，0 交给 AI，novel.KeepChapters 沿用。
func (m model) continueRunWith(params chief.WriteParams) (tea.Model, tea.Cmd) {
	if !m.bench.loaded {
		return m, m.refreshBenchCmd()
	}
	return m, m.write(params)
}

// openBody 用整屏视口展示长文本，Esc 关闭。
func (m model) openBody(content string) model {
	body := viewport.New(viewport.WithWidth(max(1, m.width-4)), viewport.WithHeight(max(1, m.height-1)))
	body.SetContent(strings.Join(readingLines(content, body.Width()), "\n"))
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

// lines 返回输出块按宽度排好的行：块 ID、版本与宽度都没变就复用。
func (c *outputLayoutCache) lines(block activity.OutputBlock, width int, layout func() []string) []string {
	if entry, ok := c.blocks[block.ID]; ok && entry.version == block.Version && entry.width == width {
		return entry.lines
	}
	lines := layout()
	if c.blocks == nil {
		c.blocks = make(map[uint64]outputWrappedBlock)
	}
	c.blocks[block.ID] = outputWrappedBlock{version: block.Version, width: width, lines: lines}
	return lines
}

// prune 丢掉已被活动缓冲淘汰的块：块 ID 单调递增，比当前最早一块还旧的不会再出现在
// 实时快照里（冻结阅读持有的旧块命中不了就重排，缓存只是缓存）。
func (c *outputLayoutCache) prune(output []activity.OutputBlock) {
	if len(output) == 0 || len(c.blocks) <= 2*len(output) {
		return
	}
	for id := range c.blocks {
		if id < output[0].ID {
			delete(c.blocks, id)
		}
	}
}

// benchStateBadge 顶栏运行状态。
func (m model) benchStateBadge() string {
	bench := m.bench
	switch bench.situation() {
	case situationPausing:
		return benchTheme.Warning.Render("Ⅱ 正在暂停 · 等当前任务停下")
	case situationCancelling:
		return benchTheme.Warning.Render("已停止创作 · 当前任务收尾中")
	case situationWriting:
		// 取正在执行的任务：审阅、重写时不能停在最后一章。
		label := "◉ 正在创作"
		if phase := bench.snap.CurrentPhase; phase != "" {
			label = "◉ " + phase
		}
		return benchTheme.Focus.Render(label + " " + bench.pulse.spinner())
	case situationDecidingProposal, situationDeciding:
		return benchTheme.Warning.Render("◇ 等你决定")
	case situationCompleted:
		return benchTheme.Success.Render("✓ " + runStateLabel(bench.run().State))
	case situationFailed:
		return benchTheme.Error.Render("! " + runStateLabel(bench.run().State))
	case situationNoRun:
		return benchTheme.Muted.Render("尚未开始")
	default:
		return benchTheme.Muted.Render(runStateLabel(bench.run().State))
	}
}
