package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var errSourcesUnavailable = errors.New("扫榜库数据源快照不可用")

// librarySchemaVersion 是扫榜库整体 schema 版本。
// 不匹配时显式要求重扫，不猜测迁移（与 rip/imp 同纪律）。
const librarySchemaVersion = 1

// Digest 计算内容摘要，沿用仓库既有约定 "sha256:"+hex。
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Artifact 是扫榜库中每份语义工件的统一身份：schema 版本 + 输入摘要 + 载荷。
// 只有能从当前真实语义输入重建出相同 InputDigest 才可复用。
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}

// Library 是 扫榜库/{平台}_{榜单}_{日期}/ 目录的原子工件读写句柄。
type Library struct {
	dir string
}

// OpenLibrary 返回指向某次扫榜产物目录的句柄；不保证目录已存在，用 Active() 判断。
func OpenLibrary(dir string) *Library {
	return &Library{dir: dir}
}

// Dir 返回扫榜库绝对路径（诊断、失败工件与产物落点用）。
func (l *Library) Dir() string { return l.dir }

// Active 检查扫榜库是否存在。
func (l *Library) Active() bool {
	fi, err := os.Stat(l.dir)
	return err == nil && fi.IsDir()
}

// path 返回相对路径在库内的绝对路径。
func (l *Library) path(rel string) string {
	return filepath.Join(l.dir, rel)
}

// writeJSON 原子写入 JSON 文件。
func (l *Library) writeJSON(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return l.writeAtomic(rel, append(data, '\n'))
}

// readJSON 读取 JSON 文件。
func (l *Library) readJSON(rel string, v any) error {
	data, err := os.ReadFile(l.path(rel))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// readBytes 读取工件原始字节，用于下游 InputDigest 绑定。
func (l *Library) readBytes(rel string) ([]byte, error) {
	return os.ReadFile(l.path(rel))
}

// writeAtomic 以「临时文件 + fsync + rename」原子写入 rel（相对扫榜库）。
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
// Windows 等平台可能不支持目录 Sync，其错误忽略。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// WriteMarkdown 落一份人类可读投影。.json 是数据真源，.md 是投影。
func (l *Library) WriteMarkdown(rel, content string) error {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return l.writeAtomic(rel, []byte(content))
}

// writeText 写入文本文件。
func (l *Library) writeText(rel string, content string) error {
	return l.writeAtomic(rel, []byte(content))
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
		return nil, fmt.Errorf("%s schema 版本 %d != %d，请重新扫榜", rel, a.SchemaVersion, librarySchemaVersion)
	}
	return &a, nil
}

// artifactFresh 判定工件是否存在且与当前输入摘要匹配。
// 工件不存在返回 false；schema 版本不匹配返回错误（须人工处置，不静默重跑）。
func artifactFresh[T any](l *Library, rel, want string) (bool, error) {
	a, err := readArtifact[T](l, rel)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.InputDigest == want, nil
}

// Meta 是扫榜库元数据，是库身份而非派生工件。
type Meta struct {
	Version      int       `json:"version"`
	Platform     string    `json:"platform"`
	RankName     string    `json:"rank_name"`
	ScanDate     string    `json:"scan_date"` // YYYYMMDD
	Sources      int       `json:"sources"`   // 数据源文件数
	SourceDigest string    `json:"source_digest,omitempty"`
	Created      time.Time `json:"created"`
}

// 标准工件路径
const (
	fileMeta     = "_meta.json"
	fileEntries  = "entries.json"
	fileAnalysis = "analysis.json"
	fileTopic    = "topic.json"
	fileReport   = "扫榜报告.md"
	fileTopicMD  = "选题决策.md"
	dirSources   = "sources"
	dirLogs      = "logs"
	dirFailures  = "failures"
	logFileName  = "scan.log"
)

// LoadMeta 读取扫榜库身份。
func (l *Library) LoadMeta() (*Meta, error) {
	var m Meta
	if err := l.readJSON(fileMeta, &m); err != nil {
		return nil, err
	}
	if m.Version != librarySchemaVersion {
		return nil, fmt.Errorf("_meta.json schema 版本 %d != %d，请重新扫榜", m.Version, librarySchemaVersion)
	}
	return &m, nil
}

// InitLibrary 初始化扫榜库目录结构。
func InitLibrary(dir, platform, rankName, scanDate string, sourceCount int) (*Library, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	l := OpenLibrary(dir)

	meta := Meta{
		Version:  librarySchemaVersion,
		Platform: platform,
		RankName: rankName,
		ScanDate: scanDate,
		Sources:  sourceCount,
		Created:  time.Now(),
	}

	if err := l.writeJSON(fileMeta, meta); err != nil {
		return nil, err
	}

	// 创建子目录
	for _, subdir := range []string{dirSources, dirLogs} {
		if err := os.MkdirAll(l.path(subdir), 0o755); err != nil {
			return nil, err
		}
	}

	return l, nil
}

