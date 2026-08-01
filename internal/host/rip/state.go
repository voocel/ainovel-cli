package rip

import (
	"fmt"
	"os"
)

// Action 是 NextAction 从拆文库事实推导出的下一步确定性动作。
// 持久状态不写会漂移的阶段枚举；下一动作只由工件推导。
type Action string

const (
	ActionIngest          Action = "ingest"
	ActionBound           Action = "bound"
	ActionAwaitFormChoice Action = "await_form_choice"
	ActionPreview         Action = "preview"
	ActionAwaitPreviewAck Action = "await_preview_ack"
	ActionSummarize       Action = "summarize"
	ActionAggregate       Action = "aggregate"
	ActionProfile         Action = "profile"
	ActionReport          Action = "report"
	ActionStyle           Action = "style"
	ActionDone            Action = "done"
)

// Facts 是从拆文库读出的、决定下一动作所需的最小事实快照。
// 把纯决策（NextAction）与 IO（LoadState）分离：NextAction 对同一 Facts 恒定。
// 全字段可比较（无切片）——runner 的停滞检测靠 facts == *previous，切片会让它编译不过。
// 失败章号列表按需从 failures/ 重算（Library.failedChapters），不进 Facts。
type Facts struct {
	LibraryReady     bool // manifest + intent + 原文快照三件套齐备
	Bounded          bool // 章节边界表新鲜（唯一切片真值）
	ExpectedChapters int
	Form             Form // FormLong / FormShort / FormAmbiguous
	FormResolved     bool // 灰区已由用户裁定
	Previewed        bool // 黄金三章深读 + 快速预览.md 就绪（仅长篇）
	PreviewAccepted  bool // 用户已授权继续后续阶段

	// SummarizedChapters 是从第 1 章起连续「已有新鲜摘要或已记录持久失败」的章数。
	// 把持久失败计入连续前缀，管线才能越过坏章往下走，而不是卡在同一章无限重试。
	SummarizedChapters int
	FailedChapters     int // failures/ 里的持久失败章数，>0 即终态降级

	Aggregated bool // 剧情单元 / 节奏 / 情绪
	Profiled   bool // 角色 / 设定 / 关系
	Reported   bool // 拆文报告
	Styled     bool // 文风
}

// Degraded 报告本次拆解是否有章节重试后仍失败：产物不完整但可用。
func (f Facts) Degraded() bool { return f.FailedChapters > 0 }

// NextAction 沿固定线性管线，返回第一份缺失或未满足的动作。纯函数，无 IO。
// 长短篇的分支只体现在两个 case 的条件里，不复制管线：短篇跳过黄金三章停靠点，
// 其余阶段共用；灰区字数在拿到用户裁定前停下，不替他猜。
func NextAction(f Facts) Action {
	switch {
	case f.Styled:
		// 文风是终态：全部产物已落盘。上游工件因 prompt 版本升级失鲜不再要求重做——
		// 否则版本升级会把已拆完的书追溯判回半路，用户每次升级都要重付一遍全书调用。
		return ActionDone
	case !f.LibraryReady:
		return ActionIngest
	case !f.Bounded:
		return ActionBound
	case f.Form == FormAmbiguous && !f.FormResolved:
		return ActionAwaitFormChoice
	case f.Form == FormLong && !f.Previewed:
		return ActionPreview
	case f.Form == FormLong && !f.PreviewAccepted:
		return ActionAwaitPreviewAck
	case f.SummarizedChapters < f.ExpectedChapters:
		return ActionSummarize
	case !f.Aggregated:
		return ActionAggregate
	case !f.Profiled:
		return ActionProfile
	case !f.Reported:
		return ActionReport
	default:
		return ActionStyle
	}
}

// artifactFresh 判定工件存在且其 InputDigest 等于当前应重建的 want；
// 缺失、解析失败、schema 或 digest 失配都视为不新鲜（需重做）。
func artifactFresh[T any](l *Library, rel, want string) (bool, error) {
	a, err := readArtifact[T](l, rel)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.InputDigest == want, nil
}

