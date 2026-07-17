package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

func hubFieldIDs(fields []hubField) []string {
	ids := make([]string, len(fields))
	for i, f := range fields {
		ids[i] = f.id
	}
	return ids
}

// 选中已有 Provider 应进入详情 hub（先看信息，再逐一调整），而不是直接跳进“改协议”。
func TestSelectingProviderOpensHub(t *testing.T) {
	st := &modelConfigState{editModelIdx: -1}
	st.applyProviderChoice(configProviderChoice{existing: &host.ProviderSnapshot{
		Name: "openrouter", BaseURL: "u", HasAPIKey: true,
		Models: []bootstrap.ModelConfig{{Name: "m"}},
	}})
	if st.step != configStepHub {
		t.Fatalf("选中已有 Provider 应进入 hub，得到 step=%d", st.step)
	}
	ids := hubFieldIDs(st.hubFields())
	// 内置 provider（type 空）不铺 协议/Endpoint 噪音，但保留 key/models/save。
	if slices.Contains(ids, "protocol") || slices.Contains(ids, "api") {
		t.Fatalf("内置 provider hub 不应出现协议/Endpoint，得到 %v", ids)
	}
	for _, want := range []string{"key", "baseurl", "models", "save"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("hub 缺少 %q，得到 %v", want, ids)
		}
	}
}

// 自定义（显式 openai 协议）Provider 的 hub 才展示协议与 Endpoint。
func TestCustomProviderHubShowsProtocolAndEndpoint(t *testing.T) {
	st := &modelConfigState{editModelIdx: -1}
	st.applyProviderChoice(configProviderChoice{existing: &host.ProviderSnapshot{
		Name: "proxy", Type: "openai", API: "responses", HasAPIKey: true,
		Models: []bootstrap.ModelConfig{{Name: "m"}},
	}})
	ids := hubFieldIDs(st.hubFields())
	if !slices.Contains(ids, "protocol") || !slices.Contains(ids, "api") {
		t.Fatalf("自定义 openai provider hub 应含协议/Endpoint，得到 %v", ids)
	}
}

// Esc 逐级返回：字段编辑器 → hub → Provider 列表 → 关闭。
func TestEscapeBackHierarchy(t *testing.T) {
	st := &modelConfigState{}
	st.step = configStepBaseURL
	if got, ok := st.escapeBack(); !ok || got != configStepHub {
		t.Fatalf("字段编辑器 Esc 应回 hub，得到 %d,%v", got, ok)
	}
	st.step = configStepHub
	if got, ok := st.escapeBack(); !ok || got != configStepProvider {
		t.Fatalf("hub Esc 应回列表，得到 %d,%v", got, ok)
	}
	st.step = configStepProvider
	if _, ok := st.escapeBack(); ok {
		t.Fatal("列表 Esc 应关闭整个面板")
	}
}

// 模型列表末项恒为“新增模型”入口，Enter 进入命名（不再靠隐藏的 A 快捷键）。
func TestModelListAddEntryOpensNameInput(t *testing.T) {
	st := &modelConfigState{step: configStepModels, editModelIdx: -1,
		models: []bootstrap.ModelConfig{{Name: "m1"}}}
	st.cursor = len(st.models) // 停在“+ 新增模型…”
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.step != configStepModelName || st.editModelIdx != -1 {
		t.Fatalf("选中新增入口应进入命名(step=%d, idx=%d)", st.step, st.editModelIdx)
	}
}

// 选中已有模型 Enter → 进入该模型详情（设默认/改窗口/删除），而不是隐藏的 E/D。
func TestSelectingModelOpensDetail(t *testing.T) {
	st := &modelConfigState{step: configStepModels, editModelIdx: -1,
		models: []bootstrap.ModelConfig{{Name: "m1"}, {Name: "m2"}}}
	st.cursor = 1
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.step != configStepModelDetail || st.editModelIdx != 1 {
		t.Fatalf("选中模型应进入详情(step=%d, idx=%d)", st.step, st.editModelIdx)
	}
	ids := hubFieldIDs(st.modelDetailFields())
	// 详情只留 上下文窗口 / 删除；“设默认”已移除（切换归 /model）。
	if slices.Contains(ids, "default") {
		t.Fatalf("模型详情不应再有“设为默认”，得到 %v", ids)
	}
	for _, want := range []string{"window", "delete"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("模型详情缺少 %q，得到 %v", want, ids)
		}
	}
}

// 改已有模型窗口后退回其详情、Esc 也逐级回详情→列表（新增流程则退回命名）。
func TestModelWindowEscapeHierarchy(t *testing.T) {
	editing := &modelConfigState{step: configStepModelWindow, editModelIdx: 0}
	if got, ok := editing.escapeBack(); !ok || got != configStepModelDetail {
		t.Fatalf("改已有模型窗口 Esc 应回详情，得到 %d,%v", got, ok)
	}
	adding := &modelConfigState{step: configStepModelWindow, editModelIdx: -1}
	if got, ok := adding.escapeBack(); !ok || got != configStepModelName {
		t.Fatalf("新增流程窗口 Esc 应回命名，得到 %d,%v", got, ok)
	}
	detail := &modelConfigState{step: configStepModelDetail}
	if got, ok := detail.escapeBack(); !ok || got != configStepModels {
		t.Fatalf("模型详情 Esc 应回列表，得到 %d,%v", got, ok)
	}
}

