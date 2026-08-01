package scan

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
	parsePromptVersion   = "scan-parse-v1"
	analyzePromptVersion = "scan-analyze-v1"
	topicPromptVersion   = "scan-topic-v1"
)

// cleanedEntries 是 entries.json 的载荷：清洗后的条目 + 质量报告 + 解析备注。
// 质量报告与条目同工件：稀疏判定是条目的属性，分开存会让两者失同步。
type cleanedEntries struct {
	Entries      []Entry       `json:"entries"`
	Quality      QualityReport `json:"quality"`
	Notes        []string      `json:"notes,omitempty"` // 解析阶段的可疑之处
	SourceDigest string        `json:"source_digest"`
}

// RunBudgets 是各语义函数的输入/输出预算，默认由各档位模型能力推导。
type RunBudgets struct {
	ParseContextBytes int
	ParseMaxTokens    int
	AnalyzeMaxTokens  int
	TopicMaxTokens    int
}

// DefaultRunBudgets 返回保守默认预算，用于模型能力未知（探测失败）时兜底。
func DefaultRunBudgets() RunBudgets {
	return RunBudgets{ParseContextBytes: 24000, ParseMaxTokens: 8192, AnalyzeMaxTokens: 8192, TopicMaxTokens: 8192}
}

// budgetsFromRuntime 从模型真实 completion 上限派生预算；能力未知时回退保守默认。
func budgetsFromRuntime(rt ModelRuntime) RunBudgets {
	if rt.ContextTokens <= 0 || rt.MaxOutputTokens <= 0 {
		return DefaultRunBudgets()
	}
	out := min(rt.MaxOutputTokens, max(1024, rt.ContextTokens/3))
	reserve := max(512, rt.ContextTokens/10)
	inTokens := rt.ContextTokens - out - reserve
	if inTokens < 512 {
		inTokens = 512
	}
	return RunBudgets{
		ParseContextBytes: inTokens * 3,
		ParseMaxTokens:    out, AnalyzeMaxTokens: out, TopicMaxTokens: out,
	}
}

// budgetsFromDeps 按各语义函数自己的档位能力派生预算：
// 廉价档位的小窗口只约束它自己的阶段，不拖累其它阶段。
func budgetsFromDeps(d Deps) RunBudgets {
	parse := budgetsFromRuntime(d.Parse.Runtime)
	ana := budgetsFromRuntime(d.Analyze.Runtime)
	return RunBudgets{
		ParseContextBytes: parse.ParseContextBytes,
		ParseMaxTokens:    parse.ParseMaxTokens,
		AnalyzeMaxTokens:  ana.AnalyzeMaxTokens,
		TopicMaxTokens:    ana.TopicMaxTokens,
	}
}

// resolveLibraryDir 解析扫榜库根目录；空则用 <cwd>/扫榜库。
func resolveLibraryDir(dir string) (string, error) {
	if strings.TrimSpace(dir) != "" {
		return filepath.Abs(dir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, "扫榜库"), nil
}

// today 返回本地日期的 YYYYMMDD 形式。
func today() string { return time.Now().Format("20060102") }

// sanitizeSegment 把平台/榜单名清成可安全用作目录名的一段。
func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "未指明"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			b.WriteRune('_')
		default:
			if r < 0x20 {
				b.WriteRune('_')
				continue
			}
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		return "未指明"
	}
	return out
}

// LibraryName 给出一次扫榜的目录名：{平台}_{榜单}_{日期}。
func LibraryName(platform, rankName, scanDate string) string {
	return fmt.Sprintf("%s_%s_%s", sanitizeSegment(platform), sanitizeSegment(rankName), sanitizeSegment(scanDate))
}

