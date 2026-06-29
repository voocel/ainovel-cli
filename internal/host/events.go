package host

import (
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Event 是 TUI 消费的结构化事件。
//
// 对于 TOOL / DISPATCH 两类调用事件，同一次调用的Bắt đầu与Kết thúc共用一个 ID：
// Bắt đầu时先发 FinishedAt 为零值的事件（TUI 渲染为"Đang thực hiện"样式）；
// Kết thúc时再发一条同 ID 的事件，填入 FinishedAt + Duration（+ Failed），
// TUI 按 ID 定位原行原地更Mới，避免"Bắt đầu一行、Hoàn thành又一行"的冗余。
//
// SYSTEM / ERROR / CONTEXT 等非调用类事件 ID 为Rỗng，每条独立追加。
type Event struct {
	ID         string    // 同一次调用的Bắt đầu/Kết thúc共用；非调用事件为Rỗng
	Time       time.Time // 首次发出时间（Bắt đầu时刻）
	FinishedAt time.Time // 零值 = Đang thực hiện；非零 = Đã hoàn thành
	Failed     bool      // Đã hoàn thành但Thất bại（仅Hoàn thành态有意义）
	Category   string    // DISPATCH / TOOL / SYSTEM / REVIEW / CHECK / ERROR / CONTEXT
	Agent      string    // 产生事件的 agent
	Summary    string
	Detail     string        // 完整文案，写入日志不截断供排查；为Rỗng回退 Summary。UI 只读 Summary
	Kind       string        // Lỗi分类（如 stream_idle），随日志输出供过滤/告警；为Rỗng不输出
	Level      string        // info / warn / error / success
	Depth      int           // 0 = coordinator 层, 1 = sub-agent 层
	Duration   time.Duration // Hoàn thành时的执行耗时
}

// Running Quay lại事件Có czy không处于Đang thực hiện。
// 仅调用类事件（有 ID 的 TOOL / DISPATCH）可能Đang thực hiện；其它类型总是Quay lại false。
func (e Event) Running() bool {
	return e.ID != "" && e.FinishedAt.IsZero()
}

// UISnapshot 是 TUI 渲染所需的聚合Trạng tháiChụp。
type UISnapshot struct {
	Provider           string
	NovelName          string
	ModelName          string
	ModelContextWindow int // Hiện tạiMặc địnhMô hình的Cửa sổ ngữ cảnh（随 /model 切换实时解析）
	Style              string
	RuntimeState       string // idle / running / pausing / paused / completed
	StatusLabel        string
	Phase              string
	Flow               string
	CurrentChapter     int
	TotalChapters      int
	CompletedCount     int
	TotalWordCount     int
	InProgressChapter  int
	PendingRewrites    []int
	RewriteReason      string
	PendingSteer       string
	RecoveryLabel      string
	IsRunning          bool
	Agents             []AgentSnapshot

	// Ngữ cảnh
	ContextTokens         int
	ContextWindow         int
	ContextPercent        float64
	ContextScope          string
	ContextStrategy       string
	ContextActiveMessages int
	ContextSummaryCount   int
	ContextCompactedCount int
	ContextKeptCount      int

	// 累计用量（整个会话，跨所有 agent 与Mô hình切换）
	TotalInputTokens      int
	TotalOutputTokens     int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	TotalCostUSD          float64
	TotalSavedUSD         float64 // 因 CacheRead 命中省下的美元（相对全按非缓存Nhập价计费）
	BudgetLimitUSD        float64 // 预算上限（config budget.book_usd）；0 = 未Bật

	// 缓存诊断
	OverallCacheCapable    bool // 至少一个 role 跑过支持 prompt cache 的Mô hình（区分"未Bật"和"0% 命中"）
	OverallRecentCacheRead int  // 滑动窗最近 N 次的 cacheRead 总和
	OverallRecentInput     int  // 滑动窗最近 N 次的 input 总和
	OverallRecentSamples   int  // 滑动窗内的样本数（≤ recentSampleCap）

	// MissingAssistantUsage > 0 通常意味着上游 streaming 没按 OpenAI
	// stream_options.include_usage 协议发 final usage chunk（自建 proxy 常见），
	// 导致 UsageTracker 收不到任何累计数据。UI 据此明示用户排查 backend，
	// 不要让用户误以为是缓存模块本身坏了。
	MissingAssistantUsage int

	// 缓存 per-role 维度，按 CacheRead 降序，已过滤未消费 token 的 role
	CachePerAgent []AgentCacheStat
	CachePerModel []AgentCacheStat

	// 基础设定
	Premise          string
	Outline          []OutlineSnapshot
	Characters       []string
	SupportingCount  int      // 配角名册中的次要角色总数
	RecentSupporting []string // 最近活跃的次要角色（最多 5 个，按 LastSeenChapter 倒序）
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

// OutlineSnapshot 是Đại cương条目的展示Tóm tắt。
type OutlineSnapshot struct {
	Chapter   int
	Title     string
	CoreEvent string
}

// AgentSnapshot 是 Agent Trạng thái的展示投影。
type AgentSnapshot struct {
	Name      string
	State     string
	TaskID    string
	TaskKind  string
	Summary   string
	Tool      string
	Turn      int
	Context   AgentContextSnapshot
	UpdatedAt time.Time
}

// AgentCacheStat 是单个 agent 的缓存命中累计（投影到左栏）。
// HitRate = CacheRead / Input；Input 在 litellm 层已统一为"含 CacheRead"语义。
//
// CacheCapable 用来区分两种 0% 命中：
//   - true  → Mô hình支持 prompt cache，0% 是 prompt 设计差或前缀不稳定，Cần优化
//   - false → Mô hình/provider Không hỗ trợ prompt cache，0% 是预期，不必排查
//
// Recent* 是滑动窗（最近 N 次调用）的命中数据，对比累计可识别"前期拖累"vs"稳态低命中"。
type AgentCacheStat struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// AgentContextSnapshot 是 Agent Ngữ cảnh使用情况。
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

// CoCreateReply 是共创对话的 LLM 回复。Raw 保留Mô hình完整四段原文，
// 用于写回 history 让下一轮Mô hình看到自己上一轮的 [DRAFT]，从而真Đang
// 已有Bản nháp上累积更Mới（仅 Message 不含 [DRAFT]，会导致Mô hình每轮凭对话重Mới归纳）。
// Suggestions 是 AI 主动给的"接下来你可能想说"，用户卡壳时按数字键一键填入Nhập框。
type CoCreateReply struct {
	Message     string
	Prompt      string
	Ready       bool
	Suggestions []string
	Raw         string
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
	return "Vui lòng根据以下创作要求Bắt đầu创作一部小说。进入规划后，Premise 第一行必须输出 `# Tên sách`。Chương数量由你根据故事Cần自行决定；若题材与冲突天然适合长篇连载，Vui lòng优先规划为分层长篇结构，而不是压缩成短篇式梗概。\n\n[创作要求]\n" +
		prompt +
		"\n\n若某些细节未明确，Vui lòng在不违背用户方向的前提下自行补全。"
}
