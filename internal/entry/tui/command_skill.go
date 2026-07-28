package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/host"
)

// runSkillCommand 实现 /skill：无参数列清单，带参数走干预通道执行技能。
//
// 运行中可用（Steer 即时注入）与停机可用（Continue 裁定后拉起引擎）都由
// Host.RunSkill 分流，因此这里不设 NeedsIdle。
func runSkillCommand(m Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		return listSkills(m)
	}
	// reload 是保留子命令：技能目录只在启动时扫一次，用户新放一个文件后需要一条
	// 不重启就能让它生效的路径。技能名受 ^[a-z0-9-]+$ 约束，"reload" 理论上可能
	// 与用户自定义技能同名，因此优先按子命令处理并在冲突时提示。
	if args[0] == "reload" {
		if len(args) > 1 {
			return skillError(m, "用法：/skill reload（不接受其他参数）")
		}
		return reloadSkills(m)
	}
	name := args[0]
	chapters, err := parseChapterArgs(args[1:])
	if err != nil {
		return skillError(m, err.Error())
	}
	if err := m.runtime.RunSkill(name, chapters); err != nil {
		return skillError(m, "技能执行失败："+err.Error())
	}
	return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime), m.textarea.Focus())
}

// reloadSkills 重扫技能目录。用户放完文件不必重启，也在这里第一次看到写坏的文件。
func reloadSkills(m Model) (tea.Model, tea.Cmd) {
	n, problems, err := m.runtime.ReloadSkills()
	if err != nil {
		return skillError(m, "重载技能失败："+err.Error())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已重载技能：当前 %d 个可用（/skill 查看清单）", n)
	appendSkillProblems(&b, problems)
	level := "info"
	if len(problems) > 0 {
		level = "warn"
	}
	m.applyEvent(host.Event{Time: time.Now(), Category: "SYSTEM", Summary: b.String(), Level: level})
	m.refreshEventViewport()
	return m, nil
}

// listSkills 把清单写进事件流。刻意不做 modal：清单短（内置两条 + 用户自定义），
// 为它建一个面板并不比直接看事件流更好用。
func listSkills(m Model) (tea.Model, tea.Cmd) {
	list := m.runtime.Skills()
	problems := m.runtime.SkillProblems()
	if len(list) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "当前没有可用技能；可在 %s 下新建 .md 文件（该目录有 README.txt 说明格式）",
			m.runtime.SkillsDir())
		appendSkillProblems(&b, problems)
		return skillError(m, b.String())
	}
	var b strings.Builder
	b.WriteString("可用专项技能（/skill <名称> [章节范围]，/skill reload 重扫目录）：")
	for _, sk := range list {
		fmt.Fprintf(&b, "\n  %s（%s，%s）— %s", sk.Name, scopeLabel(sk.Scope), sk.Source, sk.Description)
	}
	fmt.Fprintf(&b, "\n自定义技能放 %s", m.runtime.SkillsDir())
	appendSkillProblems(&b, problems)
	level := "info"
	if len(problems) > 0 {
		level = "warn"
	}
	m.applyEvent(host.Event{Time: time.Now(), Category: "SYSTEM", Summary: b.String(), Level: level})
	m.refreshEventViewport()
	return m, nil
}

// appendSkillProblems 把被跳过的技能文件附到消息末尾。
// 只写日志不够：用户写坏一个技能，清单里只是少一条，无从排查。
func appendSkillProblems(b *strings.Builder, problems []host.SkillProblem) {
	if len(problems) == 0 {
		return
	}
	fmt.Fprintf(b, "\n\n以下 %d 个技能文件被跳过（格式不合法）：", len(problems))
	for _, p := range problems {
		fmt.Fprintf(b, "\n  %s — %s", p.Source, p.Err)
	}
}

// scopeLabel 把 scope 渲染成中文。未知取值原样回显，不伪装成已知范围。
func scopeLabel(scope string) string {
	switch scope {
	case "chapters":
		return "作用于已完成章节"
	case "forward":
		return "作用于后续写作"
	case "foundation":
		return "作用于设定层"
	default:
		return scope
	}
}

func skillError(m Model, summary string) (tea.Model, tea.Cmd) {
	m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: summary, Level: "error"})
	m.refreshEventViewport()
	return m, nil
}

// parseChapterArgs 解析章节参数：支持 "3"、"3-5"、"3,5,7" 及它们的空格分隔组合。
// 解析失败必须报错而不是静默忽略——把 "3-5" 悄悄当成"未指定范围"会让技能作用到
// 用户没打算改的章节上。
func parseChapterArgs(args []string) ([]int, error) {
	var out []int
	for _, arg := range args {
		for _, part := range strings.Split(arg, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			nums, err := parseChapterRange(part)
			if err != nil {
				return nil, err
			}
			out = append(out, nums...)
		}
	}
	return out, nil
}

// parseChapterRange 解析单个片段：纯数字或 N-M 区间。
func parseChapterRange(part string) ([]int, error) {
	if from, to, ok := strings.Cut(part, "-"); ok {
		start, err1 := strconv.Atoi(strings.TrimSpace(from))
		end, err2 := strconv.Atoi(strings.TrimSpace(to))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("无法识别章节范围 %q（示例：3 / 3-5 / 3,5,7）", part)
		}
		if start < 1 || end < start {
			return nil, fmt.Errorf("章节范围 %q 无效（起始须 ≥1 且不大于结束）", part)
		}
		out := make([]int, 0, end-start+1)
		for ch := start; ch <= end; ch++ {
			out = append(out, ch)
		}
		return out, nil
	}
	ch, err := strconv.Atoi(part)
	if err != nil || ch < 1 {
		return nil, fmt.Errorf("无法识别章节号 %q（示例：3 / 3-5 / 3,5,7）", part)
	}
	return []int{ch}, nil
}
