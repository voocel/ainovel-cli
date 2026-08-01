package rip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/logger"
)

// prompt 版本纳入各阶段 InputDigest；升级 prompt 契约时递增以自然失效下游工件。
const (
	boundPromptVersion     = "rip-bound-v1"
	previewPromptVersion   = "rip-preview-v1"
	summaryPromptVersion   = "rip-summary-v1"
	aggregatePromptVersion = "rip-aggregate-v1"
	profilePromptVersion   = "rip-profile-v1"
	reportPromptVersion    = "rip-report-v1"
	stylePromptVersion     = "rip-style-v1"
)

// 人类可读投影的文件名。.json 是数据真源，.md 是投影。
const (
	filePreviewMD    = "快速预览.md"
	fileReportMD     = "拆文报告.md"
	filePlotPointsMD = "情节节点.md"
	fileTechniquesMD = "写作手法.md"
	filePacingMD     = "节奏.md"
	fileEmotionMD    = "情绪模块.md"
	fileStyleMD      = "文风.md"
)

// RunBudgets 是各语义函数的输入/输出预算，默认由各档位模型能力推导。
type RunBudgets struct {
	MaxUnitBytes       int
	BoundChunkBytes    int
	BoundContextMargin int
	BoundMaxTokens     int
	Summary            SummaryBudget
	PreviewMaxTokens   int
	AggregateMaxTokens int
	ReportMaxTokens    int
}

// DefaultRunBudgets 返回保守默认预算，用于模型能力未知（探测失败）时兜底。
func DefaultRunBudgets() RunBudgets {
	return RunBudgets{
		MaxUnitBytes:       8000,
		BoundChunkBytes:    24000,
		BoundContextMargin: 20,
		BoundMaxTokens:     8192,
		Summary:            SummaryBudget{ContextBytes: 24000, MaxOutputTokens: 8000, PerChapterOutput: 900, PromptOverhead: 2000},
		PreviewMaxTokens:   8192,
		AggregateMaxTokens: 8192,
		ReportMaxTokens:    8192,
	}
}

// budgetsFromRuntime 从模型真实 context/completion 上限派生预算：
// 换更强模型自动扩大批次、减少调用次数；能力未知时回退保守默认。
func budgetsFromRuntime(rt ModelRuntime) RunBudgets {
	if rt.ContextTokens <= 0 || rt.MaxOutputTokens <= 0 {
		return DefaultRunBudgets()
	}
	const bytesPerToken = 3 // 中文 UTF-8 保守换算：token→字节（偏低估容量更安全）
	out := rt.MaxOutputTokens
	reserve := rt.ContextTokens / 10 // 推理/系统预留
	inTokens := rt.ContextTokens - out - reserve
	if inTokens < 2000 {
		inTokens = 2000
	}
	inBytes := inTokens * bytesPerToken
	return RunBudgets{
		MaxUnitBytes:       min(inBytes/2, 32000),
		BoundChunkBytes:    inBytes,
		BoundContextMargin: 20,
		BoundMaxTokens:     out,
		Summary: SummaryBudget{
			ContextBytes:     inBytes,
			MaxOutputTokens:  out,
			PerChapterOutput: 900,
			PromptOverhead:   2000,
		},
		PreviewMaxTokens:   out,
		AggregateMaxTokens: out,
		ReportMaxTokens:    out,
	}
}

// budgetsFromDeps 按各语义函数自己的档位能力派生预算：
// 廉价档位的小窗口只约束它自己的阶段，不拖累其它阶段。
func budgetsFromDeps(d Deps) RunBudgets {
	bound := budgetsFromRuntime(d.Bound.Runtime)
	summary := budgetsFromRuntime(d.Summary.Runtime)
	agg := budgetsFromRuntime(d.Aggregate.Runtime)
	rep := budgetsFromRuntime(d.Report.Runtime)
	return RunBudgets{
		MaxUnitBytes:       bound.MaxUnitBytes,
		BoundChunkBytes:    bound.BoundChunkBytes,
		BoundContextMargin: bound.BoundContextMargin,
		BoundMaxTokens:     bound.BoundMaxTokens,
		Summary:            summary.Summary,
		PreviewMaxTokens:   summary.PreviewMaxTokens,
		AggregateMaxTokens: agg.AggregateMaxTokens,
		ReportMaxTokens:    rep.ReportMaxTokens,
	}
}

