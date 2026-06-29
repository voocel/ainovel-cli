package domain

import "testing"

func TestCanTransitionPhase(t *testing.T) {
	tests := []struct {
		from Phase
		to   Phase
		want bool
	}{
		{from: "", to: PhaseInit, want: true},
		{from: PhaseInit, to: PhasePremise, want: true},
		{from: PhaseInit, to: PhaseOutline, want: true},
		{from: PhaseOutline, to: PhaseWriting, want: true},
		{from: PhaseWriting, to: PhaseComplete, want: true},
		{from: PhaseOutline, to: PhasePremise, want: false},
		{from: PhaseComplete, to: PhaseWriting, want: false},
	}
	for _, tt := range tests {
		if got := CanTransitionPhase(tt.from, tt.to); got != tt.want {
			t.Fatalf("CanTransitionPhase(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestCanTransitionFlow(t *testing.T) {
	tests := []struct {
		from FlowState
		to   FlowState
		want bool
	}{
		{from: "", to: FlowRewriting, want: true},
		{from: FlowWriting, to: FlowReviewing, want: true},
		{from: FlowReviewing, to: FlowPolishing, want: true},
		{from: FlowRewriting, to: FlowWriting, want: true},
		{from: FlowSteering, to: FlowRewriting, want: true},
		{from: FlowRewriting, to: FlowReviewing, want: false},
		{from: FlowPolishing, to: FlowReviewing, want: false},
	}
	for _, tt := range tests {
		if got := CanTransitionFlow(tt.from, tt.to); got != tt.want {
			t.Fatalf("CanTransitionFlow(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestExtractNovelNameFromPremise_Placeholder(t *testing.T) {
	cases := []struct {
		name    string
		premise string
		want    string
	}{
		{"真实Tên sách", "# 长夜将明\n\n## 题材", "长夜将明"},
		{"带Tên sách号", "# 《星河彼岸》\n## 题材", "星河彼岸"},
		{"占位-Tên sách", "# Tên sách\n## 题材", ""},
		{"占位-示例Tên sách", "# 《示例Tên sách》\n## 题材", ""},
		{"占位-实际Tên sách", "# 实际Tên sách\n## 题材", ""},
		{"首行非Tiêu đề", "Văn bản thuần第一行\n# Tên sách", ""},
	}
	for _, c := range cases {
		if got := ExtractNovelNameFromPremise(c.premise); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
