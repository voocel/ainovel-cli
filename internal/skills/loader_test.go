package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeSkill 在目录里落一个 skill 文件（目录按需创建）。
func writeSkill(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillFile(name, desc, agent, scope, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc +
		"\nagent: " + agent + "\nscope: " + scope + "\n---\n" + body + "\n"
}

// 内置层必须能自己加载起来：assets/skills/*.md 的 frontmatter 写坏了要在这里就炸，
// 而不是等用户 /skill 时才发现清单是空的。
func TestLoadBuiltinSkills(t *testing.T) {
	c := Load(LoadOptions{})
	if c.Len() == 0 {
		t.Fatal("内置技能清单不应为空")
	}
	for _, sk := range c.List() {
		if err := sk.validate(); err != nil {
			t.Fatalf("内置技能 %s 自身不合法: %v", sk.Source, err)
		}
		if got, ok := c.Get(sk.Name); !ok || got.Name != sk.Name {
			t.Fatalf("Get(%q) 取不回自己", sk.Name)
		}
	}
	antiAI, ok := c.Get("anti-ai-tone")
	if !ok {
		t.Fatal("内置层应含 anti-ai-tone")
	}
	for _, required := range []string{
		"check_consistency.prose_findings",
		"Pass 1：去泛化和模板",
		"Pass 2：去解释腔和书面腔",
		"Pass 3：恢复自然叙事",
		"电报体",
		"返工完成后必须重新",
	} {
		if !strings.Contains(antiAI.Body, required) {
			t.Errorf("去 AI 味技能缺少关键执行协议 %q", required)
		}
	}
}

// 三层覆盖：同名整文件替换，本书 > 全局 > 内置。
func TestLoadOverlayPriority(t *testing.T) {
	home := filepath.Join(t.TempDir(), "skills")
	project := filepath.Join(t.TempDir(), "skills")

	// 全局层覆盖内置的 anti-ai-tone，并新增一个只在全局存在的技能。
	writeSkill(t, home, "anti-ai-tone.md",
		skillFile("anti-ai-tone", "全局版去 AI 味", "writer", "forward", "全局正文"))
	writeSkill(t, home, "global-only.md",
		skillFile("global-only", "只在全局", "editor", "chapters", "全局独有正文"))
	// 本书层再覆盖同名，且新增本书独有技能。
	writeSkill(t, project, "anti-ai-tone.md",
		skillFile("anti-ai-tone", "本书版去 AI 味", "editor", "chapters", "本书正文"))
	writeSkill(t, project, "book-only.md",
		skillFile("book-only", "只在本书", "editor", "chapters", "本书独有正文"))

	c := Load(LoadOptions{HomeSkillsDir: home, ProjectSkillsDir: project})

	sk, ok := c.Get("anti-ai-tone")
	if !ok {
		t.Fatal("覆盖后仍应存在 anti-ai-tone")
	}
	if sk.Description != "本书版去 AI 味" || sk.Body != "本书正文" || sk.Source != "project:anti-ai-tone.md" {
		t.Fatalf("本书层未覆盖全局与内置: %+v", sk)
	}
	// 整文件替换：全局层声明的 writer/forward 不该有任何残留。
	if sk.Agent != "editor" || sk.Scope != ScopeChapters {
		t.Fatalf("同名替换应是整文件替换，字段有残留: %+v", sk)
	}
	if g, ok := c.Get("global-only"); !ok || g.Source != "global:global-only.md" {
		t.Fatalf("全局独有技能应保留: %+v", g)
	}
	if b, ok := c.Get("book-only"); !ok || b.Source != "project:book-only.md" {
		t.Fatalf("本书独有技能应保留: %+v", b)
	}
}

// 一个坏文件不能带走其他技能——这是"用户随手写"的前提。
func TestLoadSkipsInvalidFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, dir, "good.md", skillFile("good", "正常技能", "editor", "chapters", "正文"))
	writeSkill(t, dir, "no-frontmatter.md", "直接就是正文，没有 --- 围栏\n")
	writeSkill(t, dir, "unclosed.md", "---\nname: unclosed\ndescription: 缺结束围栏\n")
	writeSkill(t, dir, "bad-name.md", skillFile("Bad Name", "名字非法", "editor", "chapters", "正文"))
	writeSkill(t, dir, "bad-agent.md", skillFile("bad-agent", "执行者非法", "poet", "chapters", "正文"))
	writeSkill(t, dir, "bad-scope.md", skillFile("bad-scope", "范围非法", "editor", "everything", "正文"))
	writeSkill(t, dir, "bad-pair.md", skillFile("bad-pair", "执行者与范围冲突", "writer", "chapters", "正文"))
	writeSkill(t, dir, "bad-fence.md", "---\nname: bad-fence\ndescription: 围栏不完整\nagent: editor\nscope: chapters\n---suffix\n正文\n")
	writeSkill(t, dir, "no-desc.md", skillFile("no-desc", "", "editor", "chapters", "正文"))
	writeSkill(t, dir, "no-body.md", skillFile("no-body", "缺正文", "editor", "chapters", ""))
	writeSkill(t, dir, "not-markdown.txt", skillFile("txt-skill", "扩展名不对", "editor", "chapters", "正文"))
	writeSkill(t, dir, "empty.md", "   \n")

	c := Load(LoadOptions{HomeSkillsDir: dir})
	for _, bad := range []string{"unclosed", "Bad Name", "bad-agent", "bad-scope", "bad-pair", "bad-fence", "no-desc", "no-body", "txt-skill"} {
		if _, ok := c.Get(bad); ok {
			t.Fatalf("非法技能 %q 不应进入清单", bad)
		}
	}
	if _, ok := c.Get("good"); !ok {
		t.Fatal("坏文件不应影响同目录的合法技能")
	}
}

