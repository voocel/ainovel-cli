package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/litellm"
	litellmprovider "github.com/voocel/litellm/provider"
	"github.com/voocel/litellm/provider/bedrock"
	"github.com/voocel/litellm/provider/openai"
)

// FailoverEvent 表示一次显式 provider 切换。
// Reason 为短标签（rate_limit / timeout / stream_idle / network / overloaded），
// 用于结构化日志。
type FailoverEvent struct {
	Role         string
	Reason       string
	FromProvider string
	FromModel    string
	ToProvider   string
	ToModel      string
	Err          error
}

// FailoverReporter 在发生显式切换时被调用。
type FailoverReporter func(FailoverEvent)

// Support 是能力支持度的本地三态。litellm 只声明协议事实（思考、输出上限、
// ProviderOptions），应用层判定（缓存路由键门控）由本包补齐，因此这里保留自己
// 的三态定义。
type Support int

const (
	SupportUnknown Support = iota
	SupportNo
	SupportYes
)

// ThinkingCapabilities 描述思考参数的发送能力。
type ThinkingCapabilities struct {
	Supported    Support
	Disable      Support
	Efforts      []string
	BudgetTokens Support
}

// CacheCapabilities 描述提示词缓存能力。
type CacheCapabilities struct {
	PromptKey Support
}

// Capabilities 是 bootstrap 对单个端点/模型的协议能力视图。
type Capabilities struct {
	Provider string
	Model    string

	Thinking ThinkingCapabilities
	Cache    CacheCapabilities

	// MaxTokensRequired 表示协议要求必填输出上限（Anthropic / Bedrock）。
	MaxTokensRequired bool
	// ProviderOptions 是适配器认识的 Request.ProviderOptions 键，已排序。
	ProviderOptions []string
}

// thinkingEfforts 是适配器接受 effort 字符串时的候选档位：具体支持哪几档由
// 厂商决定，越级值由厂商拒绝。
var thinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// ThinkingLevel 是推理强度档位；空值表示不覆盖（沿用 provider 默认）。
type ThinkingLevel string

