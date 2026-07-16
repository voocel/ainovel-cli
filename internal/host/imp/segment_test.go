package imp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestUnitLessNumericNotLexical(t *testing.T) {
	// 字典序会判 L900 > L1000、L1257.2 > L1800；数值序必须相反。
	if !unitLess(SourceUnit{Line: 900}, SourceUnit{Line: 1000}) {
		t.Fatal("L900 应 < L1000（数值序）")
	}
	if !unitLess(SourceUnit{Line: 1257, Part: 2}, SourceUnit{Line: 1800}) {
		t.Fatal("L1257.2 应 < L1800")
	}
	if !unitLess(SourceUnit{Line: 1257, Part: 1}, SourceUnit{Line: 1257, Part: 2}) {
		t.Fatal("同行 part 应按数值序")
	}
	if unitLess(SourceUnit{Line: 5}, SourceUnit{Line: 5}) {
		t.Fatal("相等不应 less")
	}
}

func TestBuildSourceUnitsRoundtrip(t *testing.T) {
	norm := []byte("第一章\n正文一\n\n第二章\n正文二")
	units := buildSourceUnits(norm, 0)
	// 拼回：每个 unit 文本 + 行间 '\n' 应还原归一化文本。
	var b strings.Builder
	for i, u := range units {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(u.Text)
		if u.Text != string(norm[u.StartByte:u.EndByte]) {
			t.Fatalf("unit %s 字节范围与文本不符", u.ID)
		}
	}
	if b.String() != string(norm) {
		t.Fatalf("拼回不符：%q", b.String())
	}
	if units[0].ID != "L1" || units[3].ID != "L4" {
		t.Fatalf("ID 不符：%s %s", units[0].ID, units[3].ID)
	}
}

func TestBuildSourceUnitsVirtualShard(t *testing.T) {
	// 一整行远超预算 → 拆多个虚拟 unit，边界在 UTF-8 字符边界。
	long := strings.Repeat("字", 100) // 每字 3 字节 = 300 字节
	units := buildSourceUnits([]byte(long), 30)
	if len(units) < 2 {
		t.Fatalf("超预算行应分片，得到 %d", len(units))
	}
	var b strings.Builder
	for _, u := range units {
		if u.Line != 1 || u.Part == 0 {
			t.Fatalf("虚拟分片应同 Line、Part>=1：%+v", u)
		}
		b.WriteString(u.Text) // 分片同一行，无换行分隔
	}
	if b.String() != long {
		t.Fatal("虚拟分片拼回丢字")
	}
}

func TestResolveBoundaryByteAnchor(t *testing.T) {
	units := []SourceUnit{{ID: "L1", Line: 1, StartByte: 0, EndByte: 10, Text: "楔子风起楔"}}
	m := map[string]SourceUnit{"L1": units[0]}
	if _, err := resolveBoundaryByte(m, "L1", "风起"); err != nil {
		t.Fatalf("唯一锚点应成功：%v", err)
	}
	if _, err := resolveBoundaryByte(m, "L1", "楔"); err == nil {
		t.Fatal("重复锚点应失败")
	}
	if _, err := resolveBoundaryByte(m, "L1", "缺失"); err == nil {
		t.Fatal("不存在锚点应失败")
	}
	if _, err := resolveBoundaryByte(m, "L9", ""); err == nil {
		t.Fatal("不存在 unit 应失败")
	}
}

func TestPlanChunksCoversWithoutGap(t *testing.T) {
	units := buildSourceUnits([]byte(strings.Repeat("行内容\n", 50)), 0)
	chunks := planChunks(units, 40)
	if len(chunks) < 2 {
		t.Fatalf("应分多块，得 %d", len(chunks))
	}
	// 无缝无重叠且完整覆盖。
	if chunks[0][0] != 0 || chunks[len(chunks)-1][1] != len(units) {
		t.Fatal("未完整覆盖")
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i][0] != chunks[i-1][1] {
			t.Fatalf("块 %d 与前块不相接：%v", i, chunks)
		}
	}
}

