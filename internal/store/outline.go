package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SavePremise 保存故事前提到 premise.md。
func (s *Store) SavePremise(content string) error {
	return s.writeMarkdown("premise.md", content)
}

// LoadPremise 读取 premise.md。不存在时返回空字符串。
func (s *Store) LoadPremise() (string, error) {
	data, err := s.readFile("premise.md")
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// SaveOutline 同时保存 outline.json（机器读）和 outline.md（人读）。
func (s *Store) SaveOutline(entries []domain.OutlineEntry) error {
	if err := s.writeJSON("outline.json", entries); err != nil {
		return err
	}
	return s.writeMarkdown("outline.md", renderOutline(entries))
}

// LoadOutline 从 outline.json 读取结构化大纲。
func (s *Store) LoadOutline() ([]domain.OutlineEntry, error) {
	var entries []domain.OutlineEntry
	if err := s.readJSON("outline.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// GetChapterOutline 获取指定章节的大纲条目。
func (s *Store) GetChapterOutline(chapter int) (*domain.OutlineEntry, error) {
	entries, err := s.LoadOutline()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Chapter == chapter {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("chapter %d not found in outline", chapter)
}

// SaveLayeredOutline 保存分层大纲（长篇模式）。
// 同时保存 layered_outline.json（机器读）和 layered_outline.md（人读）。
func (s *Store) SaveLayeredOutline(volumes []domain.VolumeOutline) error {
	if err := s.writeJSON("layered_outline.json", volumes); err != nil {
		return err
	}
	return s.writeMarkdown("layered_outline.md", renderLayeredOutline(volumes))
}

// LoadLayeredOutline 读取分层大纲。
func (s *Store) LoadLayeredOutline() ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.readJSON("layered_outline.json", &volumes); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return volumes, nil
}

// ClearLayeredOutline 清理分层大纲文件，供从长篇降级为普通大纲时使用。
func (s *Store) ClearLayeredOutline() error {
	return s.withWriteLock(func() error {
		if err := s.removeFileUnlocked("layered_outline.json"); err != nil {
			return err
		}
		return s.removeFileUnlocked("layered_outline.md")
	})
}

// GetChapterFromLayered 从分层大纲中按全局章节号查找。
func (s *Store) GetChapterFromLayered(chapter int) (*domain.OutlineEntry, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for i := range a.Chapters {
				if ch == chapter {
					e := a.Chapters[i]
					e.Chapter = ch
					return &e, nil
				}
				ch++
			}
		}
	}
	return nil, fmt.Errorf("chapter %d not found in layered outline", chapter)
}

// LocateChapter 根据全局章节号定位所在的卷和弧。
func (s *Store) LocateChapter(chapter int) (volume, arc int, err error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return 0, 0, err
	}
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for range a.Chapters {
				if ch == chapter {
					return v.Index, a.Index, nil
				}
				ch++
			}
		}
	}
	return 0, 0, fmt.Errorf("chapter %d not found in layered outline", chapter)
}

// ArcBoundary 弧边界信息。
type ArcBoundary struct {
	IsArcEnd       bool // 是否为弧内最后一章
	IsVolumeEnd    bool // 是否同时为卷内最后一章
	Volume         int  // 当前章所在卷
	Arc            int  // 当前章所在弧
	NextVolume           int  // 下一弧所在卷（0 = 全书结束）
	NextArc              int  // 下一弧序号（0 = 全书结束）
	NeedsExpansion       bool // 下一弧是骨架（需要展开章节后才能写作）
	NeedsVolumeExpansion bool // 下一卷是骨架（需要展开弧结构后才能写作）
}

// HasNextArc 是否还有后续弧（含骨架弧）。
func (b *ArcBoundary) HasNextArc() bool {
	return b.NextVolume > 0 || b.NextArc > 0
}

