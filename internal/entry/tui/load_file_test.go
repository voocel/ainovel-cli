package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProjectPath_AtPrefix(t *testing.T) {
	dir := t.TempDir()
	abs, err := resolveProjectPath(dir, "@meta/cocreate/x.md")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	want := filepath.Join(dir, "meta", "cocreate", "x.md")
	if abs != want {
		t.Errorf("解析错误：got %s want %s", abs, want)
	}
}

func TestResolveProjectPath_Relative(t *testing.T) {
	dir := t.TempDir()
	abs, err := resolveProjectPath(dir, "outline.md")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	want := filepath.Join(dir, "outline.md")
	if abs != want {
		t.Errorf("解析错误：got %s want %s", abs, want)
	}
}

func TestResolveProjectPath_Absolute(t *testing.T) {
	dir := t.TempDir()
	abs := "/tmp/some-abs-path.md"
	got, err := resolveProjectPath(dir, abs)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got != abs {
		t.Errorf("绝对路径应原样返回：got %s want %s", got, abs)
	}
}

func TestLoadFileAsPrompt_ReadsContent(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "meta", "cocreate")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "x.md")
	want := "# 创作指令\n\n主角：陆沉\n世界观：赛博朋克"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadFileAsPrompt(dir, "@meta/cocreate/x.md")
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if got != strings.TrimSpace(want) {
		t.Errorf("内容不匹配：got=%q want=%q", got, want)
	}
}

func TestLoadFileAsPrompt_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := loadFileAsPrompt(dir, "@meta/cocreate/missing.md")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误消息应包含'不存在'，got：%v", err)
	}
}

func TestLoadFileAsPrompt_TooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	if err := os.WriteFile(path, make([]byte, maxLoadFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadFileAsPrompt(dir, "big.md")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "文件过大") {
		t.Errorf("错误消息应包含'文件过大'，got：%v", err)
	}
}

func TestLoadFileAsPrompt_Directory(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "meta"), 0o755)
	_, err := loadFileAsPrompt(dir, "@meta")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "目录") {
		t.Errorf("错误消息应提到目标为目录，got：%v", err)
	}
}