func segFixture() ([]byte, []SourceUnit) {
	norm := []byte("前言\n感谢阅读\n第一章 风起\n正文一\n卷二\n第二章 云涌\n正文二")
	return norm, buildSourceUnits(norm, 0)
}

func TestResolveSegmentationHappy(t *testing.T) {
	norm, units := segFixture()
	// L1 前言(front) / L3 第一章 / L5 卷二(group) / L6 第二章
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L5", Kind: kindGroup, Title: "卷二"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	}
	seg, err := resolveSegmentation(norm, units, decisions)
	if err != nil {
		t.Fatalf("覆盖校验应通过：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("章节数应为 2（group 不计），得 %d", len(seg.Chapters))
	}
	if seg.Chapters[0].Number != 1 || seg.Chapters[1].Number != 2 {
		t.Fatal("章节号应连续")
	}
	if !strings.Contains(seg.Content(norm, 0), "正文一") {
		t.Fatalf("章一正文不符：%q", seg.Content(norm, 0))
	}
	// 覆盖：首段(front_matter)从 0 起，末章覆盖到文本尾。
	if len(seg.Matter) == 0 || seg.Matter[0].Kind != kindFrontMatter || seg.Matter[0].Start != 0 {
		t.Fatalf("首段应为从 0 起的 front_matter：%+v", seg.Matter)
	}
	if seg.Chapters[len(seg.Chapters)-1].End != len(norm) {
		t.Fatal("末章应覆盖到文本尾")
	}
}

func TestResolveSegmentationRejections(t *testing.T) {
	norm, units := segFixture()
	cases := []struct {
		name string
		ds   []BoundaryDecision
	}{
		{"无章节", []BoundaryDecision{
			{UnitID: "L1", Kind: kindFrontMatter},
		}},
		{"非法kind", []BoundaryDecision{
			{UnitID: "L1", Kind: "verse"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := resolveSegmentation(norm, units, c.ds); err == nil {
				t.Fatalf("应被拒绝：%s", c.name)
			}
		})
	}
}

// TestResolveSegmentationReordersAndDedups 守护终局兜底的坐标纪律：块内模型偶发乱序按
// 字节排序确定性恢复（实测 319 个边界曾败于 1 处倒序，且块缓存会让失败确定性复现）；
// 同字节重复保留先出现者并记 Notes 交确认预览。
func TestResolveSegmentationReordersAndDedups(t *testing.T) {
	norm, units := segFixture()
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L1", Kind: kindChapter, Title: "开篇"}, // 乱序：位置在 L3 之前
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 重复"}, // 同字节重复
	})
	if err != nil {
		t.Fatalf("乱序/重复应被确定性修复而非拒绝：%v", err)
	}
	if len(seg.Chapters) != 3 {
		t.Fatalf("应得 3 章，得 %d：%+v", len(seg.Chapters), seg.Chapters)
	}
	if seg.Chapters[0].Title != "开篇" || seg.Chapters[0].Start != 0 {
		t.Fatalf("排序后首章应为位置最前的边界：%+v", seg.Chapters[0])
	}
	if seg.Chapters[2].Title != "第二章 云涌" {
		t.Fatalf("同字节重复应保留先出现者：%+v", seg.Chapters[2])
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "重合") {
		t.Fatalf("重复边界应记入 Notes：%v", seg.Notes)
	}
}

// TestResolveSegmentationAbsorbsLeadingText 守护起始漏报的确定性修复：书首简介/广告等非空
// 头部文本若被模型漏报边界，不得终局否决——漏报已进块缓存，否决会让重跑零调用确定性复现
// 失败。Go 补一个 front_matter 兜住 [0, first) 并记 Notes 交确认预览。
func TestResolveSegmentationAbsorbsLeadingText(t *testing.T) {
	norm, units := segFixture()
	// 只报了 L3 起的章节：L1/L2 非空文本无归属。
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	})
	if err != nil {
		t.Fatalf("起始未归属文本应被收为 front_matter 而非拒绝：%v", err)
	}
	if len(seg.Matter) != 1 || seg.Matter[0].Kind != kindFrontMatter || seg.Matter[0].Start != 0 {
		t.Fatalf("应补出从 0 起的 front_matter：%+v", seg.Matter)
	}
	if len(seg.Chapters) != 2 || seg.Chapters[0].Start == 0 {
		t.Fatalf("章节不应吞掉头部文本：%+v", seg.Chapters)
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "未被模型归属") {
		t.Fatalf("应记录人工核对说明：%v", seg.Notes)
	}
}

