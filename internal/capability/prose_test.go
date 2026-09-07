package capability

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
)

var putChapterPath = prosePaths[prompt.ToolWorkspacePutChapter]
var replaceBlockPath = prosePaths[prompt.ToolWorkspaceReplaceBlock]

// feedAll 整段喂入并断言输出合法 UTF-8。
func feedAll(t *testing.T, path prosePath, full string) string {
	t.Helper()
	got := newProseExtractor(path).feed(full)
	if !utf8.ValidString(got) {
		t.Fatalf("whole feed output is not valid UTF-8: %q", got)
	}
	return got
}

// assertSplitEquivalence 是切分等价引理：任意两段切分与逐字节喂入
// 的拼接结果都必须与整段一致，且每次 feed 返回值都是合法 UTF-8。
func assertSplitEquivalence(t *testing.T, path prosePath, full, want string) {
	t.Helper()
	if got := feedAll(t, path, full); got != want {
		t.Fatalf("whole feed = %q, want %q", got, want)
	}
	for i := 0; i <= len(full); i++ {
		e := newProseExtractor(path)
		first, second := e.feed(full[:i]), e.feed(full[i:])
		if !utf8.ValidString(first) || !utf8.ValidString(second) {
			t.Fatalf("split at %d produced invalid UTF-8: %q + %q", i, first, second)
		}
		if got := first + second; got != want {
			t.Fatalf("split at byte %d = %q, want %q", i, got, want)
		}
	}
	e := newProseExtractor(path)
	var got strings.Builder
	for i := 0; i < len(full); i++ {
		piece := e.feed(full[i : i+1])
		if !utf8.ValidString(piece) {
			t.Fatalf("byte-wise feed at %d produced invalid UTF-8: %q", i, piece)
		}
		got.WriteString(piece)
	}
	if got.String() != want {
		t.Fatalf("byte-wise feed = %q, want %q", got.String(), want)
	}
}

func TestProseExtractorPutChapter(t *testing.T) {
	full := `{"key":"ch-1","expected_version":0,"chapter":{"id":"c1","plan_node_id":"p1",` +
		`"number":1,"title":"山野少年","blocks":[{"id":"b1","text":"少年在山野间奔跑。"},` +
		`{"id":"b2","text":"暮色四合，他停下了脚步。"}]}}`
	assertSplitEquivalence(t, putChapterPath, full,
		"少年在山野间奔跑。\n\n暮色四合，他停下了脚步。")
}

func TestProseExtractorReplaceBlockFieldOrders(t *testing.T) {
	assertSplitEquivalence(t, replaceBlockPath,
		`{"key":"ch-1","block_id":"b2","text":"改写后的段落。","expected_version":3}`,
		"改写后的段落。")
	assertSplitEquivalence(t, replaceBlockPath,
		`{"text":"正文在前。","key":"ch-1","block_id":"b1","expected_version":1}`,
		"正文在前。")
}

func TestProseExtractorEscapes(t *testing.T) {
	full := `{"text":"引\"号\\反\/斜\b\f\n\r\t与你好"}`
	assertSplitEquivalence(t, replaceBlockPath, full,
		"引\"号\\反/斜\b\f\n\r\t与你好")
}

func TestProseExtractorSurrogatePairs(t *testing.T) {
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"a😀b"}`, "a😀b")
	// 孤立高代理项后接普通字符/普通转义/闭引号 → U+FFFD。
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"x\ud83dy"}`, "x�y")
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"x\ud83d\n"}`, "x�\n")
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"x\ud83d"}`, "x�")
	// 孤立低代理项 → U+FFFD；连续两个高代理项 → 前一个 U+FFFD。
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"x\ude00y"}`, "x�y")
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"\ud83d😀"}`, "�😀")
}

func TestProseExtractorRawUTF8AcrossSplits(t *testing.T) {
	// 三字节中文与四字节 emoji 的原始 UTF-8 在任意字节处切断都不产生非法输出。
	assertSplitEquivalence(t, replaceBlockPath, `{"text":"中文😀混排"}`, "中文😀混排")
}

