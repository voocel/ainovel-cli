package rip

import (
	"testing"
)

// TestNextActionAllBranches 表驱动验证 NextAction 的全部分支：从空库到完成的每个转换。
func TestNextActionAllBranches(t *testing.T) {
	tests := []struct {
		name string
		f    Facts
		want Action
	}{
		{
			name: "空库：无身份 → ingest",
			f:    Facts{LibraryReady: false},
			want: ActionIngest,
		},
		{
			name: "身份就绪但无边界 → bound",
			f:    Facts{LibraryReady: true, Bounded: false},
			want: ActionBound,
		},
		{
			name: "边界就绪、短篇、摘要未开始 → summarize（短篇跳过预览）",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormShort, ExpectedChapters: 5, SummarizedChapters: 0},
			want: ActionSummarize,
		},
		{
			name: "短篇、摘要进行中 → summarize",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormShort, ExpectedChapters: 8, SummarizedChapters: 3},
			want: ActionSummarize,
		},
		{
			name: "长篇、边界就绪、形式未定（灰区）→ 等待裁定",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormAmbiguous, FormResolved: false, ExpectedChapters: 50},
			want: ActionAwaitFormChoice,
		},
		{
			name: "长篇、灰区已裁定、预览缺失 → preview",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, FormResolved: true, ExpectedChapters: 50, Previewed: false},
			want: ActionPreview,
		},
		{
			name: "长篇、形式已定（long）、预览缺失 → preview",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: false},
			want: ActionPreview,
		},
		{
			name: "长篇、预览完成但未放行 → await preview",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: false},
			want: ActionAwaitPreviewAck,
		},
		{
			name: "长篇、预览已放行、摘要未开始 → summarize",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 0},
			want: ActionSummarize,
		},
		{
			name: "长篇、摘要进行中 → summarize",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 80},
			want: ActionSummarize,
		},
		{
			name: "摘要完成、聚合缺失 → aggregate",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 120, Aggregated: false},
			want: ActionAggregate,
		},
		{
			name: "聚合完成、角色设定缺失 → profile",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 120, Aggregated: true, Profiled: false},
			want: ActionProfile,
		},
		{
			name: "角色设定完成、报告缺失 → report",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 120, Aggregated: true, Profiled: true, Reported: false},
			want: ActionReport,
		},
		{
			name: "报告完成、文风缺失 → style",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 120, Aggregated: true, Profiled: true, Reported: true, Styled: false},
			want: ActionStyle,
		},
		{
			name: "全部工件就绪 → done",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 120, Previewed: true, PreviewAccepted: true, SummarizedChapters: 120, Aggregated: true, Profiled: true, Reported: true, Styled: true},
			want: ActionDone,
		},
		{
			name: "降级完成（有失败章节）仍判 done",
			f:    Facts{LibraryReady: true, Bounded: true, Form: FormLong, ExpectedChapters: 200, Previewed: true, PreviewAccepted: true, SummarizedChapters: 200, Aggregated: true, Profiled: true, Reported: true, Styled: true, FailedChapters: 3},
			want: ActionDone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextAction(tt.f)
			if got != tt.want {
				t.Errorf("NextAction(%+v) = %q, want %q", tt.f, got, tt.want)
			}
		})
	}
}

// TestNextActionIdempotent 验证在同一 Facts 下反复调用 NextAction 总是返回相同动作。
func TestNextActionIdempotent(t *testing.T) {
	f := Facts{
		LibraryReady: true, Bounded: true, Form: FormLong,
		ExpectedChapters: 80, Previewed: true, PreviewAccepted: true,
		SummarizedChapters: 50, // 摘要进行中
	}
	want := ActionSummarize
	for i := 0; i < 5; i++ {
		if got := NextAction(f); got != want {
			t.Errorf("iteration %d: NextAction = %q, want %q", i, got, want)
		}
	}
}

// TestDegraded 验证 Degraded() 判定。
func TestDegraded(t *testing.T) {
	tests := []struct {
		name         string
		failedChapters int
		want         bool
	}{
		{"无失败章节", 0, false},
		{"有 1 章失败", 1, true},
		{"有多章失败", 10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Facts{FailedChapters: tt.failedChapters}
			if got := f.Degraded(); got != tt.want {
				t.Errorf("Degraded() = %v, want %v", got, tt.want)
			}
		})
	}
}