// LibraryPath 给出某本书在拆文库中的目录路径，供 Host/UI 回显与恢复检测。
func LibraryPath(libraryDir, bookName string) (string, error) {
	root, err := resolveLibraryDir(libraryDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sanitizeBookName(bookName)), nil
}

// resolveTarget 解析本次运行的拆文库目录：书名显式优先，否则取源文件名。
func resolveTarget(opts Options) (root, bookName, dir string, err error) {
	root, err = resolveLibraryDir(opts.LibraryDir)
	if err != nil {
		return "", "", "", fmt.Errorf("解析拆文库根目录：%w", err)
	}
	name := strings.TrimSpace(opts.BookName)
	if name == "" {
		if strings.TrimSpace(opts.SourcePath) == "" {
			return "", "", "", fmt.Errorf("新拆解需要源文件路径，恢复需要书名（--book）")
		}
		name = deriveBookName(opts.SourcePath)
	}
	name = sanitizeBookName(name)
	return root, name, filepath.Join(root, name), nil
}

// Run 执行完整拆解管线：LoadState → NextAction → 执行一个动作 → 重新读取事实。
// 在自己的 goroutine 中跑；返回的事件通道由本函数关闭。
func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Bound.Model == nil || deps.Summary.Model == nil ||
		deps.Aggregate.Model == nil || deps.Report.Model == nil {
		return nil, fmt.Errorf("deps 不完整：四个语义档位都需要模型")
	}
	if deps.Budgets == (RunBudgets{}) {
		deps.Budgets = budgetsFromDeps(deps)
	}
	root, bookName, dir, err := resolveTarget(opts)
	if err != nil {
		return nil, err
	}
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		r := &runner{
			deps: deps, opts: opts, events: events,
			l: OpenLibrary(dir), root: root, bookName: bookName,
		}
		defer r.closeLogFile()
		// 日志落在库内（logs/rip.log），因此只有库已发布时才能打开——提前建目录会让
		// createLibrary 把它误判成「拆文库已存在」。ingest 之后再补开。
		r.openLog()
		r.run(ctx)
	}()
	return events, nil
}

type runner struct {
	deps     Deps
	opts     Options
	events   chan Event
	l        *Library
	root     string // 拆文库根目录
	bookName string
	act      Action // 当前执行动作，供失败工件标注阶段

	log       *slog.Logger
	closeLog  func()
	logOpened bool
}

// openLog 在拆文库已发布后打开独立转录（logs/rip.log）。
// 打开失败必须回显：面板会指引用户去看这个文件，静默回退等于指向不存在的路径。
func (r *runner) openLog() {
	if r.logOpened || !r.l.Active() {
		return
	}
	r.logOpened = true
	log, closeLog, err := logger.FileLogger(r.l.Dir(), "rip.log")
	r.log, r.closeLog = log, closeLog
	if err != nil {
		r.emit(StageIngesting, 0, 0, fmt.Sprintf("拆文日志文件创建失败（%v），本次转录改走默认日志", err), nil)
		return
	}
	r.log.Info("rip 拆解模型运行时",
		"book", r.bookName,
		"bound_ctx", r.deps.Bound.Runtime.ContextTokens,
		"summary_ctx", r.deps.Summary.Runtime.ContextTokens,
		"aggregate_ctx", r.deps.Aggregate.Runtime.ContextTokens,
		"report_ctx", r.deps.Report.Runtime.ContextTokens,
		"summary_context_bytes", r.deps.Budgets.Summary.ContextBytes)
}

func (r *runner) closeLogFile() {
	if r.closeLog != nil {
		r.closeLog()
	}
}

func (r *runner) emit(stage Stage, current, total int, msg string, err error) {
	r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err})
}

func (r *runner) warn(stage Stage, format string, args ...any) {
	r.send(Event{Time: time.Now(), Stage: stage, Message: fmt.Sprintf(format, args...), Level: "warn"})
}

