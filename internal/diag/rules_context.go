package diag

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

// DeadCharacterAppears 检测"已记录死亡的角色在更晚章节仍出场"。
// 出场依据章节摘要的 Characters 名单；死亡依据 state_changes 中该章之前最后一条 status。
// 复活剧情（死后又有非死亡 status 记录）自动豁免。
func DeadCharacterAppears(snap *Snapshot) []Finding {
	if len(snap.StateChanges) == 0 || len(snap.Summaries) == 0 {
		return nil
	}
	// 别名 → 正名
	alias := make(map[string]string)
	for _, c := range snap.Characters {
		for _, a := range c.Aliases {
			alias[a] = c.Name
		}
	}
	// 每个实体按时间序的 status 变化（切片顺序即追加顺序）
	statusSeq := make(map[string][]domain.StateChange)
	for _, sc := range snap.StateChanges {
		if sc.Field == "status" {
			statusSeq[sc.Entity] = append(statusSeq[sc.Entity], sc)
		}
	}
	var hits []string
	for ch, sum := range snap.Summaries {
		if sum == nil {
			continue
		}
		for _, name := range sum.Characters {
			canon := name
			if c, ok := alias[name]; ok {
				canon = c
			}
			// 找出该实体在 ch 之前最后一条 status 记录
			var last *domain.StateChange
			for i := range statusSeq[canon] {
				if statusSeq[canon][i].Chapter < ch {
					last = &statusSeq[canon][i]
				}
			}
			if last != nil && domain.IsDeadValue(last.NewValue) {
				hits = append(hits, fmt.Sprintf("%s(死于ch%d,ch%d仍出场)", canon, last.Chapter, ch))
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Strings(hits) // map 遍历无序，排序保证输出与测试确定性
	return []Finding{{
		Rule:       "DeadCharacterAppears",
		Category:   CatContext,
		Severity:   SevCritical,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.characters",
		Title:      fmt.Sprintf("死亡角色出场: %d 处", len(hits)),
		Evidence:   strings.Join(hits, "; "),
		Suggestion: "核对相应章节：若为闪回/复活剧情，补 state_changes 修正状态；否则需要重写相关段落。",
	}}
}

// GhostCharacter 检测 core/important 角色长期未出现。
func GhostCharacter(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Characters) == 0 || len(snap.Summaries) == 0 {
		return nil
	}
	completed := snap.CompletedCount()
	if completed < 5 {
		return nil
	}

	// 计算每个角色最后出现的章节号
	lastSeen := make(map[string]int)
	for ch, s := range snap.Summaries {
		for _, name := range s.Characters {
			if ch > lastSeen[name] {
				lastSeen[name] = ch
			}
		}
	}

	threshold := completed / 3
	if threshold < 5 {
		threshold = 5
	}
	latest := snap.LatestCompleted()

	var ghosts []string
	for _, c := range snap.Characters {
		if c.Tier != "core" && c.Tier != "important" {
			continue
		}
		seen, ok := lastSeen[c.Name]
		if !ok {
			// 也检查别名
			for _, alias := range c.Aliases {
				if s, exists := lastSeen[alias]; exists && s > seen {
					seen = s
					ok = true
				}
			}
		}
		gap := latest - seen
		if !ok {
			ghosts = append(ghosts, fmt.Sprintf("%s(从未出现在摘要中)", c.Name))
		} else if gap > threshold {
			ghosts = append(ghosts, fmt.Sprintf("%s(最后出现ch%d,已缺席%d章)", c.Name, seen, gap))
		}
	}
	if len(ghosts) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "GhostCharacter",
		Category:   CatContext,
		Severity:   SevInfo,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.characters",
		Title:      fmt.Sprintf("角色消失: %d 个核心角色长期缺席", len(ghosts)),
		Evidence:   strings.Join(ghosts, "; "),
		Suggestion: "Writer 可能丢失了该角色的追踪。考虑直接在输入框提交干预指令重新引入该角色，或在 characters.json 中降级其 tier。",
	}}
}

// TimelineGaps 检测已完成章节缺少时间线事件。
func TimelineGaps(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.CompletedChapters) == 0 {
		return nil
	}
	if len(snap.Timeline) == 0 && snap.CompletedCount() > 0 {
		return []Finding{{
			Rule:       "TimelineGaps",
			Category:   CatContext,
			Severity:   SevInfo,
			Confidence: ConfMedium,
			AutoLevel:  AutoNone,
			Target:     "context.timeline",
			Title:      "时间线为空",
			Evidence:   fmt.Sprintf("completed=%d, timeline_events=0", snap.CompletedCount()),
			Suggestion: "commit_chapter 的时间线提取可能未生效。检查 Writer 输出是否包含 timeline 字段。",
		}}
	}

	// 建立章节→事件映射
	chaptersWithEvents := make(map[int]bool)
	for _, e := range snap.Timeline {
		chaptersWithEvents[e.Chapter] = true
	}

	var missing []int
	for _, ch := range snap.Progress.CompletedChapters {
		if !chaptersWithEvents[ch] {
			missing = append(missing, ch)
		}
	}
	// 允许少量缺失（某些过渡章可能确实无重大事件）
	if len(missing) == 0 || float64(len(missing))/float64(snap.CompletedCount()) < ThresholdTimelineGapRate {
		return nil
	}
	return []Finding{{
		Rule:       "TimelineGaps",
		Category:   CatContext,
		Severity:   SevInfo,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.timeline",
		Title:      fmt.Sprintf("时间线缺口: %d 章无事件记录", len(missing)),
		Evidence:   fmt.Sprintf("missing=[%s]", intsToStr(missing)),
		Suggestion: "commit_chapter 的时间线提取可能部分失效。检查 Writer 输出的 timeline 字段格式。",
	}}
}

// RelationshipStagnation 检测关系数据停止更新。
func RelationshipStagnation(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Relationships) == 0 {
		return nil
	}
	completed := snap.CompletedCount()
	if completed < 6 {
		return nil
	}

	// 找到关系数据的最新章节
	latestRelCh := 0
	for _, r := range snap.Relationships {
		if r.Chapter > latestRelCh {
			latestRelCh = r.Chapter
		}
	}

	// 如果最新关系数据在前 1/3，判定为停滞
	cutoff := snap.LatestCompleted() - completed/3
	if latestRelCh >= cutoff {
		return nil
	}
	return []Finding{{
		Rule:       "RelationshipStagnation",
		Category:   CatContext,
		Severity:   SevInfo,
		Confidence: ConfLow,
		AutoLevel:  AutoNone,
		Target:     "context.relationships",
		Title:      fmt.Sprintf("关系数据停滞: 最新更新在第 %d 章", latestRelCh),
		Evidence:   fmt.Sprintf("relationship_entries=%d, latest_update=ch%d, latest_completed=ch%d", len(snap.Relationships), latestRelCh, snap.LatestCompleted()),
		Suggestion: "commit_chapter 的关系更新可能停止工作，或故事关系确实无变化。检查 Writer 输出的 relationships 字段。",
	}}
}