const (
	ThinkingAuto    ThinkingLevel = ""
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// ParseThinkingLevel 校验并归一化配置中的推理强度。空值合法（= 不覆盖）；
// 除配置文档列出的 off/low/medium/high/xhigh/max 外也接受 minimal。
func ParseThinkingLevel(s string) (ThinkingLevel, error) {
	switch level := ThinkingLevel(strings.ToLower(strings.TrimSpace(s))); level {
	case ThinkingAuto, ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium,
		ThinkingHigh, ThinkingXHigh, ThinkingMax:
		return level, nil
	default:
		return ThinkingAuto, fmt.Errorf("无效推理强度 %q（可选：off/minimal/low/medium/high/xhigh/max）", s)
	}
}

// thinkingFor 把档位转换为请求里的思考配置；nil 表示不发送。
// 能力未知时按"发出去由厂商裁定"处理，与旧适配器一致。
func thinkingFor(level ThinkingLevel, caps ThinkingCapabilities, known bool) *litellm.Thinking {
	level = ThinkingLevel(strings.ToLower(strings.TrimSpace(string(level))))
	switch level {
	case ThinkingAuto:
		return nil
	case ThinkingOff:
		if known && caps.Disable == SupportNo {
			return nil
		}
		return &litellm.Thinking{Disabled: true}
	default:
		if known && caps.Supported == SupportNo {
			return nil
		}
		return &litellm.Thinking{Effort: string(level)}
	}
}

// ApplyThinking 返回把推理强度写入 Core.Request.Thinking 的模型副本。
// 空档位清空该字段（不覆盖 provider 默认）。
func ApplyThinking(m llmcontract.Model, level ThinkingLevel) llmcontract.Model {
	caps, known := ModelCapabilities(m)
	m.Core.Request.Thinking = thinkingFor(level, caps.Thinking, known)
	return m
}

// AvailableThinking 返回该模型可选的推理强度档位，首项恒为"不覆盖"。
func AvailableThinking(m llmcontract.Model) []ThinkingLevel {
	caps, known := ModelCapabilities(m)
	if known && caps.Thinking.Supported == SupportNo {
		return []ThinkingLevel{ThinkingAuto}
	}
	out := []ThinkingLevel{ThinkingAuto}
	if !known || caps.Thinking.Disable != SupportNo {
		out = append(out, ThinkingOff)
	}
	for _, effort := range thinkingEfforts {
		out = append(out, ThinkingLevel(effort))
	}
	return out
}

// clientFacts 按 client 指针登记端点能力。litellm.Client 不暴露其 provider，
// 而 client 全部由本包构造，因此这里维护一张自有的事实表。
var clientFacts sync.Map // *litellm.Client → Capabilities

func registerClientFacts(client *litellm.Client, caps Capabilities) {
	if client == nil {
		return
	}
	clientFacts.Store(client, caps)
}

// ModelCapabilities 返回模型的适配器能力视图；未知模型返回 false（能力未知）。
func ModelCapabilities(m llmcontract.Model) (Capabilities, bool) {
	if m.Core.Client == nil {
		return Capabilities{}, false
	}
	value, ok := clientFacts.Load(m.Core.Client)
	if !ok {
		return Capabilities{}, false
	}
	caps := value.(Capabilities)
	if provider := ModelProvider(m); provider != "" {
		caps.Provider = provider
	}
	if name := ModelName(m); name != "" {
		caps.Model = name
	}
	return caps, true
}

// WithPromptCacheKey 返回附带缓存路由键的模型副本。键是否真的发出由目标端点
// 决定：只有声明 prompt_cache_key 且开启了 prompt_cache_params（或官方端点）
// 的 OpenAI 端点才会带上它。
func WithPromptCacheKey(m llmcontract.Model, key string) llmcontract.Model {
	key = strings.TrimSpace(key)
	if key == "" {
		return m
	}
	options := cloneOptions(m.Core.Request.ProviderOptions)
	if options == nil {
		options = make(litellm.ProviderOptions, 1)
	}
	if err := options.Set(openai.ProviderOptionPromptCacheKey, key); err != nil {
		return m
	}
	m.Core.Request.ProviderOptions = options
	return m
}

// modelRef 指向某个端点上的一次具体模型请求：端点、模型名与协议要求的输出上限。
type modelRef struct {
	endpoint  *endpoint
	name      string
	maxTokens *int
}

// shape 把端点与模型的事实写进 req。
func (r modelRef) shape(req *litellm.Request) {
	if r.endpoint == nil {
		return
	}
	r.endpoint.shape(r.name, r.maxTokens, req)
}

// endpoint 是一个 provider 配置构建出的协议端点：客户端、协议类型与模型无关的
// 请求事实。同一 provider 下的多个模型共享端点。
type endpoint struct {
	// key 是 providers 配置里的键，同时作为 litellm provider 名（每个端点一个
	// 身份，reasoning 回放状态不会串到别的端点）。
	key      string
	typeName string
	client   *litellm.Client
	caps     litellm.Capabilities
	capsOK   bool

	// options 是 extra_body 基线：每次请求并入 ProviderOptions。
	options litellm.ProviderOptions
	// promptCacheKey 表示该端点接受 prompt_cache_key 路由键。
	promptCacheKey bool
}

// shape 把端点与模型的请求事实写进 req：模型名、输出上限、目标端点认识的
// ProviderOptions、被拒绝的思考参数与 tool_use id 归一化。
func (e *endpoint) shape(model string, maxTokens *int, req *litellm.Request) {
	req.Model = model
	// 上限指针只读共享：client 发请求前会自行克隆请求。
	req.MaxTokens = maxTokens
	req.ProviderOptions = e.mergeOptions(req.ProviderOptions)
	if req.Thinking != nil && e.capsOK {
		switch {
		case req.Thinking.Disabled && !e.caps.DisableThinking:
			req.Thinking = nil
		case !req.Thinking.Disabled && req.Thinking.Effort != "" && !e.caps.ThinkingEffort:
			req.Thinking = nil
		}
	}
	normalizeToolUseIDs(req.Messages)
}

// mergeOptions 保留调用方带的、目标端点认识的键（如 prompt_cache_key），
// 丢弃它不认识的键（严格校验的端点会直接拒绝），再叠加 extra_body 基线。
func (e *endpoint) mergeOptions(caller litellm.ProviderOptions) litellm.ProviderOptions {
	if len(caller) == 0 && len(e.options) == 0 {
		return nil
	}
	merged := make(litellm.ProviderOptions, len(caller)+len(e.options))
	for key, value := range caller {
		if _, owned := e.options[key]; owned {
			continue
		}
		if e.acceptsOption(key) {
			merged[key] = value
		}
	}
	for key, value := range e.options {
		merged[key] = value
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func (e *endpoint) acceptsOption(key string) bool {
	if key == openai.ProviderOptionPromptCacheKey {
		return e.promptCacheKey
	}
	return slices.Contains(e.caps.ProviderOptions, key)
}

// protocolRequiredMaxTokens 返回协议要求必填的输出上限：只有声明
// MaxTokensRequired 的适配器（Anthropic / Bedrock）需要，取注册表登记的物理上限；
// 未知模型不猜，直接报错，避免发出未经验证的上限。其余协议返回 nil（不设上限）。
func (e *endpoint) protocolRequiredMaxTokens(model string) (*int, error) {
	if !e.capsOK || !e.caps.MaxTokensRequired {
		return nil, nil
	}
	if entry, ok := modelreg.DefaultRegistry().Resolve(model); ok && entry.MaxTokens > 0 {
		limit := entry.MaxTokens
		return &limit, nil
	}
	return nil, fmt.Errorf("provider %q 的协议要求必填输出上限，但模型 %q 未在注册表中登记物理上限：请改用注册表认识的模型名: %w",
		e.key, model, errs.ErrConfig)
}

// capabilities 返回端点的事实（不含模型名）。
func (e *endpoint) capabilities() Capabilities {
	return Capabilities{
		Provider:          e.key,
		Thinking:          thinkingCapabilities(e.caps, e.capsOK),
		Cache:             CacheCapabilities{PromptKey: supportOf(e.promptCacheKey)},
		MaxTokensRequired: e.caps.MaxTokensRequired,
		ProviderOptions:   slices.Clone(e.caps.ProviderOptions),
	}
}

// modelFactory 按 provider 配置构建端点客户端，进程内按 provider 键复用。
type modelFactory struct {
	cfg       Config
	mu        sync.Mutex
	endpoints map[string]*endpoint
}

func newModelFactory(cfg Config) *modelFactory {
	return &modelFactory{cfg: cfg, endpoints: make(map[string]*endpoint)}
}

// target 返回 provider/model 的请求引用；端点按需构建并缓存。
func (f *modelFactory) target(providerKey, model string) (modelRef, error) {
	endpoint, err := f.endpoint(providerKey)
	if err != nil {
		return modelRef{}, err
	}
	limit, err := endpoint.protocolRequiredMaxTokens(model)
	if err != nil {
		return modelRef{}, err
	}
	return modelRef{endpoint: endpoint, name: model, maxTokens: limit}, nil
}

func (f *modelFactory) endpoint(providerKey string) (*endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.endpoints[providerKey]; ok {
		return e, nil
	}
	pc, ok := f.cfg.Providers[providerKey]
	if !ok {
		return nil, fmt.Errorf("provider %q 未在 providers 中配置凭证: %w", providerKey, errs.ErrConfig)
	}
	e, err := buildEndpoint(providerKey, pc)
	if err != nil {
		return nil, err
	}
	f.endpoints[providerKey] = e
	return e, nil
}

// buildEndpoint 按 provider 配置构建端点：选协议适配器、装配客户端选项。
func buildEndpoint(providerKey string, pc ProviderConfig) (*endpoint, error) {
	typeName, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, fmt.Errorf("解析 provider 类型失败: %w", err)
	}
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	settings, err := transportSettingsFrom(pc)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w: %w", providerKey, errs.ErrConfig, err)
	}
	impl, err := newProviderImpl(typeName, providerKey, pc, settings)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w: %w", providerKey, typeName, errs.ErrProvider, err)
	}
	streamIdle, err := pc.StreamIdleTimeoutValue()
	if err != nil {
		return nil, fmt.Errorf("provider %s stream_idle_timeout: %w: %w", providerKey, errs.ErrConfig, err)
	}
	client, err := litellm.New(impl, litellm.WithStreamIdleTimeout(streamIdle))
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s) client: %w: %w", providerKey, typeName, errs.ErrProvider, err)
	}
	options, err := requestOptions(pc.ExtraBody)
	if err != nil {
		return nil, fmt.Errorf("provider %s extra_body: %w: %w", providerKey, errs.ErrConfig, err)
	}
	caps, capsOK := client.Capabilities()
	endpoint := &endpoint{
		key:            providerKey,
		typeName:       typeName,
		client:         client,
		caps:           caps,
		capsOK:         capsOK,
		options:        options,
		promptCacheKey: promptCacheKeyAllowed(typeName, pc.BaseURL, caps, capsOK, settings.promptCache),
	}
	registerClientFacts(client, endpoint.capabilities())
	return endpoint, nil
}