// CheckArcBoundary 检查某章是否为弧/卷的最后一章。
// 能正确识别骨架弧（未展开的弧），在下一弧是骨架时设置 NeedsExpansion。
// 非分层大纲或未找到章节时返回 nil。
func (s *Store) CheckArcBoundary(chapter int) (*ArcBoundary, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}

	// 定位当前章所在的弧
	type arcPos struct {
		volIdx, arcIdx int // volumes/arcs 数组下标
		volume, arc    int // 卷/弧逻辑序号（Index 字段）
		chInArc        int // 当前章在弧内的位置
		arcLen         int // 弧内章节数
	}

	ch := 1
	var cur *arcPos
	for vi, v := range volumes {
		for ai, a := range v.Arcs {
			for ci := range a.Chapters {
				if ch == chapter {
					cur = &arcPos{
						volIdx:  vi,
						arcIdx:  ai,
						volume:  v.Index,
						arc:     a.Index,
						chInArc: ci,
						arcLen:  len(a.Chapters),
					}
				}
				ch++
			}
		}
	}
	if cur == nil {
		return nil, nil
	}

	b := &ArcBoundary{
		Volume: cur.volume,
		Arc:    cur.arc,
	}

	isLastChInArc := cur.chInArc == cur.arcLen-1
	isLastArcInVol := cur.arcIdx == len(volumes[cur.volIdx].Arcs)-1

	if isLastChInArc {
		b.IsArcEnd = true
		if isLastArcInVol {
			b.IsVolumeEnd = true
		}
	}

	// 找下一个弧（含骨架弧和骨架卷）
	found := false
	for vi := cur.volIdx; vi < len(volumes); vi++ {
		startArc := 0
		if vi == cur.volIdx {
			startArc = cur.arcIdx + 1
		}
		// 骨架卷：没有弧结构，需要先展开卷
		if vi != cur.volIdx && !volumes[vi].IsExpanded() {
			b.NextVolume = volumes[vi].Index
			b.NextArc = 0
			b.NeedsVolumeExpansion = true
			found = true
			break
		}
		for ai := startArc; ai < len(volumes[vi].Arcs); ai++ {
			b.NextVolume = volumes[vi].Index
			b.NextArc = volumes[vi].Arcs[ai].Index
			b.NeedsExpansion = !volumes[vi].Arcs[ai].IsExpanded()
			found = true
			break
		}
		if found {
			break
		}
	}

	return b, nil
}

// ExpandArc 将骨架弧展开为详细章节。
// 替换目标弧的 Chapters，清空 EstimatedChapters，重新生成扁平大纲。
func (s *Store) ExpandArc(volumeIdx, arcIdx int, chapters []domain.OutlineEntry) error {
	return s.withWriteLock(func() error {
		var volumes []domain.VolumeOutline
		if err := s.readJSONUnlocked("layered_outline.json", &volumes); err != nil {
			return fmt.Errorf("load layered_outline: %w", err)
		}
		// 按 Index 字段查找目标弧
		found := false
		for vi := range volumes {
			if volumes[vi].Index != volumeIdx {
				continue
			}
			for ai := range volumes[vi].Arcs {
				if volumes[vi].Arcs[ai].Index != arcIdx {
					continue
				}
				volumes[vi].Arcs[ai].Chapters = chapters
				volumes[vi].Arcs[ai].EstimatedChapters = 0
				found = true
				break
			}
			if found {
				break
			}
		}
		if !found {
			return fmt.Errorf("arc not found: volume=%d, arc=%d", volumeIdx, arcIdx)
		}
		// 保存分层大纲
		if err := s.writeJSONUnlocked("layered_outline.json", volumes); err != nil {
			return err
		}
		if err := s.writeFileUnlocked("layered_outline.md", []byte(renderLayeredOutline(volumes))); err != nil {
			return err
		}
		// 重新生成扁平大纲（全局章节号自动连续）
		flat := domain.FlattenOutline(volumes)
		if err := s.writeJSONUnlocked("outline.json", flat); err != nil {
			return err
		}
		if err := s.writeFileUnlocked("outline.md", []byte(renderOutline(flat))); err != nil {
			return err
		}
		// 更新总章节数
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.TotalChapters = domain.TotalChapters(volumes)
		return s.saveProgressUnlocked(p)
	})
}

