package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/errs"
)

const validGlobal = `{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "providers": { "openrouter": { "api_key": "sk-test-123456" } }
}`

// writeGlobal 在隔离的 HOME 下写入全局Cấu hình，并Quay lại该 HOME。
func writeGlobal(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ainovel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o644); err != nil {
			t.Fatalf("write global: %v", err)
		}
	}
	return home
}

// writeProjectConfig 在Hiện tại工作Thư mục的 ./.ainovel/ 下写入项目级Cấu hình。
// 调用前需先 t.Chdir 到目标Thư mục。
func writeProjectConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(".ainovel", 0o755); err != nil {
		t.Fatalf("mkdir .ainovel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(".ainovel", "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write project: %v", err)
	}
}

// 根因 3：项目级 ./.ainovel/config.json 存在但是坏 JSON，必须报错，不能静默吞掉退回全局。
func TestLoadConfig_CorruptProjectFailsLoud(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	// 手抄示例多了个尾逗号——最常见的坏 JSON。
	writeProjectConfig(t, `{ "model": "x", }`)

	if _, err := LoadConfig(""); err == nil {
		t.Fatal("坏的 ./.ainovel/config.json 应当报错，却被静默忽略了")
	}
}

// 全局是最低优先级基底：坏Tập tin不得阻断更高优先级的 --config 覆盖（回归守卫——
// 上一版误把全局也 fail-loud，导致"坏全局 + 有效 --config"的用户被Không có关Tập tin挡住）。
func TestLoadConfig_CorruptGlobalDoesNotBlockOverride(t *testing.T) {
	writeGlobal(t, `{ not json`)
	proj := t.TempDir()
	t.Chdir(proj)
	good := filepath.Join(proj, "good.json")
	if err := os.WriteFile(good, []byte(validGlobal), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	cfg, err := LoadConfig(good)
	if err != nil {
		t.Fatalf("坏全局不应阻断有效 --config，得到: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("应使用 --config 的值，得到 provider=%q", cfg.Provider)
	}
}

// Tập tin不存在是正常情况（便携/首次），不能报错。
func TestLoadConfig_MissingFilesNoError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // ~/.ainovel/config.json 不存在
	t.Chdir(t.TempDir())   // 也没有 ./.ainovel/config.json

	if _, err := LoadConfig(""); err != nil {
		t.Fatalf("缺失Tập tin cấu hình不应报错，得到: %v", err)
	}
}

// 正常Đường dẫn：全局 + 项目级合并生效。
func TestLoadConfig_ValidMergeWorks(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "model": "google/gemini-2.5-pro",
  "thinking": "high",
  "roles": {
    "writer": {
      "provider": "openrouter",
      "model": "google/gemini-2.5-flash",
      "thinking": "low"
    }
  }
}`)

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("有效Cấu hình不应报错: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("provider 应保留全局值 openrouter，得到 %q", cfg.Provider)
	}
	if cfg.ModelName != "google/gemini-2.5-pro" {
		t.Errorf("model 应被项目级覆盖，得到 %q", cfg.ModelName)
	}
	if cfg.Thinking != "high" {
		t.Errorf("thinking 应被项目级覆盖，得到 %q", cfg.Thinking)
	}
	if got := cfg.Roles["writer"].Thinking; got != "low" {
		t.Errorf("roles.writer.thinking 应被项目级覆盖，得到 %q", got)
	}
}

func TestMergeConfig_ProviderExtraFields(t *testing.T) {
	base := Config{
		Provider:  "openrouter",
		ModelName: "google/gemini-2.5-flash",
		Providers: map[string]ProviderConfig{
			"openrouter": {
				APIKey: "sk-test-123456",
				ExtraBody: map[string]any{
					"temperature": 0.8,
				},
				Extra: map[string]any{
					"user_agent": "base-client/1.0",
				},
			},
		},
	}
	overlay := Config{
		Providers: map[string]ProviderConfig{
			"openrouter": {
				BaseURL: "https://proxy.example.com/v1",
				ExtraBody: map[string]any{
					"min_p": 0.05,
				},
				Extra: map[string]any{
					"user_agent": "override-client/1.0",
					"headers": map[string]any{
						"X-Custom-Client": "ainovel",
					},
				},
			},
		},
	}

	cfg := mergeConfig(base, overlay)
	pc := cfg.Providers["openrouter"]
	if pc.APIKey != "sk-test-123456" {
		t.Fatalf("APIKey = %q, want inherited key", pc.APIKey)
	}
	if pc.BaseURL != "https://proxy.example.com/v1" {
		t.Fatalf("BaseURL = %q, want overlay URL", pc.BaseURL)
	}
	if _, ok := pc.ExtraBody["temperature"]; ok {
		t.Fatalf("ExtraBody should be replaced by overlay, got %#v", pc.ExtraBody)
	}
	if got := pc.ExtraBody["min_p"]; got != 0.05 {
		t.Fatalf("ExtraBody[min_p] = %#v, want 0.05", got)
	}
	if got := pc.Extra["user_agent"]; got != "override-client/1.0" {
		t.Fatalf("Extra[user_agent] = %#v, want override-client/1.0", got)
	}
	headers, ok := pc.Extra["headers"].(map[string]any)
	if !ok {
		t.Fatalf("Extra[headers] missing or invalid: %#v", pc.Extra["headers"])
	}
	if got := headers["X-Custom-Client"]; got != "ainovel" {
		t.Fatalf("Extra.headers[X-Custom-Client] = %#v, want ainovel", got)
	}
}

// 根因 2（issue #37 核心复现）：项目级覆盖 provider 但没声明对应 providers 凭证，
// ValidateBase 必须报 config Lỗi（而非放行后在更深处崩溃）。
func TestValidateBase_ProviderOverrideWithoutCredentials(t *testing.T) {
	cfg := Config{
		Provider:  "mimo",
		ModelName: "mimo-v2.5-pro",
		Providers: map[string]ProviderConfig{
			"openrouter": {APIKey: "sk-test-123456"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("provider 缺凭证应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

// 内置示例（go:embed 的 config.example.jsonc）必须自洽：去注释后是合法 JSON、
// 顶层 provider 指针不悬Rỗng、且点破了“指针”心智——它是用户照抄的样板，自己坏了就坑人。
func TestExampleConfigIsValidAndSelfConsistent(t *testing.T) {
	if exampleConfig == "" {
		t.Fatal("go:embed 未生效，exampleConfig 为Rỗng")
	}
	var cfg Config
	if err := json.Unmarshal(stripJSONComments([]byte(exampleConfig)), &cfg); err != nil {
		t.Fatalf("内置示例去注释后不是合法 JSON（用户照抄即坑）: %v", err)
	}
	if cfg.Provider == "" || cfg.ModelName == "" {
		t.Fatal("示例应给出Mặc định provider/model")
	}
	if _, ok := cfg.Providers[cfg.Provider]; !ok {
		t.Errorf("示例顶层 provider %q 未指向 providers 中的条目——指针正面样板自己悬Rỗng了", cfg.Provider)
	}
	if !contains(exampleConfig, "指针") {
		t.Error("示例应点破“provider 是指针”——别让 #37 的认知陷阱回潮")
	}
}

func TestWriteStartupError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := WriteStartupError("boom: provider not configured")
	if path == "" {
		t.Fatal("应Quay lại落盘Đường dẫn")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Đọc last-error.log: %v", err)
	}
	if want := "boom: provider not configured"; !contains(string(data), want) {
		t.Errorf("日志应包含 %q，实际: %s", want, data)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