// transportSettings 是 provider 配置里传输层与端点级的设置。
type transportSettings struct {
	headers     map[string]string
	userAgent   string
	api         string
	promptCache bool
}

// transportSettingsFrom 解析 providers.<name>.extra：headers / user_agent /
// anthropic_beta / api / prompt_cache_params。显式 api 字段优先于 extra。
func transportSettingsFrom(pc ProviderConfig) (transportSettings, error) {
	var settings transportSettings
	headers, err := stringMap(pc.Extra["headers"])
	if err != nil {
		return settings, fmt.Errorf("extra.headers: %w", err)
	}
	if beta := stringValue(pc.Extra["anthropic_beta"]); beta != "" {
		if headers == nil {
			headers = make(map[string]string, 1)
		}
		if _, ok := headers["anthropic-beta"]; !ok {
			headers["anthropic-beta"] = beta
		}
	}
	settings.headers = headers
	settings.userAgent = stringValue(pc.Extra["user_agent"])
	settings.api = strings.ToLower(strings.TrimSpace(pc.API))
	if settings.api == "" {
		settings.api = strings.ToLower(firstString(pc.Extra, "api", "api_mode"))
	}
	settings.promptCache = boolValue(pc.Extra["prompt_cache_params"])
	return settings, nil
}

// newProviderImpl 构建协议适配器。bedrock 用签名凭证而不是 api key，单独装配。
func newProviderImpl(typeName, providerKey string, pc ProviderConfig, settings transportSettings) (litellm.Provider, error) {
	if typeName == "bedrock" {
		accessKeyID := firstString(pc.Extra, "access_key_id", "aws_access_key_id")
		secretAccessKey := firstString(pc.Extra, "secret_access_key", "aws_secret_access_key")
		if accessKeyID == "" || secretAccessKey == "" {
			return nil, fmt.Errorf("bedrock: access_key_id and secret_access_key are required in provider extra")
		}
		return bedrock.New(bedrock.Config{
			Name:    providerKey,
			Region:  firstString(pc.Extra, "region", "aws_region"),
			BaseURL: pc.BaseURL,
			Credentials: bedrock.StaticCredentials(
				accessKeyID,
				secretAccessKey,
				firstString(pc.Extra, "session_token", "aws_session_token"),
			),
		})
	}
	return litellmprovider.New(typeName, litellmprovider.Config{
		Name:      providerKey,
		API:       settings.api,
		APIKey:    pc.APIKey,
		BaseURL:   pc.BaseURL,
		UserAgent: settings.userAgent,
		Headers:   settings.headers,
		// OpenAI 兼容族的 vendor 包装默认放行未列出的 ProviderOptions，
		// 与 extra_body 逐字并入请求体的既有语义一致。
		AllowUnknownProviderOptions: true,
	})
}

