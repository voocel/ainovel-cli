package imp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/logger"
	"github.com/voocel/ainovel-cli/internal/store"
)

// prompt/schema 版本纳入各阶段 InputDigest；升级 prompt 契约时递增以自然失效下游工件。
const (
	segmentPromptVersion = "seg-v2" // v2：边界只落真实分隔处、标题逐字复制（配合标题回显校验）
	analyzePromptVersion = "analyze-v1"
	confirmMethodAuto    = "auto_authorized"
	confirmMethodUser    = "user_confirmed" // TUI 预览后按 y 的显式人工确认
)

// Prompts 是各语义函数的系统提示词。综合分两阶段：Synthesize 出全书 BookSynthesis，
// Range 出长书连续区间 RangeDigest；两者输出结构不同，须各用对应提示词。
type Prompts struct {
	Segment    string
	Analyze    string
	Synthesize string
	Range      string
}

// RunBudgets 是各语义函数的输入/输出预算。第一版用保守常量；
// 未来应由当前 architect 模型的 context window / completion 上限推导，使批次随能力自然放大（RFC §9.2/§21）。
type RunBudgets struct {
	MaxUnitBytes         int
	SegmentChunkBytes    int
	SegmentContextMargin int
	SegmentMaxTokens     int
	Analyze              AnalyzeBudget
	SynthesizeRangeBytes int
	SynthesizeMaxTokens  int
}

// DefaultRunBudgets 返回保守默认预算，用于模型能力未知（探测失败）时兜底。
func DefaultRunBudgets() RunBudgets {
	return RunBudgets{
		MaxUnitBytes:         8000,
		SegmentChunkBytes:    24000,
		SegmentContextMargin: 20,
		SegmentMaxTokens:     8192,
		Analyze:              AnalyzeBudget{ContextBytes: 24000, MaxOutputTokens: 8000, PerChapterOutput: 900, PromptOverhead: 2000},
		SynthesizeRangeBytes: 16000,
		SynthesizeMaxTokens:  8192,
	}
}

// ModelRuntime 承载 imp 语义调用所需的模型能力事实，由 Host 在边界探测后注入（RFC §13/§17）。
// 让双预算随 context/completion 自然放大、thinking 随能力发送；全零值时回退保守默认，
// 行为与接入能力前一致。结构化输出不按 provider 能力发 response_format（见 callProfile 注释）。
type ModelRuntime struct {
	ContextTokens   int                     // 输入上下文上限（token）
	MaxOutputTokens int                     // 单次可见输出上限（token）
	Thinking        agentcore.ThinkingLevel // 已按能力 resolve；ThinkingAuto("") 表示不显式发送
}

// profile 派生本运行时的调用能力选项（thinking）。
func (rt ModelRuntime) profile() callProfile {
	return callProfile{thinking: rt.Thinking}
}

// Caller 是一个语义函数的模型档位：模型 + 该模型的能力事实（RFC §13.1/§17）。
// segment/analyze/synthesize 各自持有档位，预算与调用选项都按各自档位派生，
// 廉价档位的小窗口只约束它自己的函数，不拖累其它阶段。
type Caller struct {
	Model   callModel
	Runtime ModelRuntime
}