func (r *runner) send(ev Event) {
	r.logEvent(ev)
	// 终态与停靠点事件承载唯一的成败/须行动信号（放行提示丢了用户就不知道该做什么），
	// 必须可靠送达；只有中间进度事件才可在积压时丢弃。
	if ev.Stage == StageError || ev.Stage == StageDone ||
		ev.Stage == StageAwaitingForm || ev.Stage == StageAwaitingPreview {
		r.events <- ev
		return
	}
	select {
	case r.events <- ev:
	default: // 通道满时丢弃进度，绝不阻塞管线
	}
}

// logEvent 把每条事件转录进 logs/rip.log：面板的重试行原地覆盖、面板随 Esc 消失，
// 日志是唯一可事后排查的完整流程记录。
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
		level = slog.LevelError
	case ev.Level == "warn":
		level = slog.LevelWarn
	}
	log.Log(context.Background(), level, ev.Message, args...)
}

func (r *runner) fail(msg string, err error) {
	r.saveFailure(err)
	r.emit(StageError, 0, 0, msg, err)
}

// saveFailure 把携带原始响应的失败落到 failures/。
// 原始响应可能含他人作品正文，只留在用户自己的拆文库里。
func (r *runner) saveFailure(err error) {
	if !r.l.Active() {
		return
	}
	meta, raw, ok := failureOf(string(r.act), err)
	if !ok {
		return
	}
	r.l.writeFailure(meta, raw)
}

// failureOf 从错误链里取出可保存的模型原始响应；无原始响应（IO、取消、前置校验）不写。
func failureOf(stage string, err error) (FailureMeta, string, bool) {
	var se *errSemantic
	var tr *errTruncated
	switch {
	case errors.As(err, &se):
		return FailureMeta{Stage: stage, Detail: err.Error()}, se.Raw, true
	case errors.As(err, &tr):
		return FailureMeta{Stage: stage, Detail: err.Error(), StopReason: "length"}, tr.Raw, true
	}
	return FailureMeta{}, "", false
}

// profileFor 派生某档位的调用选项，并把请求退避/校验重问回显到对应阶段的事件流——
// 重试退避可静默累计数分钟，不回显用户会误以为卡死。
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

// applyGuidance 把本次 --guide 持久化为库内语义输入。
// 指导是边界表 InputDigest 的输入之一：内容变化自然使旧边界及其全部下游失配并重做。
func (r *runner) applyGuidance() error {
	g := strings.TrimSpace(r.opts.Guidance)
	if g == "" || !r.l.Active() {
		return nil
	}
	existing, err := r.l.LoadGuidance()
	if err != nil {
		return fmt.Errorf("读取切分指导: %w", err)
	}
	if existing == g {
		return nil
	}
	return r.l.writeAtomic(fileGuidance, []byte(g))
}

// applyIntentOptions 把跨停靠点仍然有效的选择合并回 intent。Form 不是一次性按钮状态，
// 灰区一旦选成长篇，后续黄金三章放行必须继续沿用该裁定。
func (r *runner) applyIntentOptions() error {
	if !r.l.Active() || (r.opts.Form == "" && !r.opts.AutoConfirm) {
		return nil
	}
	in, err := r.l.LoadIntent()
	if err != nil {
		return fmt.Errorf("读取拆文 intent: %w", err)
	}
	changed := false
	if r.opts.Form != "" && in.Form != r.opts.Form {
		in.Form = r.opts.Form
		changed = true
	}
	if r.opts.AutoConfirm && !in.AutoConfirm {
		in.AutoConfirm = true
		changed = true
	}
	if !changed {
		return nil
	}
	return r.l.writeJSON(fileIntent, in)
}

// checkSourceIdentity 拦截「拆文库进行中却传入不同源文件」：ingest 只在无库时执行，
// 若不比对，/rip B.txt 会静默从 A 的断点继续、把 A 拆完而 B 一个字节都没读。
func (r *runner) checkSourceIdentity() error {
	if r.opts.SourcePath == "" || !r.l.Active() {
		return nil
	}
	m, err := r.l.LoadManifest()
	if err != nil {
		return nil // 身份三件套不可读走 ingest 的损坏诊断，不在此重复报错
	}
	raw, err := os.ReadFile(r.opts.SourcePath)
	if err != nil {
		return fmt.Errorf("读取源文件 %s：%w", r.opts.SourcePath, err)
	}
	if Digest(raw) != m.RawSHA256 {
		return fmt.Errorf("拆文库 %q 已存在且原文与本次源文件不同：请换 --book 指定新书名，或删除 %s 后重拆",
			m.BookName, r.l.Dir())
	}
	return nil
}