// promptCacheKeyAllowed 复刻旧适配器的门控：只有确认透传 prompt_cache_key 的
// OpenAI 端点才发路由键——官方端点，或 prompt_cache_params 显式开启的代理。
func promptCacheKeyAllowed(typeName, baseURL string, caps litellm.Capabilities, capsOK, optIn bool) bool {
	if !capsOK || typeName != "openai" {
		return false
	}
	if !slices.Contains(caps.ProviderOptions, openai.ProviderOptionPromptCacheKey) {
		return false
	}
	return optIn || isOfficialOpenAI(baseURL)
}

func isOfficialOpenAI(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return true
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func thinkingCapabilities(caps litellm.Capabilities, known bool) ThinkingCapabilities {
	if !known {
		return ThinkingCapabilities{Supported: SupportUnknown, Disable: SupportUnknown, BudgetTokens: SupportUnknown}
	}
	out := ThinkingCapabilities{
		Supported:    SupportNo,
		Disable:      supportOf(caps.DisableThinking),
		BudgetTokens: supportOf(caps.MaxTokensRequired), // Claude 协议用 budget_tokens 表达思考预算
	}
	if caps.ThinkingEffort {
		out.Supported = SupportYes
		out.Efforts = slices.Clone(thinkingEfforts)
	} else if caps.DisableThinking {
		out.Supported = SupportYes
	}
	return out
}

// requestOptions 把 extra_body 编码为 ProviderOptions。
func requestOptions(extra map[string]any) (litellm.ProviderOptions, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	options, err := litellm.NewProviderOptions(extra)
	if err != nil {
		return nil, err
	}
	return options, nil
}

func stringMap(value any) (map[string]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case map[string]string:
		return typed, nil
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("header %q must be string", key)
			}
			out[key] = text
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an object of strings")
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func firstString(extra map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(extra[key]); value != "" {
			return value
		}
	}
	return ""
}

func supportOf(ok bool) Support {
	if ok {
		return SupportYes
	}
	return SupportNo
}

// modelEndpoint 是 failover 使用的目标身份：provider 键、模型名、端点与配置覆盖。
type modelEndpoint struct {
	provider   string
	name       string
	target     modelRef
	jsonSchema *bool
}

// model 用端点自带 client 构造一次模型请求模板（非热切换路径）。
func (e modelEndpoint) model() llmcontract.Model {
	return llmcontract.Model{
		Core: agentcore.Model{
			Client: e.target.endpoint.client,
			Request: litellm.Request{
				Model:           e.name,
				MaxTokens:       cloneIntPtr(e.target.maxTokens),
				ProviderOptions: cloneOptions(e.target.endpoint.options),
			},
		},
		Provider:           e.provider,
		ModelName:          e.name,
		JSONSchemaOverride: cloneBoolPtr(e.jsonSchema),
	}
}

// modelState 是热切换的完整状态：目标端点 + 当前思考设置。
type modelState struct {
	modelEndpoint
	thinking *litellm.Thinking
}

// model 用稳定的 client 构造模型快照。
func (s modelState) model(client *litellm.Client) llmcontract.Model {
	m := s.modelEndpoint.model()
	m.Core.Client = client
	m.Core.Request.Thinking = cloneThinking(s.thinking)
	return m
}

// shape 把热切换状态写进请求：先落模型级思考设置，再由端点统一裁剪与归一化。
func (s modelState) shape(req *litellm.Request) {
	if s.thinking != nil {
		req.Thinking = cloneThinking(s.thinking)
	}
	s.target.shape(req)
}

// withEndpoint 返回同一思考设置下指向另一个端点/模型的状态。
func (s modelState) withEndpoint(endpoint modelEndpoint) modelState {
	return modelState{modelEndpoint: endpoint, thinking: s.thinking}
}

// facts 返回该状态的端点能力（模型名已填）。
func (s modelState) facts() Capabilities {
	caps := s.target.endpoint.capabilities()
	caps.Provider = s.provider
	caps.Model = s.name
	return caps
}

// switchingProvider 是热切换的接缝：Client 地址稳定，每次请求按当前状态重写
// 模型名、输出上限、ProviderOptions 与思考设置，已装配的 Worker 自动跟随。
type switchingProvider struct {
	mu    sync.RWMutex
	state modelState
}

func (p *switchingProvider) current() modelState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func (p *switchingProvider) swap(state modelState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
}

func (p *switchingProvider) Name() string { return p.current().provider }

func (p *switchingProvider) Capabilities() litellm.Capabilities {
	state := p.current()
	return state.target.endpoint.caps
}

func (p *switchingProvider) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	state := p.current()
	shaped := *req
	state.shape(&shaped)
	return state.target.endpoint.client.Chat(ctx, shaped)
}

func (p *switchingProvider) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	state := p.current()
	shaped := *req
	state.shape(&shaped)
	return state.target.endpoint.client.Stream(ctx, shaped)
}

// SwappableModel 是可热切换的模型持有者：Client 地址稳定，切换在下一次请求生效。
type SwappableModel struct {
	mu       sync.RWMutex
	state    modelState
	switcher *switchingProvider
	client   *litellm.Client
}

