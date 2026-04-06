package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// restoreBudgetTokens is the maximum total token budget for the post-compact
// restore message. Sized to hold a typical chapter plan + outline + compressed
// character snapshots without re-stuffing the freshly compacted context.
const restoreBudgetTokens = 6000

// writerRestorePack holds pre-assembled context that the Writer needs after
// compression. It is refreshed by the orchestrator at key lifecycle points
// (chapter start, commit, recovery) and consumed by the PostSummaryHook as a
// pure in-memory injection — no I/O in the hook path.
type writerRestorePack struct {
	mu       sync.RWMutex
	plan     *domain.ChapterPlan
	outline  *domain.OutlineEntry
	snaps    []domain.CharacterSnapshot
	chapter  int
}

// Refresh loads the current chapter's context from store and caches it.
// Called by the orchestrator before each writing cycle or on recovery.
func (p *writerRestorePack) Refresh(s *store.Store) {
	if s == nil {
		p.Clear()
		return
	}
	progress, err := s.Progress.Load()
	if err != nil || progress == nil {
		p.Clear()
		return
	}
	ch := progress.CurrentChapter
	if progress.InProgressChapter > 0 {
		ch = progress.InProgressChapter
	}
	if ch <= 0 {
		p.Clear()
		return
	}

	plan, _ := s.Drafts.LoadChapterPlan(ch)
	outline, _ := s.Outline.GetChapterOutline(ch)
	if outline == nil {
		outline, _ = s.Outline.GetChapterFromLayered(ch)
	}
	snaps, _ := s.Characters.LoadLatestSnapshots()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.chapter = ch
	p.plan = plan
	p.outline = outline
	p.snaps = snaps
}

// Clear drops cached data (e.g., when switching chapters).
func (p *writerRestorePack) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plan = nil
	p.outline = nil
	p.snaps = nil
	p.chapter = 0
}

// Hook returns a PostSummaryHook that injects the cached restore pack.
// The hook performs no I/O — it only reads the in-memory pack under a read lock.
func (p *writerRestorePack) Hook() corecontext.PostSummaryHook {
	return func(_ context.Context, _ corecontext.SummaryInfo, _ []agentcore.AgentMessage) ([]agentcore.AgentMessage, error) {
		msg, ok := p.buildMessage(restoreBudgetTokens)
		if !ok {
			return nil, nil
		}
		return []agentcore.AgentMessage{msg}, nil
	}
}

// buildMessage assembles the restore message within the given token budget.
// Items are added in priority order: plan → outline → snapshots.
// Returns false if nothing to inject.
func (p *writerRestorePack) buildMessage(budgetTokens int) (agentcore.Message, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.plan == nil && p.outline == nil && len(p.snaps) == 0 {
		return agentcore.Message{}, false
	}

	type item struct {
		heading string
		data    any
	}
	// Priority order: plan first, outline second, snapshots last.
	candidates := []item{
		{"当前章节计划", p.plan},
		{"当前章节大纲", p.outline},
		{"角色快照", p.snaps},
	}

	var parts []string
	remaining := budgetTokens
	for _, c := range candidates {
		if c.data == nil {
			continue
		}
		// Skip empty slices
		if snaps, ok := c.data.([]domain.CharacterSnapshot); ok && len(snaps) == 0 {
			continue
		}
		b, err := json.Marshal(c.data)
		if err != nil {
			continue
		}
		tokens := corecontext.EstimateTokens(agentcore.UserMsg(string(b)))
		if tokens > remaining {
			// Try truncating (keep first N bytes that fit)
			if remaining > 100 {
				truncated := truncateJSONToTokens(b, remaining)
				parts = append(parts, fmt.Sprintf("## %s\n%s [已截断]", c.heading, truncated))
			}
			break // budget exhausted, skip lower-priority items
		}
		remaining -= tokens
		parts = append(parts, fmt.Sprintf("## %s\n%s", c.heading, string(b)))
	}

	if len(parts) == 0 {
		return agentcore.Message{}, false
	}

	text := "<post-compact-context>\n" + strings.Join(parts, "\n\n") + "\n</post-compact-context>"
	return agentcore.UserMsg(text), true
}

// truncateJSONToTokens keeps the first portion of JSON bytes that fits within
// the token budget. Simple byte-level truncation — the result may not be valid
// JSON, but it preserves the most important leading content (keys, early fields).
func truncateJSONToTokens(b []byte, budgetTokens int) string {
	// Rough: 1 token ≈ 4 bytes for ASCII-dominant JSON
	maxBytes := budgetTokens * 4
	if maxBytes >= len(b) {
		return string(b)
	}
	if maxBytes < 20 {
		maxBytes = 20
	}
	return string(b[:maxBytes])
}
