package main

import (
	"fmt"
	"path/filepath"

	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/skills"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── 专项技能中心 ──
//
// 技能是一份「可命名、可复用、带作用范围的专项处理方法」（去 AI 味、收紧节奏……），
// 由 internal/skills 三层加载（内置 < ~/.ainovel/skills < 本书 .ainovel/skills）。
//
// 桌面端只做两件事：列清单、发起执行。执行本身仍走 host 的用户干预通道，
// 与 ReviseFoundation 同一手法——绕过 Arbiter 就同时绕过阶段校验、hold 判断与裁定审计。

// SkillItem 是技能在前端的投影。Body 一并给出，让用户在执行前能看清这份技能到底会让
// Worker 做什么——技能是提示词资产，不透明就没法判断该不该用。
type SkillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Agent       string `json:"agent"`
	Scope       string `json:"scope"`
	Source      string `json:"source"`
	Body        string `json:"body"`
}

// SkillCatalog 是技能面板需要的完整状态：清单 + 存放目录 + 被跳过的文件。
//
// Dir 与 Problems 一起给出，面板才能回答"我放的技能为什么没出现"——
// 只给清单的话，用户既不知道该放哪，也不知道写坏了。
type SkillCatalog struct {
	Skills   []SkillItem    `json:"skills"`
	Dir      string         `json:"dir"`
	Problems []SkillProblem `json:"problems"`
}

// SkillProblem 是一个因格式不合法而被跳过的技能文件。
type SkillProblem struct {
	Source string `json:"source"`
	Err    string `json:"err"`
}

// ListSkills 返回当前生效的技能清单（三层合并后，字典序）与存放目录。
func (a *App) ListSkills() (SkillCatalog, error) {
	h, err := a.reqHost()
	if err != nil {
		return SkillCatalog{}, err
	}
	return skillCatalogOf(h), nil
}

// ReloadSkills 重扫技能目录，让用户新放的 .md 立即可用（免重启）。
// 引擎运行中会被拒绝——裁定与执行必须看到同一份技能快照。
func (a *App) ReloadSkills() (SkillCatalog, error) {
	h, err := a.reqHost()
	if err != nil {
		return SkillCatalog{}, err
	}
	if _, _, err := h.ReloadSkills(); err != nil {
		return SkillCatalog{}, err
	}
	return skillCatalogOf(h), nil
}

// OpenSkillsDir 在系统文件管理器里打开技能目录，省掉用户手动找路径。
//
// 目录不存在时先建出来并补上 README——用户点"打开目录"就是想往里放东西，
// 打开一个空目录还得自己猜格式是最差的结果。
func (a *App) OpenSkillsDir() (string, error) {
	if _, err := a.reqHost(); err != nil {
		return "", err
	}
	dir := skills.DefaultHomeSkillsDir()
	if dir == "" {
		return "", fmt.Errorf("无法定位技能目录（家目录不可用）")
	}
	skills.EnsureHomeSkillsDir()
	// file:// URL 交给系统默认处理器；Wails 自带，不必为此引入平台相关的 exec。
	wailsruntime.BrowserOpenURL(a.ctx, "file://"+filepath.ToSlash(dir))
	return dir, nil
}

func skillCatalogOf(h *host.Host) SkillCatalog {
	views := h.Skills()
	out := SkillCatalog{
		Skills:   make([]SkillItem, 0, len(views)),
		Dir:      h.SkillsDir(),
		Problems: []SkillProblem{},
	}
	for _, sk := range views {
		out.Skills = append(out.Skills, SkillItem{
			Name: sk.Name, Description: sk.Description, Agent: sk.Agent,
			Scope: sk.Scope, Source: sk.Source, Body: sk.Body,
		})
	}
	for _, p := range h.SkillProblems() {
		out.Problems = append(out.Problems, SkillProblem{Source: p.Source, Err: p.Err})
	}
	return out
}

// RunSkill 执行指定技能。chapters 为空表示按技能声明的范围推导。
func (a *App) RunSkill(name string, chapters []int) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	return h.RunSkill(name, chapters)
}
