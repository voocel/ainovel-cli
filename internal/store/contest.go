// internal/store/contest.go
package store

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ContestStore 管理多人格竞稿的候选稿与裁定文件。
type ContestStore struct{ io *IO }

func NewContestStore(io *IO) *ContestStore { return &ContestStore{io: io} }

// candPath 返回某章节某 persona 候选稿的相对路径。
func candPath(chapter int, persona string) string {
	return fmt.Sprintf("drafts/%02d.cand-%s.md", chapter, persona)
}

// SaveCandidate 保存某 persona 的候选稿。
func (s *ContestStore) SaveCandidate(chapter int, persona, content string) error {
	return s.io.WriteMarkdown(candPath(chapter, persona), content)
}

// LoadCandidate 读取某 persona 的候选稿；不存在返回空串。
func (s *ContestStore) LoadCandidate(chapter int, persona string) (string, error) {
	data, err := s.io.ReadFile(candPath(chapter, persona))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ListCandidates 返回给定 persona 列表中已落盘候选稿的存在性映射。
// 不存在的 persona 映射值为 false，确保返回 map 包含全部请求的 persona。
func (s *ContestStore) ListCandidates(chapter int, personas []string) (map[string]bool, error) {
	present := make(map[string]bool, len(personas))
	for _, p := range personas {
		c, err := s.LoadCandidate(chapter, p)
		if err != nil {
			return nil, err
		}
		present[p] = c != ""
	}
	return present, nil
}

// verdictPath 返回某章节裁定结果文件的相对路径。
func verdictPath(chapter int) string {
	return fmt.Sprintf("drafts/%02d.verdict.json", chapter)
}

// SaveVerdict 保存裁定结果。
func (s *ContestStore) SaveVerdict(v domain.Verdict) error {
	return s.io.WriteJSON(verdictPath(v.Chapter), v)
}

// LoadVerdict 读取裁定；不存在返回 nil。
func (s *ContestStore) LoadVerdict(chapter int) (*domain.Verdict, error) {
	var v domain.Verdict
	if err := s.io.ReadJSON(verdictPath(chapter), &v); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &v, nil
}

// SavePersonas 缓存人格映射（key=作者名）。
func (s *ContestStore) SavePersonas(m map[string]domain.Persona) error {
	return s.io.WriteJSON("personas.json", m)
}

// LoadPersonas 读取人格缓存；不存在返回空 map。
func (s *ContestStore) LoadPersonas() (map[string]domain.Persona, error) {
	m := make(map[string]domain.Persona)
	if err := s.io.ReadJSON("personas.json", &m); err != nil {
		if os.IsNotExist(err) {
			return map[string]domain.Persona{}, nil
		}
		return nil, err
	}
	return m, nil
}

// IsPromoted 报告本章中选稿是否已提升为正式 draft.md。
func (s *ContestStore) IsPromoted(chapter int) bool {
	v, err := s.LoadVerdict(chapter)
	return err == nil && v != nil && v.Promoted
}

// PromoteCandidate 把中选候选稿复制为正式 draft.md，再置 verdict.Promoted=true。
// 幂等：先复制（同内容重复无副作用）后置标记，崩溃后重做安全。
func (s *ContestStore) PromoteCandidate(chapter int, winner string) error {
	content, err := s.LoadCandidate(chapter, winner)
	if err != nil {
		return fmt.Errorf("load winner candidate: %w", err)
	}
	if content == "" {
		return fmt.Errorf("winner candidate %q for chapter %d is empty", winner, chapter)
	}
	if err := s.io.WriteMarkdown(fmt.Sprintf("drafts/%02d.draft.md", chapter), content); err != nil {
		return fmt.Errorf("write promoted draft: %w", err)
	}
	v, err := s.LoadVerdict(chapter)
	if err != nil {
		return fmt.Errorf("load verdict before mark promoted: %w", err)
	}
	if v == nil {
		return fmt.Errorf("chapter %d has no verdict; call SaveVerdict before PromoteCandidate", chapter)
	}
	v.Promoted = true
	return s.SaveVerdict(*v)
}