// budgetsFromRuntime 从模型真实 context/completion 上限派生各语义函数预算（RFC §9.2/§21）。
// 这才让「换更强模型自动扩大批次、减少调用次数」成立；能力未知时回退保守默认。
func budgetsFromRuntime(rt ModelRuntime) RunBudgets {
	if rt.ContextTokens <= 0 || rt.MaxOutputTokens <= 0 {
		return DefaultRunBudgets()
	}
	const bytesPerToken = 3 // 中文 UTF-8 保守换算：token→字节（偏低估容量更安全）
	out := rt.MaxOutputTokens
	// 输入预算：上下文扣掉可见输出与 ~10% 推理/系统预留后按字节换算。
	reserve := rt.ContextTokens / 10
	inTokens := rt.ContextTokens - out - reserve
	if inTokens < 2000 {
		inTokens = 2000
	}
	inBytes := inTokens * bytesPerToken
	return RunBudgets{
		MaxUnitBytes:         min(inBytes/2, 32000),
		SegmentChunkBytes:    inBytes,
		SegmentContextMargin: 20,
		SegmentMaxTokens:     out,
		Analyze: AnalyzeBudget{
			ContextBytes:     inBytes,
			MaxOutputTokens:  out,
			PerChapterOutput: 900,
			PromptOverhead:   2000,
		},
		SynthesizeRangeBytes: inBytes,
		SynthesizeMaxTokens:  out,
	}
}

// Confirmation 是切分确认工件，绑定当前 segmentation（RFC §8.4）。
type Confirmation struct {
	Method   string `json:"method"`
	Chapters int    `json:"chapters"`
}

// StoryResolution 是 uncertain 故事状态的用户裁定，绑定当前 synthesis（RFC §10.4）。
type StoryResolution struct {
	Choice string `json:"choice"` // open / closed
}

// Deps 是 runner 的窄依赖（RFC §17）。三个语义函数各自声明模型档位；
// Host 默认全部落 architect，配置层可把机械性更强的函数指到更便宜档位（RFC §13.1）。
type Deps struct {
	Store         *store.Store
	CommitChapter ChapterCommitter
	Segment       Caller
	Analyze       Caller
	Synthesize    Caller // range digest 与 book synthesis 同档位（同一综合阶段）
	Prompts       Prompts
	Budgets       RunBudgets
}

// budgetsFromDeps 按各语义函数自己的档位能力派生预算（RFC §9.2/§13.1）。
func budgetsFromDeps(d Deps) RunBudgets {
	seg := budgetsFromRuntime(d.Segment.Runtime)
	ana := budgetsFromRuntime(d.Analyze.Runtime)
	syn := budgetsFromRuntime(d.Synthesize.Runtime)
	return RunBudgets{
		MaxUnitBytes:         seg.MaxUnitBytes,
		SegmentChunkBytes:    seg.SegmentChunkBytes,
		SegmentContextMargin: seg.SegmentContextMargin,
		SegmentMaxTokens:     seg.SegmentMaxTokens,
		Analyze:              ana.Analyze,
		SynthesizeRangeBytes: syn.SynthesizeRangeBytes,
		SynthesizeMaxTokens:  syn.SynthesizeMaxTokens,
	}
}

// Run 执行完整导入管线：LoadState → NextAction → 执行一个动作 → 重新读取事实。
// 在自己的 goroutine 中跑；返回的事件通道由本函数关闭。
func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.CommitChapter == nil ||
		deps.Segment.Model == nil || deps.Analyze.Model == nil || deps.Synthesize.Model == nil {
		return nil, fmt.Errorf("deps 不完整")
	}
	if deps.Budgets == (RunBudgets{}) {
		deps.Budgets = budgetsFromDeps(deps)
	}
	// 导入流程日志独立成文件：一次导入的完整转录（事件、重试、完整错误链）不与
	// 引擎/TUI 日志混流，排查时只看这一个文件。创建失败须回显——面板会指引用户
	// 查看 logs/import.log，静默回退等于指向一个不存在的文件（Debug-First）。
	log, closeLog, logErr := logger.FileLogger(deps.Store.Dir(), "import.log")
	log.Info("imp 导入模型运行时",
		"segment_ctx", deps.Segment.Runtime.ContextTokens,
		"analyze_ctx", deps.Analyze.Runtime.ContextTokens,
		"synthesize_ctx", deps.Synthesize.Runtime.ContextTokens,
		"analyze_max_output", deps.Analyze.Runtime.MaxOutputTokens,
		"analyze_context_bytes", deps.Budgets.Analyze.ContextBytes)
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer closeLog()
		r := &runner{deps: deps, opts: opts, events: events, ws: OpenWorkspace(deps.Store.Dir()), log: log}
		if logErr != nil {
			r.emit(StageIngesting, 0, 0, fmt.Sprintf("导入日志文件创建失败（%v），本次转录改走默认日志", logErr), nil)
		}
		r.run(ctx)
	}()
	return events, nil
}

