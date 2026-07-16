package assets

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/tools"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles/*.md
var stylesFS embed.FS

//go:embed voice.md
var voiceFS embed.FS

// Prompts 表示嵌入的提示词集合。
type Prompts struct {
	ArchitectShort   string
	ArchitectLong    string
	Writer           string // 协议模板,含 {{VOICE}} 占位符;终稿经 BuildWriterPrompt 组装
	Editor           string
	ImportSegment    string // 语义切分：识别章节/卷/附属文本边界
	ImportAnalyze    string // 连续批次逐章事实提取
	ImportSynthesize string // 分层综合与卷弧划分（全书 BookSynthesis）
	ImportRange      string // 长书 Map 阶段连续区间摘要（RangeDigest）
	SimulationSource string
	SimulationMerge  string

	// Arbiter 裁定提示词(LLM-as-function,无 simulation guidance 包装)。
	ArbiterPlanStart    string
	ArbiterIntervention string
	ArbiterFailure      string
}

// Bundle 表示运行所需的静态资源集合。
type Bundle struct {
	References tools.References
	Prompts    Prompts
	Styles     map[string]string
	Voice      string // 写作标准(文风层),已按三层覆盖组装;见 docs/voice-layer.md
}

// LoadOptions 声明文风层的覆盖来源。空目录 = 跳过该层(eval 传零值以获得
// 纯内置的确定性 baseline,不受使用者本机覆盖污染)。
//
// 路径语义:BookStyleDir 绑定书目录(outputDir)而非 cwd——文风随书走,换目录
// 恢复同一本书加载同一份文风。注意与 rules 层不同(rules 的项目级绑定 cwd)。
type LoadOptions struct {
	BookStyleDir string // <outputDir>/style
	HomeStyleDir string // ~/.ainovel/style
}

// DefaultLoadOptions 根据书目录构造生产环境的覆盖来源。
func DefaultLoadOptions(outputDir string) LoadOptions {
	var opts LoadOptions
	if outputDir != "" {
		opts.BookStyleDir = filepath.Join(outputDir, "style")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		opts.HomeStyleDir = filepath.Join(home, ".ainovel", "style")
	}
	return opts
}

// Load 返回指定风格对应的资源集合。文风资产(voice / anti-ai-tone / styles /
// 题材 style-references)按 opts 做三层覆盖:内置 < 全局 < 本书。
func Load(style string, opts LoadOptions) Bundle {
	return Bundle{
		References: loadReferences(style, opts),
		Prompts:    loadPrompts(),
		Styles:     loadStyles(opts),
		Voice:      resolveAppendable(mustRead(voiceFS, "voice.md"), "voice.md", opts),
	}
}

// voicePlaceholder 是 writer 协议模板中文风段的原位插入点。
const voicePlaceholder = "{{VOICE}}"

// BuildWriterPrompt 是 writer 系统提示词的唯一组装入口,生产 / eval / 测试共用,
// 保证 A/B 两臂走同一路径(先例教训见 WithSimulationGuidance)。
// writerPrompt 为含占位符的协议模板(可以已带 simulation guidance 后缀,占位符在
// 前缀内,替换不受影响);style 为空时不追加。
func BuildWriterPrompt(writerPrompt, voice, style string) string {
	out := strings.Replace(writerPrompt, voicePlaceholder, strings.TrimSpace(voice), 1)
	if style != "" {
		out += "\n\n" + style
	}
	return out
}

// OverrideVoice 用 raw 整体替换已组装的文风段(eval 做 voice A/B 用)。
// variant 与 baseline 仍经 BuildWriterPrompt 同一路径组装。
func (b *Bundle) OverrideVoice(raw string) {
	b.Voice = raw
}

