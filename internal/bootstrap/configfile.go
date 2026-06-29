package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const configDirName = ".ainovel"

// DefaultConfigPath Quay lại全局Tập tin cấu hìnhĐường dẫn ~/.ainovel/config.json。
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName, "config.json")
}

// DefaultConfigDir Quay lại ~/.ainovel Thư mụcĐường dẫn；取不到家Thư mục时Quay lạiRỗng字符串。
// 仅用于读/写不强制存在的Tập tin（如Mô hình缓存），不会自动TạoThư mục。
func DefaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName)
}

// configDir Quay lại ~/.ainovel Thư mụcĐường dẫn，不存在时Tạo。
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, configDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return dir, nil
}

// projectConfigPath Quay lại项目级Tập tin cấu hình的相对Đường dẫn ./.ainovel/config.json。
// 项目级 dotdir 镜像全局 ~/.ainovel/，复用同一个 configDirName；相对 cwd 解析。
func projectConfigPath() string {
	return filepath.Join(configDirName, "config.json")
}

// LoadConfig 按优先级加载并合并Cấu hình：
//  1. ~/.ainovel/config.json（全局）
//  2. ./.ainovel/config.json（项目级覆盖）
//  3. flagPath 指定的Đường dẫn（最高优先级）
func LoadConfig(flagPath string) (Config, error) {
	var cfg Config

	// 1. 全局Cấu hình。它是最低优先级基底，坏Tập tin降级为告警而非阻断——可被项目级
	//    / --config 覆盖；硬Thất bại会把"坏全局 + 有效 --config"的用户挡在门外，
	//    违反 --config"我明确指定这个"的语义。
	if p := DefaultConfigPath(); p != "" {
		global, found, err := loadOptionalJSON(p)
		switch {
		case err != nil:
			slog.Warn("全局Cấu hình解析Thất bại，已忽略（可被项目级/--config 覆盖）", "module", "config", "path", p, "err", err)
		case found:
			cfg = global
		}
	}

	// 2. 项目级覆盖。坏Tập tin fail loud：用户在Hiện tạiThư mục主动放的Cấu hình，静默吞掉会让
	//    "配了不生效"Không có从排查（issue #37）。
	project, found, err := loadOptionalJSON(projectConfigPath())
	if err != nil {
		return cfg, fmt.Errorf("项目级Cấu hình ./.ainovel/config.json 解析Thất bại（Vui lòngKiểm tra JSON 语法）: %w", err)
	}
	if found {
		cfg = mergeConfig(cfg, project)
	}

	// 3. CLI flag 覆盖
	if flagPath != "" {
		override, err := loadJSONFile(flagPath)
		if err != nil {
			return cfg, fmt.Errorf("load config %s: %w", flagPath, err)
		}
		cfg = mergeConfig(cfg, override)
	}

	return cfg, nil
}

// loadOptionalJSON Đọc一个可选的Tập tin cấu hình：
//   - Tập tin不存在 → (zero, false, nil)，由调用方决定用Mặc định/上层值
//   - Tập tin存在但解析Thất bại → Quay lạiLỗi（不再静默吞掉——否则用户的Cấu hình"配了不生效"
//     却Không có从排查，正是 issue #37 的根因）
func loadOptionalJSON(path string) (Config, bool, error) {
	cfg, err := loadJSONFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	return cfg, true, nil
}

// LoadConfigFile Đọc单个 JSON Tập tin cấu hình，支持 // 行注释。
// 不做任何合并，仅Quay lại该Tập tin自身的Cấu hình。Tập tin不存在时Quay lạiLỗi。
func LoadConfigFile(path string) (Config, error) {
	return loadJSONFile(path)
}

