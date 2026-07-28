package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

// ── 首次引导（替代终端版 bootstrap.RunSetup） ──
//
// RunSetup 是 Bubbletea 终端交互，桌面版绝不调用它：改为前端表单收集同样的四项
// （Provider / API Key / Base URL / 模型），由 SaveInitialConfig 组装 Config 并落盘，
// 与 setup.go 生成的结构保持一致。

// ProviderPreset 是投影给前端的 provider 预设。
type ProviderPreset struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	BaseURL        string `json:"baseURL"`
	NeedType       bool   `json:"needType"`
	APIKeyOptional bool   `json:"apiKeyOptional"`
}

// GetProviderPresets 返回引导页可选的 provider 预设列表。
func (a *App) GetProviderPresets() []ProviderPreset {
	presets := bootstrap.ProviderPresets()
	out := make([]ProviderPreset, 0, len(presets))
	for _, p := range presets {
		out = append(out, ProviderPreset{
			Name: p.Name, Label: p.Label, BaseURL: p.BaseURL,
			NeedType: p.NeedType, APIKeyOptional: p.APIKeyOptional,
		})
	}
	return out
}

// InitialSetup 是引导表单提交的内容。Provider 为 "custom" 时用 CustomName + Type。
type InitialSetup struct {
	Provider   string `json:"provider"`
	CustomName string `json:"customName"`
	Type       string `json:"type"`
	APIKey     string `json:"apiKey"`
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
}

// SaveInitialConfig 用引导表单生成全局配置并落盘（写 ~/.ainovel/config.json）。
// 成功后调用方应接着 OpenBook 打开默认书目录。
func (a *App) SaveInitialConfig(in InitialSetup) error {
	name := strings.TrimSpace(in.Provider)
	if name == "" {
		return fmt.Errorf("请选择 Provider")
	}
	providerType := strings.ToLower(strings.TrimSpace(in.Type))
	if name == "custom" {
		name = strings.TrimSpace(in.CustomName)
		if name == "" {
			return fmt.Errorf("请填写自定义 Provider 名称")
		}
		if providerType == "" {
			return fmt.Errorf("自定义 Provider 需要选择 API 协议类型")
		}
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		return fmt.Errorf("请填写模型名称")
	}

	pc := bootstrap.ProviderConfig{
		Type:    providerType,
		APIKey:  strings.TrimSpace(in.APIKey),
		BaseURL: strings.TrimSpace(in.BaseURL),
		Models:  []bootstrap.ModelConfig{{Name: model}},
	}
	if pc.RequiresAPIKey(name) && pc.APIKey == "" {
		return fmt.Errorf("Provider %q 必须配置 API Key", name)
	}

	cfg := bootstrap.Config{
		Provider:  name,
		ModelName: model,
		Providers: map[string]bootstrap.ProviderConfig{name: pc},
		Roles:     map[string]bootstrap.RoleConfig{},
		Style:     "default",
	}
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return err
	}
	if err := bootstrap.SaveConfig(bootstrap.DefaultConfigPath(), cfg); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}

	// 让后续 OpenBook 读到新配置。
	a.mu.Lock()
	a.baseCfg = cfg
	a.cfgLoaded = true
	a.mu.Unlock()
	return nil
}

// ── /config：Provider 与模型库 ──

// ProviderView 是脱敏后的 provider 配置（绝不含完整 API Key）。
type ProviderView struct {
	Name           string      `json:"name"`
	Type           string      `json:"type"`
	API            string      `json:"api"`
	BaseURL        string      `json:"baseURL"`
	Models         []ModelView `json:"models"`
	HasAPIKey      bool        `json:"hasAPIKey"`
	APIKeyHint     string      `json:"apiKeyHint"`
	RequiresAPIKey bool        `json:"requiresAPIKey"`
}

type ModelView struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	// References 是引用该模型的角色（default / writer / …）。非空表示不能直接删除。
	References []string `json:"references"`
}

type ConfigView struct {
	Providers       []ProviderView `json:"providers"`
	DefaultProvider string         `json:"defaultProvider"`
	DefaultModel    string         `json:"defaultModel"`
	ConfigPath      string         `json:"configPath"`
}

