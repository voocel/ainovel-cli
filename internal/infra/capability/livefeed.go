package capability

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

// 创作现场的直播内容（页面设计 §3）：模型生成工具参数的同时，正文逐字直播，大纲、
// 设定、审阅意见按条写成一行故事语言。路径与写法和工具 schema（prompt/profiles.go）
// 同生共死；没有固定形状的参数（结构候选的 content）不猜，只报接收进度。

// liveLine 是一条结构化产出：所属栏目与写好的一行。
type liveLine struct{ section, text string }

const (
	sectionOutline = "大纲"
	sectionCanon   = "设定"
	sectionReview  = "审阅"
	sectionVerdict = "结论"
)

// streamSpec 是一个工具参数里值得直播的部分：至多一条正文路径，若干条目路径。
type streamSpec struct {
	text  tapPath
	items []itemTap
}

// itemTap 是一类条目：参数里的位置、所属栏目与写成一行的方式（形状不符返回 false）。
type itemTap struct {
	path    tapPath
	section string
	line    func(raw []byte) (string, bool)
}

func (s streamSpec) itemPaths() []tapPath {
	paths := make([]tapPath, len(s.items))
	for i, item := range s.items {
		paths[i] = item.path
	}
	return paths
}

func (s streamSpec) lines(items []capturedItem) []liveLine {
	var lines []liveLine
	for _, item := range items {
		tap := s.items[item.item]
		if text, ok := tap.line(item.raw); ok {
			lines = append(lines, liveLine{section: tap.section, text: text})
		}
	}
	return lines
}

var (
	chapterProse = tapPath{"chapter", "blocks", "", "text"}
	blockProse   = tapPath{"text"}
)

// streamSpecs 按任务给出各工具的直播方式：审阅结论里的要求按任务输入写回原文。
func streamSpecs(task model.TaskInput) map[string]streamSpec {
	requirements := make(map[string]string)
	if review, ok := task.(*model.ReviewRangeInput); ok {
		for _, requirement := range review.Requirements {
			requirements[requirement.ID] = requirement.Text
		}
	}
	return map[string]streamSpec{
		prompt.ToolWorkspacePutChapter:   {text: chapterProse},
		prompt.ToolWorkspaceReplaceBlock: {text: blockProse},
		prompt.ToolProposalSubmit: {items: []itemTap{
			{tapPath{"compass"}, sectionOutline, compassLine},
			{tapPath{"volumes", ""}, sectionOutline, decoded(func(v narrative.VolumeEdit) string {
				return outlineLine(fmt.Sprintf("第 %d 卷", v.Volume), v.Title, v.Summary)
			})},
			{tapPath{"arcs", ""}, sectionOutline, decoded(func(v narrative.ArcEdit) string {
				return outlineLine(fmt.Sprintf("第 %d 个故事弧", v.Arc), v.Title, v.Summary)
			})},
			{tapPath{"chapters", ""}, sectionOutline, decoded(func(v narrative.ChapterEdit) string {
				return outlineLine(fmt.Sprintf("第 %d 章", v.Chapter), v.Title, v.Summary)
			})},
			{tapPath{"entities", ""}, sectionCanon, decoded(entityLine)},
			{tapPath{"facts", ""}, sectionCanon, decoded(factLine)},
			{tapPath{"remove_facts", ""}, sectionCanon, decoded(func(v narrative.FactRef) string {
				return "删除" + factRefLabel(v.Subject, v.Predicate, v.Chapter)
			})},
		}},
		prompt.ToolWorkspacePutReview: {items: []itemTap{
			{tapPath{"findings", ""}, sectionReview, decoded(findingLine)},
		}},
		prompt.ToolVerdictSubmit: {items: []itemTap{
			{tapPath{"status"}, sectionVerdict, decoded(verdictStatus)},
			{tapPath{"checks", ""}, sectionVerdict, decoded(func(v model.RequirementCheck) string {
				return checkLine(v, requirements)
			})},
		}},
	}
}

// decoded 把一类条目的写法包成 itemTap.line：解不开或写不出内容都不成行。
func decoded[T any](line func(T) string) func([]byte) (string, bool) {
	return func(raw []byte) (string, bool) {
		var value T
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		text := line(value)
		return text, text != ""
	}
}

func outlineLine(head, title, summary string) string {
	if title = flat(title); title != "" {
		head += " · " + title
	}
	if summary = flat(summary); summary != "" {
		head += " —— " + summary
	}
	return head
}

func compassLine(raw []byte) (string, bool) {
	var compass model.Compass
	if json.Unmarshal(raw, &compass) != nil {
		return "", false
	}
	var parts []string
	if ending := flat(compass.Ending); ending != "" {
		parts = append(parts, "终局："+ending)
	}
	if compass.ScaleMax > 0 {
		parts = append(parts, fmt.Sprintf("篇幅上限 %d 章", compass.ScaleMax))
	}
	if compass.Final > 0 {
		parts = append(parts, fmt.Sprintf("收官 %d 章", compass.Final))
	}
	if len(parts) == 0 {
		return "", false
	}
	return "故事罗盘 · " + strings.Join(parts, " · "), true
}

