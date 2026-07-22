package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
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

func hubFieldIndex(fields []hubField, id string) int {
	for i, field := range fields {
		if field.id == id {
			return i
		}
	}
	return -1
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

// Esc 逐级返回：hub 行内编辑 → hub → Provider 列表 → 关闭。
func TestEscapeBackHierarchy(t *testing.T) {
	st := &modelConfigState{step: configStepHub, provider: "proxy"}
	st.beginInlineEdit("baseurl")
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if st.step != configStepHub || st.editingField != "" {
		t.Fatalf("行内编辑 Esc 应留在 hub 并取消输入，得到 step=%d field=%q", st.step, st.editingField)
	}
	if got, ok := st.escapeBack(); !ok || got != configStepProvider {
		t.Fatalf("hub Esc 应回列表，得到 %d,%v", got, ok)
	}
	st.step = configStepProvider
	if _, ok := st.escapeBack(); ok {
		t.Fatal("列表 Esc 应关闭整个面板")
	}
}

func TestModelListAddsAndEditsInPlace(t *testing.T) {
	st := &modelConfigState{step: configStepModels, editModelIdx: -1,
		models: []bootstrap.ModelConfig{{Name: "m1"}}, modelOrigins: []string{"m1"}}
	st.cursor = len(st.models)
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.step != configStepModels || st.editingField != configModelNameField || !st.addingModel {
		t.Fatalf("新增应留在列表行内编辑，step=%d field=%q adding=%v", st.step, st.editingField, st.addingModel)
	}
	st.input.SetValue("  m2  ")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.editingField != configModelWindowField || st.models[1].Name != "m2" {
		t.Fatalf("名称提交后应在同一行进入窗口列，field=%q models=%#v", st.editingField, st.models)
	}
	st.input.SetValue("128K")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.editingField != "" || st.addingModel || st.models[1].ContextWindow != 128000 {
		t.Fatalf("新增模型未在同一页完成: %#v", st)
	}
}

func TestModelListEditsSelectedCellAndCancels(t *testing.T) {
	st := &modelConfigState{step: configStepModels, editModelIdx: -1,
		models: []bootstrap.ModelConfig{{Name: "m1", ContextWindow: 1000}}, modelOrigins: []string{"m1"}}
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyRight})
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.editingField != configModelWindowField || st.step != configStepModels {
		t.Fatalf("右列 Enter 应行内编辑窗口，step=%d field=%q", st.step, st.editingField)
	}
	st.input.SetValue("200K")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if st.editingField != "" || st.models[0].ContextWindow != 1000 {
		t.Fatalf("Esc 应取消当前单元格且不改值: %#v", st.models[0])
	}
}

func TestModelRenameProducesExplicitDraftAndReferenceNotice(t *testing.T) {
	st := &modelConfigState{
		step: configStepModels, provider: "proxy", models: []bootstrap.ModelConfig{{Name: "old"}},
		modelOrigins: []string{"old"}, snapshot: host.ModelConfigurationSnapshot{
			References: map[string][]string{"proxy\x00old": {"default", "writer fallback[0]"}},
		},
	}
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	st.input.SetValue("renamed")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	draft := st.draft()
	if len(draft.Renames) != 1 || draft.Renames[0] != (host.ModelRename{From: "old", To: "renamed"}) {
		t.Fatalf("模型改名必须保留显式身份关系，renames=%#v", draft.Renames)
	}
	if !strings.Contains(st.message, "同步更新引用") || !strings.Contains(st.message, "default") {
		t.Fatalf("引用模型改名应明确提示保存行为，message=%q", st.message)
	}
}

func TestModelListRendersEditableColumnsAndReferences(t *testing.T) {
	st := &modelConfigState{
		step: configStepModels, provider: "proxy", models: []bootstrap.ModelConfig{{Name: "deepseek-chat", ContextWindow: 128000}},
		modelOrigins: []string{"deepseek-chat"}, snapshot: host.ModelConfigurationSnapshot{
			References: map[string][]string{"proxy\x00deepseek-chat": {"default"}},
		},
	}
	plain := ansi.Strip(renderModelConfigModal(120, st))
	for _, want := range []string{"模型 ID", "上下文窗口", "引用", "deepseek-chat", "128K", "default", "+ 新增模型"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("单页模型表缺少 %q:\n%s", want, plain)
		}
	}
}

