package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/skills"
)

// resolveRewritePending 解析 /rewrite 的参数为重写后的新方向（pendingArgs）。
//
// 三种形态：
//   - 无参数：pendingArgs 为空，授权后用户自己填
//   - @<路径>：读取文件内容（共创导出的 md），作为新方向
//   - 普通文本：原样作为新方向
//
// 返回 (pendingArgs, cmd, err)：cmd 用于在 @ 路径解析阶段调度额外命令（目前恒为 nil）。
func resolveRewritePending(m Model, args []string) (string, tea.Cmd, error) {
	if len(args) == 0 {
		return "", nil, nil
	}
	first := args[0]
	if strings.HasPrefix(first, "@") || isLikelyPathOnly(args) {
		// @path 形式：把所有 args 合并为单一路径字符串（支持路径含空格的边缘场景）
		raw := strings.Join(args, " ")
		content, err := loadFileAsPrompt(m.runtime.Dir(), raw)
		if err != nil {
			return "", nil, err
		}
		return content, nil, nil
	}
	return strings.Join(args, " "), nil, nil
}

// isLikelyPathOnly 当首个 arg 看起来像路径（含 / 或 .md 后缀）而 args 总长很短时，
// 把整段 args 当路径解析。避免误把含 / 的中文需求当路径。
func isLikelyPathOnly(args []string) bool {
	if len(args) == 0 || len(args) > 2 {
		return false
	}
	// 仅当看起来像 "path" 或 "path with space" 这种短结构
	for _, a := range args {
		if strings.Contains(a, "\n") {
			return false
		}
	}
	first := args[0]
	if strings.HasSuffix(first, ".md") {
		return true
	}
	// 含 / 但开头不是中文（用首字节粗略判断：ASCII / UTF-8 多字节首位 >= 0x80）
	if strings.Contains(first, "/") {
		if first == "" {
			return false
		}
		if first[0] >= 0x80 {
			return false
		}
		return true
	}
	return false
}

type slashCommandSpec struct {
	Name        string
	Aliases     []string
	Group       string
	Usage       string
	Description string
	AutoExecute bool
	Hidden      bool
	NeedsIdle   bool
	Run         func(m Model, args []string) (tea.Model, tea.Cmd)
}

type slashCommand struct {
	name string
	args []string
}

func parseSlashCommand(text string) (slashCommand, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return slashCommand{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return slashCommand{}, false
	}
	return slashCommand{name: strings.ToLower(fields[0]), args: fields[1:]}, true
}

func (s slashCommandSpec) matches(name string) bool {
	if s.Name == name {
		return true
	}
	for _, alias := range s.Aliases {
		if strings.EqualFold(alias, name) {
			return true
		}
	}
	return false
}

