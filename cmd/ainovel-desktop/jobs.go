package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── 长任务（导入 / 仿写）与导出 ──
//
// 导入与仿写各返回一条 <-chan Event（Host 侧 supervisor 负责关闭并释放独占槽）：
// 绑定方法启动后立即返回，由抽取 goroutine 把事件转成 Wails 事件推给前端。
// 导出是同步只读操作，直接返回结果。
//
// 互斥由 Host 的 acquireExclusive 保证（引擎运行中/已有独占作业时拒绝），
// 前端把错误当提示展示即可，不必自己判断。

// jobRegistry 记录在途长任务的取消函数。
type jobRegistry struct {
	mu   sync.Mutex
	seq  uint64
	jobs map[string]jobHandle
}

type jobHandle struct {
	id     uint64
	cancel context.CancelFunc
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{jobs: make(map[string]jobHandle)}
}

func (r *jobRegistry) begin(kind string) (context.Context, uint64) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.seq++
	id := r.seq
	if old := r.jobs[kind]; old.cancel != nil {
		old.cancel()
	}
	r.jobs[kind] = jobHandle{id: id, cancel: cancel}
	r.mu.Unlock()
	return ctx, id
}

// end 只结束自己对应的那一代作业。同类作业重入时旧 goroutine 可能比新作业晚
// 返回；若只按 kind 删除，它的 defer 会误删新作业的取消句柄。
func (r *jobRegistry) end(kind string, id uint64) {
	r.mu.Lock()
	if current, ok := r.jobs[kind]; ok && current.id == id {
		delete(r.jobs, kind)
	}
	r.mu.Unlock()
}

func (r *jobRegistry) abort(kind string) {
	r.mu.Lock()
	handle := r.jobs[kind]
	delete(r.jobs, kind)
	r.mu.Unlock()
	if handle.cancel != nil {
		handle.cancel()
	}
}

func (r *jobRegistry) abortAll() {
	r.mu.Lock()
	all := make([]context.CancelFunc, 0, len(r.jobs))
	for k, handle := range r.jobs {
		all = append(all, handle.cancel)
		delete(r.jobs, k)
	}
	r.mu.Unlock()
	for _, c := range all {
		c()
	}
}

func (r *jobRegistry) active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs) > 0
}