func (r *runner) run(ctx context.Context) {
	if err := r.checkSourceIdentity(); err != nil {
		r.fail("校验源文件身份", err)
		return
	}
	if err := r.applyIntentOptions(); err != nil {
		r.fail("保存拆文选择", err)
		return
	}
	if r.opts.RetryFailed && r.l.Active() {
		if n := r.l.clearAllChapterFailures(); n > 0 {
			r.emit(StageSummarizing, 0, 0, fmt.Sprintf("已清除 %d 个失败章标记，重新拆解这些章节", n), nil)
		}
	}
	var previous *Facts
	for {
		if ctx.Err() != nil {
			r.fail("用户取消", ctx.Err())
			return
		}
		if err := r.applyGuidance(); err != nil {
			r.fail("写入切分指导", err)
			return
		}
		facts, err := LoadState(r.l, r.opts)
		if err != nil {
			r.fail("读取拆解状态", err)
			return
		}
		if previous != nil && facts == *previous {
			r.fail("拆解停滞", fmt.Errorf("动作执行后事实没有变化，下一动作仍为 %q", NextAction(facts)))
			return
		}
		snapshot := facts
		previous = &snapshot
		act := NextAction(facts)
		r.act = act
		err = nil
		switch act {
		case ActionIngest:
			err = r.ingest(ctx)
		case ActionBound:
			err = r.bound(ctx)
		case ActionAwaitFormChoice:
			r.awaitForm(facts)
			return // 停靠点：等待 --form=long|short
		case ActionPreview:
			err = r.preview(ctx)
		case ActionAwaitPreviewAck:
			if !r.awaitPreview() {
				return // 停靠点：等待放行
			}
		case ActionSummarize:
			err = r.summarize(ctx)
		case ActionAggregate:
			err = r.aggregate(ctx)
		case ActionProfile:
			err = r.profile(ctx)
		case ActionReport:
			err = r.report(ctx)
		case ActionStyle:
			err = r.style(ctx)
		case ActionDone:
			r.done(facts)
			return
		default:
			err = fmt.Errorf("未知动作 %q", act)
		}
		if err != nil {
			r.fail("拆解失败", err)
			return
		}
	}
}

func (r *runner) ingest(ctx context.Context) error {
	// 走到 ingest 而目录已存在 = 身份三件套（manifest/intent/原文快照）缺失或损坏：
	// createLibrary 会以「已存在」拒绝，无参数重跑又因 LibraryReady=false 回到这里要求源路径，
	// 两条提示互相打架，用户无路可走。这里直接说清该怎么办。
	if r.l.Active() {
		return fmt.Errorf("%s 已存在但拆文库身份不可用（manifest/intent/原文快照缺失或损坏），请人工确认后删除该目录再重拆", r.l.Dir())
	}
	if strings.TrimSpace(r.opts.SourcePath) == "" {
		return fmt.Errorf("新拆解需要源文件路径")
	}
	r.emit(StageIngesting, 0, 0, "读取、解码、归一化并备份原文...", nil)
	_, m, err := Ingest(r.root, r.bookName, r.opts.SourcePath, r.opts.intent())
	if err != nil {
		return err
	}
	r.openLog()
	r.emit(StageIngesting, 0, 0, fmt.Sprintf("原文备份就绪：%s（编码 %s，%d 字，%s）：%s",
		m.SourceName, m.Encoding, m.Runes, formLabel(resolveForm(m.Runes)), r.l.Dir()), nil)
	return nil
}

