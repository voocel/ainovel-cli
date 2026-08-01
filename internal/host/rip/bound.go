package rip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// BoundaryDecision 是模型对单个 owned range 的边界判断。
type BoundaryDecision struct {
	UnitID    string `json:"unit_id"`
	Anchor    string `json:"anchor,omitempty"`
	Kind      string `json:"kind"` // chapter / group / front_matter / back_matter
	Title     string `json:"title,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const (
	kindChapter     = "chapter"
	kindGroup       = "group"
	kindFrontMatter = "front_matter"
	kindBackMatter  = "back_matter"
)

// boundaryBatch 是一次边界调用的结构化返回。
type boundaryBatch struct {
	Boundaries []BoundaryDecision `json:"boundaries"`
}

// ChapterSpan 是一个可拆解的章节：标题 + 归一化文本字节范围（含标题行）。
type ChapterSpan struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Start  int    `json:"start_byte"`
	End    int    `json:"end_byte"`
}

// MatterSpan 是卷/篇标题或明确的附属区域。
type MatterSpan struct {
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Start int    `json:"start_byte"`
	End   int    `json:"end_byte"`
}

// Boundaries 是全文覆盖校验通过的章节边界表，是拆解流程唯一的切片真值。
// 逐章摘要、聚合、文风锚点全部按它取正文；它的 Artifact.InputDigest 变化即令下游整体失配。
type Boundaries struct {
	Chapters  []ChapterSpan `json:"chapters"`
	Matter    []MatterSpan  `json:"matter,omitempty"`
	Uncertain []int         `json:"uncertain,omitempty"` // 标记 uncertain 的章节号，供预览提示
	Notes     []string      `json:"notes,omitempty"`     // 需人工核对的容错说明
}

// Content 返回第 i 个章节的归一化正文（含标题行）。
func (b *Boundaries) Content(normalized []byte, i int) string {
	c := b.Chapters[i]
	return string(normalized[c.Start:c.End])
}

// resolveBoundaries 把有序边界决策映射为经全文覆盖校验的 Boundaries。
// 纯函数：模型输出与代码校验分离，「某行是不是章标题」不由 Go 复判，但覆盖不变量必须成立。
func resolveBoundaries(normalized []byte, units []SourceUnit, decisions []BoundaryDecision) (*Boundaries, error) {
	if len(decisions) == 0 {
		return nil, fmt.Errorf("未识别到任何边界")
	}
	// 前置契约：units 必须按 (Line,Part) 数值序排列（禁止 ID 字典序）。
	for i := 1; i < len(units); i++ {
		if !unitLess(units[i-1], units[i]) {
			return nil, fmt.Errorf("SourceUnit 未按 (Line,Part) 数值序排列：%s 后接 %s", units[i-1].ID, units[i].ID)
		}
	}
	unitByID := make(map[string]SourceUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}

	type point struct {
		byte int
		d    BoundaryDecision
	}
	points := make([]point, 0, len(decisions))
	for i, d := range decisions {
		switch d.Kind {
		case kindChapter, kindGroup, kindFrontMatter, kindBackMatter:
		default:
			return nil, fmt.Errorf("边界[%d] kind 非法：%q", i, d.Kind)
		}
		at, err := resolveBoundaryByte(unitByID, d.UnitID, d.Anchor)
		if err != nil {
			return nil, err
		}
		points = append(points, point{byte: at, d: d})
	}
	// 模型偶发的乱序与重复是坐标纪律问题，Go 确定性修复而非终局否决：块间顺序由 owned
	// 区间不重叠保证，乱序只可能发生在块内，按字节稳定排序即恢复真实顺序，零信息损失。
	sort.SliceStable(points, func(i, j int) bool { return points[i].byte < points[j].byte })
	var notes []string
	uniq := points[:0]
	for _, p := range points {
		if n := len(uniq); n > 0 && uniq[n-1].byte == p.byte {
			if prev := uniq[n-1].d; prev.Kind != p.d.Kind || boundaryLabel(prev) != boundaryLabel(p.d) {
				notes = append(notes, fmt.Sprintf("边界 %q 与 %q 重合（byte %d），已保留前者",
					boundaryLabel(prev), boundaryLabel(p.d), p.byte))
			}
			continue
		}
		uniq = append(uniq, p)
	}
	points = uniq
	// 首个边界前的非空文本（书首简介/广告等，模型漏报起始边界）不终局否决：Go 确定性
	// 补一个 front_matter 兜住 [0, first)，记 Notes 交预览人工核对。
	if head := points[0].byte; head != 0 && strings.TrimSpace(string(normalized[:head])) != "" {
		notes = append(notes, fmt.Sprintf("起始 %d 字节文本（%s…）未被模型归属，已收为 front_matter，请核对是否漏切章节",
			head, snippet(string(normalized[:min(head, 48)]), 24)))
		points = append([]point{{byte: 0, d: BoundaryDecision{UnitID: units[0].ID, Kind: kindFrontMatter}}}, points...)
	}

	out := &Boundaries{Notes: notes}
	chapterNo := 0
	// absorb 把一段并入最近产出的 span（章节或附属区域皆可），无可并入时返回 false。
	absorb := func(end int) bool {
		ci, mi := len(out.Chapters)-1, len(out.Matter)-1
		switch {
		case ci >= 0 && (mi < 0 || out.Chapters[ci].Start > out.Matter[mi].Start):
			out.Chapters[ci].End = end
		case mi >= 0:
			out.Matter[mi].End = end
		default:
			return false
		}
		return true
	}
	for i, p := range points {
		start := p.byte
		if i == 0 {
			start = 0 // 首段吸收起始处的空白
		}
		end := len(normalized)
		if i+1 < len(points) {
			end = points[i+1].byte
		}
		title := strings.TrimSpace(p.d.Title)
		if title == "" {
			title = firstLine(normalized, p.byte, end)
		}
		switch p.d.Kind {
		case kindChapter:
			if strings.TrimSpace(bodyAfterTitle(normalized, p.byte, end)) == "" {
				// 真实网络小说源常见「已锁定/付费章节」占位：标题在、正文缺失。不整体失败——
				// 标题行并入前段（文本一字不丢），记入 Notes 由预览呈现。
				out.Notes = append(out.Notes,
					fmt.Sprintf("章节标题 %q 无正文（byte %d..%d），已并入前段（常见于锁定/付费占位章节）", title, start, end))
				if !absorb(end) {
					out.Matter = append(out.Matter, MatterSpan{Kind: kindFrontMatter, Title: title, Start: start, End: end})
				}
				continue
			}
			chapterNo++
			out.Chapters = append(out.Chapters, ChapterSpan{Number: chapterNo, Title: title, Start: start, End: end})
			if p.d.Uncertain {
				out.Uncertain = append(out.Uncertain, chapterNo)
			}
		default:
			out.Matter = append(out.Matter, MatterSpan{Kind: p.d.Kind, Title: title, Start: start, End: end})
		}
	}
	if chapterNo == 0 {
		return nil, fmt.Errorf("边界识别未产出任何章节（group 不计入章节）")
	}
	// 同名章节是「同章被误切」的确定性信号，只记 Notes 交预览人工核对——是否合并不由 Go 裁定。
	titleAt := make(map[string]int, len(out.Chapters))
	for _, c := range out.Chapters {
		key := squashSpace(c.Title)
		if first, ok := titleAt[key]; ok && key != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("第 %d 章与第 %d 章标题相同（%q），疑似同章被误切，请核对",
				c.Number, first, snippet(c.Title, 24)))
		} else {
			titleAt[key] = c.Number
		}
	}
	return out, nil
}

// squashSpace 去除全部空白，用于标题回显与同名比对——空白/装饰差异不构成语义差异。
func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// firstLine 返回 [start,end) 内首行去空白后的文本。
func firstLine(normalized []byte, start, end int) string {
	s := string(normalized[start:end])
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// bodyAfterTitle 返回 [start,end) 去掉首行（标题）后的正文。
// 多行章节标题独占首行，正文在其后；无换行的单行段（锚点切分场景）整段即正文。
func bodyAfterTitle(normalized []byte, start, end int) string {
	s := string(normalized[start:end])
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// planChunks 按字节预算把 units 切成互不重叠、完整覆盖的 owned 索引区间 [start,end)。
func planChunks(units []SourceUnit, budgetBytes int) [][2]int {
	if len(units) == 0 {
		return nil
	}
	if budgetBytes <= 0 {
		return [][2]int{{0, len(units)}}
	}
	var chunks [][2]int
	start := 0
	acc := 0
	for i, u := range units {
		size := u.EndByte - u.StartByte
		if acc > 0 && acc+size > budgetBytes {
			chunks = append(chunks, [2]int{start, i})
			start = i
			acc = 0
		}
		acc += size
	}
	chunks = append(chunks, [2]int{start, len(units)})
	return chunks
}

// buildProjection 组装一个 owned 区间的结构投影 payload（含少量上下文），模型只为 owned 返回边界。
// 同时返回投影内全部 unit_id 集合（owned + 上下文区），供输出校验区分幻觉与越界。
func buildProjection(units []SourceUnit, owned [2]int, contextMargin, ctxBudget int, guidance string) (string, map[string]bool) {
	lo, budget := owned[0], ctxBudget
	for lo > 0 && owned[0]-lo < contextMargin {
		if n := len(units[lo-1].Text); ctxBudget > 0 {
			if n > budget {
				break
			}
			budget -= n
		}
		lo--
	}
	hi, budget := owned[1], ctxBudget
	for hi < len(units) && hi-owned[1] < contextMargin {
		if n := len(units[hi].Text); ctxBudget > 0 {
			if n > budget {
				break
			}
			budget -= n
		}
		hi++
	}
	type projUnit struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	proj := struct {
		OwnedStart   string     `json:"owned_start"`
		OwnedEnd     string     `json:"owned_end"`
		Units        []projUnit `json:"units"`
		UserGuidance string     `json:"user_guidance,omitempty"`
	}{
		OwnedStart:   units[owned[0]].ID,
		OwnedEnd:     units[owned[1]-1].ID,
		UserGuidance: guidance,
	}
	ids := make(map[string]bool, hi-lo)
	for i := lo; i < hi; i++ {
		proj.Units = append(proj.Units, projUnit{ID: units[i].ID, Text: units[i].Text})
		ids[units[i].ID] = true
	}
	data, _ := json.MarshalIndent(proj, "", "  ")
	return string(data), ids
}

// boundInputDigest 覆盖边界动作实际消费的语义输入：归一化原文、用户指导、prompt 版本。
func boundInputDigest(normalizedDigest, guidance, promptVersion string) string {
	return Digest([]byte(strings.Join([]string{"bound", promptVersion, normalizedDigest, guidance}, "\x00")))
}

// boundChunkPath / boundChunkDigest：块级边界缓存的工件路径与身份。
// 身份绑定边界身份（原文+指导+prompt 版本）与块的 owned 单元范围——上游任何变化都使缓存自然失配。
func boundChunkPath(owned [2]int) string {
	return fmt.Sprintf("%s/chunk-%06d-%06d.json", dirBoundChunks, owned[0], owned[1])
}

func boundChunkDigest(identity, loID, hiID string) string {
	return Digest([]byte(strings.Join([]string{"bound-chunk", identity, loID, hiID}, "\x00")))
}

// Bound 对整份归一化原文做章节边界识别：逐 owned 区间调用模型，再全文覆盖校验。
// l 非空时逐块落盘边界缓存：单块可达数分钟，任何一块失败不应重付已完成块的调用。
func Bound(ctx context.Context, m callModel, systemPrompt string, normalized []byte, units []SourceUnit, guidance string, chunkBytes, contextMargin, maxTokens int, prof callProfile, l *Library, identity string) (*Boundaries, error) {
	chunks := planChunks(units, planningBudget(chunkBytes, systemPrompt, guidance))
	unitByID := make(map[string]SourceUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}
	var decisions []BoundaryDecision
	// chunk 处理一个 owned 区间：缓存命中零调用；输出被长度截断且区间可再分时对半缩块递归重试。
	var chunk func(owned [2]int, cur, total int) ([]BoundaryDecision, error)
	chunk = func(owned [2]int, cur, total int) ([]BoundaryDecision, error) {
		lo, hi := units[owned[0]], units[owned[1]-1]
		rel, want := boundChunkPath(owned), boundChunkDigest(identity, lo.ID, hi.ID)
		if l != nil {
			if art, err := readArtifact[boundaryBatch](l, rel); err == nil && art.InputDigest == want {
				return art.Payload.Boundaries, nil
			}
		}
		prof.step(cur, total, "识别第 %d/%d 块边界（%s..%s），已识别 %d 处...",
			cur, total, lo.ID, hi.ID, len(decisions))
		payload, projIDs := buildProjection(units, owned, contextMargin, max(chunkBytes/8, 4096), guidance)
		ownedIDs := make(map[string]bool, owned[1]-owned[0])
		for i := owned[0]; i < owned[1]; i++ {
			ownedIDs[units[i].ID] = true
		}
		v := chunkValidator{projIDs: projIDs, ownedIDs: ownedIDs, unitByID: unitByID,
			normalized: normalized, coverStart: owned[0] == 0}
		batch, err := callStructured[boundaryBatch](ctx, m, boundContract, systemPrompt, payload, maxTokens, prof, func(b *boundaryBatch) error {
			return v.validate(b.Boundaries)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) && owned[1]-owned[0] > 1 {
				mid := (owned[0] + owned[1]) / 2
				prof.step(0, 0, "块 %s..%s 边界输出被截断（章节过密），对半缩块重试", lo.ID, hi.ID)
				prof.logger().Warn("rip 边界输出截断，对半缩块", "chunk", lo.ID+".."+hi.ID)
				left, lerr := chunk([2]int{owned[0], mid}, cur, total)
				if lerr != nil {
					return nil, lerr
				}
				right, rerr := chunk([2]int{mid, owned[1]}, cur, total)
				if rerr != nil {
					return nil, rerr
				}
				return append(left, right...), nil
			}
			return nil, fmt.Errorf("识别区间 %s..%s 边界：%w", lo.ID, hi.ID, err)
		}
		// 上下文区边界归相邻块管辖（它会在自己的 owned 区间再报告一次），Go 直接裁掉：
		// 坐标纪律由代码执行，语义重试只留给真正的语义失败。
		kept := make([]BoundaryDecision, 0, len(batch.Boundaries))
		for _, bd := range batch.Boundaries {
			if ownedIDs[bd.UnitID] {
				kept = append(kept, bd)
			}
		}
		if n := len(batch.Boundaries) - len(kept); n > 0 {
			prof.step(0, 0, "已裁掉 %d 个上下文区多报的边界（归相邻块自行报告，非错误）", n)
		}
		if len(kept) > 0 {
			prof.step(0, 0, "模型识别出：%s", previewBoundaries(kept))
		}
		if l != nil {
			if err := writeArtifact(l, rel, want, boundaryBatch{Boundaries: kept}); err != nil {
				return nil, fmt.Errorf("落盘边界块 %s..%s：%w", lo.ID, hi.ID, err)
			}
		}
		return kept, nil
	}
	for ci, owned := range chunks {
		kept, err := chunk(owned, ci+1, len(chunks))
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, kept...)
	}
	out, err := resolveBoundaries(normalized, units, decisions)
	if err != nil {
		// 终局整合失败时块缓存已无价值：digest 恒匹配会让重跑零调用复读同一批边界、
		// 确定性复现同一失败。清缓存换取下次重新识别的模型机会。清除失败必须如实报告。
		hint := "块缓存已清除，重跑将重新识别边界"
		if l != nil {
			if cerr := l.clearDir(dirBoundChunks); cerr != nil {
				hint = fmt.Sprintf("块缓存清除失败：%v，重跑前请手动删除 %s/", cerr, dirBoundChunks)
			}
		}
		raw, _ := json.MarshalIndent(decisions, "", "  ")
		return nil, &errSemantic{Raw: string(raw), Err: fmt.Errorf("整合全书边界失败（%s）：%w", hint, err)}
	}
	return out, nil
}

// planningBudget 从输入预算扣除请求的结构性开销：系统提示与指导按实际长度扣除，剩余按
// 3/4 折算投影 JSON 包装的暴涨（id/引号/转义 ≈ 正文的 1/3）。下限 chunkBytes/4 防超长
// 提示把预算挤成负数；chunkBytes<=0 表示无预算（单块），原样透传。
func planningBudget(chunkBytes int, systemPrompt, guidance string) int {
	if chunkBytes <= 0 {
		return chunkBytes
	}
	b := (chunkBytes - len(systemPrompt) - len(guidance)) * 3 / 4
	return max(b, chunkBytes/4)
}

// boundaryLabel 给边界决策一个可读标识：标题优先，无标题回落到 kind@unit_id。
func boundaryLabel(d BoundaryDecision) string {
	if t := strings.TrimSpace(d.Title); t != "" {
		return t
	}
	return d.Kind + "@" + d.UnitID
}

// previewBoundaries 把一批边界决策压成一行标题预览（最多 3 个 + 计数），供面板回显。
func previewBoundaries(bs []BoundaryDecision) string {
	titles := make([]string, 0, 3)
	for _, b := range bs {
		titles = append(titles, snippet(boundaryLabel(b), 24))
		if len(titles) == 3 {
			break
		}
	}
	s := strings.Join(titles, " / ")
	if len(bs) > len(titles) {
		s += fmt.Sprintf("（共 %d 处）", len(bs))
	}
	return s
}

// chunkValidator 承载一次边界调用的调用期校验上下文：投影外 unit_id 是幻觉；owned 区
// 边界还须 kind 合法、anchor 可解析、同位不语义冲突；首块须有边界兜住文本起点。
// 这些坏值调用期不拦会随块落进缓存——digest 恒匹配，重跑零调用复读同一份坏数据。
type chunkValidator struct {
	projIDs, ownedIDs map[string]bool
	unitByID          map[string]SourceUnit
	normalized        []byte
	coverStart        bool // 首块：文本起点前的非空文本必须有边界归属
}

func (v chunkValidator) validate(bs []BoundaryDecision) error {
	seen := make(map[int]BoundaryDecision)
	first := -1
	for _, b := range bs {
		if b.UnitID == "" {
			return fmt.Errorf("边界缺 unit_id")
		}
		if !v.projIDs[b.UnitID] {
			return fmt.Errorf("边界 unit_id %q 不存在于本次投影中", b.UnitID)
		}
		if !v.ownedIDs[b.UnitID] {
			continue
		}
		switch b.Kind {
		case kindChapter, kindGroup, kindFrontMatter, kindBackMatter:
		default:
			return fmt.Errorf("边界 %s kind 非法：%q（只能是 chapter/group/front_matter/back_matter）", b.UnitID, b.Kind)
		}
		at, err := resolveBoundaryByte(v.unitByID, b.UnitID, b.Anchor)
		if err != nil {
			return err
		}
		// 标题回显：chapter/group 的标题必须真实存在于边界单元原文（忽略空白差异）——
		// 编造的标题在此被事实拦下。语义裁量仍归模型：真无标题规约的源可置 uncertain
		// 保留归纳标题；front/back matter 的描述性标题低风险，不核对。
		if (b.Kind == kindChapter || b.Kind == kindGroup) && !b.Uncertain {
			if t := squashSpace(b.Title); t != "" && !strings.Contains(squashSpace(v.unitByID[b.UnitID].Text), t) {
				return fmt.Errorf("边界 %s 的标题 %q 在该单元原文中找不到：若这里是上一章的延续正文，请不要为它设边界（由前文边界归属，boundaries 可为空）；若源文此处确实没有标题行、标题是你归纳的，请置 uncertain=true",
					b.UnitID, snippet(b.Title, 24))
			}
		}
		// 同位冲突（kind/标题不同）是语义问题，保留哪个不由 Go 裁定；完全相同的重复
		// 是机械冗余，放行后由 resolve 静默去重。
		if prev, ok := seen[at]; ok {
			if prev.Kind != b.Kind || boundaryLabel(prev) != boundaryLabel(b) {
				return fmt.Errorf("边界 %q 与 %q 落在同一位置（%s），语义冲突，请只保留正确的一个",
					boundaryLabel(prev), boundaryLabel(b), b.UnitID)
			}
		} else {
			seen[at] = b
		}
		if first < 0 || at < first {
			first = at
		}
	}
	if v.coverStart {
		head := first
		if head < 0 {
			head = len(v.normalized) // 首块一个 owned 边界都没报：全部起始文本未归属
		}
		if head > 0 && strings.TrimSpace(string(v.normalized[:head])) != "" {
			return fmt.Errorf("起始 %d 字节文本（%s…）未归属任何边界，请为文本开头补充边界（front_matter/chapter/group）",
				head, snippet(string(v.normalized[:min(head, 48)]), 24))
		}
	}
	return nil
}
