package store

import (
	"bufio"
	"encoding/json"
	"os"
	"sync/atomic"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const checkpointsFile = "meta/checkpoints.jsonl"

// CheckpointStore 管理 step 级 checkpoint 的追加与查询。
// 存储格式为 JSONL（每行一条 JSON），只追加不修改。
type CheckpointStore struct {
	io     *IO
	seqGen atomic.Int64
}

// NewCheckpointStore 创建 checkpoint 存储并初始化序号。
func NewCheckpointStore(io *IO) *CheckpointStore {
	cs := &CheckpointStore{io: io}
	cs.initSeq()
	return cs
}

// initSeq 从已有文件中获取最大 seq，以便续接。
func (cs *CheckpointStore) initSeq() {
	all := cs.loadAll()
	var maxSeq int64
	for _, cp := range all {
		if cp.Seq > maxSeq {
			maxSeq = cp.Seq
		}
	}
	cs.seqGen.Store(maxSeq)
}

// Append 追加一条 checkpoint。
// 幂等：如果同 Scope + Step + Digest 已存在，跳过写入。
func (cs *CheckpointStore) Append(scope domain.Scope, step, artifact, digest string) (*domain.Checkpoint, error) {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()

	// 幂等检查
	if digest != "" {
		all := cs.loadAllUnlocked()
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.Scope.Matches(scope) && cp.Step == step && cp.Digest == digest {
				return &cp, nil
			}
		}
	}

	seq := cs.seqGen.Add(1)
	cp := domain.Checkpoint{
		Seq:        seq,
		Scope:      scope,
		Step:       step,
		Artifact:   artifact,
		Digest:     digest,
		OccurredAt: time.Now(),
	}

	data, err := json.Marshal(cp)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := cs.io.AppendLineUnlocked(checkpointsFile, data); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Latest 返回指定 scope 的最新 checkpoint。
func (cs *CheckpointStore) Latest(scope domain.Scope) *domain.Checkpoint {
	all := cs.loadAll()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Scope.Matches(scope) {
			return &all[i]
		}
	}
	return nil
}

// LatestByStep 返回指定 scope + step 的最新 checkpoint。
func (cs *CheckpointStore) LatestByStep(scope domain.Scope, step string) *domain.Checkpoint {
	all := cs.loadAll()
	for i := len(all) - 1; i >= 0; i-- {
		cp := all[i]
		if cp.Scope.Matches(scope) && cp.Step == step {
			return &cp
		}
	}
	return nil
}

// LatestGlobal 返回全局最新 checkpoint（不区分 scope）。
func (cs *CheckpointStore) LatestGlobal() *domain.Checkpoint {
	all := cs.loadAll()
	if len(all) == 0 {
		return nil
	}
	return &all[len(all)-1]
}

// All 返回全部 checkpoint 列表（按 seq 递增）。
func (cs *CheckpointStore) All() []domain.Checkpoint {
	return cs.loadAll()
}

// Reset 清空 checkpoint 文件。仅在新建小说时使用。
func (cs *CheckpointStore) Reset() error {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	cs.seqGen.Store(0)
	return cs.io.RemoveFileUnlocked(checkpointsFile)
}

// loadAll 加读锁后解析全部行。
func (cs *CheckpointStore) loadAll() []domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	return cs.loadAllUnlocked()
}

// loadAllUnlocked 在已持锁的情况下解析 JSONL。
// 跳过格式错误的行以容忍尾部截断。
func (cs *CheckpointStore) loadAllUnlocked() []domain.Checkpoint {
	f, err := os.Open(cs.io.path(checkpointsFile))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var result []domain.Checkpoint
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var cp domain.Checkpoint
		if json.Unmarshal(line, &cp) == nil {
			result = append(result, cp)
		}
	}
	return result
}
