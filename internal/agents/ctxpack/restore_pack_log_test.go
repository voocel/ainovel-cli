package ctxpack

// 恢复包被丢弃时的可观测性回归。
//
// CompactionEnd 只说"压缩发生了"，看不出恢复包被预算挤掉了；ctxpack 因此必须
// 自己留一条 Warn（strategy.go 的 restoreMessage）。这条日志是排查"压缩后模型
// 忘设定"的唯一线索，容易被静默重构掉，所以钉成测试：
//   - 恢复包超预算被丢弃 → 恰好一条 Warn，带 budget 字段；
//   - 恢复包为空（或未装） → 零日志，压缩照常进行。
//
// 用自定义 slog handler 捕获 default logger，只把 Compact 本身包进窗口；
// store 的种数据在窗口外完成。

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/agentcore"
)

// logRecord 是捕获到的一条 slog 记录。
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// captureHandler 把 default logger 的输出收进内存，供断言。
type captureHandler struct {
	mu      sync.Mutex
	records []logRecord
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]string, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, logRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

// WithAttrs / WithGroup 不关心分组语义，保持同一个捕获器。
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []logRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]logRecord(nil), h.records...)
}

func (h *captureHandler) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = nil
}

// captureSlog 把 default logger 换成捕获器，测试结束复原。
func captureSlog(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// restorePackHistory 是被替换段足够大的历史：压缩一定发生（与
// TestWriterCompactorInjectsRestorePack 同一形状）。
func restorePackHistory() []agentcore.Message {
	msgs := []agentcore.Message{agentcore.UserText("继续写第 3 章")}
	for i := 1; i <= 6; i++ {
		msgs = append(msgs, toolGroup("t"+strconv.Itoa(i), "read_chapter", strings.Repeat("a", 1600))...)
	}
	return msgs
}

func newRestorePackCompactor(t *testing.T, budget int) (*WriterCompactor, *WriterRestorePack) {
	t.Helper()
	pack := &WriterRestorePack{}
	return NewWriterCompactor(WriterCompactConfig{
		Store:              seededWriterStore(t),
		Restore:            pack,
		RestoreTokenBudget: budget,
		KeepRecentTokens:   600,
		SummaryTokenBudget: 2000,
	}), pack
}

func TestRestorePackDropLog(t *testing.T) {
	logs := captureSlog(t)

	t.Run("超预算丢弃留一条 Warn", func(t *testing.T) {
		logs.reset()
		compactor, pack := newRestorePackCompactor(t, 100)
		// 直接塞超预算内容：buildMessage 估算 required > budget 时返回错误。
		pack.mu.Lock()
		pack.text = "<post-compact-context>\n" + strings.Repeat("上下文", 400) + "\n</post-compact-context>"
		pack.mu.Unlock()

		out, err := compactor.Compact(context.Background(), restorePackHistory(), nil, "")
		if err != nil {
			t.Fatalf("恢复包被丢弃不应让压缩失败: %v", err)
		}
		if out == nil {
			t.Fatal("expected compaction")
		}
		for _, m := range out.Messages {
			if strings.Contains(m.Text(), "<post-compact-context>") {
				t.Fatalf("超预算的恢复包不得进入压缩结果: %q", m.Text())
			}
		}

		records := logs.snapshot()
		if len(records) != 1 {
			t.Fatalf("丢弃恢复包必须只留一条 Warn，got %d: %+v", len(records), records)
		}
		rec := records[0]
		if rec.level != slog.LevelWarn {
			t.Fatalf("expected Warn，got %v: %q", rec.level, rec.msg)
		}
		if !strings.Contains(rec.msg, "恢复包") {
			t.Fatalf("日志应说明发生了恢复包丢弃，got %q", rec.msg)
		}
		budget, ok := rec.attrs["budget"]
		if !ok {
			t.Fatalf("日志必须带 budget 字段（排查预算来源），got attrs %+v", rec.attrs)
		}
		if budget != "100" {
			t.Fatalf("budget 应为 compactor 实际使用的 100，got %q", budget)
		}
		if errAttr, ok := rec.attrs["err"]; !ok || !strings.Contains(errAttr, "requires") {
			t.Fatalf("日志必须带丢弃原因（err），got attrs %+v", rec.attrs)
		}
	})

	t.Run("空包零日志", func(t *testing.T) {
		logs.reset()
		compactor, _ := newRestorePackCompactor(t, 100)

		out, err := compactor.Compact(context.Background(), restorePackHistory(), nil, "")
		if err != nil {
			t.Fatalf("空恢复包不应影响压缩: %v", err)
		}
		if out == nil {
			t.Fatal("expected compaction")
		}
		if records := logs.snapshot(); len(records) != 0 {
			t.Fatalf("空恢复包必须零日志，got %d: %+v", len(records), records)
		}
	})
}
