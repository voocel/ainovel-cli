package revision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func TestAnalysisContractIsStrictReady(t *testing.T) {
	if err := llmcontract.ValidateStrictReady(analysisContract.Schema); err != nil {
		t.Fatal(err)
	}
}

func TestScanUsesAcceptedContentInsteadOfFileMetadata(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	acceptTestChapter(t, st, 1, "第一段\n第二段", domain.ChapterFacts{Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"}})

	path := filepath.Join(st.Dir(), "chapters", "01.md")
	if err := os.WriteFile(path, []byte("第一段\r\n第二段"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := Scan(st)
	if err != nil || len(changes) != 0 {
		t.Fatalf("仅行尾变化不应产生修订: changes=%v err=%v", changes, err)
	}
	if err := os.WriteFile(path, []byte("第一段\r\n用户改写"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err = Scan(st)
	if err != nil || len(changes) != 1 || changes[0].Before == changes[0].After {
		t.Fatalf("正文变化未被识别: changes=%+v err=%v", changes, err)
	}
}

func TestScanRejectsEmptyCompletedChapter(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"}}
	acceptTestChapter(t, st, 1, "系统正文", facts)
	if err := os.WriteFile(filepath.Join(st.Dir(), "chapters", "01.md"), []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(st); err == nil {
		t.Fatal("空终稿必须显式拒绝")
	}
}

func TestMigrateLegacyBaselineKeepsExternalChangeDirty(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{
		Title: "第一章", Summary: "林墨离开村庄", Characters: []string{"林墨"}, KeyEvents: []string{"离村"},
		TimelineEvents: []domain.TimelineEvent{{Time: "清晨", Event: "林墨离村", Characters: []string{"林墨"}}},
		HookType:       "mystery", DominantStrand: "quest",
	}
	saveLegacyChapter(t, st, 1, "系统提交的正文", "用户后来修改的正文", facts)

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil {
		t.Fatalf("迁移后接纳记录缺失: record=%+v err=%v", record, err)
	}
	if record.Content != "系统提交的正文" {
		t.Fatalf("迁移错误接纳了当前工作区正文: %q", record.Content)
	}
	changes, err := Scan(st)
	if err != nil || len(changes) != 1 || changes[0].Chapter != 1 {
		t.Fatalf("迁移前的外部修改应保持待同步: changes=%+v err=%v", changes, err)
	}
	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
}

func TestMigrateLegacyBaselineWithoutDraftUsesFinal(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{
		Title: "第一章", Summary: "旧书导入", Characters: []string{"林墨"}, KeyEvents: []string{"进入旧城"},
		HookType: "mystery", DominantStrand: "quest",
	}
	saveLegacyChapter(t, st, 1, "", "导入正文", facts)

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || record.Facts.Summary != facts.Summary || record.Content != "导入正文" {
		t.Fatalf("导入书迁移结果错误: record=%+v err=%v", record, err)
	}
	if record.Origin != domain.ChapterOriginLegacy {
		t.Fatalf("origin = %q, want legacy", record.Origin)
	}
}

func TestMigrateLegacyBaselineFillsOnlyMissingRecords(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	facts1 := domain.ChapterFacts{
		Title: "第一章", Summary: "已经接纳", KeyEvents: []string{"出发"},
		HookType: "mystery", DominantStrand: "quest",
	}
	acceptTestChapter(t, st, 1, "第一章正文", facts1)
	existing, err := st.ChapterRecords.Load(1)
	if err != nil {
		t.Fatal(err)
	}
	existing.Revision = 2
	if err := st.ChapterRecords.Save(*existing); err != nil {
		t.Fatal(err)
	}

	facts2 := domain.ChapterFacts{
		Title: "第二章", Summary: "抵达旧城", KeyEvents: []string{"入城"},
		HookType: "mystery", DominantStrand: "quest",
	}
	saveLegacyChapter(t, st, 2, "第二章历史草稿", "第二章历史草稿", facts2)

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	after, err := st.ChapterRecords.Load(1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, existing) {
		t.Fatalf("existing record changed: before=%+v after=%+v", existing, after)
	}
	migrated, err := st.ChapterRecords.Load(2)
	if err != nil || migrated == nil || migrated.Content != "第二章历史草稿" {
		t.Fatalf("missing record was not migrated: record=%+v err=%v", migrated, err)
	}
}

func TestMigrateLegacyBaselineReconstructsWorldProjection(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	facts1 := domain.ChapterFacts{
		Title: "第一章", Summary: "掌柜交出密信", Characters: []string{"掌柜"}, KeyEvents: []string{"得到密信"},
		TimelineEvents:      []domain.TimelineEvent{{Time: "清晨", Event: "掌柜交出密信", Characters: []string{"掌柜"}}},
		ForeshadowUpdates:   []domain.ForeshadowUpdate{{ID: "密信", Action: "plant", Description: "未拆的密信"}},
		RelationshipChanges: []domain.RelationshipEntry{{CharacterA: "掌柜", CharacterB: "林墨", Relation: "试探"}},
		StateChanges:        []domain.StateChange{{Entity: "林墨", Field: "location", NewValue: "客栈"}},
		CastIntros:          []domain.CastIntro{{Name: "掌柜", BriefRole: "客栈掌柜"}},
		HookType:            "mystery", DominantStrand: "quest",
	}
	facts2 := domain.ChapterFacts{
		Title: "第二章", Summary: "密信线索推进", Characters: []string{"掌柜"}, KeyEvents: []string{"核对火漆"},
		ForeshadowUpdates:   []domain.ForeshadowUpdate{{ID: "密信", Action: "advance"}},
		RelationshipChanges: []domain.RelationshipEntry{{CharacterA: "林墨", CharacterB: "掌柜", Relation: "合作"}},
		HookType:            "choice", DominantStrand: "quest",
	}
	saveLegacyChapter(t, st, 1, "第一章正文", "第一章正文", facts1)
	saveLegacyChapter(t, st, 2, "第二章正文", "第二章正文", facts2)

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	records, err := st.ChapterRecords.LoadCompleted([]int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewProjector(st).Apply(records); err != nil {
		t.Fatal(err)
	}
	ledger, err := st.World.LoadForeshadowLedger()
	if err != nil || len(ledger) != 1 || ledger[0].Status != "advanced" {
		t.Fatalf("foreshadow = %+v, err = %v", ledger, err)
	}
	cast, err := st.BuildCast([]int{1, 2})
	if err != nil || len(cast) != 1 || cast[0].BriefRole != "客栈掌柜" {
		t.Fatalf("cast = %+v, err = %v", cast, err)
	}
}

func TestMigrateLegacyBaselineAllowsStaleCastAfterRewrite(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	oldFacts := domain.ChapterFacts{
		Title: "第一章", Summary: "旧配角出场", Characters: []string{"旧配角"}, KeyEvents: []string{"相遇"},
		CastIntros: []domain.CastIntro{{Name: "旧配角", BriefRole: "旧友"}},
	}
	saveLegacyChapter(t, st, 1, "旧正文", "旧正文", oldFacts)
	legacyCast, err := loadLegacyCast(st)
	if err != nil || len(legacyCast) != 1 || legacyCast[0].Name != "旧配角" {
		t.Fatalf("stale cast precondition failed: cast=%+v err=%v", legacyCast, err)
	}

	newFacts := domain.ChapterFacts{
		Title: "第一章", Summary: "新配角出场", Characters: []string{"新配角"}, KeyEvents: []string{"改写相遇"},
	}
	if err := st.Drafts.SaveDraft(1, "新正文"); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "新正文"); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter: 1, Title: newFacts.Title, Summary: newFacts.Summary,
		Characters: newFacts.Characters, KeyEvents: newFacts.KeyEvents,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, len([]rune("新正文")), "", ""); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	records, err := st.ChapterRecords.LoadCompleted([]int{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewProjector(st).Apply(records); err != nil {
		t.Fatal(err)
	}
	cast, err := st.BuildCast([]int{1})
	if err != nil || len(cast) != 1 || cast[0].Name != "新配角" {
		t.Fatalf("cast = %+v, err = %v", cast, err)
	}
}

func TestMigrateLegacyBaselineAllowsCRLFDraft(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	content := "第一行\r\n第二行"
	facts := domain.ChapterFacts{Title: "第一章", Summary: "两行正文", KeyEvents: []string{"事件"}}
	saveLegacyChapter(t, st, 1, content, content, facts)
	progress, err := st.Progress.Load()
	if err != nil || progress.TotalWordCount != len([]rune(content)) {
		t.Fatalf("legacy word count precondition failed: progress=%+v err=%v", progress, err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || record.Content != "第一行\n第二行" {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
}

func TestMigrateLegacyBaselineAcceptsChapterWithoutTitle(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	// v0.7.4 之前的提交 schema 没有 title
	facts := domain.ChapterFacts{Summary: "旧版摘要", Characters: []string{"林墨"}, KeyEvents: []string{"入城"}}
	saveLegacyChapter(t, st, 1, "正文", "正文", facts)

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	records, err := st.ChapterRecords.LoadCompleted([]int{1})
	if err != nil || records[0].Facts.Title != "" {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
	if err := NewProjector(st).Apply(records); err != nil {
		t.Fatalf("legacy record must replay without new-contract validation: %v", err)
	}
}

func TestMigrateLegacyBaselineIgnoresOrphanedCommitState(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	facts := domain.ChapterFacts{
		Title: "第一章", Summary: "埋下密信", Characters: []string{"林墨"}, KeyEvents: []string{"得到密信"},
		ForeshadowUpdates: []domain.ForeshadowUpdate{{ID: "密信", Action: "plant", Description: "未拆的密信"}},
	}
	saveLegacyChapter(t, st, 1, "正文", "正文", facts)
	// 第 2 章提交写完世界状态后崩溃，从未标记完成
	if err := st.World.AppendTimelineEvents([]domain.TimelineEvent{{Chapter: 2, Time: "夜", Event: "拆信"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.UpdateForeshadow(2, []domain.ForeshadowUpdate{{ID: "密信", Action: "resolve"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.UpdateRelationships([]domain.RelationshipEntry{{CharacterA: "林墨", CharacterB: "掌柜", Relation: "敌对", Chapter: 2}}); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || len(record.Facts.TimelineEvents) != 0 || len(record.Facts.RelationshipChanges) != 0 {
		t.Fatalf("orphaned chapter 2 state leaked into chapter 1: record=%+v err=%v", record, err)
	}
	actions := make([]string, 0, 2)
	for _, update := range record.Facts.ForeshadowUpdates {
		actions = append(actions, update.Action)
	}
	if !slices.Equal(actions, []string{"plant", "advance"}) {
		t.Fatalf("foreshadow actions = %v, want plant then advance", actions)
	}
}

func TestMigrateLegacyBaselineToleratesEarlyLedger(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"}}
	saveLegacyChapter(t, st, 1, "正文", "正文", facts)
	// v0.0.x 的 plant 不校验 id 和 description，重复埋设会追加同 ID 条目
	if err := st.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "", Description: "无名", PlantedAt: 1, Status: "planted"},
		{ID: "密信", PlantedAt: 1, Status: "planted"},
		{ID: "密信", PlantedAt: 1, Status: "advanced"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	records, err := st.ChapterRecords.LoadCompleted([]int{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewProjector(st).Apply(records); err != nil {
		t.Fatal(err)
	}
	ledger, err := st.World.LoadForeshadowLedger()
	if err != nil || len(ledger) != 1 || ledger[0].ID != "密信" || ledger[0].Status != "advanced" {
		t.Fatalf("ledger = %+v, err = %v", ledger, err)
	}
}

func TestMigrateLegacyBaselineRestoresFinalFromDraft(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"}}
	saveLegacyChapter(t, st, 1, "草稿正文", "", facts)

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	final, err := st.Drafts.LoadChapterText(1)
	if err != nil || final != "草稿正文" {
		t.Fatalf("final = %q, err = %v", final, err)
	}
	if changes, err := Scan(st); err != nil || len(changes) != 0 {
		t.Fatalf("restored chapter must be clean: changes=%v err=%v", changes, err)
	}
}

func TestMigrateLegacyBaselineAcceptsMissingChapterText(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"}}
	saveLegacyChapter(t, st, 1, "", "", facts)
	if err := os.Remove(filepath.Join(st.Dir(), "chapters", "01.md")); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatalf("missing chapter must not block upgrade: %v", err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || record.Content != "" {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
	if changes, err := Scan(st); err != nil || len(changes) != 0 {
		t.Fatalf("empty baseline with no file must scan clean: changes=%v err=%v", changes, err)
	}
}

func TestMigrateLegacyBaselineAcceptsMissingSummary(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	if err := st.Drafts.SaveFinalChapter(1, "正文"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 2, "", ""); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatalf("missing summary is just missing context: %v", err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || record.Content != "正文" || record.Facts.Summary != "" {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
}

func TestMigrateLegacyBaselineRestoresPlantDroppedFromExistingRecord(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	facts1 := domain.ChapterFacts{Title: "第一章", Summary: "旧章", KeyEvents: []string{"事件"}}
	facts2 := domain.ChapterFacts{Title: "第二章", Summary: "新章", KeyEvents: []string{"事件"}}
	saveLegacyChapter(t, st, 1, "正文一", "正文一", facts1)
	saveLegacyChapter(t, st, 2, "正文二", "正文二", facts2)
	record2 := testRecord(2, "正文二", facts2, domain.StyleDelta{}, time.Now())
	record2.Origin = domain.ChapterOriginGenerated
	if err := st.ChapterRecords.Save(record2); err != nil {
		t.Fatal(err)
	}
	// 账本说伏笔埋在第 2 章，但第 2 章的接纳记录里没有这次埋设
	if err := st.World.SaveForeshadowLedger([]domain.ForeshadowEntry{{ID: "孤儿", Description: "无主伏笔", PlantedAt: 2, Status: "planted"}}); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatal(err)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || len(record.Facts.ForeshadowUpdates) != 0 {
		t.Fatalf("record 1 = %+v, err = %v", record, err)
	}
	record, err = st.ChapterRecords.Load(2)
	want := []domain.ForeshadowUpdate{{ID: "孤儿", Action: "plant", Description: "无主伏笔"}}
	if err != nil || record == nil || !reflect.DeepEqual(record.Facts.ForeshadowUpdates, want) {
		t.Fatalf("record 2 = %+v, err = %v", record, err)
	}
}

// 复现 #128：v0.7.6 返工覆盖了第 1 章记录、丢掉 plant，后续章节的 advance 让 v2→v3
// 升级在重放时报"推进未知伏笔"，整本书打不开。
func TestMigrateLegacyBaselineRepairsChainBrokenByOldRework(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	acceptTestChapter(t, st, 1, "返工后的第一章", domain.ChapterFacts{
		Title: "第一章", Summary: "返工丢了埋设", KeyEvents: []string{"出发"},
	})
	facts2 := domain.ChapterFacts{
		Title: "第二章", Summary: "推进线索", KeyEvents: []string{"追查"},
		ForeshadowUpdates: []domain.ForeshadowUpdate{{ID: "F1", Action: "advance"}},
	}
	acceptTestChapter(t, st, 2, "第二章正文", facts2)
	if err := st.World.SaveForeshadowLedger([]domain.ForeshadowEntry{{
		ID: "F1", Description: "执照暂停线索", PlantedAt: 1, Status: "advanced",
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := st.ChapterRecords.Load(2)
	if err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatalf("旧版返工丢失的 plant 应在升级时补回: %v", err)
	}
	record, err := st.ChapterRecords.Load(1)
	want := []domain.ForeshadowUpdate{{ID: "F1", Action: "plant", Description: "执照暂停线索"}}
	if err != nil || record == nil || !reflect.DeepEqual(record.Facts.ForeshadowUpdates, want) {
		t.Fatalf("record 1 = %+v, err = %v", record, err)
	}
	after, err := st.ChapterRecords.Load(2)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("record 2 changed: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestMigrateLegacyBaselineWarnsInsteadOfFailingOnDrift(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	facts1 := domain.ChapterFacts{Title: "第一章", Summary: "旧章", KeyEvents: []string{"事件"}}
	facts2 := domain.ChapterFacts{Title: "第二章", Summary: "磁盘上的摘要", KeyEvents: []string{"事件"}}
	saveLegacyChapter(t, st, 1, "正文一", "正文一", facts1)
	saveLegacyChapter(t, st, 2, "正文二", "正文二", facts2)
	drifted := facts2
	drifted.Summary = "记录里的摘要"
	if err := st.ChapterRecords.Save(testRecord(2, "正文二", drifted, domain.StyleDelta{}, time.Now())); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyBaseline(st); err != nil {
		t.Fatalf("projection drift must not block upgrade: %v", err)
	}
	if record, err := st.ChapterRecords.Load(1); err != nil || record == nil {
		t.Fatalf("record 1 = %+v, err = %v", record, err)
	}
}

func TestChangedExcerptOmitsUnchangedPrefixAndSuffix(t *testing.T) {
	got := changedExcerpt("相同开头\n旧内容\n相同结尾", "相同开头\n新内容\n相同结尾")
	if got.Before != "旧内容" || got.After != "新内容" || got.BeforeStart != 2 || got.AfterStart != 2 {
		t.Fatalf("changed excerpt = %+v", got)
	}
}

func TestProjectorRebuildsWorldStateFromChapterRecords(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	if err := st.World.SaveTimeline([]domain.TimelineEvent{{Chapter: 1, Time: "旧", Event: "应被删除"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	records := []domain.ChapterRecord{
		testRecord(1, "正文一", domain.ChapterFacts{
			Title: "第一章", Summary: "新摘要", Characters: []string{"林墨", "店主"}, KeyEvents: []string{"离城"},
			TimelineEvents:      []domain.TimelineEvent{{Time: "当夜", Event: "林墨离城", Characters: []string{"林墨"}}},
			ForeshadowUpdates:   []domain.ForeshadowUpdate{{ID: "信件", Action: "plant", Description: "未拆的信"}},
			RelationshipChanges: []domain.RelationshipEntry{{CharacterA: "林墨", CharacterB: "店主", Relation: "互相信任"}},
			StateChanges:        []domain.StateChange{{Entity: "林墨", Field: "location", NewValue: "城外"}},
			CastIntros:          []domain.CastIntro{{Name: "店主", BriefRole: "客栈店主"}}, HookType: "mystery", DominantStrand: "quest",
		}, domain.StyleDelta{Prose: []string{"减少解释性心理描写"}}, now),
		testRecord(2, "正文二", domain.ChapterFacts{
			Title: "第二章", Summary: "后续", Characters: []string{"林墨", "店主"}, KeyEvents: []string{"拆信"},
			ForeshadowUpdates:   []domain.ForeshadowUpdate{{ID: "信件", Action: "resolve"}},
			RelationshipChanges: []domain.RelationshipEntry{{CharacterA: "店主", CharacterB: "林墨", Relation: "决裂"}},
		}, domain.StyleDelta{}, now.Add(time.Minute)),
	}
	if err := NewProjector(st).Apply(records); err != nil {
		t.Fatal(err)
	}
	timeline, _ := st.World.LoadTimeline()
	if len(timeline) != 1 || timeline[0].Event != "林墨离城" || timeline[0].Chapter != 1 {
		t.Fatalf("时间线未按记录重建: %+v", timeline)
	}
	ledger, _ := st.World.LoadForeshadowLedger()
	if len(ledger) != 1 || ledger[0].Status != "resolved" || ledger[0].ResolvedAt != 2 {
		t.Fatalf("伏笔投影错误: %+v", ledger)
	}
	relationships, _ := st.World.LoadRelationships()
	if len(relationships) != 1 || relationships[0].Relation != "决裂" || relationships[0].Chapter != 2 {
		t.Fatalf("关系投影错误: %+v", relationships)
	}
	style, _ := st.World.LoadAuthorRevisionStyle()
	if style == nil || len(style.Prose) != 1 || style.Prose[0] != "减少解释性心理描写" {
		t.Fatalf("用户修订风格未投影: %+v", style)
	}
}

func TestServiceAcceptsRevisionAndRefreshesFacts(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	acceptTestChapter(t, st, 1, "林墨留在城中。", domain.ChapterFacts{
		Title: "第一章", Summary: "林墨留在城中", Characters: []string{"林墨"}, KeyEvents: []string{"留城"},
	})
	if err := os.WriteFile(filepath.Join(st.Dir(), "chapters", "01.md"), []byte("林墨连夜离开城市。"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := revisionTestModel(t, `{
  "change_summary":"林墨由留城改为连夜离城",
  "story_changed":true,
  "facts":{
    "title":"第一章","summary":"林墨连夜离城","characters":["林墨"],"key_events":["林墨离城"],
    "timeline_events":[{"time":"当夜","event":"林墨离开城市","characters":["林墨"]}],
    "foreshadow_updates":[],"relationship_changes":[],
    "state_changes":[{"entity":"林墨","field":"location","old_value":"城中","new_value":"城外","reason":"主动离开"}],
    "cast_intros":[],"hook_type":null,"dominant_strand":null
  },
  "style_delta":{"prose":["动作表达直接，不补充解释"],"dialogue":[],"taboos":[]},
  "outline_impact":{"deviation":"主角已提前离城","suggestion":"后续从城外承接"},
  "downstream_issues":[]
}`)
	index := &recordingStyleIndex{}
	result, err := NewService(st, model, "分析用户修订", index).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 || result.Applied[0] != 1 {
		t.Fatalf("同步结果错误: %+v", result)
	}
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || record.Origin != domain.ChapterOriginUser || record.Revision != 2 {
		t.Fatalf("接纳记录错误: record=%+v err=%v", record, err)
	}
	summary, _ := st.Summaries.LoadSummary(1)
	if summary == nil || summary.Summary != "林墨连夜离城" {
		t.Fatalf("摘要未刷新: %+v", summary)
	}
	changes, err := Scan(st)
	if err != nil || len(changes) != 0 {
		t.Fatalf("接纳后工作区仍为 dirty: changes=%v err=%v", changes, err)
	}
	if index.chapter != 1 || index.text != "林墨连夜离开城市。" {
		t.Fatalf("风格统计索引未刷新: %+v", index)
	}
	if cp := st.Checkpoints.LatestByStep(domain.ChapterScope(1), "revision_sync"); cp == nil {
		t.Fatal("缺少 revision_sync checkpoint")
	}
}

func TestServiceResumesProjectionWithoutCallingModel(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	oldFacts := domain.ChapterFacts{Title: "第一章", Summary: "旧摘要", KeyEvents: []string{"旧事件"}}
	acceptTestChapter(t, st, 1, "旧正文", oldFacts)
	newFacts := domain.ChapterFacts{Title: "第一章", Summary: "新摘要", KeyEvents: []string{"新事件"}}
	record := testRecord(1, "用户正文", newFacts, domain.StyleDelta{}, time.Now())
	record.Revision = 2
	if err := st.Drafts.SaveFinalChapter(1, record.Content); err != nil {
		t.Fatal(err)
	}
	if err := st.ChapterRecords.Save(record); err != nil {
		t.Fatal(err)
	}
	pending := domain.PendingRevision{
		Stage:     domain.RevisionStageRecordsApplied,
		Items:     []domain.PendingRevisionItem{{Chapter: 1, Record: record, Analysis: domain.RevisionAnalysis{Facts: newFacts}}},
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Revisions.SavePending(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(st, llmcontract.Model{}, "", nil).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summaries.LoadSummary(1)
	if summary == nil || summary.Summary != "新摘要" {
		t.Fatalf("恢复后摘要未投影: %+v", summary)
	}
	if pending, _ := st.Revisions.LoadPending(); pending != nil {
		t.Fatalf("恢复记录未清理: %+v", pending)
	}
}

func TestServiceResumesPreparedAfterRecordWasAlreadyWritten(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	oldFacts := domain.ChapterFacts{Title: "第一章", Summary: "旧摘要", KeyEvents: []string{"旧事件"}}
	acceptTestChapter(t, st, 1, "旧正文", oldFacts)
	base, err := st.ChapterRecords.Load(1)
	if err != nil {
		t.Fatal(err)
	}
	newFacts := domain.ChapterFacts{Title: "第一章", Summary: "新摘要", KeyEvents: []string{"新事件"}}
	record := testRecord(1, "用户正文", newFacts, domain.StyleDelta{}, time.Now())
	record.Revision = base.Revision + 1
	if err := st.Drafts.SaveFinalChapter(1, record.Content); err != nil {
		t.Fatal(err)
	}
	if err := st.ChapterRecords.Save(record); err != nil {
		t.Fatal(err)
	}
	pending := domain.PendingRevision{
		Stage: domain.RevisionStagePrepared,
		Items: []domain.PendingRevisionItem{{
			Chapter: 1, BaseSHA256: base.ContentSHA256, CurrentSHA256: record.ContentSHA256,
			Record: record, Analysis: domain.RevisionAnalysis{Facts: newFacts},
		}},
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Revisions.SavePending(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(st, llmcontract.Model{}, "", nil).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summaries.LoadSummary(1)
	if summary == nil || summary.Summary != "新摘要" {
		t.Fatalf("prepared 恢复未重建投影: %+v", summary)
	}
	if pending, _ := st.Revisions.LoadPending(); pending != nil {
		t.Fatalf("prepared 恢复记录未清理: %+v", pending)
	}
}

func TestServiceResumesPartiallyWrittenPreparedBatch(t *testing.T) {
	st := newRevisionTestStore(t, 2)
	oldFacts := func(chapter int) domain.ChapterFacts {
		return domain.ChapterFacts{Title: "旧标题", Summary: "旧摘要", KeyEvents: []string{"旧事件"}}
	}
	acceptTestChapter(t, st, 1, "旧正文一", oldFacts(1))
	acceptTestChapter(t, st, 2, "旧正文二", oldFacts(2))
	items := make([]domain.PendingRevisionItem, 0, 2)
	for chapter := 1; chapter <= 2; chapter++ {
		base, err := st.ChapterRecords.Load(chapter)
		if err != nil {
			t.Fatal(err)
		}
		facts := domain.ChapterFacts{Title: "新标题", Summary: fmt.Sprintf("新摘要%d", chapter), KeyEvents: []string{"新事件"}}
		content := fmt.Sprintf("用户正文%d", chapter)
		record := testRecord(chapter, content, facts, domain.StyleDelta{}, time.Now())
		record.Revision = base.Revision + 1
		if err := st.Drafts.SaveFinalChapter(chapter, content); err != nil {
			t.Fatal(err)
		}
		items = append(items, domain.PendingRevisionItem{
			Chapter: chapter, BaseSHA256: base.ContentSHA256, CurrentSHA256: record.ContentSHA256,
			Record: record, Analysis: domain.RevisionAnalysis{Facts: facts},
		})
	}
	if err := st.ChapterRecords.Save(items[0].Record); err != nil {
		t.Fatal(err)
	}
	pending := domain.PendingRevision{Stage: domain.RevisionStagePrepared, Items: items, StartedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.Revisions.SavePending(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(st, llmcontract.Model{}, "", nil).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		record, _ := st.ChapterRecords.Load(chapter)
		summary, _ := st.Summaries.LoadSummary(chapter)
		if record == nil || record.Revision != 2 || summary == nil || summary.Summary != fmt.Sprintf("新摘要%d", chapter) {
			t.Fatalf("chapter %d not recovered: record=%+v summary=%+v", chapter, record, summary)
		}
	}
}

func TestServiceRejectsAndClearsStalePreparedAnalysis(t *testing.T) {
	st := newRevisionTestStore(t, 1)
	facts := domain.ChapterFacts{Title: "第一章", Summary: "摘要", KeyEvents: []string{"事件"}}
	acceptTestChapter(t, st, 1, "系统正文", facts)
	path := filepath.Join(st.Dir(), "chapters", "01.md")
	if err := os.WriteFile(path, []byte("第一次修改"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, _ := st.ChapterRecords.Load(1)
	record := testRecord(1, "第一次修改", facts, domain.StyleDelta{}, time.Now())
	record.Revision = 2
	pending := domain.PendingRevision{
		Stage: domain.RevisionStagePrepared,
		Items: []domain.PendingRevisionItem{{
			Chapter: 1, BaseSHA256: base.ContentSHA256,
			CurrentSHA256: domain.ChapterContentSHA256("第一次修改"), Record: record,
		}},
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Revisions.SavePending(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("第二次修改"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(st, llmcontract.Model{}, "", nil).Sync(context.Background()); err == nil {
		t.Fatal("分析后再次修改正文应拒绝应用")
	}
	if pending, _ := st.Revisions.LoadPending(); pending != nil {
		t.Fatalf("过期 prepared 记录应被清理: %+v", pending)
	}
}

// revisionTestModel 构造无网络的修订模型：litellmtest provider 按脚本返回文本。
func revisionTestModel(t *testing.T, response string) llmcontract.Model {
	t.Helper()
	client, err := litellm.New(litellmtest.New(litellmtest.Text(response)))
	if err != nil {
		t.Fatalf("构造 client: %v", err)
	}
	return llmcontract.Model{
		Core:      agentcore.Model{Client: client, Request: litellm.Request{Model: "test-model"}},
		Provider:  "test",
		ModelName: "test-model",
	}
}

type recordingStyleIndex struct {
	chapter int
	text    string
}

func (i *recordingStyleIndex) ChapterCommitted(chapter int, text string) {
	i.chapter, i.text = chapter, text
}

func newRevisionTestStore(t *testing.T, total int) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(total); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	return st
}

func acceptTestChapter(t *testing.T, st *store.Store, chapter int, content string, facts domain.ChapterFacts) {
	t.Helper()
	if err := st.Drafts.SaveFinalChapter(chapter, content); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChapterRecords.Accept(chapter, domain.ChapterOriginGenerated, content, facts, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.StartChapter(chapter); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(chapter, len([]rune(content)), facts.HookType, facts.DominantStrand); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter: chapter, Title: facts.Title, Summary: facts.Summary, Characters: facts.Characters, KeyEvents: facts.KeyEvents,
	}); err != nil {
		t.Fatal(err)
	}
}

func testRecord(chapter int, content string, facts domain.ChapterFacts, style domain.StyleDelta, acceptedAt time.Time) domain.ChapterRecord {
	return domain.ChapterRecord{
		Version: domain.ChapterRecordVersion, Chapter: chapter, Revision: 1, Origin: domain.ChapterOriginUser,
		Content: content, ContentSHA256: domain.ChapterContentSHA256(content), Facts: facts, StyleDelta: style, AcceptedAt: acceptedAt,
	}
}

func saveLegacyChapter(t *testing.T, st *store.Store, chapter int, draft, final string, facts domain.ChapterFacts) {
	t.Helper()
	if draft != "" {
		if err := st.Drafts.SaveDraft(chapter, draft); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Drafts.SaveFinalChapter(chapter, final); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter: chapter, Title: facts.Title, Summary: facts.Summary,
		Characters: facts.Characters, KeyEvents: facts.KeyEvents,
	}); err != nil {
		t.Fatal(err)
	}
	timeline := make([]domain.TimelineEvent, len(facts.TimelineEvents))
	for i, event := range facts.TimelineEvents {
		event.Chapter = chapter
		timeline[i] = event
	}
	if err := st.World.AppendTimelineEvents(timeline); err != nil {
		t.Fatal(err)
	}
	if err := st.World.UpdateForeshadow(chapter, facts.ForeshadowUpdates); err != nil {
		t.Fatal(err)
	}
	relationships := make([]domain.RelationshipEntry, len(facts.RelationshipChanges))
	for i, relation := range facts.RelationshipChanges {
		relation.Chapter = chapter
		relationships[i] = relation
	}
	if err := st.World.UpdateRelationships(relationships); err != nil {
		t.Fatal(err)
	}
	changes := make([]domain.StateChange, len(facts.StateChanges))
	for i, change := range facts.StateChanges {
		change.Chapter = chapter
		changes[i] = change
	}
	if err := st.World.AppendStateChanges(changes); err != nil {
		t.Fatal(err)
	}
	characters, err := st.Characters.Load()
	if err != nil {
		t.Fatal(err)
	}
	core := make(map[string]bool)
	for _, character := range characters {
		core[character.Name] = true
		for _, alias := range character.Aliases {
			core[alias] = true
		}
	}
	entries, err := loadLegacyCast(st)
	if err != nil {
		t.Fatal(err)
	}
	intros := make(map[string]string, len(facts.CastIntros))
	for _, intro := range facts.CastIntros {
		intros[intro.Name] = intro.BriefRole
	}
	for _, name := range facts.Characters {
		if core[name] {
			continue
		}
		index := slices.IndexFunc(entries, func(entry legacyCastEntry) bool {
			return entry.Name == name || slices.Contains(entry.Aliases, name)
		})
		if index < 0 {
			entries = append(entries, legacyCastEntry{Name: name, FirstSeenChapter: chapter})
			index = len(entries) - 1
		}
		entry := &entries[index]
		if entry.BriefRole == "" {
			entry.BriefRole = intros[name]
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(), "meta", "cast_ledger.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.StartChapter(chapter); err != nil {
		t.Fatal(err)
	}
	content := draft
	if content == "" {
		content = final
	}
	if err := st.Progress.MarkChapterComplete(chapter, len([]rune(content)), facts.HookType, facts.DominantStrand); err != nil {
		t.Fatal(err)
	}
}
