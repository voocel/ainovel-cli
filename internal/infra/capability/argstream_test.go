package capability

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

var putChapterPath = chapterProse
var replaceBlockPath = blockProse

// proseExtractor 是只带一条正文路径的提取器：正文直播的既有契约在它上面验证。
func proseExtractor(path tapPath) *argExtractor { return newArgExtractor(tapTree(path, nil)) }

// proseOf 只取正文输出。
func proseOf(e *argExtractor, fragment string) string {
	text, _ := e.feed(fragment)
	return text
}

// feedAll 整段喂入并断言输出合法 UTF-8。
func feedAll(t *testing.T, path tapPath, full string) string {
	t.Helper()
	got := proseOf(proseExtractor(path), full)
	if !utf8.ValidString(got) {
		t.Fatalf("whole feed output is not valid UTF-8: %q", got)
	}
	return got
}

// assertSplitEquivalence 是切分等价引理：任意两段切分与逐字节喂入
// 的拼接结果都必须与整段一致，且每次 feed 返回值都是合法 UTF-8。
func assertSplitEquivalence(t *testing.T, path tapPath, full, want string) {
	t.Helper()
	if got := feedAll(t, path, full); got != want {
		t.Fatalf("whole feed = %q, want %q", got, want)
	}
	for i := 0; i <= len(full); i++ {
		e := proseExtractor(path)
		first, second := proseOf(e, full[:i]), proseOf(e, full[i:])
		if !utf8.ValidString(first) || !utf8.ValidString(second) {
			t.Fatalf("split at %d produced invalid UTF-8: %q + %q", i, first, second)
		}
		if got := first + second; got != want {
			t.Fatalf("split at byte %d = %q, want %q", i, got, want)
		}
	}
	e := proseExtractor(path)
	var got strings.Builder
	for i := 0; i < len(full); i++ {
		piece := proseOf(e, full[i:i+1])
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
	full := `{"key":"ch-1","chapter":{"id":"c1","plan_node_id":"p1",` +
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
		e := proseExtractor(replaceBlockPath)
		out := proseOf(e, payload)
		if !utf8.ValidString(out) {
			t.Fatalf("payload %q produced invalid UTF-8 %q", payload, out)
		}
		// 已判死则恒空；截断流的后续字节仍是字符串内容，只要求合法 UTF-8 不 panic。
		wasFailed := e.state == sFailed
		next := proseOf(e, `{"text":"后续"}`)
		if !utf8.ValidString(next) {
			t.Fatalf("follow-up produced invalid UTF-8 %q after %q", next, payload)
		}
		if wasFailed && next != "" {
			t.Fatalf("failed extractor emitted %q after %q", next, payload)
		}
	}
}

func TestProseExtractorTruncationKeepsEmitted(t *testing.T) {
	e := proseExtractor(replaceBlockPath)
	if got := proseOf(e, `{"text":"写到一半`); got != "写到一半" {
		t.Fatalf("truncated stream = %q", got)
	}
}

func FuzzProseFeed(f *testing.F) {
	f.Add(`{"chapter":{"blocks":[{"id":"b1","text":"正文你😀"}]}}`, 7)
	f.Add(`{"text":"中文😀"}`, 3)
	f.Add("\xff\xfe{[", 1)
	f.Fuzz(func(t *testing.T, payload string, split int) {
		e := proseExtractor(putChapterPath)
		if split < 0 {
			split = -split
		}
		if len(payload) > 0 {
			split %= len(payload) + 1
		} else {
			split = 0
		}
		a, b := proseOf(e, payload[:split]), proseOf(e, payload[split:])
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			t.Fatalf("invalid UTF-8 output: %q %q", a, b)
		}
	})
}

// trackerFeed 简化断言：喂一片增量，核对正文与中断宣告。
func trackerFeed(t *testing.T, tracker *argTracker, tool, callID, delta, wantText string, wantStalled bool) {
	t.Helper()
	text, _, stalled := tracker.feed(tool, callID, delta)
	if text != wantText || stalled != wantStalled {
		t.Fatalf("feed(%q, %q, %q) = (%q, %v), want (%q, %v)",
			tool, callID, delta, text, stalled, wantText, wantStalled)
	}
}

func TestProseTrackerInterleavedCallsKeepIndependentState(t *testing.T) {
	tracker := newArgTracker(streamSpecs(nil))
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
	tracker := newArgTracker(streamSpecs(nil))
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
	tracker := newArgTracker(streamSpecs(nil))
	// 首块不带工具名：先缓冲，名字到达后补喂出完整开头。
	trackerFeed(t, tracker, "", "", `{"chapter":{"blocks":[{"te`, "", false)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `xt":"开头`, "开头", false)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `继续`, "继续", false)
}

func TestProseTrackerNonProseToolStaysSilent(t *testing.T) {
	tracker := newArgTracker(streamSpecs(nil))
	trackerFeed(t, tracker, "", "", `{"key":"a`, "", false)
	trackerFeed(t, tracker, "workspace_put_review", "call-1", `","text":"审阅意见"}`, "", false)
	trackerFeed(t, tracker, "workspace_put_review", "call-1", `x`, "", false)
}

