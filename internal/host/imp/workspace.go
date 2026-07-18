package imp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// workspaceSchemaVersion 是导入工作区整体 schema 版本。
// 不匹配时显式要求用匹配版本继续或重新导入，不猜测迁移（RFC §6.1）。
const workspaceSchemaVersion = 1

// Digest 计算内容摘要，沿用仓库既有约定 "sha256:"+hex（见 store/checkpoints.go）。
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Artifact 是工作区中每份语义工件的统一身份：schema 版本 + 输入摘要 + 载荷。
// 只有能从当前真实语义输入重建出相同 InputDigest 才可复用（RFC §6.3 / 不变量 1）。
// 不实现依赖图：LoadState 沿固定线性管线逐步比对 InputDigest 判定复用与失效，NextAction 据此推导下一步。
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}

// Manifest 对应唯一归一化源快照，是工作区身份而非派生工件（RFC §6.1）。
// 不保存绝对源路径，避免泄露机器目录并消除移动文件带来的恢复问题。
type Manifest struct {
	Version          int    `json:"version"`
	SourceName       string `json:"source_name"`
	RawSHA256        string `json:"raw_sha256"`
	NormalizedSHA256 string `json:"normalized_sha256"`
	Encoding         string `json:"encoding"`
	SizeBytes        int64  `json:"size_bytes"`
	CreatedAt        string `json:"created_at"`
}

// Intent 保存启动导入时的显式用户授权，恢复后仍必须遵守，不由工件猜出，Runner 不静默改写（RFC §6.1）。
type Intent struct {
	Version             int    `json:"version"`
	AutoConfirm         bool   `json:"auto_confirm,omitempty"`
	StoryResolution     string `json:"story_resolution,omitempty"` // open / closed
	ContinueAfterImport bool   `json:"continue_after_import,omitempty"`
}

// 工作区标准工件相对路径。
const (
	fileManifest     = "manifest.json"
	fileIntent       = "intent.json"
	fileSource       = "source.txt"
	fileGuidance     = "guidance.txt"
	fileSegmentation = "segmentation.json"
	fileConfirmation = "confirmation.json"
	fileSynthesis    = "synthesis.json"
	fileStoryResolve = "story-resolution.json"
	dirAnalyses      = "analyses"
	dirRangeDigests  = "range-digests"
	dirSegmentChunks = "segment-chunks"
	dirFailures      = "failures"
)

// Workspace 是 <书根>/meta/import/ 目录的原子工件读写句柄。
type Workspace struct {
	dir string
}

// OpenWorkspace 返回指向书根下 meta/import/ 的句柄；不保证目录已存在，用 Active() 判断。
func OpenWorkspace(bookDir string) *Workspace {
	return &Workspace{dir: filepath.Join(bookDir, "meta", "import")}
}

// Dir 返回工作区绝对路径（诊断与失败工件落点用）。
func (w *Workspace) Dir() string { return w.dir }

func (w *Workspace) path(rel string) string { return filepath.Join(w.dir, rel) }

// Active 判断是否存在已发布的活动工作区。meta/import/ 不存在就不算活动，
// 半初始化目录以 meta/import.init-* 形态存在，不会被误判为活动（RFC §6.1）。
func (w *Workspace) Active() bool {
	fi, err := os.Stat(w.dir)
	return err == nil && fi.IsDir()
}

func (w *Workspace) has(rel string) bool {
	_, err := os.Stat(w.path(rel))
	return err == nil
}

// writeAtomic 以「临时文件 + fsync + rename」原子写入 rel（相对工作区）。
func (w *Workspace) writeAtomic(rel string, data []byte) error {
	full := w.path(rel)
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
// Windows 等平台可能不支持目录 Sync，其错误忽略——进程崩溃安全不依赖它，仅补掉电场景（RFC §12.3）。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func (w *Workspace) writeJSON(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return w.writeAtomic(rel, append(data, '\n'))
}

