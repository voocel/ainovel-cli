package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
)

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
			Description: "Xem danh sách lệnh",
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
			Description: "Chuyển mô hình và cường độ suy luận cho vai trò",
			AutoExecute: true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				roleHint := ""
				if len(args) > 0 {
					roleHint = args[0]
					if normalizeRoleKey(roleHint) == "" {
						m.applyEvent(host.Event{
							Time: time.Now(), Category: "ERROR", Summary: "Vai trò không rõ: " + roleHint, Level: "error",
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
			Name:        "config",
			Group:       "system",
			Usage:       "/config",
			Description: "Thêm hoặc sửa Provider, mô hình và cửa sổ ngữ cảnh",
			AutoExecute: true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				if len(args) != 0 {
					m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Sử dụng: /config", Level: "error"})
					m.refreshEventViewport()
					return m, nil
				}
				m.modelConfig = newModelConfigState(m.runtime)
				m.textarea.Blur()
				return m, nil
			},
		},
		{
			Name:        "diag",
			Group:       "analysis",
			Usage:       "/diag",
			Description: "Chẩn đoán sức khỏe sáng tác tiểu thuyết",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				m.reportSeq++
				m.report = newReportState(m.width, m.height, m.reportSeq, time.Now())
				m.textarea.Blur()
				return m, loadReport(m.runtime.Dir(), m.reportSeq)
			},
		},
		{
			Name:        "review",
			Group:       "writing",
			Usage:       "/review on|off",
			Description: "Bật/tắt chế độ duyệt từng chương",
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
					m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Sử dụng: /review on|off", Level: "error"})
					m.refreshEventViewport()
					return m, nil
				}
				mode := domain.ChapterAdvanceReview
				if args[0] == "off" {
					mode = domain.ChapterAdvanceAuto
				}
				if err := m.runtime.SetAdvanceMode(mode); err != nil {
					m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Chuyển chế độ đẩy chương thất bại: " + err.Error(), Level: "error"})
					m.refreshEventViewport()
					return m, nil
				}
				return m, fetchSnapshot(m.runtime)
			},
		},
		{
			Name:        "next",
			Group:       "writing",
			Usage:       "/next",
			Description: "Sau khi duyệt, cho phép một chương mới",
			AutoExecute: true,
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				if len(args) != 0 {
					m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Sử dụng: /next", Level: "error"})
					m.refreshEventViewport()
					return m, nil
				}
				if err := m.runtime.AdvanceOneChapter(); err != nil {
					m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Cho phép chương tiếp theo thất bại: " + err.Error(), Level: "error"})
					m.refreshEventViewport()
					return m, nil
				}
				return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime), m.textarea.Focus())
			},
		},
		{
			Name:        "start",
			Group:       "writing",
			Usage:       "/start <path>",
			Description: "Tạo sách mới từ tệp cài đặt hoặc đề cương",
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				if m.mode != modeNew {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "/start chỉ khả dụng ở trang chào mừng khi tạo sách mới", Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				prompt, err := prepareFileStart(args)
				if err != nil {
					m.err = err
					return m, nil
				}
				cmd := m.enterStarting(prompt)
				return m, tea.Batch(startRuntime(m.runtime, prompt), cmd)
			},
		},
		{
			Name:        "import",
			Group:       "writing",
			Usage:       "/import <path> [--yes] [--story=open|closed] [--continue] [--guide=<切分指导>]",
			Description: "Nhập tiểu thuyết ngoài bằng ngữ nghĩa (không có tham số thì khôi phục nhập dở; --guide dùng ngôn ngữ tự nhiên để điều chỉnh tách)",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				m.importSeq++
				state, listenCmd, err := startImport(m.runtime, m.importSeq, args, m.width, m.height)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Khởi động nhập thất bại: " + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.importer = state
				m.importHint = "" // Đã vào quy trình nhập, lời nhắc khôi phục màn hình chào hoàn thành sứ mệnh
				m.textarea.Blur()
				return m, listenCmd
			},
		},
		{
			Name:        "reopen",
			Group:       "writing",
			Usage:       "/reopen [续写方向]",
			Description: "Mở lại sách đã hoàn tất để tiếp tục sáng tác (hướng được đưa vào phán quyết trước, rồi tự động chạy tiếp)",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				if err := m.runtime.Reopen(strings.Join(args, " ")); err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Mở lại thất bại: " + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				return m, tea.Batch(m.textarea.Focus(), resumeBook(m.runtime))
			},
		},
		{
			Name:        "cocreate",
			Aliases:     []string{"plan"},
			Group:       "writing",
			Usage:       "/cocreate",
			Description: "Tạm dừng sáng tác, cùng lên kế hoạch hướng đi giai đoạn tiếp theo",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				if m.mode != modeRunning {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Cùng sáng tác giai đoạn chỉ khả dụng khi đang viết", Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				if !m.runtime.PauseForCoCreate() {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Không thể vào cùng sáng tác: toàn bộ sách đã hoàn tất hoặc đang trong cùng sáng tác", Level: "error",
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
			Description: "Đọc ./simulate để tạo hoặc cập nhật gia tăng chân dung phỏng viết",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				m.simSeq++
				state, listenCmd, err := startSimulate(m.runtime, m.simSeq, args, m.width, m.height)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Khởi động chân dung phỏng viết thất bại: " + err.Error(), Level: "error",
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
			Description: "Nhập chân dung phỏng viết hiện có và hợp nhất theo dấu vân tay ngữ liệu",
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				m.simSeq++
				state, listenCmd, err := startImportSimulation(m.runtime, m.simSeq, args, m.width, m.height)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Nhập chân dung phỏng viết thất bại: " + err.Error(), Level: "error",
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
			Name:        "sync",
			Group:       "writing",
			Usage:       "/sync [--check]",
			Description: "Kiểm tra hoặc chấp nhận thay đổi thủ công ở chương đã hoàn tất",
			AutoExecute: true,
			NeedsIdle:   true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				cmd, checkOnly, err := startRevisionSync(m.runtime, args)
				if err != nil {
					m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Khởi động đồng bộ chương thất bại: " + err.Error(), Level: "error"})
					m.refreshEventViewport()
					return m, nil
				}
				summary := "Đang phân tích và chấp nhận sửa đổi chương..."
				if checkOnly {
					summary = "Đang kiểm tra sửa đổi chương bên ngoài..."
				}
				m.applyEvent(host.Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
				m.refreshEventViewport()
				return m, cmd
			},
		},
		{
			Name:        "export",
			Group:       "writing",
			Usage:       "/export [path] [from=N] [to=M] [--overwrite]",
			Description: "Xuất các chương đã hoàn tất ra TXT/EPUB",
			AutoExecute: true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				cmd, err := startExport(m.runtime, args)
				if err != nil {
					m.applyEvent(host.Event{
						Time: time.Now(), Category: "ERROR", Summary: "Khởi động xuất thất bại: " + err.Error(), Level: "error",
					})
					m.refreshEventViewport()
					return m, nil
				}
				m.applyEvent(host.Event{
					Time: time.Now(), Category: "SYSTEM", Summary: "Đang xuất...", Level: "info",
				})
				m.refreshEventViewport()
				return m, cmd
			},
		},
	})
}

