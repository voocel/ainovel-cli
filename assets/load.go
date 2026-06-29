package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/voocel/ainovel-cli/internal/tools"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles/*.md
var stylesFS embed.FS

//go:embed rules
var rulesFS embed.FS

// Prompts 表示嵌入的提示词集合。
type Prompts struct {
	Coordinator      string
	ArchitectShort   string
	ArchitectLong    string
	Writer           string
	Editor           string
	ImportFoundation string
	ImportAnalyzer   string
	SimulationSource string
	SimulationMerge  string
}

// Bundle 表示运行所需的静态资源集合。
type Bundle struct {
	References tools.References
	Prompts    Prompts
	Styles     map[string]string
	// RulesFS 是 assets/rules 子树（根Thư mục直接包含 default.md）。
	// 调用方传给 rules.Load 作为内置规则来源。
	RulesFS fs.FS
}

// Load Quay lại指定风格对应的资源集合。
func Load(style string) Bundle {
	return Bundle{
		References: loadReferences(style),
		Prompts:    loadPrompts(),
		Styles:     loadStyles(),
		RulesFS:    loadRulesFS(),
	}
}

// loadRulesFS Quay lại assets/rules 的子Tập tin系统；根Thư mục直接包含 default.md。
// fs.Sub Thất bại时（理论不应发生）Quay lại nil，rules.Load 据此Bỏ qua内置来源。
func loadRulesFS() fs.FS {
	sub, err := fs.Sub(rulesFS, "rules")
	if err != nil {
		return nil
	}
	return sub
}

func loadReferences(style string) tools.References {
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
		AntiAITone:        mustRead(referencesFS, "references/anti-ai-tone.md"),
	}
	if style != "" && style != "default" {
		genreDir := "references/genres/" + style + "/"
		if data, err := referencesFS.ReadFile(genreDir + "style-references.md"); err == nil {
			refs.StyleReference = string(data)
		}
		if data, err := referencesFS.ReadFile(genreDir + "arc-templates.md"); err == nil {
			refs.ArcTemplates = string(data)
		}
	}
	return refs
}

func loadPrompts() Prompts {
	return Prompts{
		Coordinator:      withSimulationGuidance(mustRead(promptsFS, "prompts/coordinator.md"), "coordinator"),
		ArchitectShort:   withSimulationGuidance(mustRead(promptsFS, "prompts/architect-short.md"), "architect"),
		ArchitectLong:    withSimulationGuidance(mustRead(promptsFS, "prompts/architect-long.md"), "architect"),
		Writer:           withSimulationGuidance(mustRead(promptsFS, "prompts/writer.md"), "writer"),
		Editor:           withSimulationGuidance(mustRead(promptsFS, "prompts/editor.md"), "editor"),
		ImportFoundation: mustRead(promptsFS, "prompts/import-foundation.md"),
		ImportAnalyzer:   mustRead(promptsFS, "prompts/import-chapter-analyzer.md"),
		SimulationSource: mustRead(promptsFS, "prompts/simulation-source.md"),
		SimulationMerge:  mustRead(promptsFS, "prompts/simulation-merge.md"),
	}
}

func withSimulationGuidance(prompt, role string) string {
	return prompt + "\n\n" + strings.ReplaceAll(simulationGuidance, "{{role}}", role)
}

const simulationGuidance = `## 仿写画像

当 novel_context Quay lại simulation_profile 时，必须把它视为Hiện tại作品的仿写方向约束。{{role}} 应Đọc其中的 style、lexicon、plot_design、hook_design、pacing_density、reader_engagement 和 role_guidance。

使用原则：借鉴结构、节奏、钩子、信息释放和吸引读者的手法；不要复制原文句子、Nhân vật、地名、专有设定或固定桥段。若 simulation_profile 与用户显式要求冲突，优先服从用户要求。`

func loadStyles() map[string]string {
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
	return styles
}

func mustRead(fs embed.FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embed read %s: %v", path, err))
	}
	return string(data)
}