func commandRegistryInstance() commandRegistry {
	return newCommandRegistry([]slashCommandSpec{
		{
			Name:        "help",
			Group:       "system",
			Usage:       "/help",
			Description: "查看命令列表",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				m.help = newHelpState(m.width, m.height)
				m.textarea.Blur()
				return m, nil
			},
		},
		{
			Name:        "model",
			Group:       "system",
			Usage:       "/model [role]",
			Description: "切换默认或角色模型",
			AutoExecute: true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				roleHint := ""
				if len(args) > 0 {
					roleHint = args[0]
					if normalizeRoleKey(roleHint) == "" {
						m.applyEvent(host.Event{
							Time: time.Now(), Category: "ERROR", Summary: "未知角色：" + roleHint, Level: "error",
						})
						m.refreshEventViewport()
						return m, nil
					}
				}
				m.modelSwitch = newModelSwitchState(m.runtime, roleHint)
				m.textarea.Blur()
				return m, nil
			},
		},
		{
			Name:        "diag",
			Group:       "analysis",
			Usage:       "/diag",
			Description: "诊断小说创作健康度",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				m.reportSeq++
				m.report = newReportState(m.width, m.height, m.reportSeq, time.Now())
				m.textarea.Blur()
				return m, loadReport(m.runtime.Dir(), m.reportSeq)
			},
		},
		{
			Name:        "import",
			Group:       "writing",
			Usage:       "/import <path> [from=N]",
			Description: "反推外部小说续写",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				m.importSeq++
				state, listenCmd, err := startImport(m.runtime, m.importSeq, args, m.width, m.height)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "导入启动失败：" + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.importer = state
				m.textarea.Blur()
				return m, listenCmd
			},
		},
		{
			Name:        "cocreate",
			Aliases:     []string{"plan"},
			Group:       "writing",
			Usage:       "/cocreate",
			Description: "暂停创作，共创规划后续阶段走向",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				if m.mode != modeRunning {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "阶段共创仅在创作中可用", Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				if !m.runtime.PauseForCoCreate() {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "无法进入阶段共创：全书已完成或已在共创中", Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.cocreate = newStageCoCreateState()
				m.resizeTextarea()
				m.textarea.Blur()
				return m, m.sendCoCreate()
			},
		},
		{
			Name:        "simulate",
			Group:       "writing",
			Usage:       "/simulate",
			Description: "读取 ./simulate 生成或增量更新仿写画像",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				m.simSeq++
				state, listenCmd, err := startSimulate(m.runtime, m.simSeq, args, m.width, m.height)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "仿写画像启动失败：" + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.simulator = state
				m.textarea.Blur()
				return m, listenCmd
			},
		},
		{
			Name:        "importsim",
			Group:       "writing",
			Usage:       "/importsim <profile.json>",
			Description: "导入已有仿写画像并按语料指纹合并",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				m.simSeq++
				state, listenCmd, err := startImportSimulation(m.runtime, m.simSeq, args, m.width, m.height)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "导入仿写画像失败：" + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.simulator = state
				m.textarea.Blur()
				return m, listenCmd
			},
		},
		{
			Name:        "export",
			Group:       "writing",
			Usage:       "/export [path] [from=N] [to=M] [--overwrite]",
			Description: "导出已完成章节为 TXT/EPUB",
			AutoExecute: true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				cmd, err := startExport(m.runtime, args)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "导出启动失败：" + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.applyEvent(host.Event{
					Time: time.Now(), Category: "SYSTEM", Summary: "正在导出...", Level: "info",
				})
				m.refreshEventViewport()
				return m, cmd
			},
		},
		{
			Name:        "materials",
			Aliases:     []string{"material", "素材"},
			Group:       "writing",
			Usage:       "/materials [需求描述]",
			Description: "搜集素材并筛选入库（项目级 meta/materials.json）",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				userPrompt := strings.Join(args, " ")
				if strings.TrimSpace(userPrompt) == "" {
					// 无参数时尝试用本书 premise 作 prompt；没有 premise 就报错
					if prem, _ := m.runtime.LoadPremise(); strings.TrimSpace(prem) != "" {
						userPrompt = prem
					} else {
						m.applyEvent(host.Event{
							Time: time.Now(), Category: "ERROR",
							Summary: "请提供需求描述，例：/materials 赛博朋克短篇 霓虹残响",
							Level:   "error",
						})
						m.refreshEventViewport()
						return m, nil
					}
				}
				m.materialsSeq++
				state := newMaterialsState(userPrompt)
				m.materials = state
				m.textarea.Blur()
				return m, runMaterialsCollect(m.runtime, userPrompt)
			},
		},
		{
			Name:        "rewrite",
			Aliases:     []string{"reset", "重写"},
			Group:       "system",
			Usage:       "/rewrite [新方向 | @路径]",
			Description: "清空全量 foundation 重新规划（需输入 yes/确认 授权）",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				pending, cmd, err := resolveRewritePending(m, args)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR",
						Summary: err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.rewriteConfirm = newRewriteConfirmState(pending)
				m.textarea.Blur()
				return m, cmd
			},
		},
		{
			Name:        "load",
			Aliases:     []string{"导入"},
			Group:       "writing",
			Usage:       "/load <路径> [--send]",
			Description: "加载 md 文件：≤32KB 进输入框可编辑，>32KB 自动直接发送（也可显式 --send 强制发送）",
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				if len(args) == 0 {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR",
						Summary: "请提供路径，例：/load meta/cocreate/x.md [--send]",
						Level:   "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				sendMode := false
				var pathArgs []string
				for _, a := range args {
					if a == "--send" || a == "-s" {
						sendMode = true
					} else {
						pathArgs = append(pathArgs, a)
					}
				}
				if len(pathArgs) == 0 {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR",
						Summary: "请提供路径，例：/load meta/cocreate/x.md [--send]",
						Level:   "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				var content string
				var err error
				// 自动判断：文件超过 maxLoadFileSize 时自动启用 send 模式（绕过 32KB 限制），
				// 否则按原行为加载到输入框。用户也可显式 --send 强制发送。
				autoSend := false
				if abs, statErr := resolveProjectPath(m.runtime.Dir(), pathArgs[0]); statErr == nil {
					if info, infoErr := os.Stat(abs); infoErr == nil && !info.IsDir() {
						autoSend = info.Size() > maxLoadFileSize
					}
				}
				effectiveSend := sendMode || autoSend
				if effectiveSend {
					content, err = loadFileForSend(m.runtime.Dir(), pathArgs[0])
				} else {
					content, err = loadFileAsPrompt(m.runtime.Dir(), pathArgs[0])
				}
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR",
						Summary: err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				if effectiveSend {
					hint := "直接发送文件内容"
					if autoSend && !sendMode {
						hint = fmt.Sprintf("文件超过 %d 字节，自动切换为直接发送", maxLoadFileSize)
					}
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "SYSTEM",
						Summary: fmt.Sprintf("%s：%d 字符", hint, len([]rune(content))),
						Level:   "info",
					})
					m.refreshEventViewport()
					return m.submitUserText(content)
				}
				m.textarea.SetValue(content)
				m.refitTextareaHeight()
				m.applyEvent(host.Event{
					Time: time.Now(), Category: "SYSTEM",
					Summary: fmt.Sprintf("已加载 %d 字符到输入框（按 Enter 提交，或继续编辑）", len([]rune(content))),
					Level:   "info",
				})
				m.refreshEventViewport()
				return m, nil
			},
		},
	})
}

