package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestLoad_ThreeLayers 验证 Default + Global + Project 三层在升序中各就各位。
func TestLoad_ThreeLayers(t *testing.T) {
	rulesFS := fstest.MapFS{
		"default.md": {Data: []byte("---\nchapter_words: 3000-6000\n---\n")},
	}
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "rules")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(tmp, "project-rules")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "global.md"), []byte("---\nforbidden_chars:\n  - \"——\"\n---\n# 全局偏好\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "project.md"), []byte("---\nchapter_words: 4000-8000\n---\n# 项目偏好\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	layers := Load(LoadOptions{
		RulesFS:         rulesFS,
		HomeRulesDir:    globalDir,
		ProjectRulesDir: projectDir,
	})

	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d: %+v", len(layers), layers)
	}
	expectKinds := []SourceKind{SourceDefault, SourceGlobal, SourceProject}
	for i, want := range expectKinds {
		if layers[i].Kind != want {
			t.Errorf("layer[%d].Kind=%v, want %v", i, layers[i].Kind, want)
		}
	}
	// Merge 后 project 的 chapter_words 应胜出
	b := Merge(layers)
	if b.Structured.ChapterWords == nil || b.Structured.ChapterWords.Min != 4000 {
		t.Errorf("project chapter_words should win, got %+v", b.Structured.ChapterWords)
	}
	// global 贡献的 forbidden_chars 在 project 未声明时保留
	if len(b.Structured.ForbiddenChars) != 1 || b.Structured.ForbiddenChars[0] != "——" {
		t.Errorf("global forbidden_chars should propagate, got %v", b.Structured.ForbiddenChars)
	}
	if !strings.Contains(b.Preferences, "全局偏好") || !strings.Contains(b.Preferences, "项目偏好") {
		t.Errorf("merged preferences missing body: %q", b.Preferences)
	}
}

func TestLoad_GenreFieldIsPassThrough(t *testing.T) {
	// Phase 1.1：genre 仅作字段透传，不再触发 assets/rules/genres/ 加载。
	// 即使 fs 里放了 genres/xianxia.md 也不会被读出。
	rulesFS := fstest.MapFS{
		"default.md":        {Data: []byte("")},
		"genres/xianxia.md": {Data: []byte("---\nforbidden_chars:\n  - \"——\"\n---\n")},
	}
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "project-rules")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "project.md"), []byte("---\ngenre: xianxia\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	layers := Load(LoadOptions{
		RulesFS:         rulesFS,
		ProjectRulesDir: projectDir,
	})

	// 期望仅 default + project，Không có genre 层
	if len(layers) != 2 {
		t.Fatalf("expected 2 layers (no genre loading), got %d: %+v", len(layers), layers)
	}
	b := Merge(layers)
	if b.Structured.Genre != "xianxia" {
		t.Errorf("genre field should be passed through, got %q", b.Structured.Genre)
	}
	// genre Tập tin未被加载 → 不应有 "——" 来自题材Tập tin
	if len(b.Structured.ForbiddenChars) != 0 {
		t.Errorf("genres/*.md must not be auto-loaded in Phase 1.1, got %v", b.Structured.ForbiddenChars)
	}
}

func TestLoad_NilFSDoesNotPanic(t *testing.T) {
	// 入参全Rỗng：不崩，Quay lạiRỗng layers
	layers := Load(LoadOptions{})
	if len(layers) != 0 {
		t.Errorf("expected 0 layers, got %d", len(layers))
	}
}

func TestLoad_OnlyDefault(t *testing.T) {
	// 仅项目内置Mặc định规则可用，用户两个Tập tin都缺
	rulesFS := fstest.MapFS{
		"default.md": {Data: []byte("---\nchapter_words: 3000-6000\n---\n")},
	}
	layers := Load(LoadOptions{RulesFS: rulesFS})
	if len(layers) != 1 || layers[0].Kind != SourceDefault {
		t.Errorf("expected only default layer, got %+v", layers)
	}
}

// TestLoad_GlobalDirScansAllMarkdown 验证 global Thư mục下多个 .md 都被加载，
// 按Tập tin名字典序合并（后者覆盖前者），非 .md Tập tin被忽略。
func TestLoad_GlobalDirScansAllMarkdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nchapter_words: 1000-2000\n---\n# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("---\nchapter_words: 3000-4000\n---\n# B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非 .md Tập tin应被忽略
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("not a rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	layers := Load(LoadOptions{HomeRulesDir: dir})
	if len(layers) != 2 {
		t.Fatalf("expected 2 global layers (a.md, b.md), got %d", len(layers))
	}
	for _, p := range layers {
		if p.Kind != SourceGlobal {
			t.Errorf("dir files should be SourceGlobal, got %v", p.Kind)
		}
	}
	// 字典序 a 在前 b 在后，合并后 b 覆盖 a
	b := Merge(layers)
	if b.Structured.ChapterWords == nil || b.Structured.ChapterWords.Min != 3000 {
		t.Errorf("later file (b.md) should win on chapter_words, got %+v", b.Structured.ChapterWords)
	}
	if !strings.Contains(b.Preferences, "# A") || !strings.Contains(b.Preferences, "# B") {
		t.Errorf("both files' preferences should be merged, got %q", b.Preferences)
	}
}