// LibraryPath 给出一次扫榜在扫榜库中的目录路径，供 Host/UI 回显与恢复检测。
func LibraryPath(libraryDir, platform, rankName, scanDate string) (string, error) {
	root, err := resolveLibraryDir(libraryDir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(scanDate) == "" {
		scanDate = today()
	}
	return filepath.Join(root, LibraryName(platform, rankName, scanDate)), nil
}

// Run 执行完整扫榜管线：LoadState → NextAction → 执行一个动作 → 重新读取事实。
// 在自己的 goroutine 中跑；返回的事件通道由本函数关闭。
func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Parse.Model == nil || deps.Analyze.Model == nil {
		return nil, fmt.Errorf("deps 不完整：解析与分析两个语义档位都需要模型")
	}
	if deps.Budgets == (RunBudgets{}) {
		deps.Budgets = budgetsFromDeps(deps)
	}
	scanDate := strings.TrimSpace(opts.ScanDate)
	if scanDate == "" {
		scanDate = today()
	}
	opts.ScanDate = scanDate

	dir, err := LibraryPath(opts.LibraryDir, opts.Platform, opts.RankName, scanDate)
	if err != nil {
		return nil, fmt.Errorf("解析扫榜库目录：%w", err)
	}

	events := make(chan Event, 32)
	go func() {
		defer close(events)
		r := &runner{deps: deps, opts: opts, events: events, l: OpenLibrary(dir), scanDate: scanDate}
		defer r.closeLogFile()
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
	scanDate string
	act      Action // 当前执行动作，供失败工件标注阶段

	log       *slog.Logger
	closeLog  func()
	logOpened bool
}

// openLog 在扫榜库已建立后打开独立转录（logs/scan.log）。
// 打开失败必须回显：面板会指引用户去看这个文件，静默回退等于指向不存在的路径。
func (r *runner) openLog() {
	if r.logOpened || !r.l.Active() {
		return
	}
	r.logOpened = true
	log, closeLog, err := logger.FileLogger(r.l.path(dirLogs), logFileName)
	r.log, r.closeLog = log, closeLog
	if err != nil {
		r.emit(StageFetching, 0, 0, fmt.Sprintf("扫榜日志文件创建失败（%v），本次转录改走默认日志", err), nil)
		return
	}
	r.log.Info("scan 扫榜模型运行时",
		"platform", r.opts.Platform, "rank", r.opts.RankName, "date", r.scanDate,
		"parse_ctx", r.deps.Parse.Runtime.ContextTokens,
		"analyze_ctx", r.deps.Analyze.Runtime.ContextTokens,
		"parse_context_bytes", r.deps.Budgets.ParseContextBytes)
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
	// 终态事件承载唯一的成败信号，必须可靠送达；中间进度可在积压时丢弃。
	if ev.Stage == StageError || ev.Stage == StageDone {
		r.events <- ev
		return
	}
	select {
	case r.events <- ev:
	default: // 通道满时丢弃进度，绝不阻塞管线
	}
}

// logEvent 把每条事件转录进 logs/scan.log：面板随 Esc 消失，日志是唯一可事后排查的记录。
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

// profileFor 派生某档位的调用选项，并把请求退避/校验重问回显到对应阶段的事件流。
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

func (r *runner) run(ctx context.Context) {
	if err := r.syncProvidedSources(ctx); err != nil {
		r.fail("校验榜单数据源", err)
		return
	}
	var previous *Facts
	for {
		if ctx.Err() != nil {
			r.fail("用户取消", ctx.Err())
			return
		}
		facts, err := LoadState(r.l, r.opts)
		if err != nil {
			r.fail("读取扫榜状态", err)
			return
		}
		if previous != nil && facts == *previous {
			r.fail("扫榜停滞", fmt.Errorf("动作执行后事实没有变化，下一动作仍为 %q", NextAction(facts)))
			return
		}
		snapshot := facts
		previous = &snapshot
		act := NextAction(facts)
		r.act = act
		err = nil
		switch act {
		case ActionFetch:
			err = r.fetch(ctx)
		case ActionParse:
			err = r.parse(ctx)
		case ActionAnalyze:
			err = r.analyze(ctx)
		case ActionTopic:
			err = r.topic(ctx)
		case ActionDone:
			r.done(facts)
			return
		default:
			err = fmt.Errorf("未知动作 %q", act)
		}
		if err != nil {
			r.fail("扫榜失败", err)
			return
		}
	}
}

func (r *runner) syncProvidedSources(ctx context.Context) error {
	sources, provided, err := sourcesFromOptions(ctx, r.opts)
	if err != nil || !provided || !r.l.Active() {
		return err
	}
	saved, err := r.l.LoadSources()
	if err != nil {
		if errors.Is(err, errSourcesUnavailable) {
			return nil // 半初始化库交给 fetch 的既有诊断处理
		}
		return fmt.Errorf("读取已有榜单数据源快照: %w", err)
	}
	if sourcesDigest(saved) == sourcesDigest(sources) {
		r.clearProvidedSources()
		return nil
	}
	if err := r.l.ReplaceSources(sources); err != nil {
		return fmt.Errorf("更新榜单数据源快照: %w", err)
	}
	r.warn(StageFetching, "检测到本次榜单数据与库内快照不同，已更新数据源并使旧分析失效")
	r.clearProvidedSources()
	return nil
}

func (r *runner) clearProvidedSources() {
	r.opts.PastedText = ""
	r.opts.FilePath = ""
	r.opts.DirPath = ""
}

// fetch 取数据并把原始输入快照落盘。原始输入是唯一不可再生的材料，第一步就备份。
func (r *runner) fetch(ctx context.Context) error {
	if r.l.Active() {
		// 目录在但身份不全（_meta.json 或 sources/ 缺失）：两条提示会互相打架，直接说清怎么办。
		if _, err := r.l.LoadMeta(); err != nil {
			return fmt.Errorf("%s 已存在但扫榜库身份不可用（_meta.json 缺失或损坏），请确认后删除该目录再重扫", r.l.Dir())
		}
	}
	if strings.TrimSpace(r.opts.PastedText) == "" &&
		strings.TrimSpace(r.opts.FilePath) == "" &&
		strings.TrimSpace(r.opts.DirPath) == "" {
		return fmt.Errorf("需要数据源：粘贴榜单文本，或用 --file/--dir 指定本地文件或目录")
	}

	r.emit(StageFetching, 0, 0, "读取榜单数据源…", nil)
	f := NewFileFetcher(r.opts.PastedText, r.opts.FilePath, r.opts.DirPath, r.opts.Platform, r.opts.RankName)
	sources, err := f.Fetch(ctx)
	if err != nil {
		return err
	}

	l, err := InitLibrary(r.l.Dir(), r.opts.Platform, r.opts.RankName, r.scanDate, len(sources))
	if err != nil {
		return fmt.Errorf("初始化扫榜库：%w", err)
	}
	if err := l.SaveSources(sources); err != nil {
		return err
	}
	r.l = l
	r.clearProvidedSources()
	r.openLog() // 库已建立，转录从这里开始落到 logs/scan.log
	r.emit(StageFetching, len(sources), len(sources), fmt.Sprintf("已快照 %d 份数据源到 %s", len(sources), filepath.Join(r.l.Dir(), dirSources)), nil)
	return nil
}

// parse 逐份数据源解析成条目，合并后清洗，落 entries.json。
// 清洗与解析同一步：清洗是纯 Go 变换，独立成阶段只会多一个永远为真的状态位。
func (r *runner) parse(ctx context.Context) error {
	meta, err := r.l.LoadMeta()
	if err != nil {
		return fmt.Errorf("读取扫榜库元数据：%w", err)
	}
	sources, err := r.l.LoadSources()
	if err != nil {
		return err
	}

	prof := r.profileFor(r.deps.Parse, StageParsing)
	var raw []Entry
	var notes []string
	for i, src := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(StageParsing, i, len(sources), fmt.Sprintf("解析数据源 %s（%d/%d）", src.Origin, i+1, len(sources)), nil)
		entries, srcNotes, err := ParseSource(ctx, r.deps.Parse.Model, r.deps.Prompts.Parse, src,
			r.deps.Budgets.ParseContextBytes, r.deps.Budgets.ParseMaxTokens, prof)
		if err != nil {
			return fmt.Errorf("解析 %s：%w", src.Origin, err)
		}
		raw = append(raw, entries...)
		for _, n := range srcNotes {
			notes = append(notes, fmt.Sprintf("%s：%s", src.Origin, n))
		}
	}

	r.emit(StageCleaning, 0, 0, fmt.Sprintf("清洗 %d 条原始条目…", len(raw)), nil)
	cleaned, quality := CleanEntries(raw, meta.Platform)
	if len(cleaned) == 0 {
		return fmt.Errorf("清洗后没有有效条目（原始 %d 条全部缺排名/书名/作者）：请检查数据源是否为榜单文本", len(raw))
	}
	if quality.Sparse {
		r.warn(StageCleaning, "[数据稀疏] 有效条目仅 %d 条，趋势与选题结论参考价值有限", quality.ValidEntries)
	}
	for _, issue := range quality.Issues {
		r.warn(StageCleaning, "%s", issue)
	}

	payload := cleanedEntries{
		Entries: sortByRank(cleaned), Quality: quality, Notes: notes,
		SourceDigest: sourcesDigest(sources),
	}
	digest := entriesInputDigest(payload.Entries, meta.ScanDate, meta.Platform)
	if err := writeArtifact(r.l, fileEntries, digest, payload); err != nil {
		return fmt.Errorf("写条目工件：%w", err)
	}
	r.emit(StageCleaning, quality.ValidEntries, quality.TotalRaw,
		fmt.Sprintf("已落盘 %d 条有效条目（原始 %d 条）", quality.ValidEntries, quality.TotalRaw), nil)
	return nil
}

