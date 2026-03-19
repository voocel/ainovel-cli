package assets

import (
	"embed"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/tools"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles/*.md
var stylesFS embed.FS

// Prompts 表示嵌入的提示词集合。
type Prompts struct {
	Coordinator    string
	ArchitectShort string
	ArchitectMid   string
	ArchitectLong  string
	Writer         string
	Editor         string
}

// Bundle 表示运行所需的静态资源集合。
type Bundle struct {
	References tools.References
	Prompts    Prompts
	Styles     map[string]string
}

// Load 返回指定风格对应的资源集合。
func Load(style string) Bundle {
	return Bundle{
		References: loadReferences(style),
		Prompts:    loadPrompts(),
		Styles:     loadStyles(),
	}
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
	}
	if style != "" && style != "default" {
		path := "references/" + style + "/style-references.md"
		if data, err := referencesFS.ReadFile(path); err == nil {
			refs.StyleReference = string(data)
		}
	}
	return refs
}

func loadPrompts() Prompts {
	return Prompts{
		Coordinator:    mustRead(promptsFS, "prompts/coordinator.md"),
		ArchitectShort: mustRead(promptsFS, "prompts/architect-short.md"),
		ArchitectMid:   mustRead(promptsFS, "prompts/architect-mid.md"),
		ArchitectLong:  mustRead(promptsFS, "prompts/architect-long.md"),
		Writer:         mustRead(promptsFS, "prompts/writer.md"),
		Editor:         mustRead(promptsFS, "prompts/editor.md"),
	}
}

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
