package binding

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	if binder.bindings == nil || binder.bindings.Default.Provider != "deepseek" || binder.bindings.Default.Model != "deepseek-chat" || binder.bindings.Default.Thinking != "high" {
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
	if binder.bindings.Default.Provider != "openai" || binder.bindings.Default.Model != "gpt-5" || binder.bindings.Default.Thinking != "high" {
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
	if got := service.Current("writer"); !got.Inherited || got.Model != "deepseek-chat" || got.Thinking != "high" {
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
	if writer.Provider != "openai" || writer.Model != "gpt-5" || writer.Thinking != "low" || binder.bindings.Default.Model != "deepseek-chat" {
		t.Fatalf("writer binding = %+v default = %+v", writer, binder.bindings.Default)
	}
	if got := service.Current("writer"); got.Inherited || got.Connection != "proxy" || got.Thinking != "low" {
		t.Fatalf("writer selection = %+v", got)
	}
	if saved, _ := appconfig.LoadConfig(dir); saved.Roles["writer"] != (appconfig.RoleConfig{Provider: "proxy", Model: "gpt-5", Thinking: "low"}) {
		t.Fatalf("saved roles = %#v", saved.Roles)
	}
	if err := service.SetThinking("writer", "inherit"); err != nil || service.Current("writer").Thinking != "high" {
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

func TestUnassembledRuntimeAndConnectionLookup(t *testing.T) {
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

// 模型清单里有的模型，绑定时附上标价、Current 给出上下文窗口；没有的不附，用量不计花费。
// 本地清单在有效期内时不联网、不重绑。
func TestBindingsCarryPricesFromTheModelList(t *testing.T) {
	dir := t.TempDir()
	list := `{"deepseek-chat": {"mode": "chat", "litellm_provider": "deepseek", "max_input_tokens": 131072,
		"input_cost_per_token": 3e-7, "output_cost_per_token": 1.2e-6, "cache_read_input_token_cost": 6e-9}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	binder := &fakeBinder{}
	service := New(dir, binder)
	config := testConfig().WithRole("writer", "proxy", "gpt-5", "")
	if err := service.Apply(config); err != nil {
		t.Fatal(err)
	}
	if pricing := binder.bindings.Default.Chat.Pricing; pricing == nil || pricing.Input != 3e-7 || binder.bindings.Roles["writer"].Chat.Pricing != nil {
		t.Fatalf("pricing = %+v / %+v", pricing, binder.bindings.Roles["writer"].Chat.Pricing)
	}
	if window := service.Current("").ContextWindow; window != 131072 || service.Current("writer").ContextWindow != 0 {
		t.Fatalf("context window = %d", window)
	}
	if err := service.RefreshFacts(context.Background()); err != nil || binder.calls != 1 {
		t.Fatalf("a fresh list must not download or rebind: err=%v calls=%d", err, binder.calls)
	}
}

// 列模型：清单认识的对话模型排前（附上下文窗口与每百万 token 标价），其余新发布的在前；
// 密钥被拒原样报出（归类由 models.Classify 负责），地址不合法不发请求。
func TestListModelsRanksKnownFirstAndClassifiesRefusal(t *testing.T) {
	dir := t.TempDir()
	list := `{"gpt-known": {"mode": "chat", "litellm_provider": "openai", "max_input_tokens": 400000,
		"input_cost_per_token": 1.25e-6, "output_cost_per_token": 1e-5}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"zz-old","created":100},{"id":"aa-new","created":200},{"id":"gpt-known","created":50}]}`))
	}))
	defer server.Close()
	service := New(dir, &fakeBinder{})
	relay := appconfig.ProviderConfig{Type: "openai", APIKey: "good", BaseURL: server.URL}
	options, err := service.ListModels(context.Background(), "relay", relay)
	if err != nil || len(options) != 3 || options[0].ID != "gpt-known" || options[1].ID != "aa-new" || options[2].ID != "zz-old" {
		t.Fatalf("options = %+v, %v", options, err)
	}
	if known := options[0]; known.ContextWindow != 400000 || known.Input != 1.25 || known.Output != 10 {
		t.Fatalf("known facts = %+v", known)
	}
	relay.APIKey = "bad"
	if _, err := service.ListModels(context.Background(), "relay", relay); err == nil || !strings.Contains(err.Error(), "invalid api key") || errors.Is(err, appconfig.ErrBaseURL) {
		t.Fatalf("refused key = %v", err)
	}
	relay.BaseURL = "ftp://relay.test"
	if _, err := service.ListModels(context.Background(), "relay", relay); !errors.Is(err, appconfig.ErrBaseURL) {
		t.Fatalf("bad address = %v", err)
	}
}

// 保存连接校验连接本身；删除连接或模型时，默认或角色还在用的拒绝，删了的不再出现在选项里。
func TestSaveAndRemoveConnections(t *testing.T) {
	dir := t.TempDir()
	service := New(dir, &fakeBinder{})
	if err := service.Apply(testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveConnection("relay", appconfig.ProviderConfig{Type: "openai", BaseURL: "ftp://relay.test"}, ""); !errors.Is(err, appconfig.ErrBaseURL) {
		t.Fatalf("invalid connection saved: %v", err)
	}
	if err := service.SaveConnection("relay", appconfig.ProviderConfig{Type: "openai", BaseURL: "https://relay.test"}, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := service.Current(""); got.Connection != "relay" || got.Model != "r1" {
		t.Fatalf("saving with a model must make it the default: %+v", got)
	}
	if err := service.RemoveConnection("relay"); err == nil {
		t.Fatal("the default connection was removed")
	}
	if err := service.RemoveModel("deepseek", "deepseek-reasoner"); err != nil {
		t.Fatal(err)
	}
	if err := service.Use("", "deepseek", "deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveConnection("proxy"); err != nil {
		t.Fatal(err)
	}
	file, _ := appconfig.LoadConfig(dir)
	if _, ok := file.Providers["proxy"]; ok || slices.Contains(file.Providers["deepseek"].Models, "deepseek-reasoner") || len(service.Choices()) != 2 {
		t.Fatalf("file = %+v choices = %+v", file.Providers, service.Choices())
	}
}

// 试用模型：与创作同形的一次对话；模型名错时是拒绝，地址不合法不发请求。
func TestTryModelSendsOneChatAndClassifiesRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/chat/completions" || !strings.Contains(string(body), `"model":"good"`) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"model not found","type":"invalid_request_error","code":"model_not_found"}}`))
			return
		}
		w.Write([]byte(`{"id":"1","object":"chat.completion","model":"good","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	service := New(t.TempDir(), &fakeBinder{})
	relay := appconfig.ProviderConfig{Type: "openai", APIKey: "k", BaseURL: server.URL}
	if err := service.TryModel(context.Background(), "relay", relay, "good"); err != nil {
		t.Fatal(err)
	}
	if err := service.TryModel(context.Background(), "relay", relay, "typo"); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("typo = %v", err)
	}
	relay.BaseURL = "ftp://relay.test"
	if err := service.TryModel(context.Background(), "relay", relay, "good"); !errors.Is(err, appconfig.ErrBaseURL) {
		t.Fatalf("bad address = %v", err)
	}
}