func newSwappableModel(state modelState) (*SwappableModel, error) {
	switcher := &switchingProvider{state: state}
	client, err := litellm.New(switcher)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w: %w",
			state.provider, state.target.endpoint.typeName, errs.ErrProvider, err)
	}
	registerClientFacts(client, state.facts())
	return &SwappableModel{state: state, switcher: switcher, client: client}, nil
}

// Model 返回当前模型快照。
func (m *SwappableModel) Model() llmcontract.Model {
	if m == nil {
		return llmcontract.Model{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.model(m.client)
}

// ProviderName 返回当前 provider 配置键。
func (m *SwappableModel) ProviderName() string {
	provider, _ := m.Current()
	return provider
}

// Current 返回当前生效的 provider 配置键与模型名。
func (m *SwappableModel) Current() (provider, name string) {
	if m == nil {
		return "", ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.provider, m.state.name
}

// JSONSchemaOverride 返回当前选中模型的 config json_schema 三态声明。
func (m *SwappableModel) JSONSchemaOverride() *bool {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneBoolPtr(m.state.jsonSchema)
}

// swapState 切换到新的端点/模型。思考设置是运行时意图，随切换保留。
func (m *SwappableModel) swapState(state modelState) {
	if m == nil {
		return
	}
	m.mu.RLock()
	thinking := m.state.thinking
	m.mu.RUnlock()
	state.thinking = thinking
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
	m.switcher.swap(state)
	registerClientFacts(m.client, state.facts())
}

// setThinking 更新模型级思考设置；nil 表示不覆盖 provider 默认。
func (m *SwappableModel) setThinking(thinking *litellm.Thinking) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.state.thinking = cloneThinking(thinking)
	state := m.state
	m.mu.Unlock()
	m.switcher.swap(state)
}

// stateCopy 返回当前状态副本。
func (m *SwappableModel) stateCopy() modelState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// ModelSet 持有按角色分配的模型实例，未配置的角色回退到默认模型。
type ModelSet struct {
	mu        sync.RWMutex
	Default   *SwappableModel
	models    map[string]*SwappableModel
	fallbacks map[string][]modelEndpoint
	reporters map[string]FailoverReporter
	failover  map[string]*litellm.Client
	config    Config
	factory   *modelFactory
}

// ForRole 返回指定角色的模型，未配置时返回默认模型。
func (ms *ModelSet) ForRole(role string) llmcontract.Model {
	model := ms.swappable(role)
	if model == nil {
		return llmcontract.Model{}
	}
	return model.Model()
}

// swappable 返回角色持有者；未配置的角色回退默认模型。
func (ms *ModelSet) swappable(role string) *SwappableModel {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if m, ok := ms.models[role]; ok {
		return m
	}
	return ms.Default
}

// ForRoleWithFailover 返回带有单次请求级 fallback 的角色模型。
// 仅当该角色显式配置了 fallbacks 时生效；未配置时退化为普通模型。
func (ms *ModelSet) ForRoleWithFailover(role string, report FailoverReporter) llmcontract.Model {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	primary, ok := ms.models[role]
	if !ok {
		if ms.Default == nil {
			return llmcontract.Model{}
		}
		return ms.Default.Model()
	}
	if len(ms.fallbacks[role]) == 0 {
		return primary.Model()
	}
	if report != nil {
		ms.reporters[role] = report
	}
	client, cached := ms.failover[role]
	if !cached {
		built, err := litellm.New(&failoverProvider{primary: primary, set: ms, role: role})
		if err != nil {
			slog.Warn("failover 模型装配失败，退化为单端点", "module", "config", "role", role, "err", err)
			return primary.Model()
		}
		client = built
		ms.failover[role] = client
	}
	model := primary.Model()
	model.Core.Client = client
	registerClientFacts(client, primary.stateCopy().facts())
	return model
}

// Summary 返回模型分配摘要（供日志使用）。
func (ms *ModelSet) Summary() string {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var parts []string
	for role, m := range ms.models {
		provider, name := m.Current()
		parts = append(parts, fmt.Sprintf("%s=%s/%s", role, provider, name))
	}
	provider, name := ms.Default.Current()
	if len(parts) == 0 {
		return fmt.Sprintf("default=%s/%s", provider, name)
	}
	return fmt.Sprintf("default=%s/%s %s", provider, name, strings.Join(parts, " "))
}

// CurrentSelection 返回角色当前生效的 provider/model。
// role 为空或 "default" 时返回默认模型。
func (ms *ModelSet) CurrentSelection(role string) (provider, model string, explicit bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
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
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if _, ok := ms.config.Providers[provider]; !ok {
		return fmt.Errorf("provider %q is not configured: %w", provider, errs.ErrConfig)
	}
	state, err := buildState(ms.config, ms.factory, provider, model)
	if err != nil {
		return fmt.Errorf("切换模型失败: %w", err)
	}

	if role == "" || role == "default" {
		ms.Default.swapState(state)
		ms.config.Provider = provider
		ms.config.ModelName = model
		return nil
	}

	if !knownRoles[role] {
		return fmt.Errorf("unknown role %q: %w", role, errs.ErrConfig)
	}
	if existing, ok := ms.models[role]; ok {
		existing.swapState(state)
	} else {
		created, err := newSwappableModel(state)
		if err != nil {
			return fmt.Errorf("切换模型失败: %w", err)
		}
		ms.models[role] = created
	}
	if ms.config.Roles == nil {
		ms.config.Roles = make(map[string]RoleConfig)
	}
	rc := ms.config.Roles[role]
	rc.Provider = provider
	rc.Model = model
	ms.config.Roles[role] = rc
	return nil
}

// SetThinking 更新角色模型的思考设置；空档位表示不覆盖 provider 默认。
// 已装配的 Worker 在下一次请求即用新设置。
func (ms *ModelSet) SetThinking(role string, level ThinkingLevel) {
	model := ms.swappable(role)
	if model == nil {
		return
	}
	state := model.stateCopy()
	caps := state.facts()
	model.setThinking(thinkingFor(level, caps.Thinking, true))
}

// ResolveContextWindow 使用 ModelSet 的最新配置解析窗口，供运行时热切换后的
// ContextManagerFactory 使用，避免捕获启动时的 Config 副本。
func (ms *ModelSet) ResolveContextWindow(provider, model string) (int, ContextWindowSource) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.config.ResolveContextWindow(provider, model)
}

// ApplyPrepared 提交一个已成功构建的候选 ModelSet。已有 SwappableModel 的地址
// 保持不变，因此已装配的 Worker/Arbiter 会在下一次请求自动使用新端点。
func (ms *ModelSet) ApplyPrepared(candidate *ModelSet) {
	if candidate == nil {
		return
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.Default != nil && candidate.Default != nil {
		ms.Default.swapState(candidate.Default.stateCopy())
	}

	next := make(map[string]*SwappableModel, len(candidate.models))
	for role, incoming := range candidate.models {
		if existing, ok := ms.models[role]; ok {
			existing.swapState(incoming.stateCopy())
			next[role] = existing
		} else {
			next[role] = incoming
		}
	}
	ms.models = next
	ms.fallbacks = candidate.fallbacks
	ms.config = CloneConfig(candidate.config)
	ms.factory = candidate.factory
	// 旧的 failover client 包着旧端点，直接重建。
	ms.failover = make(map[string]*litellm.Client)
}

// fallbackTargets 返回角色的备用端点副本。
func (ms *ModelSet) fallbackTargets(role string) []modelEndpoint {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return append([]modelEndpoint(nil), ms.fallbacks[role]...)
}

// failoverReporter 返回角色的切换回调。
func (ms *ModelSet) failoverReporter(role string) FailoverReporter {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.reporters[role]
}

// ModelName 从模型中提取当前模型名，失败返回空字符串。
// 支持热切换：调用时总是返回最新值。
func ModelName(m llmcontract.Model) string {
	if m.ModelName != "" {
		return m.ModelName
	}
	return m.Core.Request.Model
}

// ModelProvider 从模型中提取当前 provider 配置键，失败返回空字符串。
func ModelProvider(m llmcontract.Model) string {
	if m.Provider != "" {
		return m.Provider
	}
	if m.Core.Client != nil {
		return m.Core.Client.ProviderName()
	}
	return ""
}

// Chat 用模型自带的请求模板发一次调用：Messages 由调用方提供，模板里的模型名、
// 输出上限、思考与 ProviderOptions 保持不变（连接自检、一次性调用用）。
func Chat(ctx context.Context, m llmcontract.Model, messages []litellm.Message) (*litellm.Response, error) {
	if m.Core.Client == nil {
		return nil, fmt.Errorf("model has no client: %w", errs.ErrProvider)
	}
	req := m.Core.Request
	req.Messages = messages
	return m.Core.Client.Chat(ctx, req)
}

// TrackUsage 返回在每次 Chat 响应后回调 record 的模型副本：用量必须进入预算与
// usage 统计，否则预算对开销失明。Stream 路径透传（由 Run 的事件链路记账）。
func TrackUsage(m llmcontract.Model, record func(*litellm.Response)) llmcontract.Model {
	if record == nil || m.Core.Client == nil {
		return m
	}
	client, err := litellm.New(&usageProvider{inner: m.Core.Client, record: record})
	if err != nil {
		return m
	}
	registerClientFacts(client, modelFacts(m))
	m.Core.Client = client
	return m
}

// modelFacts 返回模型已知的能力事实（未知时为零值事实）。
func modelFacts(m llmcontract.Model) Capabilities {
	caps, _ := ModelCapabilities(m)
	caps.Provider = ModelProvider(m)
	caps.Model = ModelName(m)
	return caps
}

// usageProvider 把一次调用交给内层 client 并在 Chat 响应后回调。
type usageProvider struct {
	inner  *litellm.Client
	record func(*litellm.Response)
}

func (p *usageProvider) Name() string { return p.inner.ProviderName() }

func (p *usageProvider) Capabilities() litellm.Capabilities {
	caps, _ := p.inner.Capabilities()
	return caps
}

func (p *usageProvider) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	resp, err := p.inner.Chat(ctx, *req)
	if resp != nil {
		p.record(resp)
	}
	return resp, err
}

func (p *usageProvider) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	return p.inner.Stream(ctx, *req)
}