func TestModelNameInlineEditorKeepsModalRowsIntact(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })
	st := &modelConfigState{
		step: configStepModels, provider: "deepseek",
		models:       []bootstrap.ModelConfig{{Name: "deepseek-v4-pro"}, {Name: "deepseek-v4-flash"}},
		modelOrigins: []string{"deepseek-v4-pro", "deepseek-v4-flash"},
	}
	st.beginModelEdit(0, 0)
	view := renderModelConfigModal(120, st)
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if width := lipgloss.Width(line); width != 72 {
			t.Fatalf("行内模型名编辑破坏了第 %d 行宽度: width=%d line=%q\n%s", i, width, ansi.Strip(line), ansi.Strip(view))
		}
	}
	if len(lines) != 7 {
		t.Fatalf("行内编辑不应引入物理换行，得到 %d 行:\n%s", len(lines), ansi.Strip(view))
	}
}

func TestRenamedReferencedModelStillCannotBeDeleted(t *testing.T) {
	st := &modelConfigState{
		provider: "proxy", currentModel: "old", models: []bootstrap.ModelConfig{{Name: "renamed"}},
		modelOrigins: []string{"old"}, snapshot: host.ModelConfigurationSnapshot{
			References: map[string][]string{"proxy\x00old": {"default"}},
		},
	}
	if st.deleteModel(0) || len(st.models) != 1 || !strings.Contains(st.message, "正在使用") {
		t.Fatalf("重命名尚未保存时仍应按原身份保护删除，models=%#v message=%q", st.models, st.message)
	}
}

func TestCancellingNewModelNameRemovesTemporaryRow(t *testing.T) {
	st := &modelConfigState{step: configStepModels, models: []bootstrap.ModelConfig{{Name: "m1"}}, modelOrigins: []string{"m1"}}
	st.cursor = 1
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if len(st.models) != 1 || len(st.modelOrigins) != 1 || st.cursor != 1 {
		t.Fatalf("取消新增应清理临时行，models=%#v origins=%#v cursor=%d", st.models, st.modelOrigins, st.cursor)
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
	state := &modelConfigState{step: configStepHub, provider: "proxy", apiKeyOptional: true}
	state.beginInlineEdit("key")
	state.input.SetValue("sk-super-secret")
	view := renderModelConfigModal(120, state)
	if strings.Contains(view, "sk-super-secret") {
		t.Fatal("API key leaked into rendered modal")
	}
}

func TestProviderHubEditsAPIKeyInlineAndTrims(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", existing: true,
		hasAPIKey: true, apiKeyHint: "sk-o******7890", apiKeyOptional: true, apiKeyAction: host.APIKeyKeep}
	state.cursor = hubFieldIndex(state.hubFields(), "key")
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.step != configStepHub || state.editingField != "key" {
		t.Fatalf("API Key 应在 hub 原行编辑，得到 step=%d field=%q", state.step, state.editingField)
	}
	state.input.SetValue("  sk-new-secret-1234567890  ")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.editingField != "" || state.apiKey != "sk-new-secret-1234567890" || state.apiKeyAction != host.APIKeyReplace {
		t.Fatalf("API Key 行内提交结果错误: field=%q key=%q action=%q", state.editingField, state.apiKey, state.apiKeyAction)
	}
	if got := state.keyStatus(); got != "sk-n******7890" {
		t.Fatalf("新 API Key 应显示脱敏提示，得到 %q", got)
	}
}

func TestProviderHubEditsBaseURLInlineAndKeepsLongTailVisible(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", existing: true,
		apiKeyOptional: true, baseURL: "https://old.example/v1"}
	state.cursor = hubFieldIndex(state.hubFields(), "baseurl")
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.editingField != "baseurl" || state.input.Value() != "https://old.example/v1" {
		t.Fatalf("Base URL 应在原行预填编辑，field=%q value=%q", state.editingField, state.input.Value())
	}
	state.input.SetValue("  https://example.com/a/very/long/provider/path/UNIQUE-END  ")
	state.input.CursorEnd()
	view := renderModelConfigModal(76, state)
	if !strings.Contains(view, "UNIQUE-END") {
		t.Fatalf("长 Base URL 编辑时应显示光标附近尾部:\n%s", view)
	}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.baseURL != "https://example.com/a/very/long/provider/path/UNIQUE-END" {
		t.Fatalf("Base URL 未 TrimSpace，得到 %q", state.baseURL)
	}
}

