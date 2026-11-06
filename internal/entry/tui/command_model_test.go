package tui

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

type fakeModelRuntime struct {
	providers   []string
	models      map[string][]host.ConfiguredModel
	curProvider string
	curModel    string
	thinking    map[string]string // role -> 存储的原始意图
	available   []bootstrap.ThinkingLevel
	setCalls    []struct{ role, level string }
	switchCalls int
}

func (f *fakeModelRuntime) ConfiguredProviders() []string { return f.providers }
func (f *fakeModelRuntime) ConfiguredModelOptions(provider string) []host.ConfiguredModel {
	return f.models[provider]
}
func (f *fakeModelRuntime) CurrentModelSelection(role string) (string, string, bool) {
	return f.curProvider, f.curModel, true
}
func (f *fakeModelRuntime) AvailableThinking(role string) []bootstrap.ThinkingLevel {
	return f.available
}
func (f *fakeModelRuntime) CurrentThinking(role string) string { return f.thinking[role] }
func (f *fakeModelRuntime) SwitchModel(role, provider, model string) error {
	f.switchCalls++
	f.curProvider, f.curModel = provider, model
	return nil
}
func (f *fakeModelRuntime) SetRoleThinking(role, level string) error {
	f.setCalls = append(f.setCalls, struct{ role, level string }{role, level})
	if f.thinking == nil {
		f.thinking = map[string]string{}
	}
	f.thinking[role] = level
	return nil
}

// 存储的强度意图高于当前模型能力、面板无法呈现时，用户不动强度字段直接应用，
// 不应把意图误抹成初始默认值。
func TestModelSwitchKeepsUnrepresentableThinkingIntent(t *testing.T) {
	rt := &fakeModelRuntime{
		providers:   []string{"proxy"},
		models:      map[string][]host.ConfiguredModel{"proxy": {{Name: "chat-only"}}},
		curProvider: "proxy", curModel: "chat-only",
		thinking:  map[string]string{"writer": "high"},
		available: nil, // 当前模型只有“继承”一档
	}
	st := newModelSwitchState(rt, "writer")
	if st.thinkingKey() != "" {
		t.Fatalf("high 无法呈现时面板应落在继承档，得到 %q", st.thinkingKey())
	}
	if err := st.apply(rt); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rt.setCalls) != 0 {
		t.Fatalf("未改动强度不应回写：%+v", rt.setCalls)
	}
	if rt.thinking["writer"] != "high" {
		t.Fatalf("意图被抹成 %q，应保留 high", rt.thinking["writer"])
	}
}

// 用户在面板里显式改动强度，则应回写为新值。
func TestModelSwitchAppliesExplicitThinkingChange(t *testing.T) {
	rt := &fakeModelRuntime{
		providers:   []string{"proxy"},
		models:      map[string][]host.ConfiguredModel{"proxy": {{Name: "m"}}},
		curProvider: "proxy", curModel: "m",
		thinking:  map[string]string{"writer": ""},
		available: []bootstrap.ThinkingLevel{bootstrap.ThinkingLow, bootstrap.ThinkingHigh},
	}
	st := newModelSwitchState(rt, "writer")
	st.focus = modelFocusThinking
	st.cycle(1, rt) // 移动强度字段
	want := st.thinkingKey()
	if want == "" {
		t.Fatal("测试前置：应已移动到某个非空强度档")
	}
	if err := st.apply(rt); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rt.setCalls) != 1 || rt.setCalls[0].level != want {
		t.Fatalf("显式改动应回写 %q，得到 %+v", want, rt.setCalls)
	}
}
