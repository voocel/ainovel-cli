package main

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host/scan"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── 扫榜（榜单趋势与选题决策）──
//
// 与拆文同一形状，但没有停靠点：数据一旦提供，解析到选题一路跑完。
// 第一版不联网抓取——数据由用户提供（粘贴文本 / 本地文件 / 目录）。

// RankScanOptions 是前端提交的扫榜参数，对应 scan.Options。
// 数据源三选一，优先级：粘贴文本 > 单文件 > 目录。
type RankScanOptions struct {
	PastedText string `json:"pastedText"`
	FilePath   string `json:"filePath"`
	DirPath    string `json:"dirPath"`
	Platform   string `json:"platform"`
	RankName   string `json:"rankName"`
	LibraryDir string `json:"libraryDir"`
	ScanDate   string `json:"scanDate"`
}

// StartRankScan 启动（或续做）一次扫榜。
// 同一天同一份数据重跑会复用已有工件，跨日或换数据自然重扫。
func (a *App) StartRankScan(opts RankScanOptions) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	coreOpts, err := prepareRankScanOptions(opts)
	if err != nil {
		return err
	}

	ctx, jobID := a.jobs.begin("scan")
	ch, err := h.ScanRanks(ctx, coreOpts)
	if err != nil {
		a.jobs.end("scan", jobID)
		return err
	}

	go func() {
		defer a.jobs.end("scan", jobID)
		last := jobEvent{}
		var sparse bool
		var entries int
		var libDir string
		for ev := range ch {
			last = toScanJobEvent(ev)
			if ev.Stage == scan.StageDone {
				sparse, entries, libDir = ev.Sparse, ev.Entries, ev.Dir
			}
			a.emit("job:scan", last)
		}
		a.emit("job:scan:done", map[string]any{
			"stage":   last.Stage,
			"error":   last.Error,
			"sparse":  sparse,
			"entries": entries,
			"dir":     libDir,
		})
	}()
	return nil
}

func prepareRankScanOptions(opts RankScanOptions) (scan.Options, error) {
	pasted := strings.TrimSpace(opts.PastedText)
	file := strings.TrimSpace(opts.FilePath)
	dir := strings.TrimSpace(opts.DirPath)
	date := strings.TrimSpace(opts.ScanDate)
	if date != "" && !isScanDate(date) {
		return scan.Options{}, fmt.Errorf("采集日期需要 YYYYMMDD 形式，收到 %q", opts.ScanDate)
	}
	if pasted == "" && file == "" && dir == "" {
		path, err := scan.LibraryPath(strings.TrimSpace(opts.LibraryDir), strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.RankName), date)
		if err != nil {
			return scan.Options{}, err
		}
		if _, err := scan.OpenLibrary(path).LoadSources(); err != nil {
			return scan.Options{}, fmt.Errorf("没有提供新数据源，且扫榜库没有可恢复的 sources/ 快照：%w", err)
		}
	}
	return scan.Options{
		PastedText: pasted,
		FilePath:   file,
		DirPath:    dir,
		Platform:   strings.TrimSpace(opts.Platform),
		RankName:   strings.TrimSpace(opts.RankName),
		LibraryDir: strings.TrimSpace(opts.LibraryDir),
		ScanDate:   date,
	}, nil
}

// CancelRankScan 取消在途扫榜（已完成的阶段工件保留，可再次 StartRankScan 续做）。
func (a *App) CancelRankScan() {
	a.jobs.abort("scan")
}

// RankScanResumeHint 返回某次扫榜未完成的一行提示（无则空串）。
func (a *App) RankScanResumeHint(libraryDir, platform, rankName, scanDate string) (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	return h.ScanResumeHint(strings.TrimSpace(libraryDir), strings.TrimSpace(platform),
		strings.TrimSpace(rankName), strings.TrimSpace(scanDate)), nil
}

// RankScanLibraryPath 回显一次扫榜的产物目录，供前端展示落点。
func (a *App) RankScanLibraryPath(libraryDir, platform, rankName, scanDate string) (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	return h.ScanLibraryPath(strings.TrimSpace(libraryDir), strings.TrimSpace(platform),
		strings.TrimSpace(rankName), strings.TrimSpace(scanDate))
}

// toScanJobEvent 把 scan.Event 投影成前端共用的 jobEvent 形状。
func toScanJobEvent(ev scan.Event) jobEvent {
	out := jobEvent{
		Stage: string(ev.Stage), Current: ev.Current, Total: ev.Total,
		Message: ev.Message, Level: ev.Level, Key: ev.Key,
	}
	if !ev.RetryAt.IsZero() {
		out.RetryAt = ev.RetryAt.Format(rfc3339)
	}
	if ev.Err != nil {
		out.Error = ev.Err.Error()
	}
	return out
}

// isScanDate 校验 YYYYMMDD 形式。只做形状校验，不判断日期是否真实存在——
// 目录名合法即可，用户想给未来日期是他的事。
func isScanDate(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// PickRankFile 选择榜单数据文件（txt / md）。取消返回空串。
func (a *App) PickRankFile() (string, error) {
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择榜单数据文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "榜单文本 (*.txt;*.md)", Pattern: "*.txt;*.md"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
}