func TestParseContextWindowInput(t *testing.T) {
	cases := map[string]int{
		"": 0, "0": 0, "auto": 0, "128K": 128000, "1M": 1000000,
		"1.5m": 1500000, "200000": 200000,
	}
	for input, want := range cases {
		got, err := parseContextWindowInput(input)
		if err != nil || got != want {
			t.Errorf("parseContextWindowInput(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "abc", "0.5"} {
		if _, err := parseContextWindowInput(input); err == nil {
			t.Errorf("parseContextWindowInput(%q) should fail", input)
		}
	}
}

func TestModelConfigModalDoesNotRenderAPIKey(t *testing.T) {
	state := &modelConfigState{step: configStepKeyInput, input: "sk-super-secret"}
	view := renderModelConfigModal(120, state)
	if strings.Contains(view, "sk-super-secret") {
		t.Fatal("API key leaked into rendered modal")
	}
}

func TestConfigCommandIsRegistered(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("config")
	if !ok {
		t.Fatal("/config is not registered")
	}
	if spec.Usage != "/config" || !spec.AutoExecute {
		t.Fatalf("config spec = %#v", spec)
	}
}

func TestModelSwitchLabelIncludesContextWindow(t *testing.T) {
	state := modelSwitchState{models: []host.ConfiguredModel{{Name: "gpt-test", ContextWindow: 400000}}}
	if got := state.modelLabel(); got != "gpt-test · 400K" {
		t.Fatalf("modelLabel = %q", got)
	}
}

// 与 /model 一致：/config 渲染成内容高度的带框浮层（不再撑成 3/4 屏的居中蒙层）。
func TestModelConfigModalIsCompactOverlay(t *testing.T) {
	state := &modelConfigState{step: configStepProvider, providerChoices: []configProviderChoice{
		{label: "编辑 openrouter", existing: &host.ProviderSnapshot{Name: "openrouter"}},
		{label: "+ 新增 Provider…", add: true},
	}}
	lines := strings.Split(renderModelConfigModal(120, state), "\n")

	// 1 标题行 + 2 选项 + 上下边框 = 5 行；高度随内容走，不会因为屏高而膨胀。
	if len(lines) != 5 {
		t.Fatalf("紧凑浮层应为 5 行（内容高度），得到 %d 行:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "┌") || !strings.Contains(lines[0], "/config") {
		t.Fatalf("首行应是带 /config 标题的上边框，得到 %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "└") {
		t.Fatalf("末行应是下边框，得到 %q", lines[len(lines)-1])
	}
}

// 一级菜单只列“编辑已有 + 新增入口”，不铺开整份内置 Provider 目录；目录只在二级菜单出现。
func TestProviderMenuIsTwoLevel(t *testing.T) {
	state := &modelConfigState{snapshot: host.ModelConfigurationSnapshot{
		Providers:       []host.ProviderSnapshot{{Name: "openrouter"}, {Name: "anthropic"}},
		DefaultProvider: "openrouter",
	}}
	state.buildProviderMenus()

	// 一级 = 2 个编辑 + 1 个新增入口；末项是“新增”，且没有别的 add/preset 混入。
	if len(state.providerChoices) != 3 {
		t.Fatalf("一级菜单应为 2 编辑 + 1 新增，得到 %d 项", len(state.providerChoices))
	}
	if !state.providerChoices[len(state.providerChoices)-1].add {
		t.Fatal("一级菜单末项应是“新增 Provider…”入口")
	}
	for i, c := range state.providerChoices[:2] {
		if c.existing == nil || c.add {
			t.Fatalf("一级菜单第 %d 项应为编辑已有 Provider，得到 %#v", i, c)
		}
	}

	// 二级 = 可新增目录：非空，且已配置的内置项（openrouter/anthropic）不再重复出现。
	if len(state.presetChoices) == 0 {
		t.Fatal("二级菜单应列出可新增的 Provider 目录")
	}
	if len(state.presetChoices) >= len(bootstrap.ProviderPresets()) {
		t.Fatalf("已配置的内置 Provider 应从新增目录中剔除，presets=%d 全量=%d",
			len(state.presetChoices), len(bootstrap.ProviderPresets()))
	}
	for _, c := range state.presetChoices {
		if c.preset != nil && (c.preset.Name == "openrouter" || c.preset.Name == "anthropic") {
			t.Fatalf("新增目录不应包含已配置的 %q", c.preset.Name)
		}
	}
}

func TestAtlasCloudPresetUsesOpenAICompatibleDefaults(t *testing.T) {
	var preset *bootstrap.ProviderPreset
	for _, value := range bootstrap.ProviderPresets() {
		if value.Name == "atlascloud" {
			copyValue := value
			preset = &copyValue
			break
		}
	}
	if preset == nil {
		t.Fatal("atlascloud preset not found")
	}

	state := &modelConfigState{}
	state.applyProviderChoice(configProviderChoice{label: preset.Label, preset: preset})
	if state.provider != "atlascloud" || state.providerType != "openai" {
		t.Fatalf("provider/type = %q/%q", state.provider, state.providerType)
	}
	if state.baseURL != "https://api.atlascloud.ai/v1" {
		t.Fatalf("baseURL = %q", state.baseURL)
	}
	if len(state.models) != 2 || state.models[0].Name != "qwen/qwen3.5-flash" {
		t.Fatalf("atlascloud models = %#v", state.models)
	}
	ids := hubFieldIDs(state.hubFields())
	if !slices.Contains(ids, "protocol") || !slices.Contains(ids, "api") {
		t.Fatalf("atlascloud hub should expose OpenAI protocol and endpoint fields, got %v", ids)
	}
}
