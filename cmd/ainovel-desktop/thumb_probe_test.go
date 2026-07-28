package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMakeThumb_RealSizedCover 用与真实封面同规格的 PNG（1024x1536）验证缩略图管线。
// 之前的 2MB 阈值会让真实封面（实测 2.1MB）在书库里完全不显示。
// 尺寸放大到超过 thumbSourceThreshold，才会走缩略图分支。
func TestMakeThumb_RealSizedCover(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cover.png")
	if err := os.WriteFile(src, bigPNG(t, 1024, 1536*3), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(src)
	t.Logf("源图 %d 字节", info.Size())

	data, err := makeThumb(src)
	if err != nil {
		t.Fatalf("makeThumb: %v", err)
	}
	t.Logf("缩略图 %d 字节 (压缩到 %.1f%%)", len(data), float64(len(data))/float64(info.Size())*100)
	if len(data) == 0 {
		t.Fatal("缩略图为空")
	}
	if len(data) >= int(info.Size()) {
		t.Errorf("缩略图 %d 未比原图 %d 更小", len(data), info.Size())
	}
	if got := sniffImageMime(data); got != "image/jpeg" {
		t.Errorf("mime = %q，想要 image/jpeg", got)
	}

	// 走一遍 fillBookCover：这是书库列表真正调用的入口。
	book := &LibraryBook{Path: dir}
	fillBookCover(book)
	if book.CoverURL == "" {
		t.Fatal("fillBookCover 未产出 CoverURL —— 卡片会是纯文字")
	}
	if len(book.CoverURL) > 400<<10 {
		t.Errorf("内联 data URL 过大: %d 字节", len(book.CoverURL))
	}
	// 缩略图应被缓存下来。
	if _, err := os.Stat(thumbPath(dir)); err != nil {
		t.Errorf("缩略图未缓存: %v", err)
	}
}