func TestProseTrackerPendingOverflowAnnouncesStall(t *testing.T) {
	tracker := newArgTracker(streamSpecs(nil))
	chunk := strings.Repeat("a", 4<<10)
	for i := 0; i < 4; i++ {
		trackerFeed(t, tracker, "", "", chunk, "", false)
	}
	// 超限后判明是正文工具：开头已弃、中途起播必错乱，宣告一次中断后整调用放弃。
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `zzz`, "", true)
	trackerFeed(t, tracker, "workspace_put_chapter", "call-1", `zzz`, "", false)
	// 超限后判明是非正文工具：与直播无关，不宣告。
	fresh := newArgTracker(streamSpecs(nil))
	for i := 0; i < 4; i++ {
		trackerFeed(t, fresh, "", "", chunk, "", false)
	}
	trackerFeed(t, fresh, "workspace_put_review", "call-1", `zzz`, "", false)
}

func TestProseTrackerParserFailureAnnouncesOnce(t *testing.T) {
	tracker := newArgTracker(streamSpecs(nil))
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
		e := proseExtractor(putChapterPath)
		for j := 0; j < len(full); j += 64 {
			end := j + 64
			if end > len(full) {
				end = len(full)
			}
			_, _ = e.feed(full[j:end])
		}
	}
}

// 规划提交里各类条目混排，字符串里夹着括号、引号与 \u 转义：任何切分都交出同样的条目。
const planPayload = `{"reason":"首次规划","compass":{"ending":"他终于寄出那封\"不该寄的信\"","scale_max":60},` +
	`"volumes":[{"volume":1,"title":"未寄出的信","summary":"邮差替亡者送信。\n他想起自己的过去。"}],` +
	`"arcs":[{"arc":1,"volume":1,"title":"雨夜","summary":"第一封 {信} 抵达]"}],` +
	`"chapters":[{"chapter":1,"arc":1,"title":"无人签收","summary":"没有邮戳的信"},{"chapter":2,"arc":1,"title":"雨夜来客"}],` +
	`"entities":[{"name":"陈渡","kind":"character","aliases":["邮差","\u9648\u6e21\u54e5"]},{"name":"镇邮局","kind":"location"}],` +
	`"facts":[{"subject":"陈渡","predicate":"state.job","value":"邮差"},{"subject":"钥匙","predicate":"foreshadow.origin","value":"来历不明","resolved":true}],` +
	`"confirm_facts":[{"subject":"陈渡","predicate":"state.age"}],` +
	`"remove_facts":[{"subject":"陈渡","predicate":"state.home"}]}`

var planLines = []liveLine{
	{sectionOutline, `故事罗盘 · 终局：他终于寄出那封"不该寄的信" · 篇幅上限 60 章`},
	{sectionOutline, "第 1 卷 · 未寄出的信 —— 邮差替亡者送信。 他想起自己的过去。"},
	{sectionOutline, "第 1 个故事弧 · 雨夜 —— 第一封 {信} 抵达]"},
	{sectionOutline, "第 1 章 · 无人签收 —— 没有邮戳的信"},
	{sectionOutline, "第 2 章 · 雨夜来客"},
	{sectionCanon, "陈渡（人物） 又名 邮差、陈渡哥"},
	{sectionCanon, "镇邮局（地点）"},
	{sectionCanon, "「陈渡」state.job：邮差"},
	{sectionCanon, "「钥匙」foreshadow.origin：来历不明（回收）"},
	{sectionCanon, "删除「陈渡」state.home"},
}

func feedLines(spec streamSpec, e *argExtractor, fragment string) []liveLine {
	text, items := e.feed(fragment)
	if text != "" {
		panic("item-only spec emitted prose: " + text)
	}
	return spec.lines(items)
}

func TestArgExtractorItemsSurviveAnySplit(t *testing.T) {
	spec := streamSpecs(nil)[prompt.ToolProposalSubmit]
	extractor := func() *argExtractor { return newArgExtractor(tapTree(spec.text, spec.itemPaths())) }
	check := func(name string, got []liveLine) {
		t.Helper()
		if !slices.Equal(got, planLines) {
			t.Fatalf("%s:\n got %q\nwant %q", name, got, planLines)
		}
	}
	check("whole", feedLines(spec, extractor(), planPayload))
	for i := 0; i <= len(planPayload); i++ {
		e := extractor()
		check(fmt.Sprintf("split at %d", i), append(feedLines(spec, e, planPayload[:i]), feedLines(spec, e, planPayload[i:])...))
	}
	e := extractor()
	var bytewise []liveLine
	for i := 0; i < len(planPayload); i++ {
		bytewise = append(bytewise, feedLines(spec, e, planPayload[i:i+1])...)
	}
	check("byte-wise", bytewise)
}

