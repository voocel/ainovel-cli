package orchestrator

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/diag"
)

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

func diagActionKey(action diag.Action) string {
	if action.Fingerprint != "" {
		return fmt.Sprintf("%s|%s|%s", action.SourceRule, action.Kind, action.Fingerprint)
	}
	return fmt.Sprintf("%s|%s|%s|%s", action.SourceRule, action.Kind, action.Summary, action.Message)
}

func diagActionsToPolicyActions(actions []diag.Action) []policyAction {
	if len(actions) == 0 {
		return nil
	}

	out := make([]policyAction, 0, len(actions)*2)
	for _, action := range actions {
		key := diagActionKey(action)
		switch action.Kind {
		case diag.ActionEmitNotice:
			out = append(out, withDedupKey(policyAction{
				Kind:     actionEmitNotice,
				Category: "SYSTEM",
				Summary:  action.Summary,
				Level:    diagSeverityToLevel(action.Severity),
			}, "diag."+key+".notice"))
		case diag.ActionEnqueueFollowUp:
			if action.Message == "" {
				continue
			}
			out = append(out, withDedupKey(policyAction{
				Kind:    actionFollowUp,
				Message: action.Message,
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