// TestChunkValidatorOwnedDiscipline 守护调用期校验的覆盖面：owned 区内的非法 kind、
// 坏 anchor、同位语义冲突、首块起始未归属必须在调用期带反馈重问——放行会随块进缓存，
// 终局 resolve 才发现时重跑零调用复读同一份坏数据；上下文区边界注定被裁掉，不为其重问；
// 同位完全相同的重复是机械冗余，放行后由 resolve 静默去重。
func TestChunkValidatorOwnedDiscipline(t *testing.T) {
	norm, units := segFixture()
	unitByID := map[string]SourceUnit{}
	proj, owned := map[string]bool{}, map[string]bool{}
	for _, u := range units {
		unitByID[u.ID] = u
		proj[u.ID] = true
	}
	owned["L1"], owned["L2"], owned["L3"] = true, true, true
	v := chunkValidator{projIDs: proj, ownedIDs: owned, unitByID: unitByID, normalized: norm}

	cases := []struct {
		name    string
		bs      []BoundaryDecision
		wantErr bool
	}{
		{"owned 非法 kind", []BoundaryDecision{{UnitID: "L1", Kind: "volume"}}, true},
		{"owned 坏 anchor", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Anchor: "不存在的锚"}}, true},
		{"owned 合法 anchor", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Anchor: "第一章"}}, false},
		{"上下文区非法 kind 不重问", []BoundaryDecision{{UnitID: "L6", Kind: "volume"}}, false},
		{"投影外幻觉 ID", []BoundaryDecision{{UnitID: "L99", Kind: kindChapter}}, true},
		{"同位语义冲突重问", []BoundaryDecision{
			{UnitID: "L1", Kind: kindChapter, Title: "开篇"},
			{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		}, true},
		{"同位完全重复放行", []BoundaryDecision{
			{UnitID: "L1", Kind: kindChapter, Title: "开篇"},
			{UnitID: "L1", Kind: kindChapter, Title: "开篇"},
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := v.validate(c.bs); (err != nil) != c.wantErr {
				t.Fatalf("wantErr=%v，得 %v", c.wantErr, err)
			}
		})
	}

	// 首块起始覆盖：L1/L2 非空却无边界归属 → 重问；补上起点边界后通过。
	vs := v
	vs.coverStart = true
	if err := vs.validate([]BoundaryDecision{{UnitID: "L3", Kind: kindChapter}}); err == nil {
		t.Fatal("首块起始未归属应重问")
	}
	if err := vs.validate([]BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter}, {UnitID: "L3", Kind: kindChapter},
	}); err != nil {
		t.Fatalf("起点已覆盖应通过：%v", err)
	}
	if err := vs.validate(nil); err == nil {
		t.Fatal("首块零边界应重问（全部起始文本未归属）")
	}
}

// TestSegmentClearsChunksOnResolveFailure 守护「缓存确定性复现」的总闸：终局整合失败时
// 块缓存已无价值（digest 恒匹配，重跑零调用复读同一批边界再死一次），必须清除换取下次
// 重新切分的模型机会；决策快照经 errSemantic 统一落 failures/。
func TestSegmentClearsChunksOnResolveFailure(t *testing.T) {
	norm, units := segFixture()
	// 模型把全书标成 front_matter：无章节，Go 无法确定性修复，终局失败。
	m := &mockModel{responses: []string{`{"boundaries":[{"unit_id":"L1","kind":"front_matter","title":"前言"}]}`}}
	w := &Workspace{dir: t.TempDir()}
	_, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, w, "id-1")
	if err == nil {
		t.Fatal("无章节应终局失败")
	}
	var se *errSemantic
	if !errors.As(err, &se) {
		t.Fatalf("终局失败应为 errSemantic（统一落 failures/），得 %T", err)
	}
	if _, statErr := os.Stat(filepath.Join(w.dir, dirSegmentChunks)); !os.IsNotExist(statErr) {
		t.Fatalf("终局失败后块缓存应被清除：%v", statErr)
	}
}

