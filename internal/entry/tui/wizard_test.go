package tui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/app/binding"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// listedModels 是不联网的模型列表：任何连接都列出这些模型。
func listedModels(ids ...string) func(context.Context, string, appconfig.ProviderConfig) ([]binding.ModelOption, error) {
	return func(context.Context, string, appconfig.ProviderConfig) ([]binding.ModelOption, error) {
		options := make([]binding.ModelOption, len(ids))
		for i, id := range ids {
			options[i] = binding.ModelOption{ID: id}
		}
		return options, nil
	}
}

// listing 是一次列模型请求：记下它用的连接，返回给定的结果。
type listing struct {
	name    string
	pc      appconfig.ProviderConfig
	options []binding.ModelOption
	err     error
}

func (l *listing) list(_ context.Context, name string, pc appconfig.ProviderConfig) ([]binding.ModelOption, error) {
	l.name, l.pc = name, pc
	return l.options, l.err
}

// trial 是一次试用模型的请求：记下试的模型，返回给定的结果。
type trial struct {
	model string
	err   error
}

func (r *trial) try(_ context.Context, _ string, _ appconfig.ProviderConfig, model string) error {
	r.model = model
	return r.err
}

func paste(t *testing.T, m model, text string) model {
	t.Helper()
	next, _ := m.Update(tea.PasteMsg{Content: text})
	return next.(model)
}

// deliver 把命令产生的列模型结果送回界面。
func deliver(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	next, _ := m.Update(findMsg[wizardListedMsg](t, cmd))
	return next.(model)
}

// 首次配置：选服务商、粘贴密钥回车，服务商列出模型，筛选后回车即绑定、落盘并进首页。
func TestFirstRunPicksAListedModel(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	lister := &listing{options: []binding.ModelOption{{ID: "deepseek-v4"}, {ID: "deepseek-v4-flash"}}}
	deps.ListModels = lister.list
	m := newModel(context.Background(), deps)
	if m.page != pageWizard || m.wizard.step != wizardProviders {
		t.Fatalf("first run must start from providers: page=%v step=%v", m.page, m.wizard.step)
	}
	m, _ = press(t, m, tea.KeyEnter)
	if m.wizard.step != wizardConnection || m.wizard.row != rowKey {
		t.Fatalf("DeepSeek must ask for the key first: step=%v row=%v", m.wizard.step, m.wizard.row)
	}
	m = paste(t, m, "sk-secret")
	m, cmd := press(t, m, tea.KeyEnter)
	if m.wizard.step != wizardModels || !m.wizard.listing {
		t.Fatal("submitting the key must list models")
	}
	m = deliver(t, m, cmd)
	if lister.name != "deepseek" || lister.pc.APIKey != "sk-secret" || lister.pc.Type != "deepseek" {
		t.Fatalf("listed with %q %+v", lister.name, lister.pc)
	}
	requireContains(t, ansi.Strip(m.screen()), "deepseek-v4", "deepseek-v4-flash")
	m = typeText(t, m, "flash")
	if rows := m.wizard.pickerRows(); len(rows) != 2 || rows[0].id != "deepseek-v4-flash" || rows[1].option != nil {
		t.Fatalf("filter rows = %+v", rows)
	}
	m, _ = press(t, m, tea.KeyEnter)
	if m.page != pageHome {
		t.Fatalf("page = %v (err %q), want home after the first model", m.page, m.wizard.err)
	}
	saved := savedConfig(t, m)
	if saved.Provider != "deepseek" || saved.Model != "deepseek-v4-flash" || saved.Providers["deepseek"].APIKey != "sk-secret" {
		t.Fatalf("saved = %+v", saved)
	}
}

