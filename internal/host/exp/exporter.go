package exp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

// Run 执行一次导出。同步返回，IO 量小（本地文件读写）。
//
// 失败语义：
//   - deps/opts 非法 → 配置错误立即返回
//   - 无任何已完成章节 → 返回错误（让调用方明确）
//   - 范围内某章 chapters/{ch}.md 缺失 → 返回错误（progress 与文件系统不一致是事实层 bug，应让用户看见）
//   - 输出路径已存在且未指定 Overwrite → 返回错误
//
// Skipped 用于"范围内合法但尚未完成"的情况（用户传 to=100 但只写到 80）。
func Run(ctx context.Context, deps Deps, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("exp: deps.Store is nil")
	}

	if opts.Format == "" {
		f, err := inferFormat(opts.OutPath)
		if err != nil {
			return nil, err
		}
		opts.Format = f
	}
	if opts.Format != FormatTXT && opts.Format != FormatEPUB {
		return nil, fmt.Errorf("exp: 暂不支持的格式 %q", opts.Format)
	}

	progress, err := deps.Store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("加载 progress 失败：%w", err)
	}
	if progress == nil || len(progress.CompletedChapters) == 0 {
		return nil, fmt.Errorf("尚无已完成章节，无内容可导出")
	}

	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	maxCh := 0
	for _, c := range progress.CompletedChapters {
		completed[c] = struct{}{}
		if c > maxCh {
			maxCh = c
		}
	}

	from := opts.From
	if from <= 0 {
		from = 1
	}
	to := opts.To
	if to <= 0 {
		to = maxCh
	}
	if from > to {
		return nil, fmt.Errorf("章节范围无效：from=%d > to=%d", from, to)
	}

	var chapters, skipped []int
	for ch := from; ch <= to; ch++ {
		if _, ok := completed[ch]; ok {
			chapters = append(chapters, ch)
		} else {
			skipped = append(skipped, ch)
		}
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("范围 %d..%d 内无已完成章节", from, to)
	}

	bodies := make(map[int]string, len(chapters))
	for _, ch := range chapters {
		text, err := deps.Store.Drafts.LoadChapterText(ch)
		if err != nil {
			return nil, fmt.Errorf("读取第 %d 章失败：%w", ch, err)
		}
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("progress 标记第 %d 章已完成，但 chapters/%02d.md 缺失或为空", ch, ch)
		}
		bodies[ch] = text
	}

	outline, _ := deps.Store.Outline.LoadOutline()
	var volumes []domain.VolumeOutline
	if progress.Layered {
		volumes, _ = deps.Store.Outline.LoadLayeredOutline()
	}

	outPath := opts.OutPath
	if outPath == "" {
		name := strings.TrimSpace(progress.NovelName)
		if name == "" {
			name = filepath.Base(deps.Store.Dir())
		}
		outPath = filepath.Join(deps.Store.Dir(), sanitizeFileName(name)+"."+string(opts.Format))
	}

	if !opts.Overwrite {
		if _, err := os.Stat(outPath); err == nil {
			return nil, fmt.Errorf("文件已存在：%s（添加 --overwrite 覆盖）", outPath)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("检查输出路径失败：%w", err)
		}
	}

	titleIdx := buildTitleIndex(outline)
	var locations map[int]chapterLocation
	if len(volumes) > 0 {
		locations = buildLocations(volumes)
	}

	var data []byte
	switch opts.Format {
	case FormatTXT:
		data = []byte(renderTXT(progress.NovelName, chapters, titleIdx, locations, bodies))
	case FormatEPUB:
		buf, err := renderEPUB(progress.NovelName, chapters, titleIdx, locations, bodies)
		if err != nil {
			return nil, fmt.Errorf("渲染 EPUB 失败：%w", err)
		}
		data = buf
	}

	if err := atomicWrite(outPath, data); err != nil {
		return nil, fmt.Errorf("写入失败：%w", err)
	}

	return &Result{
		Path:     outPath,
		Chapters: len(chapters),
		Bytes:    len(data),
		Skipped:  skipped,
	}, nil
}

// inferFormat 从输出路径后缀推断格式。空路径回退 TXT；未知后缀报错（避免静默错误）。
func inferFormat(path string) (Format, error) {
	if path == "" {
		return FormatTXT, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case "", ".txt":
		return FormatTXT, nil
	case ".epub":
		return FormatEPUB, nil
	default:
		return "", fmt.Errorf("无法从扩展名 %q 推断格式（支持 .txt / .epub）", filepath.Ext(path))
	}
}

// atomicWrite 与 store/io.go 的 WriteFile 同形：tmp + sync + rename。
// 不复用 store.IO 是因为输出路径可能在 store.Dir() 之外。
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// sanitizeFileName 替换文件名里在大多数文件系统上不允许或易混淆的字符。
// 不做激进的转码，只挡住路径分隔符和控制字符。
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "novel"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		"\x00", "_",
	)
	return replacer.Replace(name)
}