func commandSpecs() []slashCommandSpec {
	return commandRegistryInstance().Visible()
}

func prepareFileStart(args []string) (string, error) {
	path := strings.TrimSpace(strings.Join(args, " "))
	if len(path) >= 2 && ((path[0] == '"' && path[len(path)-1] == '"') ||
		(path[0] == '\'' && path[len(path)-1] == '\'')) {
		path = path[1 : len(path)-1]
	}
	if path == "" {
		return "", fmt.Errorf("Sử dụng: /start <đường dẫn tệp cài đặt hoặc đề cương>")
	}
	prompt, err := startup.LoadPromptFile(path)
	if err != nil {
		return "", err
	}
	return startup.PrepareQuick(prompt)
}

func (m Model) handleSlashCommand(cmd slashCommand) (tea.Model, tea.Cmd) {
	spec, ok := commandRegistryInstance().Find(cmd.name)
	if !ok {
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "ERROR", Summary: "Lệnh không rõ: /" + cmd.name, Level: "error",
		})
		m.refreshEventViewport()
		return m, nil
	}
	if spec.NeedsIdle && m.snapshot.IsRunning {
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "ERROR", Summary: "Lệnh chỉ có thể thực hiện khi nhàn rỗi: /" + spec.Name, Level: "error",
		})
		m.refreshEventViewport()
		return m, nil
	}
	return spec.Run(m, cmd.args)
}
