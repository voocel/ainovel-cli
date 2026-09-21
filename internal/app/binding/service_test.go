package binding

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
)

type fakeBinder struct {
	bindings *models.Bindings
	calls    int
}

func (b *fakeBinder) Bind(bindings models.Bindings) { b.bindings, b.calls = &bindings, b.calls+1 }
func (b *fakeBinder) Bound() bool                   { return b.bindings != nil }

func testConfig() appconfig.Config {
	cfg := appconfig.Config{Thinking: "high"}.WithProvider("deepseek", "deepseek-chat", appconfig.ProviderConfig{APIKey: "k1", Models: []string{"deepseek-chat", "deepseek-reasoner"}})
	return cfg.WithProvider("proxy", "gpt-5", appconfig.ProviderConfig{Type: "openai", API: "responses", APIKey: "k2", BaseURL: "https://proxy.test/v1"}).
		WithProvider("deepseek", "deepseek-chat", appconfig.ProviderConfig{APIKey: "k1", Models: []string{"deepseek-chat", "deepseek-reasoner"}})
}

func TestApplyBindsWithoutSavingAndKeepsIntentOnFailure(t *testing.T) {
	dir := t.TempDir()
	binder := &fakeBinder{}
	service := New(dir, binder)
	if err := service.Apply(testConfig()); err != nil {
		t.Fatal(err)
	}
	if binder.bindings == nil || binder.bindings.Default.Provider != "deepseek" || binder.bindings.Default.Model != "deepseek-chat" || binder.bindings.Default.Thinking != agentcore.ThinkingHigh {
		t.Fatalf("default binding = %+v", binder.bindings)
	}
	if loaded, err := appconfig.LoadConfig(dir); err != nil || loaded.Configured() {
		t.Fatalf("Apply must not write the config file: %#v %v", loaded, err)
	}
	broken := testConfig()
	broken.Model = ""
	if err := service.Apply(broken); err == nil || binder.calls != 1 {
		t.Fatalf("invalid config rebound the runtime: err=%v calls=%d", err, binder.calls)
	}
	if service.Config().Model != "" {
		t.Fatal("failed Apply must still record the intent for the wizard to repair")
	}
	current := service.Current("")
	if !current.Bound || current.Provider != "deepseek" || current.Model != "" {
		t.Fatalf("current = %+v", current)
	}
}

func TestUseAndSetThinkingPersistAndRebind(t *testing.T) {
	dir := t.TempDir()
	binder := &fakeBinder{}
	service := New(dir, binder)
	if err := service.Apply(testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := service.Use("", "proxy", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	saved, err := appconfig.LoadConfig(dir)
	if err != nil || saved.Provider != "proxy" || saved.Model != "gpt-5" || saved.Thinking != "high" {
		t.Fatalf("saved = %#v, %v", saved, err)
	}
	if binder.bindings.Default.Provider != "openai" || binder.bindings.Default.Model != "gpt-5" || binder.bindings.Default.Thinking != agentcore.ThinkingHigh {
		t.Fatalf("rebound default = %+v", binder.bindings.Default)
	}
	if err := service.Use("", "", "gpt-5-mini"); err != nil {
		t.Fatal(err)
	}
	if got := service.Current(""); got.Connection != "proxy" || got.Model != "gpt-5-mini" || got.Provider != "openai" {
		t.Fatalf("same-connection switch = %+v", got)
	}
	if err := service.SetThinking("", "AUTO"); err != nil || service.Current("").Thinking != "" || binder.bindings.Default.Thinking != "" {
		t.Fatalf("auto thinking: err=%v current=%+v", err, service.Current(""))
	}
	if err := service.SetThinking("", "extreme"); err == nil || !strings.Contains(err.Error(), "auto / off") {
		t.Fatalf("unknown level accepted: %v", err)
	}
	for _, bad := range [][2]string{{"missing", "m"}, {"proxy", ""}} {
		if err := service.Use("", bad[0], bad[1]); err == nil {
			t.Fatalf("Use(%q, %q) accepted", bad[0], bad[1])
		}
	}
	choices := service.Choices()
	if len(choices) != 4 || choices[0].Connection != "deepseek" || choices[3] != (Choice{Connection: "proxy", Model: "gpt-5-mini", Current: true}) {
		t.Fatalf("choices = %+v", choices)
	}
}

func TestRoleOverridesFollowDefaultUntilSet(t *testing.T) {
	dir := t.TempDir()
	binder := &fakeBinder{}
	service := New(dir, binder)
	if err := service.Apply(testConfig()); err != nil {
		t.Fatal(err)
	}
	if got := service.Current("writer"); !got.Inherited || got.Model != "deepseek-chat" || got.Thinking != agentcore.ThinkingHigh {
		t.Fatalf("unset role must follow the default: %+v", got)
	}
	if err := service.SetThinking("writer", "low"); err == nil {
		t.Fatal("thinking on an inherited role must require a model first")
	}
	if err := service.Use("writer", "proxy", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetThinking("writer", "low"); err != nil {
		t.Fatal(err)
	}
	writer := binder.bindings.Roles["writer"]
	if writer.Provider != "openai" || writer.Model != "gpt-5" || writer.Thinking != agentcore.ThinkingLow || binder.bindings.Default.Model != "deepseek-chat" {
		t.Fatalf("writer binding = %+v default = %+v", writer, binder.bindings.Default)
	}
	if got := service.Current("writer"); got.Inherited || got.Connection != "proxy" || got.Thinking != agentcore.ThinkingLow {
		t.Fatalf("writer selection = %+v", got)
	}
	if saved, _ := appconfig.LoadConfig(dir); saved.Roles["writer"] != (appconfig.RoleConfig{Provider: "proxy", Model: "gpt-5", Thinking: "low"}) {
		t.Fatalf("saved roles = %#v", saved.Roles)
	}
	if err := service.SetThinking("writer", "inherit"); err != nil || service.Current("writer").Thinking != agentcore.ThinkingHigh {
		t.Fatalf("inherit thinking: %v %+v", err, service.Current("writer"))
	}
	if err := service.Use("writer", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := binder.bindings.Roles["writer"]; ok || !service.Current("writer").Inherited {
		t.Fatal("clearing a role must drop its binding")
	}
	if err := service.Use("narrator", "proxy", "gpt-5"); err == nil {
		t.Fatal("unknown role accepted")
	}
	if levels := service.Levels("proxy", "gpt-5"); len(levels) == 0 || levels[0] != "" {
		t.Fatalf("levels = %v, want auto first", levels)
	}
}

func TestVerifyAndUnassembledRuntime(t *testing.T) {
	service := New(t.TempDir(), nil)
	if err := service.Apply(testConfig()); err == nil || !strings.Contains(err.Error(), "not assembled") {
		t.Fatalf("Apply without runtime = %v", err)
	}
	if _, err := modelConfigFor(testConfig(), "proxy", "gpt-5", "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := modelConfigFor(testConfig(), "missing", "gpt-5", ""); err == nil {
		t.Fatal("missing connection accepted")
	}
}
