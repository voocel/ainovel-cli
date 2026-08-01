// Package rip 实现对标小说的只读拆解管线（拆文）。
//
// 与 imp（导入）同一架构纪律：模型只裁定开放语义（章节边界、逐章事实、聚合判断），
// Go 掌管坐标、切片、哈希、顺序与幂等；下一动作只从工件推导（NextAction），不存会漂移的
// 阶段枚举，恢复不依赖 from=N。
//
// 与 imp 的本质差别：拆文是只读的。它不写正式 Store、没有 publish 阶段、不改任何创作状态。
// 产物落在独立的拆文库目录（拆文库/{书名}/），与 output/{novel_name}/ 的创作产物平级共存、
// 互不引用——拆的是别人的书，不该跟自己在写的书混在一个命名空间里。
package rip

import (
	"time"

	"github.com/voocel/agentcore"
)

// Form 是长短篇形态。字数路由的结果，决定是否走黄金三章停靠点。
type Form string

const (
	FormLong      Form = "long"      // 长篇：完整管线，含黄金三章停靠点
	FormShort     Form = "short"     // 短篇：跳过黄金三章，Stage 2-6 直接跑完
	FormAmbiguous Form = "ambiguous" // 灰区：字数介于长短之间，须用户裁定
)

// 长短篇字数分界（rune 数）。灰区抛回用户，不替他猜。
const (
	shortFormMaxRunes = 15000 // < 15k：短篇
	longFormMinRunes  = 20000 // > 20k：长篇；中间为灰区
)

// resolveForm 按字数做长短篇路由。纯函数，是 Form 的唯一判定处。
func resolveForm(runes int) Form {
	switch {
	case runes < shortFormMaxRunes:
		return FormShort
	case runes > longFormMinRunes:
		return FormLong
	default:
		return FormAmbiguous
	}
}

// Options 控制一次拆解。恢复时字段可空，直接从活动拆文库与已保存 Intent 推导。
type Options struct {
	SourcePath string // 新拆解必填；恢复时可空
	LibraryDir string // 拆文库根目录；空则用 <cwd>/拆文库
	BookName   string // 书名（决定库内子目录）；空则取源文件名（去扩展名）

	// Form 是灰区字数的用户裁定（"long"/"short"）。非灰区传值无效——字数是客观事实，
	// 不接受用户把 3 万字的书声明成短篇后按短篇管线跑（会丢掉逐章摘要这一层）。
	Form string

	// AcceptPreview 是看过快速预览后的显式放行，一次性授权继续 Stage 2+，不写 intent。
	AcceptPreview bool
	// AutoConfirm（--yes）是未看预览的盲授权，写入 intent 持久化。
	// 与 AcceptPreview 的区别同 imp 的 AutoConfirm vs AcceptSegmentation：
	// 盲授权不放行带容错说明（Notes）的边界表，看过预览的裁定才放行。
	AutoConfirm bool
	// Guidance（--guide）是自然语言切分指导，落盘后自然使旧边界表失配重识别。
	Guidance string
	// RetryFailed 显式清除当前失败章标记并重试。失败章默认会被视为已处理，
	// 必须有一次性开关才能既避免同轮无限重试，又允许用户主动修复降级产物。
	RetryFailed bool
}

// intent 从 Options 抽取需持久化的用户授权。
func (o Options) intent() Intent {
	return Intent{
		Version:     librarySchemaVersion,
		AutoConfirm: o.AutoConfirm,
		Form:        o.Form,
	}
}

// Stage 表示拆解流程的当前阶段，仅用于 UI 展示，不是恢复事实源。
type Stage string

const (
	StageIngesting       Stage = "ingesting"
	StageBounding        Stage = "bounding"
	StageAwaitingForm    Stage = "awaiting_form"    // 灰区字数：等待 --form=long|short
	StagePreviewing      Stage = "previewing"       // 黄金三章深读
	StageAwaitingPreview Stage = "awaiting_preview" // 快速预览就绪：等待放行
	StageSummarizing     Stage = "summarizing"
	StageAggregating     Stage = "aggregating"
	StageProfiling       Stage = "profiling"
	StageReporting       Stage = "reporting"
	StageStyling         Stage = "styling"
	StageDone            Stage = "done"
	StageError           Stage = "error"
)

// Event 是拆解流程对外发出的进度事件。Event 是投影，不参与恢复。
type Event struct {
	Time    time.Time
	Stage   Stage
	Current int       // 章节/批次进度
	Total   int       // 总数
	Message string    // 人类可读描述
	Level   string    // ""=普通进度；"warn"=退避重试/校验重问等警示状态
	Key     string    // 非空时 UI 对同 Key 连续事件原地更新
	RetryAt time.Time // 非零 = 下次重试的截止时刻，UI 据此倒计时
	Err     error     // StageError 时携带

	// Degraded 在 StageDone 时置位：有章节重试后仍失败，产物不完整但可用。
	// 对应参考实现的 completed_with_errors，但不持久化状态串——失败章号从 failures/ 重算。
	Degraded bool
	Failed   []int // Degraded 时携带失败章号
}

// Prompts 是各语义函数的系统提示词。
// Preview 与 Summary 分开：黄金三章问的是「这个开篇怎么留人」，逐章摘要问的是
// 「这一章的结构与手法」，同一份提示词兼任两者只会两头都写不准。
type Prompts struct {
	Bound     string // 章节边界识别
	Preview   string // 黄金三章深读
	Summary   string // 逐章摘要
	Aggregate string // 剧情单元 / 节奏 / 情绪聚合
	Profile   string // 角色 / 设定 / 关系
	Report    string // 拆文报告
	Style     string // 文风裁定
}

// ModelRuntime 承载 rip 语义调用所需的模型能力事实，由 Host 在边界探测后注入。
// 与 imp.ModelRuntime 同义：让双预算随 context/completion 自然放大、thinking 随能力发送；
// 全零值时回退保守默认，行为与接入能力前一致。
type ModelRuntime struct {
	ContextTokens   int                     // 输入上下文上限（token）
	MaxOutputTokens int                     // 单次可见输出上限（token）
	Thinking        agentcore.ThinkingLevel // 已按能力 resolve；ThinkingAuto("") 表示不显式发送
}

func (rt ModelRuntime) profile() callProfile {
	return callProfile{thinking: rt.Thinking}
}

// Caller 是一个语义函数的模型档位：模型 + 该模型的能力事实。
// bound/summary/aggregate/report 各自持有档位，预算与调用选项按各自档位派生，
// 廉价档位的小窗口只约束它自己的函数。
type Caller struct {
	Model   callModel
	Runtime ModelRuntime
}

// Deps 是 runner 的窄依赖。四个语义档位由 Host 解析，默认全落 architect，
// 配置层可把机械性更强的函数（边界、逐章摘要）指到更便宜档位。
type Deps struct {
	Bound     Caller
	Summary   Caller
	Aggregate Caller
	Report    Caller // 报告与文风同档位（同属全书级裁定）
	Prompts   Prompts
	Budgets   RunBudgets
}
