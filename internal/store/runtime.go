package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const runtimeQueuePath = "meta/runtime/queue.jsonl"

// RuntimeStore 管理统一运行时队列和每任务日志。
type RuntimeStore struct {
	io *IO

	mu         sync.Mutex
	seqLoaded  bool
	nextSeqNum int64
}

func NewRuntimeStore(io *IO) *RuntimeStore {
	return &RuntimeStore{io: io}
}

// AppendQueue 追加一条运行时队列记录，并自动分配递增序号。
func (s *RuntimeStore) AppendQueue(item domain.RuntimeQueueItem) (domain.RuntimeQueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureSeqLoadedLocked(); err != nil {
		return item, err
	}
	s.nextSeqNum++
	item.Seq = s.nextSeqNum
	if item.Time.IsZero() {
		item.Time = time.Now()
	}
	if err := s.appendJSONLine(runtimeQueuePath, item); err != nil {
		return item, err
	}
	return item, nil
}

// LoadQueue ĐọcHiện tại持久化的Tất cả运行时队列项。
func (s *RuntimeStore) LoadQueue() ([]domain.RuntimeQueueItem, error) {
	return loadJSONLines[domain.RuntimeQueueItem](s.io, runtimeQueuePath)
}

// LoadQueueAfter Quay lại指定序号之后的队列项。
func (s *RuntimeStore) LoadQueueAfter(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	items, err := s.LoadQueue()
	if err != nil || afterSeq <= 0 {
		return items, err
	}
	filtered := items[:0]
	for _, item := range items {
		if item.Seq > afterSeq {
			filtered = append(filtered, item)
		}
	}
	return append([]domain.RuntimeQueueItem(nil), filtered...), nil
}

// AppendTaskLog 追加某个任务的运行日志。
func (s *RuntimeStore) AppendTaskLog(taskID string, entry domain.RuntimeTaskLogEntry) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	if entry.TaskID == "" {
		entry.TaskID = taskID
	}
	return s.appendJSONLine(taskLogPath(taskID), entry)
}

// LoadTaskLog Đọc某个任务的Tất cả运行日志。
func (s *RuntimeStore) LoadTaskLog(taskID string) ([]domain.RuntimeTaskLogEntry, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil
	}
	return loadJSONLines[domain.RuntimeTaskLogEntry](s.io, taskLogPath(taskID))
}

func taskLogPath(taskID string) string {
	return filepath.Join("meta", "runtime", "tasks", taskID+".log")
}

// Reset 清Rỗng运行时队列和任务日志。
func (s *RuntimeStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seqLoaded = false
	s.nextSeqNum = 0

	var errs []string
	if err := os.Remove(filepath.Join(s.io.dir, runtimeQueuePath)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err.Error())
	}
	if err := os.RemoveAll(filepath.Join(s.io.dir, "meta", "runtime", "tasks")); err != nil {
		errs = append(errs, err.Error())
	}
	if err := os.MkdirAll(filepath.Join(s.io.dir, "meta", "runtime", "tasks"), 0o755); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("reset runtime store: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *RuntimeStore) ensureSeqLoadedLocked() error {
	if s.seqLoaded {
		return nil
	}
	items, err := loadJSONLines[domain.RuntimeQueueItem](s.io, runtimeQueuePath)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		s.nextSeqNum = items[len(items)-1].Seq
	}
	s.seqLoaded = true
	return nil
}

func (s *RuntimeStore) appendJSONLine(rel string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.io.AppendLine(rel, data)
}

func loadJSONLines[T any](io *IO, rel string) ([]T, error) {
	data, err := io.ReadFile(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 8*1024*1024)
	var out []T
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("parse %s: %w", rel, err)
		}
		out = append(out, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
