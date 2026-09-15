package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

// ShareReport is a closed projection: no raw IDs, errors, payloads or free text.
type ShareReport struct {
	FormatVersion             int              `json:"format_version"`
	RulesVersion              int              `json:"rules_version"`
	BuildVersion              string           `json:"build_version"`
	Platform                  string           `json:"platform"`
	SchemaVersion             int              `json:"schema_version,omitempty"`
	Shareable                 bool             `json:"shareable"`
	DatabaseIssue             string           `json:"database_issue,omitempty"`
	Selection                 string           `json:"selection"`
	Project                   string           `json:"project,omitempty"`
	Run                       string           `json:"run,omitempty"`
	State                     string           `json:"state"`
	Metrics                   ShareMetrics     `json:"metrics"`
	Findings                  []ShareFinding   `json:"findings"`
	Coverage                  []ShareCoverage  `json:"coverage"`
	Operations                []ShareOperation `json:"operations"`
	Events                    []ShareEvent     `json:"events"`
	Omitted                   int              `json:"omitted_items"`
	OmittedEvents             int              `json:"omitted_events"`
	OmittedTaskDetails        int              `json:"omitted_task_details"`
	OmittedEvidenceReferences int              `json:"omitted_evidence_references"`
}

type ShareMetrics struct {
	Operations        int            `json:"operations"`
	States            map[string]int `json:"states"`
	Attempts          int            `json:"attempts"`
	RetriedOperations int            `json:"retried_operations"`
	Events            int            `json:"events"`
}
type ShareFinding struct {
	Code      string          `json:"code"`
	Severity  string          `json:"severity"`
	Certainty string          `json:"certainty"`
	Operation string          `json:"operation,omitempty"`
	Attempt   int             `json:"attempt,omitempty"`
	Count     int             `json:"count"`
	Evidence  []ShareEvidence `json:"evidence,omitempty"`
}
type ShareEvidence struct {
	Source    string `json:"source"`
	Operation string `json:"operation"`
	Attempt   int    `json:"attempt"`
	Sequence  int64  `json:"sequence,omitempty"`
}
type ShareCoverage struct {
	Source string `json:"source"`
	Status string `json:"status"`
}
type ShareOperation struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Attempt         int    `json:"attempt"`
	FailureCode     string `json:"failure_code,omitempty"`
	Executor        string `json:"executor"`
	Configuration   string `json:"configuration"`
	CreatedOffsetMS int64  `json:"created_offset_ms"`
	UpdatedOffsetMS int64  `json:"updated_offset_ms"`
	EventBoundary   int64  `json:"event_boundary"`
}
type ShareEvent struct {
	Operation string `json:"operation"`
	Sequence  int64  `json:"sequence"`
	Attempt   int    `json:"attempt"`
	Kind      string `json:"kind"`
	OffsetMS  int64  `json:"offset_ms"`
	Usage     *Usage `json:"usage,omitempty"`
}

var buildPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$|^v1-dev$`)

func safeBuild(version string) string {
	if buildPattern.MatchString(version) {
		return version
	}
	return "unknown"
}

func allowed(value string, choices ...string) string {
	for _, choice := range choices {
		if value == choice {
			return value
		}
	}
	return "unknown"
}
func safeState(value string) string {
	return allowed(value, "queued", "running", "paused", "awaiting_approval", "succeeded", "failed", "cancelled", "stale", "waiting_user", "completed", "no_run", "environment")
}
func safeKind(value string) string {
	return allowed(value, "initialize_project", "develop_plan", "write_chapter", "revise_plan", "revise_canon", "rewrite_chapter", "rewrite_affected", "review_range", "generate_asset", "inspect_asset")
}

// Share reconstructs every string from a fixed vocabulary or a report-local alias.
func Share(report Report) ShareReport {
	s := ShareReport{FormatVersion: FormatVersion, RulesVersion: RulesVersion,
		Selection:     allowed(report.Header.Selection, "paged_details", "issue_context_and_stream_tails", "environment"),
		DatabaseIssue: databaseIssue(report.Header.DatabaseIssue),
		BuildVersion:  safeBuild(report.Header.BuildVersion), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		SchemaVersion: report.Header.SchemaVersion, Shareable: true, State: safeState(report.Summary.State),
		Metrics:  ShareMetrics{report.Metrics.Operations, map[string]int{}, report.Metrics.Attempts, report.Metrics.RetriedOperations, report.Metrics.Events},
		Findings: []ShareFinding{}, Coverage: []ShareCoverage{}, Operations: []ShareOperation{}, Events: []ShareEvent{}}
	if report.Header.Scope.ProjectID != "" {
		s.Project = "book-1"
	}
	s.OmittedEvents = max(0, report.Metrics.Events-len(report.Events))
	s.OmittedTaskDetails = max(0, report.Metrics.Operations-len(report.Operations))
	if report.Header.Scope.RunID != "" {
		s.Run = "run-1"
	}
	aliases := map[string]map[string]string{}
	alias := func(kind, raw string) string {
		if raw == "" {
			return ""
		}
		if aliases[kind] == nil {
			aliases[kind] = map[string]string{}
		}
		if v := aliases[kind][raw]; v != "" {
			return v
		}
		v := fmt.Sprintf("%s-%d", kind, len(aliases[kind])+1)
		aliases[kind][raw] = v
		return v
	}
	for key, count := range report.Metrics.States {
		s.Metrics.States[safeState(string(key))] += count
	}
	for _, op := range report.Operations {
		s.Operations = append(s.Operations, ShareOperation{
			ID: alias("task", op.ID), Kind: safeKind(string(op.Kind)), State: safeState(string(op.State)), Attempt: op.Attempt,
			FailureCode: allowed(string(op.FailureCode), "", "result_unknown"), Executor: alias("executor", op.Executor), Configuration: alias("config", op.ConfigDigest),
			CreatedOffsetMS: offset(op.CreatedAt, report.Header.CapturedAt), UpdatedOffsetMS: offset(op.UpdatedAt, report.Header.CapturedAt), EventBoundary: op.EventBoundary})
	}
	for _, f := range report.Findings {
		template := finding(f.Code, f.Count)
		if template.Code == "unknown" {
			s.Omitted++
			continue
		}
		shared := ShareFinding{Code: template.Code, Severity: template.Severity, Certainty: template.Certainty, Operation: alias("task", f.OperationID), Attempt: f.Attempt, Count: f.Count}
		s.OmittedEvidenceReferences += max(0, f.Count-len(f.Evidence))
		for _, e := range f.Evidence {
			source := allowed(e.Source, "operation.state", "operation.lease", "operation.failure_code", "operation.event_boundary", "event.tool_error")
			if source == "unknown" {
				s.Omitted++
				s.OmittedEvidenceReferences++
				continue
			}
			shared.Evidence = append(shared.Evidence, ShareEvidence{source, alias("task", e.OperationID), e.Attempt, e.Sequence})
		}
		s.Findings = append(s.Findings, shared)
	}
	for _, c := range report.Coverage {
		source := allowed(c.Source, "database", "statistics", "operations", "events", "tool_errors", "usage", "environment", "content_commits", "models", "findings")
		if source == "unknown" {
			s.Omitted++
			continue
		}
		s.Coverage = append(s.Coverage, ShareCoverage{source, allowed(c.Status, "complete", "partial", "unavailable", "not_collected", "truncated")})
	}
	for _, e := range report.Events {
		kind := allowed(e.Kind, "agent.run_ended", "agent.message_committed", "operation.claimed", "operation.priority_changed", "operation.transitioned", "operation.lease_expired", "workspace.artifact_written", "workspace.seeded", "semantic.compliance_checked", "semantic.compliance_failed")
		if kind == "unknown" || len(s.Events) == 200 {
			s.Omitted++
			s.OmittedEvents++
			continue
		}
		s.Events = append(s.Events, ShareEvent{alias("task", e.OperationID), e.Sequence, e.Attempt, kind, offset(e.CreatedAt, report.Header.CapturedAt), e.Usage})
	}
	return s
}

func offset(at, captured time.Time) int64 { return at.Sub(captured).Milliseconds() }

func (q *Query) ExportShare(ctx context.Context, request Request, path string) error {
	report, err := q.inspect(ctx, request, true)
	if err != nil {
		return err
	}
	return writeShare(ctx, path, Share(report))
}

func writeShare(ctx context.Context, path string, report ShareReport) error {
	if path == "" {
		return fmt.Errorf("diagnostic output path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".diag-*.tmp")
	if err != nil {
		return fmt.Errorf("create diagnostic file: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode diagnostic report: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync diagnostic file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close diagnostic file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Hard-link publication is atomic and refuses an existing destination.
	if err := os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("publish diagnostic file: %w", err)
	}
	return nil
}
