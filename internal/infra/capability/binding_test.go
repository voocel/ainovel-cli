package capability

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/litellm/litellmtest"
)

// TestExecuteUsesRoleBindingAndRecordsRunStart：模型按 Worker 角色取绑定（D57），思考
// 强度按适配器能力折算后随请求发出，每次尝试首条事件记录实际使用的模型。
func TestExecuteUsesRoleBindingAndRecordsRunStart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	worker, err := prompt.BuiltinWorkerProfile("architect.design")
	if err != nil {
		t.Fatal(err)
	}
	plan := func() json.RawMessage {
		return json.RawMessage(`{"reason":"规划","volumes":[{"volume":1,"title":"卷","summary":"卷"}],` +
			`"arcs":[{"arc":1,"volume":1,"title":"弧","summary":"弧"}],"chapters":[{"chapter":1,"arc":1,"title":"第一章","summary":"开篇"}]}`)
	}
	task := json.RawMessage(`{"intent":"规划开篇","fixed_chapters":3}`)
	runID := createRuntimeTestRun(t, ctx, authorityStore, "book-plan", now)
	profile := seedRuntimeProfile(t, ctx, authorityStore, "book-plan", worker, task, 1)

	for _, tc := range []struct {
		provider, want string
	}{
		{provider: "openai", want: "high"},
		// MiMo 发不出强度：退回自动，而不是每次调用都报错。
		{provider: "mimo"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			operation := domainmodel.Operation{
				ID: "plan-" + tc.provider, Kind: domainmodel.OperationDevelopPlan,
				Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-plan"},
				State:  domainmodel.OperationQueued, RunID: runID,
				Snapshot: runtimeSnapshot(task, 1, domainmodel.ApprovalAuto, profile),
				Input:    task, CreatedAt: now, UpdatedAt: now,
			}
			if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
				t.Fatal(err)
			}
			if operation, err = authorityStore.ClaimOperationForExecutor(ctx, operation.ID, "worker-1", prompt.ExecutorIdentity, time.Minute, now); err != nil {
				t.Fatal(err)
			}
			bind := func(model string, p *litellmtest.Provider) models.Binding {
				binding, err := models.Bind(models.Config{Provider: tc.provider, Model: model, APIKey: "k", Thinking: "high"})
				if err != nil {
					t.Fatal(err)
				}
				// 换成脚本化的客户端，请求模板（含思考强度）沿用绑定的。
				binding.Chat.Client = testChat(p).Client
				return binding
			}
			defaultModel := litellmtest.New(callReply("plan", prompt.ToolProposalSubmit, plan()))
			architect := litellmtest.New(callReply("plan", prompt.ToolProposalSubmit, plan()))
			runtime := NewRuntime(authorityStore)
			if _, err := runtime.Execute(ctx, operation); !errors.Is(err, domainmodel.ErrInvalid) {
				t.Fatalf("unbound runtime executed: %v", err)
			}
			runtime.Bind(models.Bindings{
				Default: bind("default-model", defaultModel),
				Roles:   map[string]models.Binding{"architect": bind("architect-model", architect)},
			})
			if _, err := runtime.Execute(ctx, operation); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(defaultModel.Requests()) != 0 || len(architect.Requests()) == 0 {
				t.Fatalf("architect task must use the architect binding: default=%d architect=%d", len(defaultModel.Requests()), len(architect.Requests()))
			}
			sent := architect.Requests()[0]
			if got := sent.Thinking; sent.Model != "architect-model" || (tc.want == "") != (got == nil) || got != nil && got.Effort != tc.want {
				t.Fatalf("request model = %q thinking = %+v, want %q", sent.Model, got, tc.want)
			}
			events, err := authorityStore.ListOperationEvents(ctx, operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			var started struct {
				Role         string `json:"role"`
				Provider     string `json:"provider"`
				Model        string `json:"model"`
				ConfigDigest string `json:"config_digest"`
				Thinking     string `json:"thinking"`
			}
			if len(events) < 2 || events[1].Kind != "agent.run_started" || json.Unmarshal(events[1].Payload, &started) != nil {
				t.Fatalf("first agent event = %+v, want agent.run_started after the claim", events)
			}
			bound, _ := runtime.bindingFor("architect")
			if started.Role != "architect" || started.Model != "architect-model" || started.Provider != tc.provider || started.ConfigDigest != bound.Digest || started.Thinking != tc.want {
				t.Fatalf("run_started payload = %+v", started)
			}
			if events[1].IdempotencyKey != "agent-start:1" || events[1].Attempt != 1 {
				t.Fatalf("run_started keyed by attempt: %+v", events[1])
			}
		})
	}
}