// mockModel 顺序返回预设响应，供 typed-call 契约测试。
// stops 可为每次调用指定 stop reason；缺省用 stop 或 StopReasonStop。
type mockModel struct {
	responses []string
	stops     []agentcore.StopReason
	i         int
	stop      agentcore.StopReason
}

func (m *mockModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	idx := m.i
	r := m.responses[idx%len(m.responses)]
	sr := m.stop
	if idx < len(m.stops) {
		sr = m.stops[idx]
	}
	if sr == "" {
		sr = agentcore.StopReasonStop
	}
	m.i++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(r)},
		StopReason: sr,
	}}, nil
}

// TestResolveSegmentationSingleLineChapters 守护 #9：无换行的单行段（锚点切分场景）整段即正文，
// 单行/单行多章小说不应被误判"正文为空"拒绝。
func TestResolveSegmentationSingleLineChapters(t *testing.T) {
	normalized := []byte("第一章甲的故事第二章乙的故事") // 整篇一行，无换行
	units := buildSourceUnits(normalized, 0)
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindChapter, Title: "第一章"},                // 无锚点 → byte 0
		{UnitID: "L1", Anchor: "第二章", Kind: kindChapter, Title: "第二章"}, // 行内锚点切出第二章
	}
	seg, err := resolveSegmentation(normalized, units, decisions)
	if err != nil {
		t.Fatalf("单行多章应被接受：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应切出 2 章，得 %d", len(seg.Chapters))
	}
	if got := seg.Content(normalized, 0); got != "第一章甲的故事" {
		t.Fatalf("首章正文范围不对：%q", got)
	}
}

func TestSegmentWithMockModel(t *testing.T) {
	norm, units := segFixture()
	resp := `{"boundaries":[
		{"unit_id":"L1","kind":"front_matter","title":"前言"},
		{"unit_id":"L3","kind":"chapter","title":"第一章 风起"},
		{"unit_id":"L5","kind":"group","title":"卷二"},
		{"unit_id":"L6","kind":"chapter","title":"第二章 云涌"}
	]}`
	m := &mockModel{responses: []string{resp}}
	seg, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, nil, "")
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应得 2 章，得 %d", len(seg.Chapters))
	}
}

// TestResolveSegmentationAbsorbsEmptyChapter 守护脏源容错：真实网络小说源常见"已锁定/付费章节"
// 占位标题（标题在、正文缺失）。这类边界不得整体失败——终局一票否决会浪费切分阶段全部模型调用；
// 占位段并入前段（文本一字不丢），记入 Notes 由确认预览呈现人工核对。
func TestResolveSegmentationAbsorbsEmptyChapter(t *testing.T) {
	norm, units := segFixture()
	// L5 "卷二" 行被模型标成章节标题：其 span [L5,L6) 无正文 → 并入第一章。
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L5", Kind: kindChapter, Title: "第五章 [本章节已锁定]"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	}
	seg, err := resolveSegmentation(norm, units, decisions)
	if err != nil {
		t.Fatalf("空正文占位章应被吸收而非整体失败：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应得 2 章（占位并入前段），得 %d", len(seg.Chapters))
	}
	if got := seg.Content(norm, 0); !strings.Contains(got, "卷二") {
		t.Fatalf("占位段应并入第一章（文本不丢）：%q", got)
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "已锁定") {
		t.Fatalf("应记录一条人工核对说明：%v", seg.Notes)
	}
	// 首点即空正文章节：无前段可并 → 落为 front_matter，同样不失败。
	seg, err = resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L1", Kind: kindChapter, Title: "占位"}, // [L1,L2) 单行标题无正文
		{UnitID: "L2", Kind: kindChapter, Title: "第一章"},
	})
	if err != nil {
		t.Fatalf("首点空正文应落为 front_matter：%v", err)
	}
	if len(seg.Matter) != 1 || seg.Matter[0].Kind != kindFrontMatter {
		t.Fatalf("首点空正文应为 front_matter：%+v", seg.Matter)
	}
}

