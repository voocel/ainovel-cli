package host

import (
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/skills"
)

// SkillView 是专项技能给 UI 与入口层看的投影。
type SkillView struct {
	Name        string
	Description string
	Agent       string
	Scope       string
	Source      string // 来源标签(builtin/global/项目:文件名),让用户看清哪一层生效
	Body        string
}

// SkillProblem 是一个被跳过的技能文件,供 UI 告知用户"你写的技能没生效,原因是"。
type SkillProblem struct {
	Source string
	Err    string
}

// skillCatalog 取当前技能目录。所有读取都必须走这里:ReloadSkills 会整体替换它。
func (h *Host) skillCatalog() skills.Catalog {
	h.skillsMu.RLock()
	defer h.skillsMu.RUnlock()
	return h.skills
}

// engineRunning 只读生命周期位。刻意不走 Snapshot():那是给 UI 的完整聚合,会去碰
// models/usage/budget,只为取一个 bool 而放大依赖面。与包内其他门禁同一手法。
func (h *Host) engineRunning() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lifecycle == lifecycleRunning
}

// SkillsDir 返回全局技能目录路径,供 UI 告诉用户该把文件放哪。
func (h *Host) SkillsDir() string { return skills.DefaultHomeSkillsDir() }

// SkillProblems 返回本次加载被跳过的技能文件。
func (h *Host) SkillProblems() []SkillProblem {
	probs := h.skillCatalog().Problems()
	out := make([]SkillProblem, 0, len(probs))
	for _, p := range probs {
		out = append(out, SkillProblem{Source: p.Source, Err: p.Err})
	}
	return out
}

// ReloadSkills 重扫三层目录,让用户新放的技能立即可用,免去重启。
//
// 刻意拒绝在引擎运行时重载:裁定时看到的名单与执行时取到的正文必须是同一份快照。
// 运行中换目录会让一次已裁定的技能派单在执行期取到不同正文(甚至取不到)。
// 返回重载后的技能数与被跳过的文件,让调用方能直接回显结果。
func (h *Host) ReloadSkills() (int, []SkillProblem, error) {
	if h.engineRunning() {
		return 0, nil, fmt.Errorf("创作运行中不能重载技能，请先暂停（Esc）后重试")
	}
	catalog := skills.Load(skills.DefaultOptions())

	h.skillsMu.Lock()
	h.skills = catalog
	h.skillsMu.Unlock()
	// engine 持有自己的副本(执行期取正文用),必须一并换掉,否则新技能只在裁定
	// 名单里出现、执行时却找不到正文。引擎已停机,此处无竞争。
	if h.engine != nil {
		h.engine.skills = catalog
	}
	return catalog.Len(), h.SkillProblems(), nil
}

// Skills 返回当前生效的技能清单(三层合并后,字典序)。
func (h *Host) Skills() []SkillView {
	list := h.skillCatalog().List()
	out := make([]SkillView, 0, len(list))
	for _, sk := range list {
		out = append(out, SkillView{
			Name: sk.Name, Description: sk.Description, Agent: sk.Agent,
			Scope: string(sk.Scope), Source: sk.Source, Body: sk.Body,
		})
	}
	return out
}

// RunSkill 用户显式调用技能。
//
// 刻意走干预通道而非直接派单:绕过 Arbiter 就同时绕过阶段校验
// (validateDispatchAgainst)、配套 hold 判断与裁定审计,与"所有修改经裁定"的架构
// 纪律冲突。用户的选择通过合成的干预原文表达,范围与配套动作仍由 Arbiter 裁定——
// 技能名与章节已在此处机械校验过,裁定只需处理"要不要同时暂停/该派给谁"。
//
// chapters 为空表示按技能声明的范围由 Engine 推导。
func (h *Host) RunSkill(name string, chapters []int) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("请指定技能名（可用 /skill 查看清单）")
	}
	catalog := h.skillCatalog()
	sk, ok := catalog.Get(name)
	if !ok {
		available := catalog.Names()
		if len(available) == 0 {
			return fmt.Errorf("当前没有可用技能；可在 %s 下新建 .md 文件（该目录有 README.txt 说明格式）",
				skills.DefaultHomeSkillsDir())
		}
		return fmt.Errorf("未知技能 %q（可用：%s）", name, strings.Join(available, "、"))
	}
	// 范围与技能声明不符必须在裁定前挡住:让 Arbiter 去拒绝会白花一次模型调用,
	// 且错误信息会被包装成裁定失败,不如在这里直接给用户可操作的提示。
	if len(chapters) > 0 && sk.Scope != skills.ScopeChapters {
		return fmt.Errorf("技能 %s 的作用范围是 %s，不接受章节号", sk.Name, sk.Scope)
	}
	for _, chapter := range chapters {
		if chapter < 1 {
			return fmt.Errorf("章节号必须大于 0，收到 %d", chapter)
		}
	}
	chapters = normalizeChapters(chapters)
	text := buildSkillIntervention(sk, chapters)

	// 运行中 → Steer 即时注入;停机 → Continue(它带预算与独占作业门禁,
	// 并在裁定后拉起引擎)。与 ReviseFoundation 同一分流。
	if h.engineRunning() {
		return h.Steer(text)
	}
	return h.Continue(text)
}

// buildSkillIntervention 把显式技能调用合成干预原文。
//
// 它同时是下游 Worker 看到的授权段原文(interventionDispatchTask 会原样附上),
// 因此必须把范围写死:说"第 3-5 章"就只授权这三章,不留可被扩大解读的余地。
func buildSkillIntervention(sk skills.Skill, chapters []int) string {
	var b strings.Builder
	b.WriteString("执行专项技能 ")
	b.WriteString(sk.Name)
	if len(chapters) > 0 {
		b.WriteString("，作用范围严格限定为")
		b.WriteString(describeChapterList(chapters))
		b.WriteString("，不得改动范围外的任何章节")
	} else {
		switch sk.Scope {
		case skills.ScopeChapters:
			b.WriteString("，作用于最近已完成的章节")
		case skills.ScopeForward:
			b.WriteString("，作用于后续写作")
		case skills.ScopeFoundation:
			b.WriteString("，作用于设定层")
		}
	}
	b.WriteString("。技能职责：")
	b.WriteString(sk.Description)
	b.WriteString("。这是用户显式选择的技能，请按该技能执行。")
	return b.String()
}

// describeChapterList 渲染章节列表:连续区间用范围,离散用列举。
func describeChapterList(chapters []int) string {
	if len(chapters) == 0 {
		return ""
	}
	if len(chapters) == 1 {
		return fmt.Sprintf("第 %d 章", chapters[0])
	}
	if chapters[len(chapters)-1]-chapters[0] == len(chapters)-1 {
		return fmt.Sprintf("第 %d-%d 章", chapters[0], chapters[len(chapters)-1])
	}
	parts := make([]string, 0, len(chapters))
	for _, ch := range chapters {
		parts = append(parts, fmt.Sprint(ch))
	}
	return "第 " + strings.Join(parts, "、") + " 章"
}

// normalizeChapters 去重排序并丢掉非正数(用户手输与前端都可能给脏数据)。
func normalizeChapters(chapters []int) []int {
	if len(chapters) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(chapters))
	out := make([]int, 0, len(chapters))
	for _, ch := range chapters {
		if ch < 1 {
			continue
		}
		if _, dup := seen[ch]; dup {
			continue
		}
		seen[ch] = struct{}{}
		out = append(out, ch)
	}
	sort.Ints(out)
	return out
}