// LoadState 从拆文库读出当前事实快照。
// 线性短路：每一步都校验工件 InputDigest 与当前上游可重建的摘要一致，任一步失配即视为该步
// 未完成，下游事实保持 false，交 NextAction 从此处重做——这才让「改边界/prompt 版本/原文」
// 自然失效下游。opts 只提供本次运行的一次性授权（灰区裁定、预览放行），不写盘的部分也要反映
// 进事实，否则用户带参数重跑会看不到任何变化。
func LoadState(l *Library, opts Options) (Facts, error) {
	var f Facts
	if !l.Active() {
		return f, nil
	}
	if !(l.has(fileManifest) && l.has(fileIntent) && l.has(sourceRel())) {
		return f, nil
	}
	src, err := l.LoadSource()
	if err != nil {
		return f, fmt.Errorf("读取原文快照: %w", err)
	}
	m, err := l.LoadManifest()
	if err != nil {
		return f, fmt.Errorf("读取拆文库身份: %w", err)
	}
	in, err := l.LoadIntent()
	if err != nil {
		return f, fmt.Errorf("读取拆解意图: %w", err)
	}
	f.LibraryReady = true

	// 长短篇形态由字数客观判定，用户只能裁定灰区。字数取 manifest 里落盘的 rune 数，
	// 不每次重数——它是拆文库身份的一部分。
	f.Form = resolveForm(m.Runes)
	if f.Form == FormAmbiguous {
		switch choice := resolvedForm(in, opts); choice {
		case FormLong, FormShort:
			f.Form = choice
			f.FormResolved = true
		}
	}

	guidance, err := l.LoadGuidance()
	if err != nil {
		return f, fmt.Errorf("读取切分指导: %w", err)
	}

	// 边界表：绑定归一化原文 + 用户指导 + 边界 prompt 版本。
	// 指导变化（--guide 重识别）自然失效旧边界表及其全部下游。
	boundArt, err := readArtifact[Boundaries](l, fileBoundaries)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("读取边界工件: %w", err)
	}
	if boundArt.InputDigest != boundInputDigest(Digest(src), guidance, boundPromptVersion) {
		return f, nil
	}
	f.Bounded = true
	b := &boundArt.Payload
	f.ExpectedChapters = len(b.Chapters)

	// 灰区裁定必须在边界之后才有意义？不——字数与边界无关，上面已判定。
	// 但停靠点要排在边界之后：先让用户看到章节数再问长短篇，比空口问字数更有依据。
	if f.Form == FormAmbiguous && !f.FormResolved {
		return f, nil
	}

	// 黄金三章预览：绑定边界工件原始字节。短篇不走这一步（Form 判定已跳过）。
	boundRaw, err := l.readBytes(fileBoundaries)
	if err != nil {
		return f, fmt.Errorf("读取边界工件原文: %w", err)
	}
	if f.Form == FormLong {
		previewed, err := artifactFresh[Preview](l, filePreview, previewInputDigest(boundRaw))
		if err != nil {
			return f, fmt.Errorf("读取快速预览: %w", err)
		}
		if !previewed {
			return f, nil
		}
		f.Previewed = true
		// 放行是一次性授权（AcceptPreview）或落盘的盲授权（--yes）。盲授权不放行带容错
		// 说明的边界表：结构被确定性改写过，必须人工核对——否则 Notes 在 --yes 下无人看见。
		switch {
		case opts.AcceptPreview:
			f.PreviewAccepted = true
		case (opts.AutoConfirm || in.AutoConfirm) && len(b.Notes) == 0:
			f.PreviewAccepted = true
		}
		if !f.PreviewAccepted {
			return f, nil
		}
	}

	// 逐章摘要：逐章 InputDigest 与边界身份/版本/正文匹配的连续数；持久失败章计入前缀。
	digestFor := func(chapter int) string {
		return chapterInputDigest(boundArt.InputDigest, summaryPromptVersion, b, src, chapter-1)
	}
	failed := l.failedChaptersForInput(f.ExpectedChapters, digestFor)
	f.FailedChapters = len(failed)
	failedSet := make(map[int]bool, len(failed))
	for _, n := range failed {
		failedSet[n] = true
	}
	f.SummarizedChapters, err = summarizedChaptersStrict(l, b, src, boundArt.InputDigest, summaryPromptVersion, failedSet)
	if err != nil {
		return f, err
	}
	if f.SummarizedChapters < f.ExpectedChapters {
		return f, nil
	}

	// 聚合：绑定有序逐章摘要的合并摘要。
	summaries := loadSummaries(l, f.ExpectedChapters)
	f.Aggregated, err = artifactFresh[Aggregate](l, fileAggregate, aggregateInputDigest(summaries))
	if err != nil {
		return f, fmt.Errorf("读取聚合工件: %w", err)
	}
	if !f.Aggregated {
		return f, nil
	}

	// 角色设定：绑定聚合工件原始字节。
	aggRaw, err := l.readBytes(fileAggregate)
	if err != nil {
		return f, fmt.Errorf("读取聚合工件原文: %w", err)
	}
	f.Profiled, err = artifactFresh[Profile](l, fileProfile, profileInputDigest(aggRaw))
	if err != nil {
		return f, fmt.Errorf("读取角色设定工件: %w", err)
	}
	if !f.Profiled {
		return f, nil
	}

	// 报告：绑定角色设定工件原始字节。
	profRaw, err := l.readBytes(fileProfile)
	if err != nil {
		return f, fmt.Errorf("读取角色设定工件原文: %w", err)
	}
	f.Reported, err = artifactFresh[Report](l, fileReport, reportInputDigest(profRaw))
	if err != nil {
		return f, fmt.Errorf("读取报告工件: %w", err)
	}
	if !f.Reported {
		return f, nil
	}

	// 文风：绑定边界身份 + 确定性文风统计摘要。文风不依赖报告——它读的是原文本身，
	// 换报告 prompt 不该让文风重跑。
	f.Styled, err = artifactFresh[Style](l, fileStyle, styleInputDigest(boundArt.InputDigest, b, src))
	if err != nil {
		return f, fmt.Errorf("读取文风工件: %w", err)
	}
	return f, nil
}

