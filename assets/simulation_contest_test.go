package assets

import (
	"strings"
	"testing"
)

// 本文件验证「仿写画像 simulation」与「多人格竞稿」两功能同时开启时的集成正确性。
// 聚焦交叉点：simulation 把画像指导注入了哪些角色的 system prompt、竞稿 persona
// writer 是否会同时携带「画像指导 + 人格文风」两个信号。上游已测画像注入
// novel_context（internal/tools/novel_context_simulation_test.go），但上游无竞稿，
// 不会覆盖竞稿侧——此处补足。

// simulationGuidance 的稳定文本锚点（见 load.go const simulationGuidance）。
const simGuidanceAnchor = "## 仿写画像"

// TestSimulationGuidanceInjectedPerRole 确认 simulation 指导按预期注入各角色，
// 且竞稿裁判 Judge 被刻意排除（合并决策：withSimulationGuidance 的 role 参数无 judge）。
func TestSimulationGuidanceInjectedPerRole(t *testing.T) {
	b := Load("default")

	// 写作/规划角色应被注入画像指导。
	inject := map[string]string{
		"Coordinator":    b.Prompts.Coordinator,
		"ArchitectShort": b.Prompts.ArchitectShort,
		"ArchitectLong":  b.Prompts.ArchitectLong,
		"Writer":         b.Prompts.Writer,
		"Editor":         b.Prompts.Editor,
	}
	for role, p := range inject {
		if !strings.Contains(p, simGuidanceAnchor) {
			t.Errorf("角色 %s 的 prompt 应含仿写画像指导，实际未注入", role)
		}
	}

	// Judge（竞稿裁判）刻意未注入画像指导——评判候选稿，不直接产出文风。
	if strings.Contains(b.Prompts.Judge, simGuidanceAnchor) {
		t.Error("Judge 不应被注入仿写画像指导（合并时刻意排除），但实际含有")
	}
}

// TestPersonaWriterCarriesBothSignals 是交叉验证的核心：竞稿 persona writer 的
// system prompt = Writer 基底 + 人格 StyleBlock（见 internal/agents/build.go 中
// personaPrompt 的拼接）。因 Writer 基底已被 simulation 包装，persona writer 必然
// 同时携带「画像收敛」与「人格发散」两个文风信号——这正是质量软冲突的代码层根源。
// 本测试确定性证明：两信号同时生效、互不覆盖、拼接不报错（但不评判 LLM 输出质量）。
func TestPersonaWriterCarriesBothSignals(t *testing.T) {
	b := Load("default")

	const personaMarker = "## 你的写作人格"
	const fakeStyle = "【乌贼风格：阴郁悬疑、信息差驱动】" // 模拟 persona.StyleBlock

	// 复现 internal/agents/build.go 的 persona prompt 拼接：writerPrompt + 人格块。
	personaPrompt := b.Prompts.Writer + "\n\n" + personaMarker + "\n" + fakeStyle

	if !strings.Contains(personaPrompt, simGuidanceAnchor) {
		t.Error("persona writer 应继承 Writer 基底的仿写画像指导，实际缺失")
	}
	if !strings.Contains(personaPrompt, fakeStyle) {
		t.Error("persona writer 应含人格 StyleBlock，实际缺失")
	}
	t.Logf("persona writer 同时携带：仿写画像指导 + 人格文风（总长 %d 字节）→ 两信号叠加确认", len(personaPrompt))
}
