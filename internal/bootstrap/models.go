package bootstrap

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm"
)

// SwappableModel 是可热切换的 ChatModel 包装器。
// 已开始的请求继续使用旧实例；后续请求自动切到新实例。
type SwappableModel struct {
	*agentcore.SwappableModel
	mu       sync.RWMutex
	provider string
	name     string
}

func NewSwappableModel(provider, name string, model agentcore.ChatModel) *SwappableModel {
	return &SwappableModel{
		SwappableModel: agentcore.NewSwappableModel(model),
		provider:       provider,
		name:           name,
	}
}

func (m *SwappableModel) ProviderName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider
}

func (m *SwappableModel) Info() llm.ModelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, ok := m.SwappableModel.Current().(interface{ Info() llm.ModelInfo }); ok {
		modelInfo := info.Info()
		if modelInfo.Name == "" {
			modelInfo.Name = m.name
		}
		if modelInfo.Provider == "" {
			modelInfo.Provider = m.provider
		}
		return modelInfo
	}
	return llm.ModelInfo{
		Name:     m.name,
		Provider: m.provider,
	}
}

func (m *SwappableModel) Swap(provider, name string, model agentcore.ChatModel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SwappableModel.Swap(model)
	m.provider = provider
	m.name = name
}

func (m *SwappableModel) Current() (provider, name string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider, m.name
}

// ModelSet 持有按角色分配的模型实例，未配置的角色回退到默认模型。
type ModelSet struct {
	Default *SwappableModel
	models  map[string]*SwappableModel
	config  Config
}

// ForRole 返回指定角色的模型，未配置时返回默认模型。
func (ms *ModelSet) ForRole(role string) agentcore.ChatModel {
	if m, ok := ms.models[role]; ok {
		return m
	}
	return ms.Default
}

// Summary 返回模型分配摘要（供日志使用）。
func (ms *ModelSet) Summary() string {
	var parts []string
	for role, m := range ms.models {
		provider, name := m.Current()
		parts = append(parts, fmt.Sprintf("%s=%s/%s", role, provider, name))
	}
	if len(parts) == 0 {
		provider, name := ms.Default.Current()
		return fmt.Sprintf("default=%s/%s", provider, name)
	}
	provider, name := ms.Default.Current()
	return fmt.Sprintf("default=%s/%s %s", provider, name, strings.Join(parts, " "))
}

// CurrentSelection 返回角色当前生效的 provider/model。
// role 为空或 "default" 时返回默认模型。
func (ms *ModelSet) CurrentSelection(role string) (provider, model string, explicit bool) {
	if role == "" || role == "default" {
		provider, model = ms.Default.Current()
		return provider, model, true
	}
	if sw, ok := ms.models[role]; ok {
		provider, model = sw.Current()
		return provider, model, true
	}
	provider, model = ms.Default.Current()
	return provider, model, false
}

// Swap 切换默认模型或指定角色模型。
// role 为空或 "default" 时切换默认模型；其他角色切换为显式覆盖。
func (ms *ModelSet) Swap(role, provider, model string) error {
	pc, ok := ms.config.Providers[provider]
	if !ok {
		return fmt.Errorf("provider %q is not configured", provider)
	}
	next, err := createModelFromConfig(provider, model, pc, make(map[string]agentcore.ChatModel))
	if err != nil {
		return err
	}

	if role == "" || role == "default" {
		ms.Default.Swap(provider, model, next)
		return nil
	}

	if !knownRoles[role] {
		return fmt.Errorf("unknown role %q", role)
	}

	if existing, ok := ms.models[role]; ok {
		existing.Swap(provider, model, next)
		return nil
	}
	ms.models[role] = NewSwappableModel(provider, model, next)
	return nil
}

func modelName(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Name
	}
	return "unknown"
}

// NewModelSet 根据配置创建多模型集合。
// 相同 provider+model 组合复用同一个实例。
func NewModelSet(cfg Config) (*ModelSet, error) {
	cache := make(map[string]agentcore.ChatModel)

	// 创建默认模型
	defaultPC := cfg.DefaultProviderConfig()
	defaultModel, err := createModelFromConfig(cfg.Provider, cfg.ModelName, defaultPC, cache)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}

	ms := &ModelSet{
		Default: NewSwappableModel(cfg.Provider, cfg.ModelName, defaultModel),
		models:  make(map[string]*SwappableModel),
		config:  cfg,
	}

	// 创建角色覆盖模型
	for role, rc := range cfg.Roles {
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return nil, fmt.Errorf("role %s references unknown provider %q", role, rc.Provider)
		}
		m, err := createModelFromConfig(rc.Provider, rc.Model, pc, cache)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		ms.models[role] = NewSwappableModel(rc.Provider, rc.Model, m)
		slog.Info("角色模型分配", "module", "config", "role", role, "provider", rc.Provider, "model", rc.Model)
	}

	return ms, nil
}

// createModelFromConfig 创建或复用 ChatModel 实例。
func createModelFromConfig(providerKey, model string, pc ProviderConfig, cache map[string]agentcore.ChatModel) (agentcore.ChatModel, error) {
	cacheKey := providerKey + "|" + model
	if m, ok := cache[cacheKey]; ok {
		return m, nil
	}

	providerType, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, err
	}
	lcfg := litellm.ProviderConfig{APIKey: pc.APIKey}
	if pc.BaseURL != "" {
		lcfg.BaseURL = pc.BaseURL
	}

	client, err := litellm.NewWithProvider(providerType, lcfg)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w", providerKey, providerType, err)
	}

	m := llm.NewLiteLLMAdapter(model, client)
	cache[cacheKey] = m
	return m, nil
}