// analyze 归纳趋势并渲染扫榜报告。
func (r *runner) analyze(ctx context.Context) error {
	meta, err := r.l.LoadMeta()
	if err != nil {
		return fmt.Errorf("读取扫榜库元数据：%w", err)
	}
	entArt, err := readArtifact[cleanedEntries](r.l, fileEntries)
	if err != nil {
		return fmt.Errorf("读取条目工件：%w", err)
	}
	entRaw, err := r.l.readBytes(fileEntries)
	if err != nil {
		return err
	}

	r.emit(StageAnalyzing, 0, 0, fmt.Sprintf("分析 %d 条条目的题材分布与趋势…", entArt.Payload.Quality.ValidEntries), nil)
	prof := r.profileFor(r.deps.Analyze, StageAnalyzing)
	analysis, err := AnalyzeRanks(ctx, r.deps.Analyze.Model, r.deps.Prompts.Analyze,
		entArt.Payload.Entries, entArt.Payload.Quality, meta.Platform, meta.RankName,
		r.deps.Budgets.AnalyzeMaxTokens, prof)
	if err != nil {
		return err
	}

	if err := writeArtifact(r.l, fileAnalysis, analysisInputDigest(entRaw), *analysis); err != nil {
		return fmt.Errorf("写趋势工件：%w", err)
	}
	md := RenderReportMarkdown(analysis, entArt.Payload.Entries, entArt.Payload.Quality,
		meta.Platform, meta.RankName, meta.ScanDate)
	if err := r.l.WriteMarkdown(fileReport, md); err != nil {
		return fmt.Errorf("写扫榜报告：%w", err)
	}
	r.emit(StageAnalyzing, 0, 0, "扫榜报告已落盘："+fileReport, nil)
	return nil
}

