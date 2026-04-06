package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/orchestrator/action"
)

// ---------------------------------------------------------------------------
// Operational diagnostics
// ---------------------------------------------------------------------------

func (s *session) runOperationalDiag() {
	if s == nil || s.store == nil {
		return
	}

	report := diag.Analyze(s.store)
	current := make(map[string]struct{}, len(report.Actions))
	var pending []diag.Action
	for _, action := range report.Actions {
		key := diagActionKey(action)
		current[key] = struct{}{}
		if _, seen := s.diagActionKeys[key]; seen {
			continue
		}
		pending = append(pending, action)
	}
	s.diagActionKeys = current

	if len(pending) == 0 {
		return
	}
	s.executePolicyActions(diagActionsToPolicyActions(pending), s.emit)
}

func diagActionKey(a diag.Action) string {
	if a.Fingerprint != "" {
		return fmt.Sprintf("%s|%s|%s", a.SourceRule, a.Kind, a.Fingerprint)
	}
	return fmt.Sprintf("%s|%s|%s|%s", a.SourceRule, a.Kind, a.Summary, a.Message)
}

func diagActionsToPolicyActions(actions []diag.Action) []action.Action {
	if len(actions) == 0 {
		return nil
	}

	out := make([]action.Action, 0, len(actions)*2)
	for _, act := range actions {
		key := diagActionKey(act)
		switch act.Kind {
		case diag.ActionEmitNotice:
			out = append(out, action.WithDedupKey(action.Action{
				Kind:     action.KindEmitNotice,
				Category: "SYSTEM",
				Summary:  act.Summary,
				Level:    diagSeverityToLevel(act.Severity),
			}, "diag."+key+".notice"))
		case diag.ActionEnqueueFollowUp:
			if act.Message == "" {
				continue
			}
			out = append(out, action.WithDedupKey(action.Action{
				Kind:    action.KindFollowUp,
				Message: act.Message,
			}, "diag."+key+".followup"))
		}
	}
	return out
}

func diagSeverityToLevel(sev diag.Severity) string {
	switch sev {
	case diag.SevCritical:
		return "error"
	case diag.SevWarning:
		return "warn"
	default:
		return "info"
	}
}

// ---------------------------------------------------------------------------
// Evidence extraction & building
// ---------------------------------------------------------------------------

func extractContextBuildEvidence(raw json.RawMessage) *domain.ContextBuildEvidence {
	if len(raw) == 0 {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}

	evidence := domain.ContextBuildEvidence{
		Mode:               nestedString(payload, "memory_policy", "mode"),
		SummaryWindow:      nestedInt(payload, "memory_policy", "summary_window"),
		TimelineWindow:     nestedInt(payload, "memory_policy", "timeline_window"),
		LayeredSummaries:   nestedBool(payload, "memory_policy", "layered_summaries"),
		HasCurrentOutline:  hasNonEmptyValue(payload["current_chapter_outline"]),
		HasNextOutline:     hasNonEmptyValue(payload["next_chapter_outline"]),
		HasChapterPlan:     hasNonEmptyValue(payload["chapter_plan"]),
		HasChapterContract: hasNonEmptyValue(payload["chapter_contract"]),
		RecentSummaryCount: sliceLen(payload["recent_summaries"]) + sliceLen(payload["volume_summaries"]) + sliceLen(payload["arc_summaries"]),
		TimelineCount:      sliceLen(payload["timeline"]),
		ForeshadowCount:    sliceLen(payload["foreshadow_ledger"]),
		RelationshipCount:  sliceLen(payload["relationship_state"]),
		StateChangeCount:   sliceLen(payload["recent_state_changes"]),
		StoryThreadCount:   nestedSliceLen(payload, "selected_memory", "story_threads"),
		ReviewLessonCount:  nestedSliceLen(payload, "selected_memory", "review_lessons"),
		WarnSections:       warningScopes(payload["_warnings"]),
		TrimmedSections:    stringSlice(payload["_trimmed"]),
	}
	evidence.Chapter = nestedInt(payload, "current_chapter_outline", "chapter")
	if evidence.Chapter == 0 {
		evidence.Chapter = nestedInt(payload, "chapter_plan", "chapter")
	}
	if evidence.Mode == "" {
		if evidence.Chapter > 0 {
			evidence.Mode = "chapter"
		} else {
			evidence.Mode = "architect"
		}
	}
	return &evidence
}