type runner struct {
	deps   Deps
	opts   Options
	events chan Event
	ws     *Workspace
	act    Action       // 当前执行动作，供失败工件标注阶段
	log    *slog.Logger // 导入专属日志（logs/import.log）；nil 时回退默认 logger
}

func (r *runner) emit(stage Stage, current, total int, msg string, err error) {
	r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err})
}

func (r *runner) send(ev Event) {
	r.logEvent(ev)
	// 终态与停点事件承载唯一的成败/须行动信号（确认预览、--story 提示丢了用户就不知道该做什么），
	// 必须可靠送达；只有中间进度事件才可在积压时丢弃。
	if ev.Stage == StageError || ev.Stage == StageDone ||
		ev.Stage == StageAwaitingConfirmation || ev.Stage == StageAwaitingStoryStatus {
		r.events <- ev
		return
	}
	select {
	case r.events <- ev:
	default: // 通道满时丢弃进度，绝不阻塞管线
	}
}

// logEvent 把每条进度事件转录进导入专属日志（<书根>/logs/import.log）：面板的重试行原地覆盖、
// 面板随 Esc 消失，日志是唯一可事后排查的完整流程记录（§14.1）。
func (r *runner) logEvent(ev Event) {
	log := r.log
	if log == nil {
		log = slog.Default()
	}
	args := []any{"stage", string(ev.Stage)}
	if ev.Total > 0 {
		args = append(args, "progress", fmt.Sprintf("%d/%d", ev.Current, ev.Total))
	}
	if ev.Err != nil {
		args = append(args, "err", ev.Err)
	}
	level := slog.LevelInfo
	switch {
	case ev.Stage == StageError:
		level = slog.LevelError // 失败终态是日志里最该被过滤出来的一条，不能落成 INFO
	case ev.Level == "warn":
		level = slog.LevelWarn
	}
	log.Log(context.Background(), level, ev.Message, args...)
}

func (r *runner) fail(msg string, err error) {
	r.saveFailure(err)
	r.emit(StageError, 0, 0, msg, err)
}

// saveFailure 统一把携带原始响应的失败落到 failures/（RFC §14.2 第三落点），
// segment/synthesize 等所有语义函数共用此兜底；分析截断打捞路径已就地写更精细的元数据。
// 无原始响应的失败（IO、取消、前置校验）没有可保存的模型输出，不写。
func (r *runner) saveFailure(err error) {
	var se *errSemantic
	var tr *errTruncated
	switch {
	case errors.As(err, &se):
		r.ws.writeFailure(FailureMeta{Stage: string(r.act), Detail: err.Error()}, se.Raw)
	case errors.As(err, &tr):
		r.ws.writeFailure(FailureMeta{Stage: string(r.act), Detail: err.Error(), StopReason: "length"}, tr.Raw)
	}
}

// facts 组合工作区事实与正式发布对账。
func (r *runner) facts() Facts {
	f := LoadState(r.ws)
	f.Published = isPublished(r.deps.Store, f.ExpectedChapters)
	return f
}

