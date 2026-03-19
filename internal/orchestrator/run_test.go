package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestFinalizeSteerIfIdleClearsPendingState(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.InitProgress("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.SetFlow(domain.FlowSteering); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.SetPendingSteer("主角改成女性"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	finalizeSteerIfIdle(store)

	progress, err := store.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Flow != domain.FlowWriting {
		t.Fatalf("expected flow writing, got %s", progress.Flow)
	}

	runMeta, err := store.LoadRunMeta()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if runMeta.PendingSteer != "" {
		t.Fatalf("expected pending steer cleared, got %q", runMeta.PendingSteer)
	}
}

func TestFinalizeSteerIfIdleKeepsActiveFlow(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.InitProgress("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.SetPendingSteer("加入反转"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	finalizeSteerIfIdle(store)

	progress, err := store.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Flow != domain.FlowRewriting {
		t.Fatalf("expected flow rewriting, got %s", progress.Flow)
	}

	runMeta, err := store.LoadRunMeta()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if runMeta.PendingSteer != "加入反转" {
		t.Fatalf("expected pending steer preserved, got %q", runMeta.PendingSteer)
	}
}

func TestParseProgressSummaryIgnoresThinkingUpdate(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"agent":    "architect",
		"thinking": "好的，我已经获得了模板。",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	summary := parseProgressSummary(agentcore.Event{Result: result})
	if summary != "" {
		t.Fatalf("expected thinking update to be ignored, got %q", summary)
	}
}

func TestParseProgressSummaryKeepsToolProgress(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"agent": "writer",
		"tool":  "plan_chapter",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	summary := parseProgressSummary(agentcore.Event{Result: result})
	if summary != "writer → plan_chapter" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestNewModelSetUsesOpenRouterProvider(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "openrouter",
		ModelName: "stepfun/step-3.5-flash:free",
		Providers: map[string]bootstrap.ProviderConfig{
			"openrouter": {APIKey: "test-key", BaseURL: "https://openrouter.ai/api/v1"},
		},
	}
	ms, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}

	providerModel, ok := ms.Default.(interface{ ProviderName() string })
	if !ok {
		t.Fatalf("model does not expose provider name")
	}
	if provider := providerModel.ProviderName(); provider != "openrouter" {
		t.Fatalf("expected provider openrouter, got %q", provider)
	}
}

func TestDetermineRecoveryIncludesPlanningTierGuidance(t *testing.T) {
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    3,
		CompletedChapters: []int{1, 2},
		TotalWordCount:    2400,
		TotalChapters:     12,
	}
	runMeta := &domain.RunMeta{
		PlanningTier: domain.PlanningTierLong,
	}

	recovery := determineRecovery(progress, runMeta)
	if !strings.Contains(recovery.PromptText, "architect_long") {
		t.Fatalf("expected architect_long guidance, got %q", recovery.PromptText)
	}
	if !strings.Contains(recovery.PromptText, "分层大纲") {
		t.Fatalf("expected layered-outline guidance, got %q", recovery.PromptText)
	}
}

func TestPlanningTierGuidanceForMid(t *testing.T) {
	guidance := planningTierGuidance(&domain.RunMeta{PlanningTier: domain.PlanningTierMid})
	if !strings.Contains(guidance, "architect_mid") {
		t.Fatalf("expected architect_mid guidance, got %q", guidance)
	}
}

func TestExtractToolErrorTextFromJSONString(t *testing.T) {
	result, err := json.Marshal("save planning tier: permission denied")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	text := extractToolErrorText(result)
	if text != "save planning tier: permission denied" {
		t.Fatalf("unexpected error text: %q", text)
	}
}

func TestExtractToolErrorTextFromJSONObject(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"message": "parse outline JSON: invalid character",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	text := extractToolErrorText(result)
	if text != "parse outline JSON: invalid character" {
		t.Fatalf("unexpected error text: %q", text)
	}
}
