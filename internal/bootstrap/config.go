package bootstrap

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// DefaultContextWindow Mô hình未在 registry 登记时的兜底Cửa sổ大小。
const DefaultContextWindow = 200000

// CompactRatio 触发Ngữ cảnh压缩的相对阈值：tokens >= window * CompactRatio 时压缩。
// 0.85 是经验值，给"下一轮 prompt + 大工具Kết quả"留 15% 头部Rỗng间，同时让大Cửa sổ
// Mô hình也能在 85% 主动压缩，避免在 1M 名义Cửa sổ下吃满才压（注意力衰退区）。
//
// 不暴露给用户Cấu hình：与已删除的 context_window 同源——多Mô hình架构下让用户调
// 数字旋钮反复横跳，不如代码内固定一个合理值。
const CompactRatio = 0.85

// MinCompactReserve 是 ReserveTokens 的下限。小Cửa sổMô hình（如 32k 本地 qwen3:8b）
// 按 0.15 比例算 reserve 仅 4800，单次 commit_chapter 工具响应就能塞 5-8k，
// 一章Chính văn 8-15k——会出现"压完立刻又超"。8000 兜底保证最坏Cảnh下还有半轮缓冲。
const MinCompactReserve = 8000

// CompactReserveTokens 按 CompactRatio 反算 ReserveTokens 并Áp dụng MinCompactReserve floor：
//
//	threshold = window - reserve = window * CompactRatio
//	reserve   = max(MinCompactReserve, window * (1 - CompactRatio))
//
// 给 agentcore.context.Engine 的 EngineConfig.ReserveTokens 用。
func CompactReserveTokens(window int) int {
	if window <= 0 {
		return 0
	}
	reserve := window - int(float64(window)*CompactRatio)
	if reserve < MinCompactReserve {
		return MinCompactReserve
	}
	return reserve
}

// ProviderConfig 定义单个 LLM Nhà cung cấp的凭证。
type ProviderConfig struct {
	Type    string   `json:"type,omitempty"`     // API 协议类型（openai/anthropic/gemini），Tuỳ chỉnhProxy时指定
	APIKey  string   `json:"api_key,omitempty"`  // API Key
	BaseURL string   `json:"base_url,omitempty"` // API Base URL
	Models  []string `json:"models,omitempty"`   // 可选Danh sách mô hình，供 TUI 切换时展示
	// ExtraBody 透传给该 provider 每次Vui lòng求的额外参数（如 temperature/top_p/min_p/
	// presence_penalty，或厂商特有键如 nvidia 开 think 的 chat_template_kwargs）。
	// OpenAI 兼容端逐字并入Vui lòng求体（即 extra_body 约定）；值由用户自负其责。
	ExtraBody map[string]any `json:"extra_body,omitempty"`
	// Extra 透传给 provider 级Cấu hình（litellm.ProviderConfig.Extra），用于 HTTP
	// headers、user_agent、anthropic_beta 等客户端/传输层选项。
	Extra map[string]any `json:"extra,omitempty"`
}

// RequiresAPIKey Quay lại该 provider Có czy không必须显式Cấu hình api_key。
// 约定：
// 1. ollama / bedrock 允许Không có key；
// 2. 显式指定 Type 的Cấu hình视为Tuỳ chỉnhProxy，允许Không có key；
// 3. Khác provider Mặc định要求 key，保持对官方托管接口的保守校验。
func (pc ProviderConfig) RequiresAPIKey(name string) bool {
	switch name {
	case "ollama", "bedrock":
		return false
	}
	return pc.Type == ""
}

// ProviderType Quay lại有效的 API 协议类型。
// 优先使用显式 Type；否则要求 provider 名本身已在 litellm 注册表中。
func (pc ProviderConfig) ProviderType(name string) (string, error) {
	if pc.Type != "" {
		return pc.Type, nil
	}
	if llm.IsProviderRegistered(name) {
		return name, nil
	}
	return "", fmt.Errorf("provider %q Thiếu type，且不在 litellm 已知 provider 列表中: %w", name, errs.ErrConfig)
}

// ModelRef 表示一个 provider/model 组合。
type ModelRef struct {
	Provider string `json:"provider"` // provider 名称（Providers map 中的 key）
	Model    string `json:"model"`    // Mô hình名（原样透传，不做任何解析）
}