// 被跳过的文件必须以数据形式带出来：只写日志的话，用户写坏一个技能，
// 界面上只表现为"清单里少一条"，无从排查。
func TestProblemsReportSkippedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, dir, "good.md", skillFile("good", "正常技能", "editor", "chapters", "正文"))
	writeSkill(t, dir, "bad-scope.md", skillFile("bad-scope", "范围非法", "editor", "everything", "正文"))
	writeSkill(t, dir, "no-frontmatter.md", "直接就是正文，没有 --- 围栏\n")

	c := Load(LoadOptions{HomeSkillsDir: dir})
	problems := c.Problems()
	if len(problems) != 2 {
		t.Fatalf("应报告 2 个坏文件，实际 %d：%+v", len(problems), problems)
	}
	for _, p := range problems {
		if !strings.HasPrefix(p.Source, "global:") {
			t.Fatalf("Problem.Source 应带来源层前缀，实际 %q", p.Source)
		}
		if strings.TrimSpace(p.Err) == "" {
			t.Fatalf("Problem.Err 不能为空，否则用户只知道『有问题』：%+v", p)
		}
	}
	// 合法文件不受影响，且自己不进 problems。
	if _, ok := c.Get("good"); !ok {
		t.Fatal("合法技能应仍在清单里")
	}
	for _, p := range problems {
		if strings.Contains(p.Source, "good.md") {
			t.Fatalf("合法文件不该出现在 problems：%+v", p)
		}
	}
	// 返回的是副本：调用方改动不能污染 catalog。
	problems[0].Err = "改坏它"
	if c.Problems()[0].Err == "改坏它" {
		t.Fatal("Problems 必须返回副本")
	}
}

// 「装一个技能」不能要求用户先自己 mkdir、再猜 frontmatter 的四个字段。
func TestEnsureSkillsDirWritesReadme(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "skills")
	if err := ensureSkillsDirAt(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "README.txt"))
	if err != nil {
		t.Fatalf("引导说明必须写出来: %v", err)
	}
	// 说明里必须摊开机械校验的合法取值：写错就整个文件被跳过，靠猜代价太高。
	for _, must := range []string{
		"name:", "description:", "agent:", "scope:",
		"editor", "writer", "architect_long", "architect_short",
		"chapters", "forward", "foundation",
	} {
		if !strings.Contains(string(data), must) {
			t.Fatalf("README 缺少用户必须知道的取值 %q", must)
		}
	}

	// 重复调用要幂等（每次启动都会跑），且已有技能文件不能被动到。
	writeSkill(t, dir, "mine.md", skillFile("mine", "我的技能", "editor", "chapters", "正文"))
	if err := ensureSkillsDirAt(dir); err != nil {
		t.Fatalf("重复调用不应失败: %v", err)
	}

	// README.txt 是 .txt，扫描只认 .md，因此不能被当成技能。
	c := Load(LoadOptions{HomeSkillsDir: dir})
	if len(c.Problems()) != 0 {
		t.Fatalf("README.txt 不应被当成技能文件解析：%+v", c.Problems())
	}
	if _, ok := c.Get("mine"); !ok {
		t.Fatal("补写 README 不应影响已有技能加载")
	}
}

