package host

import (
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Event 是 TUI 消费的结构化事件。
//
// 对于 TOOL / DISPATCH 两类调用事件，同一次调用的开始与结束共用一个 ID：
// 开始时先发 FinishedAt 为零值的事件（TUI 渲染为"进行中"样式）；
// 结束时再发一条同 ID 的事件，填入 FinishedAt + Duration（+ Failed），
// TUI 按 ID 定位原行原地更新，避免"开始一行、完成又一行"的冗余。
//
// SYSTEM / ERROR / CONTEXT 等非调用类事件 ID 为空，每条独立追加。
type Event struct {
	ID         string        // 同一次调用的开始/结束共用；非调用事件为空
	Time       time.Time     // 首次发出时间（开始时刻）
	FinishedAt time.Time     // 零值 = 进行中；非零 = 已完成
	Failed     bool          // 已完成但失败（仅完成态有意义）
	Category   string        // DISPATCH / TOOL / SYSTEM / REVIEW / CHECK / ERROR / CONTEXT
	Agent      string        // 产生事件的 agent
	Summary    string
	Level      string        // info / warn / error / success
	Depth      int           // 0 = coordinator 层, 1 = sub-agent 层
	Duration   time.Duration // 完成时的执行耗时
}

// Running 返回事件是否处于进行中。
// 仅调用类事件（有 ID 的 TOOL / DISPATCH）可能进行中；其它类型总是返回 false。
func (e Event) Running() bool {
	return e.ID != "" && e.FinishedAt.IsZero()
}

// UISnapshot 是 TUI 渲染所需的聚合状态快照。
type UISnapshot struct {
	Provider           string
	NovelName          string
	ModelName          string
	ModelContextWindow int // 当前默认模型的上下文窗口（随 /model 切换实时解析）
	Style              string
	RuntimeState       string // idle / running / pausing / paused / completed
	StatusLabel        string
	Phase             string
	Flow              string
	CurrentChapter    int
	TotalChapters     int
	CompletedCount    int
	TotalWordCount    int
	InProgressChapter int
	PendingRewrites   []int
	RewriteReason     string
	PendingSteer      string
	RecoveryLabel     string
	IsRunning         bool
	Agents            []AgentSnapshot
	Tasks             []TaskSnapshot

	// 上下文
	ContextTokens         int
	ContextWindow         int
	ContextPercent        float64
	ContextScope          string
	ContextStrategy       string
	ProjectionTokens      int
	ProjectionWindow      int
	ProjectionPercent     float64
	ProjectionStrategy    string
	ProjectionCompacted   int
	ProjectionKept        int
	ContextActiveMessages int
	ContextSummaryCount   int
	ContextCompactedCount int
	ContextKeptCount      int

	// 累计用量（整个会话，跨所有 agent 与模型切换）
	TotalInputTokens      int
	TotalOutputTokens     int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	TotalCostUSD          float64

	// 基础设定
	Premise          string
	Outline          []OutlineSnapshot
	Characters       []string
	Layered          bool
	CurrentVolumeArc string
	NextVolumeTitle  string
	CompassDirection string
	CompassScale     string

	// 详情
	LastCommitSummary  string
	LastReviewSummary  string
	LastCheckpointName string
	RecentSummaries    []string
}

// OutlineSnapshot 是大纲条目的展示摘要。
type OutlineSnapshot struct {
	Chapter   int
	Title     string
	CoreEvent string
}

// TaskSnapshot 是任务状态的展示投影（从事件流投影，非事实层）。
type TaskSnapshot struct {
	ID        string
	Kind      string
	Owner     string
	Title     string
	Status    string
	Chapter   int
	Volume    int
	Arc       int
	Summary   string
	Tool      string
	OutputRef string
	UpdatedAt time.Time
}

// AgentSnapshot 是 Agent 状态的展示投影。
type AgentSnapshot struct {
	Name             string
	State            string
	TaskID           string
	TaskKind         string
	Summary          string
	Tool             string
	Turn             int
	Context          AgentContextSnapshot
	RecentProjection AgentContextSnapshot
	UpdatedAt        time.Time
}

// AgentContextSnapshot 是 Agent 上下文使用情况。
type AgentContextSnapshot struct {
	Tokens          int
	ContextWindow   int
	Percent         float64
	Scope           string
	Strategy        string
	ActiveMessages  int
	SummaryMessages int
	CompactedCount  int
	KeptCount       int
}

// CoCreateMessage 是共创对话的消息。
type CoCreateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CoCreateReply 是共创对话的 LLM 回复。
type CoCreateReply struct {
	Message string
	Prompt  string
	Ready   bool
}

// ReplayDeltaText 从运行时队列项中提取可回放的流式文本。
func ReplayDeltaText(item domain.RuntimeQueueItem) string {
	if payload, ok := item.Payload.(map[string]any); ok {
		if text, ok := payload["delta"].(string); ok {
			return text
		}
	}
	return ""
}

// BuildStartPrompt 将用户需求包装为 Coordinator 的启动 prompt。
func BuildStartPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	return "请根据以下创作要求开始创作一部小说。进入规划后，Premise 第一行必须输出 `# 书名`。章节数量由你根据故事需要自行决定；若题材与冲突天然适合长篇连载，请优先规划为分层长篇结构，而不是压缩成短篇式梗概。\n\n[创作要求]\n" +
		prompt +
		"\n\n若某些细节未明确，请在不违背用户方向的前提下自行补全。"
}
