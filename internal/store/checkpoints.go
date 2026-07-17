package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const checkpointsFile = "meta/checkpoints.jsonl"

// CheckpointStore 管理 step 级 checkpoint 的追加与查询。
// 磁盘格式：meta/checkpoints.jsonl，只追加；查询走内存镜像。
// 不变量：cache 是 checkpoints.jsonl 的镜像，由 Append/Reset 单点维护。
// 并发：cache 受 io.mu 保护，写走 Lock、读走 RLock。
type CheckpointStore struct {
	io      *IO
	seqGen  atomic.Int64
	cache   []domain.Checkpoint
	loadErr error
}

// NewCheckpointStore 创建 checkpoint 存储，从磁盘一次性加载已有 checkpoint 到 cache。
func NewCheckpointStore(io *IO) *CheckpointStore {
	cs := &CheckpointStore{io: io}
	cs.loadFromDisk()
	return cs
}

// loadFromDisk 一次性把磁盘 jsonl 读进 cache 并恢复 seqGen。
func (cs *CheckpointStore) loadFromDisk() {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()

	cs.cache, cs.loadErr = readCheckpointsFile(cs.io.path(checkpointsFile))
	var maxSeq int64
	for _, cp := range cs.cache {
		if cp.Seq > maxSeq {
			maxSeq = cp.Seq
		}
	}
	cs.seqGen.Store(maxSeq)
}

// Append 追加一条 checkpoint。
// 幂等：相同 Scope + Step + Digest 已存在则跳过写入，直接返回已有记录。
func (cs *CheckpointStore) Append(scope domain.Scope, step, artifact, digest string) (*domain.Checkpoint, error) {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	if cs.loadErr != nil {
		return nil, fmt.Errorf("checkpoint store 初始化失败: %w", cs.loadErr)
	}

	if digest != "" {
		for i := len(cs.cache) - 1; i >= 0; i-- {
			cp := cs.cache[i]
			if cp.Scope.Matches(scope) && cp.Step == step && cp.Digest == digest {
				return &cp, nil
			}
		}
	}

	// seq 写成功后才推进，避免写失败留下永久跳号。
	// 已持 io.mu 写锁，Load+Store 之间不会被并发抢占。
	seq := cs.seqGen.Load() + 1
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
	cs.seqGen.Store(seq)
	cs.cache = append(cs.cache, cp)
	return &cp, nil
}

// AppendArtifact 计算 artifact 内容指纹后追加 checkpoint。
func (cs *CheckpointStore) AppendArtifact(scope domain.Scope, step, artifact string) (*domain.Checkpoint, error) {
	if artifact == "" {
		return cs.Append(scope, step, "", "")
	}
	data, err := cs.io.ReadFile(artifact)
	if err != nil {
		return nil, fmt.Errorf("digest artifact %s: %w", artifact, err)
	}
	sum := sha256.Sum256(data)
	return cs.Append(scope, step, artifact, "sha256:"+hex.EncodeToString(sum[:]))
}

// Latest 返回指定 scope 的最新 checkpoint。
func (cs *CheckpointStore) Latest(scope domain.Scope) *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	for i := len(cs.cache) - 1; i >= 0; i-- {
		if cs.cache[i].Scope.Matches(scope) {
			cp := cs.cache[i]
			return &cp
		}
	}
	return nil
}

// LatestByStep 返回指定 scope + step 的最新 checkpoint。
func (cs *CheckpointStore) LatestByStep(scope domain.Scope, step string) *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	for i := len(cs.cache) - 1; i >= 0; i-- {
		cp := cs.cache[i]
		if cp.Scope.Matches(scope) && cp.Step == step {
			return &cp
		}
	}
	return nil
}

// LatestGlobal 返回全局最新 checkpoint（不区分 scope）。
func (cs *CheckpointStore) LatestGlobal() *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	if len(cs.cache) == 0 {
		return nil
	}
	cp := cs.cache[len(cs.cache)-1]
	return &cp
}

// All 返回全部 checkpoint 列表副本（按 seq 递增）。
func (cs *CheckpointStore) All() []domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	if len(cs.cache) == 0 {
		return nil
	}
	out := make([]domain.Checkpoint, len(cs.cache))
	copy(out, cs.cache)
	return out
}

// Reset 清空 checkpoint 文件与 cache。仅在新建小说时使用。
// 先删文件再清内存：删除失败时保留 cache 与 seqGen，避免内存与磁盘状态错位。
func (cs *CheckpointStore) Reset() error {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	if err := cs.io.RemoveFileUnlocked(checkpointsFile); err != nil {
		return err
	}
	cs.seqGen.Store(0)
	cs.cache = nil
	cs.loadErr = nil
	return nil
}

// InitError 返回构造时加载 checkpoint 镜像的错误。Store.Init 必须先检查它，
// 防止损坏的 jsonl 被解释成“没有 checkpoint”。
func (cs *CheckpointStore) InitError() error {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	return cs.loadErr
}

// readCheckpointsFile 严格解析 jsonl；尾部截断也是需要用户可见的持久化错误。
func readCheckpointsFile(path string) ([]domain.Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var result []domain.Checkpoint
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var cp domain.Checkpoint
		if err := json.Unmarshal(raw, &cp); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", checkpointsFile, lineNo, err)
		}
		result = append(result, cp)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", checkpointsFile, err)
	}
	return result, nil
}