// resolvedForm 取灰区裁定：本次 --form 优先于落盘 intent（用户显式改主意应当生效）。
func resolvedForm(in *Intent, opts Options) Form {
	if v := Form(opts.Form); v == FormLong || v == FormShort {
		return v
	}
	if in != nil {
		if v := Form(in.Form); v == FormLong || v == FormShort {
			return v
		}
	}
	return ""
}

// ResumeStatus 报告某本书是否存在活动拆文库，以及它是否已彻底完成。
func ResumeStatus(bookDir string) (active, done bool, err error) {
	l := OpenLibrary(bookDir)
	if !l.Active() {
		return false, false, nil
	}
	f, err := LoadState(l, Options{})
	if err != nil {
		return true, false, err
	}
	return true, NextAction(f) == ActionDone, nil
}

// ResumeSummary 生成未完成拆解的一行提示；无未完成拆解返回空串。
func ResumeSummary(bookDir string) string {
	l := OpenLibrary(bookDir)
	if !l.Active() {
		return ""
	}
	f, err := LoadState(l, Options{})
	if err != nil {
		return "发现拆文状态读取异常：" + err.Error() + "；请运行 /rip 查看并修复"
	}
	var state string
	switch NextAction(f) {
	case ActionDone:
		return ""
	case ActionIngest, ActionBound:
		state = "尚未完成章节边界识别"
	case ActionAwaitFormChoice:
		state = fmt.Sprintf("字数介于长短篇之间，等待裁定（--form=long|short），已识别 %d 章", f.ExpectedChapters)
	case ActionPreview:
		state = fmt.Sprintf("已识别 %d 章，待深读黄金三章", f.ExpectedChapters)
	case ActionAwaitPreviewAck:
		state = "快速预览就绪，等待放行"
	case ActionSummarize:
		state = fmt.Sprintf("已拆解 %d/%d 章", f.SummarizedChapters, f.ExpectedChapters)
	case ActionAggregate:
		state = "逐章拆解完成，待聚合剧情单元"
	case ActionProfile:
		state = "待归纳角色与设定"
	case ActionReport:
		state = "待生成拆文报告"
	case ActionStyle:
		state = "待裁定文风"
	}
	return "发现未完成的拆解（" + state + "），输入 /rip 从断点恢复"
}