// jobEvent 是投影给前端的长任务进度事件（导入与仿写共用形状）。
type jobEvent struct {
	Stage   string `json:"stage"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Message string `json:"message"`
	Level   string `json:"level"`
	// Key 非空时前端对同 Key 的连续事件原地更新（如退避重试在一行变动）。
	Key     string `json:"key"`
	RetryAt string `json:"retryAt"`
	Error   string `json:"error"`
	// Continued 仅 done 阶段有意义：导入完成后是否已自动接力启动引擎。
	Continued bool `json:"continued"`
	// Paused 表示管线在等待用户裁定处停下（切分确认 / 故事状态）。
	Paused bool `json:"paused"`
}

// ── 导入 ──

// ImportOptions 是前端提交的导入参数，对应 imp.Options。
type ImportOptions struct {
	SourcePath         string `json:"sourcePath"`
	AutoConfirm        bool   `json:"autoConfirm"`
	StoryResolution    string `json:"storyResolution"`
	ContinueAfter      bool   `json:"continueAfter"`
	Guidance           string `json:"guidance"`
	AcceptSegmentation bool   `json:"acceptSegmentation"`
}

// StartImport 启动（或恢复/推进）一次导入。
//
// 管线在需要用户裁定处会**停下并关闭事件通道**（切分确认、故事状态存疑），
// 恢复是无状态的：前端据 paused 阶段带上相应选项再调一次本方法即可继续——
// AcceptSegmentation 放行当前切分，StoryResolution 明确故事状态，
// Guidance 则触发重新切分。SourcePath 留空表示从中断处恢复。
func (a *App) StartImport(opts ImportOptions) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	if opts.StoryResolution != "" &&
		opts.StoryResolution != "open" && opts.StoryResolution != "closed" {
		return fmt.Errorf("故事状态只能是 open 或 closed，收到 %q", opts.StoryResolution)
	}

	ctx, jobID := a.jobs.begin("import")
	ch, err := h.ImportFrom(ctx, imp.Options{
		SourcePath:         strings.TrimSpace(opts.SourcePath),
		AutoConfirm:        opts.AutoConfirm,
		StoryResolution:    opts.StoryResolution,
		ContinueAfter:      opts.ContinueAfter,
		Guidance:           strings.TrimSpace(opts.Guidance),
		AcceptSegmentation: opts.AcceptSegmentation,
	})
	if err != nil {
		a.jobs.end("import", jobID)
		return err
	}

	go func() {
		defer a.jobs.end("import", jobID)
		// 记录最后阶段：通道关闭时若停在等待裁定处，据此告知前端“已暂停”。
		last := jobEvent{}
		for ev := range ch {
			last = toJobEvent(ev)
			a.emit("job:import", last)
		}
		paused := last.Stage == string(imp.StageAwaitingConfirmation) ||
			last.Stage == string(imp.StageAwaitingStoryStatus)
		a.emit("job:import:done", map[string]any{
			"paused":    paused,
			"stage":     last.Stage,
			"continued": last.Continued,
			"error":     last.Error,
		})
	}()
	return nil
}

// CancelImport 取消在途导入（已完成的阶段产物保留，可再次 StartImport 续做）。
func (a *App) CancelImport() {
	a.jobs.abort("import")
}

// ImportResumeHint 返回未完成导入的一行提示（无则空串）。
func (a *App) ImportResumeHint() (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	return h.ImportResumeHint(), nil
}

func toJobEvent(ev imp.Event) jobEvent {
	out := jobEvent{
		Stage: string(ev.Stage), Current: ev.Current, Total: ev.Total,
		Message: ev.Message, Level: ev.Level, Key: ev.Key, Continued: ev.Continued,
	}
	if !ev.RetryAt.IsZero() {
		out.RetryAt = ev.RetryAt.Format(rfc3339)
	}
	if ev.Err != nil {
		out.Error = ev.Err.Error()
	}
	return out
}

// ── 仿写画像 ──

// StartSimulate 从书目录下的 simulate/ 生成或增量更新仿写画像。
// 桌面版按“每本书一个目录”传入书目录下的语料目录（而非进程 cwd）。
func (a *App) StartSimulate() error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	sourceDir, err := ensureSimulationSourceDir(h.Dir())
	if err != nil {
		return err
	}
	ctx, jobID := a.jobs.begin("simulate")
	ch, err := h.SimulateFrom(ctx, sourceDir)
	if err != nil {
		a.jobs.end("simulate", jobID)
		return err
	}
	a.drainSim(ch, jobID)
	return nil
}

// ImportSimulationProfile 导入此前生成的画像 JSON 并按语料指纹合并。
func (a *App) ImportSimulationProfile(path string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("请选择画像文件")
	}
	ctx, jobID := a.jobs.begin("simulate")
	ch, err := h.ImportSimulationProfile(ctx, path)
	if err != nil {
		a.jobs.end("simulate", jobID)
		return err
	}
	a.drainSim(ch, jobID)
	return nil
}

// SimulateSourceDir 返回本书的仿写语料目录（供前端提示用户往哪放参考文章）。
func (a *App) SimulateSourceDir() (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	return ensureSimulationSourceDir(h.Dir())
}

// AddSimulationSources 选择参考文章并复制到当前书的 simulate 目录。
// 返回目标文件名，前端据此确认本次实际加入了哪些语料；取消选择返回空数组。
func (a *App) AddSimulationSources() ([]string, error) {
	h, err := a.reqHost()
	if err != nil {
		return nil, err
	}
	sourceDir, err := ensureSimulationSourceDir(h.Dir())
	if err != nil {
		return nil, err
	}
	paths, err := wailsruntime.OpenMultipleFilesDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择仿写参考文章",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "参考文章 (*.txt;*.md;*.markdown)", Pattern: "*.txt;*.md;*.markdown"},
		},
	})
	if err != nil {
		return nil, err
	}
	return importSimulationSources(sourceDir, paths)
}

// OpenSimulationSourceDir 在系统文件管理器中打开当前书的仿写语料目录。
func (a *App) OpenSimulationSourceDir() (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	sourceDir, err := ensureSimulationSourceDir(h.Dir())
	if err != nil {
		return "", err
	}
	wailsruntime.BrowserOpenURL(a.ctx, "file://"+filepath.ToSlash(sourceDir))
	return sourceDir, nil
}

// CancelSimulate 取消在途仿写作业。
func (a *App) CancelSimulate() {
	a.jobs.abort("simulate")
}

func ensureSimulationSourceDir(bookDir string) (string, error) {
	bookDir = strings.TrimSpace(bookDir)
	if bookDir == "" {
		return "", fmt.Errorf("当前书目录为空，无法准备仿写语料")
	}
	sourceDir := filepath.Join(bookDir, "simulate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		return "", fmt.Errorf("创建仿写语料目录失败 %s: %w", sourceDir, err)
	}
	return sourceDir, nil
}

func importSimulationSources(sourceDir string, paths []string) ([]string, error) {
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return nil, fmt.Errorf("仿写语料目录不能为空")
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建仿写语料目录失败 %s: %w", sourceDir, err)
	}
	type sourceFile struct {
		path string
		name string
	}
	files := make([]sourceFile, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("读取参考文章失败 %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("参考文章不是普通文件：%s", path)
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".txt" && ext != ".md" && ext != ".markdown" {
			return nil, fmt.Errorf("不支持的参考文章格式 %q，仅支持 .txt、.md 和 .markdown", ext)
		}
		name := filepath.Base(path)
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("所选参考文章存在同名文件：%s", name)
		}
		seen[key] = struct{}{}
		files = append(files, sourceFile{path: path, name: name})
	}

	imported := make([]string, 0, len(files))
	for _, file := range files {
		target := filepath.Join(sourceDir, file.name)
		if targetInfo, err := os.Stat(target); err == nil {
			sourceInfo, statErr := os.Stat(file.path)
			if statErr == nil && os.SameFile(sourceInfo, targetInfo) {
				imported = append(imported, file.name)
				continue
			}
		}
		if err := copySimulationSource(file.path, target); err != nil {
			return nil, fmt.Errorf("导入参考文章 %s 失败: %w", file.name, err)
		}
		imported = append(imported, file.name)
	}
	return imported, nil
}

func copySimulationSource(source, target string) (retErr error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(target), ".simulation-source-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = io.Copy(tmp, in); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

func (a *App) drainSim(ch <-chan sim.Event, jobID uint64) {
	go func() {
		defer a.jobs.end("simulate", jobID)
		last := jobEvent{}
		for ev := range ch {
			last = jobEvent{
				Stage: string(ev.Stage), Current: ev.Current, Total: ev.Total,
				Message: ev.Message, Key: ev.Key,
			}
			if ev.Err != nil {
				last.Error = ev.Err.Error()
			}
			a.emit("job:simulate", last)
		}
		a.emit("job:simulate:done", map[string]any{
			"stage": last.Stage, "error": last.Error,
		})
	}()
}

// ── 导出 ──

// ExportOptions 是前端提交的导出参数。Format 留空则按 OutPath 后缀推断。
type ExportOptions struct {
	Format    string `json:"format"`
	OutPath   string `json:"outPath"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

// ExportResult 是导出结果。
type ExportResult struct {
	Path     string `json:"path"`
	Chapters int    `json:"chapters"`
	Bytes    int    `json:"bytes"`
	Skipped  []int  `json:"skipped"`
}

// Export 合并已完成章节导出为 TXT / EPUB。只读操作，创作运行中也可调用。
func (a *App) Export(opts ExportOptions) (ExportResult, error) {
	h, err := a.reqHost()
	if err != nil {
		return ExportResult{}, err
	}
	format := exp.Format(strings.ToLower(strings.TrimSpace(opts.Format)))
	switch format {
	case "", exp.FormatTXT, exp.FormatEPUB:
	default:
		return ExportResult{}, fmt.Errorf("不支持的导出格式: %q（支持 txt / epub）", opts.Format)
	}

	// EPUB 才需要封面；TXT 无处安放。书目录里有封面就自动带上，不必用户勾选。
	var cover *exp.CoverImage
	if format == exp.FormatEPUB || (format == "" && strings.HasSuffix(strings.ToLower(opts.OutPath), ".epub")) {
		if path, mime := findCoverFile(h.Dir()); path != "" {
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				cover = &exp.CoverImage{Data: data, MediaType: mime}
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := h.Export(ctx, exp.Options{
		Format:    format,
		OutPath:   strings.TrimSpace(opts.OutPath),
		From:      opts.From,
		To:        opts.To,
		Overwrite: opts.Overwrite,
		Cover:     cover,
	})
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		Path: res.Path, Chapters: res.Chapters, Bytes: res.Bytes, Skipped: res.Skipped,
	}, nil
}

// ── 原生文件对话框 ──

// PickImportFile 选择要导入的小说源文件（txt / md）。取消返回空串。
func (a *App) PickImportFile() (string, error) {
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择要导入的小说",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "小说文本 (*.txt;*.md)", Pattern: "*.txt;*.md"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
}

// PickProfileFile 选择要导入的仿写画像 JSON。取消返回空串。
func (a *App) PickProfileFile() (string, error) {
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择仿写画像文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "画像 JSON (*.json)", Pattern: "*.json"},
		},
	})
}

// PickExportPath 选择导出目标路径。defaultName 用于预填文件名。取消返回空串。
func (a *App) PickExportPath(defaultName string) (string, error) {
	return wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "导出到",
		DefaultFilename: defaultName,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "纯文本 (*.txt)", Pattern: "*.txt"},
			{DisplayName: "EPUB 电子书 (*.epub)", Pattern: "*.epub"},
		},
	})
}

// PickDirectory 选择一个目录（书库/书目录用）。取消返回空串。
func (a *App) PickDirectory(title string) (string, error) {
	if title == "" {
		title = "选择目录"
	}
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{Title: title})
}