// 单条超出上界只丢这一条，之后的条目照常交出；结构损坏后恒静默。
func TestArgExtractorDropsOversizeItemAndStopsOnMalformedInput(t *testing.T) {
	spec := streamSpecs(nil)[prompt.ToolProposalSubmit]
	e := newArgExtractor(tapTree(spec.text, spec.itemPaths()))
	huge := strings.Repeat("长", maxTapItemSize)
	got := feedLines(spec, e, `{"chapters":[{"chapter":1,"title":"`+huge+`"},{"chapter":2,"title":"雨夜来客"}]`)
	if !slices.Equal(got, []liveLine{{sectionOutline, "第 2 章 · 雨夜来客"}}) {
		t.Fatalf("oversize item = %q", got)
	}
	if got := feedLines(spec, e, `]]{"chapters":[{"chapter":3,"title":"回信"}]}`); len(got) != 0 {
		t.Fatalf("failed extractor emitted %q", got)
	}
}

// 审阅意见与结论用故事语言写出：要求按任务输入写回原文，未知的要求保留 ID。
func TestReviewStreamsFindingsAndVerdict(t *testing.T) {
	specs := streamSpecs(&domainmodel.ReviewRangeInput{Requirements: []domainmodel.Requirement{{ID: "d-1", Text: "保持悬疑氛围，不要过早揭示门后人的身份"}}})
	tracker := newArgTracker(specs)
	_, findings, _ := tracker.feed(prompt.ToolWorkspacePutReview, "call-r",
		`{"key":"review","findings":[{"chapter":3,"severity":"blocking","note":"门后人的身份\n已被暗示","requirement":"d-1"},{"chapter":4,"severity":"note","note":"雨声意象重复"}]}`)
	_, verdict, _ := tracker.feed(prompt.ToolVerdictSubmit, "call-v",
		`{"status":"blocked","review_key":"review","checks":[{"id":"d-1","status":"violated","note":"第 3 章"},{"id":"d-9","status":"pending"}]}`)
	want := []liveLine{
		{sectionReview, "第 3 章 · 阻塞 —— 门后人的身份 已被暗示"},
		{sectionReview, "第 4 章 · 参考 —— 雨声意象重复"},
		{sectionVerdict, "未通过"},
		{sectionVerdict, "被违反 · 保持悬疑氛围，不要过早揭示门后人的身份 —— 第 3 章"},
		{sectionVerdict, "待定 · d-9"},
	}
	if got := append(findings, verdict...); !slices.Equal(got, want) {
		t.Fatalf("review lines:\n got %q\nwant %q", got, want)
	}
}

func TestToolDetailNamesTheTarget(t *testing.T) {
	cases := []struct{ tool, args, want string }{
		{prompt.ToolAuthorityRead, `{"chapter":3}`, "第 3 章"},
		{prompt.ToolAuthorityRead, `{"arc":2}`, "第 2 个故事弧"},
		{prompt.ToolAuthorityRead, `{"entity":"陈渡"}`, "「陈渡」"},
		{prompt.ToolWorkspaceRead, `{"key":"draft"}`, "draft"},
		{prompt.ToolWorkspacePutChapter, `{"key":"draft","chapter":{"title":"门后的声音","blocks":[{"id":"b1","text":"雨停了。"},{"id":"b2","text":"他没有敲门。"}]}}`, "《门后的声音》 · 10 字"},
		{prompt.ToolWorkspacePutChapter, `{"key":"ch5","chapter":{"number":5,"title":"回信","blocks":[{"id":"b1","text":"信"}]}}`, "第 5 章《回信》 · 1 字"},
		{prompt.ToolWorkspaceReplaceBlock, `{"key":"draft","block_id":"b2","text":"他敲了门。","expected_version":2}`, "段落 b2 · 5 字"},
		{prompt.ToolWorkspacePutReview, `{"key":"r","findings":[{"severity":"blocking"},{"severity":"note"}]}`, "2 条意见（阻塞 1）"},
		{prompt.ToolWorkspacePutReview, `{"key":"r","findings":[]}`, "0 条意见"},
		{prompt.ToolProposalSubmit, `{"reason":"完成第四章\n初稿"}`, "完成第四章 初稿"},
		{prompt.ToolVerdictSubmit, `{"status":"pass","review_key":"r"}`, "通过"},
		{prompt.ToolWorkspaceList, `{}`, ""},
		{prompt.ToolAuthorityRead, `not json`, ""},
	}
	for _, c := range cases {
		if got := toolDetail(c.tool, json.RawMessage(c.args)); got != c.want {
			t.Errorf("toolDetail(%s, %s) = %q, want %q", c.tool, c.args, got, c.want)
		}
	}
}

func FuzzArgItems(f *testing.F) {
	f.Add(planPayload, 17)
	f.Add(`{"chapters":[{"chapter":1,"title":"\ud83d"}]}`, 5)
	f.Add("\xff{[\"", 2)
	spec := streamSpecs(nil)[prompt.ToolProposalSubmit]
	f.Fuzz(func(t *testing.T, payload string, split int) {
		e := newArgExtractor(tapTree(spec.text, spec.itemPaths()))
		if split < 0 {
			split = -split
		}
		split %= len(payload) + 1
		for _, line := range append(feedLines(spec, e, payload[:split]), feedLines(spec, e, payload[split:])...) {
			if !utf8.ValidString(line.text) {
				t.Fatalf("invalid UTF-8 line %q", line.text)
			}
		}
	})
}
