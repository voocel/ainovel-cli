package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionStore_AppendAndRecent(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	first, err := s.Decisions.Append(DecisionRecord{
		Kind: "intervention", Decider: "arbiter",
		Input: "重写第3章", Facts: json.RawMessage(`{"phase":"writing"}`),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if first.ID == "" || first.At == "" || first.SchemaVersion != decisionSchemaVersion {
		t.Fatalf("Append 应补齐 ID/At/SchemaVersion: %+v", first)
	}

	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "继续写"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	// 失败裁定:error 是审计事实,必须原样落盘并可读回。
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: "凡人修仙", Error: "USER_INACTIVE"}); err != nil {
		t.Fatalf("append 3: %v", err)
	}

	recent, err := s.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("应有 3 条记录, got %d", len(recent))
	}
	if recent[2].Error != "USER_INACTIVE" || len(recent[2].Decision) != 0 {
		t.Fatalf("失败裁定应带 error 且无 decision: %+v", recent[2])
	}
	if recent[0].Input != "重写第3章" || recent[1].Input != "继续写" {
		t.Fatalf("记录顺序应为旧→新: %+v", recent)
	}

	// n 截取:只要最近 1 条
	last, err := s.Decisions.Recent(1)
	if err != nil || len(last) != 1 || last[0].Input != "凡人修仙" {
		t.Fatalf("Recent(1) 应取最新一条, got %+v err=%v", last, err)
	}
}

func TestDecisionStore_InputTruncation(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	huge := strings.Repeat("长", maxDecisionInputBytes) // 3 字节/字,远超上限
	rec, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: huge})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !rec.InputTruncated || len(rec.Input) > maxDecisionInputBytes {
		t.Fatalf("超限 input 必须截断并标记: truncated=%v len=%d", rec.InputTruncated, len(rec.Input))
	}
	// 截断后的记录仍然可读回
	recent, err := s.Decisions.Recent(1)
	if err != nil || len(recent) != 1 {
		t.Fatalf("读回失败: %v", err)
	}
}

// 文件中部的已提交损坏行(其后仍有完整提交的行)必须硬失败——不能在残缺历史上裁定。
func TestDecisionStore_RecentRejectsCommittedCorruptLine(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "好的"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// 已 '\n' 收尾的损坏行(完整提交却损坏),其后再追加一条完整记录。
	if err := s.Decisions.io.AppendLine(decisionsFile, []byte("{\"schema_version\":1,\"kind\":\"interv\n")); err != nil {
		t.Fatalf("append corrupt: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "之后"}); err != nil {
		t.Fatalf("append trailing: %v", err)
	}
	if _, err := s.Decisions.Recent(10); err == nil {
		t.Fatal("文件中部的已提交损坏行必须显式报错")
	}
}

// 崩溃留下的尾部残行(末字节非 '\n' 的未提交追加)按 not-exist 容忍:丢弃残行、返回其前
// 的完整记录,不硬失败——否则一次崩溃就永久毒化 append-only 审计。
func TestDecisionStore_RecentToleratesUncommittedTail(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "好的"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// 模拟崩溃打断的尾部残行:无换行结尾。
	if err := s.Decisions.io.AppendLine(decisionsFile, []byte(`{"schema_version":1,"kind":"interv`)); err != nil {
		t.Fatalf("append partial: %v", err)
	}
	recent, err := s.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("尾部残行应被容忍,不应报错: %v", err)
	}
	if len(recent) != 1 || recent[0].Input != "好的" {
		t.Fatalf("应丢弃残行并保留已提交记录,得到: %+v", recent)
	}
	// 恢复必须真正截断磁盘尾部，而不是只在本次读取中忽略；否则下一次追加会把两段
	// JSON 拼成永久损坏。追加后再次读取应保持完整闭环。
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "恢复后"}); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	recent, err = s.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("recent after append: %v", err)
	}
	if len(recent) != 2 || recent[0].Input != "好的" || recent[1].Input != "恢复后" {
		t.Fatalf("尾部恢复后应可继续追加，得到: %+v", recent)
	}
	raw, err := os.ReadFile(filepath.Join(dir, decisionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("恢复后的审计文件必须以提交换行结尾: %q", raw)
	}
}

// 即使尾部恰好是完整 JSON，只要没有协议要求的换行，也属于未提交记录；恢复必须丢弃
// 它并确保后续追加不会发生 `}{` 拼接。
func TestDecisionStore_RecoveryDropsValidJSONWithoutCommitNewline(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "已提交"}); err != nil {
		t.Fatal(err)
	}
	partial, err := json.Marshal(DecisionRecord{SchemaVersion: decisionSchemaVersion, Kind: "intervention", Input: "未提交"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decisions.io.AppendLine(decisionsFile, partial); err != nil {
		t.Fatal(err)
	}

	// 模拟重启：下一次读取或追加就是审计恢复边界。
	reopened := NewStore(dir)
	if err := reopened.Init(); err != nil {
		t.Fatalf("restart init: %v", err)
	}
	if _, err := reopened.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "重启后"}); err != nil {
		t.Fatal(err)
	}
	recent, err := reopened.Decisions.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Input != "已提交" || recent[1].Input != "重启后" {
		t.Fatalf("未提交的无换行 JSON 不应被接纳，得到: %+v", recent)
	}
}
