package userrules

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
)

// nil 模型 + 空规则目录：归一化全降级，但快照仍可产出（system_defaults 兜底）并落盘。
// LoadOptions{} 的两个目录为空串，RawFileSources 返回 nil，测试不触碰真实磁盘。
func newDegradedService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := store.NewStore(t.TempDir())
	return NewService(st, llmcontract.Model{}, rules.LoadOptions{}), st
}

func TestService_Build_DegradesButPersists(t *testing.T) {
	svc, st := newDegradedService(t)

	snap, err := svc.Build(t.Context(), "每章1200字，主角冷静克制")
	if err != nil {
		t.Fatalf("Build 不应报错（降级而非阻断）：%v", err)
	}
	if snap.Status != rules.StatusDegraded {
		t.Fatalf("无模型应降级，status=%q", snap.Status)
	}
	// system_defaults 始终兜底机械基线。
	if len(snap.Structured.FatigueWords) == 0 || len(snap.Structured.ForbiddenPhrases) == 0 {
		t.Fatalf("应保留 system_defaults 机械基线，got %+v", snap.Structured)
	}
	// 启动 prompt 降级为 raw preferences，原文不丢。
	if snap.Preferences == "" {
		t.Fatal("降级应把启动 prompt 原文记入 preferences")
	}

	// 已落盘：GetOrBuild 读回同一份而非重建。
	reloaded, err := st.UserRules.Load()
	if err != nil || reloaded == nil {
		t.Fatalf("快照应已落盘：err=%v snap=%v", err, reloaded)
	}
	if reloaded.Preferences != snap.Preferences {
		t.Fatal("落盘内容与返回不一致")
	}
}

func TestService_GetOrBuildInitializesMissingSnapshot(t *testing.T) {
	svc, st := newDegradedService(t)

	if cur, _ := st.UserRules.Load(); cur != nil {
		t.Fatal("初始应无快照")
	}
	snap, err := svc.GetOrBuild(t.Context())
	if err != nil {
		t.Fatalf("GetOrBuild 不应报错：%v", err)
	}
	if len(snap.Structured.FatigueWords) == 0 {
		t.Fatal("惰性生成应含 system_defaults")
	}
	if cur, _ := st.UserRules.Load(); cur == nil {
		t.Fatal("GetOrBuild 应顺带落盘")
	}
}

func TestService_AddRuntimeRule_PersistsAndReturnsCandidate(t *testing.T) {
	svc, st := newDegradedService(t)

	const text = "以后少用比喻"
	merged, cand, err := svc.AddRuntimeRule(t.Context(), text)
	if err != nil {
		t.Fatalf("AddRuntimeRule 不应报错：%v", err)
	}
	// 候选用于回显：无模型时降级，原文进 preferences。
	if !cand.Degraded {
		t.Fatal("无模型时本次候选应降级")
	}
	if cand.Preferences != text {
		t.Fatalf("候选应保留原文，got %q", cand.Preferences)
	}
	// 叠加后快照含该条且已落盘。
	if merged.Preferences == "" {
		t.Fatal("叠加后 preferences 不应为空")
	}
	reloaded, err := st.UserRules.Load()
	if err != nil || reloaded == nil {
		t.Fatalf("叠加后应落盘：err=%v", err)
	}
	if reloaded.Status != rules.StatusDegraded {
		t.Fatalf("含降级来源，status 应为 degraded，got %q", reloaded.Status)
	}
}

// alwaysRetryableProvider 恒返回 retryable 错误：llmretry 会一直退避重试，
// 只有 context 能终止它——这正是 issue #125 的卡死形态。
type alwaysRetryableProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *alwaysRetryableProvider) Name() string { return "test" }

func (p *alwaysRetryableProvider) Chat(context.Context, *litellm.Request) (*litellm.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil, rateLimitErr()
}

func (p *alwaysRetryableProvider) Stream(context.Context, *litellm.Request) (litellm.Stream, error) {
	return nil, rateLimitErr()
}

func (p *alwaysRetryableProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// issue #125 回归：provider 持续限流/不可用时，Build 必须由 context 终止并降级，
// 不得无限重试卡死开书流程。
func TestService_BuildStopsAtContextDeadline(t *testing.T) {
	st := store.NewStore(t.TempDir())
	provider := &alwaysRetryableProvider{}
	svc := NewService(st, rulesModel(t, provider, nil), rules.LoadOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	type result struct {
		snap *rules.Snapshot
		err  error
	}
	done := make(chan result, 1)
	go func() {
		snap, err := svc.Build(ctx, "每章1200字，主角冷静克制")
		done <- result{snap, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("超时应降级而非阻断开书：%v", got.err)
		}
		if got.snap.Status != rules.StatusDegraded {
			t.Fatalf("归一化超时应降级，status=%q", got.snap.Status)
		}
		if got.snap.Preferences == "" {
			t.Fatal("降级应保留启动 prompt 原文")
		}
		if provider.Calls() == 0 {
			t.Fatal("应至少尝试过一次请求")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Build 未受 context 约束，已卡死（issue #125）")
	}
}
