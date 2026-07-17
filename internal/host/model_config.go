package host

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type APIKeyAction string

const (
	APIKeyKeep    APIKeyAction = "keep"
	APIKeyReplace APIKeyAction = "replace"
	APIKeyClear   APIKeyAction = "clear"
)

// ProviderSnapshot 是供 TUI 使用的脱敏 provider 配置。
type ProviderSnapshot struct {
	Name           string
	Type           string
	API            string
	BaseURL        string
	Models         []bootstrap.ModelConfig
	HasAPIKey      bool
	RequiresAPIKey bool
}

type ModelConfigurationSnapshot struct {
	Providers       []ProviderSnapshot
	DefaultProvider string
	DefaultModel    string
	Targets         []bootstrap.ConfigTarget
	References      map[string][]string
}

func (s ModelConfigurationSnapshot) ReferencesFor(provider, model string) []string {
	return append([]string(nil), s.References[modelReferenceKey(provider, model)]...)
}

// ModelConfigurationDraft 是 /config 提交给 Host 的完整模型配置草稿。
type ModelConfigurationDraft struct {
	Provider     string
	Type         string
	API          string
	BaseURL      string
	Models       []bootstrap.ModelConfig
	DefaultModel string
	APIKeyAction APIKeyAction
	APIKey       string
	TargetID     string
}

type ConfiguredModel struct {
	Name          string
	ContextWindow int
	ContextSource bootstrap.ContextWindowSource
}

func modelReferenceKey(provider, model string) string {
	return strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(model)
}

// ModelConfiguration 返回脱敏配置、可写目标和模型引用，绝不暴露现有 API Key。
func (h *Host) ModelConfiguration() ModelConfigurationSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	providers := make([]ProviderSnapshot, 0, len(h.cfg.Providers))
	for name, pc := range h.cfg.Providers {
		configuredModels := make([]bootstrap.ModelConfig, 0)
		for _, modelName := range h.cfg.CandidateModels(name) {
			model, ok := pc.ModelConfig(modelName)
			if !ok {
				model = bootstrap.ModelConfig{Name: modelName}
			}
			configuredModels = append(configuredModels, model)
		}
		providers = append(providers, ProviderSnapshot{
			Name: name, Type: pc.Type, API: pc.API, BaseURL: pc.BaseURL,
			Models:    configuredModels,
			HasAPIKey: pc.APIKey != "", RequiresAPIKey: pc.RequiresAPIKey(name),
		})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })

	refs := make(map[string][]string)
	refs[modelReferenceKey(h.cfg.Provider, h.cfg.ModelName)] = append(
		refs[modelReferenceKey(h.cfg.Provider, h.cfg.ModelName)], "default")
	for role, rc := range h.cfg.Roles {
		key := modelReferenceKey(rc.Provider, rc.Model)
		refs[key] = append(refs[key], role)
		for i, fallback := range rc.Fallbacks {
			key = modelReferenceKey(fallback.Provider, fallback.Model)
			refs[key] = append(refs[key], fmt.Sprintf("%s fallback[%d]", role, i))
		}
	}
	for key := range refs {
		sort.Strings(refs[key])
	}

	return ModelConfigurationSnapshot{
		Providers: providers, DefaultProvider: h.cfg.Provider, DefaultModel: h.cfg.ModelName,
		Targets: append([]bootstrap.ConfigTarget(nil), h.configTargets...), References: refs,
	}
}

func (h *Host) ConfiguredModelOptions(provider string) []ConfiguredModel {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := h.cfg.CandidateModels(provider)
	out := make([]ConfiguredModel, 0, len(names))
	for _, name := range names {
		window, source := h.cfg.ResolveContextWindow(provider, name)
		out = append(out, ConfiguredModel{Name: name, ContextWindow: window, ContextSource: source})
	}
	return out
}