func (a *App) loadedConfig() (bootstrap.Config, error) {
	if err := a.ensureConfig(); err != nil {
		return bootstrap.Config{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return bootstrap.CloneConfig(a.baseCfg), nil
}

func (a *App) refreshConfigCache() error {
	cfg, err := bootstrap.LoadConfig()
	if err != nil {
		return fmt.Errorf("重新加载配置失败: %w", err)
	}
	cfg.FillDefaults()
	a.mu.Lock()
	a.baseCfg = cfg
	a.cfgLoaded = true
	a.mu.Unlock()
	return nil
}

func configViewFrom(cfg bootstrap.Config) ConfigView {
	refs := make(map[string][]string)
	refKey := func(provider, model string) string { return provider + "\x00" + model }
	refs[refKey(cfg.Provider, cfg.ModelName)] = append(refs[refKey(cfg.Provider, cfg.ModelName)], "default")
	for role, rc := range cfg.Roles {
		refs[refKey(rc.Provider, rc.Model)] = append(refs[refKey(rc.Provider, rc.Model)], role)
		for i, fallback := range rc.Fallbacks {
			key := refKey(fallback.Provider, fallback.Model)
			refs[key] = append(refs[key], fmt.Sprintf("%s fallback[%d]", role, i))
		}
	}

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := ConfigView{
		DefaultProvider: cfg.Provider,
		DefaultModel:    cfg.ModelName,
		ConfigPath:      bootstrap.EffectiveConfigPath(),
		Providers:       make([]ProviderView, 0, len(names)),
	}
	for _, name := range names {
		pc := cfg.Providers[name]
		models := make([]ModelView, 0)
		for _, modelName := range cfg.CandidateModels(name) {
			model, ok := pc.ModelConfig(modelName)
			if !ok {
				model = bootstrap.ModelConfig{Name: modelName}
			}
			modelRefs := append([]string(nil), refs[refKey(name, modelName)]...)
			sort.Strings(modelRefs)
			models = append(models, ModelView{
				Name: model.Name, ContextWindow: model.ContextWindow, References: modelRefs,
			})
		}
		out.Providers = append(out.Providers, ProviderView{
			Name: name, Type: pc.Type, API: pc.API, BaseURL: pc.BaseURL,
			Models: models, HasAPIKey: pc.APIKey != "", APIKeyHint: host.MaskAPIKey(pc.APIKey),
			RequiresAPIKey: pc.RequiresAPIKey(name),
		})
	}
	return out
}

// GetConfig 读取当前生效配置的脱敏快照（供设置页渲染）。不要求先打开书。
func (a *App) GetConfig() (ConfigView, error) {
	cfg, err := a.loadedConfig()
	if err != nil {
		return ConfigView{}, err
	}
	return configViewFrom(cfg), nil
}

// ProviderDraft 是设置页提交的单个 provider 草稿。
// apiKeyAction: keep（不动现有 key）/ replace（用 apiKey 覆盖）/ clear（清空）。
type ProviderDraft struct {
	Provider     string        `json:"provider"`
	Type         string        `json:"type"`
	API          string        `json:"api"`
	BaseURL      string        `json:"baseURL"`
	Models       []ModelDraft  `json:"models"`
	Renames      []RenameDraft `json:"renames"`
	APIKeyAction string        `json:"apiKeyAction"`
	APIKey       string        `json:"apiKey"`
}

type ModelDraft struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
}

type RenameDraft struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// toHostDraft 把前端草稿翻译成 Host 的 ModelConfigurationDraft。
func (d ProviderDraft) toHostDraft() (host.ModelConfigurationDraft, error) {
	action := host.APIKeyAction(strings.TrimSpace(d.APIKeyAction))
	switch action {
	case "", host.APIKeyKeep, host.APIKeyReplace, host.APIKeyClear:
	default:
		return host.ModelConfigurationDraft{}, fmt.Errorf("未知的 API Key 操作: %q", d.APIKeyAction)
	}
	models := make([]bootstrap.ModelConfig, 0, len(d.Models))
	for _, m := range d.Models {
		models = append(models, bootstrap.ModelConfig{
			Name: m.Name, ContextWindow: m.ContextWindow,
		})
	}
	renames := make([]host.ModelRename, 0, len(d.Renames))
	for _, r := range d.Renames {
		renames = append(renames, host.ModelRename{From: r.From, To: r.To})
	}
	return host.ModelConfigurationDraft{
		Provider: d.Provider, Type: d.Type, API: d.API, BaseURL: d.BaseURL,
		Models: models, Renames: renames, APIKeyAction: action, APIKey: d.APIKey,
	}, nil
}

func prepareProviderConfig(cfg bootstrap.Config, draft host.ModelConfigurationDraft) (bootstrap.Config, bootstrap.ProviderConfig, bool, error) {
	draft.Provider = strings.TrimSpace(draft.Provider)
	draft.Type = strings.ToLower(strings.TrimSpace(draft.Type))
	draft.API = strings.ToLower(strings.TrimSpace(draft.API))
	draft.BaseURL = strings.TrimSpace(draft.BaseURL)
	draft.APIKey = strings.TrimSpace(draft.APIKey)
	if draft.Provider == "" {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("provider 不能为空")
	}
	if len(draft.Models) == 0 {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("请至少配置一个模型")
	}

	candidate := bootstrap.CloneConfig(cfg)
	if candidate.Providers == nil {
		candidate.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	pc := candidate.Providers[draft.Provider]
	oldNames := candidate.CandidateModels(draft.Provider)
	pc.Type = draft.Type
	pc.API = draft.API
	pc.BaseURL = draft.BaseURL
	pc.Models = make([]bootstrap.ModelConfig, 0, len(draft.Models))
	newNames := make(map[string]bool, len(draft.Models))
	for _, model := range draft.Models {
		model.Name = strings.TrimSpace(model.Name)
		if model.Name == "" {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("模型名称不能为空")
		}
		if model.ContextWindow < 0 {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("模型 %q 的上下文窗口不能为负数", model.Name)
		}
		if newNames[model.Name] {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("模型 %q 重复", model.Name)
		}
		newNames[model.Name] = true
		pc.Models = append(pc.Models, model)
	}
	switch draft.APIKeyAction {
	case "", host.APIKeyKeep:
	case host.APIKeyReplace:
		pc.APIKey = draft.APIKey
	case host.APIKeyClear:
		pc.APIKey = ""
	default:
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("未知 API Key 操作 %q", draft.APIKeyAction)
	}
	if pc.RequiresAPIKey(draft.Provider) && pc.APIKey == "" {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("Provider %q 必须配置 API Key", draft.Provider)
	}
	candidate.Providers[draft.Provider] = pc

	oldSet := make(map[string]bool, len(oldNames))
	for _, name := range oldNames {
		oldSet[name] = true
	}
	renames := make(map[string]string, len(draft.Renames))
	targets := make(map[string]bool, len(draft.Renames))
	for _, rename := range draft.Renames {
		from := strings.TrimSpace(rename.From)
		to := strings.TrimSpace(rename.To)
		if from == "" || to == "" {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("模型重命名的原名称和新名称不能为空")
		}
		if from == to {
			continue
		}
		if !oldSet[from] {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("无法重命名不存在的模型 %q", from)
		}
		if !newNames[to] {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("重命名目标模型 %q 不在当前模型列表中", to)
		}
		if _, exists := renames[from]; exists {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("模型 %q 被重复重命名", from)
		}
		if targets[to] {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("多个模型不能同时重命名为 %q", to)
		}
		renames[from] = to
		targets[to] = true
	}

	if renamed, ok := renames[candidate.ModelName]; ok && candidate.Provider == draft.Provider {
		candidate.ModelName = renamed
	}
	for role, rc := range candidate.Roles {
		changed := false
		if rc.Provider == draft.Provider {
			if renamed, ok := renames[rc.Model]; ok {
				rc.Model = renamed
				changed = true
			}
		}
		for i := range rc.Fallbacks {
			if rc.Fallbacks[i].Provider == draft.Provider {
				if renamed, ok := renames[rc.Fallbacks[i].Model]; ok {
					rc.Fallbacks[i].Model = renamed
					changed = true
				}
			}
		}
		if changed {
			candidate.Roles[role] = rc
		}
	}
	// 配置文件存在但默认选择为空、指向已删除的 provider/model 时，桌面端仍要能
	// 从设置页自救。保存当前 provider 时把无效默认值收敛到它的第一个模型，并要求
	// 整份配置写回；否则补丁式 SaveProviderConfig 只会新增 provider，顶层默认值
	// 仍然无效，用户依旧打不开任何书。
	defaultValid := false
	for _, name := range candidate.CandidateModels(candidate.Provider) {
		if name == candidate.ModelName {
			defaultValid = true
			break
		}
	}
	repairedDefault := false
	if !defaultValid {
		candidate.Provider = draft.Provider
		candidate.ModelName = pc.Models[0].Name
		repairedDefault = true
	}

	modelRefs := func(provider, model string) []string {
		var refs []string
		if candidate.Provider == provider && candidate.ModelName == model {
			refs = append(refs, "default")
		}
		for role, rc := range candidate.Roles {
			if rc.Provider == provider && rc.Model == model {
				refs = append(refs, role)
			}
			for i, fallback := range rc.Fallbacks {
				if fallback.Provider == provider && fallback.Model == model {
					refs = append(refs, fmt.Sprintf("%s fallback[%d]", role, i))
				}
			}
		}
		sort.Strings(refs)
		return refs
	}
	for _, old := range oldNames {
		if newNames[old] {
			continue
		}
		if _, renamed := renames[old]; renamed {
			continue
		}
		if refs := modelRefs(draft.Provider, old); len(refs) > 0 {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false,
				fmt.Errorf("模型 %q 仍被 %s 引用，请先在模型与角色页切换后再删除", old, strings.Join(refs, "、"))
		}
	}
	if err := candidate.ValidateBase(); err != nil {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, err
	}
	if _, err := bootstrap.NewModelSet(candidate); err != nil {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("创建模型客户端失败: %w", err)
	}
	return candidate, pc, len(renames) > 0 || repairedDefault, nil
}

// SaveProvider 校验、持久化并热应用一个 provider 的配置。
func (a *App) SaveProvider(draft ProviderDraft) error {
	hd, err := draft.toHostDraft()
	if err != nil {
		return err
	}
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h != nil {
		if err := h.ConfigureModels(hd); err != nil {
			return err
		}
		return a.refreshConfigCache()
	}

	cfg, err := a.loadedConfig()
	if err != nil {
		return err
	}
	candidate, pc, fullSave, err := prepareProviderConfig(cfg, hd)
	if err != nil {
		return err
	}
	path := bootstrap.EffectiveConfigPath()
	if fullSave {
		err = bootstrap.SaveConfig(path, candidate)
	} else {
		err = bootstrap.SaveProviderConfig(path, strings.TrimSpace(hd.Provider), pc)
	}
	if err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return a.refreshConfigCache()
}

// TestConnection 用草稿构造临时客户端发一个最小真实请求（会产生少量 API 用量）。
// 不保存配置、不切换运行时模型。
func (a *App) TestConnection(draft ProviderDraft, modelName string) error {
	hd, err := draft.toHostDraft()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h != nil {
		return h.TestModelConnection(ctx, hd, modelName)
	}
	cfg, err := a.loadedConfig()
	if err != nil {
		return err
	}
	candidate, pc, _, err := prepareProviderConfig(cfg, hd)
	if err != nil {
		return err
	}
	modelName = strings.TrimSpace(modelName)
	found := false
	for _, model := range pc.Models {
		found = found || model.Name == modelName
	}
	if !found {
		return fmt.Errorf("连接测试模型 %q 不在当前模型列表中", modelName)
	}
	candidate.Provider = strings.TrimSpace(hd.Provider)
	candidate.ModelName = modelName
	candidate.Roles = nil
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	models, err := bootstrap.NewModelSet(candidate)
	if err != nil {
		return fmt.Errorf("创建测试模型客户端失败: %w", err)
	}
	if _, err := models.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("Reply OK.")}, nil); err != nil {
		return fmt.Errorf("连接测试失败（%s/%s）: %w", hd.Provider, modelName, err)
	}
	return nil
}

