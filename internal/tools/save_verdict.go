package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveVerdictTool 保存 Judge 对多人格候选稿的选优裁定。
type SaveVerdictTool struct {
	store *store.Store
}

// NewSaveVerdictTool 构造 SaveVerdictTool。
func NewSaveVerdictTool(store *store.Store) *SaveVerdictTool {
	return &SaveVerdictTool{store: store}
}

func (t *SaveVerdictTool) Name() string  { return "save_verdict" }
func (t *SaveVerdictTool) Label() string { return "保存选优裁定" }
func (t *SaveVerdictTool) Description() string {
	return "保存多人格竞稿的选优裁定：从各候选稿中选出 winner（persona slug），给出各稿评分与给中选稿的修改意见。" +
		"winner 必须出现在 scores 列表中。"
}

// 写工具，禁止并发。
func (t *SaveVerdictTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveVerdictTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// Schema 返回工具的 JSON Schema 描述。
func (t *SaveVerdictTool) Schema() map[string]any {
	// 单个候选稿的评分结构
	scoreSchema := schema.Object(
		schema.Property("persona", schema.String("候选 persona slug")).Required(),
		schema.Property("score", schema.Number("评分（0-10）")).Required(),
		schema.Property("comment", schema.String("该稿的简要评语")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("winner", schema.String("中选 persona slug（必须在 scores 中）")).Required(),
		schema.Property("scores", schema.Array("各候选稿评分（每个候选一条）", scoreSchema)).Required(),
		schema.Property("revision_notes", schema.String("给中选 writer 的具体修改意见")).Required(),
	)
}

// Execute 验证并保存裁定，追加 checkpoint artifact。
func (t *SaveVerdictTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var v domain.Verdict
	if err := json.Unmarshal(args, &v); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	// 基础校验
	if v.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	if strings.TrimSpace(v.Winner) == "" {
		return nil, fmt.Errorf("winner is required")
	}
	if len(v.Scores) == 0 {
		return nil, fmt.Errorf("scores must not be empty")
	}
	if strings.TrimSpace(v.RevisionNotes) == "" {
		return nil, fmt.Errorf("revision_notes is required")
	}

	// winner 必须在 scores 中存在
	inScores := false
	for _, s := range v.Scores {
		if s.Persona == v.Winner {
			inScores = true
			break
		}
	}
	if !inScores {
		return nil, fmt.Errorf("winner %q must appear in scores", v.Winner)
	}

	// score 范围校验：Judge 是 LLM，可能给出越界分
	for _, s := range v.Scores {
		if s.Score < 0 || s.Score > 10 {
			return nil, fmt.Errorf("score for %q out of range [0,10]: %v", s.Persona, s.Score)
		}
	}

	// 落盘时一律置为未提升状态；提升由 dispatcher 内联完成
	v.Promoted = false

	// 持久化裁定
	if err := t.store.Contest.SaveVerdict(v); err != nil {
		return nil, fmt.Errorf("save verdict: %w", err)
	}

	// 追加 checkpoint artifact，用于步骤级别崩溃恢复
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(v.Chapter), "verdict",
		fmt.Sprintf("drafts/%02d.verdict.json", v.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint verdict: %w", err)
	}

	return json.Marshal(map[string]any{
		"saved":          true,
		"chapter":        v.Chapter,
		"winner":         v.Winner,
		"winner_score":   v.WinnerScore(),
		"revision_notes": v.RevisionNotes,
		"next_step":      "Host 将提升中选稿并指派中选 writer 润色，无需你继续操作",
	})
}
