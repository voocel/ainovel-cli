// Package binding 是模型绑定用例（D57）：随时切换连接 / 模型 / 思考强度，按角色
// 覆盖，落盘配置并重绑常驻 Runtime。它不触碰作品数据，也不进任务身份——
// 队列里的任务在下一次尝试开始时自然用上新绑定。
package binding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	agentllm "github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/llm"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
)

// Binder 是可重绑定的执行器；*capability.Runtime 满足。
type Binder interface {
	Bind(models.Bindings)
	Bound() bool
}

type Service struct {
	mu       sync.Mutex
	dir      string
	config   appconfig.Config
	binder   Binder
	bindings *models.Bindings // 最近一次成功绑定，供查询档位；nil = 未绑定
}

func New(dir string, binder Binder) *Service {
	return &Service{dir: dir, binder: binder}
}

// Selection 是某个角色当前生效的绑定。
type Selection struct {
	Role       string                    `json:"role,omitempty"`
	Connection string                    `json:"connection"`
	Provider   string                    `json:"provider"`
	Model      string                    `json:"model"`
	Thinking   agentcore.ThinkingLevel   `json:"thinking,omitempty"`
	Inherited  bool                      `json:"inherited,omitempty"` // 角色未覆盖，跟随默认
	Bound      bool                      `json:"bound"`
	Levels     []agentcore.ThinkingLevel `json:"levels,omitempty"` // 该模型可接受的思考档位，含自动（空）
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

// Apply 记录配置并重绑 Runtime，不落盘：启动时用，环境变量覆盖的配置不该写回文件。
// 绑定失败时保留旧绑定，只记录配置以便向导修复。
func (s *Service) Apply(config appconfig.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config
	return s.bindLocked(config)
}

// Switch 校验、绑定并落盘：任一步失败都不改变已生效的绑定与文件。
func (s *Service) Switch(config appconfig.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switchLocked(config)
}

func (s *Service) switchLocked(config appconfig.Config) error {
	bindings, err := s.build(config)
	if err != nil {
		return err
	}
	if err := appconfig.SaveConfig(s.dir, config); err != nil {
		return err
	}
	s.binder.Bind(bindings)
	s.config, s.bindings = config, &bindings
	return nil
}

func (s *Service) bindLocked(config appconfig.Config) error {
	bindings, err := s.build(config)
	if err != nil {
		return err
	}
	s.binder.Bind(bindings)
	s.bindings = &bindings
	return nil
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
		next.Thinking = string(thinking)
	case !prompt.KnownModelRole(role):
		return fmt.Errorf("未知角色 %q，可选：%s", role, strings.Join(prompt.ModelRoles, "、"))
	default:
		rc, ok := next.Roles[role]
		if !ok {
			return fmt.Errorf("角色 %q 跟随默认，先为它指定模型", role)
		}
		next = next.WithRole(role, rc.Provider, rc.Model, string(thinking))
	}
	return s.switchLocked(next)
}

// Current 返回角色当前生效的绑定；role 为空是默认。
func (s *Service) Current(role string) Selection {
	s.mu.Lock()
	defer s.mu.Unlock()
	selection := Selection{Role: role, Connection: s.config.Provider, Model: s.config.Model, Thinking: agentcore.ThinkingLevel(s.config.Thinking)}
	if rc, ok := s.config.Roles[role]; ok && role != "" {
		selection.Connection, selection.Model = rc.Provider, rc.Model
		if rc.Thinking != "" {
			selection.Thinking = agentcore.ThinkingLevel(rc.Thinking)
		}
	} else if role != "" {
		selection.Inherited = true
	}
	if pc, err := s.config.Connection(selection.Connection); err == nil {
		selection.Provider = pc.Type
	}
	if s.bindings != nil && s.binder != nil && s.binder.Bound() {
		selection.Bound = true
		selection.Levels = llm.ThinkingPolicy(s.bindings.For(role).Chat).Available
	}
	return selection
}

// Levels 返回某连接/模型可接受的思考档位；构建失败时给全部档位，由执行时折算。
func (s *Service) Levels(connection, model string) []agentcore.ThinkingLevel {
	s.mu.Lock()
	config := s.config
	s.mu.Unlock()
	modelConfig, err := modelConfigFor(config, connection, model, "")
	if err != nil {
		return llm.ThinkingPolicy(nil).Available
	}
	chat, err := models.New(modelConfig)
	if err != nil {
		return llm.ThinkingPolicy(nil).Available
	}
	return llm.ThinkingPolicy(chat).Available
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

// Verify 用最小请求验证配置可真实连通：配置向导先验证再落盘，避免把坏配置写进文件。
func Verify(ctx context.Context, config appconfig.Config) error {
	modelConfig, err := modelConfigFor(config, config.Provider, config.Model, config.Thinking)
	if err != nil {
		return err
	}
	modelConfig.Timeout = 30 * time.Second
	return models.Verify(ctx, modelConfig)
}

// build 按配置构建整套绑定：默认一套，角色覆盖各一套；同一「连接 / 模型 / 强度」复用实例。
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
		Thinking: agentcore.ThinkingLevel(thinking),
	}, nil
}

// parseThinking 接受 auto / inherit / 空与 agentcore 的全部档位。
func parseThinking(level string) (agentcore.ThinkingLevel, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "", "auto", "inherit":
		return agentllm.ThinkingAuto, nil
	}
	for _, known := range agentllm.ThinkingLevelOrder {
		if string(known) == level {
			return known, nil
		}
	}
	names := make([]string, 0, len(agentllm.ThinkingLevelOrder)+1)
	names = append(names, "auto")
	for _, known := range agentllm.ThinkingLevelOrder {
		names = append(names, string(known))
	}
	return "", fmt.Errorf("未知思考强度 %q，可选：%s", level, strings.Join(names, " / "))
}
