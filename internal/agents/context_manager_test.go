package agents

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

// TestCompactAtLeavesReserve 钉住 agents 侧的装配口径：CompactAt = 窗口 − 保留余量。
// 压缩行为本身（摘要/微压缩/恢复包）由 ctxpack 自己的测试覆盖。
func TestCompactAtLeavesReserve(t *testing.T) {
	if got := compactAt(0); got != 0 {
		t.Fatalf("未知窗口应返回 0（只在溢出时压缩），got %d", got)
	}
	for _, window := range []int{32000, 200000} {
		got := compactAt(window)
		want := window - bootstrap.CompactReserveTokens(window)
		if got != want {
			t.Fatalf("compactAt(%d) = %d, want %d", window, got, want)
		}
		if got <= 0 || got >= window {
			t.Fatalf("compactAt(%d) 必须落在 (0, window) 之间，got %d", window, got)
		}
	}
}

// TestRoleContextManagerAssembly 校验各角色的压缩档案真的接到了
// agentcore.Compactor：Architect/Editor 用角色专属提示词，Writer 用 ctxpack 默认
// 提示词并带上恢复包。
func TestRoleContextManagerAssembly(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}

	for name, profile := range map[string]roleContextProfile{
		"architect": architectContextProfile,
		"editor":    editorContextProfile,
	} {
		if profile.Prompts.System == "" || profile.Prompts.Summary == "" ||
			profile.Prompts.Update == "" || profile.Prompts.TurnPrefix == "" {
			t.Fatalf("%s 压缩档案缺少角色摘要提示词", name)
		}
		if c := newRoleContextManager(profile, st); c == nil {
			t.Fatalf("%s 应得到非 nil 的 agentcore.Compactor", name)
		}
	}

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(st)
	if c := newContextManager(contextManagerConfig{Store: st, Restore: restore}); c == nil {
		t.Fatal("writer 压缩器不应为 nil")
	}
}