// TestLoad_GlobalDirMissing 验证 global Thư mục不存在时静默Bỏ qua。
func TestLoad_GlobalDirMissing(t *testing.T) {
	layers := Load(LoadOptions{HomeRulesDir: filepath.Join(t.TempDir(), "does-not-exist")})
	if len(layers) != 0 {
		t.Errorf("missing global dir should yield 0 layers, got %d", len(layers))
	}
}

// TestLoad_GlobalDirIgnoresHiddenAndSubdirs 锁死:隐藏/Sửa器临时Tập tin(. 开头)被忽略、
// 子Thư mục不递归——防止脏Tập tin二进制内容当偏好Chính văn注入 LLM。
func TestLoad_GlobalDirIgnoresHiddenAndSubdirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("---\nchapter_words: 3000-6000\n---\n# real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// macOS AppleDouble / emacs 锁 / 普通隐藏Tập tin —— 都该被忽略
	for _, dirty := range []string{"._real.md", ".#lock.md", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(dir, dirty), []byte("\x00binary garbage\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 子Thư mục里的 .md 不该被递归
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.md"), []byte("---\nchapter_words: 1-2\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	layers := Load(LoadOptions{HomeRulesDir: dir})
	if len(layers) != 1 {
		t.Fatalf("expected only real.md loaded (hidden/dirty/subdir ignored), got %d layers", len(layers))
	}
	if layers[0].Source != filepath.Join(dir, "real.md") {
		t.Errorf("loaded wrong file: %s", layers[0].Source)
	}
	if b := Merge(layers); strings.Contains(b.Preferences, "garbage") || strings.Contains(b.Preferences, "\x00") {
		t.Errorf("dirty file content leaked into preferences: %q", b.Preferences)
	}
}

// TestLoad_GlobalDirIsFileExposesConflict 验证 rules Đường dẫn误建成Tập tin(非Thư mục)时
// 暴露 conflict 而非静默吞错——与单Tập tin IO Lỗi的容错契约一致。
func TestLoad_GlobalDirIsFileExposesConflict(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(p, []byte("oops, should be a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	layers := Load(LoadOptions{HomeRulesDir: p})
	if len(layers) != 1 || len(layers[0].Conflicts) == 0 {
		t.Fatalf("expected 1 layer carrying a conflict, got %+v", layers)
	}
	if layers[0].Conflicts[0].Kind != ConflictParseError {
		t.Errorf("expected ConflictParseError, got %v", layers[0].Conflicts[0].Kind)
	}
}

// TestEnsureRulesDirAt 验证备好Thư mục + README.txt：写入说明、始终覆盖为最Mới模板，
// 且 README.txt(非 .md)不会被 loader 当成规则。
func TestEnsureRulesDirAt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	if err := ensureRulesDirAt(dir); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(dir, "README.txt")
	data, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("README.txt should be written: %v", err)
	}
	if !strings.Contains(string(data), "front matter") {
		t.Errorf("README.txt missing guidance, got %q", data)
	}

	// 始终覆盖为最Mới模板：CũPhiên bản写的过时文案（如Đường dẫn仍是 ./rules.md）再次 ensure 时被Làm mới
	if err := os.WriteFile(readme, []byte("CũPhiên bản写的 ./rules.md 文案"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRulesDirAt(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(readme); string(again) != homeRulesReadme {
		t.Errorf("README.txt should be refreshed to latest template, got %q", again)
	}

	// README.txt 不被当规则(loader 只扫 .md)
	if layers := Load(LoadOptions{HomeRulesDir: dir}); len(layers) != 0 {
		t.Errorf("README.txt must not be loaded as a rule, got %d layers", len(layers))
	}
}

// TestDefaultProjectRulesDir 锁死项目级规则Thư mục镜像全局：./.ainovel/rules/。
func TestDefaultProjectRulesDir(t *testing.T) {
	proj := filepath.Join("/tmp", "demo-book")
	want := filepath.Join(proj, ".ainovel", "rules")
	if got := DefaultProjectRulesDir(proj); got != want {
		t.Errorf("DefaultProjectRulesDir=%q, want %q", got, want)
	}
	if got := DefaultProjectRulesDir(""); got != "" {
		t.Errorf("Rỗng项目根应Quay lạiRỗng串，得到 %q", got)
	}
}

// TestDefaultOptions_LoadsProjectRulesFromDotAinovel 端到端验证：
// DefaultOptions 把 cwd 下的 ./.ainovel/rules/ 接进 SourceProject 来源。
func TestDefaultOptions_LoadsProjectRulesFromDotAinovel(t *testing.T) {
	proj := t.TempDir()
	t.Chdir(proj)
	rulesDir := filepath.Join(proj, ".ainovel", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "book.md"), []byte("---\nchapter_words: 4000-8000\n---\n# 本书偏好\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rulesFS := fstest.MapFS{"default.md": {Data: []byte("---\nchapter_words: 3000-6000\n---\n")}}
	layers := Load(DefaultOptions(rulesFS))

	var got *Parsed
	for i := range layers {
		if layers[i].Kind == SourceProject {
			got = &layers[i]
		}
	}
	if got == nil {
		t.Fatalf("应从 ./.ainovel/rules/ 加载到项目规则层，得到 %+v", layers)
	}
	if b := Merge(layers); b.Structured.ChapterWords == nil || b.Structured.ChapterWords.Min != 4000 {
		t.Errorf("项目规则应覆盖Mặc định chapter_words，得到 %+v", b.Structured.ChapterWords)
	}
}
