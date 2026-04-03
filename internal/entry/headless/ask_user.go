package headless

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/tools"
)

type terminalAskUser struct {
	in  *bufio.Reader
	out io.Writer
	mu  sync.Mutex
}

func newTerminalAskUser(in io.Reader, out io.Writer) *terminalAskUser {
	return &terminalAskUser{
		in:  bufio.NewReader(in),
		out: out,
	}
}

func (h *terminalAskUser) handle(ctx context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	resp := &tools.AskUserResponse{
		Answers: make(map[string]string, len(questions)),
		Notes:   make(map[string]string),
	}

	for _, q := range questions {
		answer, note, err := h.askOne(ctx, q)
		if err != nil {
			return nil, err
		}
		resp.Answers[q.Question] = answer
		if strings.TrimSpace(note) != "" {
			resp.Notes[q.Question] = note
		}
	}

	return resp, nil
}

func (h *terminalAskUser) askOne(ctx context.Context, q tools.Question) (string, string, error) {
	fmt.Fprintf(h.out, "\n[%s] %s\n", q.Header, q.Question)
	for i, opt := range q.Options {
		fmt.Fprintf(h.out, "  %d. %s - %s\n", i+1, opt.Label, opt.Description)
	}
	fmt.Fprintln(h.out, "  0. 自定义输入")

	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if q.MultiSelect {
			fmt.Fprint(h.out, "请输入编号，多个用逗号分隔: ")
		} else {
			fmt.Fprint(h.out, "请输入编号: ")
		}

		line, err := h.readLine()
		if err != nil {
			return "", "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Fprintln(h.out, "输入不能为空，请重试。")
			continue
		}
		if line == "0" {
			fmt.Fprint(h.out, "请输入自定义内容: ")
			note, err := h.readLine()
			if err != nil {
				return "", "", err
			}
			note = strings.TrimSpace(note)
			if note == "" {
				fmt.Fprintln(h.out, "自定义内容不能为空，请重试。")
				continue
			}
			return "自定义", note, nil
		}

		labels, err := parseSelections(line, q.Options, q.MultiSelect)
		if err != nil {
			fmt.Fprintf(h.out, "%v，请重试。\n", err)
			continue
		}
		return strings.Join(labels, "、"), "", nil
	}
}

func (h *terminalAskUser) readLine() (string, error) {
	line, err := h.in.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func parseSelections(line string, options []tools.Option, multi bool) ([]string, error) {
	parts := strings.Split(line, ",")
	if !multi && len(parts) > 1 {
		return nil, fmt.Errorf("当前问题只允许单选")
	}

	seen := make(map[int]bool, len(parts))
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("编号不能为空")
		}

		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
			return nil, fmt.Errorf("无法识别编号 %q", part)
		}
		if idx <= 0 || idx > len(options) {
			return nil, fmt.Errorf("编号 %d 超出范围", idx)
		}
		if seen[idx] {
			continue
		}
		seen[idx] = true
		labels = append(labels, options[idx-1].Label)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("至少选择一个选项")
	}
	return labels, nil
}
