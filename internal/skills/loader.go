package skills

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadOptions 枚举 skill 文件的覆盖来源目录。
//
// 路径语义与 rules 层严格对称（internal/rules/loader.go）：ProjectSkillsDir 绑定
// **当前工作目录**而非书目录——用户 cd 到不同目录写不同的书，./.ainovel/skills/
// 自然跟着走；要跨书共享就放 ~/.ainovel/skills/。
//
// 目录不存在不算错误，扫描时静默跳过。
type LoadOptions struct {
	HomeSkillsDir    string // ~/.ainovel/skills/
	ProjectSkillsDir string // ./.ainovel/skills/
}

// ainovelDirName 是 ainovel 在 user / project 两级共用的 dotdir 名，与 rules 层同名。
const ainovelDirName = ".ainovel"

// DefaultHomeSkillsDir 拼出 ~/.ainovel/skills/；home 解析失败返回空串（调用方跳过）。
func DefaultHomeSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ainovelDirName, "skills")
}

// DefaultProjectSkillsDir 拼出 <projectDir>/.ainovel/skills/。
func DefaultProjectSkillsDir(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ainovelDirName, "skills")
}

// DefaultOptions 按当前工作目录构造生产环境的来源配置。
// 解析 cwd 失败时 ProjectSkillsDir 留空（扫描跳过该层）。
func DefaultOptions() LoadOptions {
	cwd, _ := os.Getwd()
	return LoadOptions{
		HomeSkillsDir:    DefaultHomeSkillsDir(),
		ProjectSkillsDir: DefaultProjectSkillsDir(cwd),
	}
}

// homeSkillsReadme 是首次引导时写入 ~/.ainovel/skills/README.txt 的说明。
//
// 与 rules 层的 README 同一取向：用 .txt 后缀（扫描只认 .md，这份说明不会被误当技能），
// 每次启动覆盖为最新模板，因此不需要任何版本兼容逻辑。
//
// 内容必须把四个字段的**合法取值**写全：技能的 frontmatter 是机械校验的（写错就整个
// 文件被跳过），而跳过只进日志、UI 上只表现为"清单里少一条"。把 agent 白名单与 scope
// 枚举摊在这里，用户就不必靠猜或读源码。
const homeSkillsReadme = `这里放专项技能，跨所有书生效。

技能是一份「可复用的处理方法」——用 /skill 调用时，它的正文会作为专业判据
拼进本次改稿任务，让 AI 按你定的标准处理，而不是靠一句转述。

和 rules 的区别（两者不重叠）：
  rules   = 长效约束，"以后都这么写"，每章自动生效
  skills  = 一次性方法，"这次这么处理"，你点了才跑

新建一个 .md 文件（如 my-polish.md），格式如下：

    ---
    name: my-polish
    description: 一句话说明这个技能干什么
    agent: editor
    scope: chapters
    ---
    这里写完整的处理指令，也就是 AI 会看到的专业判据。
    写得越具体越好：检查什么、怎么判断、改到什么程度算达标。

四个字段都必填，取值有限制（写错整个文件会被跳过）：

  name         只能用小写字母、数字、连字符（如 my-polish），不能有空格或中文
  description  一行。这是 AI 判断"用户这次诉求是否该用这个技能"的唯一依据，
               写清适用场景，别只写技能名的同义词
  agent        由谁执行，只能填这四个之一：
                 editor           改已完成的章节（最常用）
                 writer           影响后续写作
                 architect_long   调整长篇的设定/大纲
                 architect_short  调整短篇的设定/大纲
  scope        作用范围，只能填这三个之一：
                 chapters    已完成章节（可在调用时指定 3 / 3-5 / 3,5,7）
                 forward     后续写作，不回改已有内容
                 foundation  设定层（前提/大纲/人物/世界观）

  agent 与 scope 必须配套：chapters 只能配 editor，forward 只能配 writer，
  foundation 只能配 architect_long / architect_short；不匹配的文件会被跳过。

改完文件需要重启才会重新加载（正在跑的裁定必须与执行看到同一份技能）。

多个 .md 按文件名字典序加载；点开头的隐藏文件、子目录、非 .md 文件都会被忽略
（所以这份 README.txt 不会被当成技能）。

内置已带几个开箱可用的技能（去 AI 味、收紧节奏），/skill 可以看到完整清单。

加载优先级（高 → 低）：./.ainovel/skills/*.md（本书） > ~/.ainovel/skills/*.md（这里） > 内置
同名 = 整文件替换，不是字段级合并。
`

