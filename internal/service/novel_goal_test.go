package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// novelFixture 构造带索引的小说快照：plans 个章节计划、written 个正文，索引 revision 统一为 2。
func novelFixture(t *testing.T, plans, written int) ProjectSnapshot {
	t.Helper()
	project := ProjectSnapshot{
		ID: "book", Revision: 2, Intent: domain.Intent{Premise: "故事", TargetChapters: 5}, Index: DocumentIndex{},
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Order: 1, Title: "卷一", Summary: "开端"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Order: 1, Title: "弧一", Summary: "启程"},
		},
	}
	for number := 1; number <= plans; number++ {
		project.Plan = append(project.Plan, domain.PlanNode{
			ID: "chapter-plan-" + strings.Repeat("i", number), Kind: domain.PlanChapter, ParentID: "arc-1",
			Order: number, Title: "第" + strings.Repeat("一", number) + "章", Summary: "推进",
		})
	}
	for number := 1; number <= written; number++ {
		plan := project.Plan[1+number]
		chapterID := "chapter-" + strings.Repeat("i", number)
		project.Manuscript = append(project.Manuscript, domain.ManuscriptChapter{
			ID: chapterID, PlanNodeID: plan.ID, Number: number, Title: plan.Title,
			Author: domain.AuthorAI, Blocks: []domain.ManuscriptBlock{{ID: "b", Text: "正文"}},
		})
		// 每章随章入账一条事实：没有来源事实的章视为未入账（§4.5）。
		project.Canon = append(project.Canon, domain.CanonFact{
			ID: chapterID + "-outcome", Kind: domain.CanonEvent, SubjectID: "hero", Predicate: "event.chapter_outcome",
			Value: json.RawMessage(`"推进"`), SourceChapterID: chapterID,
		})
	}
	index := func(ref domain.DocumentRef, value any) {
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode %s: %v", ref.Key(), err)
		}
		if err := project.Index.add(domain.DocumentVersion{Document: ref, Revision: 2, Content: content}); err != nil {
			t.Fatalf("index %s: %v", ref.Key(), err)
		}
	}
	index(domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, project.Intent)
	for _, node := range project.Plan {
		index(domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID}, node)
	}
	for _, chapter := range project.Manuscript {
		index(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, chapter)
	}
	project.Entities = []domain.Entity{{ID: "hero", Kind: domain.EntityCharacter, Name: "主角"}}
	index(domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}, project.Entities[0])
	for _, fact := range project.Canon {
		index(domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, fact)
	}
	return project
}

// editChapter 模拟用户改动正文：该章最后变化 revision 前进，事实不动。
func editChapter(project *ProjectSnapshot, chapterID string, revision domain.Revision) {
	key := (domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapterID}).Key()
	entry := project.Index[key]
	entry.Revision = revision
	project.Index[key] = entry
}

// dropFacts 移除某章的全部来源事实：该章未入账。
func dropFacts(project *ProjectSnapshot, chapterID string) {
	kept := project.Canon[:0]
	for _, fact := range project.Canon {
		if fact.SourceChapterID == chapterID {
			delete(project.Index, (domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}).Key())
			continue
		}
		kept = append(kept, fact)
	}
	project.Canon = kept
}

func TestCanonGapsFollowRevisions(t *testing.T) {
	project := novelFixture(t, 3, 3)
	if gaps := canonGaps(project); len(gaps) != 0 {
		t.Fatalf("intact project has gaps: %#v", gaps)
	}
	editChapter(&project, "chapter-iii", 3)
	dropFacts(&project, "chapter-i")
	gaps := canonGaps(project)
	if len(gaps) != 2 || !gaps[0].Unrecorded || gaps[0].ChapterID != "chapter-i" || gaps[0].Revision != 2 ||
		gaps[1].Unrecorded || gaps[1].ChapterID != "chapter-iii" || gaps[1].Revision != 3 ||
		len(gaps[1].Pending) != 1 || gaps[1].Pending[0] != "chapter-iii-outcome" {
		t.Fatalf("gaps = %#v", gaps)
	}
}

