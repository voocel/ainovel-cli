package imp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ChapterCommitter 是发布章节所需的最小接口，由 tools.CommitChapterTool 满足。
// 复用其 PendingCommit saga、checkpoint 与完成章节幂等检查，不复制第二套提交逻辑（RFC §12.3）。
type ChapterCommitter interface {
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// publishFoundation 按正式依赖顺序发布 Foundation，与 Architect 长篇落盘顺序一致（RFC §12.2）。
// 重复发布相同内容是幂等的（Store 覆盖同内容 + checkpoint 去重）。
func publishFoundation(st *store.Store, f *Foundation) error {
	// 发布前冲突对账：已存在且不同的正式工件拒绝覆盖（§12.2 / 不变量 6）。
	// 相同内容按幂等继续写（Store 覆盖同内容 + checkpoint 去重）。
	if err := checkFoundationConflicts(st, f); err != nil {
		return err
	}
	if err := st.RunMeta.SetPlanningTier(f.PlanningTier); err != nil {
		return fmt.Errorf("planning tier：%w", err)
	}
	// premise
	if err := st.Outline.SavePremise(f.Premise); err != nil {
		return fmt.Errorf("premise：%w", err)
	}
	if name := domain.ExtractNovelNameFromPremise(f.Premise); name != "" {
		if err := st.Progress.SetNovelName(name); err != nil {
			return fmt.Errorf("novel name：%w", err)
		}
	}
	if err := st.Progress.UpdatePhase(domain.PhasePremise); err != nil {
		return fmt.Errorf("phase premise：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "premise", "premise.md"); err != nil {
		return fmt.Errorf("checkpoint premise：%w", err)
	}
	// characters
	if err := st.Characters.Save(f.Characters); err != nil {
		return fmt.Errorf("characters：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "characters", "characters.json"); err != nil {
		return fmt.Errorf("checkpoint characters：%w", err)
	}
	// world rules
	if err := st.World.SaveWorldRules(f.WorldRules); err != nil {
		return fmt.Errorf("world_rules：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "world_rules", "world_rules.json"); err != nil {
		return fmt.Errorf("checkpoint world_rules：%w", err)
	}
	// layered + flat outline
	if err := st.Outline.SaveLayeredOutline(f.Volumes); err != nil {
		return fmt.Errorf("layered outline：%w", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(f.Volumes)); err != nil {
		return fmt.Errorf("flat outline：%w", err)
	}
	// 大纲阶段的进度是引擎重算路由的依据（总章数/分层/当前卷弧），写入失败会留下不一致的
	// 已发布状态，必须暴露而非吞掉（RFC §12.2）。
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		return fmt.Errorf("phase outline：%w", err)
	}
	if err := st.Progress.SetTotalChapters(domain.TotalChapters(f.Volumes)); err != nil {
		return fmt.Errorf("total chapters：%w", err)
	}
	if err := st.Progress.SetLayered(true); err != nil {
		return fmt.Errorf("set layered：%w", err)
	}
	if len(f.Volumes) > 0 && len(f.Volumes[0].Arcs) > 0 {
		if err := st.Progress.UpdateVolumeArc(f.Volumes[0].Index, f.Volumes[0].Arcs[0].Index); err != nil {
			return fmt.Errorf("volume arc：%w", err)
		}
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "layered_outline", "layered_outline.json"); err != nil {
		return fmt.Errorf("checkpoint layered outline：%w", err)
	}
	// compass
	if err := st.Outline.SaveCompass(f.Compass); err != nil {
		return fmt.Errorf("compass：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "compass", "meta/compass.json"); err != nil {
		return fmt.Errorf("checkpoint compass：%w", err)
	}
	// 导入 Foundation 的全部正式写入均已成功，可以显式进入 writing。
	// 不能复用普通创作流程的 FoundationMissing：导入允许 world_rules 为空，
	// 把“合法空值”当成缺失会令进度永远停在 outline，随后 StartChapter 被阶段门禁拒绝。
	p, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress：%w", err)
	}
	if p == nil {
		return fmt.Errorf("load progress：progress 未初始化")
	}
	if p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete {
		if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
			return fmt.Errorf("phase writing：%w", err)
		}
	}
	return nil
}

// checkFoundationConflicts 校验待发布 Foundation 与既有正式工件的一致性：
// 既有为空视为首次发布；相同视为幂等；不同则报冲突不覆盖（RFC §12.2 / 不变量 6）。
// compass 与扁平大纲由分层大纲派生，分层一致即派生一致，故只查四个源工件。
// 读错误不得吞成"文件不存在"：store 加载器对缺失返回 (零值, nil)，故任何非 nil 都是真实错误
// （损坏/权限/JSON 非法），若当作空值继续会覆盖无法读取的正式工件（RFC §12.2）。
func checkFoundationConflicts(st *store.Store, f *Foundation) error {
	cur, err := st.Outline.LoadPremise()
	if err != nil {
		return fmt.Errorf("读取正式 premise：%w", err)
	}
	if cur != "" && cur != f.Premise {
		return fmt.Errorf("正式 premise 与导入综合冲突（已存在不同版本），拒绝覆盖")
	}
	chars, err := st.Characters.Load()
	if err != nil {
		return fmt.Errorf("读取正式 characters：%w", err)
	}
	if len(chars) > 0 && !jsonEqual(chars, f.Characters) {
		return fmt.Errorf("正式 characters 与导入综合冲突（已存在不同版本），拒绝覆盖")
	}
	rules, err := st.World.LoadWorldRules()
	if err != nil {
		return fmt.Errorf("读取正式 world_rules：%w", err)
	}
	if len(rules) > 0 && !jsonEqual(rules, f.WorldRules) {
		return fmt.Errorf("正式 world_rules 与导入综合冲突（已存在不同版本），拒绝覆盖")
	}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("读取正式 layered_outline：%w", err)
	}
	if len(layered) > 0 && !jsonEqual(layered, f.Volumes) {
		return fmt.Errorf("正式 layered_outline 与导入综合冲突（已存在不同版本），拒绝覆盖")
	}
	return nil
}

// jsonEqual 按规范化 JSON 字节比较两个值是否等价。
func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// publishChapter 复用 commit_chapter 发布单章；已完成章节由其幂等检查跳过（RFC §12.3）。
func publishChapter(ctx context.Context, st *store.Store, commit ChapterCommitter, chapter int, content string, f ImportedChapterFacts) error {
	completed, err := st.Progress.IsChapterCompleted(chapter)
	if err != nil {
		return fmt.Errorf("load progress ch%d：%w", chapter, err)
	}
	if completed {
		// 崩溃可能落在 MarkChapterComplete 与 ClearPendingCommit 之间：pending_commit 残留
		// 指向本章。直接跳过会绕开 commit 工具专为此窗口准备的清理分支（补 checkpoint+清残留），
		// 下一章 Execute 将以「存在未恢复的章节提交」拒绝，导入每次重跑都死在同一处且
		// 需手工删 meta/pending_commit.json 才能解锁。命中残留时仍走工具幂等路径完成清理。
		pending, err := st.Signals.LoadPendingCommit()
		if err != nil {
			return fmt.Errorf("load pending commit ch%d：%w", chapter, err)
		}
		if pending != nil && pending.Chapter == chapter {
			raw, err := json.Marshal(commitArgs(chapter, f))
			if err != nil {
				return fmt.Errorf("marshal commit ch%d：%w", chapter, err)
			}
			if _, err := commit.Execute(ctx, raw); err != nil {
				return fmt.Errorf("commit ch%d：%w", chapter, err)
			}
		}
		return nil
	}
	if err := st.Drafts.SaveDraft(chapter, content); err != nil {
		return fmt.Errorf("save draft ch%d：%w", chapter, err)
	}
	if err := st.Progress.StartChapter(chapter); err != nil {
		return fmt.Errorf("start ch%d：%w", chapter, err)
	}
	raw, err := json.Marshal(commitArgs(chapter, f))
	if err != nil {
		return fmt.Errorf("marshal commit ch%d：%w", chapter, err)
	}
	if _, err := commit.Execute(ctx, raw); err != nil {
		return fmt.Errorf("commit ch%d：%w", chapter, err)
	}
	return nil
}

// commitArgs 把逐章事实映射为 commit_chapter 入参。
func commitArgs(chapter int, f ImportedChapterFacts) map[string]any {
	keyEvents := f.KeyEvents
	if len(keyEvents) == 0 {
		keyEvents = []string{f.CoreEvent} // core_event 已校验非空
	}
	args := map[string]any{
		"chapter":         chapter,
		"summary":         f.Summary,
		"characters":      f.Characters,
		"key_events":      keyEvents,
		"hook_type":       f.HookType,
		"dominant_strand": f.DominantStrand,
	}
	if len(f.TimelineEvents) > 0 {
		args["timeline_events"] = f.TimelineEvents
	}
	if len(f.ForeshadowUpdates) > 0 {
		args["foreshadow_updates"] = f.ForeshadowUpdates
	}
	if len(f.RelationshipChanges) > 0 {
		args["relationship_changes"] = f.RelationshipChanges
	}
	if len(f.StateChanges) > 0 {
		args["state_changes"] = f.StateChanges
	}
	return args
}

// isPublished 判断正式状态是否已反映完整导入：Foundation 已落盘且已完成章节达到预期。
// 只对账导入真正产出的工件——premise、覆盖全章的扁平大纲、完成章节——而不复用
// FoundationMissing()：后者是普通创作流程的“可写作”门禁，会把合法为空的 world_rules
// 误判为未完成，导致发布对账永不收敛（RFC §12.3）。
func isPublished(st *store.Store, expected int) (bool, error) {
	if expected == 0 {
		return false, nil
	}
	p, err := st.Outline.LoadPremise()
	if err != nil {
		return false, fmt.Errorf("读取正式 premise: %w", err)
	}
	if p == "" {
		return false, nil
	}
	o, err := st.Outline.LoadOutline()
	if err != nil {
		return false, fmt.Errorf("读取正式 outline: %w", err)
	}
	if len(o) < expected {
		return false, nil
	}
	prog, err := st.Progress.Load()
	if err != nil {
		return false, fmt.Errorf("读取正式 progress: %w", err)
	}
	return prog != nil && len(prog.CompletedChapters) >= expected, nil
}