func (r *runner) bound(ctx context.Context) error {
	src, err := r.l.LoadSource()
	if err != nil {
		return err
	}
	units := buildSourceUnits(src, r.deps.Budgets.MaxUnitBytes)
	guidance, err := r.l.LoadGuidance()
	if err != nil {
		return fmt.Errorf("读取切分指导: %w", err)
	}
	r.emit(StageBounding, 0, 0, fmt.Sprintf("识别章节边界（%d 个坐标单元）...", len(units)), nil)
	digest := boundInputDigest(Digest(src), guidance, boundPromptVersion)
	// 块缓存身份额外绑定 MaxUnitBytes：unit 表由（归一化原文, MaxUnitBytes）唯一确定，
	// 换档位改变 MaxUnitBytes 会重塑超长行的虚拟分片，仅凭端点 ID 匹配会复用错位的旧边界。
	chunkIdentity := fmt.Sprintf("%s\x00units:%d", digest, r.deps.Budgets.MaxUnitBytes)
	b, err := Bound(ctx, r.deps.Bound.Model, r.deps.Prompts.Bound, src, units, guidance,
		r.deps.Budgets.BoundChunkBytes, r.deps.Budgets.BoundContextMargin, r.deps.Budgets.BoundMaxTokens,
		r.profileFor(r.deps.Bound, StageBounding), r.l, chunkIdentity)
	if err != nil {
		return err
	}
	if err := writeArtifact(r.l, fileBoundaries, digest, *b); err != nil {
		return err
	}
	// 最终边界表已落盘，块级缓存完成使命；清理失败无碍正确性（digest 仍一致），但要留痕。
	if cerr := r.l.clearDir(dirBoundChunks); cerr != nil {
		r.emit(StageBounding, 0, 0, fmt.Sprintf("块级缓存清理失败（不影响边界结果）：%v", cerr), nil)
	}
	r.emit(StageBounding, len(b.Chapters), len(b.Chapters),
		fmt.Sprintf("边界识别完成：%d 章、%d 个附属区域", len(b.Chapters), len(b.Matter)), nil)
	return nil
}

// awaitForm 在灰区字数处停下等待用户裁定。字数是客观事实，长短篇管线差别是真实的，
// 不替用户猜——猜错要么白付全书逐章摘要，要么丢掉该有的那一层。
func (r *runner) awaitForm(f Facts) {
	// manifest 走到这里必定可读（LoadState 已读过它才判出灰区），读失败是真异常，不掩盖。
	m, err := r.l.LoadManifest()
	if err != nil {
		r.fail("读取拆文库身份", err)
		return
	}
	r.emit(StageAwaitingForm, f.ExpectedChapters, f.ExpectedChapters,
		fmt.Sprintf("全书 %d 字、%d 章，介于短篇（<%d 字）与长篇（>%d 字）之间，请用 --form=long|short 裁定后重试",
			m.Runes, f.ExpectedChapters, shortFormMaxRunes, longFormMinRunes), nil)
}

func (r *runner) preview(ctx context.Context) error {
	src, err := r.l.LoadSource()
	if err != nil {
		return err
	}
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return err
	}
	boundRaw, err := r.l.readBytes(fileBoundaries)
	if err != nil {
		return fmt.Errorf("读取边界工件原文: %w", err)
	}
	b := &boundArt.Payload
	want := min(previewChapters, len(b.Chapters))
	r.emit(StagePreviewing, 0, want, fmt.Sprintf("深读开篇黄金 %d 章...", want), nil)
	p, err := PreviewGolden(ctx, r.deps.Summary.Model, r.deps.Prompts.Preview, src, b,
		r.deps.Budgets.PreviewMaxTokens, r.profileFor(r.deps.Summary, StagePreviewing))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.l, filePreview, previewInputDigest(boundRaw), *p); err != nil {
		return err
	}
	m, err := r.l.LoadManifest()
	if err != nil {
		return err
	}
	if err := r.l.WriteMarkdown(filePreviewMD, renderPreviewMarkdown(m, b, p)); err != nil {
		return fmt.Errorf("写 %s：%w", filePreviewMD, err)
	}
	r.emit(StagePreviewing, want, want, "黄金三章深读完成", nil)
	return nil
}

