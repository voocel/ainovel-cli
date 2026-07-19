package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildWriterPrompt_ByteIdenticalToPreSplit 是文风层验收标准 ①:
// 不放任何覆盖文件时,组装产物与拆分前的 writer.md 管线逐字节一致。
// golden 是拆分前 writer.md 的原始快照(testdata/writer-golden.md)。
func TestBuildWriterPrompt_ByteIdenticalToPreSplit(t *testing.T) {
	golden, err := os.ReadFile("testdata/writer-golden.md")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	protocol := mustRead(promptsFS, "prompts/writer.md")
	voice := mustRead(voiceFS, "voice.md")

	// 文件级:占位符回填 == 拆分前原文
	if got := strings.Replace(protocol, voicePlaceholder, strings.TrimSpace(voice), 1); got != string(golden) {
		t.Fatalf("占位符回填与拆分前不一致:\n--- 长度 golden=%d got=%d", len(golden), len(got))
	}

	// 管线级:新组装 == 旧管线(writer.md → simGuidance → style)
	const style = "## 某风格\n\n- 测试"
	old := WithSimulationGuidance(string(golden), "writer") + "\n\n" + style
	got := BuildWriterPrompt(WithSimulationGuidance(protocol, "writer"), voice, style)
	if got != old {
		t.Fatal("组装管线与拆分前不等价")
	}

	// 无风格追加时也等价
	if BuildWriterPrompt(WithSimulationGuidance(protocol, "writer"), voice, "") != WithSimulationGuidance(string(golden), "writer") {
		t.Fatal("无 style 时组装管线与拆分前不等价")
	}
}

// TestLoad_NoOverrides 零覆盖时 Voice/AntiAITone 与内置逐字节一致。
func TestLoad_NoOverrides(t *testing.T) {
	b := Load("default", LoadOptions{})
	if b.Voice != mustRead(voiceFS, "voice.md") {
		t.Fatal("无覆盖时 Voice 应与内置逐字节一致")
	}
	if b.References.AntiAITone != mustRead(referencesFS, "references/anti-ai-tone.md") {
		t.Fatal("无覆盖时 AntiAITone 应与内置逐字节一致")
	}
	if _, ok := b.Styles["default"]; !ok {
		t.Fatal("内置风格集应含 default")
	}
}

func TestInterventionPromptsKeepScopeContract(t *testing.T) {
	prompts := loadPrompts()
	for _, phrase := range []string{"上下文不等于修改授权", "最小充分范围", "分析范围不等于修改范围"} {
		if !strings.Contains(prompts.ArbiterIntervention, phrase) {
			t.Fatalf("Arbiter 干预提示缺少范围契约 %q", phrase)
		}
	}
	for _, phrase := range []string{"用户原始干预", "分析范围不等于修改范围", "最小充分章节集合"} {
		if !strings.Contains(prompts.Editor, phrase) {
			t.Fatalf("Editor 提示缺少范围契约 %q", phrase)
		}
	}
}

