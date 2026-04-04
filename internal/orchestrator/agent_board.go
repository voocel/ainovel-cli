package orchestrator

import (
	"sort"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// AgentSnapshot 是 TUI 展示使用的 Agent 状态投影。
type AgentSnapshot struct {
	Name      string
	State     string
	TaskID    string
	TaskKind  string
	Summary   string
	Tool      string
	Turn      int
	Context   AgentContextSnapshot
	UpdatedAt time.Time
}

type AgentContextSnapshot struct {
	Tokens          int
	ContextWindow   int
	Percent         float64
	Scope           string
	Strategy        string
	ActiveMessages  int
	SummaryMessages int
	CompactedCount  int
	KeptCount       int
}

type agentBoard struct {
	mu      sync.Mutex
	entries map[string]*AgentSnapshot
}

func newAgentBoard() *agentBoard {
	board := &agentBoard{
		entries: make(map[string]*AgentSnapshot, 4),
	}
	for _, name := range []string{"coordinator", "architect", "writer", "editor"} {
		board.entries[name] = &AgentSnapshot{
			Name:      name,
			State:     "idle",
			Summary:   "待命",
			UpdatedAt: time.Now(),
		}
	}
	return board
}

func (b *agentBoard) Snapshot() []AgentSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.entries))
	for name := range b.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]AgentSnapshot, 0, len(names))
	for _, name := range names {
		out = append(out, *b.entries[name])
	}
	return out
}

func (b *agentBoard) Start(name, taskID string, kind domain.TaskKind, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.ensure(name)
	entry.State = "running"
	entry.TaskID = taskID
	entry.TaskKind = string(kind)
	entry.Summary = summary
	entry.Tool = ""
	entry.Turn = 0
	entry.UpdatedAt = time.Now()
}

func (b *agentBoard) Update(name, tool, summary string, turn int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.ensure(name)
	if entry.State == "" || entry.State == "idle" {
		entry.State = "running"
	}
	if tool != "" {
		entry.Tool = tool
	}
	if summary != "" {
		entry.Summary = summary
	}
	if turn > 0 {
		entry.Turn = turn
	}
	entry.UpdatedAt = time.Now()
}

func (b *agentBoard) Idle(name, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.ensure(name)
	entry.State = "idle"
	entry.TaskID = ""
	entry.TaskKind = ""
	entry.Tool = ""
	entry.Turn = 0
	entry.Context = AgentContextSnapshot{}
	if summary != "" {
		entry.Summary = summary
	}
	entry.UpdatedAt = time.Now()
}

func (b *agentBoard) Fail(name, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.ensure(name)
	entry.State = "failed"
	entry.Summary = summary
	entry.Context = AgentContextSnapshot{}
	entry.UpdatedAt = time.Now()
}

func (b *agentBoard) ResetAll(summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.entries {
		entry.State = "idle"
		entry.TaskID = ""
		entry.TaskKind = ""
		entry.Tool = ""
		entry.Turn = 0
		entry.Context = AgentContextSnapshot{}
		entry.Summary = summary
		entry.UpdatedAt = time.Now()
	}
}

func (b *agentBoard) UpdateContext(name string, ctx AgentContextSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.ensure(name)
	entry.Context = ctx
	entry.UpdatedAt = time.Now()
}

func (b *agentBoard) ensure(name string) *AgentSnapshot {
	entry, ok := b.entries[name]
	if ok {
		return entry
	}
	entry = &AgentSnapshot{Name: name, State: "idle", Summary: "待命", UpdatedAt: time.Now()}
	b.entries[name] = entry
	return entry
}
