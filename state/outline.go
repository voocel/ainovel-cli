package state

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/domain"
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
	IsArcEnd    bool // 是否为弧内最后一章
	IsVolumeEnd bool // 是否同时为卷内最后一章
	Volume      int  // 当前章所在卷
	Arc         int  // 当前章所在弧
	NextVolume  int  // 下一章所在卷（0 = 全书结束）
	NextArc     int  // 下一章所在弧（0 = 全书结束）
}

// CheckArcBoundary 检查某章是否为弧/卷的最后一章。
// 非分层大纲或未找到章节时返回 nil。
func (s *Store) CheckArcBoundary(chapter int) (*ArcBoundary, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}

	type chapterPos struct {
		volume, arc, indexInArc, arcLen int
		isLastArc                       bool
	}

	// 构建全局章节号 → 位置映射
	ch := 1
	var cur *chapterPos
	var nextVol, nextArc int
	for _, v := range volumes {
		for ai, a := range v.Arcs {
			for ci := range a.Chapters {
				if ch == chapter {
					cur = &chapterPos{
						volume:     v.Index,
						arc:        a.Index,
						indexInArc: ci,
						arcLen:     len(a.Chapters),
						isLastArc:  ai == len(v.Arcs)-1,
					}
				} else if cur != nil && nextVol == 0 {
					// 紧跟 cur 的下一章
					nextVol = v.Index
					nextArc = a.Index
				}
				ch++
			}
		}
	}
	if cur == nil {
		return nil, nil
	}

	b := &ArcBoundary{
		Volume:     cur.volume,
		Arc:        cur.arc,
		NextVolume: nextVol,
		NextArc:    nextArc,
	}
	if cur.indexInArc == cur.arcLen-1 {
		b.IsArcEnd = true
		if cur.isLastArc {
			b.IsVolumeEnd = true
		}
	}
	return b, nil
}

func renderLayeredOutline(volumes []domain.VolumeOutline) string {
	var b strings.Builder
	b.WriteString("# 分层大纲\n\n")
	ch := 1
	for _, v := range volumes {
		fmt.Fprintf(&b, "## 第 %d 卷：%s\n\n", v.Index, v.Title)
		fmt.Fprintf(&b, "**主题**：%s\n\n", v.Theme)
		for _, a := range v.Arcs {
			fmt.Fprintf(&b, "### 第 %d 弧：%s\n\n", a.Index, a.Title)
			fmt.Fprintf(&b, "**目标**：%s\n\n", a.Goal)
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