// ExpandVolume 将骨架卷展开为弧级结构。
// arcs 中可包含骨架弧（只有 goal + estimated_chapters）和展开弧（有 chapters）。
func (s *Store) ExpandVolume(volumeIdx int, arcs []domain.ArcOutline) error {
	return s.withWriteLock(func() error {
		var volumes []domain.VolumeOutline
		if err := s.readJSONUnlocked("layered_outline.json", &volumes); err != nil {
			return fmt.Errorf("load layered_outline: %w", err)
		}
		found := false
		for vi := range volumes {
			if volumes[vi].Index != volumeIdx {
				continue
			}
			volumes[vi].Arcs = arcs
			volumes[vi].EstimatedChapters = 0
			found = true
			break
		}
		if !found {
			return fmt.Errorf("volume not found: %d", volumeIdx)
		}
		// 保存分层大纲
		if err := s.writeJSONUnlocked("layered_outline.json", volumes); err != nil {
			return err
		}
		if err := s.writeFileUnlocked("layered_outline.md", []byte(renderLayeredOutline(volumes))); err != nil {
			return err
		}
		// 重新生成扁平大纲
		flat := domain.FlattenOutline(volumes)
		if err := s.writeJSONUnlocked("outline.json", flat); err != nil {
			return err
		}
		if err := s.writeFileUnlocked("outline.md", []byte(renderOutline(flat))); err != nil {
			return err
		}
		// 更新总章节数
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.TotalChapters = domain.TotalChapters(volumes)
		return s.saveProgressUnlocked(p)
	})
}

func renderLayeredOutline(volumes []domain.VolumeOutline) string {
	var b strings.Builder
	b.WriteString("# 分层大纲\n\n")
	ch := 1
	for _, v := range volumes {
		fmt.Fprintf(&b, "## 第 %d 卷：%s\n\n", v.Index, v.Title)
		fmt.Fprintf(&b, "**主题**：%s\n\n", v.Theme)
		if !v.IsExpanded() {
			fmt.Fprintf(&b, "*（待展开，预估 %d 章）*\n\n", v.EstimatedChapters)
			continue
		}
		for _, a := range v.Arcs {
			fmt.Fprintf(&b, "### 第 %d 弧：%s\n\n", a.Index, a.Title)
			fmt.Fprintf(&b, "**目标**：%s\n\n", a.Goal)
			if !a.IsExpanded() {
				fmt.Fprintf(&b, "*（待展开，预估 %d 章）*\n\n", a.EstimatedChapters)
				continue
			}
			for _, e := range a.Chapters {
				fmt.Fprintf(&b, "#### 第 %d 章：%s\n\n", ch, e.Title)
				fmt.Fprintf(&b, "**核心事件**：%s\n\n", e.CoreEvent)
				if e.Hook != "" {
					fmt.Fprintf(&b, "**钩子**：%s\n\n", e.Hook)
				}
				ch++
			}
		}
	}
	return b.String()
}

func renderOutline(entries []domain.OutlineEntry) string {
	var b strings.Builder
	b.WriteString("# 大纲\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "## 第 %d 章：%s\n\n", e.Chapter, e.Title)
		fmt.Fprintf(&b, "**核心事件**：%s\n\n", e.CoreEvent)
		if e.Hook != "" {
			fmt.Fprintf(&b, "**钩子**：%s\n\n", e.Hook)
		}
		if len(e.Scenes) > 0 {
			b.WriteString("**场景**：\n")
			for i, sc := range e.Scenes {
				fmt.Fprintf(&b, "%d. %s\n", i+1, sc)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