// awaitPreview 处理预览放行。返回 false 表示停在停靠点等待用户。
// 放行是一次性授权（AcceptPreview）或落盘的盲授权（--yes），二者的判定都在 LoadState 里；
// 走到这里说明两者都没给，只需把停靠点事实呈现出来。
func (r *runner) awaitPreview() bool {
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		r.fail("读取边界工件", err)
		return false
	}
	prevArt, err := readArtifact[Preview](r.l, filePreview)
	if err != nil {
		r.fail("读取快速预览", err)
		return false
	}
	b := &boundArt.Payload
	msg := buildPreviewPause(b, &prevArt.Payload, filepath.Join(r.l.Dir(), filePreviewMD))
	// 盲授权（--yes）被容错说明拦下时要说清原因，否则用户以为 --yes 没生效。
	in, ierr := r.l.LoadIntent()
	auto := r.opts.AutoConfirm || (ierr == nil && in != nil && in.AutoConfirm)
	if auto && len(b.Notes) > 0 {
		msg += "  ! 边界表存在容错说明，--yes 未自动放行，请人工核对\n"
	}
	r.emit(StageAwaitingPreview, len(b.Chapters), len(b.Chapters), msg, nil)
	return false
}

// summarize 逐批推进逐章摘要，直到全书章节都有摘要或被记为持久失败。
// 失败隔离：多章批失败先降为单章定位，单章仍失败则记 failures/ 并越过它继续——
// 一章拆不出来不该让整本书的拆解卡死。
func (r *runner) summarize(ctx context.Context) error {
	src, err := r.l.LoadSource()
	if err != nil {
		return err
	}
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return err
	}
	b := &boundArt.Payload
	total := len(b.Chapters)
	failedSet := failedSetOf(r.l, total, b, src, boundArt.InputDigest)
	// 逐章 digest 只绑定本章正文。若第 K 章需重摘，其后 digest 恰好匹配的旧工件会被当作
	// 新鲜前缀复用，聚合将消费新旧混拼的事实。开摘前清理越过新鲜前缀的尾部。
	keep := summarizedChapters(r.l, b, src, boundArt.InputDigest, summaryPromptVersion, failedSet)
	if err := discardSummariesAfter(r.l, keep, total); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failedSet = failedSetOf(r.l, total, b, src, boundArt.InputDigest)
		start := summarizedChapters(r.l, b, src, boundArt.InputDigest, summaryPromptVersion, failedSet)
		if start >= total {
			break
		}
		end := capBatchAtFailed(planBatch(b.Chapters, start, r.deps.Budgets.Summary), start, failedSet)
		r.emit(StageSummarizing, start, total, fmt.Sprintf("拆解第 %d 章起的连续批次（共 %d 章）...", start+1, total), nil)
		n, err := r.summarizeBatch(ctx, src, b, boundArt.InputDigest, start, end)
		if err != nil {
			return err
		}
		if n == 0 {
			fresh := failedSetOf(r.l, total, b, src, boundArt.InputDigest)
			if !fresh[start+1] {
				return fmt.Errorf("第 %d 章批次既未产出摘要也未记录失败，停止以避免空转", start+1)
			}
		}
	}
	done := total - len(r.failedChaptersFor(b, src, boundArt.InputDigest))
	r.emit(StageSummarizing, total, total, fmt.Sprintf("逐章拆解完成：%d/%d 章", done, total), nil)
	return nil
}

// summarizeBatch 跑一个批次，处理失败降级。返回本次落盘的章数；
// 单章持久失败已记录时返回 0 且不报错（由调用方继续推进）。
func (r *runner) summarizeBatch(ctx context.Context, src []byte, b *Boundaries, boundIdentity string, start, end int) (int, error) {
	prof := r.profileFor(r.deps.Summary, StageSummarizing)
	n, err := SummarizeNext(ctx, r.deps.Summary.Model, r.deps.Prompts.Summary, r.l, src, b,
		boundIdentity, summaryPromptVersion, start, end, r.deps.Budgets.Summary, prof)
	if err == nil {
		return n, nil
	}
	if ctx.Err() != nil {
		return 0, err
	}
	// 多章批失败无法归因到具体某章：降为单章重试，把损失限制在真正坏的那一章。
	if end-start > 1 {
		r.warn(StageSummarizing, "第 %d-%d 章批次失败（%s），改为单章重试", start+1, end, briefErr(err))
		n, err = SummarizeNext(ctx, r.deps.Summary.Model, r.deps.Prompts.Summary, r.l, src, b,
			boundIdentity, summaryPromptVersion, start, start+1, r.deps.Budgets.Summary, prof)
		if err == nil {
			return n, nil
		}
		if ctx.Err() != nil {
			return 0, err
		}
	}
	// 单章仍失败：记录持久失败，管线越过它继续，终态报告降级。
	meta, raw, ok := failureOf("summarize", err)
	if !ok {
		meta = FailureMeta{Stage: "summarize", Detail: err.Error()}
	}
	meta.InputDigest = chapterInputDigest(boundIdentity, summaryPromptVersion, b, src, start)
	r.l.writeChapterFailure(start+1, meta, raw)
	r.warn(StageSummarizing, "第 %d 章拆解失败并记入 failures/，跳过继续：%s", start+1, briefErr(err))
	return 0, nil
}

