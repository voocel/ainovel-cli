package rip

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// librarySchemaVersion 是拆文库整体 schema 版本。
// 不匹配时显式要求用匹配版本继续或重新拆解，不猜测迁移（与 imp 同纪律）。
const librarySchemaVersion = 1

// Digest 计算内容摘要，沿用仓库既有约定 "sha256:"+hex。
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Artifact 是拆文库中每份语义工件的统一身份：schema 版本 + 输入摘要 + 载荷。
// 只有能从当前真实语义输入重建出相同 InputDigest 才可复用。
// 不实现依赖图：LoadState 沿固定线性管线逐步比对 InputDigest 判定复用与失效。
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}

// Manifest 对应唯一归一化源快照，是拆文库身份而非派生工件。
// 不保存绝对源路径，避免泄露机器目录并消除移动文件带来的恢复问题。
type Manifest struct {
	Version          int    `json:"version"`
	BookName         string `json:"book_name"`
	SourceName       string `json:"source_name"`
	RawSHA256        string `json:"raw_sha256"`
	NormalizedSHA256 string `json:"normalized_sha256"`
	Encoding         string `json:"encoding"`
	SizeBytes        int64  `json:"size_bytes"`
	Runes            int    `json:"runes"` // 归一化后 rune 数，长短篇路由的客观依据
	CreatedAt        string `json:"created_at"`
}

// Intent 保存启动拆解时的显式用户授权，恢复后仍必须遵守，不由工件猜出，Runner 不静默改写。
type Intent struct {
	Version     int    `json:"version"`
	AutoConfirm bool   `json:"auto_confirm,omitempty"`
	Form        string `json:"form,omitempty"` // 灰区字数的用户裁定：long / short
}

// 拆文库标准工件相对路径。
const (
	fileManifest    = "manifest.json"
	fileIntent      = "intent.json"
	fileGuidance    = "guidance.txt"
	fileBoundaries  = "boundaries.json"
	filePreview     = "preview.json"
	fileAggregate   = "aggregate.json"
	fileProfile     = "profile.json"
	fileReport      = "report.json"
	fileStyle       = "style.json"
	dirSource       = "原文"
	dirChapters     = "chapters"
	dirBoundChunks  = "bound-chunks"
	dirFailures     = "failures"
	dirLogs         = "logs"
	fileSourceInDir = "原文.txt"
)

// sourceRel 是归一化源快照在库内的相对路径（原文/原文.txt）。
// 「拆解前先备份原文」是管线第一步：即使后续阶段炸了，原始材料也不会丢。
func sourceRel() string { return filepath.Join(dirSource, fileSourceInDir) }

// Library 是 拆文库/{书名}/ 目录的原子工件读写句柄。
type Library struct {
	dir string
}

// OpenLibrary 返回指向拆文库某本书目录的句柄；不保证目录已存在，用 Active() 判断。
func OpenLibrary(bookDir string) *Library {
	return &Library{dir: bookDir}
}

// Dir 返回拆文库绝对路径（诊断、失败工件与产物落点用）。
func (l *Library) Dir() string { return l.dir }

// LogDir 返回本次运行的转录目录。
func (l *Library) LogDir() string { return l.path(dirLogs) }

func (l *Library) path(rel string) string { return filepath.Join(l.dir, rel) }

// Active 判断是否存在已发布的活动拆文库。
// 半初始化目录以 {书名}.init-* 形态存在，不会被误判为活动。
func (l *Library) Active() bool {
	fi, err := os.Stat(l.dir)
	return err == nil && fi.IsDir()
}

func (l *Library) has(rel string) bool {
	_, err := os.Stat(l.path(rel))
	return err == nil
}

// writeAtomic 以「临时文件 + fsync + rename」原子写入 rel（相对拆文库）。
func (l *Library) writeAtomic(rel string, data []byte) error {
	full := l.path(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), filepath.Base(full)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return err
	}
	syncDir(filepath.Dir(full))
	return nil
}

// syncDir best-effort fsync 目录项，使刚完成的 rename 在掉电后仍持久。
// Windows 等平台可能不支持目录 Sync，其错误忽略——进程崩溃安全不依赖它，仅补掉电场景。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func (l *Library) writeJSON(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return l.writeAtomic(rel, append(data, '\n'))
}

