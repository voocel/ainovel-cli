// Package binding 是模型绑定用例（D57）：随时切换连接 / 模型 / 思考强度，按角色
// 覆盖，落盘配置并重绑常驻 Runtime。它不触碰作品数据，也不进任务身份——
// 队列里的任务在下一次尝试开始时自然用上新绑定。
package binding

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
)

// Binder 是可重绑定的执行器；*capability.Runtime 满足。
type Binder interface {
	Bind(models.Bindings)
	Bound() bool
}

type Service struct {
	mu     sync.Mutex
	dir    string
	config appconfig.Config
	binder Binder
	facts  *models.Facts
	// bound 是最近一次生效的整套绑定：Runtime 之外的模型使用者（主编）按角色从这里取。
	bound *models.Bindings
}

// New 装配绑定用例，并读本地的模型清单：绑定时据此给模型附上标价，用量随之计花费。
func New(dir string, binder Binder) *Service {
	facts := models.NewFacts(filepath.Join(dir, "models.json"))
	facts.Read()
	return &Service{dir: dir, binder: binder, facts: facts}
}

// Selection 是某个角色当前生效的绑定。
type Selection struct {
	Role       string `json:"role,omitempty"`
	Connection string `json:"connection"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Thinking   string `json:"thinking,omitempty"`
	// ContextWindow 是模型的上下文窗口（token），模型清单里查不到为 0。
	ContextWindow int  `json:"context_window,omitempty"`
	Inherited     bool `json:"inherited,omitempty"` // 角色未覆盖，跟随默认
	Bound         bool `json:"bound"`
}

// Choice 是配置里一条可切换的「连接 / 模型」。
type Choice struct {
	Connection string `json:"connection"`
	Model      string `json:"model"`
	Current    bool   `json:"current"`
}

// Config 返回当前配置（含尚未成功绑定的意图，向导预填用）。
func (s *Service) Config() appconfig.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

// Apply 记录配置并重绑 Runtime，不落盘：启动时用。绑定失败时保留旧绑定，只记录配置以便
// 向导修复。
func (s *Service) Apply(config appconfig.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config
	return s.bindLocked(config)
}

// switchLocked 校验、绑定并落盘：任一步失败都不改变已生效的绑定与文件。
func (s *Service) switchLocked(config appconfig.Config) error {
	bindings, err := s.build(config)
	if err != nil {
		return err
	}
	if err := appconfig.SaveConfig(s.dir, config); err != nil {
		return err
	}
	s.binder.Bind(bindings)
	s.config, s.bound = config, &bindings
	return nil
}

func (s *Service) bindLocked(config appconfig.Config) error {
	bindings, err := s.build(config)
	if err != nil {
		return err
	}
	s.binder.Bind(bindings)
	s.bound = &bindings
	return nil
}

// Binding 取角色此刻生效的模型，未覆盖的角色落到默认；还没有成功绑定时 ok=false。
func (s *Service) Binding(role string) (models.Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bound == nil {
		return models.Binding{}, false
	}
	return s.bound.For(role), true
}

// Use 让角色改用指定连接的模型；role 为空是默认绑定，connection 为空表示沿用当前
// 连接（默认）或清除覆盖回到跟随默认（角色）。角色的思考强度保持不变。
func (s *Service) Use(role, connection, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	role, connection, model = strings.TrimSpace(role), strings.TrimSpace(connection), strings.TrimSpace(model)
	next := s.config
	switch {
	case role == "":
		if connection == "" {
			connection = next.Provider
		}
		if model == "" {
			return fmt.Errorf("模型不能为空")
		}
		pc, err := next.Connection(connection)
		if err != nil {
			return err
		}
		next = next.WithProvider(connection, model, pc)
	case !prompt.KnownModelRole(role):
		return fmt.Errorf("未知角色 %q，可选：%s", role, strings.Join(prompt.ModelRoles, "、"))
	case connection == "":
		next = next.WithoutRole(role)
	default:
		if model == "" {
			return fmt.Errorf("模型不能为空")
		}
		if _, err := next.Connection(connection); err != nil {
			return err
		}
		next = next.WithRole(role, connection, model, next.Roles[role].Thinking)
	}
	return s.switchLocked(next)
}

// SetThinking 设置角色的思考强度意图：auto 或空是自动；角色传 inherit 或空表示继承默认。
// 意图不按当前模型能力裁剪，换回支持的模型即恢复。
func (s *Service) SetThinking(role, level string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	role = strings.TrimSpace(role)
	thinking, err := parseThinking(level)
	if err != nil {
		return err
	}
	next := s.config
	switch {
	case role == "":
		next.Thinking = thinking
	case !prompt.KnownModelRole(role):
		return fmt.Errorf("未知角色 %q，可选：%s", role, strings.Join(prompt.ModelRoles, "、"))
	default:
		rc, ok := next.Roles[role]
		if !ok {
			return fmt.Errorf("角色 %q 跟随默认，先为它指定模型", role)
		}
		next = next.WithRole(role, rc.Provider, rc.Model, thinking)
	}
	return s.switchLocked(next)
}

// SaveConnection 写入或更新连接；model 非空时连同模型设为默认绑定。
func (s *Service) SaveConnection(name string, pc appconfig.ProviderConfig, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.config.WithConnection(name, pc)
	if _, err := next.Connection(name); err != nil {
		return err
	}
	if model != "" {
		next = next.WithProvider(name, model, pc)
	}
	return s.switchLocked(next)
}

// RemoveConnection 删除连接及其已保存的模型；默认绑定或角色还在用它时拒绝。
func (s *Service) RemoveConnection(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.config.WithoutConnection(name)
	if err != nil {
		return err
	}
	return s.switchLocked(next)
}

// RemoveModel 从连接的已保存模型里删除一个；默认绑定或角色还在用它时拒绝。
func (s *Service) RemoveModel(connection, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.config.WithoutModel(connection, model)
	if err != nil {
		return err
	}
	return s.switchLocked(next)
}

// Current 返回角色当前生效的绑定；role 为空是默认。
func (s *Service) Current(role string) Selection {
	s.mu.Lock()
	defer s.mu.Unlock()
	selection := Selection{Role: role, Connection: s.config.Provider, Model: s.config.Model, Thinking: s.config.Thinking}
	if rc, ok := s.config.Roles[role]; ok && role != "" {
		selection.Connection, selection.Model = rc.Provider, rc.Model
		if rc.Thinking != "" {
			selection.Thinking = rc.Thinking
		}
	} else if role != "" {
		selection.Inherited = true
	}
	if pc, err := s.config.Connection(selection.Connection); err == nil {
		selection.Provider = pc.Type
	}
	selection.ContextWindow = s.facts.ContextWindow(selection.Provider, selection.Model)
	selection.Bound = s.binder != nil && s.binder.Bound()
	return selection
}

// RefreshFacts 本地模型清单缺失或过期时联网更新，更新后重绑：之后开始的尝试按新清单
// 计花费（D57 的换绑语义，正在执行的尝试不变）。取不到不影响创作，只是没有上下文窗口与花费。
func (s *Service) RefreshFacts(ctx context.Context) error {
	updated, err := s.facts.Refresh(ctx)
	if !updated {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binder == nil || !s.binder.Bound() {
		return err
	}
	return errors.Join(err, s.bindLocked(s.config))
}

// Levels 返回某连接/模型可选的思考档位；构建失败时给全部档位，由执行时折算。
func (s *Service) Levels(connection, model string) []string {
	s.mu.Lock()
	config := s.config
	s.mu.Unlock()
	modelConfig, err := modelConfigFor(config, connection, model, "")
	if err != nil {
		return models.ThinkingLevels
	}
	chat, err := models.New(modelConfig)
	if err != nil {
		return models.ThinkingLevels
	}
	return models.Levels(chat.Client)
}

// Roles 是可单独绑定的角色：空串代表默认。
func (s *Service) Roles() []string { return append([]string{""}, prompt.ModelRoles...) }

// Connections 返回已保存的连接名（排序）。
func (s *Service) Connections() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.config.Providers))
	for name := range s.config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Models 返回某连接已保存的模型，保持记录顺序。
func (s *Service) Models(connection string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.config.Providers[connection].Models...)
}

// Choices 列出全部「连接 / 模型」，标出默认绑定当前所用的那条。
func (s *Service) Choices() []Choice {
	var choices []Choice
	for _, connection := range s.Connections() {
		for _, model := range s.Models(connection) {
			choices = append(choices, Choice{Connection: connection, Model: model, Current: connection == s.config.Provider && model == s.config.Model})
		}
	}
	return choices
}

// ModelOption 是服务商列出的一个模型，附模型清单查得到的上下文窗口与标价（每百万 token
// 美元，查不到为 0）。
type ModelOption struct {
	ID, Name      string
	ContextWindow int
	Input, Output float64
}

// ListModels 列出连接这组凭据能用的模型，顺带验证密钥（被拒绝时是 FailureModelRefused）。
// 模型清单认识的对话模型排前，同组内新发布的在前，其余保持厂商顺序。
func (s *Service) ListModels(ctx context.Context, name string, pc appconfig.ProviderConfig) ([]ModelOption, error) {
	config, err := draftConfig(name, pc)
	if err != nil {
		return nil, err
	}
	listed, err := models.List(ctx, config)
	if err != nil {
		return nil, models.Classify(err)
	}
	type ranked struct {
		option  ModelOption
		known   bool
		created time.Time
	}
	items := make([]ranked, 0, len(listed))
	for _, info := range listed {
		option := ModelOption{ID: info.ID, Name: info.Name, ContextWindow: s.facts.ContextWindow(config.Provider, info.ID)}
		pricing := s.facts.Pricing(config.Provider, info.ID)
		if pricing != nil {
			option.Input, option.Output = pricing.Input*1e6, pricing.Output*1e6
		}
		items = append(items, ranked{option: option, known: option.ContextWindow > 0 || pricing != nil, created: info.Created})
	}
	slices.SortStableFunc(items, func(a, b ranked) int {
		if a.known != b.known {
			if a.known {
				return -1
			}
			return 1
		}
		return b.created.Compare(a.created)
	})
	options := make([]ModelOption, len(items))
	for i, item := range items {
		options[i] = item.option
	}
	return options, nil
}

// TryModel 向连接上的模型发一句极短的对话，确认手动输入的模型 ID 真能用（花很少的
// token）：ID 拼错时当场报出，不用等开写。失败按 models.Classify 归类。
func (s *Service) TryModel(ctx context.Context, name string, pc appconfig.ProviderConfig, model string) error {
	config, err := draftConfig(name, pc)
	if err != nil {
		return err
	}
	config.Model = model
	return models.Classify(models.Try(ctx, config))
}

// draftConfig 校验一个还没保存的连接，转成协议适配器配置。
func draftConfig(name string, pc appconfig.ProviderConfig) (models.Config, error) {
	pc, err := appconfig.Config{Providers: map[string]appconfig.ProviderConfig{name: pc}}.Connection(name)
	if err != nil {
		return models.Config{}, err
	}
	return models.Config{Provider: pc.Type, API: pc.API, APIKey: pc.APIKey, BaseURL: pc.BaseURL}, nil
}

// ContextWindow 是某连接上模型的上下文窗口（token），模型清单查不到为 0。
func (s *Service) ContextWindow(connection, model string) int {
	s.mu.Lock()
	pc, err := s.config.Connection(connection)
	s.mu.Unlock()
	if err != nil {
		return 0
	}
	return s.facts.ContextWindow(pc.Type, model)
}

// build 按配置构建整套绑定：默认一套，角色覆盖各一套；同一「连接 / 模型 / 强度」复用实例。
// 模型清单有标价时附上，用量按它计花费。
func (s *Service) build(config appconfig.Config) (models.Bindings, error) {
	if s.binder == nil {
		return models.Bindings{}, errors.New("model runtime is not assembled")
	}
	if err := config.Validate(); err != nil {
		return models.Bindings{}, err
	}
	if _, err := parseThinking(config.Thinking); err != nil {
		return models.Bindings{}, err
	}
	built := make(map[string]models.Binding)
	bind := func(connection, model, thinking string) (models.Binding, error) {
		key := connection + "\x00" + model + "\x00" + thinking
		if binding, ok := built[key]; ok {
			return binding, nil
		}
		modelConfig, err := modelConfigFor(config, connection, model, thinking)
		if err != nil {
			return models.Binding{}, err
		}
		binding, err := models.Bind(modelConfig)
		if err != nil {
			return models.Binding{}, err
		}
		binding.Chat.Pricing = s.facts.Pricing(modelConfig.Provider, model)
		built[key] = binding
		return binding, nil
	}
	bindings := models.Bindings{Roles: make(map[string]models.Binding, len(config.Roles))}
	var err error
	if bindings.Default, err = bind(config.Provider, config.Model, config.Thinking); err != nil {
		return models.Bindings{}, err
	}
	for role, rc := range config.Roles {
		if !prompt.KnownModelRole(role) {
			return models.Bindings{}, fmt.Errorf("未知角色 %q，可选：%s", role, strings.Join(prompt.ModelRoles, "、"))
		}
		thinking := rc.Thinking
		if thinking == "" {
			thinking = config.Thinking
		}
		if _, err := parseThinking(rc.Thinking); err != nil {
			return models.Bindings{}, fmt.Errorf("角色 %q：%w", role, err)
		}
		if bindings.Roles[role], err = bind(rc.Provider, rc.Model, thinking); err != nil {
			return models.Bindings{}, fmt.Errorf("角色 %q：%w", role, err)
		}
	}
	return bindings, nil
}

// modelConfigFor 把用户拥有的连接名解析成协议适配器配置。
func modelConfigFor(config appconfig.Config, connection, model, thinking string) (models.Config, error) {
	pc, err := config.Connection(connection)
	if err != nil {
		return models.Config{}, err
	}
	return models.Config{
		Provider: pc.Type, API: pc.API, Model: model, APIKey: pc.APIKey, BaseURL: pc.BaseURL,
		Thinking: thinking,
	}, nil
}

// parseThinking 接受 auto / inherit / 空（都是自动）与 models.ThinkingLevels 的档位。
func parseThinking(level string) (string, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "auto", "inherit":
		return "", nil
	}
	if slices.Contains(models.ThinkingLevels, level) {
		return level, nil
	}
	return "", fmt.Errorf("未知思考强度 %q，可选：auto / %s", level, strings.Join(models.ThinkingLevels[1:], " / "))
}
