// Package stylestat 对已写Chính văn做全书级风格统计，产出纯事实。
//
// 动机：弧内评审Cửa sổ（~10 章）对全书级模式固化天然失明——句式 tic 章均几十次、
// 章末形态同构、跨章复读，单章看每处都"正常"，只有全书统计能暴露。统计归代码
//（确定性、零幻觉），裁定归 LLM（editor 按数字判维度分，writer 据此自避免）。
package stylestat

import (
	"regexp"
	"sort"
	"strings"
)

// minChapters 少于此章数不出统计——样本太小，频率没有意义。
const minChapters = 5

// phraseWindow 动态短语挖掘只看最近 N 章：writer Cần避免的是"现在的口头禅"。
const phraseWindow = 20

// Input 统计Nhập。Chapters 按章号升序；Stopwords 为角色名等专有名词，
// 动态短语挖掘时Bỏ qua（出场人名天然高频，不是文风问题）。
type Input struct {
	Chapters  []string
	Titles    []string
	Stopwords []string
}

// Stats 全书风格统计Kết quả。所有字段都是事实计数，不含任何裁定或指令。
type Stats struct {
	Chapters          int            `json:"chapters"`
	Patterns          []PatternStat  `json:"patterns,omitempty"`
	TopPhrases        []PhraseStat   `json:"top_phrases,omitempty"`
	RepeatedSentences []SentenceStat `json:"repeated_sentences,omitempty"`
	Ending            EndingStat     `json:"ending"`
	OpeningTimeRate   float64        `json:"opening_time_rate"`
	TitleFormats      *TitleStat     `json:"title_formats,omitempty"`
}

// PatternStat 固定句式模式类的全书计数（通用 AI 文风 tic）。
type PatternStat struct {
	Name       string  `json:"name"`
	Total      int     `json:"total"`
	PerChapter float64 `json:"per_chapter"`
}

// PhraseStat 最近 phraseWindow 章内挖掘出的高频短语。
type PhraseStat struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

// SentenceStat 跨章逐字重复的长句（复读交代的直接证据）。
type SentenceStat struct {
	Text     string `json:"text"`
	Chapters int    `json:"chapters"`
	Count    int    `json:"count"`
}

// EndingStat 章末行形态分布。短结尾本身合法，全书同构才是问题。
type EndingStat struct {
	ShortRatio  float64 `json:"short_ratio"`
	MedianRunes int     `json:"median_runes"`
}

// TitleStat ChươngTiêu đề「第N章」前缀混用计数（混用=机制痕迹暴露在产物里）。
type TitleStat struct {
	WithPrefix    int `json:"with_prefix"`
	WithoutPrefix int `json:"without_prefix"`
}

// patternDefs 通用 AI 文风句式模式。计数是近似（正则不做语法分析），
// 用途是本书自身的纵向基线对比，绝对精度不重要。
var patternDefs = []struct {
	name string
	re   *regexp.Regexp
}{
	{"矫正句『不是…(而)是…』", regexp.MustCompile(`不是[^。！？\n]{1,24}?[，、]?(?:而)?是`)},
	{"计时量词『X息/X瞬』", regexp.MustCompile(`[一两二三四五六七八九十几数半][息瞬]`)},
	{"明喻『像一/仿佛/如同/宛如』", regexp.MustCompile(`像一|仿佛|如同|宛如`)},
	{"沉默节拍『沉默了/没有说话/没有回头』", regexp.MustCompile(`沉默了|没有说话|没有回头`)},
}

var (
	sentenceSplit = regexp.MustCompile(`[。！？\n]+`)
	openingTimeRe = regexp.MustCompile(`夜|清晨|黎明|天亮|醒来|晨光|一整夜`)
	titlePrefixRe = regexp.MustCompile(`^#{0,2}\s*第[零〇一二三四五六七八九十百千万\d]+章`)
)

// shortEndingRunes 末行不超过此字数计为"短结尾"。
const shortEndingRunes = 30

// Compute 计算全书风格统计；章数不足时Quay lại nil。
func Compute(in Input) *Stats {
	n := len(in.Chapters)
	if n < minChapters {
		return nil
	}
	all := strings.Join(in.Chapters, "\n")

	s := &Stats{Chapters: n}
	for _, def := range patternDefs {
		total := len(def.re.FindAllStringIndex(all, -1))
		if total == 0 {
			continue
		}
		s.Patterns = append(s.Patterns, PatternStat{
			Name:       def.name,
			Total:      total,
			PerChapter: round1(float64(total) / float64(n)),
		})
	}
	s.TopPhrases = minePhrases(recentWindow(in.Chapters), in.Stopwords)
	s.RepeatedSentences = repeatedSentences(in.Chapters)
	s.Ending = endingShape(in.Chapters)
	s.OpeningTimeRate = openingTimeRate(in.Chapters)
	s.TitleFormats = titleFormats(in.Titles)
	return s
}

func recentWindow(chapters []string) []string {
	if len(chapters) <= phraseWindow {
		return chapters
	}
	return chapters[len(chapters)-phraseWindow:]
}

