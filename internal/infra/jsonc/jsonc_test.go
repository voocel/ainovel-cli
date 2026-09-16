package jsonc_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/infra/jsonc"
)

type sample struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecodeAcceptsCommentsAndTrailingCommas(t *testing.T) {
	var got sample
	if err := jsonc.Decode([]byte("{\n  // 注释\n  \"name\": \"a\",\n  \"count\": 2,\n}\n"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "a" || got.Count != 2 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestDecodeRejectsUnknownFieldsAndBrokenSyntax(t *testing.T) {
	var got sample
	if err := jsonc.Decode([]byte(`{"name":"a","extra":true}`), &got); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := jsonc.Decode([]byte(`{"name":`), &got); err == nil {
		t.Fatal("broken syntax accepted")
	}
}

func TestDecodeFileReportsMissingFile(t *testing.T) {
	var got sample
	if err := jsonc.DecodeFile(filepath.Join(t.TempDir(), "missing.jsonc"), &got); err == nil {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "sample.jsonc")
	if err := os.WriteFile(path, []byte("{\"name\":\"file\"} // 尾注释\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := jsonc.DecodeFile(path, &got); err != nil || got.Name != "file" {
		t.Fatalf("decode file: %v %+v", err, got)
	}
}
