package headless

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestTerminalAskUserSingleSelect(t *testing.T) {
	handler := newTerminalAskUser(strings.NewReader("2\n"), &strings.Builder{})
	resp, err := handler.handle(context.Background(), []tools.Question{
		{
			Question: "你想要什么风格？",
			Header:   "风格",
			Options: []tools.Option{
				{Label: "热血", Description: "偏升级"},
				{Label: "悬疑", Description: "偏谜团"},
			},
		},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := resp.Answers["你想要什么风格？"]; got != "悬疑" {
		t.Fatalf("unexpected answer: %q", got)
	}
}

func TestTerminalAskUserCustomInput(t *testing.T) {
	handler := newTerminalAskUser(strings.NewReader("0\n不要感情线\n"), &strings.Builder{})
	resp, err := handler.handle(context.Background(), []tools.Question{
		{
			Question: "还有什么限制？",
			Header:   "限制",
			Options: []tools.Option{
				{Label: "黑暗", Description: "整体压抑"},
				{Label: "轻松", Description: "基调明快"},
			},
		},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := resp.Answers["还有什么限制？"]; got != "自定义" {
		t.Fatalf("unexpected answer: %q", got)
	}
	if got := resp.Notes["还有什么限制？"]; got != "不要感情线" {
		t.Fatalf("unexpected note: %q", got)
	}
}
