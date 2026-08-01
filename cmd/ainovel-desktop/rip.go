package main

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host/rip"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── 拆文（对标小说只读拆解）──
//
// 与导入同一形状：绑定方法启动后立即返回，抽取 goroutine 把 rip.Event 转成
// Wails 事件推给前端。互斥由 Host 的 acquireExclusive 保证。
//
// 拆文是只读的：产物落独立拆文库（拆文库/{书名}/），不写本书的 Store。
// 但它仍需要一个已打开的书——模型档位与用量记账都挂在 Host 上。

// DeconstructOptions 是前端提交的拆解参数，对应 rip.Options。
type DeconstructOptions struct {
	SourcePath    string `json:"sourcePath"`
	LibraryDir    string `json:"libraryDir"`
	BookName      string `json:"bookName"`
	Form          string `json:"form"`
	AcceptPreview bool   `json:"acceptPreview"`
	AutoConfirm   bool   `json:"autoConfirm"`
	Guidance      string `json:"guidance"`
	RetryFailed   bool   `json:"retryFailed"`
}

// StartDeconstruct 启动（或恢复/推进）一次拆解。
//
// 管线在需要用户裁定处会**停下并关闭事件通道**（灰区字数待裁定、黄金三章待放行），
// 恢复是无状态的：前端据 paused 阶段带上相应选项再调一次本方法即可继续——
// AcceptPreview 放行全书拆解，Form 裁定长短篇，Guidance 则触发重新切分。
// SourcePath 留空 + BookName 非空表示从已有拆文库恢复。
func (a *App) StartDeconstruct(opts DeconstructOptions) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	if opts.Form != "" && opts.Form != "long" && opts.Form != "short" {
		return fmt.Errorf("长短篇只能是 long 或 short，收到 %q", opts.Form)
	}
	if strings.TrimSpace(opts.SourcePath) == "" && strings.TrimSpace(opts.BookName) == "" {
		return fmt.Errorf("请选择要拆解的原文，或指定书名从已有拆文库恢复")
	}

	ctx, jobID := a.jobs.begin("rip")
	ch, err := h.Deconstruct(ctx, rip.Options{
		SourcePath:    strings.TrimSpace(opts.SourcePath),
		LibraryDir:    strings.TrimSpace(opts.LibraryDir),
		BookName:      strings.TrimSpace(opts.BookName),
		Form:          opts.Form,
		AcceptPreview: opts.AcceptPreview,
		AutoConfirm:   opts.AutoConfirm,
		Guidance:      strings.TrimSpace(opts.Guidance),
		RetryFailed:   opts.RetryFailed,
	})
	if err != nil {
		a.jobs.end("rip", jobID)
		return err
	}

	go func() {
		defer a.jobs.end("rip", jobID)
		// 记录最后阶段：通道关闭时若停在停靠点，据此告知前端「已暂停」。
		last := jobEvent{}
		var degraded bool
		var failed []int
		for ev := range ch {
			last = toRipJobEvent(ev)
			if ev.Degraded {
				degraded = true
				failed = append(failed[:0], ev.Failed...)
			}
			a.emit("job:rip", last)
		}
		paused := last.Stage == string(rip.StageAwaitingPreview) ||
			last.Stage == string(rip.StageAwaitingForm)
		a.emit("job:rip:done", map[string]any{
			"paused":   paused,
			"stage":    last.Stage,
			"error":    last.Error,
			"degraded": degraded,
			"failed":   failed,
		})
	}()
	return nil
}

// CancelDeconstruct 取消在途拆解（已完成的阶段工件保留，可再次 StartDeconstruct 续做）。
func (a *App) CancelDeconstruct() {
	a.jobs.abort("rip")
}

// DeconstructResumeHint 返回某本书未完成拆解的一行提示（无则空串）。
func (a *App) DeconstructResumeHint(libraryDir, bookName string) (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	return h.DeconstructResumeHint(strings.TrimSpace(libraryDir), strings.TrimSpace(bookName)), nil
}

// DeconstructLibraryPath 回显某本书在拆文库中的目录，供前端展示产物落点。
func (a *App) DeconstructLibraryPath(libraryDir, bookName string) (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	return h.DeconstructLibraryPath(strings.TrimSpace(libraryDir), strings.TrimSpace(bookName))
}

// toRipJobEvent 把 rip.Event 投影成前端共用的 jobEvent 形状。
// Paused 由通道关闭时的阶段判定（见 StartDeconstruct），不在单条事件里置位。
func toRipJobEvent(ev rip.Event) jobEvent {
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

// PickNovelFile 选择要拆解的对标小说原文（txt / md）。取消返回空串。
func (a *App) PickNovelFile() (string, error) {
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择要拆解的小说原文",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "小说文本 (*.txt;*.md)", Pattern: "*.txt;*.md"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
}
