package domain

import (
	"errors"
	"testing"
)

func TestDecodeStrictRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	type payload struct {
		ID string `json:"id"`
	}
	var value payload
	if err := DecodeStrict([]byte(`{"id":"a"}`), &value); err != nil || value.ID != "a" {
		t.Fatalf("valid payload = %#v, %v", value, err)
	}
	if err := DecodeStrict([]byte(`{"id":"a","extra":1}`), &value); err == nil {
		t.Fatal("unknown field accepted")
	}
	err := DecodeStrict([]byte(`{"id":"a"} {"id":"b"}`), &value)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("trailing value error = %v, want ErrInvalid", err)
	}
}

func TestDigestIsStableSHA256Hex(t *testing.T) {
	const want = "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae" // sha256("foo")
	if got := Digest([]byte("foo")); got != want {
		t.Fatalf("Digest = %s, want %s", got, want)
	}
	viaJSON, err := DigestJSON("foo")
	if err != nil || viaJSON != Digest([]byte(`"foo"`)) {
		t.Fatalf("DigestJSON = %s, %v; want digest of JSON encoding", viaJSON, err)
	}
}

func TestNewCreationRunPresetDigestFollowsStrategy(t *testing.T) {
	strategy := CreationRunStrategy{PlanWindowChapters: 3, ReviewCadence: ReviewPerPlanWindow, AutoRepairBudget: 2}
	preset, err := NewCreationRunPreset("quick", ApprovalAuto, strategy)
	if err != nil || preset.Source != "quick" || preset.Approval != ApprovalAuto || preset.Digest == "" {
		t.Fatalf("preset = %#v, %v", preset, err)
	}
	if err := preset.Validate(); err != nil {
		t.Fatalf("preset should validate: %v", err)
	}
	same, _ := NewCreationRunPreset("quick", ApprovalAuto, strategy)
	if same.Digest != preset.Digest {
		t.Fatal("identical inputs must produce identical digests")
	}
	strategy.AutoRepairBudget = 3
	changed, _ := NewCreationRunPreset("quick", ApprovalAuto, strategy)
	if changed.Digest == preset.Digest {
		t.Fatal("strategy change must change the preset digest")
	}
}
