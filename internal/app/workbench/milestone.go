package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Milestone 是生产线上值得一提的一件事（D77）：入稿、审阅结论，以及创作此刻停下的原因。
// 都取自落盘的记录，重开作品也还在；对话视图把它们与对话按时间交织，书况告诉主编上一
// 回合以来发生了什么。
type Milestone struct {
	At   time.Time     `json:"at"`
	Tone MilestoneTone `json:"tone"`
	Text string        `json:"text"`
}

type MilestoneTone string

const (
	MilestoneDone MilestoneTone = "done"
	MilestoneWait MilestoneTone = "wait"
	MilestoneFail MilestoneTone = "fail"
)

// marksAt 按产出它的裁定归拢标记在案的章（D94）：那份裁定落定时，推进放下这些章接着写。
func marksAt(marks []novel.Mark, current map[string]*novel.StoredVerdict) map[string][]novel.Mark {
	byVerdict := make(map[string][]novel.Mark, len(marks))
	for _, mark := range marks {
		if stored := current[mark.ChapterID]; stored != nil {
			byVerdict[stored.Key] = append(byVerdict[stored.Key], mark)
		}
	}
	return byVerdict
}

func (s *Query) milestones(ctx context.Context, project projectdoc.Snapshot, run *model.CreationRun, marks map[string][]novel.Mark) ([]Milestone, error) {
	chapters := make(map[string]model.ManuscriptChapter, len(project.Manuscript))
	for _, chapter := range project.Manuscript {
		chapters[chapter.ID] = chapter
	}
	plans := model.IndexPlan(project.Plan)
	label := func(chapter model.ManuscriptChapter) string {
		return fmt.Sprintf("第 %d 章《%s》", chapter.Number, plans[chapter.PlanNodeID].Title)
	}
	commits, err := s.store.ManuscriptCommits(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	var milestones []Milestone
	written := make(map[string]bool)
	for _, commit := range commits {
		chapter, ok := chapters[commit.ChapterID]
		if !ok {
			continue // 之后撤销掉的章
		}
		text := label(chapter) + "入稿"
		if written[commit.ChapterID] {
			text = label(chapter) + "改写入稿"
		}
		written[commit.ChapterID] = true
		milestones = append(milestones, Milestone{At: commit.At, Tone: MilestoneDone, Text: text})
	}
	verdicts, err := s.store.ListDerivedDocumentsByKind(ctx, project.ID, model.DerivedVerdictKind)
	if err != nil {
		return nil, err
	}
	for _, document := range verdicts {
		var verdict model.ReviewVerdict
		if err := json.Unmarshal(document.Content, &verdict); err != nil {
			return nil, fmt.Errorf("decode review verdict %q: %w", document.Key, err)
		}
		var numbers []int
		for _, id := range verdict.ChapterIDs {
			if chapter, ok := chapters[id]; ok {
				numbers = append(numbers, chapter.Number)
			}
		}
		if len(numbers) == 0 {
			continue
		}
		milestone := Milestone{At: document.CreatedAt, Tone: MilestoneDone, Text: "审阅" + chapterSpan(numbers) + "：通过"}
		if blocking := verdict.Blocking(); blocking > 0 {
			milestone.Tone, milestone.Text = MilestoneWait, fmt.Sprintf("审阅%s：未通过 · %d 处未解决", chapterSpan(numbers), blocking)
		}
		milestones = append(milestones, milestone)
		for _, mark := range marks[document.Key] {
			milestones = append(milestones, Milestone{At: document.CreatedAt, Tone: MilestoneWait, Text: fmt.Sprintf(
				"%s修订 %d 次仍未改好，已标记，接着写", label(chapters[mark.ChapterID]), mark.Repairs)})
		}
	}
	if run != nil {
		switch run.State {
		case model.RunWaitingUser:
			milestones = append(milestones, Milestone{At: run.UpdatedAt, Tone: MilestoneWait, Text: run.StateReason})
		case model.RunFailed:
			milestones = append(milestones, Milestone{At: run.UpdatedAt, Tone: MilestoneFail, Text: "创作出了问题停下：" + run.StateReason})
		case model.RunCompleted:
			milestones = append(milestones, Milestone{At: run.UpdatedAt, Tone: MilestoneDone, Text: run.StateReason})
		}
	}
	slices.SortStableFunc(milestones, func(a, b Milestone) int { return a.At.Compare(b.At) })
	return milestones, nil
}

// chapterSpan 把章号写成「第 1–3 章」「第 2、5 章」。
func chapterSpan(numbers []int) string {
	slices.Sort(numbers)
	first, last := numbers[0], numbers[len(numbers)-1]
	switch {
	case first == last:
		return fmt.Sprintf("第 %d 章", first)
	case last-first+1 == len(numbers):
		return fmt.Sprintf("第 %d–%d 章", first, last)
	}
	parts := make([]string, len(numbers))
	for i, number := range numbers {
		parts[i] = strconv.Itoa(number)
	}
	return "第 " + strings.Join(parts, "、") + " 章"
}