func storedTestVerdict(status string, chapters []string, findings []domain.ReviewFinding, intent bool) storedVerdict {
	verdict := domain.ReviewVerdict{
		Status: status, Revision: 2, ChapterIDs: chapters, ReviewKey: "review", Findings: findings,
		Basis: domain.EvidenceBasis{Documents: []domain.DocumentBasis{{Ref: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Revision: 1}}},
	}
	if findings == nil {
		verdict.Findings = []domain.ReviewFinding{}
	}
	if intent {
		verdict.Intent = &domain.IntentVerification{RequiredPresent: true, ForbiddenAbsent: true, EndingConsistent: true}
	}
	return storedVerdict{verdict: verdict, key: "review", createdAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
}

// acceptedVerdict 给裁定附上用户裁决（D43）：推导器只看生效裁定。
func acceptedVerdict(verdict storedVerdict, findings ...string) storedVerdict {
	verdict.accepted = make(map[string]struct{}, len(findings))
	for _, id := range findings {
		verdict.accepted[id] = struct{}{}
	}
	return verdict
}

func TestNovelDeriverNextFollowsNovelRules(t *testing.T) {
	run := domain.CreationRun{
		ID: "run:book:1", ProjectID: "book",
		Goal:     domain.NovelGoal{Premise: "故事", TargetChapters: 5}.Goal(),
		Strategy: domain.CreationRunStrategy{PlanWindowChapters: 3, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 1},
	}
	window := []string{"chapter-i", "chapter-ii", "chapter-iii"}
	whole := []string{"chapter-i", "chapter-ii", "chapter-iii", "chapter-iiii", "chapter-iiiii"}
	blocking := []domain.ReviewFinding{{ChapterID: "chapter-ii", Severity: domain.FindingBlocking, Note: "第二章崩了"}}
	note := []domain.ReviewFinding{{ChapterID: "chapter-iii", Severity: domain.FindingNote, Note: "动机要更明确"}}
	cases := []struct {
		name     string
		plans    int
		written  int
		evidence runEvidence
		edit     func(project *ProjectSnapshot)
		wantKind domain.OperationKind
		wantID   string
		wantWait string
		wantDone string
		wantFail string
		check    func(t *testing.T, work workItem)
	}{
		{name: "empty plan develops", wantKind: domain.OperationDevelopPlan, wantID: "run:book:1:plan"},
		{name: "unwritten window writes next chapter", plans: 3, written: 1,
			wantKind: domain.OperationWriteChapter, wantID: "run:book:1:chapter:chapter-plan-ii"},
		{name: "written window reviews before extending", plans: 3, written: 3,
			wantKind: domain.OperationReviewRange, wantID: "run:book:1:review:r2",
			check: func(t *testing.T, work workItem) {
				input := work.Input.(domain.ReviewRangeInput)
				if input.VerifyIntent || len(input.ChapterIDs) != 3 || len(input.Basis.Documents) == 0 {
					t.Fatalf("window review input = %#v", input)
				}
			}},
		{name: "reviewed window extends with notes", plans: 3, written: 3,
			evidence: runEvidence{verdicts: []storedVerdict{storedTestVerdict(domain.ReviewPass, window, note, false)}},
			wantKind: domain.OperationRevisePlan, wantID: "run:book:1:plan:extend:3",
			check: func(t *testing.T, work workItem) {
				input := work.Input.(domain.RevisePlanInput)
				if input.ExistingChapters != 3 || input.RequestedChapters != 5 || len(input.ReviewNotes) != 1 {
					t.Fatalf("extend input = %#v", input)
				}
			}},
		{name: "blocked window rewrites within budget", plans: 3, written: 3,
			evidence: runEvidence{verdicts: []storedVerdict{storedTestVerdict(domain.ReviewBlocked, window, blocking, false)}},
			wantKind: domain.OperationRewriteChapter, wantID: "run:book:1:rewrite:chapter-ii:r2"},
		{name: "exhausted budget waits", plans: 3, written: 3,
			evidence: runEvidence{verdicts: []storedVerdict{storedTestVerdict(domain.ReviewBlocked, window, blocking, false)}, repairsUsed: 1},
			wantWait: "自动修订预算 1 次已用尽"},
		{name: "complete manuscript needs final review", plans: 5, written: 5,
			wantKind: domain.OperationReviewRange, wantID: "run:book:1:review:r2",
			check: func(t *testing.T, work workItem) {
				if input := work.Input.(domain.ReviewRangeInput); !input.VerifyIntent || len(input.ChapterIDs) != 5 {
					t.Fatalf("final review input = %#v", input)
				}
			}},
		{name: "verified final pass completes", plans: 5, written: 5,
			evidence: runEvidence{verdicts: []storedVerdict{storedTestVerdict(domain.ReviewPass, whole, nil, true)}},
			wantDone: "全书 5 章完成并通过审阅"},
		{name: "unverified final pass fails", plans: 5, written: 5,
			evidence: runEvidence{verdicts: []storedVerdict{storedTestVerdict(domain.ReviewPass, whole, nil, false)}},
			wantFail: "审阅通过但未逐项核验意图或用户要求，需要人工检查"},
		{name: "accepted blocking finding passes the window", plans: 3, written: 3,
			evidence: runEvidence{verdicts: []storedVerdict{acceptedVerdict(storedTestVerdict(domain.ReviewBlocked, window, blocking, false), "review/0")}},
			wantKind: domain.OperationRevisePlan, wantID: "run:book:1:plan:extend:3"},
		{name: "accepted final finding completes", plans: 5, written: 5,
			evidence: runEvidence{verdicts: []storedVerdict{acceptedVerdict(storedTestVerdict(domain.ReviewBlocked, whole, blocking, true), "review/0")}},
			wantDone: "全书 5 章完成并通过审阅"},
		{name: "edited chapter verifies its facts before anything else", plans: 3, written: 3,
			evidence: runEvidence{verdicts: []storedVerdict{storedTestVerdict(domain.ReviewBlocked, window, blocking, false)}},
			edit:     func(project *ProjectSnapshot) { editChapter(project, "chapter-ii", 3) },
			wantKind: domain.OperationReviseCanon, wantID: "run:book:1:canon:chapter-ii:r3",
			check: func(t *testing.T, work workItem) {
				input := work.Input.(domain.ReviseCanonInput)
				if input.ChapterID != "chapter-ii" || len(input.FactIDs) != 1 || input.FactIDs[0] != "chapter-ii-outcome" {
					t.Fatalf("verify input = %#v", input)
				}
			}},
		{name: "unrecorded chapter is booked before writing on", plans: 3, written: 2,
			edit:     func(project *ProjectSnapshot) { dropFacts(project, "chapter-i") },
			wantKind: domain.OperationReviseCanon, wantID: "run:book:1:canon:chapter-i:r2",
			check: func(t *testing.T, work workItem) {
				if input := work.Input.(domain.ReviseCanonInput); len(input.FactIDs) != 0 || !strings.Contains(input.Reason, "没有入账") {
					t.Fatalf("booking input = %#v", input)
				}
			}},
		{name: "oversized plan waits", plans: 6, wantWait: "蓝图包含 6 个有效章节节点"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := novelFixture(t, tc.plans, tc.written)
			if tc.edit != nil {
				tc.edit(&project)
			}
			next, err := novelDeriver{}.Next(project, run, tc.evidence)
			if err != nil {
				t.Fatalf("next: %v", err)
			}
			switch {
			case tc.wantWait != "":
				if next.Work != nil || !strings.Contains(next.Wait, tc.wantWait) {
					t.Fatalf("step = %#v, want wait %q", next, tc.wantWait)
				}
			case tc.wantDone != "":
				if next.Work != nil || next.Done != tc.wantDone {
					t.Fatalf("step = %#v, want done %q", next, tc.wantDone)
				}
			case tc.wantFail != "":
				if next.Work != nil || next.Fail != tc.wantFail {
					t.Fatalf("step = %#v, want fail %q", next, tc.wantFail)
				}
			default:
				if next.Work == nil || next.Work.Kind != tc.wantKind || next.Work.ID != tc.wantID {
					t.Fatalf("step = %#v, want %s %s", next, tc.wantKind, tc.wantID)
				}
				if err := next.Work.Input.Validate(); err != nil {
					t.Fatalf("work input: %v", err)
				}
				if tc.check != nil {
					tc.check(t, *next.Work)
				}
			}
		})
	}
}