// NewModelSet 根据配置创建多模型集合。
// 相同 provider 的模型复用同一个端点客户端。
func NewModelSet(cfg Config) (*ModelSet, error) {
	factory := newModelFactory(cfg)

	defaultState, err := buildState(cfg, factory, cfg.Provider, cfg.ModelName)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}
	defaultModel, err := newSwappableModel(defaultState)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}

	ms := &ModelSet{
		Default:   defaultModel,
		models:    make(map[string]*SwappableModel),
		fallbacks: make(map[string][]modelEndpoint),
		reporters: make(map[string]FailoverReporter),
		failover:  make(map[string]*litellm.Client),
		config:    cfg,
		factory:   factory,
	}

	// 创建角色覆盖模型
	for role, rc := range cfg.Roles {
		if _, ok := cfg.Providers[rc.Provider]; !ok {
			return nil, fmt.Errorf("role %s references unknown provider %q: %w", role, rc.Provider, errs.ErrConfig)
		}
		state, err := buildState(cfg, factory, rc.Provider, rc.Model)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		model, err := newSwappableModel(state)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		ms.models[role] = model
		slog.Info("角色模型分配", "module", "config", "role", role, "provider", rc.Provider, "model", rc.Model)
		if len(rc.Fallbacks) == 0 {
			continue
		}

		targets := make([]modelEndpoint, 0, len(rc.Fallbacks))
		for _, fallback := range rc.Fallbacks {
			if _, ok := cfg.Providers[fallback.Provider]; !ok {
				return nil, fmt.Errorf("role %s fallback references unknown provider %q: %w", role, fallback.Provider, errs.ErrConfig)
			}
			target, err := buildEndpointTarget(cfg, factory, fallback.Provider, fallback.Model)
			if err != nil {
				return nil, fmt.Errorf("role %s fallback %s/%s: %w", role, fallback.Provider, fallback.Model, err)
			}
			targets = append(targets, target)
		}
		ms.fallbacks[role] = targets
	}

	return ms, nil
}