// profileFor 派生某档位的调用选项，并把请求退避/校验重问回显到对应阶段的事件流——
// 重试退避可静默累计 2 分钟以上，不回显用户会误以为卡死（§14.1）。
// Key 只给请求退避（带截止时刻）：它是同一次调用内的瞬态状态，UI 原地更新一行（"第 N/7 次"跳动）。
// 校验重问是跨调用的语义事件——切分逐块调用，各块独立重问，共用 Key 会让后一块覆盖前一块、
// 吃掉排查线索（实测面板只剩一条 unit_id 不断变化的行），因此各自成行保留历史。
func (r *runner) profileFor(c Caller, stage Stage) callProfile {
	prof := c.Runtime.profile()
	prof.log = r.log
	prof.notify = func(msg string, retryAt time.Time) {
		ev := Event{Time: time.Now(), Stage: stage, Message: msg, Level: "warn", RetryAt: retryAt}
		if !retryAt.IsZero() {
			ev.Key = "retry:" + string(stage)
		}
		r.send(ev)
	}
	prof.progress = func(current, total int, msg string) {
		r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg})
	}
	return prof
}

// applyGuidance 把本次 --guide 显式指导持久化为工作区语义输入（RFC §18.3）。
// 指导是 segmentation InputDigest 的输入之一：内容变化自然使旧切分及其全部下游失配并重做，
// 不写手工失效规则。工作区未建立时先跳过，ingest 后的下一轮循环写入。
func (r *runner) applyGuidance() error {
	g := strings.TrimSpace(r.opts.Guidance)
	if g == "" || !r.ws.Active() || r.ws.LoadGuidance() == g {
		return nil
	}
	// 发布开始后正式工件不可覆盖（§12.2）：此时重切必然在 publish 撞「拒绝覆盖」死墙，
	// 且撞墙前会先重付切分/分析/综合的全链模型调用——把失败提前到零成本处。
	// premise 是发布的第一笔写入，它非空即发布已开始（导入前置校验保证书原本为空）。
	if p, _ := r.deps.Store.Outline.LoadPremise(); p != "" {
		return fmt.Errorf("正式 Foundation 已开始发布，--guide 重切会与已发布内容冲突而被拒绝覆盖，不再接受切分指导")
	}
	return r.ws.writeAtomic(fileGuidance, []byte(g))
}

// checkSourceIdentity 拦截「工作区进行中却传入不同源文件」：ingest 只在无工作区时执行，
// 若不比对，/import B.txt 会静默从 A 的断点继续、把 A 发布完毕而 B 一个字节都没读（RFC §12.1/§18.2）。
// 同一文件重复传路径是常见习惯（/import 同路径恢复），按内容摘要比对而非拒绝所有路径。
func (r *runner) checkSourceIdentity() error {
	if r.opts.SourcePath == "" || !r.ws.Active() {
		return nil
	}
	m, err := r.ws.LoadManifest()
	if err != nil {
		return nil // 身份三件套不可读走 ingest 的损坏诊断，不在此重复报错
	}
	raw, err := os.ReadFile(r.opts.SourcePath)
	if err != nil {
		return fmt.Errorf("读取源文件 %s：%w", r.opts.SourcePath, err)
	}
	if Digest(raw) != m.RawSHA256 {
		return fmt.Errorf("已有 %q 的导入在进行中，本次源文件与其内容不同：请先完成或放弃旧导入（删除 meta/import/）再导入新书", m.SourceName)
	}
	return nil
}