// failedSetOf 从 failures/ 重算持久失败章集合。
func failedSetOf(l *Library, total int, b *Boundaries, src []byte, boundIdentity string) map[int]bool {
	failed := l.failedChaptersForInput(total, func(chapter int) string {
		return chapterInputDigest(boundIdentity, summaryPromptVersion, b, src, chapter-1)
	})
	set := make(map[int]bool, len(failed))
	for _, n := range failed {
		set[n] = true
	}
	return set
}

func (r *runner) failedChaptersFor(b *Boundaries, src []byte, boundIdentity string) []int {
	return r.l.failedChaptersForInput(len(b.Chapters), func(chapter int) string {
		return chapterInputDigest(boundIdentity, summaryPromptVersion, b, src, chapter-1)
	})
}

// capBatchAtFailed 把批次终点缩到第一个持久失败章之前：
// 否则一个已知坏章会把整批拖下水，成功的邻章跟着被判失败。
func capBatchAtFailed(end, start int, failed map[int]bool) int {
	for i := start + 1; i < end; i++ {
		if failed[i+1] {
			return i
		}
	}
	return end
}

func (r *runner) aggregate(ctx context.Context) error {
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return err
	}
	total := len(boundArt.Payload.Chapters)
	summaries := loadSummaries(r.l, total)
	r.emit(StageAggregating, 0, total, "归纳剧情单元、节奏与情绪模块...", nil)
	agg, err := AggregateBook(ctx, r.deps.Aggregate.Model, r.deps.Prompts.Aggregate, summaries, total,
		r.deps.Budgets.AggregateMaxTokens, r.profileFor(r.deps.Aggregate, StageAggregating))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.l, fileAggregate, aggregateInputDigest(summaries), *agg); err != nil {
		return err
	}
	r.emit(StageAggregating, total, total,
		fmt.Sprintf("聚合完成：%d 个剧情单元、%d 条节奏观察、%d 个情绪模块",
			len(agg.PlotUnits), len(agg.Pacing), len(agg.EmotionModules)), nil)
	return nil
}

func (r *runner) profile(ctx context.Context) error {
	src, err := r.l.LoadSource()
	if err != nil {
		return err
	}
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return err
	}
	aggArt, err := readArtifact[Aggregate](r.l, fileAggregate)
	if err != nil {
		return err
	}
	aggRaw, err := r.l.readBytes(fileAggregate)
	if err != nil {
		return fmt.Errorf("读取聚合工件原文: %w", err)
	}
	total := len(boundArt.Payload.Chapters)
	summaries := loadSummaries(r.l, total)
	r.emit(StageProfiling, 0, total, "归纳角色分层、设定与关系网...", nil)
	p, err := ProfileBook(ctx, r.deps.Aggregate.Model, r.deps.Prompts.Profile, summaries, &aggArt.Payload, src, total,
		r.deps.Budgets.AggregateMaxTokens, r.profileFor(r.deps.Aggregate, StageProfiling))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.l, fileProfile, profileInputDigest(aggRaw), *p); err != nil {
		return err
	}
	r.emit(StageProfiling, total, total,
		fmt.Sprintf("角色设定完成：%d 个角色、%d 条设定、%d 条关系", len(p.Characters), len(p.Settings), len(p.Relations)), nil)
	return nil
}

