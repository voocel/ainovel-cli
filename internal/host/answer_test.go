package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestInterventionEmitsFullAnswerAndPersistsDecision(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test-model"); err != nil {
		t.Fatal(err)
	}
	answer := strings.Repeat("这里是本书的分析。", 20) + "\n\n请使用 /reopen 以八十年大限开新卷。"
	payload, err := json.Marshal(map[string]any{
		"answer": answer, "reason": "只读回答用户问题",
		"rules": nil, "hold": nil, "reopen": nil, "dispatch": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg(string(payload)) }}
	h := &Host{
		store: st, engine: &engine{}, runCtx: t.Context(), events: make(chan Event, 8),
		models: &bootstrap.ModelSet{Default: bootstrap.NewSwappableModel("test", "test-model", model, nil)},
	}
	if err := h.doIntervention("分析这本书，说明接下来怎么操作", false); err != nil {
		t.Fatal(err)
	}
	found := 0
	for len(h.events) > 0 {
		ev := <-h.events
		if ev.Category != "ANSWER" {
			continue
		}
		found++
		if ev.Agent != "arbiter" || ev.Summary != answer || ev.Detail != answer {
			t.Fatalf("交给 UI 和日志的回答都必须完整: %+v", ev)
		}
	}
	if found != 1 {
		t.Fatalf("应单独发送一次可完整阅读的回答: %d", found)
	}
	records, err := st.Decisions.Recent(1)
	if err != nil || len(records) != 1 {
		t.Fatalf("裁定审计必须保留: %+v %v", records, err)
	}
	var saved struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(records[0].Decision, &saved); err != nil || saved.Answer != answer {
		t.Fatalf("审计中的原回答不能截断: %q %v", saved.Answer, err)
	}
}
