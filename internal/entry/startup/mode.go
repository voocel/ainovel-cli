package startup

import "fmt"

// startup 层承载“进入 Engine 之前”的启动编排。
// 分层约定：
// 1. entry/tui、entry/headless 是宿主入口；
// 2. startup 负责快速/共创/续写等启动策略；
// 3. orchestrator.Engine 只负责正式会话执行，不负责模式前置准备。

// Mode 表示进入 Engine 之前的启动策略类型。
type Mode string

const (
	// ModeQuick 直接以用户输入作为创作起点。
	ModeQuick Mode = "quick"
	// ModeCoCreate 先做多轮澄清，再产出创作草稿进入 Engine。
	ModeCoCreate Mode = "cocreate"
	// ModeContinueFromNovel 基于已有小说内容装配上下文后续写。
	ModeContinueFromNovel Mode = "continue_from_novel"
)

// Request 描述入口层提交给启动策略层的原始输入。
// 宿主入口先收集用户输入，再由 startup 把它整理为可进入 Engine 的计划。
type Request struct {
	Mode        Mode
	UserPrompt  string
	NovelPath   string
	OutputDir   string
	Interactive bool
}

// Plan 描述启动策略层产出的结果。
// 宿主入口不应自己拼接正式启动 prompt，而应消费 Plan 再驱动 Engine。
type Plan struct {
	Mode        Mode
	DisplayName string
	StartPrompt string
	ResumeOnly  bool
}

// ErrNotImplemented 标记占位策略尚未落地。
var ErrNotImplemented = fmt.Errorf("startup mode not implemented")

// PrepareContinueFromNovel 是“根据已有小说续写”的统一预留落点。
// TUI/headless 未来都应先把输入整理到 Request，再从这里产出可进入 Engine 的 Plan。
func PrepareContinueFromNovel(req Request) (Plan, error) {
	return Plan{}, fmt.Errorf("%w: %s", ErrNotImplemented, ModeContinueFromNovel)
}