// buildState 构建一个热切换状态（端点 + 模型 + 配置覆盖）。
func buildState(cfg Config, factory *modelFactory, providerKey, model string) (modelState, error) {
	target, err := buildEndpointTarget(cfg, factory, providerKey, model)
	if err != nil {
		return modelState{}, err
	}
	return modelState{modelEndpoint: target}, nil
}

// buildEndpointTarget 构建一次具体模型请求的目标。
func buildEndpointTarget(cfg Config, factory *modelFactory, providerKey, model string) (modelEndpoint, error) {
	ref, err := factory.target(providerKey, model)
	if err != nil {
		return modelEndpoint{}, err
	}
	return modelEndpoint{
		provider:   providerKey,
		name:       model,
		target:     ref,
		jsonSchema: cfg.ModelJSONSchema(providerKey, model),
	}, nil
}

// failoverProvider 是请求级 fallback 的接缝：主端点出错时按角色配置切到备用
// 端点重试一次，并把切换事件报给 FailoverReporter。
type failoverProvider struct {
	primary *SwappableModel
	set     *ModelSet
	role    string
}

func (p *failoverProvider) Name() string { return p.primary.ProviderName() }

func (p *failoverProvider) Capabilities() litellm.Capabilities {
	return p.primary.stateCopy().target.endpoint.caps
}

func (p *failoverProvider) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	state := p.primary.stateCopy()
	requireJSONSchema := requiresJSONSchema(*req)
	resp, err := p.call(ctx, state, *req)
	if err == nil {
		return resp, nil
	}
	next, reason, ok := p.pickFallback(state.modelEndpoint, err, requireJSONSchema)
	if !ok {
		return nil, err
	}
	p.report(state.modelEndpoint, next, reason, err)
	return p.call(ctx, state.withEndpoint(next), *req)
}

func (p *failoverProvider) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	state := p.primary.stateCopy()
	stream, err := p.open(ctx, state, *req)
	if err == nil {
		return &failoverStream{
			provider: p,
			ctx:      ctx,
			request:  *req,
			current:  stream,
			from:     state.modelEndpoint,
			thinking: state.thinking,
		}, nil
	}
	next, reason, ok := p.pickFallback(state.modelEndpoint, err, requiresJSONSchema(*req))
	if !ok {
		return nil, err
	}
	p.report(state.modelEndpoint, next, reason, err)
	return p.open(ctx, state.withEndpoint(next), *req)
}

// call 用目标端点发一次非流式请求。
func (p *failoverProvider) call(ctx context.Context, state modelState, req litellm.Request) (*litellm.Response, error) {
	state.shape(&req)
	return state.target.endpoint.client.Chat(ctx, req)
}

// open 用目标端点开一条流。
func (p *failoverProvider) open(ctx context.Context, state modelState, req litellm.Request) (litellm.Stream, error) {
	state.shape(&req)
	return state.target.endpoint.client.Stream(ctx, req)
}

// pickFallback 选第一个可用的备用端点；只切一次，不级联。
func (p *failoverProvider) pickFallback(from modelEndpoint, err error, requireJSONSchema bool) (modelEndpoint, string, bool) {
	reason := failoverReason(err)
	if reason == "" {
		return modelEndpoint{}, "", false
	}
	for _, target := range p.set.fallbackTargets(p.role) {
		if target.provider == from.provider && target.name == from.name {
			continue
		}
		if target.target.endpoint == nil {
			continue
		}
		if requireJSONSchema && !supportsJSONSchema(target) {
			continue
		}
		return target, reason, true
	}
	return modelEndpoint{}, reason, false
}

