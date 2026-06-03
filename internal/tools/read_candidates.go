package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReadCandidatesTool 让 judge 读取某章所有 persona 的候选稿用于对比选优。
// 绑定 persona slug 列表，只返回已落盘的候选（缺席的跳过）。
type ReadCandidatesTool struct {
	store    *store.Store
	personas []string
}

func NewReadCandidatesTool(store *store.Store, personas []string) *ReadCandidatesTool {
	return &ReadCandidatesTool{store: store, personas: personas}
}

func (t *ReadCandidatesTool) Name() string  { return "read_candidates" }
func (t *ReadCandidatesTool) Label() string { return "读取候选稿" }
func (t *ReadCandidatesTool) Description() string {
	return "读取某章所有 persona 写手的候选稿（用于选优对比）。返回各候选的 persona、正文与字数。"
}

// 纯读工具，可并发。
func (t *ReadCandidatesTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *ReadCandidatesTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ReadCandidatesTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
	)
}

func (t *ReadCandidatesTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	type cand struct {
		Persona   string `json:"persona"`
		Content   string `json:"content"`
		WordCount int    `json:"word_count"`
	}
	out := make([]cand, 0, len(t.personas))
	for _, p := range t.personas {
		content, err := t.store.Contest.LoadCandidate(a.Chapter, p)
		if err != nil {
			return nil, fmt.Errorf("load candidate %s: %w", p, err)
		}
		if content == "" {
			continue // 该 persona 候选稿尚未落盘，跳过
		}
		out = append(out, cand{Persona: p, Content: content, WordCount: utf8.RuneCountInString(content)})
	}
	return json.Marshal(map[string]any{
		"chapter":    a.Chapter,
		"candidates": out,
		"count":      len(out),
	})
}