func (r *runner) run(ctx context.Context) {
	if err := r.checkSourceIdentity(); err != nil {
		r.fail("校验源文件身份", err)
		return
	}
	var lastAct Action
	repeats := 0
	for {
		if ctx.Err() != nil {
			r.fail("用户取消", ctx.Err())
			return
		}
		if err := r.applyGuidance(); err != nil {
			r.fail("写入切分指导", err)
			return
		}
		act := NextAction(r.facts())
		r.act = act
		// 防御：执行型动作连续重复而事实无进展 = bug，转成明确错误而非死循环。
		if act == lastAct {
			if repeats++; repeats > 2 {
				r.fail("导入停滞", fmt.Errorf("动作 %q 反复执行但事实无进展", act))
				return
			}
		} else {
			repeats = 0
			lastAct = act
		}
		var err error
		switch act {
		case ActionIngest:
			err = r.ingest(ctx)
		case ActionSegment:
			err = r.segment(ctx)
		case ActionAwaitConfirmation:
			if !r.confirm() {
				return // 交互模式：等待用户确认，停在此处
			}
		case ActionAnalyze:
			err = r.analyze(ctx)
		case ActionSynthesize:
			err = r.synthesize(ctx)
		case ActionAwaitStoryResolution:
			if !r.resolveStoryStatus() {
				return // 无显式裁定：停在此处，等待 --story=open|closed
			}
		case ActionPublish:
			err = r.publish(ctx)
		case ActionDone:
			r.emit(StageDone, 0, 0, "导入完成，等待验收后续写", nil)
			return
		default:
			err = fmt.Errorf("未知动作 %q", act)
		}
		if err != nil {
			r.fail("导入失败", err)
			return
		}
	}
}

func (r *runner) ingest(ctx context.Context) error {
	// 走到 ingest 而目录已存在 = 身份三件套（manifest/source/intent）缺失或损坏：
	// createWorkspace 会以「已存在（无参数 /import 可恢复）」拒绝，无参数重跑又因
	// WorkspaceReady=false 回到这里要求源路径——两条提示互相打架，用户无路可走。
	if r.ws.Active() {
		return fmt.Errorf("meta/import/ 已存在但工作区身份不可用（manifest/source/intent 缺失或损坏），请人工确认后删除该目录再重新导入")
	}
	if err := checkImportPreconditions(r.deps.Store); err != nil {
		return err
	}
	if r.opts.SourcePath == "" {
		return fmt.Errorf("新导入需要源文件路径")
	}
	r.emit(StageIngesting, 0, 0, "读取、解码、归一化并快照源文件...", nil)
	_, m, err := Ingest(r.deps.Store.Dir(), r.opts.SourcePath, r.opts.intent())
	if err != nil {
		return err
	}
	r.emit(StageIngesting, 0, 0, fmt.Sprintf("源快照就绪：%s（编码 %s，%d 字节）", m.SourceName, m.Encoding, m.SizeBytes), nil)
	return nil
}

func (r *runner) segment(ctx context.Context) error {
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	units := buildSourceUnits(src, r.deps.Budgets.MaxUnitBytes)
	guidance := r.ws.LoadGuidance()
	r.emit(StageSegmenting, 0, 0, fmt.Sprintf("语义识别章节边界（%d 个坐标单元）...", len(units)), nil)
	digest := segmentInputDigest(Digest(src), guidance, segmentPromptVersion)
	// 块缓存身份额外绑定 MaxUnitBytes：unit 表由（归一化源, MaxUnitBytes）唯一确定，换模型
	// 档位改变 MaxUnitBytes 会重塑超长行的虚拟分片——ID 序列（L1.1…）与块端点可复现但字节
	// 范围已变，仅凭端点 ID 匹配会复用错位的旧边界（anchor 失配确定性失败或静默错切）。
	chunkIdentity := fmt.Sprintf("%s\x00units:%d", digest, r.deps.Budgets.MaxUnitBytes)
	seg, err := Segment(ctx, r.deps.Segment.Model, r.deps.Prompts.Segment, src, units, guidance,
		r.deps.Budgets.SegmentChunkBytes, r.deps.Budgets.SegmentContextMargin, r.deps.Budgets.SegmentMaxTokens,
		r.profileFor(r.deps.Segment, StageSegmenting), r.ws, chunkIdentity)
	if err != nil {
		return err
	}
	if err := writeArtifact(r.ws, fileSegmentation, digest, *seg); err != nil {
		return err
	}
	// 最终切分已落盘，块级缓存完成使命；清理失败无碍正确性（digest 仍一致），但要留痕。
	if cerr := r.ws.clearDir(dirSegmentChunks); cerr != nil {
		r.emit(StageSegmenting, 0, 0, fmt.Sprintf("块级缓存清理失败（不影响切分结果）：%v", cerr), nil)
	}
	r.emit(StageSegmenting, len(seg.Chapters), len(seg.Chapters),
		fmt.Sprintf("切分完成：%d 章、%d 个附属区域", len(seg.Chapters), len(seg.Matter)), nil)
	return nil
}

