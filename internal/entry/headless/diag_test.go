package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/app/diag"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestDiagEnvironmentAndShare(t *testing.T) {
	api := bootstrap.NewDiagnostics(nil, "test-build")
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), api, []string{"diag"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var report diag.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Header.BuildVersion != "test-build" || report.Summary.State != "environment" || report.Header.Shareable {
		t.Fatalf("unexpected report: %+v", report)
	}
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	args := []string{"diag", "export", "--file", path}
	stdout.Reset()
	if err := Run(context.Background(), api, args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || !json.Valid(payload) {
		t.Fatalf("share report: %s %v", payload, err)
	}
	if err := Run(context.Background(), api, args, &stdout, &stderr); err == nil {
		t.Fatal("existing report overwritten")
	}
}

func TestDiagRejectsInvalidScopeAndArguments(t *testing.T) {
	api := bootstrap.NewDiagnostics(nil, "test")
	for _, args := range [][]string{
		{"--project", "missing-db"}, {"--run", "run"}, {"--operation", "task"},
		{"--event-after", "-1"}, {"--event-after", "1"}, {"--after", "task"},
		{"--project", "book", "--operation", "task", "--after", "task"},
		{"export"}, {"repair"}, {"--file", "unused"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Run(context.Background(), api, append([]string{"diag"}, args...), &stdout, &stderr); err == nil {
				t.Fatal("invalid request succeeded")
			}
			if stdout.Len() != 0 {
				t.Fatal("failed request emitted a report")
			}
		})
	}
}
