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

// Finding 是一条可执行的诊断结果。
type Finding struct {
	Rule       string   // 规则名，如 "StaleForeshadow"
	Category   Category // 分类
	Severity   Severity // 严重程度
	Title      string   // 一行摘要
	Evidence   string   // 具体数据证据
	Suggestion string   // 改进建议（指向 prompt/flow/config）
}

// RuleFunc 是诊断规则的统一签名。
type RuleFunc func(snap *Snapshot) []Finding

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
}