// confirm 处理切分确认。--yes 自动接受并写 confirmation 工件；否则展示预览并停止。
func (r *runner) confirm() bool {
	seg, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		r.fail("读取切分结果", err)
		return false
	}
	in, _ := r.ws.LoadIntent()
	accept := r.opts.AcceptSegmentation
	auto := r.opts.AutoConfirm || (in != nil && in.AutoConfirm)
	// 语义容错发生过（Notes 非空：空章吸收/起始兜底/重合去重）的切分不由 --yes 盲放行：
	// 结构被确定性改写过，必须人工核对——否则容错说明在 --yes 下无人看见，等于静默改写。
	// TUI 预览后按 y 走 AcceptSegmentation（看过预览的显式裁定），不受此限。
	blockedByNotes := auto && !accept && len(seg.Payload.Notes) > 0
	if blockedByNotes {
		auto = false
	}
	if !auto && !accept {
		msg := buildConfirmPreview(&seg.Payload)
		if blockedByNotes {
			msg += "  ! 存在切分容错说明，--yes 未自动放行，请人工核对\n"
		}
		r.emit(StageAwaitingConfirmation, len(seg.Payload.Chapters), len(seg.Payload.Chapters), msg, nil)
		return false
	}
	raw, err := r.ws.readBytes(fileSegmentation)
	if err != nil {
		r.fail("读取切分工件", err)
		return false
	}
	method, doneMsg := confirmMethodAuto, "已自动接受切分（--yes）"
	if accept {
		method, doneMsg = confirmMethodUser, "已确认切分（人工核对）"
	}
	conf := Confirmation{Method: method, Chapters: len(seg.Payload.Chapters)}
	if err := writeArtifact(r.ws, fileConfirmation, Digest(raw), conf); err != nil {
		r.fail("写确认工件", err)
		return false
	}
	r.emit(StageAwaitingConfirmation, len(seg.Payload.Chapters), len(seg.Payload.Chapters), doneMsg, nil)
	return true
}

// buildConfirmPreview 组装切分确认预览：章节数、附属区域、全部章节标题与 uncertain 标记（RFC §8.4）。
// 全量列出，面板 viewport 可滚动查看；不设截断上限。
func buildConfirmPreview(seg *Segmentation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "已切分 %d 章", len(seg.Chapters))
	if len(seg.Matter) > 0 {
		fmt.Fprintf(&b, "、%d 个附属区域", len(seg.Matter))
	}
	if len(seg.Uncertain) > 0 {
		fmt.Fprintf(&b, "（%d 章存疑）", len(seg.Uncertain))
	}
	b.WriteString("，请核对：\n")
	uncertain := make(map[int]bool, len(seg.Uncertain))
	for _, n := range seg.Uncertain {
		uncertain[n] = true
	}
	for _, c := range seg.Chapters {
		fmt.Fprintf(&b, "  第%d章 %s", c.Number, c.Title)
		if uncertain[c.Number] {
			b.WriteString("  [存疑]")
		}
		b.WriteByte('\n')
	}
	for _, mt := range seg.Matter {
		fmt.Fprintf(&b, "  [%s] %s\n", mt.Kind, mt.Title)
	}
	// 切分期的容错说明（如空正文占位标题并入前段）必须呈现在人工停点上，否则吸收行为变成静默改写。
	for _, n := range seg.Notes {
		fmt.Fprintf(&b, "  ! %s\n", n)
	}
	// 操作提示（y 确认 / --guide 重切 / Esc）由 TUI 暂停块统一渲染，此处只留事实，避免双份文案漂移。
	return b.String()
}

