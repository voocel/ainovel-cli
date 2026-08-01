package tui

import "testing"

func TestParseRipArgsRetryFailed(t *testing.T) {
	opts, err := parseRipArgs([]string{"--book=测试书", "--retry-failed"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.BookName != "测试书" || !opts.RetryFailed {
		t.Fatalf("未保留失败章重试选项: %+v", opts)
	}
}
