// Package notify 提供Không có人值守告警通道。
//
// 合宪定位（architecture.md §2.3）：纯观察层动作——告警永不介入控制流
// （不Thử lại、不改派、不停机），只是把 TUI 内已有的事件"喊"到屏幕之外。
// Send 异步执行、永不阻塞 Host、Thất bại只记 slog。
package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notification 一条告警的Tất cả事实。
type Notification struct {
	Kind  string `json:"kind"`  // run_end / repeat / budget
	Level string `json:"level"` // info / warn / error
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Notifier 按Cấu hình分发通知。零值Không可用，必须经 New Tạo；nil 安全（Send noop）。
type Notifier struct {
	command string          // 非Rỗng时替代 system 通道（手机推送走这里）
	events  map[string]bool // nil = Tất cả kind 放行
	timeout time.Duration
}

// New Tạo Notifier。command 为Rỗng走内置 system 通道（macOS osascript /
// Linux notify-send，找不到命令静默降级为仅 slog）；events 非Rỗng时只放行列出的 kind。
func New(command string, events []string) *Notifier {
	n := &Notifier{command: strings.TrimSpace(command), timeout: 10 * time.Second}
	if len(events) > 0 {
		n.events = make(map[string]bool, len(events))
		for _, ev := range events {
			n.events[ev] = true
		}
	}
	return n
}

// Send 异步发送一条通知。过滤、执行、Thất bại处理Tất cả不影响调用方。
func (n *Notifier) Send(nt Notification) {
	if !n.allows(nt.Kind) {
		return
	}
	go n.deliver(nt)
}

// allows Quay lại该 kind Có czy không放行（nil Notifier / 未列入 events 时拦截）。
func (n *Notifier) allows(kind string) bool {
	if n == nil {
		return false
	}
	return n.events == nil || n.events[kind]
}

// deliver 同步执行一次发送（goroutine 内运行；测试可直接调用以同步断言）。
func (n *Notifier) deliver(nt Notification) {
	defer func() { recover() }()
	ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
	defer cancel()

	var err error
	if n.command != "" {
		err = runCommand(ctx, n.command, nt)
	} else {
		err = runSystem(ctx, nt)
	}
	if err != nil {
		slog.Warn("通知发送Thất bại", "module", "notify", "kind", nt.Kind, "err", err)
	}
}

// runCommand 执行用户Cấu hình的命令：字段经环境变量传入（一行 curl 零依赖、Không có注入
// 风险），完整 JSON 同时写 stdin（复杂分发Cảnh自行解析）。超时由 ctx 强杀。
func runCommand(ctx context.Context, command string, nt Notification) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"NOTIFY_KIND="+nt.Kind,
		"NOTIFY_LEVEL="+nt.Level,
		"NOTIFY_TITLE="+nt.Title,
		"NOTIFY_BODY="+nt.Body,
	)
	payload, _ := json.Marshal(nt)
	cmd.Stdin = strings.NewReader(string(payload))
	return cmd.Run()
}

// runSystem 内置桌面通知：只覆盖"人在电脑旁"的Cảnh，找不到命令静默降级。
func runSystem(ctx context.Context, nt Notification) error {
	switch runtime.GOOS {
	case "darwin":
		script := "display notification " + appleScriptString(nt.Body) + " with title " + appleScriptString(nt.Title)
		return exec.CommandContext(ctx, "osascript", "-e", script).Run()
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			slog.Info("通知降级为日志（Không có notify-send）", "module", "notify", "title", nt.Title, "body", nt.Body)
			return nil
		}
		return exec.CommandContext(ctx, "notify-send", nt.Title, nt.Body).Run()
	default:
		slog.Info("通知降级为日志（平台Không có system 通道）", "module", "notify", "title", nt.Title, "body", nt.Body)
		return nil
	}
}

// appleScriptString 把任意文本包装为 AppleScript 字符串字面量。
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