// ConfigureModels 校验、持久化并热应用一个 provider 的模型库。
func (h *Host) ConfigureModels(draft ModelConfigurationDraft) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	draft.Provider = strings.TrimSpace(draft.Provider)
	draft.Type = strings.ToLower(strings.TrimSpace(draft.Type))
	draft.API = strings.ToLower(strings.TrimSpace(draft.API))
	draft.BaseURL = strings.TrimSpace(draft.BaseURL)
	draft.DefaultModel = strings.TrimSpace(draft.DefaultModel)
	draft.APIKey = strings.TrimSpace(draft.APIKey)
	if draft.Provider == "" || draft.DefaultModel == "" {
		return fmt.Errorf("provider 和默认模型不能为空")
	}

	candidate := bootstrap.CloneConfig(h.cfg)
	pc := candidate.Providers[draft.Provider]
	oldModels := append([]bootstrap.ModelConfig(nil), pc.Models...)
	pc.SetType(draft.Type)
	pc.SetAPI(draft.API)
	pc.SetBaseURL(draft.BaseURL)
	configuredModels := make([]bootstrap.ModelConfig, 0, len(draft.Models))
	defaultFound := false
	seen := make(map[string]bool, len(draft.Models))
	for _, model := range draft.Models {
		model.Name = strings.TrimSpace(model.Name)
		if model.Name == "" {
			return fmt.Errorf("模型名称不能为空")
		}
		if model.ContextWindow < 0 {
			return fmt.Errorf("模型 %q 的上下文窗口不能为负数", model.Name)
		}
		if seen[model.Name] {
			return fmt.Errorf("模型 %q 重复", model.Name)
		}
		seen[model.Name] = true
		defaultFound = defaultFound || model.Name == draft.DefaultModel
		configuredModels = append(configuredModels, model)
	}
	pc.SetModels(configuredModels)
	if !defaultFound {
		return fmt.Errorf("默认模型 %q 必须保存在模型列表中", draft.DefaultModel)
	}

	switch draft.APIKeyAction {
	case "", APIKeyKeep:
		// 保留候选配置里的现有值；新增 provider 时自然为空。
	case APIKeyReplace:
		pc.SetAPIKey(draft.APIKey)
	case APIKeyClear:
		pc.SetAPIKey("")
	default:
		return fmt.Errorf("未知 API Key 操作 %q", draft.APIKeyAction)
	}

	newNames := make(map[string]bool, len(pc.Models))
	for _, model := range pc.Models {
		newNames[model.Name] = true
	}
	for _, old := range oldModels {
		if newNames[old.Name] {
			continue
		}
		for _, ref := range h.modelReferencesLocked(draft.Provider, old.Name) {
			if ref == "default" {
				continue // 本次提交会把 default 原子切到 draft.DefaultModel。
			}
			return fmt.Errorf("模型 %q 仍被 %s 引用，不能删除", old.Name, ref)
		}
	}

	if candidate.Providers == nil {
		candidate.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	candidate.Providers[draft.Provider] = pc
	candidate.Provider = draft.Provider
	candidate.ModelName = draft.DefaultModel
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	prepared, err := bootstrap.NewModelSet(candidate)
	if err != nil {
		return fmt.Errorf("创建模型客户端失败: %w", err)
	}

	var target bootstrap.ConfigTarget
	foundTarget := false
	for _, item := range h.configTargets {
		if item.ID == draft.TargetID {
			target = item
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		return fmt.Errorf("未知配置保存目标 %q", draft.TargetID)
	}
	if err := bootstrap.SaveModelConfig(target.Path, draft.Provider, pc, draft.DefaultModel); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}

	h.models.ApplyPrepared(prepared)
	h.cfg = candidate
	h.normalizeThinkingLocked("default")
	h.applyThinkingLocked("default")
	window, source := h.cfg.ResolveContextWindow(draft.Provider, draft.DefaultModel)
	bootstrap.LogContextWindowChoice("default", draft.DefaultModel, window, source)
	h.emitEvent(Event{
		Time: time.Now(), Category: "SYSTEM", Level: "info",
		Summary: fmt.Sprintf("模型配置已保存：%s/%s → %s", draft.Provider, draft.DefaultModel, target.Path),
	})
	return nil
}

func (h *Host) modelReferencesLocked(provider, model string) []string {
	var refs []string
	if h.cfg.Provider == provider && h.cfg.ModelName == model {
		refs = append(refs, "default")
	}
	for role, rc := range h.cfg.Roles {
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