func TestSaveConfigHighlightsOnlyWhenDirty(t *testing.T) {
	state := &modelConfigState{editModelIdx: -1}
	state.applyProviderChoice(configProviderChoice{existing: &host.ProviderSnapshot{
		Name: "proxy", Type: "openai", BaseURL: "https://old.example/v1", HasAPIKey: true,
		APIKeyHint: "sk-o******7890", Models: []bootstrap.ModelConfig{{Name: "m1"}},
	}})
	if state.isDirty() {
		t.Fatal("刚进入已有 Provider 时不应标记为已修改")
	}
	state.baseURL = "https://new.example/v1"
	if !state.isDirty() {
		t.Fatal("Base URL 变化后应标记为已修改")
	}
	state.baseURL = "https://old.example/v1"
	if state.isDirty() {
		t.Fatal("改回基线值后应自动恢复未修改状态")
	}
	state.beginInlineEdit("baseurl")
	state.input.SetValue("https://editing.example/v1")
	if !state.isDirty() {
		t.Fatal("Base URL 正在输入新值时应实时标记为已修改")
	}
	state.input.SetValue(" https://old.example/v1 ")
	if state.isDirty() {
		t.Fatal("行内输入等价于基线值时不应误报修改")
	}
	state.editingField = ""
	state.apiKeyAction = host.APIKeyReplace
	state.apiKey = "sk-new-secret"
	if !state.isDirty() {
		t.Fatal("替换 API Key 后应标记为已修改")
	}

	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })
	lines := renderProviderHubFields(state, 68)
	want := lipgloss.NewStyle().Foreground(colorSuccess).Render("保存配置")
	found := false
	for _, line := range lines {
		if strings.Contains(line, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("有变更时保存项应使用成功色，lines=%q", lines)
	}

	newProvider := &modelConfigState{}
	if !newProvider.isDirty() {
		t.Fatal("新增 Provider 应始终视为未保存变更")
	}
}

func TestStyledBaseURLLineKeepsANSIAndFillsModalWidth(t *testing.T) {
	plain := "› Base URL  https://api.deepseek.com"
	styled := "\x1b[38;2;255;200;0m› \x1b[0m" +
		"\x1b[1;38;2;255;200;0mBase URL\x1b[0m  " +
		"\x1b[4;38;2;220;220;220mhttps://api.deepseek.com\x1b[0m"
	if got := ansi.Strip(truncateStyledWidth(styled, 56)); got != plain {
		t.Fatalf("ANSI 感知截断破坏了输入行: %q", got)
	}

	modal := renderPaddedModalFrame(60, 3, "/config", "", []string{styled})
	lines := strings.Split(modal, "\n")
	if len(lines) != 3 || lipgloss.Width(lines[1]) != 60 {
		t.Fatalf("浮层输入行没有填满固定宽度: width=%d\n%s", lipgloss.Width(lines[1]), modal)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "https://api.deepseek.com") {
		t.Fatalf("浮层丢失 Base URL:\n%s", modal)
	}
}

