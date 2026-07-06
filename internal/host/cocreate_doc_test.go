package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

// TestSaveCoCreateDoc_EmptyContent 拒绝空 draft：不创建任何文件。
func TestSaveCoCreateDoc_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	h := &Host{store: store.NewStore(dir)}

	if _, err := h.SaveCoCreateDoc("   \n  "); err == nil {
		t.Fatalf("空内容应返回错误")
	}

	// 目录不应被创建（避免遗留空目录）
	if _, err := os.Stat(filepath.Join(dir, "meta", "cocreate")); !os.IsNotExist(err) {
		t.Fatalf("空内容不应创建目录，got err=%v", err)
	}
}

// TestSaveCoCreateDoc_WritesMarkdown 校验落盘路径与内容一致。
func TestSaveCoCreateDoc_WritesMarkdown(t *testing.T) {
	dir := t.TempDir()
	h := &Host{store: store.NewStore(dir)}

	draft := "# 创作指令\n\n主角：陆沉；世界观：赛博朋克；\n"
	path, err := h.SaveCoCreateDoc(draft)
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	if !strings.HasSuffix(path, ".md") {
		t.Errorf("文件应以 .md 结尾，got %s", path)
	}
	if !strings.HasPrefix(filepath.Base(path), "cocreate-") {
		t.Errorf("文件名应以 cocreate- 开头，got %s", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(draft) {
		t.Errorf("内容不匹配，got=%q want=%q", got, draft)
	}
}

// TestSaveCoCreateDoc_Dedup 同一秒内重复保存：文件名加 -2/-3 后缀。
func TestSaveCoCreateDoc_Dedup(t *testing.T) {
	dir := t.TempDir()
	h := &Host{store: store.NewStore(dir)}

	p1, err := h.SaveCoCreateDoc("# A")
	if err != nil {
		t.Fatalf("第一次保存失败：%v", err)
	}
	p2, err := h.SaveCoCreateDoc("# B")
	if err != nil {
		t.Fatalf("第二次保存失败：%v", err)
	}
	if p1 == p2 {
		t.Fatalf("同秒保存应生成不同路径：p1=%s p2=%s", p1, p2)
	}
	if filepath.Dir(p1) != filepath.Dir(p2) {
		t.Fatalf("两次保存应同目录：p1=%s p2=%s", p1, p2)
	}
}
