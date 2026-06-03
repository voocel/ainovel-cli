// Package exp 实现已完成章节的导出能力。
//
// 与 imp/ 对称：纯本地 IO，不依赖 LLM，不改 store 状态。导出可以与
// Coordinator 并发运行（只读 Progress + 章节终稿），属于横向能力。
//
// 第一版只支持 TXT；EPUB 留待下一轮。
package exp

import "github.com/Accelerator-mzq/ainovel-cli/internal/store"

// Format 标识导出格式。
type Format string

const (
	// FormatTXT 纯文本输出。
	FormatTXT Format = "txt"
	// FormatEPUB 标准 EPUB 3 容器（zip + xhtml）。
	FormatEPUB Format = "epub"
)

// Options 控制导出行为。zero-value 等价于"导出全本到默认路径，文件存在时报错"。
//
// 标题页（书名 + premise）和卷弧分隔不暴露开关：前者是天然的序言，后者是
// 长篇结构的骨架，两者都是"让产物更可读"的固定行为。
type Options struct {
	// Format 空字符串时由 OutPath 后缀推断（.txt → TXT，.epub → EPUB）；
	// OutPath 也为空时回退 FormatTXT。SDK 调用方可显式指定以跳过推断。
	Format Format

	// OutPath 输出文件路径；空表示 {novelDir}/{NovelName}.{ext}，
	// ext 由 Format 决定（NovelName 为空则用目录名）。
	OutPath string

	// From / To 章节范围，闭区间。0 表示从第 1 章 / 到最后一章。
	// 范围内未完成的章节会被跳过并写入 Result.Skipped，不视为错误。
	From, To int

	// Overwrite 文件存在时是否覆盖；默认拒绝。
	Overwrite bool
}

// Deps 是 Run 所需依赖。仅 store；导出无需 LLM、prompt、bundle。
type Deps struct {
	Store *store.Store
}

// Result 是一次成功导出的产物摘要。
type Result struct {
	// Path 实际写入的文件路径（绝对或调用方传入的相对）。
	Path string
	// Chapters 实际写入的章节数。
	Chapters int
	// Bytes 文件字节数（UTF-8）。
	Bytes int
	// Skipped 落在请求范围内但未完成的章节号。
	Skipped []int
}
