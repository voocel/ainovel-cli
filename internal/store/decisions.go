package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// DecisionStore 审计运行时的 LLM 语义裁定(meta/decisions.jsonl,append-only)。
//
// 定位(docs/engine-arbiter.md §4.3):审计与离线重放的数据源——记录"当时看到什么
// 事实、做了什么裁定",供 eval 回归与未来 Arbiter 的 A/B 对照。它**不是**事件溯源,
// 也**不是**恢复数据源(恢复只依赖 Progress/Checkpoint/RunMeta 等事实层)。
type DecisionStore struct{ io *IO }

func NewDecisionStore(io *IO) *DecisionStore { return &DecisionStore{io: io} }

const (
	decisionSchemaVersion = 1
	decisionsFile         = "meta/decisions.jsonl"
	// maxDecisionInputBytes 单条 input 上限;超限截断并标记,防止长粘贴撑爆审计文件。
	maxDecisionInputBytes = 8 << 10
)

// DecisionRecord 一次语义裁定的审计记录。facts 只存结构化事实与引用,不复制正文。
// input 保留在记录内(离线重放必需);脱敏发生在 diag export 边界,不在落盘时。
type DecisionRecord struct {
	SchemaVersion  int             `json:"schema_version"`
	ID             string          `json:"id"`
	At             string          `json:"at"`
	Kind           string          `json:"kind"`    // intervention | plan_start | volume_end | ...
	Decider        string          `json:"decider"` // arbiter | architect（卷末评审）
	CheckpointSeq  int64           `json:"checkpoint_seq,omitempty"`
	Input          string          `json:"input,omitempty"`
	InputTruncated bool            `json:"input_truncated,omitempty"`
	Facts          json.RawMessage `json:"facts,omitempty"`
	Decision       json.RawMessage `json:"decision,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Error          string          `json:"error,omitempty"` // 裁定失败时的错误文本——失败也是审计事实,没有它排障只能靠推理
	Model          string          `json:"model,omitempty"`
	DurationMs     int64           `json:"duration_ms,omitempty"`
}

// Append 落盘一条裁定记录;SchemaVersion/At/ID 由本方法补齐,input 超限截断。
// 返回补齐后的记录(ID 供调用方关联,如 PlanStartRecord.DecisionID)。
func (s *DecisionStore) Append(rec DecisionRecord) (DecisionRecord, error) {
	rec.SchemaVersion = decisionSchemaVersion
	if rec.At == "" {
		rec.At = time.Now().Format(time.RFC3339)
	}
	if rec.ID == "" {
		rec.ID = newDecisionID()
	}
	if len(rec.Input) > maxDecisionInputBytes {
		rec.Input = rec.Input[:maxDecisionInputBytes]
		rec.InputTruncated = true
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return rec, fmt.Errorf("marshal decision: %w", err)
	}
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	// 上一次追加可能在换行写入前崩溃。先删除协议上可证明未提交的尾部，避免把新 JSON
	// 直接拼到残行后面；完整换行记录绝不自动修改。
	if _, err := s.committedDataUnlocked(); err != nil {
		return rec, fmt.Errorf("repair decision tail: %w", err)
	}
	if err := s.io.AppendLineUnlocked(decisionsFile, append(data, '\n')); err != nil {
		return rec, err
	}
	return rec, nil
}

// Recent 返回最近 n 条记录(旧→新);文件缺失返回空。
//
// 已提交损坏行必须显式返回错误——Arbiter 不能在缺失部分历史的事实包上继续裁定。
// 崩溃打断的尾部残行（末字节非 '\n'）由 committedDataUnlocked 截断并显式告警；这不是
// 猜测式修复，因为本文件协议规定只有换行结束的记录才算提交。
func (s *DecisionStore) Recent(n int) ([]DecisionRecord, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	data, err := s.committedDataUnlocked()
	if err != nil {
		return nil, err
	}
	all, err := parseDecisionRecords(data)
	if err != nil {
		return nil, err
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// committedDataUnlocked 返回完整换行记录，并把换行之后的残留字节从磁盘截断。调用方
// 必须持有 io.mu 写锁。截断是幂等的，失败时原文件保留，错误明确上抛。
func (s *DecisionStore) committedDataUnlocked() ([]byte, error) {
	data, err := s.io.ReadFileUnlocked(decisionsFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data, nil
	}
	keep := bytes.LastIndexByte(data, '\n') + 1
	if err := os.Truncate(s.io.path(decisionsFile), int64(keep)); err != nil {
		return nil, err
	}
	slog.Warn("已修复裁定审计的未提交尾部",
		"module", "store", "file", decisionsFile, "discarded_bytes", len(data)-keep)
	return data[:keep], nil
}

func parseDecisionRecords(data []byte) ([]DecisionRecord, error) {
	var all []DecisionRecord
	lines := bytes.Split(data, []byte{'\n'})
	for i, raw := range lines {
		if i == len(lines)-1 && len(raw) == 0 {
			break
		}
		var rec DecisionRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", decisionsFile, i+1, err)
		}
		all = append(all, rec)
	}
	return all, nil
}

func newDecisionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("dec-%d", time.Now().UnixNano())
	}
	return "dec-" + hex.EncodeToString(b[:])
}
