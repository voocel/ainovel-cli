package orchestrator

import "testing"

// --- jsonFieldExtractor tests ---

func TestFieldExtractor_SingleFeed(t *testing.T) {
	e := newFieldExtractor("content")
	got := e.Feed(`{"chapter":1,"content":"hello world","mode":"write"}`)
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestFieldExtractor_CrossDelta(t *testing.T) {
	e := newFieldExtractor("content")
	var result string
	for _, d := range []string{`{"chapter":1,"con`, `tent":"`, `第三章`, `\n\n夜幕低垂`, `","mode":"write"}`} {
		result += e.Feed(d)
	}
	if want := "第三章\n\n夜幕低垂"; result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestFieldExtractor_JSONEscape(t *testing.T) {
	e := newFieldExtractor("content")
	got := e.Feed(`{"content":"line1\nline2\t\"quoted\"\\end"}`)
	if want := "line1\nline2\t\"quoted\"\\end"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFieldExtractor_NoTargetField(t *testing.T) {
	e := newFieldExtractor("content")
	got := e.Feed(`{"chapter":1,"summary":"test","characters":["A"]}`)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFieldExtractor_ColonWithSpace(t *testing.T) {
	e := newFieldExtractor("content")
	got := e.Feed(`{"content" : "spaced"}`)
	if got != "spaced" {
		t.Errorf("got %q, want %q", got, "spaced")
	}
}

func TestFieldExtractor_Reset(t *testing.T) {
	e := newFieldExtractor("content")
	e.Feed(`{"content":"partial`)
	e.Reset()
	got := e.Feed(`{"content":"fresh"}`)
	if got != "fresh" {
		t.Errorf("got %q, want %q", got, "fresh")
	}
}

func TestFieldExtractor_TaskField(t *testing.T) {
	e := newFieldExtractor("task")
	got := e.Feed(`{"agent":"writer","task":"写第1章。核心事件：林尘目睹斗法"}`)
	if want := "写第1章。核心事件：林尘目睹斗法"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFieldExtractor_TaskCrossDelta(t *testing.T) {
	e := newFieldExtractor("task")
	var result string
	for _, d := range []string{`{"agent":"writer","ta`, `sk":"写第`, `1章"}`, `extra`} {
		result += e.Feed(d)
	}
	if want := "写第1章"; result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestFieldExtractor_Chinese(t *testing.T) {
	e := newFieldExtractor("content")
	var result string
	for _, d := range []string{`{"content":"`, `林远站在窗前，`, `望着远处的山峦。`, `"}`} {
		result += e.Feed(d)
	}
	if want := "林远站在窗前，望着远处的山峦。"; result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

// --- streamFilter tests ---

func TestStreamFilter_TextPassthrough(t *testing.T) {
	f := newStreamFilter("content")
	got := f.Feed("好的，我来加载上下文信息。")
	if want := ThinkingSep + "好的，我来加载上下文信息。"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStreamFilter_JSONExtractContent(t *testing.T) {
	f := newStreamFilter("content")
	got := f.Feed(`{"chapter":1,"content":"第一章 晨曦","mode":"write"}`)
	if want := "第一章 晨曦"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStreamFilter_JSONNoContent(t *testing.T) {
	f := newStreamFilter("content")
	got := f.Feed(`{"chapter":1,"title":"暗流","goal":"揭示线索"}`)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestStreamFilter_TextThenJSON(t *testing.T) {
	f := newStreamFilter("content")
	var result string
	result += f.Feed("规划完成，开始写作。")
	result += f.Feed(`{"chapter":1,"content":"正文","mode":"write"}`)
	if want := ThinkingSep + "规划完成，开始写作。正文"; result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestStreamFilter_JSONThenText(t *testing.T) {
	f := newStreamFilter("content")
	var result string
	result += f.Feed(`{"chapter":1,"summary":"摘要"}`)
	result += f.Feed("提交完成。")
	if want := ThinkingSep + "提交完成。"; result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestStreamFilter_CrossDeltaMixed(t *testing.T) {
	f := newStreamFilter("content")
	var result string
	deltas := []string{
		"好的，开始",
		"写作。",
		`{"chapter":1`,
		`,"content":"`,
		"第一章",
		"\n\n正文",
		`","mode":"write"}`,
		"已写入。",
	}
	for _, d := range deltas {
		result += f.Feed(d)
	}
	want := ThinkingSep + "好的，开始写作。第一章\n\n正文" + ThinkingSep + "已写入。"
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestStreamFilter_NestedBraces(t *testing.T) {
	f := newStreamFilter("content")
	got := f.Feed(`{"summary":"摘要","foreshadow":[{"type":"plant"}]}`)
	if got != "" {
		t.Errorf("got %q, want empty (nested JSON should be fully consumed)", got)
	}
}

func TestStreamFilter_BracesInString(t *testing.T) {
	f := newStreamFilter("content")
	got := f.Feed(`{"content":"文中有{大括号}和\"引号\""}后续文本`)
	want := "文中有{大括号}和\"引号\"" + ThinkingSep + "后续文本"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStreamFilter_Reset(t *testing.T) {
	f := newStreamFilter("content")
	f.Feed(`{"content":"半截`)
	f.Reset()
	got := f.Feed("重新开始")
	if want := ThinkingSep + "重新开始"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStreamFilter_ThinkingMarkerOnce(t *testing.T) {
	// 连续文本只在段首插入一次标记
	f := newStreamFilter("content")
	var result string
	result += f.Feed("好的")
	result += f.Feed("，继续")
	if want := ThinkingSep + "好的，继续"; result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}