func buildReviewOutcomeEvidence(review domain.ReviewEntry) domain.ReviewOutcomeEvidence {
	lowDimensions := make([]string, 0, len(review.Dimensions))
	failedDimensions := make([]string, 0, len(review.Dimensions))
	for _, dim := range review.Dimensions {
		switch dim.Verdict {
		case "warning", "fail":
			lowDimensions = append(lowDimensions, dim.Dimension)
		}
		if dim.Verdict == "fail" {
			failedDimensions = append(failedDimensions, dim.Dimension)
		}
	}

	criticalIssueTypes := make([]string, 0, len(review.Issues))
	topReasons := make([]string, 0, len(review.Issues)+len(failedDimensions))
	for _, issue := range review.Issues {
		topReasons = append(topReasons, issue.Type)
		if issue.Severity == "critical" || issue.Severity == "error" {
			criticalIssueTypes = append(criticalIssueTypes, issue.Type)
		}
	}
	topReasons = append(topReasons, failedDimensions...)

	return domain.ReviewOutcomeEvidence{
		Chapter:            review.Chapter,
		Verdict:            review.Verdict,
		ContractStatus:     review.ContractStatus,
		AffectedChapters:   append([]int(nil), review.AffectedChapters...),
		LowDimensions:      uniqueEvidenceStrings(lowDimensions),
		FailedDimensions:   uniqueEvidenceStrings(failedDimensions),
		CriticalIssueTypes: uniqueEvidenceStrings(criticalIssueTypes),
		TopReasonCodes:     uniqueEvidenceStrings(topReasons),
	}
}

func (s *session) contextEvidenceOwner(evidence *domain.ContextBuildEvidence) string {
	if evidence == nil {
		return ""
	}
	if s != nil && s.taskRT != nil {
		candidates := []string{"writer", "editor", "architect", "coordinator"}
		for _, owner := range candidates {
			if _, ok := s.taskRT.ActiveTask(owner); ok {
				return owner
			}
		}
	}
	if evidence.Mode == "chapter" {
		return "writer"
	}
	return "architect"
}

func contextEvidenceSummary(evidence domain.ContextBuildEvidence) string {
	if evidence.Chapter > 0 {
		return fmt.Sprintf("context.ch%02d", evidence.Chapter)
	}
	return "context.architect"
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func uniqueEvidenceStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func warningScopes(raw any) []string {
	items := stringSlice(raw)
	if len(items) == 0 {
		return nil
	}
	scopes := make([]string, 0, len(items))
	for _, item := range items {
		scope := item
		if idx := strings.Index(item, " 读取失败"); idx > 0 {
			scope = item[:idx]
		}
		scopes = append(scopes, scope)
	}
	return uniqueEvidenceStrings(scopes)
}

func nestedString(payload map[string]any, parent, key string) string {
	obj, ok := payload[parent].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := obj[key].(string)
	return value
}

func nestedInt(payload map[string]any, parent, key string) int {
	obj, ok := payload[parent].(map[string]any)
	if !ok {
		return 0
	}
	return intValue(obj[key])
}

func nestedBool(payload map[string]any, parent, key string) bool {
	obj, ok := payload[parent].(map[string]any)
	if !ok {
		return false
	}
	value, _ := obj[key].(bool)
	return value
}

func nestedSliceLen(payload map[string]any, parent, key string) int {
	obj, ok := payload[parent].(map[string]any)
	if !ok {
		return 0
	}
	return sliceLen(obj[key])
}

func hasNonEmptyValue(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return true
	}
}

func sliceLen(value any) int {
	if value == nil {
		return 0
	}
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case []string:
		return len(typed)
	case []int:
		return len(typed)
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