// RoleConfig 定义单个角色的Mô hình覆盖。
type RoleConfig struct {
	Provider  string     `json:"provider"`            // 主 provider 名称（Providers map 中的 key）
	Model     string     `json:"model"`               // 主Mô hình名（原样透传，不做任何解析）
	Fallbacks []ModelRef `json:"fallbacks,omitempty"` // 显式备用 provider/model 列表
	// Thinking 该角色的思考强度（off/minimal/low/medium/high/xhigh/max），Rỗng=继承顶层Mặc định。
	// 由 agents.ParseThinkingLevel 校验后Áp dụng，越级值视为Rỗng。
	Thinking string `json:"thinking,omitempty"`
}

// knownRoles 支持的角色名。
var knownRoles = map[string]bool{
	"coordinator": true,
	"architect":   true,
	"writer":      true,
	"editor":      true,
}

// Config 小说Áp dụngCấu hình。
type Config struct {
	// 运行时字段（不序列化到 JSON）
	OutputDir string `json:"-"` // 输出根Thư mục

	// Mặc định LLM Cấu hình
	Provider  string `json:"provider"` // Mặc định provider（Providers map 中的 key）
	ModelName string `json:"model"`    // Mặc địnhMô hình名
	// Thinking 顶层Mặc định思考强度（off/minimal/low/medium/high/xhigh/max），Rỗng=不覆盖（沿用Mô hình/provider Mặc định）。
	// 角色未单独Cấu hình thinking 时回落到此值。
	Thinking string `json:"thinking,omitempty"`

	// Provider 凭证库
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// 角色级Mô hình覆盖
	Roles map[string]RoleConfig `json:"roles,omitempty"`

	// 创作参数
	Style string `json:"style,omitempty"`

	// ContextWindow Ngữ cảnh压缩使用的Cửa sổ大小。留Rỗng（0）时按Mô hình名自动解析：
	// registry 命中用Mô hình真实Cửa sổ，未命中兜底 DefaultContextWindow。
	// 显式Cấu hình则优先生效——用于给 registry 查不到的Tuỳ chỉnhMô hình指定真实Cửa sổ，
	// 或把大Cửa sổMô hình钉在更小的值上提前触发压缩（1M 名义Cửa sổ在 200k+ 通常已注意力衰退）。
	// 仅影响压缩阈值，不改变 LLM API 实际Vui lòng求长度；Cấu hình值由用户自负其责。
	ContextWindow int `json:"context_window,omitempty"`

	// Budget 单本书的成本预算政策；book_usd > 0 才Bật。
	Budget BudgetConfig `json:"budget,omitzero"`

	// Notify Không có人值守告警Cấu hình；缺省Bật（system 通道兜底）。
	Notify NotifyConfig `json:"notify,omitzero"`
}

// BudgetConfig 是用户对单本书钱包的政策声明。越线停机等同于用户在那一刻
// 手动 Abort——Host 只代为执行，不评估Mô hình行为（架构 §10 合宪边界）。
type BudgetConfig struct {
	BookUSD   float64 `json:"book_usd,omitempty"`   // 必填才Bật；0/缺省 = 不限
	WarnRatio float64 `json:"warn_ratio,omitempty"` // 告警水位，Mặc định 0.8
	HardStop  bool    `json:"hard_stop,omitempty"`  // true=越线立即停；Mặc định等Hiện tại子Proxy任务Kết thúc
}

// Enabled Quay lại预算政策Có czy khôngBật。
func (b BudgetConfig) Enabled() bool { return b.BookUSD > 0 }

// NotifyConfig Không có人值守告警通道Cấu hình。
type NotifyConfig struct {
	Enabled *bool    `json:"enabled,omitempty"` // 缺省 true（system 通道零Cấu hình可用）
	Command string   `json:"command,omitempty"` // 可选，Cấu hình后替代 system 通道（手机推送走这里）
	Events  []string `json:"events,omitempty"`  // 可选，过滤 kind（run_end/repeat/budget），缺省全开
}

// IsEnabled Quay lại告警Có czy khôngBật（缺省 true）。
func (n NotifyConfig) IsEnabled() bool { return n.Enabled == nil || *n.Enabled }