// EnsureHomeSkillsDir 尽力创建 ~/.ainovel/skills/ 并写入 README.txt 引导。
//
// 没有这一步，"装一个技能"就要求用户先自己 mkdir、再猜出 frontmatter 的四个字段和
// 它们的合法取值——技能是给用户扩展的东西，发现不了就等于没有。
//
// nice-to-have，非关键路径：home 解析失败或写入出错都静默吞掉，绝不阻断启动。
// 与 rules.EnsureHomeRulesDir 同一契约。
func EnsureHomeSkillsDir() {
	if dir := DefaultHomeSkillsDir(); dir != "" {
		_ = ensureSkillsDirAt(dir)
	}
}

// ensureSkillsDirAt 是 EnsureHomeSkillsDir 的可测内核。
func ensureSkillsDirAt(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(homeSkillsReadme), 0o644)
}

// Catalog 是三层合并后的 skill 目录，运行期只读。
type Catalog struct {
	byName map[string]Skill
	names  []string // 字典序；Names 的顺序稳定性是 schema fingerprint 稳定的前提

	// problems 记录被跳过的文件及原因。
	//
	// 只写日志不够：用户写坏一个技能，UI 上只表现为"清单里少一条"，无从排查。
	// 加载不能失败（一个坏文件不该带走其他技能），所以错误必须以数据形式带出来，
	// 由 UI 显式告知。
	problems []Problem
}

// Problem 是一个被跳过的技能文件。Source 是来源标签（global:x.md），Err 是人话原因。
type Problem struct {
	Source string
	Err    string
}

// Load 按 内置 → 全局 → 本书 顺序加载并合并 skill。
//
// 同名整文件替换（本书 > 全局 > 内置）——skill 是一套完整方法，做字段级合并只会
// 产出自相矛盾的指令。与 assets.overlayStyles 的"同名整文件替换"同一取向。
//
// 单个文件非法只跳过并告警，绝不阻断加载：用户写坏一个 skill 不该让另外几个也失效。
func Load(opts LoadOptions) Catalog {
	c := Catalog{byName: make(map[string]Skill)}
	c.overlay(builtinSources())
	c.overlay(dirSources(opts.HomeSkillsDir, "global"))
	c.overlay(dirSources(opts.ProjectSkillsDir, "project"))

	c.names = make([]string, 0, len(c.byName))
	for name := range c.byName {
		c.names = append(c.names, name)
	}
	sort.Strings(c.names)
	return c
}

// rawSource 是一份待解析的 skill 文件。
type rawSource struct {
	label   string // 来源标签，如 builtin:anti-ai-tone.md
	content string
}

// overlay 解析并叠加一批来源；后叠加的同名 skill 覆盖先前的。
func (c *Catalog) overlay(sources []rawSource) {
	for _, src := range sources {
		sk, err := parse(src.content, src.label)
		if err != nil {
			slog.Warn("跳过非法技能文件", "module", "skills", "source", src.label, "err", err)
			c.problems = append(c.problems, Problem{Source: src.label, Err: err.Error()})
			continue
		}
		c.byName[sk.Name] = sk
	}
}

// dirSources 扫描一个覆盖目录。扫描约定与 rules.rawDir 完全一致：
// 仅顶层 .md、文件名字典序、跳过隐藏文件与空文件。
//
// 目录不存在是常态（静默）；权限错误或路径其实是文件必须留痕——否则用户写了 skill
// 却完全没生效、零反馈，排查成本极高（同 rules 层的先例教训）。
func dirSources(dir, kind string) []rawSource {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("技能目录读取失败，已跳过", "module", "skills", "dir", dir, "err", err)
		}
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var out []rawSource
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("技能文件读取失败，已跳过", "module", "skills", "file", path, "err", err)
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			continue
		}
		out = append(out, rawSource{label: kind + ":" + name, content: string(data)})
	}
	return out
}

// Get 按名取 skill。
func (c Catalog) Get(name string) (Skill, bool) {
	sk, ok := c.byName[strings.TrimSpace(name)]
	return sk, ok
}

// Names 返回所有 skill 名（字典序）。顺序稳定，可直接充当 schema enum——
// 同一 skill 集合必须产出同一 contract fingerprint。
func (c Catalog) Names() []string {
	return append([]string(nil), c.names...)
}

// List 返回所有 skill（按名字典序），供 UI 清单与命令补全。
func (c Catalog) List() []Skill {
	out := make([]Skill, 0, len(c.names))
	for _, name := range c.names {
		out = append(out, c.byName[name])
	}
	return out
}

// Len 返回 skill 数量。
func (c Catalog) Len() int { return len(c.names) }

// Problems 返回加载时被跳过的文件。内置层不该出问题（有测试兜底），所以这里非空
// 基本都是用户自己写的文件——必须让用户看见，否则"我明明放了技能却没有"无从排查。
func (c Catalog) Problems() []Problem {
	return append([]Problem(nil), c.problems...)
}
