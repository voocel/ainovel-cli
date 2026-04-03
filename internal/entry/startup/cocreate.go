package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

// CoCreateSession 承载共创模式的非 UI 状态。
type CoCreateSession struct {
	history     []orchestrator.CoCreateMessage
	draftPrompt string
	ready       bool
	streamReply string
}

func NewCoCreateSession(initial string) *CoCreateSession {
	return &CoCreateSession{
		history: []orchestrator.CoCreateMessage{
			{Role: "user", Content: strings.TrimSpace(initial)},
		},
	}
}

func (s *CoCreateSession) History() []orchestrator.CoCreateMessage {
	if s == nil {
		return nil
	}
	return append([]orchestrator.CoCreateMessage(nil), s.history...)
}

func (s *CoCreateSession) AppendUser(text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.history = append(s.history, orchestrator.CoCreateMessage{Role: "user", Content: text})
}

func (s *CoCreateSession) ApplyReply(reply orchestrator.CoCreateReply) {
	if s == nil {
		return
	}
	s.streamReply = ""
	if text := strings.TrimSpace(reply.Message); text != "" {
		s.history = append(s.history, orchestrator.CoCreateMessage{Role: "assistant", Content: text})
	}
	s.draftPrompt = strings.TrimSpace(reply.Prompt)
	s.ready = reply.Ready
}

func (s *CoCreateSession) ApplyDelta(text string) {
	if s == nil {
		return
	}
	s.streamReply = strings.TrimSpace(text)
}

func (s *CoCreateSession) StreamReply() string {
	if s == nil {
		return ""
	}
	return s.streamReply
}

func (s *CoCreateSession) DraftPrompt() string {
	if s == nil {
		return ""
	}
	return s.draftPrompt
}

func (s *CoCreateSession) Ready() bool {
	if s == nil {
		return false
	}
	return s.ready
}

func (s *CoCreateSession) CanStart() bool {
	return strings.TrimSpace(s.DraftPrompt()) != ""
}

func (s *CoCreateSession) InitialInput() string {
	if s == nil || len(s.history) == 0 {
		return ""
	}
	return strings.TrimSpace(s.history[0].Content)
}

func (s *CoCreateSession) BuildPlan() (Plan, error) {
	if s == nil || !s.CanStart() {
		return Plan{}, fmt.Errorf("cocreate draft prompt is required")
	}
	return Plan{
		Mode:        ModeCoCreate,
		DisplayName: "共创规划",
		StartPrompt: orchestrator.BuildStartPrompt(s.DraftPrompt()),
	}, nil
}