// Names 必须字典序稳定：它直接充当 schema enum，顺序漂移会让 contract fingerprint 变。
func TestNamesSortedAndStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, dir, "z.md", skillFile("zeta", "z", "editor", "chapters", "正文"))
	writeSkill(t, dir, "a.md", skillFile("alpha", "a", "editor", "chapters", "正文"))
	writeSkill(t, dir, "m.md", skillFile("mu", "m", "editor", "chapters", "正文"))

	first := Load(LoadOptions{HomeSkillsDir: dir}).Names()
	second := Load(LoadOptions{HomeSkillsDir: dir}).Names()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("两次加载的 Names 顺序不一致: %v vs %v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Fatalf("Names 未按字典序: %v", first)
		}
	}
	// List 的顺序应与 Names 一致。
	list := Load(LoadOptions{HomeSkillsDir: dir}).List()
	if len(list) != len(first) {
		t.Fatalf("List 与 Names 数量不一致: %d vs %d", len(list), len(first))
	}
	for i, sk := range list {
		if sk.Name != first[i] {
			t.Fatalf("List 顺序与 Names 不一致: %v", list)
		}
	}
	// Names 返回副本，调用方改不动内部状态。
	c := Load(LoadOptions{HomeSkillsDir: dir})
	c.Names()[0] = "tampered"
	if c.Names()[0] == "tampered" {
		t.Fatal("Names 必须返回副本")
	}
}

// 目录缺失是常态（用户从没建过 ~/.ainovel/skills），不能报错也不能少掉内置层。
func TestLoadMissingDirsIsNotAnError(t *testing.T) {
	builtinOnly := Load(LoadOptions{}).Len()
	c := Load(LoadOptions{
		HomeSkillsDir:    filepath.Join(t.TempDir(), "nope", "skills"),
		ProjectSkillsDir: filepath.Join(t.TempDir(), "also-nope", "skills"),
	})
	if c.Len() != builtinOnly {
		t.Fatalf("缺失目录不应改变清单，期望 %d 实际 %d", builtinOnly, c.Len())
	}
}

// 子目录与隐藏文件都不扫（与 rules.rawDir 同一约定）。
func TestLoadIgnoresNestedAndHidden(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, filepath.Join(dir, "nested"), "deep.md",
		skillFile("deep", "子目录里的技能", "editor", "chapters", "正文"))
	writeSkill(t, dir, ".hidden.md", skillFile("hidden", "隐藏文件", "editor", "chapters", "正文"))

	c := Load(LoadOptions{HomeSkillsDir: dir})
	if _, ok := c.Get("deep"); ok {
		t.Fatal("不应扫描子目录")
	}
	if _, ok := c.Get("hidden"); ok {
		t.Fatal("不应扫描隐藏文件")
	}
}

// CRLF 与 BOM 是 Windows 编辑器的日常产物，不能让首行围栏判定失败。
func TestParseToleratesBOMAndCRLF(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	content := "\ufeff---\r\nname: winfile\r\ndescription: 记事本另存\r\nagent: editor\r\nscope: chapters\r\n---\r\n正文内容\r\n"
	writeSkill(t, dir, "winfile.md", content)

	c := Load(LoadOptions{HomeSkillsDir: dir})
	sk, ok := c.Get("winfile")
	if !ok {
		t.Fatal("带 BOM 与 CRLF 的文件应能解析")
	}
	if sk.Body != "正文内容" {
		t.Fatalf("正文解析不干净: %q", sk.Body)
	}
}

// Get 容忍首尾空白：命令行与前端都可能把参数带着空格递进来。
func TestGetTrimsName(t *testing.T) {
	c := Load(LoadOptions{})
	if _, ok := c.Get("  anti-ai-tone  "); !ok {
		t.Fatal("Get 应容忍首尾空白")
	}
	if _, ok := c.Get("no-such-skill"); ok {
		t.Fatal("不存在的技能不应命中")
	}
}