// TestSegmentClipsContextBoundaries 守护坐标纪律的 Go 侧执行：模型在上下文区返回的边界
// 不触发语义重问（弱模型常 3 次耗尽拖垮整块），由代码直接裁掉——该边界归相邻块管辖，
// 相邻块会在自己的 owned 区间报告它，保留会造成跨块重复/乱序。
func TestSegmentClipsContextBoundaries(t *testing.T) {
	norm, units := segFixture()
	chunks := planChunks(units, planningBudget(40, "sys", "")) // 与 Segment 内部规划一致
	if len(chunks) < 2 {
		t.Fatalf("fixture 应分出至少 2 块，得 %d", len(chunks))
	}
	// 每块响应：owned 首单元一个章节边界；第一块额外夹带一个下一块首单元（上下文区）的边界。
	responses := make([]string, len(chunks))
	for ci, owned := range chunks {
		bs := fmt.Sprintf(`{"unit_id":%q,"kind":"chapter","title":"第%d章"}`, units[owned[0]].ID, ci+1)
		if ci == 0 {
			bs += fmt.Sprintf(`,{"unit_id":%q,"kind":"chapter","title":"越界"}`, units[chunks[1][0]].ID)
		}
		responses[ci] = `{"boundaries":[` + bs + `]}`
	}
	// 裁剪说明走普通进度回显（例行坐标纪律，非警示——warn 色会让用户误以为出错）。
	var clipNotes int
	prof := callProfile{progress: func(_, _ int, s string) {
		if strings.Contains(s, "裁掉") {
			clipNotes++
		}
	}}
	seg, err := Segment(context.Background(), &mockModel{responses: responses}, "sys", norm, units, "", 40, 2, 4096, prof, nil, "")
	if err != nil {
		t.Fatalf("上下文区边界应被裁掉而非失败：%v", err)
	}
	if len(seg.Chapters) != len(chunks) {
		t.Fatalf("应得 %d 章（越界边界不重复计入），得 %d", len(chunks), len(seg.Chapters))
	}
	if clipNotes != 1 {
		t.Fatalf("应回显 1 条裁剪说明，得 %d", clipNotes)
	}
}

