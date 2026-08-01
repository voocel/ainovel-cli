// Package prosecheck detects chapter-local prose patterns that often make generated
// fiction feel templated. It reports review candidates and never decides authorship.
//
// The rule categories and initial thresholds are informed by oh-story-claudecode's
// story-deslop checker (MIT). This implementation is adapted to ainovel's structured
// tool output. See third_party/oh-story-claudecode/LICENSE.
package prosecheck

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	SeverityWarning  = "warning"
	SeverityAdvisory = "advisory"
	maxEvidence      = 3
)

// Finding is a deterministic prose-review candidate. Warning marks a
// high-confidence template; advisory marks a density signal that needs context.
type Finding struct {
	Rule       string   `json:"rule"`
	Severity   string   `json:"severity"`
	Count      int      `json:"count"`
	Metric     string   `json:"metric,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	Suggestion string   `json:"suggestion"`
}

type proseLine struct {
	raw       string
	narrative string
}

var (
	notIsRe          = regexp.MustCompile(`不是[^。！？!?\n，,；;]{1,30}[，,]\s*(?:而是|是)`)
	reverseNotIsRe   = regexp.MustCompile(`是[^。！？!?\n，,；;]{1,24}[，,]\s*(?:而)?不是`)
	voiceContrastRe  = regexp.MustCompile(`声音(?:并)?不(?:大|高|响亮)[^。！？!?\n]{0,20}[，,]?[却但偏]`)
	negationParadeRe = regexp.MustCompile(`(?:没有[^。！？!?\n，,]{1,14}[，,]\s*){2,}`)

	trailerEndingRe  = regexp.MustCompile(`没人知道|谁也不知道|谁也没想到|殊不知|(?:这)?才刚刚开(?:始|头)|即将(?:开始|来临|降临)|正(?:朝着|向着)[^。！？!?\n]{0,24}(?:压|涌|袭|逼)(?:了?过去|了?过来|来)`)
	trailerSummaryRe = regexp.MustCompile(`这一夜注定|这一切都结束了|(?:新|全新)的人生才刚刚开始|(?:命运|宿命)的齿轮|(?:新的|全新的)(?:人生|故事|篇章)[^。！？!?\n]{0,12}(?:开始|开启)`)

	microActionRe = regexp.MustCompile(`了(?:[一两三几半])?(?:下|阵|圈|道|声|眼|口|气|会)`)
	actionVerbRe  = regexp.MustCompile(`伸手|抬手|探手|拿起|拿过|取出|取过|掏出|摸出|抓起|攥住|握住|捏住|按住|推开|拉开|打开|关上|放下|递给|挑开|掀开|扯开|拧开|倒出|端起|转身|回头|抬头|低头|弯腰|俯身|走到|走向|坐下|站起|看向|看着|盯着|扫过`)

	abstractSummaryRe = regexp.MustCompile(`这一刻[，,]?[^。！？!?\n]{0,24}(?:终于|才)(?:明白|意识到)|从这一刻开始|(?:命运|宿命)[^。！？!?\n]{0,28}(?:齿轮|棋局|獠牙|改写|推向|安排)|前所未有的(?:决意|清醒|勇气|力量|恐惧|平静|信念)|(?:反击|复仇|战争|较量|故事|命运)[^。！？!?\n]{0,12}才刚刚开始`)
	clicheRe          = regexp.MustCompile(`仿佛|犹如|宛若|如同|一丝|一抹|些许|几分|隐约|深吸一口气|缓缓|微微|轻轻|淡淡|眼中闪过|嘴角勾起|眸光微微一闪|指节泛白|目光锐利|眼神锐利|心中涌起一股|心头一震|心中一动|心下了然|心中暗道|心中一凛|不容置疑|不容置喙|不易察觉|显而易见|毫无疑问|不可否认|平静无波|声音平直|听不出情绪|不知何时|唾手可得|无声翻涌|难以言说|散发着一股|冰冷的光|格外刺眼|深邃而冰冷`)
	metaphorRe        = regexp.MustCompile(`好像|像是|仿佛|宛如|如同|犹如|(?:死|水|冰|火|潮水|石头|木头|机器|纸|铁|鬼|死人|刀|针|网|墙)一样`)
	reasoningRe       = regexp.MustCompile(`知道|明白|意识到|清楚|判断|确认|分析|这意味着|也就是说|换句话说|真正的问题(?:在于)?|问题在于|关键在于|在这种情况下|按照这个逻辑|只有这样|想到这里|必须|需要|应该`)
	sentenceSplitRe   = regexp.MustCompile(`[。！？!?]+`)
)

var reversePrefixExclusions = "不就也还只可但于倒像若要正便总老更最算怕凡或即自竟原本仍许净光单尽"

// Check scans one chapter. Findings are returned in stable rule order.
func Check(text string) []Finding {
	lines := collectProseLines(text)
	if len(lines) == 0 {
		return []Finding{}
	}

	findings := make([]Finding, 0)
	findings = append(findings, regexFinding(lines, notIsRe, Finding{
		Rule: "not_is_comparison", Severity: SeverityWarning,
		Suggestion: "删掉否定铺垫，直接写肯定项的动作、细节或后果。",
	})...)
	findings = append(findings, reverseNotIsFinding(lines)...)
	findings = append(findings, regexFinding(lines, voiceContrastRe, Finding{
		Rule: "voice_contrast", Severity: SeverityWarning,
		Suggestion: "删掉音量反差模板，直接写声音对现场和人物造成的具体影响。",
	})...)
	findings = append(findings, regexFinding(lines, negationParadeRe, Finding{
		Rule: "negation_parade", Severity: SeverityWarning,
		Suggestion: "删掉连续否定清单，改写现场实际存在的事物，最多保留一个必要否定。",
	})...)
	findings = append(findings, endingFindings(lines)...)
	findings = append(findings, periodStutterFinding(lines)...)
	findings = append(findings, densityFinding(lines, microActionRe, densityRule{
		rule: "micro_action_tic", minHits: 5, perKilo: 6,
		suggestion: "合并无叙事作用的轻微动作，保留能改变情绪、关系或局面的动作。",
	})...)
	findings = append(findings, actionListFinding(lines)...)
	findings = append(findings, densityFinding(lines, abstractSummaryRe, densityRule{
		rule: "abstract_summary_tic", minHits: 3, perKilo: 4,
		suggestion: "删掉作者替角色和读者作出的抽象总结，把判断落回当下动作、物件或对话。",
	})...)
	findings = append(findings, densityFinding(lines, clicheRe, densityRule{
		rule: "cliche_density_tic", minHits: 8, perKilo: 12,
		suggestion: "不要轮换同义套词，改用角色当下可感知的动作、物件、声音和具体后果。",
	})...)
	findings = append(findings, densityFinding(lines, metaphorRe, densityRule{
		rule: "metaphor_density_tic", minHits: 7, perKilo: 3,
		suggestion: "只保留承担叙事功能的少数比喻，其余回到具体画面和后果。",
	})...)
	findings = append(findings, densityFinding(lines, reasoningRe, densityRule{
		rule: "reasoning_chain_tic", minHits: 8, perKilo: 18,
		suggestion: "减少替读者推理的判断链，用现场证据、人物选择和反馈呈现结论。",
	})...)
	findings = append(findings, longParagraphFinding(lines)...)
	findings = append(findings, overcompressedFinding(lines)...)
	return compactFindings(findings)
}

func collectProseLines(text string) []proseLine {
	var lines []proseLine
	inFence := false
	for raw := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" || strings.HasPrefix(trimmed, "#") || isDivider(trimmed) {
			continue
		}
		narrative := strings.TrimSpace(stripQuoted(trimmed))
		if narrative == "" {
			continue
		}
		lines = append(lines, proseLine{raw: trimmed, narrative: narrative})
	}
	return lines
}

func isDivider(s string) bool {
	trimmed := strings.Trim(s, "-*_=~ ")
	return trimmed == "" && len(s) >= 3
}

func stripQuoted(s string) string {
	closing := map[rune]rune{'“': '”', '‘': '’', '「': '」', '『': '』', '【': '】', '"': '"', '\'': '\''}
	var out strings.Builder
	var close rune
	for _, r := range s {
		if close != 0 {
			if r == close {
				close = 0
				out.WriteRune(' ')
			}
			continue
		}
		if c, ok := closing[r]; ok {
			close = c
			out.WriteRune(' ')
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func regexFinding(lines []proseLine, re *regexp.Regexp, base Finding) []Finding {
	count := 0
	var evidence []string
	for _, line := range lines {
		matches := re.FindAllString(line.narrative, -1)
		count += len(matches)
		if len(matches) > 0 {
			evidence = addEvidence(evidence, line.raw)
		}
	}
	if count == 0 {
		return nil
	}
	base.Count = count
	base.Evidence = evidence
	return []Finding{base}
}

func reverseNotIsFinding(lines []proseLine) []Finding {
	count := 0
	var evidence []string
	for _, line := range lines {
		for _, idx := range reverseNotIsRe.FindAllStringIndex(line.narrative, -1) {
			if prev := previousRune(line.narrative, idx[0]); prev != 0 && strings.ContainsRune(reversePrefixExclusions, prev) {
				continue
			}
			count++
			evidence = addEvidence(evidence, line.raw)
		}
	}
	if count == 0 {
		return nil
	}
	return []Finding{{
		Rule: "reverse_not_is", Severity: SeverityWarning, Count: count, Evidence: evidence,
		Suggestion: "删掉后置否定，直接写肯定项的具体表现，让对比由情境完成。",
	}}
}

func previousRune(s string, byteIndex int) rune {
	var previous rune
	for i, r := range s {
		if i >= byteIndex {
			break
		}
		previous = r
	}
	return previous
}

func endingFindings(lines []proseLine) []Finding {
	const endingWindow = 600
	start := len(lines)
	visible := 0
	for start > 0 && visible < endingWindow {
		start--
		visible += visibleRunes(lines[start].narrative)
	}
	window := lines[start:]
	var findings []Finding
	findings = append(findings, regexFinding(window, trailerEndingRe, Finding{
		Rule: "trailer_ending", Severity: SeverityWarning,
		Suggestion: "结尾停在具体动作、画面或台词上，让事件本身形成悬念，不替读者预告下一章。",
	})...)
	findings = append(findings, regexFinding(window, trailerSummaryRe, Finding{
		Rule: "trailer_summary", Severity: SeverityWarning,
		Suggestion: "删掉章尾状态总结，把收束落在最后一个具体动作、画面或台词上。",
	})...)
	return findings
}

func periodStutterFinding(lines []proseLine) []Finding {
	const maxSentenceRunes = 5
	const minRun = 6
	longest := 0
	current := 0
	var currentSamples []string
	var bestSamples []string
	for _, line := range lines {
		for _, sentence := range sentenceSplitRe.Split(line.narrative, -1) {
			sentence = strings.TrimSpace(sentence)
			n := visibleRunes(sentence)
			if n > 0 && n <= maxSentenceRunes {
				current++
				currentSamples = append(currentSamples, sentence)
				if current > longest {
					longest = current
					bestSamples = append([]string(nil), currentSamples...)
				}
				continue
			}
			current = 0
			currentSamples = nil
		}
	}
	if longest < minRun {
		return nil
	}
	return []Finding{{
		Rule: "period_stutter", Severity: SeverityAdvisory, Count: longest,
		Metric: fmt.Sprintf("最长连续短句=%d", longest), Evidence: []string{truncate(strings.Join(bestSamples, "。"), 120)},
		Suggestion: "合并只承担步骤说明的碎句，保留真正需要突停和加速的节奏点。",
	}}
}

type densityRule struct {
	rule       string
	minHits    int
	perKilo    float64
	suggestion string
}

func densityFinding(lines []proseLine, re *regexp.Regexp, rule densityRule) []Finding {
	hits := 0
	chars := 0
	var evidence []string
	for _, line := range lines {
		chars += visibleRunes(line.narrative)
		matches := re.FindAllString(line.narrative, -1)
		hits += len(matches)
		if len(matches) > 0 {
			evidence = addEvidence(evidence, line.raw)
		}
	}
	if chars == 0 || hits < rule.minHits {
		return nil
	}
	rate := float64(hits) * 1000 / float64(chars)
	if rate < rule.perKilo {
		return nil
	}
	return []Finding{{
		Rule: rule.rule, Severity: SeverityAdvisory, Count: hits,
		Metric: fmt.Sprintf("%.1f/千字", rate), Evidence: evidence, Suggestion: rule.suggestion,
	}}
}

func actionListFinding(lines []proseLine) []Finding {
	count := 0
	var evidence []string
	for _, line := range lines {
		verbs := actionVerbRe.FindAllString(line.narrative, -1)
		separators := strings.Count(line.narrative, "，") + strings.Count(line.narrative, ",") + strings.Count(line.narrative, "、") + strings.Count(line.narrative, "；") + strings.Count(line.narrative, ";")
		if len(verbs) < 5 || separators < 4 {
			continue
		}
		count++
		evidence = addEvidence(evidence, line.raw)
	}
	if count == 0 {
		return nil
	}
	return []Finding{{
		Rule: "action_list_tic", Severity: SeverityAdvisory, Count: count,
		Metric: fmt.Sprintf("动作清单段=%d", count), Evidence: evidence,
		Suggestion: "合并琐碎步骤，只保留有情绪或情节功能的动作，并补充人物感受或环境反馈。",
	}}
}

func longParagraphFinding(lines []proseLine) []Finding {
	count := 0
	longest := 0
	var evidence []string
	for _, line := range lines {
		n := visibleRunes(line.raw)
		if n <= 200 {
			continue
		}
		count++
		if n > longest {
			longest = n
		}
		evidence = addEvidence(evidence, line.raw)
	}
	if count == 0 {
		return nil
	}
	return []Finding{{
		Rule: "long_paragraph", Severity: SeverityAdvisory, Count: count,
		Metric: fmt.Sprintf("最长段=%d字", longest), Evidence: evidence,
		Suggestion: "按镜头、新动作、新线索或视线变化拆段，避免单段承载过多信息。",
	}}
}

func overcompressedFinding(lines []proseLine) []Finding {
	chars := 0
	short := 0
	particles := 0
	var evidence []string
	for _, line := range lines {
		n := visibleRunes(line.narrative)
		chars += n
		if n > 0 && n <= 15 {
			short++
			evidence = addEvidence(evidence, line.raw)
		}
		for _, r := range line.narrative {
			if strings.ContainsRune("的了就着过呢吧啊呀嘛", r) {
				particles++
			}
		}
	}
	if chars < 1200 || len(lines) < 45 {
		return nil
	}
	shortRatio := float64(short) / float64(len(lines))
	particleRate := float64(particles) * 1000 / float64(chars)
	if shortRatio < 0.58 || particleRate >= 85 {
		return nil
	}
	return []Finding{{
		Rule: "overcompressed_prose_tic", Severity: SeverityAdvisory, Count: short,
		Metric: fmt.Sprintf("短段占比=%.0f%%，自然连接词=%.1f/千字", shortRatio*100, particleRate), Evidence: evidence,
		Suggestion: "检查是否把正文压成提纲或电报体；恢复必要的因果、感受与场景连接，但不要机械注水。",
	}}
}

func compactFindings(findings []Finding) []Finding {
	out := findings[:0]
	for _, finding := range findings {
		if finding.Count == 0 {
			continue
		}
		if len(finding.Evidence) > maxEvidence {
			finding.Evidence = finding.Evidence[:maxEvidence]
		}
		out = append(out, finding)
	}
	return out
}

func addEvidence(evidence []string, sample string) []string {
	if len(evidence) >= maxEvidence {
		return evidence
	}
	sample = truncate(strings.Join(strings.Fields(sample), " "), 120)
	for _, existing := range evidence {
		if existing == sample {
			return evidence
		}
	}
	return append(evidence, sample)
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func visibleRunes(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			n++
		}
	}
	return n
}
