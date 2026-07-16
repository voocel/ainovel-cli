// Package imp 实现外部小说的分阶段语义导入管线（docs/import-pipeline.md）。
//
// 模型负责理解开放语义，代码负责坐标、覆盖、类型、哈希、顺序和幂等；全部语义产物在
// 独立工作区（meta/import/）验证完成后，才发布到正式书籍状态。下一动作只从工件推导
// （NextAction），不存会漂移的阶段枚举，恢复不依赖 from=N。
package imp

import "time"

// Options 控制一次导入。恢复时字段可空，直接从活动工作区与已保存 Intent 推导。
type Options struct {
	SourcePath      string // 新导入必填；恢复时可空
	AutoConfirm     bool   // --yes：覆盖校验通过后自动接受切分
	StoryResolution string // --story=open|closed：仅 synthesis 返回 uncertain 时预选
	ContinueAfter   bool   // --continue：不创建导入完成 Hold
	Guidance        string // --guide：自然语言切分指导，落盘工作区后自然使旧切分失配重识别
	// AcceptSegmentation：TUI 预览后的显式人工确认（y）。一次性放行当前切分，不写 intent；
	// 与 --yes 的区别：--yes 是未看预览的盲授权，不放行带容错说明（Notes）的切分，y 是看过预览的裁定。
	AcceptSegmentation bool
}

// intent 从 Options 抽取需持久化的用户授权。
func (o Options) intent() Intent {
	return Intent{
		Version:             workspaceSchemaVersion,
		AutoConfirm:         o.AutoConfirm,
		StoryResolution:     o.StoryResolution,
		ContinueAfterImport: o.ContinueAfter,
	}
}

// Stage 表示导入流程的当前阶段，仅用于 UI 展示，不是恢复事实源（RFC §14.1）。
type Stage string

const (
	StageIngesting            Stage = "ingesting"
	StageSegmenting           Stage = "segmenting"
	StageAwaitingConfirmation Stage = "awaiting_confirmation"
	StageAnalyzing            Stage = "analyzing"
	StageSynthesizing         Stage = "synthesizing"
	StageAwaitingStoryStatus  Stage = "awaiting_story_status"
	StageValidating           Stage = "validating"
	StagePublishing           Stage = "publishing"
	StageDone                 Stage = "done"
	StageError                Stage = "error"
)

// Event 是导入流程对外发出的进度事件。Event 是投影，不参与恢复。
type Event struct {
	Time      time.Time
	Stage     Stage
	Current   int       // 章节/区间进度
	Total     int       // 总数
	Message   string    // 人类可读描述
	Level     string    // ""=普通进度；"warn"=退避重试/校验重问等警示状态
	Key       string    // 非空时 UI 对同 Key 连续事件原地更新（如 7 次退避在一行变动），对齐事件面板 ID 机制
	RetryAt   time.Time // 非零 = 下次重试的截止时刻；UI 据此逐秒倒计时渲染，到点即清（请求已在途）
	Err       error     // StageError 时携带
	Continued bool      // StageDone 时由 Host 置位：是否已自动接力启动 Engine（--continue × auto）
}
