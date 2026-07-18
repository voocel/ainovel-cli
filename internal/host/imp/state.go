package imp

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/store"
)

// Action 是 NextAction 从工作区事实推导出的下一步确定性动作。
// 持久状态不写会漂移的阶段枚举；下一动作只由工件推导（RFC §6.2）。
type Action string

const (
	ActionIngest               Action = "ingest"
	ActionSegment              Action = "segment"
	ActionAwaitConfirmation    Action = "await_confirmation"
	ActionAnalyze              Action = "analyze"
	ActionSynthesize           Action = "synthesize"
	ActionAwaitStoryResolution Action = "await_story_resolution"
	ActionPublish              Action = "publish"
	ActionDone                 Action = "done"
)

// Facts 是从工作区读出的、决定下一动作所需的最小事实快照。
// 把纯决策（NextAction）与 IO（LoadState）分离：NextAction 对同一 Facts 恒定（RFC §20.1）。
type Facts struct {
	WorkspaceReady   bool // manifest + intent + source 三件套齐备
	Segmented        bool
	Confirmed        bool
	ExpectedChapters int // 切分确认的章节总数（阶段二起填充）
	AnalyzedChapters int // 从第 1 章起连续、InputDigest 匹配的分析数（阶段三起填充）
	Synthesized      bool
	StoryUncertain   bool
	StoryResolved    bool
	Published        bool // 正式工件与 synthesis 完全一致（阶段五起填充）
}

// NextAction 沿固定线性管线，返回第一份缺失或未满足的动作。纯函数，无 IO。
func NextAction(f Facts) Action {
	switch {
	case f.Published:
		// 发布是终态：正式库对账已全量一致，工作区只是审计存档。上游工件因
		// prompt 版本 / 指导升级失鲜不再要求重做——否则版本升级会把已发布的书
		// 追溯判回半路，Engine 跨重启门禁将其永久锁死。
		return ActionDone
	case !f.WorkspaceReady:
		return ActionIngest
	case !f.Segmented:
		return ActionSegment
	case !f.Confirmed:
		return ActionAwaitConfirmation
	case f.AnalyzedChapters < f.ExpectedChapters:
		return ActionAnalyze
	case !f.Synthesized:
		return ActionSynthesize
	case f.StoryUncertain && !f.StoryResolved:
		return ActionAwaitStoryResolution
	default:
		return ActionPublish
	}
}

// artifactFresh 判定工件存在且其 InputDigest 等于当前应重建的 want；
// 缺失、解析失败、schema 或 digest 失配都视为不新鲜（需重做）。
func artifactFresh[T any](w *Workspace, rel, want string) (bool, error) {
	a, err := readArtifact[T](w, rel)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.InputDigest == want, nil
}

// LoadState 从工作区读出当前事实快照（仅工作区，不含正式 Store）。
// 线性短路：每一步都校验工件 InputDigest 与当前上游可重建的摘要一致，任一步失配即视为该步未完成，
// 下游事实保持 false，交 NextAction 从此处重做——这才让「改切分/prompt 版本/源」自然失效下游（RFC §6.2/§6.3 / 不变量 1）。
// Published 由调用方按正式发布对账补齐（统一走 CollectFacts）。
func LoadState(w *Workspace) (Facts, error) {
	var f Facts
	if !w.Active() {
		return f, nil
	}
	if !(w.has(fileManifest) && w.has(fileIntent) && w.has(fileSource)) {
		return f, nil
	}
	src, err := w.LoadSource()
	if err != nil {
		return f, fmt.Errorf("读取导入源快照: %w", err)
	}
	f.WorkspaceReady = true
	guidance, err := w.LoadGuidance()
	if err != nil {
		return f, fmt.Errorf("读取切分指导: %w", err)
	}

	// segmentation：绑定归一化源 + 用户指导 + 切分 prompt 版本。指导变化（--guide 重识别）自然失效旧切分。
	segArt, err := readArtifact[Segmentation](w, fileSegmentation)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("读取切分工件: %w", err)
	}
	if segArt.InputDigest != segmentInputDigest(Digest(src), guidance, segmentPromptVersion) {
		return f, nil
	}
	f.Segmented = true
	seg := &segArt.Payload
	f.ExpectedChapters = len(seg.Chapters)

	// confirmation：绑定 segmentation 工件原始字节。
	segRaw, err := w.readBytes(fileSegmentation)
	if err != nil {
		return f, fmt.Errorf("读取切分工件原文: %w", err)
	}
	confirmed, err := artifactFresh[Confirmation](w, fileConfirmation, Digest(segRaw))
	if err != nil {
		return f, fmt.Errorf("读取切分确认: %w", err)
	}
	if !confirmed {
		return f, nil
	}
	f.Confirmed = true

	// 逐章分析：逐章 InputDigest 与切分身份/版本/正文匹配的连续数。
	f.AnalyzedChapters, err = analyzedChaptersStrict(w, seg, src, segArt.InputDigest, analyzePromptVersion)
	if err != nil {
		return f, err
	}
	if f.AnalyzedChapters < f.ExpectedChapters {
		return f, nil
	}

	// synthesis：绑定有序逐章事实。
	facts, err := loadPriorFactsStrict(w, f.ExpectedChapters)
	if err != nil {
		return f, err
	}
	synArt, err := readArtifact[BookSynthesis](w, fileSynthesis)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("读取全书综合工件: %w", err)
	}
	if synArt.InputDigest != synthesisInputDigest(facts) {
		return f, nil
	}
	f.Synthesized = true
	f.StoryUncertain = synArt.Payload.StoryStatus == storyUncertain

	// story resolution：uncertain 时绑定 synthesis 工件原始字节，或由 intent 预选。
	synRaw, err := w.readBytes(fileSynthesis)
	if err != nil {
		return f, fmt.Errorf("读取全书综合工件原文: %w", err)
	}
	resolved, err := artifactFresh[StoryResolution](w, fileStoryResolve, Digest(synRaw))
	if err != nil {
		return f, fmt.Errorf("读取故事状态裁定: %w", err)
	}
	if resolved {
		f.StoryResolved = true
	} else if in, iErr := w.LoadIntent(); iErr != nil {
		return f, fmt.Errorf("读取导入意图: %w", iErr)
	} else if in.StoryResolution != "" {
		f.StoryResolved = true
	}
	return f, nil
}

