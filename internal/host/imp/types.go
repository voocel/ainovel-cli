// Package imp 实现外部小说Chương的Nhập与反推。
//
// 核心思路：用 LLM 反推 foundation + 每章事实，复用现有 save_foundation /
// commit_chapter 工具的原子三件套落盘。NhậpHoàn thành后 store Trạng thái等同于"写完 N 章
// 后崩溃"，调用方调 host.Resume() 即可Không có缝续写。
//
// 不走 Coordinator：Nhập是确定性回放，不属于 LLM 决策范畴；让 Coordinator
// 介入只会引入不确定性。本包直接调 LLM 客户端 + 调工具。
package imp

import "time"

// Chapter 是切分后的单个Chương。
type Chapter struct {
	Title   string
	Content string
}

// Options 控制Nhập行为。
type Options struct {
	// SourcePath 必填。单个 txt/md Tập tinĐường dẫn。
	SourcePath string

	// ResumeFrom 可选。从第 N 章Bắt đầuNhập；0 / 1 表示从头。
	// 若 > 1，会Bỏ qua Foundation 反推（认为已落盘）。
	ResumeFrom int
}

// Stage 表示Nhập流程的Hiện tại阶段。
type Stage string

const (
	StageSplitting  Stage = "splitting"
	StageFoundation Stage = "foundation"
	StageChapter    Stage = "chapter"
	StageDone       Stage = "done"
	StageError      Stage = "error"
)

// Event 是Nhập流程对外发出的Tiến độ事件。
type Event struct {
	Time    time.Time
	Stage   Stage
	Current int    // chapter 阶段的Hiện tại章号；其它阶段为 0
	Total   int    // 总章数
	Message string // 人类可读描述
	Err     error  // StageError 时携带
}
