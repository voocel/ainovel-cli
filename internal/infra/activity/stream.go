package activity

import (
	"time"
	"unicode/utf8"
)

// OutputBlock is a display-only segment. Neither its text nor its lifetime is
// evidence that a candidate was approved or manuscript content was committed.
type OutputBlock struct {
	ID, Version                    uint64
	OperationID, CallID, TaskLabel string
	Kind                           Kind
	At                             time.Time
	Text                           []byte
	Truncated                      bool
	turn                           uint64
}

const (
	maxOutputBlocks     = 48
	maxOutputBytes      = 256 << 10
	maxOutputBlockBytes = 32 << 10
	outputSlack         = 8 << 10
)

func (s *Snapshot) foldOutput(event Event) {
	if event.Kind == TurnStart {
		s.outputTurn++
		return
	}
	if (event.Kind != Text && event.Kind != Thinking && event.Kind != Prose) || event.Text == "" {
		return
	}
	index := -1
	for i := len(s.Output) - 1; i >= 0; i-- {
		block := s.Output[i]
		if block.OperationID == event.OperationID && block.turn == s.outputTurn && block.Kind == event.Kind && block.CallID == event.CallID {
			index = i
			break
		}
		// Explanations and thinking retain chronological segments; interleaved
		// prose calls retain separate buffers keyed by their call identity.
		if event.Kind != Prose {
			break
		}
	}
	if index < 0 {
		s.Output = append(s.Output, OutputBlock{ID: s.Seq + 1, OperationID: event.OperationID, CallID: event.CallID,
			TaskLabel: event.TaskLabel, Kind: event.Kind, At: event.At, turn: s.outputTurn})
		index = len(s.Output) - 1
	}
	b := &s.Output[index]
	s.outputBytes -= len(b.Text)
	b.Text = append(b.Text, event.Text...)
	b.Version = s.Seq + 1
	if len(b.Text) > maxOutputBlockBytes+outputSlack {
		start := len(b.Text) - maxOutputBlockBytes
		for start < len(b.Text) && !utf8.RuneStart(b.Text[start]) {
			start++
		}
		b.Text = append([]byte(nil), b.Text[start:]...)
		b.Truncated = true
	}
	s.outputBytes += len(b.Text)
	for len(s.Output) > maxOutputBlocks || s.outputBytes > maxOutputBytes {
		s.outputBytes -= len(s.Output[0].Text)
		s.Output[0] = OutputBlock{}
		s.Output = s.Output[1:]
		s.OutputDropped++
	}
}