// SaveSources 保存数据源快照。原始输入是唯一不可再生的材料，第一步就落盘。
func (l *Library) SaveSources(sources []Source) error {
	if len(sources) == 0 {
		return fmt.Errorf("数据源不能为空")
	}
	for i, src := range sources {
		filename := fmt.Sprintf("%03d_%s.txt", i+1, sanitizeFilename(src.Origin))
		path := filepath.Join(dirSources, filename)
		if err := l.writeText(path, src.Raw); err != nil {
			return fmt.Errorf("保存数据源 %s: %w", filename, err)
		}
	}
	meta, err := l.LoadMeta()
	if err != nil {
		return err
	}
	meta.Sources = len(sources)
	meta.SourceDigest = sourcesDigest(sources)
	return l.writeJSON(fileMeta, meta)
}

// ReplaceSources 用完整新快照替换已有 sources/。先写临时目录，再目录级换入；
// 任一步失败都恢复旧快照，避免“换榜单数据”把可恢复的旧库变成半初始化。
func (l *Library) ReplaceSources(sources []Source) error {
	if len(sources) == 0 {
		return fmt.Errorf("数据源不能为空")
	}
	stage, err := os.MkdirTemp(l.dir, ".sources-next-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for i, src := range sources {
		name := fmt.Sprintf("%03d_%s.txt", i+1, sanitizeFilename(src.Origin))
		if err := os.WriteFile(filepath.Join(stage, name), []byte(src.Raw), 0o644); err != nil {
			return fmt.Errorf("暂存数据源 %s: %w", name, err)
		}
	}

	current := l.path(dirSources)
	backup := l.path(".sources-previous")
	_ = os.RemoveAll(backup)
	if err := os.Rename(current, backup); err != nil {
		return fmt.Errorf("备份旧数据源快照: %w", err)
	}
	restore := true
	defer func() {
		if restore {
			_ = os.RemoveAll(current)
			_ = os.Rename(backup, current)
		}
	}()
	if err := os.Rename(stage, current); err != nil {
		return fmt.Errorf("换入新数据源快照: %w", err)
	}
	meta, err := l.LoadMeta()
	if err != nil {
		return err
	}
	meta.Sources = len(sources)
	meta.SourceDigest = sourcesDigest(sources)
	if err := l.writeJSON(fileMeta, meta); err != nil {
		return err
	}
	restore = false
	_ = os.RemoveAll(backup)
	syncDir(l.dir)
	return nil
}

// sourcesDigest 只绑定数据内容，不绑定临时文件名；同一批榜单换文件名或顺序仍可复用。
func sourcesDigest(sources []Source) string {
	parts := make([]string, 0, len(sources))
	for _, src := range sources {
		parts = append(parts, Digest([]byte(src.Raw)))
	}
	sort.Strings(parts)
	return Digest([]byte("sources-v1\x00" + strings.Join(parts, "\x00")))
}

// LoadSources 从库内快照重建数据源，供恢复时无需再次提供输入。
func (l *Library) LoadSources() ([]Source, error) {
	sourcesDir := l.path(dirSources)
	entries, err := os.ReadDir(sourcesDir)
	if err != nil {
		if os.IsNotExist(err) {
			if _, statErr := os.Stat(sourcesDir); os.IsNotExist(statErr) {
				return nil, fmt.Errorf("%w: %w", errSourcesUnavailable, err)
			}
		}
		return nil, err
	}
	m, err := l.LoadMeta()
	if err != nil {
		return nil, err
	}
	var out []Source
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := l.readBytes(filepath.Join(dirSources, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Source{
			Platform: m.Platform,
			RankName: m.RankName,
			Raw:      string(data),
			Origin:   e.Name(),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: 扫榜库 %s 的 sources/ 为空", errSourcesUnavailable, l.dir)
	}
	return out, nil
}

// FailureMeta 是最近一次失败的诊断元数据。
type FailureMeta struct {
	Stage      string `json:"stage"`
	Detail     string `json:"detail"`
	StopReason string `json:"stop_reason,omitempty"`
	Origin     string `json:"origin,omitempty"` // 解析阶段的失败数据源
}

// writeFailure best-effort 保存最近失败的元数据与未裁剪的原始模型响应到 failures/。
func (l *Library) writeFailure(meta FailureMeta, rawResponse string) {
	_ = l.writeJSON(filepath.Join(dirFailures, "last.json"), meta)
	_ = l.writeAtomic(filepath.Join(dirFailures, "last-response.txt"), []byte(rawResponse))
}

// sanitizeFilename 清理文件名中的非法字符。
func sanitizeFilename(name string) string {
	// 只保留文件名部分
	result := filepath.Base(name)
	if result == "" || result == "." {
		result = "untitled"
	}
	return result
}
