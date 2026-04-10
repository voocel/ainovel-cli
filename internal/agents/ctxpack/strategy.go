package ctxpack

import (
	"context"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/store"
)

const storeSummaryStrategyName = "store_summary"

type StoreSummaryCompactConfig struct {
	Store              *store.Store
	KeepRecentTokens   int
	SummaryTokenBudget int
}

type StoreSummaryCompactStrategy struct {
	store              *store.Store
	keepRecentTokens   int
	summaryTokenBudget int
}

func NewStoreSummaryCompact(cfg StoreSummaryCompactConfig) *StoreSummaryCompactStrategy {
	if cfg.KeepRecentTokens <= 0 {
		cfg.KeepRecentTokens = 20000
	}
	if cfg.SummaryTokenBudget <= 0 {
		cfg.SummaryTokenBudget = defaultStoreSummaryBudgetTokens
	}
	return &StoreSummaryCompactStrategy{
		store:              cfg.Store,
		keepRecentTokens:   cfg.KeepRecentTokens,
		summaryTokenBudget: cfg.SummaryTokenBudget,
	}
}

func (s *StoreSummaryCompactStrategy) Name() string { return storeSummaryStrategyName }

func (s *StoreSummaryCompactStrategy) Apply(ctx context.Context, _ []agentcore.AgentMessage, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	if budget.Window <= 0 || budget.Tokens <= budget.Threshold {
		return view, corecontext.StrategyResult{Name: s.Name()}, nil
	}
	return s.apply(ctx, view, budget)
}

func (s *StoreSummaryCompactStrategy) ForceApply(ctx context.Context, transcript []agentcore.AgentMessage, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	base := transcript
	if len(base) == 0 {
		base = view
	}
	return s.apply(ctx, base, budget)
}

func (s *StoreSummaryCompactStrategy) apply(_ context.Context, msgs []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	if s.store == nil || len(msgs) == 0 {
		return msgs, corecontext.StrategyResult{Name: s.Name()}, nil
	}

	summary, ok, err := buildWriterStoreSummaryText(s.store, s.summaryTokenBudget)
	if err != nil {
		return nil, corecontext.StrategyResult{Name: s.Name()}, err
	}
	if !ok {
		return msgs, corecontext.StrategyResult{Name: s.Name()}, nil
	}

	cut := findStoreSummaryCutPoint(msgs, s.keepRecentTokens)
	if cut.isSplitTurn && cut.turnStartIndex > 0 {
		cut.firstKeptIndex = cut.turnStartIndex
		cut.isSplitTurn = false
	}
	if cut.firstKeptIndex <= 0 || cut.firstKeptIndex >= len(msgs) {
		return msgs, corecontext.StrategyResult{Name: s.Name()}, nil
	}

	toKeep := append([]agentcore.AgentMessage(nil), msgs[cut.firstKeptIndex:]...)
	tokensBefore := corecontext.EstimateTotal(msgs)
	result := make([]agentcore.AgentMessage, 0, 1+len(toKeep))
	result = append(result, corecontext.ContextSummary{
		Summary:      summary,
		TokensBefore: tokensBefore,
		Timestamp:    time.Now(),
	})
	result = append(result, toKeep...)

	tokensAfter := corecontext.EstimateTotal(result)
	if tokensAfter >= tokensBefore {
		return msgs, corecontext.StrategyResult{Name: s.Name()}, nil
	}

	info := &corecontext.SummaryInfo{
		TokensBefore:   tokensBefore,
		TokensAfter:    tokensAfter,
		MessagesBefore: len(msgs),
		MessagesAfter:  len(result),
		CompactedCount: cut.firstKeptIndex,
		KeptCount:      len(toKeep),
		IsSplitTurn:    cut.isSplitTurn,
		SummaryLen:     len([]rune(summary)),
		Duration:       time.Millisecond,
	}
	if budget.Tokens > budget.Threshold && tokensAfter > budget.Threshold {
		info.Duration = 2 * time.Millisecond
	}

	return result, corecontext.StrategyResult{
		Applied:     true,
		TokensSaved: max(0, tokensBefore-tokensAfter),
		Name:        s.Name(),
		Info:        info,
	}, nil
}

type storeSummaryCutResult struct {
	firstKeptIndex int
	turnStartIndex int
	isSplitTurn    bool
}

func findStoreSummaryCutPoint(msgs []agentcore.AgentMessage, keepTokens int) storeSummaryCutResult {
	if len(msgs) == 0 {
		return storeSummaryCutResult{}
	}

	accumulated := 0
	cutIndex := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		accumulated += corecontext.EstimateTokens(msgs[i])
		if accumulated >= keepTokens {
			cutIndex = i
			break
		}
	}
	if cutIndex >= len(msgs) {
		return storeSummaryCutResult{}
	}

	for cutIndex < len(msgs) {
		msg := msgs[cutIndex]
		m, ok := msg.(agentcore.Message)
		if !ok {
			break
		}
		if m.Role == agentcore.RoleTool {
			cutIndex++
			continue
		}
		if m.Role == agentcore.RoleUser {
			break
		}
		if m.Role == agentcore.RoleAssistant && m.HasToolCalls() {
			cutIndex++
			for cutIndex < len(msgs) {
				next, ok := msgs[cutIndex].(agentcore.Message)
				if ok && next.Role == agentcore.RoleTool {
					cutIndex++
					continue
				}
				break
			}
			continue
		}
		break
	}
	if cutIndex >= len(msgs) {
		return storeSummaryCutResult{}
	}

	result := storeSummaryCutResult{firstKeptIndex: cutIndex}
	if m, ok := msgs[cutIndex].(agentcore.Message); !ok || m.Role != agentcore.RoleUser {
		for i := cutIndex - 1; i >= 0; i-- {
			if um, ok := msgs[i].(agentcore.Message); ok && um.Role == agentcore.RoleUser {
				result.turnStartIndex = i
				result.isSplitTurn = true
				break
			}
		}
	}
	return result
}
