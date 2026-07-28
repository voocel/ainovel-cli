package userrules

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// nil 模型 + 空规则目录：归一化全降级，但快照仍可产出（system_defaults 兜底）并落盘。
// LoadOptions{} 的两个目录为空串，RawFileSources 返回 nil，测试不触碰真实磁盘。
func newDegradedService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := store.NewStore(t.TempDir())
	return NewService(st, nil, rules.LoadOptions{}), st
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

func TestService_GetOrBuildMergesNewDefaultsIntoExistingSnapshot(t *testing.T) {
	svc, st := newDegradedService(t)
	existing := rules.Snapshot{
		Version: rules.SnapshotVersion,
		Status:  rules.StatusReady,
		Structured: rules.Structured{
			Genre:        "都市",
			FatigueWords: map[string]int{"不禁": 5},
		},
		Sources: []string{"system_defaults", "runtime_update"},
	}
	if err := st.UserRules.Save(&existing); err != nil {
		t.Fatalf("保存旧快照失败：%v", err)
	}

	merged, err := svc.GetOrBuild(t.Context())
	if err != nil {
		t.Fatalf("GetOrBuild 迁移旧快照失败：%v", err)
	}
	if merged.Structured.FatigueWords["——"] != 2 {
		t.Fatalf("应补入双破折号阈值，得到 %+v", merged.Structured.FatigueWords)
	}
	if merged.Structured.FatigueWords["不禁"] != 5 {
		t.Fatalf("不应覆盖用户已有阈值，得到 %d", merged.Structured.FatigueWords["不禁"])
	}
	persisted, err := st.UserRules.Load()
	if err != nil || persisted == nil || persisted.Structured.FatigueWords["——"] != 2 {
		t.Fatalf("合并结果应持久化：err=%v snap=%+v", err, persisted)
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
