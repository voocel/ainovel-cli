package imp

import "testing"

func TestParseTaggedEnvelope_Basic(t *testing.T) {
	src := `prelude noise

=== PREMISE ===
# 书名
正文

=== CHARACTERS ===
[{"name":"A"}]

=== OUTLINE ===
[]
`
	env := parseTaggedEnvelope(src)
	if env["PREMISE"] == "" || env["CHARACTERS"] == "" || env["OUTLINE"] == "" {
		t.Fatalf("missing tags: %+v", env)
	}
	if env["PREMISE"] != "# 书名\n正文" {
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
