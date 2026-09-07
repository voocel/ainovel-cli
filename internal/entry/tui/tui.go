// Package tui 是人类主入口：配置向导 → 首页 → 作品工作台。
// 它只调用 Service 用例，与 Headless 对同一动作产生相同结果（workbench §3/§9）。
package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/app"
	"github.com/voocel/ainovel-cli/internal/service"
)

// Deps 由组合根注入：TUI 不装配服务，也不接触 Store。
type Deps struct {
	API        *service.Service
	Configured bool
	ConfigDir  string
	UserID     string
	// InitialConfig 用于向导预填；ConfigError 是启动时的配置/装配失败原因，
	// 交互模式降级进向导修复而不是退出。
	InitialConfig app.Config
	ConfigError   string
	// Rebuild 在配置向导完成后重建带执行器的服务。
	Rebuild func(app.Config) (*service.Service, error)
	// Verify 在落盘前验证配置可真实连通；nil 表示跳过（测试）。
	Verify func(context.Context, app.Config) error
	Input  io.Reader
	Output io.Writer
}

func Run(ctx context.Context, deps Deps) error {
	if deps.API == nil {
		return fmt.Errorf("TUI service is required: %w", domain.ErrInvalid)
	}
	// AltScreen 全屏渲染是“进入应用”的关键：不开则界面内联在滚动缓冲区里，
	// 看起来像普通输出。不开启鼠标捕获，保留终端原生滚动与复制（workbench §4）。
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
	api    *service.Service
	config app.Config
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
	m := model{ctx: ctx, deps: deps, api: deps.API, config: deps.InitialConfig, width: 80, height: 24}
	m.home = newHomeState()
	if !deps.Configured {
		m.page = pageWizard
		m.wizard = newWizardState(deps.InitialConfig, deps.ConfigError, false)
		return m
	}
	// 启动流程：配置完成一律落欢迎页；上次打开的作品在作品库中预选，
	// 回车即回到它的工作台恢复落点（workbench §4）。
	m.page = pageHome
	state, err := app.LoadState(deps.ConfigDir)
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
	state, err := app.LoadState(m.deps.ConfigDir)
	if err != nil {
		m.bench.notice = "界面偏好文件异常，本次不更新偏好：" + err.Error()
	} else {
		for _, id := range state.Collapsed[projectID] {
			m.bench.collapsed[id] = true
		}
		state.LastProjectID = projectID
		if err := app.SaveState(m.deps.ConfigDir, state); err != nil {
			// 落点/偏好只影响下次启动的呈现，保存失败不阻塞进入，但要让用户知道。
			m.bench.notice = "界面偏好保存失败：" + err.Error()
		}
	}
	if wake, cancel, ok := m.api.SubscribeRunActivity(projectID); ok {
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
		return m, nil
	case tea.KeyMsg:
		if message.Type == tea.KeyCtrlC {
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

func (m model) View() string {
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
	if limit <= 1 {
		return text
	}
	runes := []rune(strings.ReplaceAll(text, "\n", " "))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

func approvalLabel(policy domain.ApprovalPolicy) string {
	switch policy {
	case domain.ApprovalMilestone:
		return "里程碑确认"
	case domain.ApprovalManual:
		return "逐章确认"
	default:
		return "自动推进"
	}
}

func runStateLabel(state domain.CreationRunState) string {
	switch state {
	case domain.RunRunning:
		return "创作中"
	case domain.RunWaitingUser:
		return "等你决定"
	case domain.RunPaused:
		return "已暂停"
	case domain.RunCompleted:
		return "已完成"
	case domain.RunFailed:
		return "需要处理"
	case domain.RunCancelled:
		return "已取消"
	default:
		return string(state)
	}
}