// loadJSONFile Đọc JSON Tập tin cấu hình，支持 // 行注释。
// Tập tin不存在时Quay lạiLỗi（由调用方决定Có czy không忽略）。
func loadJSONFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cleaned := stripJSONComments(data)
	var cfg Config
	if err := json.Unmarshal(cleaned, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// mergeConfig 将 overlay 合并到 base 上。非零值字段覆盖，map 按 key 合并。
func mergeConfig(base, overlay Config) Config {
	if overlay.Provider != "" {
		base.Provider = overlay.Provider
	}
	if overlay.ModelName != "" {
		base.ModelName = overlay.ModelName
	}
	if overlay.Thinking != "" {
		base.Thinking = overlay.Thinking
	}
	if overlay.Style != "" {
		base.Style = overlay.Style
	}
	if overlay.ContextWindow > 0 {
		base.ContextWindow = overlay.ContextWindow
	}

	// Providers: overlay 的 key 覆盖 base 同名 key
	if len(overlay.Providers) > 0 {
		if base.Providers == nil {
			base.Providers = make(map[string]ProviderConfig)
		}
		for k, v := range overlay.Providers {
			existing := base.Providers[k]
			if v.Type != "" {
				existing.Type = v.Type
			}
			if v.APIKey != "" {
				existing.APIKey = v.APIKey
			}
			if v.BaseURL != "" {
				existing.BaseURL = v.BaseURL
			}
			if len(v.Models) > 0 {
				existing.Models = append([]string(nil), v.Models...)
			}
			if len(v.ExtraBody) > 0 {
				existing.ExtraBody = cloneMap(v.ExtraBody)
			}
			if len(v.Extra) > 0 {
				existing.Extra = cloneMap(v.Extra)
			}
			base.Providers[k] = existing
		}
	}

	// Roles: overlay 的 key 覆盖 base 同名 key
	if len(overlay.Roles) > 0 {
		if base.Roles == nil {
			base.Roles = make(map[string]RoleConfig)
		}
		for k, v := range overlay.Roles {
			existing := base.Roles[k]
			if v.Provider != "" {
				existing.Provider = v.Provider
			}
			if v.Model != "" {
				existing.Model = v.Model
			}
			if len(v.Fallbacks) > 0 {
				existing.Fallbacks = append([]ModelRef(nil), v.Fallbacks...)
			}
			if v.Thinking != "" {
				existing.Thinking = v.Thinking
			}
			base.Roles[k] = existing
		}
	}

	// Budget / Notify：整块覆盖（项目级预算/告警是独立政策声明，不与全局逐字段拼接）
	if overlay.Budget != (BudgetConfig{}) {
		base.Budget = overlay.Budget
	}
	if overlay.Notify.Enabled != nil || overlay.Notify.Command != "" || len(overlay.Notify.Events) > 0 {
		base.Notify = overlay.Notify
	}

	return base
}

func cloneMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// stripJSONComments 去除 JSON 中的 // 行注释，跟踪引号Trạng thái避免误删字符串内容。
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		b := data[i]

		if escaped {
			out = append(out, b)
			escaped = false
			continue
		}

		if inString {
			out = append(out, b)
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		// 不在字符串内
		if b == '"' {
			inString = true
			out = append(out, b)
			continue
		}

		// 检测 // 注释
		if b == '/' && i+1 < len(data) && data[i+1] == '/' {
			// 跳到行尾
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
			continue
		}

		out = append(out, b)
	}

	return out
}

// WriteStartupError 把启动期致命Lỗi追加写入 ~/.ainovel/last-error.log，并Quay lại
// 该Tập tinĐường dẫn（best-effort，Thất bại时Quay lạiRỗng字符串）。双击启动时控制台Cửa sổ会随进程
// Thoát立即关闭、Lỗi一闪而过，落盘是这类用户事后追溯的唯一途径。
func WriteStartupError(msg string) string {
	dir := DefaultConfigDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, "last-error.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "[%s] %s\n", time.Now().Format(time.RFC3339), msg); err != nil {
		return ""
	}
	return path
}

// SaveConfig 将Cấu hình写入指定Đường dẫn（JSON 格式，缩进美化）。
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