// CollectFacts 组合工作区事实与正式发布对账，是 ResumeStatus/ResumeSummary/runner
// 的统一事实入口。发布对账的期望章数优先取新鲜切分；切分因 prompt 版本 / 指导升级
// 而失配时，退回工件里当时确认的章数——已发布书的正式章节正是按那份切分落库的，
// 用当前版本重算 digest 对账反而对不上任何东西。
func CollectFacts(st *store.Store, w *Workspace) (Facts, error) {
	f, err := LoadState(w)
	if err != nil {
		return f, err
	}
	expected := f.ExpectedChapters
	if expected == 0 {
		if segArt, err := readArtifact[Segmentation](w, fileSegmentation); err == nil {
			expected = len(segArt.Payload.Chapters)
		}
	}
	f.Published, err = isPublished(st, expected)
	return f, err
}

// ResumeStatus 报告是否存在活动导入工作区，以及它是否已彻底完成（含正式发布对账）。
// 供跨重启 Engine 门禁使用（RFC §12.5）：active && !done 时禁止普通创作流程消费半发布状态。
func ResumeStatus(st *store.Store) (active, done bool, err error) {
	w := OpenWorkspace(st.Dir())
	if !w.Active() {
		return false, false, nil
	}
	f, err := CollectFacts(st, w)
	if err != nil {
		return true, false, err
	}
	return true, NextAction(f) == ActionDone, nil
}

// ResumeSummary 生成未完成导入的一行提示（RFC §18.2）；无未完成导入返回空串。
// 供宿主在启动/欢迎界面主动告知，避免用户只有在创作被门禁拒绝时才发现这本书停在导入半路。
func ResumeSummary(st *store.Store) string {
	w := OpenWorkspace(st.Dir())
	if !w.Active() {
		return ""
	}
	f, err := CollectFacts(st, w)
	if err != nil {
		return "发现导入状态读取异常：" + err.Error() + "；请运行 /import 查看并修复"
	}
	var state string
	switch NextAction(f) {
	case ActionDone:
		return ""
	case ActionIngest, ActionSegment:
		state = "尚未完成切分"
	case ActionAwaitConfirmation:
		state = fmt.Sprintf("已切分 %d 章，等待核对确认", f.ExpectedChapters)
	case ActionAnalyze:
		state = fmt.Sprintf("已分析 %d/%d 章", f.AnalyzedChapters, f.ExpectedChapters)
	case ActionSynthesize:
		state = "逐章分析完成，待全书综合"
	case ActionAwaitStoryResolution:
		state = "待明确故事状态（--story=open|closed）"
	case ActionPublish:
		state = "综合完成，待发布正式状态"
	}
	return "发现未完成的导入（" + state + "），输入 /import 从断点恢复"
}

// checkImportPreconditions 校验新导入前置条件（RFC §12.1）：
// 没有已完成章节、没有在途 PendingCommit。已有小说与新外部文本的合并语义不清楚，第一版明确拒绝。
func checkImportPreconditions(st *store.Store) error {
	prog, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("读取进度：%w", err)
	}
	if prog != nil && len(prog.CompletedChapters) > 0 {
		return fmt.Errorf("已有 %d 个完成章节，拒绝把外部小说并入非空书籍", len(prog.CompletedChapters))
	}
	pending, err := st.Signals.LoadPendingCommit()
	if err != nil {
		return fmt.Errorf("读取在途提交：%w", err)
	}
	if pending != nil {
		return fmt.Errorf("存在在途章节提交，请先完成或清理后再导入")
	}
	return nil
}
