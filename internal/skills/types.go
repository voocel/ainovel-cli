// Package skills 是专项技能资产层：把"一套可命名、可复用、带作用范围的专项处理方法"
// 提成一等资产，供用户干预时定向调用。
//
// 与相邻两层的分工（不可混淆）：
//   - rules（internal/rules）是**长效约束**：叠进 user_rules 快照，novel_context 每章注入、
//     commit_chapter 机械检查。语义是"以后都这么写"。
//   - skills（本包）是**一次性方法**：命中后把正文拼进本次 Worker 的 task，跑完即止，
//     不落任何持久状态。语义是"这次这么处理"。
//
// Skill 不是新执行体。它没有自己的工具集、没有自己的循环，执行仍由现有 Worker
// （editor / writer / architect）承担，因此完整复用 checkpoint、返工队列与审计链路。
package skills

import (
	"fmt"
	"regexp"
	"strings"
)

// Scope 声明 skill 的作用范围，决定 Engine 如何推导目标与 Arbiter 如何校验。
type Scope string

const (
	// ScopeChapters 作用于已完成章节（走 editor 审阅 → save_review 入队返工）。
	ScopeChapters Scope = "chapters"
	// ScopeForward 作用于后续写作，不回改已有内容。
	ScopeForward Scope = "forward"
	// ScopeFoundation 作用于设定层（前提/大纲/角色/世界观）。
	ScopeFoundation Scope = "foundation"
)

// Valid 报告 scope 是否是已知取值。
func (s Scope) Valid() bool {
	switch s {
	case ScopeChapters, ScopeForward, ScopeFoundation:
		return true
	default:
		return false
	}
}

// agentNames 是 skill 可声明的执行者白名单，必须与 arbiter.workerNames 及
// agents.BuildWorkers 注册的子代理保持一致——skill 单独出现时 Engine 直接按此派单。
var agentNames = []string{"architect_long", "architect_short", "writer", "editor"}

// validAgent 报告 agent 是否是已注册的 Worker。
func validAgent(agent string) bool {
	for _, name := range agentNames {
		if name == agent {
			return true
		}
	}
	return false
}

// nameRe 校验 skill 名，与 assets.styleNameRe 同一约定：拒绝路径字符，
// 保证名字能安全充当 schema enum 值与命令行参数。
var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Skill 是一份专项处理方法。
//
// Body 是拼进 Worker task 的指令原文；Source 只作诊断与 UI 展示，不参与任何判断。
type Skill struct {
	Name        string
	Description string
	Agent       string
	Scope       Scope
	Body        string
	Source      string // 来源标签，如 builtin:anti-ai-tone.md / global:my-polish.md
}

// validate 检查 skill 自身是否自洽。任何一项不满足都让加载方跳过该文件并告警——
// 残缺的 skill 比没有更糟：它会进 Arbiter 的可选名单，却在执行时无法组装任务。
func (s Skill) validate() error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("name %q 非法（仅小写字母、数字与连字符）", s.Name)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("description 不能为空（Arbiter 据此判断是否命中）")
	}
	if !validAgent(s.Agent) {
		return fmt.Errorf("agent %q 非法（可选 %s）", s.Agent, strings.Join(agentNames, " / "))
	}
	if !s.Scope.Valid() {
		return fmt.Errorf("scope %q 非法（可选 chapters / forward / foundation）", s.Scope)
	}
	switch s.Scope {
	case ScopeChapters:
		if s.Agent != "editor" {
			return fmt.Errorf("scope chapters 必须由 editor 执行，不能使用 %s", s.Agent)
		}
	case ScopeForward:
		if s.Agent != "writer" {
			return fmt.Errorf("scope forward 必须由 writer 执行，不能使用 %s", s.Agent)
		}
	case ScopeFoundation:
		if s.Agent != "architect_long" && s.Agent != "architect_short" {
			return fmt.Errorf("scope foundation 必须由 architect_long 或 architect_short 执行，不能使用 %s", s.Agent)
		}
	}
	if strings.TrimSpace(s.Body) == "" {
		return fmt.Errorf("正文不能为空")
	}
	return nil
}