// resolveAppendable 追加语义的三层组装:内置保留,全局/本书作为标记段追加。
// 无覆盖时返回内置原文(逐字节不变——文风层验收标准之一)。
// "后者优先"是给 LLM 的优先级指示而非机械保证;需要机械保证的约束走 rules 层。
func resolveAppendable(builtin, name string, opts LoadOptions) string {
	out := builtin
	if s := readOverride(opts.HomeStyleDir, name); s != "" {
		out += "\n\n## 用户全局文风覆盖（以下要求优先于项目默认）\n\n" + s
	}
	if s := readOverride(opts.BookStyleDir, name); s != "" {
		out += "\n\n## 本书文风覆盖（以下要求优先于以上全部）\n\n" + s
	}
	return out
}

// readOverride 读取覆盖目录下的单个文件;目录为空、文件不存在或为空白一律返回 ""。
func readOverride(dir, name string) string {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// styleNameRe 校验用户自定义 style 文件名(不含扩展名),拒绝路径字符。
var styleNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func loadReferences(style string, opts LoadOptions) tools.References {
	if style == "" {
		style = "default"
	}
	refs := tools.References{
		ChapterGuide:      mustRead(referencesFS, "references/chapter-guide.md"),
		HookTechniques:    mustRead(referencesFS, "references/hook-techniques.md"),
		QualityChecklist:  mustRead(referencesFS, "references/quality-checklist.md"),
		OutlineTemplate:   mustRead(referencesFS, "references/outline-template.md"),
		CharacterTemplate: mustRead(referencesFS, "references/character-template.md"),
		ChapterTemplate:   mustRead(referencesFS, "references/chapter-template.md"),
		Consistency:       mustRead(referencesFS, "references/consistency.md"),
		ContentExpansion:  mustRead(referencesFS, "references/content-expansion.md"),
		DialogueWriting:   mustRead(referencesFS, "references/dialogue-writing.md"),
		LongformPlanning:  mustRead(referencesFS, "references/longform-planning.md"),
		Differentiation:   mustRead(referencesFS, "references/differentiation.md"),
		AntiAITone:        resolveAppendable(mustRead(referencesFS, "references/anti-ai-tone.md"), "anti-ai-tone.md", opts),
	}
	if style != "" && style != "default" {
		genreDir := "references/genres/" + style + "/"
		if data, err := referencesFS.ReadFile(genreDir + "style-references.md"); err == nil {
			refs.StyleReference = string(data)
		}
		if data, err := referencesFS.ReadFile(genreDir + "arc-templates.md"); err == nil {
			refs.ArcTemplates = string(data)
		}
		// 题材风格参考:同名整文件替换(本书 > 全局);自定义 style 无内置参考时
		// 允许仅由覆盖提供,不回退 default(错误的参照比没有更糟)。
		relPath := filepath.Join("genres", style, "style-references.md")
		for _, dir := range []string{opts.HomeStyleDir, opts.BookStyleDir} {
			if s := readOverride(dir, relPath); s != "" {
				refs.StyleReference = s
			}
		}
	}
	return refs
}

func loadPrompts() Prompts {
	return Prompts{
		ArchitectShort:   WithSimulationGuidance(mustRead(promptsFS, "prompts/architect-short.md"), "architect"),
		ArchitectLong:    WithSimulationGuidance(mustRead(promptsFS, "prompts/architect-long.md"), "architect"),
		Writer:           WithSimulationGuidance(mustRead(promptsFS, "prompts/writer.md"), "writer"),
		Editor:           WithSimulationGuidance(mustRead(promptsFS, "prompts/editor.md"), "editor"),
		ImportSegment:    mustRead(promptsFS, "prompts/import-segment.md"),
		ImportAnalyze:    mustRead(promptsFS, "prompts/import-analyze.md"),
		ImportSynthesize: mustRead(promptsFS, "prompts/import-synthesize.md"),
		ImportRange:      mustRead(promptsFS, "prompts/import-range.md"),
		SimulationSource: mustRead(promptsFS, "prompts/simulation-source.md"),
		SimulationMerge:  mustRead(promptsFS, "prompts/simulation-merge.md"),

		ArbiterPlanStart:    mustRead(promptsFS, "prompts/arbiter-plan-start.md"),
		ArbiterIntervention: mustRead(promptsFS, "prompts/arbiter-intervention.md"),
		ArbiterFailure:      mustRead(promptsFS, "prompts/arbiter-failure.md"),
	}
}

// WithSimulationGuidance 给核心 prompt 追加仿写画像指引。导出供 eval 等外部场景做
// variant 覆盖时复用，保证覆盖后的 prompt 与 Load 产出的 baseline 等价（同一包装路径）。
func WithSimulationGuidance(prompt, role string) string {
	return prompt + "\n\n" + strings.ReplaceAll(simulationGuidance, "{{role}}", role)
}

// OverridePrompt 用 raw 覆盖 bundle 中指定 prompt 文件对应的角色提示词，并走与 Load
// 完全相同的 WithSimulationGuidance 包装——eval 做 A/B 时只需调它，不必复制包装逻辑，
// 否则 baseline 带仿写画像后缀、variant 不带，A/B 不等价。file 为 prompt 文件名。
// 注意:覆盖 writer.md 时 raw 须自带 {{VOICE}} 占位符(协议模板语义);只想 A/B 文风
// 用 OverrideVoice。
func (b *Bundle) OverridePrompt(file, raw string) error {
	role, ok := promptRole[file]
	if !ok {
		return fmt.Errorf("不支持覆盖的 prompt 文件: %s（仅核心提示词可覆盖）", file)
	}
	wrapped := WithSimulationGuidance(raw, role)
	switch file {
	case "architect-short.md":
		b.Prompts.ArchitectShort = wrapped
	case "architect-long.md":
		b.Prompts.ArchitectLong = wrapped
	case "writer.md":
		b.Prompts.Writer = wrapped
	case "editor.md":
		b.Prompts.Editor = wrapped
	}
	return nil
}

// promptRole 把核心 prompt 文件名映射到 simulation guidance 的角色占位符。
var promptRole = map[string]string{
	"architect-short.md": "architect",
	"architect-long.md":  "architect",
	"writer.md":          "writer",
	"editor.md":          "editor",
}

const simulationGuidance = `## 仿写画像

当 novel_context 返回 simulation_profile 时，必须把它视为当前作品的仿写方向约束。{{role}} 应读取其中的 style、lexicon、plot_design、hook_design、pacing_density、reader_engagement 和 role_guidance。

使用原则：借鉴结构、节奏、钩子、信息释放和吸引读者的手法；不要复制原文句子、人物、地名、专有设定或固定桥段。若 simulation_profile 与用户显式要求冲突，优先服从用户要求。`

// loadStyles 枚举内置风格预设,再按 全局 → 本书 顺序叠加覆盖目录下 styles/*.md
// (同名整文件替换,新文件名即新增风格;风格是整体声音,不做合并)。
func loadStyles(opts LoadOptions) map[string]string {
	styles := make(map[string]string)
	entries, err := stylesFS.ReadDir("styles")
	if err != nil {
		return styles
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		data, err := stylesFS.ReadFile("styles/" + e.Name())
		if err != nil {
			continue
		}
		styles[name] = string(data)
	}
	for _, dir := range []string{opts.HomeStyleDir, opts.BookStyleDir} {
		overlayStyles(styles, dir)
	}
	return styles
}

// overlayStyles 把 <dir>/styles/*.md 叠进 styles 集合;非法文件名跳过并告警。
func overlayStyles(styles map[string]string, dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(filepath.Join(dir, "styles"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if !styleNameRe.MatchString(name) {
			slog.Warn("忽略非法风格文件名", "module", "assets", "dir", dir, "file", e.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "styles", e.Name()))
		if err != nil {
			continue
		}
		styles[name] = string(data)
	}
}

func mustRead(fs embed.FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embed read %s: %v", path, err))
	}
	return string(data)
}