func TestProviderHubDeleteClearsOnlyOptionalAPIKey(t *testing.T) {
	optional := &modelConfigState{step: configStepHub, provider: "proxy", providerType: "openai",
		hasAPIKey: true, apiKeyHint: "sk-o******7890", apiKeyOptional: true, apiKeyAction: host.APIKeyKeep}
	optional.cursor = hubFieldIndex(optional.hubFields(), "key")
	m := Model{modelConfig: optional}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyDelete})
	if optional.apiKeyAction != host.APIKeyClear || optional.keyStatus() != "已清除" {
		t.Fatalf("可选 Key 的 Delete 应标记清除，action=%q status=%q", optional.apiKeyAction, optional.keyStatus())
	}

	required := &modelConfigState{step: configStepHub, provider: "openrouter",
		hasAPIKey: true, apiKeyHint: "sk-o******7890", apiKeyOptional: false, apiKeyAction: host.APIKeyKeep}
	required.cursor = hubFieldIndex(required.hubFields(), "key")
	m = Model{modelConfig: required}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyDelete})
	if required.apiKeyAction != host.APIKeyKeep || !strings.Contains(required.message, "不能清除") {
		t.Fatalf("必需 Key 不应被清除，action=%q message=%q", required.apiKeyAction, required.message)
	}
}

func TestConfigTextInputSupportsCursorEditing(t *testing.T) {
	state := &modelConfigState{step: configStepCustomName}
	state.startTextInput("ac", "Provider 名称", false)
	state.input.SetCursor(1)
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if got := state.input.Value(); got != "abc" {
		t.Fatalf("统一输入框应支持在光标处插入，得到 %q", got)
	}
}

func TestProviderHubShowsConfigPathAndConnectionAction(t *testing.T) {
	state := &modelConfigState{
		step: configStepHub, provider: "proxy", apiKeyOptional: true, currentModel: "m2",
		models:   []bootstrap.ModelConfig{{Name: "m1"}, {Name: "m2"}},
		snapshot: host.ModelConfigurationSnapshot{ConfigPath: `C:\work\.ainovel\config.json`},
	}
	fields := state.hubFields()
	idx := hubFieldIndex(fields, "test")
	if idx < 0 || fields[idx].value != "m2" {
		t.Fatalf("测试连接应优先当前模型，fields=%#v", fields)
	}
	view := renderModelConfigModal(120, state)
	for _, want := range []string{"高级配置", "extra_body"} {
		if !strings.Contains(view, want) {
			t.Fatalf("配置 Hub 缺少 %q:\n%s", want, view)
		}
	}
	compact := strings.NewReplacer("\r", "", "\n", "", " ", "", "│", "").Replace(view)
	if !strings.Contains(compact, `C:\work\.ainovel\config.json`) {
		t.Fatalf("配置 Hub 未完整展示配置路径:\n%s", view)
	}
}

func TestModelConfigMessageWrapKeepsErrorTail(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", apiKeyOptional: true,
		message: "连接失败：" + strings.Repeat("上游返回了很长的错误信息", 8) + " UNIQUE-ERROR-TAIL"}
	view := renderModelConfigModal(64, state)
	compact := strings.NewReplacer("\r", "", "\n", "", " ", "", "│", "").Replace(view)
	if !strings.Contains(compact, "UNIQUE-ERROR-TAIL") {
		t.Fatalf("长错误不应截断尾部:\n%s", view)
	}
}

func TestConnectionActionStartsAsyncTestWithoutLeavingHub(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", providerType: "openai",
		apiKeyOptional: true, models: []bootstrap.ModelConfig{{Name: "m1"}}}
	state.cursor = hubFieldIndex(state.hubFields(), "test")
	m := Model{modelConfig: state}
	_, cmd := m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !state.testing || state.step != configStepHub {
		t.Fatalf("测试连接应异步留在 hub，cmd=%v testing=%v step=%d", cmd != nil, state.testing, state.step)
	}
}

func TestConnectionTestCanBeCancelled(t *testing.T) {
	cancelled := false
	state := &modelConfigState{step: configStepHub, provider: "proxy", testing: true,
		testCancel: func() { cancelled = true }}
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled || !state.testing || state.message != "正在取消连接测试..." {
		t.Fatalf("Esc 应取消在途测试并等待结果，cancelled=%v testing=%v message=%q", cancelled, state.testing, state.message)
	}

	updated, _, handled := m.handleRuntimeMsg(modelConfigConnectionMsg{err: context.Canceled})
	m = updated.(Model)
	if !handled || m.modelConfig.testing || m.modelConfig.message != "连接测试已取消" {
		t.Fatalf("取消结果未正确收敛: handled=%v testing=%v message=%q", handled, m.modelConfig.testing, m.modelConfig.message)
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
