package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// sessionRecord 是 meta/sessions/*.jsonl 单条记录的轻量解析形态——只取
// 累计 usage 需要的字段。Content 等大字段跳过解析，节省启动期 IO。
//
// 模型归属三级降级：
//  1. Provider/Model       — agentcore.Message 上透传的真实响应模型（首选）
//  2. Meta(_meta)          — 上游未透传时，写入侧由 ModelLookup 补的"当时生效"模型
//  3. 都没有                — replay 退回 effectiveModel 用当前 ModelSet 反推（精度受损）
type sessionRecord struct {
	Role     litellm.Role       `json:"role"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
	Usage    *agentcore.Usage   `json:"usage,omitempty"`
	Meta     *sessionRecordMeta `json:"_meta,omitempty"`
}

type sessionRecordMeta struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// ReplaySessions 扫 meta/sessions/agents/*.jsonl，
// 把每条 assistant 消息的 usage 重新累加到 tracker。返回回填条数。
//
// 调用约束：仅在 meta/usage.json 缺失时调用一次回填。
// 日常持久化走 SaveNow / autoSaveLoop。
//
// 精度依赖见 sessionRecord 注释的三级降级——第 3 级（Usage 和 _meta 都缺）
// 在更老日志或上游异常时才会触发。
func (t *UsageTracker) ReplaySessions(rootDir string) (int, error) {
	if t == nil {
		return 0, nil
	}
	sessionsDir := filepath.Join(rootDir, "meta", "sessions")
	info, err := os.Stat(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, nil
	}

	total := 0
	agentsDir := filepath.Join(sessionsDir, "agents")
	walkErr := filepath.WalkDir(agentsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		agentName := parseAgentNameFromFile(name)
		if agentName == "" {
			return nil
		}
		n, fileErr := t.replayFile(path, agentName)
		if fileErr != nil {
			slog.Warn("replay agent session failed", "module", "usage", "file", name, "err", fileErr)
			return nil
		}
		total += n
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return total, walkErr
	}
	return total, nil
}

// replayFile 扫单个 jsonl 文件，把所有带 Usage 的 assistant 消息喂给 accumulate。
// agentName 由调用方从 Worker 会话文件名解析。
func (t *UsageTracker) replayFile(path, agentName string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	role := agentRoleName(agentName)
	count := 0
	scanner := bufio.NewScanner(f)
	// 单行可能很长（assistant 消息 + tool args 等都打平了），放宽到 4MB。
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Role != litellm.RoleAssistant || rec.Usage == nil {
			continue
		}
		provider, modelName := strings.TrimSpace(rec.Provider), strings.TrimSpace(rec.Model)
		if rec.Meta != nil {
			if provider == "" {
				provider = rec.Meta.Provider
			}
			if modelName == "" {
				modelName = rec.Meta.Model
			}
		}
		t.accumulate(role, provider, modelName, *rec.Usage)
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("scan %s: %w", path, err)
	}
	return count, nil
}

// parseAgentNameFromFile 从 "writer-ch01.jsonl" / "architect_short-001.jsonl" 提取
// agent 名（"-" 之前部分）。命名约定见 store/session.go::subAgentPath：
// agentName 不含 dash，suffix 是 ch<n> 或递增序号。
func parseAgentNameFromFile(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	if i := strings.Index(base, "-"); i > 0 {
		return base[:i]
	}
	return ""
}
