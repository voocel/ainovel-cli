package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// IO 封装文件系统读写操作，提供加锁和原子写入。
// 每个子存储持有独立的 IO 实例，拥有各自的 sync.RWMutex。
type IO struct {
	dir string
	mu  sync.RWMutex
}

func newIO(dir string) *IO {
	return &IO{dir: dir}
}

func (io *IO) path(rel string) string {
	return filepath.Join(io.dir, rel)
}

func (io *IO) ReadFile(rel string) ([]byte, error) {
	io.mu.RLock()
	defer io.mu.RUnlock()
	return io.ReadFileUnlocked(rel)
}

func (io *IO) ReadFileUnlocked(rel string) ([]byte, error) {
	return os.ReadFile(io.path(rel))
}

func (io *IO) WriteFileUnlocked(rel string, data []byte) error {
	p := io.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

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

func (io *IO) ReadJSON(rel string, v any) error {
	io.mu.RLock()
	defer io.mu.RUnlock()
	return io.ReadJSONUnlocked(rel, v)
}

func (io *IO) ReadJSONUnlocked(rel string, v any) error {
	data, err := io.ReadFileUnlocked(rel)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (io *IO) WriteJSON(rel string, v any) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteJSONUnlocked(rel, v)
}

func (io *IO) WriteJSONUnlocked(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return io.WriteFileUnlocked(rel, data)
}

func (io *IO) WriteMarkdown(rel string, content string) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteFileUnlocked(rel, []byte(content))
}

func (io *IO) WriteMarkdownUnlocked(rel string, content string) error {
	return io.WriteFileUnlocked(rel, []byte(content))
}

func (io *IO) RemoveFile(rel string) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.RemoveFileUnlocked(rel)
}

func (io *IO) RemoveFileUnlocked(rel string) error {
	err := os.Remove(io.path(rel))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (io *IO) WithWriteLock(fn func() error) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return fn()
}

// EnsureDirs 创建指定的子目录。
func (io *IO) EnsureDirs(dirs []string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(io.dir, d), 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}