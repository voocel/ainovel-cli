// Package tui 是人类主入口：配置向导 → 首页 → 作品工作台。
// 它只调用 应用用例，与 Headless 对同一动作产生相同结果（workbench §3/§9）。
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/app/binding"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// Deps 由组合根注入：TUI 不装配服务，也不接触 Store。
type Deps struct {
	API       *bootstrap.App
	ConfigDir string
	UserID    string
	// ConfigError 是启动时的配置/绑定失败原因：交互模式降级进向导修复而不是退出。
	ConfigError string
	// ListModels 向服务商列出一个连接能用的模型（顺带验证密钥），模型设置据此给出可选模型。
	ListModels func(context.Context, string, appconfig.ProviderConfig) ([]binding.ModelOption, error)
	// TryModel 向连接上的模型发一句极短的对话：手动输入的模型 ID 用上前先确认它真能用。
	TryModel func(context.Context, string, appconfig.ProviderConfig, string) error
	Input    io.Reader
	Output   io.Writer
}

// quitWindow 是确认退出的时限：Ctrl+C 第一次只把底栏提示换成退出确认，时限内再按一次
// 才退出，误触不会打断在途创作。
const quitWindow = 2 * time.Second

// quitExpiredMsg 是退出确认到期：底栏恢复页面自己的按键提示。
type quitExpiredMsg struct{}

// shutdownGrace 是退出时等在途创作释放任务的上限：取消后执行器立即停手，释放只是
// 一次本地写入；超时就交给租约到期回收。
const shutdownGrace = 10 * time.Second

func Run(parent context.Context, deps Deps) error {
	// 界面退出即取消在途创作：任务放回队列，下次进入接着写，而不是留着租约让人等。
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	m := newModel(ctx, deps)
	program := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(deps.Input), tea.WithOutput(deps.Output))
	_, err := program.Run()
	cancel()
	deps.API.Chief.Wait(shutdownGrace)
	if errors.Is(err, tea.ErrProgramKilled) && parent.Err() != nil {
		err = nil // 终止信号触发的退出是正常收束
	}
	return err
}

type page int

const (
	pageWizard page = iota
	pageHome
	pageWorkbench
)

type model struct {
	ctx    context.Context
	deps   Deps
	api    *bootstrap.App
	page   page
	wizard wizardState
	home   homeState
	bench  workbenchState
	// gen 是工作台代际号：每次打开作品递增，异步消息携带发出时的代际，
	// 消费前不匹配即丢弃，保证结果不会串到另一部作品（Codex 复审 #1）。
	gen    int
	width  int
	height int
	// quitArmed 表示已按过一次 Ctrl+C、正等确认；同时只有一个到期计时在途。
	quitArmed bool
}

func newModel(ctx context.Context, deps Deps) model {
	m := model{ctx: ctx, deps: deps, api: deps.API, width: minWidth, height: minHeight}
	m.home = newHomeState(m.width)
	if !deps.API.Models.Current("").Bound || deps.ConfigError != "" {
		m.page = pageWizard
		m.wizard = newWizardState(deps.API.Models.Config(), deps.ConfigError, false)
		return m
	}
	// 启动流程：配置完成一律落欢迎页；上次打开的作品在作品库中预选，
	// 回车即回到它的工作台恢复落点（workbench §4）。
	m.page = pageHome
	state, err := appconfig.LoadState(deps.ConfigDir)
	if err != nil {
		m.home.notice = "界面偏好文件异常（按空状态启动）：" + err.Error()
	}
	m.home.lastOpened = state.LastProjectID
	return m
}

// openBench 进入指定作品的工作台并记住落点：退订旧作品的活动、订阅新作品，
// 返回活动监听命令（未装配活动通道时为 nil，tea.Batch 安全忽略）。
func (m *model) openBench(projectID string) tea.Cmd {
	if m.bench.activityOff != nil {
		m.bench.activityOff()
	}
	m.gen++
	m.bench = newWorkbenchState(projectID, m.gen, m.api.Chief.Open(m.ctx, projectID, m.deps.UserID))
	// 布局偏好记忆（M3）：恢复本作品的大纲折叠并记下落点。偏好只影响呈现，读写失败
	// 不阻塞进入，但要让用户知道。
	if err := appconfig.UpdateState(m.deps.ConfigDir, func(state *appconfig.State) {
		_, m.bench.directoryInitialized = state.Collapsed[projectID]
		for _, id := range state.Collapsed[projectID] {
			m.bench.collapsed[id] = true
		}
		state.LastProjectID = projectID
	}); err != nil {
		m.bench.notice = "界面偏好未能更新：" + err.Error()
	}
	if wake, cancel, ok := m.api.Workbench.SubscribeRunActivity(projectID); ok {
		m.bench.activityCh, m.bench.activityOff = wake, cancel
	}
	m.page = pageWorkbench
	return tea.Batch(m.watchActivityCmd(), m.watchSessionCmd(), m.loadConversationCmd())
}

