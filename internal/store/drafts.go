package store

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveChapterPlan 保存章节构思到 drafts/{ch}.plan.json。
func (s *Store) SaveChapterPlan(plan domain.ChapterPlan) error {
	return s.writeJSON(fmt.Sprintf("drafts/%02d.plan.json", plan.Chapter), plan)
}

// LoadChapterPlan 读取章节构思。
func (s *Store) LoadChapterPlan(chapter int) (*domain.ChapterPlan, error) {
	var plan domain.ChapterPlan
	if err := s.readJSON(fmt.Sprintf("drafts/%02d.plan.json", chapter), &plan); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &plan, nil
}

// SaveDraft 保存整章草稿到 drafts/{ch}.draft.md。
func (s *Store) SaveDraft(chapter int, content string) error {
	return s.writeMarkdown(fmt.Sprintf("drafts/%02d.draft.md", chapter), content)
}

// AppendDraft 追加内容到现有草稿（续写模式）。
// 读取和写入在同一个写锁内完成，防止并发追加丢失数据。
func (s *Store) AppendDraft(chapter int, content string) error {
	rel := fmt.Sprintf("drafts/%02d.draft.md", chapter)
	return s.withWriteLock(func() error {
		existing, err := s.readFileUnlocked(rel)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var merged string
		if len(existing) > 0 {
			merged = string(existing) + "\n\n" + content
		} else {
			merged = content
		}
		return s.writeFileUnlocked(rel, []byte(merged))
	})
}

// LoadDraft 读取整章草稿。
func (s *Store) LoadDraft(chapter int) (string, error) {
	data, err := s.readFile(fmt.Sprintf("drafts/%02d.draft.md", chapter))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// LoadChapterContent 加载章节草稿正文及字数。
func (s *Store) LoadChapterContent(chapter int) (string, int, error) {
	draft, err := s.LoadDraft(chapter)
	if err != nil {
		return "", 0, err
	}
	if draft != "" {
		return draft, utf8.RuneCountInString(draft), nil
	}
	return "", 0, nil
}

// SaveFinalChapter 保存最终章节正文到 chapters/{ch}.md。
func (s *Store) SaveFinalChapter(chapter int, content string) error {
	return s.writeMarkdown(fmt.Sprintf("chapters/%02d.md", chapter), content)
}

// LoadChapterText 读取已提交的终稿原文。
func (s *Store) LoadChapterText(chapter int) (string, error) {
	data, err := s.readFile(fmt.Sprintf("chapters/%02d.md", chapter))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// LoadChapterRange 读取指定范围的终稿原文片段（每章截取前 maxRunes 个字符）。
func (s *Store) LoadChapterRange(from, to, maxRunes int) (map[int]string, error) {
	result := make(map[int]string)
	for ch := from; ch <= to; ch++ {
		text, err := s.LoadChapterText(ch)
		if err != nil {
			return nil, err
		}
		if text == "" {
			continue
		}
		if maxRunes > 0 {
			runes := []rune(text)
			if len(runes) > maxRunes {
				text = string(runes[:maxRunes]) + "..."
			}
		}
		result[ch] = text
	}
	return result, nil
}

// dialogueRe 匹配中文引号对话。
var dialogueRe = regexp.MustCompile(`"[^"]*"`)

// maxCompletedChapter 返回已完成的最大章节号，用于对话提取和风格锚点采样。
// 无进度时回退到 99 作为安全上限。
func (s *Store) maxCompletedChapter() int {
	p, err := s.LoadProgress()
	if err != nil || p == nil || len(p.CompletedChapters) == 0 {
		return 99
	}
	m := 0
	for _, ch := range p.CompletedChapters {
		if ch > m {
			m = ch
		}
	}
	return m
}

// ExtractDialogue 从已提交章节中提取指定角色的对话片段。
// 通过检查对话所在段落是否包含角色名/别名来关联。
func (s *Store) ExtractDialogue(characterName string, aliases []string, maxSamples int) []string {
	if maxSamples <= 0 {
		maxSamples = 5
	}
	names := append([]string{characterName}, aliases...)

	maxCh := s.maxCompletedChapter()
	var samples []string
	// 从最近的章节开始向前搜索
	for ch := maxCh; ch >= 1 && len(samples) < maxSamples; ch-- {
		text, err := s.LoadChapterText(ch)
		if err != nil || text == "" {
			continue
		}
		paragraphs := strings.Split(text, "\n")
		for _, para := range paragraphs {
			if len(samples) >= maxSamples {
				break
			}
			// 段落中要包含角色名
			found := false
			for _, name := range names {
				if strings.Contains(para, name) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			// 提取该段落中的对话
			matches := dialogueRe.FindAllString(para, -1)
			for _, m := range matches {
				if len(samples) >= maxSamples {
					break
				}
				if utf8.RuneCountInString(m) > 5 { // 过滤太短的
					samples = append(samples, characterName+": "+m)
				}
			}
		}
	}
	return samples
}

// ExtractStyleAnchors 从已提交章节中提取代表性段落作为风格锚点。
// 选取描写密度高（非对话、非短句）的段落。
func (s *Store) ExtractStyleAnchors(maxAnchors int) []string {
	if maxAnchors <= 0 {
		maxAnchors = 5
	}

	maxCh := s.maxCompletedChapter()
	var anchors []string
	// 从第 1 章开始，均匀采样
	for ch := 1; ch <= maxCh && len(anchors) < maxAnchors; ch++ {
		text, err := s.LoadChapterText(ch)
		if err != nil || text == "" {
			continue
		}
		paragraphs := strings.Split(text, "\n\n")
		for _, para := range paragraphs {
			if len(anchors) >= maxAnchors {
				break
			}
			para = strings.TrimSpace(para)
			runeCount := utf8.RuneCountInString(para)
			// 选取 50-300 字的非对话段落
			if runeCount < 50 || runeCount > 300 {
				continue
			}
			// 跳过纯对话段落
			if strings.Count(para, "\u201c") > 2 {
				continue
			}
			anchors = append(anchors, para)
		}
	}
	return anchors
}
