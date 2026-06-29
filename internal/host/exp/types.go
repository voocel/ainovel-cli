// Package exp 实现Đã hoàn thànhChương的Xuất能力。
//
// 与 imp/ 对称：纯本地 IO，不依赖 LLM，不改 store Trạng thái。Xuất可以与
// Coordinator 并发运行（只读 Progress + Chương终稿），属于横向能力。
//
// 第一版只支持 TXT；EPUB 留待下一轮。
package exp

import "github.com/voocel/ainovel-cli/internal/store"

// Format 标识Xuất格式。
type Format string

const (
	// FormatTXT Văn bản thuần输出。
	FormatTXT Format = "txt"
	// FormatEPUB 标准 EPUB 3 容器（zip + xhtml）。
	FormatEPUB Format = "epub"
)

// Options 控制Xuất行为。zero-value 等价于"Xuất全本到Mặc địnhĐường dẫn，Tập tin存在时报错"。
//
// 版式：《Tên sách》 → 卷分隔 → ChươngChính văn。两类内部数据不进Xuất：premise（创作蓝图，
// 含目标读者 / 核心消费点 / Viết禁区等后台元信息，给作者与引擎看，不是读者的序）；
// 弧分隔（读者视角下弧是过细的内部结构）。Tên sách与卷分隔始终保留。
type Options struct {
	// Format Rỗng字符串时由 OutPath 后缀推断（.txt → TXT，.epub → EPUB）；
	// OutPath 也为Rỗng时回退 FormatTXT。SDK 调用方可显式指定以Bỏ qua推断。
	Format Format

	// OutPath 输出Tập tinĐường dẫn；Rỗng表示 {novelDir}/{NovelName}.{ext}，
	// ext 由 Format 决定（NovelName 为Rỗng则用Thư mục名）。
	OutPath string

	// From / To Chương范围，闭区间。0 表示从第 1 章 / 到最后一章。
	// 范围内未Hoàn thành的Chương会被Bỏ qua并写入 Result.Skipped，不视为Lỗi。
	From, To int

	// Overwrite Tập tin存在时Có czy không覆盖；Mặc định拒绝。
	Overwrite bool
}

// Deps 是 Run 所需依赖。仅 store；XuấtKhông có需 LLM、prompt、bundle。
type Deps struct {
	Store *store.Store
}

// Result 是一次Thành côngXuất的产物Tóm tắt。
type Result struct {
	// Path 实际写入的Tập tinĐường dẫn（绝对或调用方传入的相对）。
	Path string
	// Chapters 实际写入的Chương数。
	Chapters int
	// Bytes Tập tin字节数（UTF-8）。
	Bytes int
	// Skipped 落在Vui lòng求范围内但未Hoàn thành的Chương号。
	Skipped []int
}