// ValidateBase 校验基础Cấu hình。
func (c *Config) ValidateBase() error {
	if err := validateConfigText("provider", c.Provider); err != nil {
		return err
	}
	if err := validateConfigText("model", c.ModelName); err != nil {
		return err
	}

	if c.Provider == "" {
		return fmt.Errorf("provider is required: %w", errs.ErrConfig)
	}
	if c.ModelName == "" {
		return fmt.Errorf("model is required: %w", errs.ErrConfig)
	}

	// Mặc định provider 必须有凭证
	pc, ok := c.Providers[c.Provider]
	if !ok {
		return fmt.Errorf("provider %q 未在 providers 中Cấu hình凭证；若在 ./.ainovel/config.json 里覆盖了 provider，需同时声明 providers.%s（含 api_key/base_url），不能只改顶层 provider: %w", c.Provider, c.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(c.Provider) && pc.APIKey == "" {
		return fmt.Errorf("provider %q has no api_key configured: %w", c.Provider, errs.ErrConfig)
	}
	if err := validateProviderConfigText(c.Provider, pc); err != nil {
		return err
	}
	for name, provider := range c.Providers {
		if err := validateConfigText("provider name", name); err != nil {
			return err
		}
		if err := validateProviderConfigText(name, provider); err != nil {
			return err
		}
	}

	// 校验角色覆盖
	for role, rc := range c.Roles {
		if err := validateConfigText("role name", role); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q provider", role), rc.Provider); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q model", role), rc.Model); err != nil {
			return err
		}
		if !knownRoles[role] {
			return fmt.Errorf("unknown role %q in roles config (valid: coordinator/architect/writer/editor): %w", role, errs.ErrConfig)
		}
		if rc.Provider == "" || rc.Model == "" {
			return fmt.Errorf("role %q must have both provider and model: %w", role, errs.ErrConfig)
		}
		if err := c.validateModelRef(
			fmt.Sprintf("role %q", role),
			ModelRef{Provider: rc.Provider, Model: rc.Model},
		); err != nil {
			return err
		}
		for i, fallback := range rc.Fallbacks {
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] provider", role, i), fallback.Provider); err != nil {
				return err
			}
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] model", role, i), fallback.Model); err != nil {
				return err
			}
			if err := c.validateModelRef(
				fmt.Sprintf("role %q fallback[%d]", role, i),
				fallback,
			); err != nil {
				return err
			}
		}
	}

	// 校验预算政策
	if c.Budget.BookUSD < 0 {
		return fmt.Errorf("budget.book_usd must be >= 0: %w", errs.ErrConfig)
	}
	if c.Budget.Enabled() && (c.Budget.WarnRatio <= 0 || c.Budget.WarnRatio >= 1) {
		return fmt.Errorf("budget.warn_ratio must be in (0, 1): %w", errs.ErrConfig)
	}

	// 校验告警Cấu hình
	if err := validateConfigText("notify.command", c.Notify.Command); err != nil {
		return err
	}
	for _, ev := range c.Notify.Events {
		if !knownNotifyEvents[ev] {
			return fmt.Errorf("unknown notify event %q (valid: run_end/repeat/budget): %w", ev, errs.ErrConfig)
		}
	}

	return nil
}

var knownNotifyEvents = map[string]bool{"run_end": true, "repeat": true, "budget": true}

func validateProviderConfigText(name string, pc ProviderConfig) error {
	fields := []struct {
		label string
		value string
	}{
		{label: fmt.Sprintf("provider %q type", name), value: pc.Type},
		{label: fmt.Sprintf("provider %q api_key", name), value: pc.APIKey},
		{label: fmt.Sprintf("provider %q base_url", name), value: pc.BaseURL},
	}
	for _, field := range fields {
		if err := validateConfigText(field.label, field.value); err != nil {
			return err
		}
	}
	for i, model := range pc.Models {
		if err := validateConfigText(fmt.Sprintf("provider %q models[%d]", name, i), model); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigText(name, value string) error {
	if utils.ContainsControl(value) {
		return fmt.Errorf("%s contains control character: %w", name, errs.ErrConfig)
	}
	return nil
}

// DefaultProviderConfig Quay lạiMặc định provider 的凭证Cấu hình。
func (c *Config) DefaultProviderConfig() ProviderConfig {
	if c.Providers == nil {
		return ProviderConfig{}
	}
	return c.Providers[c.Provider]
}

// FillDefaults 填充Mặc định值。
func (c *Config) FillDefaults() {
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join("output", "novel")
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleConfig)
	}
	if c.Style == "" {
		c.Style = "default"
	}
	if c.Budget.Enabled() && c.Budget.WarnRatio == 0 {
		c.Budget.WarnRatio = 0.8
	}
}

// ContextWindowSource 标记Cửa sổ取值的来源，供日志/诊断使用。
type ContextWindowSource string

