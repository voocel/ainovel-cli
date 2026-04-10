package host

import (
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Event 是 TUI 消费的结构化事件。
type Event struct {
	Time     time.Time
	Category string // TOOL / SYSTEM / REVIEW / CHECK / ERROR / AGENT
	Summary  string
	Level    string // info / warn / error / success
}

// UISnapshot 是 TUI 渲染所需的聚合状态快照。
type UISnapshot struct {
	Provider          string
	NovelName         string
	ModelName         string
	Style             string
	RuntimeState      string // idle / running / pausing / paused / completed
	StatusLabel       string
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
