package diag

// Severity 表示发现的严重程度。
type Severity string

const (
	SevCritical Severity = "critical" // 阻塞进度或数据损坏
	SevWarning  Severity = "warning"  // 可能降低质量或浪费 token
	SevInfo     Severity = "info"     // 可优化项
)

// Category 将发现按维度分组。
type Category string

const (
	CatFlow     Category = "flow"     // 流程卡顿、状态异常、恢复问题
	CatQuality  Category = "quality"  // 评审评分、合同履约、一致性
	CatPlanning Category = "planning" // 大纲缺口、伏笔漂移、指南针过时
	CatContext  Category = "context"  // 角色/时间线/关系异常
)

// Confidence 表示规则判定的置信度。
type Confidence string

const (
	ConfHigh   Confidence = "high"   // 确定性强，可信赖
	ConfMedium Confidence = "medium" // 启发式判断，可能有误判
	ConfLow    Confidence = "low"    // 粗略信号，仅供参考
)

// AutoLevel 表示 Finding 是否可以转为自动化动作。
type AutoLevel string

const (
	AutoNone    AutoLevel = "none"    // 仅报告，不自动
	AutoSuggest AutoLevel = "suggest" // 建议动作但需人工确认
	AutoSafe    AutoLevel = "safe"    // 可安全自动执行
)

// Finding 是一条可执行的诊断结果。
type Finding struct {
	Rule       string     // 规则名，如 "StaleForeshadow"
	Category   Category   // 分类
	Severity   Severity   // 严重程度
	Confidence Confidence // 判定置信度
	AutoLevel  AutoLevel  // 自动化级别
	Target     string     // 建议作用面，如 "runtime.flow"
	Title      string     // 一行摘要
	Evidence   string     // 具体数据证据
	Suggestion string     // 改进建议（指向 prompt/flow/config）
}

// RuleFunc 是诊断规则的统一签名。
type RuleFunc func(snap *Snapshot) []Finding

// ActionKind 表示诊断动作的类型。
type ActionKind string

const (
	ActionEmitNotice      ActionKind = "emit_notice"       // 发系统提示
	ActionEnqueueFollowUp ActionKind = "enqueue_follow_up" // 注入 coordinator follow-up
)

// Action 是 Planner 根据高置信 Finding 生成的可执行动作。
type Action struct {
	SourceRule  string     // 来源规则名
	Kind        ActionKind // 动作类型
	Severity    Severity   // 继承自 Finding
	Summary     string     // 简短描述
	Message     string     // 传递给控制流的消息
	Fingerprint string     // 来源 Finding 的稳定指纹，用于运行时去重
}

// Stats 是与发现并列展示的概览指标。
type Stats struct {
	CompletedChapters int
	TotalChapters     int
	TotalWords        int
	AvgWordsPerCh     int
	Phase             string
	Flow              string
	PlanningTier      string
	ReviewCount       int
	RewriteCount      int
	AvgReviewScore    float64
	ForeshadowOpen    int
	ForeshadowStale   int
}

// Report 是一次诊断运行的完整输出。
type Report struct {
	Stats    Stats
	Findings []Finding
	Actions  []Action
}