// report 产出报告真源与全部人类可读投影。
// 报告是 .md 产物最集中的一步：报告本体、情节节点、写作手法、节奏、情绪模块都由它渲染。
func (r *runner) report(ctx context.Context) error {
	m, err := r.l.LoadManifest()
	if err != nil {
		return err
	}
	src, err := r.l.LoadSource()
	if err != nil {
		return err
	}
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return err
	}
	aggArt, err := readArtifact[Aggregate](r.l, fileAggregate)
	if err != nil {
		return err
	}
	profArt, err := readArtifact[Profile](r.l, fileProfile)
	if err != nil {
		return err
	}
	profRaw, err := r.l.readBytes(fileProfile)
	if err != nil {
		return fmt.Errorf("读取角色设定工件原文: %w", err)
	}
	b := &boundArt.Payload
	total := len(b.Chapters)
	summaries := loadSummaries(r.l, total)
	failed := r.failedChaptersFor(b, src, boundArt.InputDigest)
	r.emit(StageReporting, 0, total, "生成拆文报告...", nil)
	rep, err := BuildReport(ctx, r.deps.Report.Model, r.deps.Prompts.Report, &aggArt.Payload, &profArt.Payload,
		summaries, total, r.deps.Budgets.ReportMaxTokens, r.profileFor(r.deps.Report, StageReporting))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.l, fileReport, reportInputDigest(profRaw), *rep); err != nil {
		return err
	}
	docs := []struct {
		rel     string
		content string
	}{
		{fileReportMD, renderReportMarkdown(m, b, &aggArt.Payload, &profArt.Payload, rep, failed)},
		{filePlotPointsMD, renderPlotPointsMarkdown(m, summaries, failed)},
		{fileTechniquesMD, renderTechniquesMarkdown(m, summaries, rep)},
		{filePacingMD, renderPacingMarkdown(m, &aggArt.Payload, summaries)},
		{fileEmotionMD, renderEmotionMarkdown(m, &aggArt.Payload, summaries)},
	}
	for _, d := range docs {
		if err := r.l.WriteMarkdown(d.rel, d.content); err != nil {
			return fmt.Errorf("写 %s：%w", d.rel, err)
		}
	}
	r.emit(StageReporting, total, total, fmt.Sprintf("报告产物就绪：%s", r.l.Dir()), nil)
	return nil
}

func (r *runner) style(ctx context.Context) error {
	m, err := r.l.LoadManifest()
	if err != nil {
		return err
	}
	src, err := r.l.LoadSource()
	if err != nil {
		return err
	}
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return err
	}
	b := &boundArt.Payload
	r.emit(StageStyling, 0, 0, "裁定文风（确定性统计 + 原文锚点）...", nil)
	s, err := BuildStyle(ctx, r.deps.Report.Model, r.deps.Prompts.Style, src, b,
		r.deps.Budgets.ReportMaxTokens, r.profileFor(r.deps.Report, StageStyling))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.l, fileStyle, styleInputDigest(boundArt.InputDigest, b, src), *s); err != nil {
		return err
	}
	if err := r.l.WriteMarkdown(fileStyleMD, renderStyleMarkdown(m, s)); err != nil {
		return fmt.Errorf("写 %s：%w", fileStyleMD, err)
	}
	r.emit(StageStyling, 0, 0, fmt.Sprintf("文风裁定完成（置信度 %s）", s.Confidence), nil)
	return nil
}

// done 发终态事件。降级（有章节持久失败）必须写进终态：产物不完整这件事不能只在日志里。
func (r *runner) done(f Facts) {
	failed := r.currentFailedChapters()
	ev := Event{
		Time: time.Now(), Stage: StageDone,
		Current: f.ExpectedChapters, Total: f.ExpectedChapters,
		Message: fmt.Sprintf("拆解完成：%s", r.l.Dir()),
	}
	if len(failed) > 0 {
		ev.Degraded = true
		ev.Failed = failed
		ev.Message = fmt.Sprintf("拆解完成（有 %d 章失败：第 %s 章，产物不完整）：%s",
			len(failed), joinInts(failed, 12), r.l.Dir())
	}
	r.send(ev)
}

func (r *runner) currentFailedChapters() []int {
	src, err := r.l.LoadSource()
	if err != nil {
		return nil
	}
	boundArt, err := readArtifact[Boundaries](r.l, fileBoundaries)
	if err != nil {
		return nil
	}
	return r.failedChaptersFor(&boundArt.Payload, src, boundArt.InputDigest)
}
