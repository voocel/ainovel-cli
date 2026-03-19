package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
)

// AskUserResponse 用户回答结果。
type AskUserResponse struct {
	Answers map[string]string // question text → 用户选择的答案
	Notes   map[string]string // question text → 自定义输入（选"其他"时）
}

// AskUserHandler 阻塞等待用户回答，由 CLI 或 TUI 注入具体实现。
type AskUserHandler func(ctx context.Context, questions []Question) (*AskUserResponse, error)

// Question 单个问题。
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect"`
}

// Option 可选项。
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// AskUserTool 让 LLM 向用户提出结构化问题。
type AskUserTool struct {
	mu      sync.RWMutex
	handler AskUserHandler
}

func NewAskUserTool() *AskUserTool {
	return &AskUserTool{}
}

// SetHandler 注入 UI 回调，CLI 和 TUI 各自实现。
func (t *AskUserTool) SetHandler(h AskUserHandler) {
	t.mu.Lock()
	t.handler = h
	t.mu.Unlock()
}

func (t *AskUserTool) Name() string  { return "ask_user" }
func (t *AskUserTool) Label() string { return "询问用户" }
func (t *AskUserTool) Description() string {
	return "当需求信息不足、且缺失信息会明显影响规划方向时，向用户提出 1-4 个结构化问题。每个问题必须包含 header、question 和 2-4 个选项；用户可选预设项，也可自由补充。返回结果是可直接阅读的中文摘要，格式类似：用户回答：[篇幅] 长篇；[重心] 剧情升级（补充：不要后宫）。只有在无法稳定判断篇幅、主线重心、关键约束或明确偏好时才使用；不要把能自行合理推断的问题都抛给用户。"
}

func (t *AskUserTool) Schema() map[string]any {
	option := schema.Object(
		schema.Property("label", schema.String("选项显示文本（1-5个词）")).Required(),
		schema.Property("description", schema.String("选项含义说明")).Required(),
	)
	question := schema.Object(
		schema.Property("question", schema.String("完整的问题文本")).Required(),
		schema.Property("header", schema.String("短标签（最多12字符）")).Required(),
		schema.Property("options", schema.Array("2-4个可选项", option)).Required(),
		schema.Property("multiSelect", schema.Bool("是否允许多选")),
	)
	return schema.Object(
		schema.Property("questions", schema.Array("1-4个问题", question)).Required(),
	)
}

type askUserArgs struct {
	Questions []Question `json:"questions"`
}

func (t *AskUserTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a askUserArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if err := validateQuestions(a.Questions); err != nil {
		return json.Marshal(fmt.Sprintf("参数校验失败: %s", err))
	}

	t.mu.RLock()
	h := t.handler
	t.mu.RUnlock()

	if h == nil {
		return json.Marshal("当前环境不支持交互式询问，请根据你的判断自行决策并继续。")
	}

	resp, err := h(ctx, a.Questions)
	if err != nil {
		return json.Marshal(fmt.Sprintf("用户交互失败: %s。请根据你的判断自行决策并继续。", err))
	}

	return json.Marshal(formatAnswers(a.Questions, resp))
}

func validateQuestions(questions []Question) error {
	if len(questions) == 0 {
		return fmt.Errorf("至少需要一个问题")
	}
	if len(questions) > 4 {
		return fmt.Errorf("最多4个问题，当前 %d 个", len(questions))
	}
	for i, q := range questions {
		if q.Question == "" {
			return fmt.Errorf("问题 %d: 问题文本不能为空", i+1)
		}
		if q.Header == "" {
			return fmt.Errorf("问题 %d: header 不能为空", i+1)
		}
		if utf8.RuneCountInString(q.Header) > 12 {
			return fmt.Errorf("问题 %d: header %q 超过12字符", i+1, q.Header)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf("问题 %d: 需要2-4个选项，当前 %d 个", i+1, len(q.Options))
		}
		for j, opt := range q.Options {
			if opt.Label == "" {
				return fmt.Errorf("问题 %d 选项 %d: label 不能为空", i+1, j+1)
			}
			if opt.Description == "" {
				return fmt.Errorf("问题 %d 选项 %d: description 不能为空", i+1, j+1)
			}
		}
	}
	return nil
}

func formatAnswers(questions []Question, resp *AskUserResponse) string {
	if resp == nil || len(resp.Answers) == 0 {
		return "用户未提供回答，请根据你的判断自行决策并继续。"
	}
	var parts []string
	for _, q := range questions {
		answer, ok := resp.Answers[q.Question]
		if !ok {
			continue
		}
		entry := fmt.Sprintf("[%s] %s", q.Header, answer)
		if note, hasNote := resp.Notes[q.Question]; hasNote {
			entry += "（补充：" + note + "）"
		}
		parts = append(parts, entry)
	}
	return fmt.Sprintf("用户回答：%s", strings.Join(parts, "；"))
}
