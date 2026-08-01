package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source 是一份榜单数据源。
type Source struct {
	Platform string // qidian / fanqie / qimao / jjwxc / ciweimao / other
	RankName string
	Raw      string // 半结构化原文（粘贴的文本 / 文件内容）
	Origin   string // 文件相对路径或 "paste"
}

// Fetcher 是数据获取接口，第一版只有 fileFetcher，将来可加 httpFetcher。
type Fetcher interface {
	Fetch(ctx context.Context) ([]Source, error)
}

// fileFetcher 从本地文件或粘贴文本获取数据。
type fileFetcher struct {
	pastedText string
	filePath   string
	dirPath    string
	platform   string
	rankName   string
}

// NewFileFetcher 创建文件数据获取器。
func NewFileFetcher(pastedText, filePath, dirPath, platform, rankName string) Fetcher {
	return &fileFetcher{
		pastedText: pastedText,
		filePath:   filePath,
		dirPath:    dirPath,
		platform:   platform,
		rankName:   rankName,
	}
}

func sourcesFromOptions(ctx context.Context, opts Options) ([]Source, bool, error) {
	provided := strings.TrimSpace(opts.PastedText) != "" || strings.TrimSpace(opts.FilePath) != "" || strings.TrimSpace(opts.DirPath) != ""
	if !provided {
		return nil, false, nil
	}
	sources, err := NewFileFetcher(opts.PastedText, opts.FilePath, opts.DirPath, opts.Platform, opts.RankName).Fetch(ctx)
	return sources, true, err
}

// Fetch 实现 Fetcher 接口。
func (f *fileFetcher) Fetch(ctx context.Context) ([]Source, error) {
	// 粘贴文本优先
	if f.pastedText != "" {
		return []Source{{
			Platform: f.platform,
			RankName: f.rankName,
			Raw:      f.pastedText,
			Origin:   "paste",
		}}, nil
	}

	// 单个文件
	if f.filePath != "" {
		data, err := os.ReadFile(f.filePath)
		if err != nil {
			return nil, fmt.Errorf("读取文件 %s: %w", f.filePath, err)
		}
		return []Source{{
			Platform: f.platform,
			RankName: f.rankName,
			Raw:      string(data),
			Origin:   filepath.Base(f.filePath),
		}}, nil
	}

	// 目录扫描
	if f.dirPath != "" {
		return f.scanDir(ctx, f.dirPath)
	}

	return nil, fmt.Errorf("必须提供数据源：粘贴文本、文件路径或目录路径")
}

// scanDir 扫描目录下所有 .txt 和 .md 文件。
func (f *fileFetcher) scanDir(ctx context.Context, dir string) ([]Source, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录 %s: %w", dir, err)
	}

	var sources []Source
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".txt" && ext != ".md" {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取文件 %s: %w", path, err)
		}

		sources = append(sources, Source{
			Platform: f.platform,
			RankName: f.rankName,
			Raw:      string(data),
			Origin:   name,
		})
	}

	if len(sources) == 0 {
		return nil, fmt.Errorf("目录 %s 中没有找到 .txt 或 .md 文件", dir)
	}

	return sources, nil
}