func TestStructuredArbiterPromptsContainOnlySemantics(t *testing.T) {
	prompts := loadPrompts()
	for name, prompt := range map[string]string{
		"plan_start": prompts.ArbiterPlanStart,
		"failure":    prompts.ArbiterFailure,
	} {
		for _, duplicate := range []string{"```json", "不要 Markdown", "输出一个 JSON 对象"} {
			if strings.Contains(prompt, duplicate) {
				t.Fatalf("%s 提示词仍重复维护输出格式 %q", name, duplicate)
			}
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestLoad_ThreeTierAppendAndReplace 覆盖三层优先级与逐资产语义(验收标准 ②)。
func TestLoad_ThreeTierAppendAndReplace(t *testing.T) {
	home := t.TempDir()
	book := t.TempDir()
	opts := LoadOptions{HomeStyleDir: home, BookStyleDir: book}

	// voice / anti-ai-tone:追加语义,全局在前、本书在后,带边界标记
	writeFile(t, filepath.Join(home, "voice.md"), "全局:少用成语")
	writeFile(t, filepath.Join(book, "voice.md"), "本书:多写对话")
	writeFile(t, filepath.Join(book, "anti-ai-tone.md"), "本书判据:禁排比")

	// styles:同名整文件替换 + 新名新增;非法名忽略
	writeFile(t, filepath.Join(home, "styles", "fantasy.md"), "全局改写的奇幻")
	writeFile(t, filepath.Join(book, "styles", "xianxia.md"), "自定义仙侠")
	writeFile(t, filepath.Join(book, "styles", "Bad Name!.md"), "非法")

	// 题材参考:同名整文件替换,本书 > 全局
	writeFile(t, filepath.Join(home, "genres", "fantasy", "style-references.md"), "全局参考")
	writeFile(t, filepath.Join(book, "genres", "fantasy", "style-references.md"), "本书参考")

	b := Load("fantasy", opts)

	builtinVoice := mustRead(voiceFS, "voice.md")
	if !strings.HasPrefix(b.Voice, builtinVoice) {
		t.Fatal("追加语义必须保留内置原文为前缀")
	}
	giIdx := strings.Index(b.Voice, "## 用户全局文风覆盖")
	bkIdx := strings.Index(b.Voice, "## 本书文风覆盖")
	if giIdx < 0 || bkIdx < 0 || giIdx > bkIdx {
		t.Fatalf("追加段顺序错误:global=%d book=%d", giIdx, bkIdx)
	}
	if !strings.Contains(b.Voice, "全局:少用成语") || !strings.Contains(b.Voice, "本书:多写对话") {
		t.Fatal("覆盖内容缺失")
	}
	if !strings.Contains(b.References.AntiAITone, "本书判据:禁排比") {
		t.Fatal("anti-ai-tone 本书追加缺失")
	}

	if b.Styles["fantasy"] != "全局改写的奇幻" {
		t.Fatal("styles 同名应整文件替换")
	}
	if b.Styles["xianxia"] != "自定义仙侠" {
		t.Fatal("新增自定义风格应即放即用")
	}
	if _, ok := b.Styles["Bad Name!"]; ok {
		t.Fatal("非法风格名必须被忽略")
	}

	if b.References.StyleReference != "本书参考" {
		t.Fatalf("题材参考应为本书覆盖优先,got %q", b.References.StyleReference)
	}
}

// TestLoad_BookOverridesHomeOnStyles 本书 styles 覆盖全局同名。
func TestLoad_BookOverridesHomeOnStyles(t *testing.T) {
	home := t.TempDir()
	book := t.TempDir()
	writeFile(t, filepath.Join(home, "styles", "romance.md"), "全局版")
	writeFile(t, filepath.Join(book, "styles", "romance.md"), "本书版")
	b := Load("default", LoadOptions{HomeStyleDir: home, BookStyleDir: book})
	if b.Styles["romance"] != "本书版" {
		t.Fatalf("本书应覆盖全局,got %q", b.Styles["romance"])
	}
}

// TestOverrideVoice_SharesAssemblyPath eval 的 voice A/B 与生产同组装路径(验收标准 ④)。
func TestOverrideVoice_SharesAssemblyPath(t *testing.T) {
	b := Load("default", LoadOptions{})
	b.OverrideVoice("## 实验文风\n\n- 一句话")
	got := BuildWriterPrompt(b.Prompts.Writer, b.Voice, "")
	if !strings.Contains(got, "## 实验文风") {
		t.Fatal("OverrideVoice 未生效")
	}
	if strings.Contains(got, voicePlaceholder) {
		t.Fatal("占位符必须被消耗")
	}
	// 协议部分不受 voice 覆盖影响
	if !strings.Contains(got, "## 执行协议") {
		t.Fatal("协议模板不得被 voice 覆盖破坏")
	}
}