// ── /model：角色 → provider/模型/推理强度 ──

// RoleSelection 是某个角色当前的模型与推理强度选择。
type RoleSelection struct {
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Explicit 为 false 表示该角色未单独配置，继承 default。
	Explicit  bool     `json:"explicit"`
	Thinking  string   `json:"thinking"`
	Available []string `json:"available"` // 该角色当前模型支持的推理强度
}

// ModelOption 是某 provider 下的一个可选模型。
type ModelOption struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	ContextSource string `json:"contextSource"`
}

// ModelsView 汇总 /model 面板所需的全部数据，一次调用取回。
type ModelsView struct {
	Providers []string                 `json:"providers"`
	Models    map[string][]ModelOption `json:"models"` // provider → 模型列表
	Roles     []RoleSelection          `json:"roles"`
}

// desktopRoles 是设置页展示的角色。与 TUI /model 面板一致（导入管线的
// import_* 档位属高级配置，仍走配置文件维护）。
var desktopRoles = []string{"default", "architect", "writer", "editor"}

// GetModels 返回角色/provider/模型/推理强度的当前状态。
func (a *App) GetModels() (ModelsView, error) {
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h == nil {
		cfg, err := a.loadedConfig()
		if err != nil {
			return ModelsView{}, err
		}
		providers := make([]string, 0, len(cfg.Providers))
		models := make(map[string][]ModelOption, len(cfg.Providers))
		for provider := range cfg.Providers {
			providers = append(providers, provider)
		}
		sort.Strings(providers)
		for _, provider := range providers {
			for _, name := range cfg.CandidateModels(provider) {
				window, source := cfg.ResolveContextWindow(provider, name)
				models[provider] = append(models[provider], ModelOption{
					Name: name, ContextWindow: window, ContextSource: string(source),
				})
			}
		}
		available := []string{"off", "low", "medium", "high", "xhigh", "max"}
		roles := make([]RoleSelection, 0, len(desktopRoles))
		for _, role := range desktopRoles {
			provider, model, explicit := cfg.Provider, cfg.ModelName, role == "default"
			if role != "default" {
				if rc, ok := cfg.Roles[role]; ok && rc.Provider != "" && rc.Model != "" {
					provider, model, explicit = rc.Provider, rc.Model, true
				}
			}
			roles = append(roles, RoleSelection{
				Role: role, Provider: provider, Model: model, Explicit: explicit,
				Thinking: cfg.ResolveReasoningEffort(role), Available: append([]string(nil), available...),
			})
		}
		return ModelsView{Providers: providers, Models: models, Roles: roles}, nil
	}
	providers := h.ConfiguredProviders()
	models := make(map[string][]ModelOption, len(providers))
	for _, p := range providers {
		opts := h.ConfiguredModelOptions(p)
		list := make([]ModelOption, 0, len(opts))
		for _, o := range opts {
			list = append(list, ModelOption{
				Name: o.Name, ContextWindow: o.ContextWindow,
				ContextSource: string(o.ContextSource),
			})
		}
		models[p] = list
	}

	roles := make([]RoleSelection, 0, len(desktopRoles))
	for _, role := range desktopRoles {
		provider, model, explicit := h.CurrentModelSelection(role)
		levels := h.AvailableThinking(role)
		available := make([]string, 0, len(levels))
		for _, l := range levels {
			available = append(available, string(l))
		}
		roles = append(roles, RoleSelection{
			Role: role, Provider: provider, Model: model, Explicit: explicit,
			Thinking: h.CurrentThinking(role), Available: available,
		})
	}
	return ModelsView{Providers: providers, Models: models, Roles: roles}, nil
}