func (p *failoverProvider) report(from, to modelEndpoint, reason string, err error) {
	report := p.set.failoverReporter(p.role)
	if report == nil {
		return
	}
	report(FailoverEvent{
		Role:         p.role,
		Reason:       reason,
		FromProvider: from.provider,
		FromModel:    from.name,
		ToProvider:   to.provider,
		ToModel:      to.name,
		Err:          err,
	})
}

// failoverStream 在尚未交付任何事件前出错时切换到备用端点，只切一次。
type failoverStream struct {
	provider  *failoverProvider
	ctx       context.Context
	request   litellm.Request
	current   litellm.Stream
	from      modelEndpoint
	thinking  *litellm.Thinking
	used      bool
	delivered bool
}

func (s *failoverStream) Next() (litellm.Event, error) {
	for {
		event, err := s.current.Next()
		if err == nil {
			s.delivered = true
			return event, nil
		}
		if s.used || s.delivered || errors.Is(err, io.EOF) {
			return nil, err
		}
		next, reason, ok := s.provider.pickFallback(s.from, err, requiresJSONSchema(s.request))
		if !ok {
			return nil, err
		}
		s.used = true
		s.provider.report(s.from, next, reason, err)
		// 失败流已作废；关闭错误不覆盖原始错误。
		_ = s.current.Close()
		state := modelState{modelEndpoint: next, thinking: s.thinking}
		stream, openErr := s.provider.open(s.ctx, state, s.request)
		if openErr != nil {
			return nil, openErr
		}
		s.current, s.from = stream, next
	}
}

func (s *failoverStream) Close() error {
	return s.current.Close()
}

// requiresJSONSchema 报告请求是否要求原生 JSON Schema。
func requiresJSONSchema(req litellm.Request) bool {
	return req.ResponseFormat != nil && req.ResponseFormat.Type == litellm.ResponseFormatJSONSchema
}

// supportsJSONSchema 报告备用端点能否接受原生 JSON Schema：config 三态显式声明
// false 时不切；未声明时交给目标 adapter 自己决定发原生字段还是提示词回退。
func supportsJSONSchema(target modelEndpoint) bool {
	if target.jsonSchema != nil {
		return *target.jsonSchema
	}
	return target.target.endpoint != nil
}

// failoverReason 返回错误的短标签；空串表示不值得切换 provider。
// 只对瞬时性错误切换：限流、超时、网络（含流式空闲）、过载。
func failoverReason(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	switch litellm.ErrorTypeOf(err) {
	case litellm.ErrorTypeRateLimit:
		return "rate_limit"
	case litellm.ErrorTypeTimeout:
		return "timeout"
	case litellm.ErrorTypeOverloaded:
		return "overloaded"
	case litellm.ErrorTypeNetwork:
		var litellmErr *litellm.Error
		if errors.As(err, &litellmErr) && strings.HasPrefix(strings.TrimSpace(litellmErr.Message), "no data for") {
			return "stream_idle"
		}
		return "network"
	}
	return ""
}

// maxToolUseIDLength 与 Claude 协议的 id 长度上限一致。
const maxToolUseIDLength = 64

// normalizeToolUseIDs 把 tool_use / tool_result 的 id 归一化为协议安全形态。
// 部分上游的 tool_use id 带 '#'、':' 等字符，而请求校验只接受 [A-Za-z0-9_-]；
// 归一化后 tool_result 按同一映射成对改写，否则整轮会话在本地校验阶段反复失败。
func normalizeToolUseIDs(messages []litellm.Message) {
	var ids map[string]string
	for i, message := range messages {
		for j, block := range message.Blocks {
			switch typed := block.(type) {
			case litellm.ToolUseBlock:
				normalized := normalizeToolUseID(typed.ID)
				if normalized == typed.ID {
					continue
				}
				if ids == nil {
					ids = make(map[string]string, 2)
				}
				ids[typed.ID] = normalized
				typed.ID = normalized
				message.Blocks[j] = typed
			case litellm.ToolResultBlock:
				normalized, ok := ids[typed.ToolUseID]
				if !ok {
					normalized = normalizeToolUseID(typed.ToolUseID)
				}
				if normalized == typed.ToolUseID {
					continue
				}
				typed.ToolUseID = normalized
				message.Blocks[j] = typed
			}
		}
		messages[i] = message
	}
}

// normalizeToolUseID 返回协议安全形态的 id：合规 id 原样返回，其余确定性改写，
// 并把原值的哈希拼在尾部，保证不同 id 不会合并。
func normalizeToolUseID(id string) string {
	if len(id) <= maxToolUseIDLength && strings.IndexFunc(id, invalidToolUseIDRune) < 0 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:4])
	safe := strings.Map(func(r rune) rune {
		if invalidToolUseIDRune(r) {
			return '_'
		}
		return r
	}, id)
	return safe[:min(len(safe), maxToolUseIDLength-len(suffix))] + suffix
}

func invalidToolUseIDRune(r rune) bool {
	return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_' || r == '-')
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneThinking(thinking *litellm.Thinking) *litellm.Thinking {
	if thinking == nil {
		return nil
	}
	cloned := *thinking
	cloned.BudgetTokens = cloneIntPtr(thinking.BudgetTokens)
	return &cloned
}

func cloneOptions(options litellm.ProviderOptions) litellm.ProviderOptions {
	if options == nil {
		return nil
	}
	cloned := make(litellm.ProviderOptions, len(options))
	for key, value := range options {
		cloned[key] = slices.Clone(value)
	}
	return cloned
}
