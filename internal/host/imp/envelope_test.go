package imp

import "testing"

func TestParseTaggedEnvelope_Basic(t *testing.T) {
	src := `prelude noise

=== PREMISE ===
# Tên sách
Chính văn

=== CHARACTERS ===
[{"name":"A"}]

=== WORLD_RULES ===
[]
`
	env := parseTaggedEnvelope(src)
	if env["PREMISE"] == "" || env["CHARACTERS"] == "" || env["WORLD_RULES"] == "" {
		t.Fatalf("missing tags: %+v", env)
	}
	if env["PREMISE"] != "# Tên sách\nChính văn" {
		t.Errorf("premise body: %q", env["PREMISE"])
	}
}

func TestParseTaggedEnvelope_Empty(t *testing.T) {
	if got := parseTaggedEnvelope("no tags here"); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestRequireTags(t *testing.T) {
	env := map[string]string{"A": "x", "B": "  "}
	if err := requireTags(env, "A"); err != nil {
		t.Errorf("unexpected: %v", err)
	}
	if err := requireTags(env, "A", "B", "C"); err == nil {
		t.Error("want missing-tags error")
	}
}