func (r *runner) analyze(ctx context.Context) error {
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	seg := &segArt.Payload
	total := len(seg.Chapters)
	// 逐章 digest 只绑定本章正文，不含批次上下文与前序 ledger。若第 K 章因缺失/失配需重分析，
	// 其后仍留着 digest 恰好匹配的旧工件会带着已失效的 ledger 被复用。开分析前清理越过新鲜前缀的尾部，
	// 强制"重分析某章即失效其后全部分析"，之后前向分析不再产生陈旧尾部（RFC §9.6 / #4a）。
	if err := discardAnalysesAfter(r.ws, analyzedChapters(r.ws, seg, src, segArt.InputDigest, analyzePromptVersion), total); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := analyzedChapters(r.ws, seg, src, segArt.InputDigest, analyzePromptVersion)
		if start >= total {
			break
		}
		r.emit(StageAnalyzing, start, total, fmt.Sprintf("分析第 %d 章起的连续批次...", start+1), nil)
		done, err := AnalyzeNext(ctx, r.deps.Analyze.Model, r.deps.Prompts.Analyze, r.ws, src, seg, segArt.InputDigest, analyzePromptVersion, r.deps.Budgets.Analyze, r.profileFor(r.deps.Analyze, StageAnalyzing))
		if err != nil {
			return err
		}
		if done == 0 {
			break
		}
	}
	r.emit(StageAnalyzing, total, total, "逐章事实提取完成", nil)
	return nil
}

func (r *runner) synthesize(ctx context.Context) error {
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	total := len(segArt.Payload.Chapters)
	facts := loadPriorFacts(r.ws, total)
	if len(facts) != total {
		return fmt.Errorf("逐章分析不完整：%d/%d", len(facts), total)
	}
	r.emit(StageSynthesizing, 0, total, "分层归纳全书语义...", nil)
	syn, err := Synthesize(ctx, r.deps.Synthesize.Model, r.deps.Prompts.Synthesize, r.deps.Prompts.Range, r.ws, facts,
		r.deps.Budgets.SynthesizeRangeBytes, r.deps.Budgets.SynthesizeMaxTokens, r.profileFor(r.deps.Synthesize, StageSynthesizing))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.ws, fileSynthesis, synthesisInputDigest(facts), *syn); err != nil {
		return err
	}
	r.emit(StageSynthesizing, total, total, fmt.Sprintf("综合完成：%d 卷、故事状态 %s", len(syn.Structure), syn.StoryStatus), nil)
	return nil
}

func (r *runner) publish(ctx context.Context) error {
	synArt, err := readArtifact[BookSynthesis](r.ws, fileSynthesis)
	if err != nil {
		return err
	}
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	seg := &segArt.Payload
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	total := len(seg.Chapters)
	facts := loadPriorFacts(r.ws, total)
	if len(facts) != total {
		return fmt.Errorf("发布前分析不完整：%d/%d", len(facts), total)
	}
	closed, err := r.resolveStory(&synArt.Payload)
	if err != nil {
		return err
	}
	manifest, err := r.ws.LoadManifest()
	if err != nil {
		return err
	}
	f, err := AssembleFoundation(&synArt.Payload, facts, closed, manifest.SourceName)
	if err != nil {
		return err
	}
	r.emit(StageValidating, 0, total, "Foundation 组装校验通过", nil)

	r.emit(StagePublishing, 0, total, "发布正式 Foundation...", nil)
	if err := publishFoundation(r.deps.Store, f); err != nil {
		return err
	}
	// 导入完成 Hold 必须早于任何章节提交即持久化：若在"最后一章提交"与"设置 Hold"之间崩溃，
	// 重启后 isPublished=true → 导入判为完成却漏设 Hold，Engine 会误把导入书当普通停机续写。
	// 置于 publishFoundation（已初始化 RunMeta）之后、章节提交之前，彻底关闭该窗口；重跑发布时幂等
	// 重设（--continue 不设 Hold，交由自动接力，RFC §12.4）。
	if err := r.setCompletionHold(); err != nil {
		return fmt.Errorf("建立导入完成 Hold：%w", err)
	}
	for i, c := range seg.Chapters {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(StagePublishing, c.Number, total, fmt.Sprintf("发布第 %d/%d 章：%s", c.Number, total, c.Title), nil)
		if err := publishChapter(ctx, r.deps.Store, r.deps.CommitChapter, c.Number, seg.Content(src, i), facts[i]); err != nil {
			return err
		}
	}
	return nil
}