func (w *Workspace) readJSON(rel string, v any) error {
	data, err := os.ReadFile(w.path(rel))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// LoadManifest 读取工作区源快照身份。
func (w *Workspace) LoadManifest() (*Manifest, error) {
	var m Manifest
	if err := w.readJSON(fileManifest, &m); err != nil {
		return nil, err
	}
	if m.Version != workspaceSchemaVersion {
		return nil, fmt.Errorf("manifest schema 版本 %d != %d，请用匹配版本继续或重新导入", m.Version, workspaceSchemaVersion)
	}
	return &m, nil
}

// LoadIntent 读取用户启动授权。
func (w *Workspace) LoadIntent() (*Intent, error) {
	var in Intent
	if err := w.readJSON(fileIntent, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// LoadSource 读取归一化源快照文本。
func (w *Workspace) LoadSource() ([]byte, error) {
	return os.ReadFile(w.path(fileSource))
}

// LoadGuidance 读取用户切分指导（RFC §18.3）；缺失即无指导。
// 指导与 source.txt 同为切分的语义输入而非派生工件，由显式 --guide 更新，
// 内容变化使 segmentation 及其下游 InputDigest 自然失配。
func (w *Workspace) LoadGuidance() (string, error) {
	data, err := os.ReadFile(w.path(fileGuidance))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readBytes 读取工件原始字节，用于下游 InputDigest 绑定。
func (w *Workspace) readBytes(rel string) ([]byte, error) {
	return os.ReadFile(w.path(rel))
}

// writeArtifact 写入带统一身份的语义工件。
func writeArtifact[T any](w *Workspace, rel, inputDigest string, payload T) error {
	return w.writeJSON(rel, Artifact[T]{
		SchemaVersion: workspaceSchemaVersion,
		InputDigest:   inputDigest,
		Payload:       payload,
	})
}

// readArtifact 读取语义工件并校验 schema 版本；InputDigest 是否匹配由调用方按当前输入判定。
func readArtifact[T any](w *Workspace, rel string) (*Artifact[T], error) {
	var a Artifact[T]
	if err := w.readJSON(rel, &a); err != nil {
		return nil, err
	}
	if a.SchemaVersion != workspaceSchemaVersion {
		return nil, fmt.Errorf("%s schema 版本 %d != %d，请用匹配版本继续或重新导入", rel, a.SchemaVersion, workspaceSchemaVersion)
	}
	return &a, nil
}

// clearDir 删除工作区内某个中间缓存目录。错误必须交调用方处置：吞掉会让「已清除」的
// 文案撒谎——下次重跑照样复用坏缓存（Windows 反病毒/句柄占用是真实场景，Debug-First）。
func (w *Workspace) clearDir(rel string) error {
	return os.RemoveAll(w.path(rel))
}

// FailureMeta 是最近一次失败的诊断元数据（RFC §14.2）。
type FailureMeta struct {
	Stage         string `json:"stage"`
	Detail        string `json:"detail"`
	StopReason    string `json:"stop_reason,omitempty"`
	PrefixSalvage string `json:"prefix_salvage,omitempty"` // available:N / unavailable
}

// writeFailure best-effort 保存最近失败的元数据与未裁剪的原始模型响应到 failures/（RFC §14.2）。
// 原始响应可能含正文，仅落在用户自己的书目录，不进普通日志或脱敏诊断导出。
func (w *Workspace) writeFailure(meta FailureMeta, rawResponse string) {
	_ = w.writeJSON(filepath.Join(dirFailures, "last.json"), meta)
	_ = w.writeAtomic(filepath.Join(dirFailures, "last-response.txt"), []byte(rawResponse))
}

// createWorkspace 在临时目录写齐 manifest/intent/source 并校验后，以目录 rename 原子发布为 meta/import/。
// 这样初始三件套不会以半初始化形态进入 NextAction，也无需 stage=initializing（RFC §6.1）。
func createWorkspace(bookDir string, m Manifest, in Intent, normalized []byte) (*Workspace, error) {
	base := filepath.Join(bookDir, "meta")
	final := filepath.Join(base, "import")
	if fi, err := os.Stat(final); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("导入工作区已存在：%s（无参数 /import 可从中恢复）", final)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(base, "import.init-*")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmp)
		}
	}()

	tw := &Workspace{dir: tmp}
	if err := tw.writeAtomic(fileSource, normalized); err != nil {
		return nil, err
	}
	if err := tw.writeJSON(fileManifest, m); err != nil {
		return nil, err
	}
	if err := tw.writeJSON(fileIntent, in); err != nil {
		return nil, err
	}
	// 发布前校验三件套可读且源快照与 manifest 一致，杜绝半写工作区。
	got, err := tw.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("校验初始 manifest：%w", err)
	}
	src, err := tw.LoadSource()
	if err != nil {
		return nil, fmt.Errorf("校验初始源快照：%w", err)
	}
	if d := Digest(src); d != got.NormalizedSHA256 {
		return nil, fmt.Errorf("初始源快照摘要不一致：%s != %s", d, got.NormalizedSHA256)
	}
	if _, err := tw.LoadIntent(); err != nil {
		return nil, fmt.Errorf("校验初始 intent：%w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	syncDir(base)
	committed = true
	return &Workspace{dir: final}, nil
}