const (
	CtxWindowConfig   ContextWindowSource = "config"   // Tập tin cấu hình context_window 显式指定
	CtxWindowRegistry ContextWindowSource = "registry" // OpenRouter 基线命中
	CtxWindowDefault  ContextWindowSource = "default"  // 兜底（Tuỳ chỉnhProxy/Không rõMô hình）
)

// ResolveContextWindow 解析Ngữ cảnh压缩使用的有效Cửa sổ，按优先级：
//  1. Tập tin cấu hình ContextWindow > 0 → 直接用（最高优先级，可超过Mô hình真Cửa sổ）
//  2. models.DefaultRegistry 按Mô hình名查询（OpenRouter 基线 + 24h Làm mới）
//  3. 兜底 DefaultContextWindow（Tuỳ chỉnhProxy / Không rõMô hình）
//
// 注意：Quay lại值仅用于压缩阈值计算，不会缩小 LLM API 真实可发Vui lòng求长度。
func (c Config) ResolveContextWindow(modelName string) (int, ContextWindowSource) {
	if c.ContextWindow > 0 {
		return c.ContextWindow, CtxWindowConfig
	}
	if rw := models.DefaultRegistry().ResolveContextWindow(modelName); rw > 0 {
		return rw, CtxWindowRegistry
	}
	return DefaultContextWindow, CtxWindowDefault
}

// ResolveThinking Quay lại某角色生效的思考强度原始串（off/minimal/low/medium/high/xhigh/max 或Rỗng）。
// 优先级：角色级 Roles[role].Thinking → 顶层Mặc định Thinking → ""（不覆盖，沿用Mô hình/provider Mặc định）。
// role 为Rỗng或 "default" 时直接取顶层Mặc định。值的合法性由 agents.ParseThinkingLevel 把关。
func (c Config) ResolveThinking(role string) string {
	if role != "" && role != "default" {
		if rc, ok := c.Roles[role]; ok && rc.Thinking != "" {
			return rc.Thinking
		}
	}
	return c.Thinking
}

// LogContextWindowChoice 打印某个角色的Cửa sổ决策。source=default 时发 Warn 提示
// 该Mô hình未在 registry 命中（OpenRouter 也未收录），后续Ngữ cảnh压缩会按兜底Cửa sổ
// 触发——若Mô hình实际Cửa sổ更大，可在Tập tin cấu hình用 context_window 显式指定，避免被提前压缩、丢史。
func LogContextWindowChoice(role, model string, window int, source ContextWindowSource) {
	attrs := []any{"module", "context", "role", role, "model", model, "window", window, "source", source}
	switch source {
	case CtxWindowDefault:
		slog.Warn("未识别的Mô hình，使用兜底Cửa sổ（Tuỳ chỉnhProxy或 OpenRouter 未收录，可用 context_window 显式指定）", attrs...)
	case CtxWindowConfig:
		slog.Info("Cửa sổ ngữ cảnh（来自Tập tin cấu hình context_window）", attrs...)
	default:
		slog.Info("Cửa sổ ngữ cảnh", attrs...)
	}
}

// CandidateModels Quay lại某个 provider 下可供切换的Danh sách mô hình。
// 优先使用 provider 显式声明的 models；同时补充Hiện tạiCấu hình中已出现过的该 provider Mô hình。
func (c Config) CandidateModels(provider string) []string {
	if provider == "" {
		return nil
	}

	seen := make(map[string]bool)
	models := make([]string, 0, 4)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}

	if pc, ok := c.Providers[provider]; ok {
		for _, model := range pc.Models {
			add(model)
		}
	}
	if c.Provider == provider {
		add(c.ModelName)
	}
	for _, rc := range c.Roles {
		if rc.Provider == provider {
			add(rc.Model)
		}
		for _, fallback := range rc.Fallbacks {
			if fallback.Provider == provider {
				add(fallback.Model)
			}
		}
	}
	return models
}

func (c Config) validateModelRef(owner string, ref ModelRef) error {
	if ref.Provider == "" || ref.Model == "" {
		return fmt.Errorf("%s must have both provider and model: %w", owner, errs.ErrConfig)
	}

	pc, ok := c.Providers[ref.Provider]
	if !ok {
		return fmt.Errorf("%s references provider %q which is not configured: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(ref.Provider) && pc.APIKey == "" {
		return fmt.Errorf("%s references provider %q which has no api_key: %w", owner, ref.Provider, errs.ErrConfig)
	}
	return nil
}