func TestProseExtractorSkipsDecoyFields(t *testing.T) {
	// title 值内嵌伪 JSON、错误深度的 text 键都不得进入正文。
	cases := []struct{ name, payload, want string }{
		{"title 内嵌伪结构",
			`{"chapter":{"title":"{\"text\":\"trap\"}","blocks":[{"id":"b1","text":"真"}]}}`, "真"},
		{"顶层 text 深度不符",
			`{"text":"trap","chapter":{"blocks":[{"id":"b1","text":"真"}]}}`, "真"},
		{"chapter.text 深度不符",
			`{"chapter":{"text":"trap","blocks":[{"id":"b1","text":"真"}]}}`, "真"},
		{"block 嵌套 meta.text 深度不符",
			`{"chapter":{"blocks":[{"meta":{"text":"trap"},"id":"b1","text":"真"}]}}`, "真"},
		{"blocks 元素里数组套 text",
			`{"chapter":{"blocks":[{"tags":["text","trap"],"text":"真"}]}}`, "真"},
		{"空数组是合法 JSON 不判死",
			`{"chapter":{"extra":[],"blocks":[{"nested":[[],[]],"text":"真"}]}}`, "真"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertSplitEquivalence(t, putChapterPath, c.payload, c.want)
		})
	}
}

func TestProseExtractorNonStringTargetIsSilent(t *testing.T) {
	// 目标位置的值不是字符串：零发射但不判死，后续合法正文照常提取。
	full := `{"chapter":{"blocks":[{"text":42},{"text":null},{"text":"真"}]}}`
	assertSplitEquivalence(t, putChapterPath, full, "真")
}

func TestProseExtractorMalformedInputNeverPanics(t *testing.T) {
	cases := []string{
		`{"text":"未闭合`,                        // 截断：已发射部分保留（正常流态）
		`{"text":"好"}}`,                       // 根后多余括号
		`{"text":"好"]`,                        // 括号失配
		`{"text":"好",]`,                       // 逗号后失配
		`{"text":bad}`,                        // 非法字面量起始
		`{"text":"x\q"}`,                      // 非法转义
		`{"text":"x\uZZZZ"}`,                  // 非法十六进制
		`{text:"好"}`,                          // 键未加引号
		`[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[`, // 超深
		"\x01\x02{",                           // 垃圾字节
	}
	for _, payload := range cases {
		e := newProseExtractor(replaceBlockPath)
		out := e.feed(payload)
		if !utf8.ValidString(out) {
			t.Fatalf("payload %q produced invalid UTF-8 %q", payload, out)
		}
		// 已判死则恒空；截断流的后续字节仍是字符串内容，只要求合法 UTF-8 不 panic。
		wasFailed := e.state == sFailed
		next := e.feed(`{"text":"后续"}`)
		if !utf8.ValidString(next) {
			t.Fatalf("follow-up produced invalid UTF-8 %q after %q", next, payload)
		}
		if wasFailed && next != "" {
			t.Fatalf("failed extractor emitted %q after %q", next, payload)
		}
	}
}

func TestProseExtractorTruncationKeepsEmitted(t *testing.T) {
	e := newProseExtractor(replaceBlockPath)
	if got := e.feed(`{"text":"写到一半`); got != "写到一半" {
		t.Fatalf("truncated stream = %q", got)
	}
}

func FuzzProseFeed(f *testing.F) {
	f.Add(`{"chapter":{"blocks":[{"id":"b1","text":"正文你😀"}]}}`, 7)
	f.Add(`{"text":"中文😀"}`, 3)
	f.Add("\xff\xfe{[", 1)
	f.Fuzz(func(t *testing.T, payload string, split int) {
		e := newProseExtractor(putChapterPath)
		if split < 0 {
			split = -split
		}
		if len(payload) > 0 {
			split %= len(payload) + 1
		} else {
			split = 0
		}
		a, b := e.feed(payload[:split]), e.feed(payload[split:])
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			t.Fatalf("invalid UTF-8 output: %q %q", a, b)
		}
	})
}

// trackerFeed 简化断言：喂一片增量，核对正文与中断宣告。
func trackerFeed(t *testing.T, tracker *proseTracker, tool, callID, delta, wantText string, wantStalled bool) {
	t.Helper()
	text, stalled := tracker.feed(tool, callID, delta)
	if text != wantText || stalled != wantStalled {
		t.Fatalf("feed(%q, %q, %q) = (%q, %v), want (%q, %v)",
			tool, callID, delta, text, stalled, wantText, wantStalled)
	}
}