// topic 产出选题决策并渲染 Markdown。
func (r *runner) topic(ctx context.Context) error {
	meta, err := r.l.LoadMeta()
	if err != nil {
		return fmt.Errorf("读取扫榜库元数据：%w", err)
	}
	entArt, err := readArtifact[cleanedEntries](r.l, fileEntries)
	if err != nil {
		return fmt.Errorf("读取条目工件：%w", err)
	}
	anaArt, err := readArtifact[Analysis](r.l, fileAnalysis)
	if err != nil {
		return fmt.Errorf("读取趋势工件：%w", err)
	}
	anaRaw, err := r.l.readBytes(fileAnalysis)
	if err != nil {
		return err
	}

	r.emit(StageTopicing, 0, 0, "生成选题决策…", nil)
	prof := r.profileFor(r.deps.Analyze, StageTopicing)
	analysis := anaArt.Payload
	report, err := DecideTopics(ctx, r.deps.Analyze.Model, r.deps.Prompts.Topic,
		&analysis, entArt.Payload.Quality, meta.Platform, meta.RankName,
		r.deps.Budgets.TopicMaxTokens, prof)
	if err != nil {
		return err
	}

	if err := writeArtifact(r.l, fileTopic, topicInputDigest(anaRaw), *report); err != nil {
		return fmt.Errorf("写选题工件：%w", err)
	}
	md := RenderTopicMarkdown(*report, entArt.Payload.Quality)
	if err := r.l.WriteMarkdown(fileTopicMD, md); err != nil {
		return fmt.Errorf("写选题决策：%w", err)
	}
	r.emit(StageTopicing, 0, 0, "选题决策已落盘："+fileTopicMD, nil)
	return nil
}

func (r *runner) done(f Facts) {
	msg := fmt.Sprintf("扫榜完成：%d 条有效条目，产物在 %s", f.ValidEntries, r.l.Dir())
	if f.Sparse {
		msg = fmt.Sprintf("扫榜完成（[数据稀疏]，仅 %d 条有效条目，结论参考价值有限）：产物在 %s", f.ValidEntries, r.l.Dir())
	}
	r.send(Event{
		Time: time.Now(), Stage: StageDone, Message: msg,
		Sparse: f.Sparse, Entries: f.ValidEntries, Dir: r.l.Dir(),
	})
}