// minePhrases 在Cửa sổ内挖掘 3-6 字高频短语。
// 过滤：含标点/Rỗng白、首尾虚词、命中专有名词；去重：与已选短语互为子串的丢弃。
func minePhrases(chapters []string, stopwords []string) []PhraseStat {
	text := strings.Join(chapters, "\n")
	runes := []rune(text)
	threshold := max(8, len(chapters)/2)

	counts := make(map[string]int)
	for size := 3; size <= 6; size++ {
		for i := 0; i+size <= len(runes); i++ {
			gram := runes[i : i+size]
			if !validGram(gram) {
				continue
			}
			counts[string(gram)]++
		}
	}

	stopGrams := stopwordBigrams(stopwords)
	type cand struct {
		text  string
		count int
	}
	var cands []cand
	for g, c := range counts {
		if c < threshold || hitStopword(g, stopGrams) {
			continue
		}
		cands = append(cands, cand{g, c})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		// 同频取更长的（信息量更大），再按字典序稳定排序
		if len(cands[i].text) != len(cands[j].text) {
			return len(cands[i].text) > len(cands[j].text)
		}
		return cands[i].text < cands[j].text
	})

	var out []PhraseStat
	for _, c := range cands {
		if len(out) >= 8 {
			break
		}
		dup := false
		for _, picked := range out {
			if strings.Contains(picked.Text, c.text) || strings.Contains(c.text, picked.Text) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, PhraseStat{Text: c.text, Count: c.count})
		}
	}
	return out
}

// gramEdgeStop 首尾为这些虚词/代词的 n-gram 不是文风短语，Bỏ qua。
const gramEdgeStop = "的了着是在和与就也都还又把被他她它我你这那"

func validGram(gram []rune) bool {
	for _, r := range gram {
		if r < 0x4E00 || r > 0x9FFF { // 仅纯汉字片段
			return false
		}
	}
	if strings.ContainsRune(gramEdgeStop, gram[0]) || strings.ContainsRune(gramEdgeStop, gram[len(gram)-1]) {
		return false
	}
	return true
}

// stopwordBigrams 把专有名词拆成 2 字片段：人名常以Phần形式入文
//（"九渊负手"含"九渊"），按整名匹配会漏网。宁可过滤偏严——短语事实少一条
// Không có碍，人名混进口头禅清单才是噪声。
func stopwordBigrams(stopwords []string) []string {
	var grams []string
	for _, w := range stopwords {
		runes := []rune(strings.TrimSpace(w))
		if len(runes) < 2 {
			continue
		}
		for i := 0; i+2 <= len(runes); i++ {
			grams = append(grams, string(runes[i:i+2]))
		}
	}
	return grams
}

func hitStopword(gram string, stopGrams []string) bool {
	for _, g := range stopGrams {
		if strings.Contains(gram, g) {
			return true
		}
	}
	return false
}

// repeatedSentences 找跨 ≥3 章逐字重复的 ≥12 字句子，按次数取 top 5。
func repeatedSentences(chapters []string) []SentenceStat {
	type rec struct {
		count    int
		chapters map[int]struct{}
	}
	seen := make(map[string]*rec)
	for ci, text := range chapters {
		for _, sent := range sentenceSplit.Split(text, -1) {
			// 剥掉包裹引号再归并：同一句台词带/不带前引号不应算成两条
			sent = strings.Trim(strings.TrimSpace(sent), `"“”‘’「」『』`)
			if len([]rune(sent)) < 12 {
				continue
			}
			r := seen[sent]
			if r == nil {
				r = &rec{chapters: make(map[int]struct{})}
				seen[sent] = r
			}
			r.count++
			r.chapters[ci] = struct{}{}
		}
	}

	var out []SentenceStat
	for sent, r := range seen {
		if len(r.chapters) < 3 {
			continue
		}
		out = append(out, SentenceStat{Text: truncateRunes(sent, 40), Chapters: len(r.chapters), Count: r.count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func endingShape(chapters []string) EndingStat {
	var lengths []int
	short := 0
	for _, text := range chapters {
		line := lastNonEmptyLine(text)
		if line == "" {
			continue
		}
		n := len([]rune(line))
		lengths = append(lengths, n)
		if n <= shortEndingRunes {
			short++
		}
	}
	if len(lengths) == 0 {
		return EndingStat{}
	}
	sort.Ints(lengths)
	return EndingStat{
		ShortRatio:  round2(float64(short) / float64(len(lengths))),
		MedianRunes: lengths[len(lengths)/2],
	}
}

func openingTimeRate(chapters []string) float64 {
	hit := 0
	for _, text := range chapters {
		if openingTimeRe.MatchString(firstParagraph(text)) {
			hit++
		}
	}
	return round2(float64(hit) / float64(len(chapters)))
}

func titleFormats(titles []string) *TitleStat {
	if len(titles) == 0 {
		return nil
	}
	t := &TitleStat{}
	for _, title := range titles {
		if strings.TrimSpace(title) == "" {
			continue
		}
		if titlePrefixRe.MatchString(title) {
			t.WithPrefix++
		} else {
			t.WithoutPrefix++
		}
	}
	// 只有混用才值得上报；统一格式不是事实意义上的问题
	if t.WithPrefix == 0 || t.WithoutPrefix == 0 {
		return nil
	}
	return t
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// firstParagraph 取第一个非Rỗng且非 Markdown Tiêu đề的行（章Tập tin首行常是 # Tiêu đề）。
func firstParagraph(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
