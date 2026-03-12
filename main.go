package main

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/app"
	"github.com/voocel/ainovel-cli/tools"
	"github.com/voocel/ainovel-cli/tui"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles/*.md
var stylesFS embed.FS

func main() {
	style := envOr("NOVEL_STYLE", "default")
	refs := loadReferences(style)
	prompts := loadPrompts()
	styles := loadStyles()
	cfg := buildConfig(style)

	prompt := parsePrompt()
	if prompt != "" {
		// CLI 模式：有命令行参数，直接运行
		cfg.Prompt = prompt
		if err := app.Run(cfg, refs, prompts, styles); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// TUI 模式：无命令行参数，启动交互界面
	if err := tui.Run(cfg, refs, prompts, styles); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func buildConfig(style string) app.Config {
	provider := envOr("LLM_PROVIDER", "openrouter")
	apiKey := os.Getenv("Z_OPENAI_API_KEY")
	baseURL := os.Getenv("Z_OPENAI_BASE_URL")
	switch provider {
	case "anthropic":
		apiKey = envOr("ANTHROPIC_API_KEY", apiKey)
		baseURL = envOr("ANTHROPIC_BASE_URL", baseURL)
	case "gemini":
		apiKey = envOr("GEMINI_API_KEY", apiKey)
		baseURL = envOr("GEMINI_BASE_URL", baseURL)
	case "openrouter":
		apiKey = envOr("OPENROUTER_API_KEY", apiKey)
		baseURL = envOr("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")
	}

	cfg := app.Config{
		NovelName: "novel",
		Provider:  provider,
		APIKey:    apiKey,
		BaseURL:   baseURL,
		ModelName: "stepfun/step-3.5-flash:free",
		Style:     style,
	}
	return cfg
}

func parsePrompt() string {
	if len(os.Args) < 2 {
		return ""
	}
	return strings.Join(os.Args[1:], " ")
}

func loadReferences(style string) tools.References {
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

func loadPrompts() app.Prompts {
	return app.Prompts{
		Coordinator:    mustRead(promptsFS, "prompts/coordinator.md"),
		ArchitectShort: mustRead(promptsFS, "prompts/architect-short.md"),
		ArchitectMid:   mustRead(promptsFS, "prompts/architect-mid.md"),
		ArchitectLong:  mustRead(promptsFS, "prompts/architect-long.md"),
		Writer:         mustRead(promptsFS, "prompts/writer.md"),
		Editor:         mustRead(promptsFS, "prompts/editor.md"),
	}
}

func mustRead(fs embed.FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embed read %s: %v", path, err))
	}
	return string(data)
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