// storyChoice 返回 uncertain 状态的有效裁定：优先绑定当前 synthesis 的已落盘裁定，其次本次 opts，再次原始 intent。
// 已落盘裁定必须校验 InputDigest 与当前 synthesis 一致——重新综合后旧裁定失效，不能把旧 open/closed 静默
// 套到新结果上，否则用户不会被重新征询（RFC §10.4）。显式 --story（intent）是用户跨综合的常驻指令，可保留。
func (r *runner) storyChoice() string {
	if raw, err := r.ws.readBytes(fileSynthesis); err == nil {
		if art, aerr := readArtifact[StoryResolution](r.ws, fileStoryResolve); aerr == nil && art.InputDigest == Digest(raw) {
			return art.Payload.Choice
		}
	}
	if r.opts.StoryResolution != "" {
		return r.opts.StoryResolution
	}
	if in, _ := r.ws.LoadIntent(); in != nil {
		return in.StoryResolution
	}
	return ""
}

// resolveStoryStatus 在 uncertain 且已有显式裁定时落盘 story-resolution.json（绑定当前 synthesis），
// 使下游 NextAction 自然放行；无裁定则展示等待并停止。
func (r *runner) resolveStoryStatus() bool {
	choice := r.storyChoice()
	if choice != storyOpen && choice != storyClosed {
		r.emit(StageAwaitingStoryStatus, 0, 0, "综合判定故事状态为 uncertain，请用 --story=open|closed 明确后重试", nil)
		return false
	}
	raw, err := r.ws.readBytes(fileSynthesis)
	if err != nil {
		r.fail("读取综合结果", err)
		return false
	}
	if err := writeArtifact(r.ws, fileStoryResolve, Digest(raw), StoryResolution{Choice: choice}); err != nil {
		r.fail("落盘故事状态裁定", err)
		return false
	}
	return true
}

// resolveStory 依据综合结果与用户显式裁定给出故事收束状态（RFC §10.4）。
func (r *runner) resolveStory(syn *BookSynthesis) (bool, error) {
	switch syn.StoryStatus {
	case storyClosed:
		return true, nil
	case storyOpen:
		return false, nil
	case storyUncertain:
		switch r.storyChoice() {
		case storyClosed:
			return true, nil
		case storyOpen:
			return false, nil
		default:
			return false, fmt.Errorf("故事状态 uncertain，需 --story=open|closed")
		}
	default:
		return false, fmt.Errorf("未知 story_status：%q", syn.StoryStatus)
	}
}

// setCompletionHold 设置一次导入完成 Hold；仅 --continue 才跳过（RFC §12.4）。
// 错误必须传播——Hold 是"导入后不误续写"的唯一保障，静默失败等于保护失效。
func (r *runner) setCompletionHold() error {
	in, _ := r.ws.LoadIntent()
	if r.opts.ContinueAfter || (in != nil && in.ContinueAfterImport) {
		return nil
	}
	return r.deps.Store.RunMeta.SetAdvanceHold(domain.AdvanceHold{
		After:  domain.AdvanceHoldAtBoundary,
		Reason: "外部小说导入完成，等待验收后续写",
	})
}
