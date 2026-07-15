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

// DefaultConfigPath 返回全局配置文件路径 ~/.ainovel/config.json。
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName, "config.json")
}

// DefaultConfigDir 返回 ~/.ainovel 目录路径；取不到家目录时返回空字符串。
// 仅用于读/写不强制存在的文件（如模型缓存），不会自动创建目录。
func DefaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName)
}

// configDir 返回 ~/.ainovel 目录路径，不存在时创建。
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

// projectConfigPath 返回项目级配置文件的相对路径 ./.ainovel/config.json。
// 项目级 dotdir 镜像全局 ~/.ainovel/，复用同一个 configDirName；相对 cwd 解析。
func projectConfigPath() string {
	return filepath.Join(configDirName, "config.json")
}

// ConfigTarget 是 /config 可选择的配置写入位置。
type ConfigTarget struct {
	ID         string
	Label      string
	Path       string
	Precedence int
	Exists     bool
}

// ConfigTargets 返回从高到低排列的可写配置目标。flagPath 为空时不提供指定文件项。
func ConfigTargets(flagPath string) []ConfigTarget {
	var targets []ConfigTarget
	seen := make(map[string]bool)
	add := func(id, label, path string, precedence int) {
		if path == "" {
			return
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		_, err := os.Stat(path)
		targets = append(targets, ConfigTarget{
			ID: id, Label: label, Path: path, Precedence: precedence, Exists: err == nil,
		})
	}
	add("flag", "--config 指定文件", flagPath, 3)
	add("project", "当前项目配置", projectConfigPath(), 2)
	add("global", "全局配置", DefaultConfigPath(), 1)
	return targets
}

// LoadConfig 按优先级加载并合并配置：
//  1. ~/.ainovel/config.json（全局）
//  2. ./.ainovel/config.json（项目级覆盖）
//  3. flagPath 指定的路径（最高优先级）
func LoadConfig(flagPath string) (Config, error) {
	var cfg Config

	// 1. 全局配置。它是最低优先级基底，坏文件降级为告警而非阻断——可被项目级
	//    / --config 覆盖；硬失败会把"坏全局 + 有效 --config"的用户挡在门外，
	//    违反 --config"我明确指定这个"的语义。
	if p := DefaultConfigPath(); p != "" {
		global, found, err := loadOptionalJSON(p)
		switch {
		case err != nil:
			slog.Warn("全局配置解析失败，已忽略（可被项目级/--config 覆盖）", "module", "config", "path", p, "err", err)
		case found:
			cfg = global
		}
	}

	// 2. 项目级覆盖。坏文件 fail loud：用户在当前目录主动放的配置，静默吞掉会让
	//    "配了不生效"无从排查（issue #37）。
	project, found, err := loadOptionalJSON(projectConfigPath())
	if err != nil {
		return cfg, fmt.Errorf("项目级配置 ./.ainovel/config.json 解析失败（请检查 JSON 语法）: %w", err)
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

// loadOptionalJSON 读取一个可选的配置文件：
//   - 文件不存在 → (zero, false, nil)，由调用方决定用默认/上层值
//   - 文件存在但解析失败 → 返回错误（不再静默吞掉——否则用户的配置"配了不生效"
//     却无从排查，正是 issue #37 的根因）
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

// LoadConfigFile 读取单个 JSON 配置文件，支持 // 行注释。
// 不做任何合并，仅返回该文件自身的配置。文件不存在时返回错误。
func LoadConfigFile(path string) (Config, error) {
	return loadJSONFile(path)
}

// loadJSONFile 读取 JSON 配置文件，支持 // 行注释。
// 文件不存在时返回错误（由调用方决定是否忽略）。
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
	if overlay.ReasoningEffort != "" {
		base.ReasoningEffort = overlay.ReasoningEffort
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
			if v.typeSet || v.Type != "" {
				existing.Type = v.Type
				existing.typeSet = true
			}
			if v.apiSet || v.API != "" {
				existing.API = v.API
				existing.apiSet = true
			}
			if v.apiKeySet || v.APIKey != "" {
				existing.APIKey = v.APIKey
				existing.apiKeySet = true
			}
			if v.baseURLSet || v.BaseURL != "" {
				existing.BaseURL = v.BaseURL
				existing.baseURLSet = true
			}
			if v.modelsSet || len(v.Models) > 0 {
				existing.Models = append([]ModelConfig(nil), v.Models...)
				existing.modelsSet = true
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
			if v.ReasoningEffort != "" {
				existing.ReasoningEffort = v.ReasoningEffort
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

// CloneConfig 深拷贝配置中会在运行时修改的 map/slice，避免候选配置污染当前配置。
func CloneConfig(cfg Config) Config {
	clone := cfg
	clone.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		pc.Models = append([]ModelConfig(nil), pc.Models...)
		pc.Extra = cloneMap(pc.Extra)
		pc.ExtraBody = cloneMap(pc.ExtraBody)
		clone.Providers[name] = pc
	}
	clone.Roles = make(map[string]RoleConfig, len(cfg.Roles))
	for role, rc := range cfg.Roles {
		rc.Fallbacks = append([]ModelRef(nil), rc.Fallbacks...)
		clone.Roles[role] = rc
	}
	clone.Notify.Events = append([]string(nil), cfg.Notify.Events...)
	return clone
}

// SaveModelConfig 补丁式更新目标配置层的 provider 库和默认模型。
// 目标不存在时创建最小配置；目标损坏时拒绝覆盖。
func SaveModelConfig(path string, provider string, pc ProviderConfig, model string) error {
	target, found, err := loadOptionalJSON(path)
	if err != nil {
		return err
	}
	if !found {
		target = Config{}
	}
	if target.Providers == nil {
		target.Providers = make(map[string]ProviderConfig)
	}
	target.Provider = provider
	target.ModelName = model
	target.Providers[provider] = pc
	return SaveConfig(path, target)
}

// stripJSONComments 去除 JSON 中的 // 行注释，跟踪引号状态避免误删字符串内容。
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

// WriteStartupError 把启动期致命错误追加写入 ~/.ainovel/last-error.log，并返回
// 该文件路径（best-effort，失败时返回空字符串）。双击启动时控制台窗口会随进程
// 退出立即关闭、错误一闪而过，落盘是这类用户事后追溯的唯一途径。
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

// SaveConfig 将配置写入指定路径（JSON 格式，缩进美化）。
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
