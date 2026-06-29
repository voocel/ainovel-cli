package diag

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// StaleForeshadow 检测长期未推进的伏笔。
func StaleForeshadow(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Foreshadow) == 0 {
		return nil
	}
	latest := snap.LatestCompleted()
	threshold := staleForeshadowThreshold(snap.CompletedCount())

	var stale []string
	for _, f := range snap.Foreshadow {
		if f.Status != "planted" {
			continue
		}
		gap := latest - f.PlantedAt
		if gap > threshold {
			stale = append(stale, fmt.Sprintf("%s(ch%d埋下,已过%d章)", f.ID, f.PlantedAt, gap))
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "StaleForeshadow",
		Category:   CatPlanning,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.foreshadow",
		Title:      fmt.Sprintf("伏笔停滞: %d 条超过 %d 章未推进", len(stale), threshold),
		Evidence:   strings.Join(stale, "; "),
		Suggestion: "novel_context 的伏笔提醒加载可能未生效，或 Writer prompt Thiếu推进伏笔的指引。Kiểm tra foreshadow_ledger 与Ngữ cảnh注入逻辑。",
	}}
}

// CompassDrift 检测指南针长期未更Mới。
func CompassDrift(snap *Snapshot) []Finding {
	if snap.Progress == nil || !snap.Progress.Layered {
		return nil
	}
	if snap.Compass == nil {
		if snap.CompletedCount() > 5 {
			return []Finding{{
				Rule:       "CompassDrift",
				Category:   CatPlanning,
				Severity:   SevWarning,
				Confidence: ConfMedium,
				AutoLevel:  AutoNone,
				Target:     "prompt.architect",
				Title:      "长篇模式Thiếu指南针",
				Evidence:   fmt.Sprintf("layered=true, completed=%d, compass=nil", snap.CompletedCount()),
				Suggestion: "Architect 应在初始规划时Tạo compass。Kiểm tra architect-long.md Có czy không包含 compass Tạo指令。",
			}}
		}
		return nil
	}

	gap := snap.LatestCompleted() - snap.Compass.LastUpdated
	if gap <= ThresholdCompassDrift {
		return nil
	}
	return []Finding{{
		Rule:       "CompassDrift",
		Category:   CatPlanning,
		Severity:   SevInfo,
		Confidence: ConfLow,
		AutoLevel:  AutoNone,
		Target:     "prompt.architect",
		Title:      fmt.Sprintf("指南针已 %d 章未更Mới", gap),
		Evidence:   fmt.Sprintf("last_updated=ch%d, latest=ch%d, open_threads=%d", snap.Compass.LastUpdated, snap.LatestCompleted(), len(snap.Compass.OpenThreads)),
		Suggestion: "Architect 应在弧/卷边界更Mới compass。Kiểm tra architect-long.md 中Có czy không包含 compass 更Mới指令。",
	}}
}

// OutlineExhausted 检测Đại cương耗尽但小说未完结。
func OutlineExhausted(snap *Snapshot) []Finding {
	if snap.Progress == nil {
		return nil
	}
	p := snap.Progress
	if p.Phase == domain.PhaseComplete || p.Phase == domain.PhaseInit {
		return nil
	}

	completed := snap.CompletedCount()
	if completed == 0 {
		return nil
	}

	outlinedCount := p.TotalChapters
	if outlinedCount <= 0 {
		outlinedCount = len(snap.Outline)
	}
	if outlinedCount <= 0 {
		return nil
	}

	if completed < outlinedCount {
		return nil
	}

	return []Finding{{
		Rule:       "OutlineExhausted",
		Category:   CatPlanning,
		Severity:   SevCritical,
		Confidence: ConfHigh,
		AutoLevel:  AutoSafe,
		Target:     "runtime.recovery",
		Title:      fmt.Sprintf("Đại cương耗尽: Đã hoàn thành %d 章 >= 已规划 %d 章", completed, outlinedCount),
		Evidence:   fmt.Sprintf("phase=%s, completed=%d, outlined=%d", p.Phase, completed, outlinedCount),
		Suggestion: "Mở rộng/Mới卷信号可能未触发。Kiểm tra宿主侧Nộp策略和Phục hồi逻辑，Xác nhận弧边界检测、expand_arc 或 append_volume Có czy không正常执行。",
	}}
}

// MissingSummaries 检测Đã hoàn thànhChươngThiếuTóm tắt。
func MissingSummaries(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.CompletedChapters) == 0 {
		return nil
	}

	var missing []int
	for _, ch := range snap.Progress.CompletedChapters {
		if _, ok := snap.Summaries[ch]; !ok {
			missing = append(missing, ch)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "MissingSummaries",
		Category:   CatPlanning,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.flow",
		Title:      fmt.Sprintf("ThiếuTóm tắt: %d 章Không cóTóm tắt", len(missing)),
		Evidence:   fmt.Sprintf("missing=[%s]", intsToStr(missing)),
		Suggestion: "Tóm tắt是Ngữ cảnh连续性的关键。Kiểm tra commit_chapter 的Tóm tắt写入逻辑Có czy không正常工作。",
	}}
}