// TestSegmentReusesChunkArtifacts 守护块级断点：切分逐块落盘边界缓存，重跑时 digest 匹配的块
// 零模型调用直接复用——切分是最昂贵阶段，任何一块失败不应重付已完成块（与 analyze/synthesize 同哲学）。
func TestSegmentReusesChunkArtifacts(t *testing.T) {
	norm, units := segFixture()
	chunks := planChunks(units, planningBudget(40, "sys", "")) // 与 Segment 内部规划一致
	responses := make([]string, len(chunks))
	for ci, owned := range chunks {
		responses[ci] = fmt.Sprintf(`{"boundaries":[{"unit_id":%q,"kind":"chapter","title":"第%d章"}]}`, units[owned[0]].ID, ci+1)
	}
	w := &Workspace{dir: t.TempDir()}
	m1 := &mockModel{responses: responses}
	seg1, err := Segment(context.Background(), m1, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-1")
	if err != nil {
		t.Fatalf("首跑：%v", err)
	}
	if m1.i != len(chunks) {
		t.Fatalf("首跑应调用 %d 次，得 %d", len(chunks), m1.i)
	}
	m2 := &mockModel{responses: responses}
	seg2, err := Segment(context.Background(), m2, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-1")
	if err != nil {
		t.Fatalf("重跑：%v", err)
	}
	if m2.i != 0 {
		t.Fatalf("digest 匹配的块应零调用复用，实际调用 %d 次", m2.i)
	}
	if len(seg2.Chapters) != len(seg1.Chapters) {
		t.Fatalf("复用结果应一致：%d != %d", len(seg2.Chapters), len(seg1.Chapters))
	}
	// 身份变化（换 prompt 版本/指导/源）→ 缓存自然失配，全部重做。
	m3 := &mockModel{responses: responses}
	if _, err := Segment(context.Background(), m3, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-2"); err != nil {
		t.Fatalf("身份变化重跑：%v", err)
	}
	if m3.i != len(chunks) {
		t.Fatalf("身份变化应全部重做（%d 次调用），得 %d", len(chunks), m3.i)
	}
}

// TestSegmentShrinksChunkOnTruncation 守护输出预算回路：大量短章节会让单块边界 JSON
// 超出可见输出（stop=length），必须对半缩块重试而非整体失败——与 analyze 缩批同哲学。
func TestSegmentShrinksChunkOnTruncation(t *testing.T) {
	norm, units := segFixture() // 7 个 unit，单块 [0,7)，mid=3
	left := `{"boundaries":[{"unit_id":"L1","kind":"chapter","title":"第一章"}]}`
	right := `{"boundaries":[{"unit_id":"L6","kind":"chapter","title":"第二章"}]}`
	m := &mockModel{
		responses: []string{`{"boundaries":[]}`, left, right},
		stops:     []agentcore.StopReason{agentcore.StopReasonLength}, // 首调截断，两个半块正常
	}
	seg, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, nil, "")
	if err != nil {
		t.Fatalf("截断应缩块重试而非失败：%v", err)
	}
	if m.i != 3 {
		t.Fatalf("应为 1 次截断 + 2 次半块调用，得 %d", m.i)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("缩块结果应完整覆盖（2 章），得 %d", len(seg.Chapters))
	}
}

// TestPlanningBudget 守护切分规划预算的结构性开销扣除：owned 正文只是请求的一部分。
func TestPlanningBudget(t *testing.T) {
	if got := planningBudget(0, "sys", "g"); got != 0 {
		t.Fatalf("无预算应透传，得 %d", got)
	}
	if got := planningBudget(1000, strings.Repeat("s", 100), strings.Repeat("g", 100)); got != 600 {
		t.Fatalf("(1000-200)*3/4 应为 600，得 %d", got)
	}
	if got := planningBudget(1000, strings.Repeat("s", 2000), ""); got != 250 {
		t.Fatalf("超长提示应触发下限 chunkBytes/4=250，得 %d", got)
	}
}

// TestBuildProjectionContextByteCap 守护上下文区字节上限：超长行虚拟分片（单片可达
// MaxUnitBytes）会吞掉输入预算，上下文只是参考信息，按字节上限收缩而非照单全收。
func TestBuildProjectionContextByteCap(t *testing.T) {
	_, units := segFixture()
	if _, ids := buildProjection(units, [2]int{2, 3}, 2, 1, ""); len(ids) != 1 || !ids["L3"] {
		t.Fatalf("字节上限应裁掉上下文单元，只剩 owned：%v", ids)
	}
	if _, ids := buildProjection(units, [2]int{2, 3}, 2, 0, ""); len(ids) != 5 {
		t.Fatalf("无字节上限时应含前后各 2 个上下文单元（共 5），得 %v", ids)
	}
}

func TestCallStructuredTruncation(t *testing.T) {
	m := &mockModel{responses: []string{`{"boundaries":[]}`}, stop: agentcore.StopReasonLength}
	_, err := callStructured[boundaryBatch](context.Background(), m, "s", "p", 16, callProfile{}, nil)
	var trunc *errTruncated
	if err == nil || !asTruncated(err, &trunc) {
		t.Fatalf("长度截断应返回 *errTruncated，得 %v", err)
	}
}

func asTruncated(err error, target **errTruncated) bool {
	t, ok := err.(*errTruncated)
	if ok {
		*target = t
	}
	return ok
}