// SwitchModel 切换某角色的 provider/模型（role 传 "default" 即默认档）。
func (a *App) SwitchModel(role, provider, model string) error {
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h != nil {
		if err := h.SwitchModel(normalizeRole(role), provider, model); err != nil {
			return err
		}
		return a.refreshConfigCache()
	}
	cfg, err := a.loadedConfig()
	if err != nil {
		return err
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	found := false
	for _, candidate := range cfg.CandidateModels(provider) {
		found = found || candidate == model
	}
	if !found {
		return fmt.Errorf("模型 %s/%s 未配置", provider, model)
	}
	role = normalizeRole(role)
	if role == "" {
		cfg.Provider, cfg.ModelName = provider, model
	} else {
		rc := cfg.Roles[role]
		rc.Provider, rc.Model = provider, model
		cfg.Roles[role] = rc
	}
	if err := cfg.ValidateBase(); err != nil {
		return err
	}
	if err := bootstrap.SaveConfig(bootstrap.EffectiveConfigPath(), cfg); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return a.refreshConfigCache()
}

// SetRoleThinking 设置某角色的推理强度。level 传空串表示继承上层默认。
func (a *App) SetRoleThinking(role, level string) error {
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h != nil {
		role = normalizeRole(role)
		if role != "" {
			provider, model, explicit := h.CurrentModelSelection(role)
			if !explicit {
				// RoleConfig 要求 provider/model 成对存在；为继承角色单独设置强度时，
				// 先把当前继承到的选择显式落盘，避免生成半截配置。
				if err := h.SwitchModel(role, provider, model); err != nil {
					return err
				}
			}
		}
		if err := h.SetRoleThinking(role, level); err != nil {
			return err
		}
		return a.refreshConfigCache()
	}
	parsed, err := agents.ParseThinkingLevel(level)
	if err != nil {
		return err
	}
	cfg, err := a.loadedConfig()
	if err != nil {
		return err
	}
	role = normalizeRole(role)
	if role == "" {
		cfg.ReasoningEffort = string(parsed)
	} else {
		rc := cfg.Roles[role]
		if rc.Provider == "" || rc.Model == "" {
			rc.Provider, rc.Model = cfg.Provider, cfg.ModelName
		}
		rc.ReasoningEffort = string(parsed)
		cfg.Roles[role] = rc
	}
	if err := cfg.ValidateBase(); err != nil {
		return err
	}
	if err := bootstrap.SaveConfig(bootstrap.EffectiveConfigPath(), cfg); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return a.refreshConfigCache()
}

// normalizeRole 把前端的 "default" 映射为 Host 侧的默认档语义（空串或 default 皆可）。
func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "default" {
		return ""
	}
	return role
}