func TestProseTrackerInterleavedCallsKeepIndependentState(t *testing.T) {
	tracker := newProseTracker()
	trackerFeed(t, tracker, "workspace_put_chapter", "call-a",
		`{"chapter":{"blocks":[{"text":"甲稿`, "甲稿", false)
	// B 调用交错进来（非正文工具）：不污染也不丢弃 A 的提取状态。
	trackerFeed(t, tracker, "authority_read", "call-b", `{"key":"x"}`, "", false)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-a", `继续"}]}}`, "继续", false)
	// 重试/二次落笔：新调用天然零残留。
	trackerFeed(t, tracker, "workspace_put_chapter", "call-c",
		`{"chapter":{"blocks":[{"text":"重来"}]}}`, "重来", false)
}

func TestProseTrackerFinishMessageClearsState(t *testing.T) {
	tracker := newProseTracker()
	trackerFeed(t, tracker, "workspace_put_chapter", "call-a",
		`{"chapter":{"blocks":[{"text":"写到一半`, "写到一半", false)
	trackerFeed(t, tracker, "authority_read", "call-b", `{"key`, "", false)
	// 消息结束：参数流必然终结，本轮全部调用状态清空（生命周期以消息为界）。
	tracker.finishMessage()
	if len(tracker.calls) != 0 {
		t.Fatalf("calls after finishMessage = %d, want 0", len(tracker.calls))
	}
	// 下一条消息的新调用从零开始，不受上一轮残留影响。
	trackerFeed(t, tracker, "workspace_put_chapter", "call-c",
		`{"chapter":{"blocks":[{"text":"新一轮"}]}}`, "新一轮", false)
}

func TestProseTrackerBuffersUnnamedPrefix(t *testing.T) {
	tracker := newProseTracker()
	// 首块不带工具名：先缓冲，名字到达后补喂出完整开头。
	trackerFeed(t, tracker, "", "", `{"chapter":{"blocks":[{"te`, "", false)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `xt":"开头`, "开头", false)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `继续`, "继续", false)
}

func TestProseTrackerNonProseToolStaysSilent(t *testing.T) {
	tracker := newProseTracker()
	trackerFeed(t, tracker, "", "", `{"key":"a`, "", false)
	trackerFeed(t, tracker, "workspace_put_review", "call-1", `","text":"审阅意见"}`, "", false)
	trackerFeed(t, tracker, "workspace_put_review", "call-1", `x`, "", false)
}

func TestProseTrackerPendingOverflowAnnouncesStall(t *testing.T) {
	tracker := newProseTracker()
	chunk := strings.Repeat("a", 4<<10)
	for i := 0; i < 4; i++ {
		trackerFeed(t, tracker, "", "", chunk, "", false)
	}
	// 超限后判明是正文工具：开头已弃、中途起播必错乱，宣告一次中断后整调用放弃。
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `zzz`, "", true)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `zzz`, "", false)
	// 超限后判明是非正文工具：与直播无关，不宣告。
	fresh := newProseTracker()
	for i := 0; i < 4; i++ {
		trackerFeed(t, fresh, "", "", chunk, "", false)
	}
	trackerFeed(t, fresh, "workspace_put_review", "call-1", `zzz`, "", false)
}

func TestProseTrackerParserFailureAnnouncesOnce(t *testing.T) {
	tracker := newProseTracker()
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1",
		`{"chapter":{"blocks":[{"text":"已出正文"}`, "已出正文", false)
	// 结构损坏判死：同片段可先出正文后中断由 execute 侧分两条事件承载，
	// 这里验证判死那一刻恰好宣告一次，之后恒静默。
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `]]`, "", true)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `{"text":"后续"}`, "", false)
	// 新调用重置宣告资格：恢复正常直播。
	trackerFeed(t, tracker, "workspace_put_chapter", "call-2",
		`{"chapter":{"blocks":[{"text":"新稿"}]}}`, "新稿", false)
}

func BenchmarkProseExtractorByteWise(b *testing.B) {
	var payload strings.Builder
	payload.WriteString(`{"chapter":{"blocks":[{"id":"b1","text":"`)
	for payload.Len() < 1<<20 {
		payload.WriteString(`模型逐字生成的正文，混入转义\n与你。`)
	}
	payload.WriteString(`"}]}}`)
	full := payload.String()
	b.ReportAllocs()
	b.SetBytes(int64(len(full)))
	for i := 0; i < b.N; i++ {
		e := newProseExtractor(putChapterPath)
		for j := 0; j < len(full); j += 64 {
			end := j + 64
			if end > len(full) {
				end = len(full)
			}
			_ = e.feed(full[j:end])
		}
	}
}
