package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store 封装小说输出目录，提供所有状态读写操作。
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore 创建状态管理器，dir 为小说输出根目录。
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir 返回输出根目录。
func (s *Store) Dir() string { return s.dir }

// Init 创建所需的子目录结构。
func (s *Store) Init() error {
	dirs := []string{"chapters", "summaries", "drafts", "reviews", "meta"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(s.dir, d), 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}

func (s *Store) path(rel string) string {
	return filepath.Join(s.dir, rel)
}

func (s *Store) readFile(rel string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readFileUnlocked(rel)
}

func (s *Store) writeFile(rel string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeFileUnlocked(rel, data)
}

func (s *Store) readFileUnlocked(rel string) ([]byte, error) {
	return os.ReadFile(s.path(rel))
}

func (s *Store) writeFileUnlocked(rel string, data []byte) error {
	p := s.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, p)
}

func (s *Store) readJSON(rel string, v any) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readJSONUnlocked(rel, v)
}

func (s *Store) readJSONUnlocked(rel string, v any) error {
	data, err := s.readFileUnlocked(rel)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (s *Store) writeJSON(rel string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONUnlocked(rel, v)
}

func (s *Store) writeJSONUnlocked(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFileUnlocked(rel, data)
}

func (s *Store) writeMarkdown(rel string, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeFileUnlocked(rel, []byte(content))
}

func (s *Store) removeFile(rel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeFileUnlocked(rel)
}

func (s *Store) removeFileUnlocked(rel string) error {
	err := os.Remove(s.path(rel))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) withWriteLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn()
}
