package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/apperr"
	"github.com/voocel/litellm"
)

var failoverEligibleCodes = map[apperr.Code]bool{
	apperr.CodeProviderRateLimit: true,
	apperr.CodeProviderTimeout:   true,
	apperr.CodeProviderNetwork:   true,
}

// FailoverEvent 表示一次显式 provider 切换。
type FailoverEvent struct {
	Role         string
	Code         apperr.Code
	FromProvider string
	FromModel    string
	ToProvider   string
	ToModel      string
	Err          error
}

// FailoverReporter 在发生显式切换时被调用。
type FailoverReporter func(FailoverEvent)

type modelTarget struct {
	provider string
	name     string
	model    agentcore.ChatModel
}

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
	Default   *SwappableModel
	models    map[string]*SwappableModel
	fallbacks map[string][]modelTarget
	config    Config
}

// ForRole 返回指定角色的模型，未配置时返回默认模型。
func (ms *ModelSet) ForRole(role string) agentcore.ChatModel {
	if m, ok := ms.models[role]; ok {
		return m
	}
	return ms.Default
}

// ForRoleWithFailover 返回带有单次请求级 fallback 的角色模型。
// 仅当该角色显式配置了 fallbacks 时生效；未配置时退化为普通模型。
func (ms *ModelSet) ForRoleWithFailover(role string, report FailoverReporter) agentcore.ChatModel {
	primary, ok := ms.models[role]
	if !ok {
		return ms.Default
	}
	targets := ms.fallbacks[role]
	if len(targets) == 0 {
		return primary
	}
	return &failoverModel{
		role:      role,
		primary:   primary,
		fallbacks: append([]modelTarget(nil), targets...),
		report:    report,
	}
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
		return apperr.New(
			apperr.CodeConfigInvalid,
			"bootstrap.model_set.swap",
			fmt.Sprintf("provider %q is not configured", provider),
		)
	}
	next, err := createModelFromConfig(provider, model, pc, make(map[string]agentcore.ChatModel))
	if err != nil {
		return apperr.Wrap(err, apperr.CodeProviderInitFailed, "bootstrap.model_set.swap", "切换模型失败")
	}

	if role == "" || role == "default" {
		ms.Default.Swap(provider, model, next)
		return nil
	}

	if !knownRoles[role] {
		return apperr.New(
			apperr.CodeConfigInvalid,
			"bootstrap.model_set.swap",
			fmt.Sprintf("unknown role %q", role),
		)
	}

	if existing, ok := ms.models[role]; ok {
		existing.Swap(provider, model, next)
		return nil
	}
	ms.models[role] = NewSwappableModel(provider, model, next)
	return nil
}

// ModelName 从 ChatModel 中提取当前模型名，失败返回空字符串。
// 支持 SwappableModel 的热切换：调用时总是返回最新值。
func ModelName(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Name
	}
	return ""
}

// NewModelSet 根据配置创建多模型集合。
// 相同 provider+model 组合复用同一个实例。
func NewModelSet(cfg Config) (*ModelSet, error) {
	cache := make(map[string]agentcore.ChatModel)

	// 创建默认模型
	defaultPC := cfg.DefaultProviderConfig()
	defaultModel, err := createModelFromConfig(cfg.Provider, cfg.ModelName, defaultPC, cache)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeProviderInitFailed, "bootstrap.new_model_set", "default model")
	}

	ms := &ModelSet{
		Default:   NewSwappableModel(cfg.Provider, cfg.ModelName, defaultModel),
		models:    make(map[string]*SwappableModel),
		fallbacks: make(map[string][]modelTarget),
		config:    cfg,
	}

	// 创建角色覆盖模型
	for role, rc := range cfg.Roles {
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return nil, apperr.New(
				apperr.CodeConfigInvalid,
				"bootstrap.new_model_set",
				fmt.Sprintf("role %s references unknown provider %q", role, rc.Provider),
			)
		}
		m, err := createModelFromConfig(rc.Provider, rc.Model, pc, cache)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeProviderInitFailed,
				"bootstrap.new_model_set",
				fmt.Sprintf("role %s model", role),
			)
		}
		ms.models[role] = NewSwappableModel(rc.Provider, rc.Model, m)
		slog.Info("角色模型分配", "module", "config", "role", role, "provider", rc.Provider, "model", rc.Model)
		if len(rc.Fallbacks) == 0 {
			continue
		}

		targets := make([]modelTarget, 0, len(rc.Fallbacks))
		for _, fallback := range rc.Fallbacks {
			fpc, ok := cfg.Providers[fallback.Provider]
			if !ok {
				return nil, apperr.New(
					apperr.CodeConfigInvalid,
					"bootstrap.new_model_set",
					fmt.Sprintf("role %s fallback references unknown provider %q", role, fallback.Provider),
				)
			}
			fm, err := createModelFromConfig(fallback.Provider, fallback.Model, fpc, cache)
			if err != nil {
				return nil, apperr.Wrap(
					err,
					apperr.CodeProviderInitFailed,
					"bootstrap.new_model_set",
					fmt.Sprintf("role %s fallback %s/%s", role, fallback.Provider, fallback.Model),
				)
			}
			targets = append(targets, modelTarget{
				provider: fallback.Provider,
				name:     fallback.Model,
				model:    fm,
			})
		}
		ms.fallbacks[role] = targets
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
		return nil, apperr.Wrap(err, apperr.CodeProviderInvalid, "bootstrap.create_model", "解析 provider 类型失败")
	}
	lcfg := litellm.ProviderConfig{APIKey: pc.APIKey}
	if pc.BaseURL != "" {
		lcfg.BaseURL = pc.BaseURL
	}

	client, err := litellm.NewWithProvider(providerType, lcfg)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeProviderInitFailed,
			"bootstrap.create_model",
			fmt.Sprintf("provider %s (%s)", providerKey, providerType),
		)
	}

	m := llm.NewLiteLLMAdapter(model, client)
	cache[cacheKey] = m
	return m, nil
}

