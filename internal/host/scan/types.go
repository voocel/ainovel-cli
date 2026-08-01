// Package scan 实现榜单扫描管线（扫榜）：把平台榜单的半结构化文本整理成
// 结构化条目、市场趋势报告与可直接用的选题决策。
//
// 与 rip（拆文）同一架构纪律：模型只裁定开放语义（把杂乱文本读成条目、归纳趋势、
// 提选题），Go 掌管清洗、质量门、哈希、顺序与幂等；下一动作只从工件推导（NextAction），
// 不存会漂移的阶段枚举。
//
// 第一版不含爬虫：数据由用户提供（粘贴文本 / 本地文件 / 目录）。Fetcher 留了接口缝，
// 将来要加 httpFetcher 只需实现同一接口——那是给项目新增「抓取任意 URL」的对外能力，
// 应当单独决策，不作为本功能的副产品悄悄引入。
package scan

import (
	"time"

	"github.com/voocel/agentcore"
)

// Stage 是扫榜管线的当前阶段，仅用于 UI 展示，不是恢复事实源。
type Stage string

const (
	StageFetching  Stage = "fetching"  // 获取并快照榜单数据
	StageParsing   Stage = "parsing"   // 半结构化文本 → 结构化条目
	StageCleaning  Stage = "cleaning"  // 清洗与质量门（纯 Go）
	StageAnalyzing Stage = "analyzing" // 趋势与新元素
	StageTopicing  Stage = "topicing"  // 选题决策
	StageDone      Stage = "done"
	StageError     Stage = "error"
)

// Event 是扫榜管线对外发出的进度事件。Event 是投影，不参与恢复。
type Event struct {
	Time    time.Time
	Stage   Stage
	Current int       // 条目/数据源进度
	Total   int       // 总数
	Message string    // 人类可读描述
	Level   string    // ""=普通进度；"warn"=退避重试/校验重问等警示状态
	Key     string    // 非空时 UI 对同 Key 连续事件原地更新
	RetryAt time.Time // 非零 = 下次重试的截止时刻，UI 据此倒计时
	Err     error     // StageError 时携带

	// Sparse 在 StageDone 时置位：有效条目不足阈值，结论参考价值有限。
	// 与 rip 的 Degraded 同义——产物可用但打了折扣，终态必须说清。
	Sparse bool
	// Entries 在 StageDone 时携带有效条目数，供 UI 一行回显。
	Entries int
	// Dir 在 StageDone 时携带扫榜库路径，供 UI 指引用户去看产物。
	Dir string
}

// Options 控制一次扫榜。恢复时数据源字段可空，直接从活动扫榜库的快照推导。
type Options struct {
	// 数据源三选一，优先级：粘贴文本 > 单文件 > 目录。
	PastedText string
	FilePath   string
	DirPath    string

	Platform   string // 平台：qidian/fanqie/qimao/jjwxc/ciweimao/other
	RankName   string // 榜单名
	LibraryDir string // 扫榜库根目录；空则用 <cwd>/扫榜库
	ScanDate   string // 采集日期 YYYYMMDD；空则取今天
}

// Prompts 是各语义函数的系统提示词。
type Prompts struct {
	Parse   string // 半结构化文本 → 结构化条目
	Analyze string // 趋势与新元素
	Topic   string // 选题决策
}

// ModelRuntime 承载扫榜语义调用所需的模型能力事实，由 Host 在边界探测后注入。
// 与 rip.ModelRuntime 同义：双预算随 context/completion 自然放大、thinking 随能力发送；
// 全零值时回退保守默认。
type ModelRuntime struct {
	ContextTokens   int
	MaxOutputTokens int
	Thinking        agentcore.ThinkingLevel
}

func (rt ModelRuntime) profile() callProfile {
	return callProfile{thinking: rt.Thinking}
}

// Caller 是一个语义函数的模型档位：模型 + 该模型的能力事实。
type Caller struct {
	Model   callModel
	Runtime ModelRuntime
}

// Deps 是 runner 的窄依赖。两个语义档位由 Host 解析，默认全落 architect。
// 选题与趋势同档（同属全局裁定）；解析是机械劳动，可指到更便宜档位。
type Deps struct {
	Parse   Caller
	Analyze Caller // 趋势与选题共用
	Prompts Prompts
	Budgets RunBudgets
}