// 密钥被拒回到表单、焦点在密钥上，报错里的密钥被隐去，配置不落盘。
func TestRefusedKeyReturnsToTheKeyWithoutSaving(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	deps.ListModels = (&listing{err: &domainmodel.Failure{Code: domainmodel.FailureModelRefused, Err: errors.New("401 invalid key sk-secret")}}).list
	m := newModel(context.Background(), deps)
	m, _ = press(t, m, tea.KeyEnter)
	m = paste(t, m, "sk-secret")
	m, cmd := press(t, m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	if m.wizard.step != wizardConnection || m.wizard.row != rowKey || !strings.Contains(m.wizard.err, "拒绝") {
		t.Fatalf("step=%v row=%v err=%q", m.wizard.step, m.wizard.row, m.wizard.err)
	}
	if strings.Contains(m.screen(), "sk-secret") || !strings.Contains(m.wizard.err, "[已隐藏]") {
		t.Fatal("provider error leaked the key")
	}
	if _, err := os.Stat(appconfig.ConfigPath(deps.ConfigDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused key saved config")
	}
}

// 中转：名称按地址自动起、可选 Responses 接口；列不出模型时直接输入模型 ID，先试一句，
// 试不通留在模型页给出原话（密钥隐去），试通了才落盘。
func TestRelayTakesTypedModelWhenItCannotList(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	lister := &listing{err: errors.New("404 page not found")}
	deps.ListModels = lister.list
	tried := &trial{err: errors.New("model my-model not found for relay-key")}
	deps.TryModel = tried.try
	m := newModel(context.Background(), deps)
	m.wizard.cursor = relayPreset
	m, _ = press(t, m, tea.KeyEnter)
	if m.wizard.row != rowAddress {
		t.Fatalf("relay must start at the address, got row %v", m.wizard.row)
	}
	m = typeText(t, m, "https://api.wool.dev/v1")
	if name, _ := m.wizard.draft(); name != "wool" {
		t.Fatalf("suggested name = %q", name)
	}
	m, _ = press(t, m, tea.KeyTab)
	m = paste(t, m, "relay-key")
	m, _ = press(t, m, tea.KeyTab)
	m, _ = press(t, m, tea.KeyRight)
	m, cmd := press(t, m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	if lister.pc.API != "responses" || lister.pc.BaseURL != "https://api.wool.dev/v1" || lister.pc.Type != "openai" {
		t.Fatalf("listed with %+v", lister.pc)
	}
	if m.wizard.step != wizardModels || !strings.Contains(m.wizard.unlisted, "404") {
		t.Fatalf("unlisted relay must fall back to typing: step=%v unlisted=%q", m.wizard.step, m.wizard.unlisted)
	}
	m = typeText(t, m, "my-model")
	requireContains(t, ansi.Strip(m.screen()), "没能列出模型", "使用「my-model」")
	m, cmd = press(t, m, tea.KeyEnter)
	requireContains(t, ansi.Strip(m.screen()), "正在试用「my-model」")
	next, _ := m.Update(findMsg[wizardTriedMsg](t, cmd))
	m = next.(model)
	if tried.model != "my-model" || m.wizard.step != wizardModels || !strings.Contains(m.wizard.err, "用不了") || strings.Contains(m.wizard.err, "relay-key") {
		t.Fatalf("failed trial: tried=%q step=%v err=%q", tried.model, m.wizard.step, m.wizard.err)
	}
	if _, err := os.Stat(appconfig.ConfigPath(deps.ConfigDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a model that failed its trial was saved")
	}
	tried.err = nil
	m, cmd = press(t, m, tea.KeyEnter)
	next, _ = m.Update(findMsg[wizardTriedMsg](t, cmd))
	m = next.(model)
	saved := savedConfig(t, m)
	pc := saved.Providers["wool"]
	if m.page != pageHome || saved.Provider != "wool" || saved.Model != "my-model" || pc.Type != "openai" || pc.API != "responses" || pc.APIKey != "relay-key" {
		t.Fatalf("page=%v saved=%+v", m.page, saved)
	}
}

// 新连接不能顶掉同名的已保存连接；内置服务商的连接名就是服务商名，已有时带出原密钥接着改。
func TestNewConnectionNamesStayUnique(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	if err := m.api.Models.SaveConnection("wool", appconfig.ProviderConfig{Type: "openai", APIKey: "old", BaseURL: "https://api.wool.dev/v1"}, ""); err != nil {
		t.Fatal(err)
	}
	next, _ := m.activate(focusConfig)
	m = next.(model)
	next, _ = m.newConnection(relayPreset)
	m = next.(model)
	m = typeText(t, m, "https://api.wool.dev/v1")
	if name, _ := m.wizard.draft(); name != "wool-2" {
		t.Fatalf("suggested name = %q, want wool-2", name)
	}
	m.wizard.inputs[inputName].SetValue("wool")
	m, cmd := press(t, m, tea.KeyEnter)
	if cmd != nil && m.wizard.listing || !strings.Contains(m.wizard.err, "已有名为") || m.wizard.row != rowName {
		t.Fatalf("duplicate name accepted: err=%q row=%v", m.wizard.err, m.wizard.row)
	}
	next, _ = m.newConnection(0)
	m = next.(model)
	if m.wizard.original != "deepseek" || m.wizard.inputs[inputKey].Value() != "test" || slices.Contains(m.wizard.formRows(), rowName) {
		t.Fatalf("DeepSeek again must continue the saved connection: %+v", m.wizard)
	}
}

func overviewFixture(t *testing.T) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	if err := m.api.Models.SaveConnection("wool", appconfig.ProviderConfig{Type: "openai", APIKey: "wool-key", BaseURL: "https://api.wool.dev/v1", Models: []string{"grok-4.7"}}, ""); err != nil {
		t.Fatal(err)
	}
	next, _ := m.activate(focusConfig)
	return next.(model)
}

func cursorAt(t *testing.T, m model, kind int, connection, model string) model {
	t.Helper()
	m.wizard.cursor = slices.IndexFunc(m.overviewItems(), func(item overviewItem) bool {
		return item.kind == kind && item.connection == connection && item.model == model
	})
	if m.wizard.cursor < 0 {
		t.Fatalf("no overview row %d %s %s", kind, connection, model)
	}
	return m
}

// 总览：思考强度左右即改，模型回车即换用，编辑连接验证后只改连接、不换默认模型。
func TestOverviewSwitchesAndEditsInPlace(t *testing.T) {
	m := overviewFixture(t)
	if m.wizard.step != wizardOverview {
		t.Fatalf("configured settings must open the overview, got %v", m.wizard.step)
	}
	requireContains(t, ansi.Strip(m.screen()), "deepseek / deepseek-v4-flash", "Writer", "跟随默认", "OpenAI 格式 · api.wool.dev", "grok-4.7", "+ 添加连接")
	m, _ = press(t, m, tea.KeyRight)
	if m.api.Models.Config().Thinking != "" {
		t.Fatal("left/right on the default model row must not touch thinking")
	}
	m, _ = press(t, m, tea.KeyDown)
	m, _ = press(t, m, tea.KeyRight)
	if m.api.Models.Config().Thinking == "" || !strings.Contains(m.wizard.notice, "思考强度") {
		t.Fatalf("right on thinking must change it: %q", m.api.Models.Config().Thinking)
	}
	m = cursorAt(t, m, itemModel, "wool", "grok-4.7")
	m, _ = press(t, m, tea.KeyEnter)
	if current := m.api.Models.Current(""); current.Connection != "wool" || current.Model != "grok-4.7" || m.page != pageWizard {
		t.Fatalf("model row did not switch in place: %+v", current)
	}
	lister := &listing{}
	m.deps.ListModels = lister.list
	m = cursorAt(t, m, itemConnection, "deepseek", "")
	m, _ = press(t, m, tea.KeyEnter)
	if m.wizard.step != wizardConnection || m.wizard.mode != editConnection {
		t.Fatal("connection row must open its form")
	}
	m.wizard.inputs[inputKey].SetValue("new-key")
	m, cmd := press(t, m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	saved := savedConfig(t, m)
	if m.wizard.step != wizardOverview || saved.Providers["deepseek"].APIKey != "new-key" || saved.Provider != "wool" || !strings.Contains(m.wizard.notice, "已更新") {
		t.Fatalf("edit: step=%v notice=%q saved=%+v", m.wizard.step, m.wizard.notice, saved)
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.page != pageHome {
		t.Fatal("Esc on the overview must return home")
	}
}

// 添加模型：用已保存的凭据列出模型，已有的打勾，选中的并入连接并用上。
func TestAddModelMarksSavedAndUsesThePick(t *testing.T) {
	m := overviewFixture(t)
	lister := &listing{options: []binding.ModelOption{{ID: "grok-4.7"}, {ID: "qwen3.8-27b", ContextWindow: 128000, Input: 0.2, Output: 0.6}}}
	m.deps.ListModels = lister.list
	m = cursorAt(t, m, itemAddModel, "wool", "")
	m, cmd := press(t, m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	if lister.pc.APIKey != "wool-key" || m.wizard.step != wizardModels {
		t.Fatalf("add model must list with saved credentials: %+v", lister.pc)
	}
	requireContains(t, ansi.Strip(m.screen()), "✓ grok-4.7", "qwen3.8-27b", "128K", "$0.20 / $0.60")
	m, _ = press(t, m, tea.KeyDown)
	m, _ = press(t, m, tea.KeyEnter)
	saved := savedConfig(t, m)
	if m.wizard.step != wizardOverview || saved.Provider != "wool" || saved.Model != "qwen3.8-27b" || !slices.Equal(saved.Providers["wool"].Models, []string{"grok-4.7", "qwen3.8-27b"}) {
		t.Fatalf("saved = %+v", saved)
	}
}

// 角色：回车从已保存的模型里给它挑一个，←→ 调它自己的思考强度，选「跟随默认」即清除覆盖。
func TestOverviewAssignsRoles(t *testing.T) {
	m := overviewFixture(t)
	m.wizard.cursor = slices.Index(m.overviewItems(), overviewItem{kind: itemRole, role: "writer"})
	m, _ = press(t, m, tea.KeyRight)
	if _, ok := m.api.Models.Config().Roles["writer"]; ok {
		t.Fatal("an inherited role has no thinking of its own to change")
	}
	m, _ = press(t, m, tea.KeyEnter)
	if m.wizard.step != wizardRole {
		t.Fatalf("role row must open the picker, step=%v", m.wizard.step)
	}
	requireContains(t, ansi.Strip(m.screen()), "选择模型 · Writer", "● 跟随默认", "deepseek / deepseek-v4-flash", "wool / grok-4.7")
	m.wizard.cursor = slices.Index(m.roleChoices(), binding.Choice{Connection: "wool", Model: "grok-4.7"})
	m, _ = press(t, m, tea.KeyEnter)
	if rc := m.api.Models.Config().Roles["writer"]; m.wizard.step != wizardOverview || rc.Provider != "wool" || rc.Model != "grok-4.7" || !strings.Contains(m.wizard.notice, "Writer 改用") {
		t.Fatalf("assign: step=%v roles=%+v notice=%q", m.wizard.step, m.api.Models.Config().Roles, m.wizard.notice)
	}
	if item := m.overviewItems()[m.wizard.cursor]; item.role != "writer" {
		t.Fatalf("cursor must stay on the role, got %+v", item)
	}
	m, _ = press(t, m, tea.KeyRight)
	if thinking := savedConfig(t, m).Roles["writer"].Thinking; thinking == "" {
		t.Fatal("right on an assigned role must set its own thinking")
	}
	requireContains(t, ansi.Strip(m.screen()), "wool / grok-4.7", "思考 ")
	m, _ = press(t, m, tea.KeyEnter)
	m.wizard.cursor = 0
	m, _ = press(t, m, tea.KeyEnter)
	if _, ok := savedConfig(t, m).Roles["writer"]; ok || !m.api.Models.Current("writer").Inherited {
		t.Fatal("following the default must clear the override")
	}
}

// 删除：连接与模型按两次 d 删除，中间按别的键就取消；默认或角色还在用的拒绝并说明。
func TestOverviewDeletesWithConfirmation(t *testing.T) {
	m := overviewFixture(t)
	if err := m.api.Models.Use("writer", "wool", "grok-4.7"); err != nil {
		t.Fatal(err)
	}
	m = cursorAt(t, m, itemConnection, "wool", "")
	m, _ = pressRune(t, m, 'd')
	if !strings.Contains(m.wizard.notice, "再按一次 d 删除连接「wool」和它的 1 个模型") {
		t.Fatalf("notice = %q", m.wizard.notice)
	}
	m, _ = pressRune(t, m, 'd')
	if _, ok := m.api.Models.Config().Providers["wool"]; !ok || !strings.Contains(m.wizard.err, "角色 writer 正在用") {
		t.Fatalf("a connection a role uses was deleted: err=%q", m.wizard.err)
	}
	if err := m.api.Models.Use("writer", "", ""); err != nil {
		t.Fatal(err)
	}
	m = cursorAt(t, m, itemModel, "deepseek", "deepseek-v4-flash")
	m, _ = pressRune(t, m, 'd')
	m, _ = pressRune(t, m, 'd')
	if !strings.Contains(m.wizard.err, "默认模型正在用") {
		t.Fatalf("the default model was deleted: err=%q", m.wizard.err)
	}
	m = cursorAt(t, m, itemModel, "wool", "grok-4.7")
	m, _ = pressRune(t, m, 'd')
	m, _ = press(t, m, tea.KeyEsc)
	if m.page != pageHome {
		t.Fatal("any other key must cancel the pending delete")
	}
	next, _ := m.activate(focusConfig)
	m = cursorAt(t, next.(model), itemModel, "wool", "grok-4.7")
	m, _ = pressRune(t, m, 'd')
	m, _ = pressRune(t, m, 'd')
	if saved := savedConfig(t, m); len(saved.Providers["wool"].Models) != 0 || !strings.Contains(m.wizard.notice, "已删除 wool / grok-4.7") {
		t.Fatalf("model delete: notice=%q saved=%+v", m.wizard.notice, saved.Providers["wool"])
	}
	m = cursorAt(t, m, itemConnection, "wool", "")
	m, _ = pressRune(t, m, 'd')
	m, _ = pressRune(t, m, 'd')
	if _, ok := savedConfig(t, m).Providers["wool"]; ok || slices.ContainsFunc(m.overviewItems(), func(item overviewItem) bool { return item.connection == "wool" }) {
		t.Fatal("connection delete did not remove it")
	}
}

// 内置服务商没填密钥不去列模型；取消的列模型请求，迟到的结果被丢弃。
func TestCancelledListingIsDiscarded(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	cancelled := false
	deps.ListModels = func(ctx context.Context, _ string, _ appconfig.ProviderConfig) ([]binding.ModelOption, error) {
		cancelled = ctx.Err() != nil
		return []binding.ModelOption{{ID: "late"}}, nil
	}
	m := newModel(context.Background(), deps)
	m, _ = press(t, m, tea.KeyEnter)
	m, _ = press(t, m, tea.KeyEnter)
	if m.wizard.listing || !strings.Contains(m.wizard.err, "API Key") {
		t.Fatal("an official provider must not list without a key")
	}
	m = paste(t, m, "sk-test")
	m, cmd := press(t, m, tea.KeyEnter)
	m, _ = press(t, m, tea.KeyEsc)
	m = deliver(t, m, cmd)
	if !cancelled || m.wizard.step != wizardConnection || m.wizard.listing || len(m.wizard.options) != 0 {
		t.Fatalf("late result accepted: step=%v options=%v", m.wizard.step, m.wizard.options)
	}
}

// 鼠标：服务商点了进下一步，格式点选即换，模型单击选中、双击用上。
func TestWizardMouse(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	deps.ListModels = listedModels("a-model", "b-model")
	m := newModel(context.Background(), deps)
	click := func(match func(pageHit) bool) model {
		t.Helper()
		for _, hit := range m.wizardFrame().hits {
			if match(hit) {
				next, cmd := m.Update(tea.MouseClickMsg{X: hit.x, Y: hit.y, Button: tea.MouseLeft})
				if msg, ok := findListed(cmd); ok {
					next, _ = next.(model).Update(msg)
				}
				return next.(model)
			}
		}
		t.Fatal("control not visible")
		return m
	}
	m = click(func(hit pageHit) bool { return hit.row == relayPreset })
	if m.wizard.step != wizardConnection || m.wizard.preset != relayPreset {
		t.Fatal("provider click did not open the form")
	}
	m = click(func(hit pageHit) bool { return hit.focus == int(rowFormat) && hit.option == 2 })
	if m.wizard.kind() != "gemini" || slices.Contains(m.wizard.formRows(), rowEndpoint) {
		t.Fatal("format click did not select Gemini")
	}
	m.wizard.inputs[inputAddress].SetValue("https://gemini.example.com")
	m.wizard.inputs[inputKey].SetValue("gemini-key")
	m = click(func(hit pageHit) bool { return hit.focus == int(rowSubmit) })
	if m.wizard.step != wizardModels || len(m.wizard.options) != 2 {
		t.Fatalf("submit click did not list models: step=%v err=%q", m.wizard.step, m.wizard.err)
	}
	m = click(func(hit pageHit) bool { return hit.row == 1 })
	if m.page != pageWizard || m.wizard.cursor != 1 {
		t.Fatal("single click must only select the model")
	}
	m.wizard.clicked.at = time.Now().Add(-2 * doubleClick)
	m = click(func(hit pageHit) bool { return hit.row == 1 })
	if m.page != pageWizard {
		t.Fatal("slow second click must not use the model")
	}
	m = click(func(hit pageHit) bool { return hit.row == 1 })
	if m.page != pageHome || m.api.Models.Current("").Model != "b-model" {
		t.Fatalf("double click did not use the model: page=%v err=%q", m.page, m.wizard.err)
	}
}

func findListed(cmd tea.Cmd) (wizardListedMsg, bool) {
	if cmd == nil {
		return wizardListedMsg{}, false
	}
	switch msg := cmd().(type) {
	case wizardListedMsg:
		return msg, true
	case tea.BatchMsg:
		for _, inner := range msg {
			if found, ok := findListed(inner); ok {
				return found, true
			}
		}
	}
	return wizardListedMsg{}, false
}

// 各页在各尺寸与深浅主题下恰好铺满一屏，热区都在屏内，长列表开窗而不溢出。
func TestWizardFramesFitAndHitsStayOnScreen(t *testing.T) {
	dark := darkBackground
	t.Cleanup(func() { darkBackground = dark })
	many := make([]string, 100)
	for i := range many {
		many[i] = strings.Repeat("m", i%40+1)
	}
	steps := map[string]func(m model) model{
		"overview": func(m model) model { return m },
		"overview-end": func(m model) model {
			m.wizard.cursor = len(m.overviewItems()) - 1
			return m
		},
		"providers": func(m model) model {
			m.wizard.step = wizardProviders
			return m
		},
		"relay": func(m model) model {
			next, _ := m.newConnection(relayPreset)
			return next.(model)
		},
		"role": func(m model) model {
			next, _ := m.openRolePicker("writer")
			return next.(model)
		},
		"models": func(m model) model {
			next, _ := m.newConnection(0)
			m = next.(model)
			next, cmd := m.submitConnection()
			m = deliver(t, next.(model), cmd)
			m.wizard.cursor = 99
			return m
		},
	}
	for _, isDark := range []bool{false, true} {
		darkBackground = isDark
		for _, size := range [][2]int{{80, 24}, {120, 36}, {150, 40}, {240, 60}} {
			for name, step := range steps {
				m := overviewFixture(t)
				m.deps.ListModels = listedModels(many...)
				next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m = step(next.(model))
				frame := m.wizardFrame()
				if lipgloss.Width(frame.text) > size[0] || lipgloss.Height(frame.text) != size[1] {
					t.Fatalf("frame overflow %v %s", size, name)
				}
				if name == "overview-end" && !strings.Contains(ansi.Strip(frame.text), "+ 添加连接") {
					t.Fatalf("the selected row must stay in the window %v", size)
				}
				for _, hit := range frame.hits {
					if hit.y < pageHeader || hit.y >= size[1]-pageFooter || hit.x < 0 || hit.x+hit.width > size[0] {
						t.Fatalf("invalid hit %v %s %+v", size, name, hit)
					}
				}
			}
		}
	}
}