func (l *Library) readJSON(rel string, v any) error {
	data, err := os.ReadFile(l.path(rel))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteMarkdown 落一份人类可读投影。.json 是数据真源，.md 是投影——与 store/io.go 同一约定。
func (l *Library) WriteMarkdown(rel, content string) error {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return l.writeAtomic(rel, []byte(content))
}

// LoadManifest 读取拆文库源快照身份。
func (l *Library) LoadManifest() (*Manifest, error) {
	var m Manifest
	if err := l.readJSON(fileManifest, &m); err != nil {
		return nil, err
	}
	if m.Version != librarySchemaVersion {
		return nil, fmt.Errorf("manifest schema 版本 %d != %d，请用匹配版本继续或重新拆解", m.Version, librarySchemaVersion)
	}
	return &m, nil
}

// LoadIntent 读取用户启动授权。
func (l *Library) LoadIntent() (*Intent, error) {
	var in Intent
	if err := l.readJSON(fileIntent, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// LoadSource 读取归一化源快照文本。
func (l *Library) LoadSource() ([]byte, error) {
	return os.ReadFile(l.path(sourceRel()))
}

// LoadGuidance 读取用户切分指导；缺失即无指导。
// 指导与源快照同为切分的语义输入而非派生工件，由显式 --guide 更新，
// 内容变化使 boundaries 及其下游 InputDigest 自然失配。
func (l *Library) LoadGuidance() (string, error) {
	data, err := os.ReadFile(l.path(fileGuidance))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readBytes 读取工件原始字节，用于下游 InputDigest 绑定。
func (l *Library) readBytes(rel string) ([]byte, error) {
	return os.ReadFile(l.path(rel))
}

// writeArtifact 写入带统一身份的语义工件。
func writeArtifact[T any](l *Library, rel, inputDigest string, payload T) error {
	return l.writeJSON(rel, Artifact[T]{
		SchemaVersion: librarySchemaVersion,
		InputDigest:   inputDigest,
		Payload:       payload,
	})
}

// readArtifact 读取语义工件并校验 schema 版本；InputDigest 是否匹配由调用方按当前输入判定。
func readArtifact[T any](l *Library, rel string) (*Artifact[T], error) {
	var a Artifact[T]
	if err := l.readJSON(rel, &a); err != nil {
		return nil, err
	}
	if a.SchemaVersion != librarySchemaVersion {
		return nil, fmt.Errorf("%s schema 版本 %d != %d，请用匹配版本继续或重新拆解", rel, a.SchemaVersion, librarySchemaVersion)
	}
	return &a, nil
}

// clearDir 删除库内某个中间缓存目录。错误必须交调用方处置：吞掉会让「已清除」的
// 文案撒谎——下次重跑照样复用坏缓存。
func (l *Library) clearDir(rel string) error {
	return os.RemoveAll(l.path(rel))
}

// FailureMeta 是最近一次失败的诊断元数据。
type FailureMeta struct {
	Stage         string `json:"stage"`
	Detail        string `json:"detail"`
	StopReason    string `json:"stop_reason,omitempty"`
	PrefixSalvage string `json:"prefix_salvage,omitempty"` // available:N / unavailable
	Chapter       int    `json:"chapter,omitempty"`        // 逐章阶段的失败章号
	InputDigest   string `json:"input_digest,omitempty"`   // 当前章摘要输入身份；变化后旧标记自动失效
}

// writeFailure best-effort 保存最近失败的元数据与未裁剪的原始模型响应到 failures/。
// 原始响应可能含他人作品正文，仅落在用户自己的拆文库，不进普通日志或脱敏诊断导出。
func (l *Library) writeFailure(meta FailureMeta, rawResponse string) {
	_ = l.writeJSON(filepath.Join(dirFailures, "last.json"), meta)
	_ = l.writeAtomic(filepath.Join(dirFailures, "last-response.txt"), []byte(rawResponse))
}

// chapterFailurePath 是某章持久化失败标记的相对路径。
// FailedChapters 从这些文件重算，不持久化 completed_with_errors 之类会漂移的状态串。
func chapterFailurePath(n int) string {
	return filepath.Join(dirFailures, fmt.Sprintf("chapter-%06d.json", n))
}

// writeChapterFailure 记录某章重试后仍失败，使管线可越过它继续并在终态报告降级。
func (l *Library) writeChapterFailure(n int, meta FailureMeta, rawResponse string) {
	meta.Chapter = n
	_ = l.writeJSON(chapterFailurePath(n), meta)
	_ = l.writeAtomic(filepath.Join(dirFailures, fmt.Sprintf("chapter-%06d-response.txt", n)), []byte(rawResponse))
}

// clearChapterFailure 在某章重新成功后清掉旧失败标记，避免降级状态粘住。
func (l *Library) clearChapterFailure(n int) {
	_ = os.Remove(l.path(chapterFailurePath(n)))
	_ = os.Remove(l.path(filepath.Join(dirFailures, fmt.Sprintf("chapter-%06d-response.txt", n))))
}

// failedChapters 扫 failures/ 重算失败章号（升序，去掉超出章数范围的陈旧标记）。
func (l *Library) failedChapters(expected int) []int {
	return l.failedChaptersForInput(expected, nil)
}

// failedChaptersForInput 只返回与当前章节输入身份匹配的失败标记。
// digestFor 为空时用于显式清理等管理操作，返回全部合法章号。
func (l *Library) failedChaptersForInput(expected int, digestFor func(int) string) []int {
	entries, err := os.ReadDir(l.path(dirFailures))
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "chapter-%06d.json", &n); err != nil {
			continue
		}
		if n < 1 || (expected > 0 && n > expected) {
			continue
		}
		if digestFor != nil {
			var meta FailureMeta
			if err := l.readJSON(chapterFailurePath(n), &meta); err != nil ||
				meta.InputDigest == "" || meta.InputDigest != digestFor(n) {
				continue
			}
		}
		out = append(out, n)
	}
	sortInts(out)
	return out
}

func (l *Library) clearAllChapterFailures() int {
	failed := l.failedChapters(0)
	for _, n := range failed {
		l.clearChapterFailure(n)
	}
	return len(failed)
}

func sortInts(xs []int) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// createLibrary 在临时目录写齐 manifest/intent/原文快照并校验后，以目录 rename 原子发布为 拆文库/{书名}/。
// 这样初始三件套不会以半初始化形态进入 NextAction，也无需 stage=initializing。
func createLibrary(libraryDir, bookName string, m Manifest, in Intent, normalized []byte) (*Library, error) {
	final := filepath.Join(libraryDir, bookName)
	if fi, err := os.Stat(final); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("拆文库已存在：%s（无参数 /rip 可从中恢复）", final)
	}
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(libraryDir, sanitizeBookName(bookName)+".init-*")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmp)
		}
	}()

	tl := &Library{dir: tmp}
	if err := tl.writeAtomic(sourceRel(), normalized); err != nil {
		return nil, err
	}
	if err := tl.writeJSON(fileManifest, m); err != nil {
		return nil, err
	}
	if err := tl.writeJSON(fileIntent, in); err != nil {
		return nil, err
	}
	// 发布前校验三件套可读且源快照与 manifest 一致，杜绝半写拆文库。
	got, err := tl.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("校验初始 manifest：%w", err)
	}
	src, err := tl.LoadSource()
	if err != nil {
		return nil, fmt.Errorf("校验初始原文快照：%w", err)
	}
	if d := Digest(src); d != got.NormalizedSHA256 {
		return nil, fmt.Errorf("初始原文快照摘要不一致：%s != %s", d, got.NormalizedSHA256)
	}
	if fi, err := os.Stat(tl.path(sourceRel())); err != nil || fi.Size() == 0 {
		return nil, fmt.Errorf("原文备份为空：%s", tl.path(sourceRel()))
	}
	if _, err := tl.LoadIntent(); err != nil {
		return nil, fmt.Errorf("校验初始 intent：%w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	syncDir(libraryDir)
	committed = true
	return &Library{dir: final}, nil
}
