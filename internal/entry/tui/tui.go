// Package tui 是人类主入口：配置向导 → 首页 → 作品工作台。
// 它只调用 应用用例，与 Headless 对同一动作产生相同结果（workbench §3/§9）。
package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// Deps 由组合根注入：TUI 不装配服务，也不接触 Store。
type Deps struct {
	API        *bootstrap.App
	Configured bool
	ConfigDir  string
	UserID     string
	// InitialConfig 用于向导预填；ConfigError 是启动时的配置/装配失败原因，
	// 交互模式降级进向导修复而不是退出。
	InitialConfig appconfig.Config
	ConfigError   string
	// Rebuild 在配置向导完成后重建带执行器的服务。
	Rebuild func(appconfig.Config) (*bootstrap.App, error)
	// Verify 供可选的连接测试使用；保存不调用它。nil 仅用于测试。
	Verify func(context.Context, appconfig.Config) error
	Input  io.Reader
	Output io.Writer
}

func Run(ctx context.Context, deps Deps) error {
	if deps.API == nil {
		return fmt.Errorf("TUI application is required: %w", domainmodel.ErrInvalid)
	}
	// AltScreen 全屏渲染是“进入应用”的关键：不开则界面内联在滚动缓冲区里，
	// 看起来像普通输出。鼠标捕获与键盘操作共同由下面的选项启用。
	program := tea.NewProgram(
		newModel(ctx, deps),
		tea.WithContext(ctx), tea.WithInput(deps.Input), tea.WithOutput(deps.Output),
		tea.WithAltScreen(),
		// 鼠标热区（M3，用户拍板滚轮+点击）：代价是终端原生划选复制需按住
		// Shift；键盘始终覆盖全部操作。
		tea.WithMouseCellMotion(),
	)
	_, err := program.Run()
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
	config appconfig.Config
	page   page
	wizard wizardState
	home   homeState
	bench  workbenchState
	// gen 是工作台代际号：每次打开作品递增，异步消息携带发出时的代际，
	// 消费前不匹配即丢弃，保证结果不会串到另一部作品（Codex 复审 #1）。
	gen    int
	width  int
	height int
}

func newModel(ctx context.Context, deps Deps) model {
	m := model{ctx: ctx, deps: deps, api: deps.API, config: deps.InitialConfig, width: minWidth, height: minHeight}
	m.home = newHomeState()
	if !deps.Configured {
		m.page = pageWizard
		m.wizard = newWizardState(deps.InitialConfig, deps.ConfigError, false)
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
	m.bench = newWorkbenchState(projectID, m.gen)
	// 布局偏好记忆（M3）：恢复本作品的大纲折叠；落点更新不得抹掉其他偏好，
	// 偏好文件读不出来时不回写（否则等于用空状态覆盖全部偏好）。
	state, err := appconfig.LoadState(m.deps.ConfigDir)
	if err != nil {
		m.bench.notice = "界面偏好文件异常，本次不更新偏好：" + err.Error()
	} else {
		for _, id := range state.Collapsed[projectID] {
			m.bench.collapsed[id] = true
		}
		state.LastProjectID = projectID
		if err := appconfig.SaveState(m.deps.ConfigDir, state); err != nil {
			// 落点/偏好只影响下次启动的呈现，保存失败不阻塞进入，但要让用户知道。
			m.bench.notice = "界面偏好保存失败：" + err.Error()
		}
	}
	if wake, cancel, ok := m.api.Workbench.SubscribeRunActivity(projectID); ok {
		m.bench.activityCh, m.bench.activityOff = wake, cancel
	}
	m.page = pageWorkbench
	return m.watchActivityCmd()
}

func (m model) Init() tea.Cmd {
	switch m.page {
	case pageHome:
		return m.loadLibraryCmd()
	case pageWorkbench:
		return m.refreshBenchCmd()
	}
	return nil
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		if m.bench.diag != nil {
			m.layoutDiagnostics()
		}
		if m.bench.reading {
			m.bench.body.Width, m.bench.body.Height = max(1, m.width-4), max(1, m.height-1)
			m.bench.body.SetContent(strings.Join(readingLines(m.bench.bodyText, m.bench.body.Width), "\n"))
		}
		return m, nil
	case tea.MouseMsg:
		// 门槛以下没有可命中的布局，鼠标事件无处归属。
		if m.tooSmall() {
			return m, nil
		}
	case tea.KeyMsg:
		if message.Type == tea.KeyCtrlC {
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

// 终端门槛（页面设计 §2）：达标后所有页面按固定几何排版，不做窄屏降级；
// 不达标只提示最大化窗口，键盘仍可返回或退出。
const (
	minWidth  = 150
	minHeight = 40
)

func (m model) tooSmall() bool { return m.width < minWidth || m.height < minHeight }

func (m model) viewTooSmall() string {
	w, h := max(1, m.width), max(1, m.height)
	lines := []string{
		benchTheme.Title.Render("请将终端窗口最大化"),
		benchTheme.Muted.Render(fmt.Sprintf("当前 %d × %d · 需要至少 %d × %d", m.width, m.height, minWidth, minHeight)),
		"",
		benchTheme.Muted.Render("Esc 返回 · Ctrl+C 退出"),
	}
	block := make([]string, max(0, (h-len(lines))/2))
	for _, line := range lines {
		line = fitLine(line, w)
		block = append(block, strings.Repeat(" ", max(0, (w-lipgloss.Width(line))/2))+line)
	}
	return strings.Join(fitBlock(strings.Join(block, "\n"), w, h), "\n")
}

func (m model) View() string {
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

func approvalLabel(policy domainmodel.ApprovalPolicy) string {
	switch policy {
	case domainmodel.ApprovalMilestone:
		return "里程碑确认"
	case domainmodel.ApprovalManual:
		return "逐章确认"
	default:
		return "自动推进"
	}
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
