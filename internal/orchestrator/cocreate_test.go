package orchestrator

import (
	"strings"
	"testing"
)

func TestParseCoCreateResponseSupportsCodeFence(t *testing.T) {
	reply, err := parseCoCreateResponse("```json\n{\"reply\":\"先把方向收一下。你更想突出悬疑还是情感？\",\"draft_prompt\":\"写一部都市悬疑小说，主角是一名女法医，重点突出案件推进与人物情感。\",\"ready\":false}\n```")
	if err != nil {
		t.Fatalf("parseCoCreateResponse: %v", err)
	}
	if reply.Message == "" || reply.Prompt == "" || reply.Ready {
		t.Fatalf("unexpected reply: %+v", reply)
	}
}

func TestBuildStartPromptWrapsUserPrompt(t *testing.T) {
	prompt := BuildStartPrompt("写一部都市悬疑小说，主角是一名女法医")
	if prompt == "" {
		t.Fatal("expected wrapped prompt")
	}
	if want := "[创作要求]"; !strings.Contains(prompt, want) {
		t.Fatalf("prompt missing %q: %s", want, prompt)
	}
}