func entityLine(v narrative.EntityEdit) string {
	if v.Name == "" {
		return ""
	}
	line := v.Name + "（" + v.Kind.Noun() + "）"
	if len(v.Aliases) > 0 {
		line += " 又名 " + strings.Join(v.Aliases, "、")
	}
	return line
}

// factLine 与待确认稿的设定同一写法：「主体」谓词：内容；按章归属的事实附来源章。
func factLine(v narrative.FactEdit) string {
	if v.Subject == "" || v.Predicate == "" {
		return ""
	}
	var value string
	if json.Unmarshal(v.Value, &value) != nil {
		value = string(v.Value)
	}
	line := factRefLabel(v.Subject, v.Predicate, v.Chapter) + "：" + flat(value)
	if v.Resolved {
		line += "（回收）"
	}
	return line
}

func factRefLabel(subject, predicate string, chapter int) string {
	label := "「" + subject + "」" + predicate
	if chapter > 0 {
		label += fmt.Sprintf("（第 %d 章）", chapter)
	}
	return label
}

func findingLine(v struct {
	Chapter  int    `json:"chapter"`
	Severity string `json:"severity"`
	Note     string `json:"note"`
}) string {
	note := flat(v.Note)
	if note == "" {
		return ""
	}
	severity := "参考"
	if v.Severity == model.FindingBlocking {
		severity = "阻塞"
	}
	return fmt.Sprintf("第 %d 章 · %s —— %s", v.Chapter, severity, note)
}

func verdictStatus(status string) string {
	switch status {
	case model.ReviewPass:
		return "通过"
	case model.ReviewBlocked:
		return "未通过"
	default:
		return ""
	}
}

func checkLine(v model.RequirementCheck, requirements map[string]string) string {
	var status string
	switch v.Status {
	case model.CheckSatisfied:
		status = "已兑现"
	case model.CheckViolated:
		status = "被违反"
	case model.CheckPending:
		status = "待定"
	default:
		return ""
	}
	requirement := v.ID
	if text, ok := requirements[v.ID]; ok {
		requirement = clipRunes(flat(text), 40)
	}
	line := status + " · " + requirement
	if note := flat(v.Note); note != "" {
		line += " —— " + note
	}
	return line
}

// toolDetail 用故事语言写出一次调用作用的对象：ToolStart 时参数已收齐。
func toolDetail(tool string, args json.RawMessage) string {
	switch tool {
	case prompt.ToolAuthorityRead:
		var query narrative.Query
		if json.Unmarshal(args, &query) != nil {
			return ""
		}
		switch {
		case query.Chapter > 0:
			return fmt.Sprintf("第 %d 章", query.Chapter)
		case query.Arc > 0:
			return fmt.Sprintf("第 %d 个故事弧", query.Arc)
		case query.Volume > 0:
			return fmt.Sprintf("第 %d 卷", query.Volume)
		case query.Entity != "":
			return "「" + query.Entity + "」"
		}
	case prompt.ToolWorkspaceRead, prompt.ToolWorkspacePutCandidate:
		var input struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(args, &input) == nil {
			return input.Key
		}
	case prompt.ToolWorkspacePutChapter:
		var input struct {
			Chapter model.ManuscriptChapter `json:"chapter"`
		}
		if json.Unmarshal(args, &input) != nil {
			return ""
		}
		title := "《" + input.Chapter.Title + "》"
		if input.Chapter.Number > 0 {
			title = fmt.Sprintf("第 %d 章", input.Chapter.Number) + title
		}
		return fmt.Sprintf("%s · %d 字", title, input.Chapter.Runes())
	case prompt.ToolWorkspaceReplaceBlock:
		var input struct {
			BlockID string `json:"block_id"`
			Text    string `json:"text"`
		}
		if json.Unmarshal(args, &input) == nil {
			return fmt.Sprintf("段落 %s · %d 字", input.BlockID, utf8.RuneCountInString(input.Text))
		}
	case prompt.ToolWorkspacePutReview:
		var input struct {
			Findings []struct {
				Severity string `json:"severity"`
			} `json:"findings"`
		}
		if json.Unmarshal(args, &input) != nil {
			return ""
		}
		blocking := 0
		for _, finding := range input.Findings {
			if finding.Severity == model.FindingBlocking {
				blocking++
			}
		}
		if blocking > 0 {
			return fmt.Sprintf("%d 条意见（阻塞 %d）", len(input.Findings), blocking)
		}
		return fmt.Sprintf("%d 条意见", len(input.Findings))
	case prompt.ToolProposalSubmit:
		var input struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(args, &input) == nil {
			return clipRunes(flat(input.Reason), 40)
		}
	case prompt.ToolVerdictSubmit:
		var input struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(args, &input) == nil {
			return verdictStatus(input.Status)
		}
	}
	return ""
}

// flat 把多行文字压成一行：换行与连续空白都收成一个空格。
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func clipRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}