type failoverModel struct {
	role      string
	primary   *SwappableModel
	fallbacks []modelTarget
	report    FailoverReporter
}

func (m *failoverModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	current := m.currentTarget()
	resp, err := current.model.Generate(ctx, messages, tools, opts...)
	if err == nil {
		return resp, nil
	}

	next, code, ok := m.pickFallback(current, err)
	if !ok {
		return nil, err
	}
	m.reportFailover(current, next, code, err)
	return next.model.Generate(ctx, messages, tools, opts...)
}

func (m *failoverModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	out := make(chan agentcore.StreamEvent, 100)

	go func() {
		defer close(out)

		current := m.currentTarget()
		fallbackUsed := false

	retry:
		source, resp, err := m.startAttempt(ctx, current, messages, tools, opts...)
		if err != nil {
			if !fallbackUsed {
				if next, code, ok := m.pickFallback(current, err); ok {
					fallbackUsed = true
					m.reportFailover(current, next, code, err)
					current = next
					goto retry
				}
			}
			out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}
		if resp != nil {
			out <- agentcore.StreamEvent{
				Type:       agentcore.StreamEventDone,
				Message:    resp.Message,
				StopReason: resp.Message.StopReason,
			}
			return
		}

		forwarded := false
		for ev := range source {
			switch ev.Type {
			case agentcore.StreamEventError:
				if ev.Err != nil && !forwarded && !fallbackUsed {
					if next, code, ok := m.pickFallback(current, ev.Err); ok {
						fallbackUsed = true
						m.reportFailover(current, next, code, ev.Err)
						current = next
						goto retry
					}
				}
				out <- ev
				return
			case agentcore.StreamEventDone:
				out <- ev
				return
			default:
				forwarded = true
				out <- ev
			}
		}
	}()

	return out, nil
}

func (m *failoverModel) SupportsTools() bool {
	return m.primary != nil && m.primary.SupportsTools()
}

func (m *failoverModel) ProviderName() string {
	if m.primary == nil {
		return ""
	}
	return m.primary.ProviderName()
}

func (m *failoverModel) Info() llm.ModelInfo {
	if m.primary == nil {
		return llm.ModelInfo{}
	}
	return m.primary.Info()
}

func (m *failoverModel) currentTarget() modelTarget {
	if m.primary == nil {
		return modelTarget{}
	}
	provider, name := m.primary.Current()
	return modelTarget{
		provider: provider,
		name:     name,
		model:    m.primary,
	}
}

func (m *failoverModel) pickFallback(current modelTarget, err error) (modelTarget, apperr.Code, bool) {
	if err == nil || current.model == nil {
		return modelTarget{}, apperr.CodeUnknown, false
	}
	if errors.Is(err, context.Canceled) {
		return modelTarget{}, apperr.CodeUnknown, false
	}

	classified := apperr.ClassifyProviderError(err, "bootstrap.failover")
	code := apperr.CodeOf(classified)
	if !failoverEligibleCodes[code] {
		return modelTarget{}, code, false
	}
	for _, target := range m.fallbacks {
		if target.provider == current.provider && target.name == current.name {
			continue
		}
		if target.model == nil {
			continue
		}
		return target, code, true
	}
	return modelTarget{}, code, false
}

func (m *failoverModel) reportFailover(from, to modelTarget, code apperr.Code, err error) {
	if m.report != nil {
		m.report(FailoverEvent{
			Role:         m.role,
			Code:         code,
			FromProvider: from.provider,
			FromModel:    from.name,
			ToProvider:   to.provider,
			ToModel:      to.name,
			Err:          err,
		})
	}
}

func (m *failoverModel) startAttempt(ctx context.Context, target modelTarget, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, *agentcore.LLMResponse, error) {
	if target.model == nil {
		return nil, nil, fmt.Errorf("no model configured")
	}

	streamCh, err := target.model.GenerateStream(ctx, messages, tools, opts...)
	if err == nil {
		return streamCh, nil, nil
	}

	resp, genErr := target.model.Generate(ctx, messages, tools, opts...)
	if genErr != nil {
		return nil, nil, genErr
	}
	return nil, resp, nil
}