func (m model) Init() tea.Cmd {
	switch m.page {
	case pageHome:
		return tea.Batch(tea.RequestBackgroundColor, m.loadLibraryCmd())
	case pageWorkbench:
		return tea.Batch(tea.RequestBackgroundColor, m.refreshBenchCmd())
	}
	return tea.RequestBackgroundColor
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.home.premise.SetWidth(premiseWidth(m.width))
		if m.bench.diag != nil {
			m.layoutDiagnostics()
		}
		if m.bench.reading {
			m.bench.body.SetWidth(max(1, m.width-4))
			m.bench.body.SetHeight(max(1, m.height-1))
			m.bench.body.SetContent(strings.Join(readingLines(m.bench.bodyText, m.bench.body.Width()), "\n"))
		}
		return m, nil
	case tea.BackgroundColorMsg:
		darkBackground = message.IsDark()
		return m, nil
	case tea.MouseMsg:
		// 门槛以下没有可命中的布局，鼠标事件无处归属。
		if m.tooSmall() {
			return m, nil
		}
	case quitExpiredMsg:
		m.quitArmed = false
		return m, nil
	case tea.KeyPressMsg:
		if message.String() == "ctrl+c" {
			if !m.quitArmed {
				m.quitArmed = true
				return m, tea.Tick(quitWindow, func(time.Time) tea.Msg { return quitExpiredMsg{} })
			}
			if m.page == pageWizard && m.wizard.cancel != nil {
				m.wizard.cancel()
			}
			return m, tea.Quit
		}
	}
	switch m.page {
	case pageWizard:
		return m.updateWizard(message)
	case pageHome:
		return m.updateHome(message)
	default:
		return m.updateWorkbench(message)
	}
}

// leftClick 是左键按下；滚轮、拖动与松开都不算点击。
func leftClick(msg tea.MouseMsg) bool {
	click, ok := msg.(tea.MouseClickMsg)
	return ok && click.Button == tea.MouseLeft
}

// 终端门槛（页面设计 §2）：达标后所有页面按固定几何排版，不做窄屏降级；
// 不达标只提示最大化窗口，键盘仍可返回或退出。
const (
	minWidth  = 120
	minHeight = 36
)

func (m model) tooSmall() bool { return m.width < minWidth || m.height < minHeight }

func (m model) viewTooSmall() string {
	w, h := max(1, m.width), max(1, m.height)
	lines := []string{
		benchTheme.Title.Render("请将终端窗口最大化"),
		benchTheme.Muted.Render(fmt.Sprintf("当前 %d × %d · 需要至少 %d × %d", m.width, m.height, minWidth, minHeight)),
		"",
		m.keyHint(benchTheme.Muted.Render("Esc 返回 · Ctrl+C 退出")),
	}
	block := make([]string, max(0, (h-len(lines))/2))
	for _, line := range lines {
		line = fitLine(line, w)
		block = append(block, strings.Repeat(" ", max(0, (w-lipgloss.Width(line))/2))+line)
	}
	return strings.Join(fitBlock(strings.Join(block, "\n"), w, h), "\n")
}

// keyHint 是页面底栏的按键提示；按过一次 Ctrl+C 后换成退出确认。
func (m model) keyHint(hint string) string {
	if m.quitArmed {
		return benchTheme.Warning.Render("再按一次 Ctrl+C 退出")
	}
	return hint
}

// View 全屏渲染是“进入应用”的关键：不开则界面内联在滚动缓冲区里，看起来像普通输出。
// 鼠标热区（M3，用户拍板滚轮+点击）的代价是终端原生划选复制需按住 Shift；键盘始终覆盖
// 全部操作。
func (m model) View() tea.View {
	screen, cursor := placeCaret(m.screen())
	view := tea.NewView(screen)
	view.Cursor = cursor
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m model) screen() string {
	if m.tooSmall() {
		return m.viewTooSmall()
	}
	switch m.page {
	case pageWizard:
		return m.viewWizard()
	case pageHome:
		return m.viewHome()
	default:
		return m.viewWorkbench()
	}
}

// truncate 按显示宽度截断一行文本，避免状态栏与列表溢出。
func truncate(text string, limit int) string {
	return ansi.Truncate(strings.ReplaceAll(text, "\n", " "), max(0, limit), "…")
}

func runStateLabel(state domainmodel.CreationRunState) string {
	switch state {
	case domainmodel.RunRunning:
		return "创作中"
	case domainmodel.RunWaitingUser:
		return "等你决定"
	case domainmodel.RunPaused:
		return "已暂停"
	case domainmodel.RunCompleted:
		return "已完成"
	case domainmodel.RunFailed:
		return "需要处理"
	case domainmodel.RunCancelled:
		return "已取消"
	default:
		return string(state)
	}
}