func commandSpecs() []slashCommandSpec {
	return commandRegistryInstance().Visible()
}

func (m Model) handleSlashCommand(cmd slashCommand) (tea.Model, tea.Cmd) {
	spec, ok := commandRegistryInstance().Find(cmd.name)
	if !ok {
		// 未注册命令：尝试作为 skill 引用展开（/skill-name 用户消息）
		if m.runtime != nil {
			userMsg := strings.Join(cmd.args, " ")
			result := m.runtime.SkillInject(skills.InjectRequest{
				SkillName: cmd.name,
				UserMsg:   userMsg,
			})
			if result.Expanded {
				return m.submitUserText(result.Text)
			}
			// 未命中：显示 hint，输入框已 reset，让用户重输
			hint := result.Hint
			if hint == "" {
				hint = "未知命令：/" + cmd.name
			}
			m.applyEvent(host.Event{
				Time: time.Now(), Category: "ERROR", Summary: hint, Level: "error",
			})
			m.refreshEventViewport()
			return m, nil
		}
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "ERROR", Summary: "未知命令：/" + cmd.name, Level: "error",
		})
		m.refreshEventViewport()
		return m, nil
	}
	if spec.NeedsIdle && m.snapshot.IsRunning {
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "ERROR", Summary: "命令仅可在空闲状态执行：/" + spec.Name, Level: "error",
		})
		m.refreshEventViewport()
		return m, nil
	}
	return spec.Run(m, cmd.args)
}
