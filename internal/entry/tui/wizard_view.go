package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

type wizardHit struct{ x, y, width, action int }
type wizardScreen struct {
	lines []string
	hits  []wizardHit
}

// Drawing and hit testing share geometry.
func (m model) wizardLayout() wizardScreen {
	w := max(1, m.width-4)
	fieldWidth := min(68, w)
	screen := wizardScreen{}
	add := func(line string) { screen.lines = append(screen.lines, "  "+fitLine(line, w)) }
	control := func(line string, action int, selected bool) {
		style := benchTheme.Text
		prefix := "  "
		if selected {
			style = benchTheme.Selected
			prefix = "› "
		}
		screen.hits = append(screen.hits, wizardHit{2, len(screen.lines), fieldWidth, action})
		add(style.Width(fieldWidth).Render(fitLine(prefix+line, fieldWidth)))
	}
	add(benchTheme.Accent.Bold(true).Render("AINOVEL") + benchTheme.Muted.Render("  /  模型连接"))
	add(benchRule(w))
	add("")
	if m.wizard.choosing {
		add(benchTheme.Title.Render("选择你的模型服务商"))
		add(benchTheme.Muted.Render("使用已有账号连接，随时可以更换。"))
		add("")
		choices := m.wizardChoices()
		visible := min(len(choices), max(1, m.height-10))
		start := max(0, min(m.wizard.provider-visible/2, len(choices)-visible))
		for i := start; i < start+visible; i++ {
			control(choices[i].label, i, i == m.wizard.provider)
		}
	} else {
		label := m.wizard.inputs[0].Value()
		for _, provider := range wizardProviders {
			if provider.id == label && label != "" {
				label = provider.label
				break
			}
		}
		control(label+"    选择连接 ›", 6, m.wizard.step == 6)
		add("")
		for _, i := range []int{7, 0, 1, 2} {
			if i == 0 && !m.wizard.custom {
				continue
			}
			if i == 0 {
				add(benchTheme.Muted.Render("协议类型 · ← → 选择"))
				x := 2
				parts := []string{}
				for j, protocol := range wizardProtocols {
					style := benchTheme.Muted.Padding(0, 1)
					if protocol == m.wizard.inputs[0].Value() {
						style = benchTheme.Selected.Padding(0, 1)
					}
					segment := style.Render(protocol)
					screen.hits = append(screen.hits, wizardHit{x, len(screen.lines), lipgloss.Width(segment), 20 + j})
					parts = append(parts, segment)
					x += lipgloss.Width(segment) + 1
				}
				add(strings.Join(parts, " "))
			} else {
				screen.addWizardInput(m, i, fieldWidth, add)
			}
		}
		arrow := "▸"
		if m.wizard.advanced {
			arrow = "▾"
		}
		addressLabel := arrow + " 自定义连接地址"
		if !m.wizard.advanced && m.wizard.inputs[3].Value() != "" {
			addressLabel += " · 已设置"
		}
		control(addressLabel, 5, m.wizard.step == 5)
		if m.wizard.advanced {
			screen.addWizardInput(m, 3, fieldWidth, add)
		}
		if m.wizard.inputs[0].Value() == "openai" {
			endpoint := m.wizard.endpoint
			if endpoint == "" {
				endpoint = "chat"
			}
			control("接口 · "+endpoint+"  ⇄", 8, m.wizard.step == 8)
		}
		control("保存并开始 →", 4, m.wizard.step == 4)
		button := "测试连接（可选）"
		if m.wizard.verifying {
			button = "正在验证连接…  Esc 取消"
		}
		control(button, 9, m.wizard.step == 9 || m.wizard.verifying)
	}
	add("")
	add(benchTheme.Muted.Render("配置保存在本机；仅测试连接会发送请求。"))
	add(benchTheme.Muted.Render("保存：" + appconfig.ConfigPath(m.deps.ConfigDir)))
	add(benchTheme.Muted.Render("重启时环境变量优先于配置文件。"))
	notes := []string{
		benchTheme.Accent.Render("连接，然后开始创作"),
		"", "选择服务商 → 配置连接", "", "配置只需完成一次。", "",
		benchTheme.Muted.Render("使用服务商提供的模型 ID。"),
		benchTheme.Muted.Render("环境凭据或本地模型可不填密钥。"),
		benchTheme.Muted.Render("中转服务需填写自定义地址。"),
	}
	column := max(76, m.width/2)
	for i, note := range notes {
		row := i + 3
		if row >= len(screen.lines) {
			break
		}
		// fitLine only truncates; pad the left column in terminal cells so
		// short text and blank rows cannot pull the sidebar toward the form.
		left := lipgloss.NewStyle().Width(column).Render(fitLine(screen.lines[row], column))
		screen.lines[row] = left + benchTheme.Border.Render("│") + "  " + fitLine(note, max(1, m.width-column-5))
	}
	return screen
}

func (s *wizardScreen) addWizardInput(m model, i, width int, add func(string)) {
	label := "连接名称"
	if i == 7 && m.wizard.original != "" {
		label = "连接名称 · 改名另存"
	}
	if i != 7 {
		label = wizardFields[i].label
	}
	if i == 0 {
		label = "协议类型"
	}
	style := benchTheme.Muted
	if m.wizard.step == i {
		style = benchTheme.Accent
	}
	input := m.wizard.name
	if i != 7 {
		input = m.wizard.inputs[i]
	}
	input.Prompt = "  "
	if m.wizard.step == i {
		input.Prompt = "› "
	}
	input.Width = max(1, width-4)
	add(style.Render(label))
	input.SetCursor(input.Position())
	s.hits = append(s.hits, wizardHit{2, len(s.lines), width, i})
	add(input.View())
	add("")
}

func (m model) viewWizard() string {
	screen := m.wizardLayout()
	lines := fitBlock(strings.Join(screen.lines, "\n"), m.width, m.height-3)
	hint := "↑↓ 选择 · Enter 继续 · Esc 返回"
	if !m.wizard.choosing {
		hint = "Tab 切换 · Enter 继续 · Esc 返回"
		if m.wizard.step == 0 {
			hint = "← → 选择协议 · Enter 继续"
		}
	}
	if m.wizard.verifying {
		hint = "正在连接 · Esc 取消验证"
	}
	feedback := benchTheme.Error.Render(m.wizard.err)
	if m.wizard.err == "" {
		feedback = styleNotice.Render(m.wizard.notice)
	}
	lines = append(lines, "  "+benchRule(m.width-4), "  "+fitLine(feedback, m.width-4), "  "+fitLine(benchTheme.Muted.Render(hint), m.width-4))
	return strings.Join(fitBlock(strings.Join(lines, "\n"), m.width, m.height), "\n")
}
