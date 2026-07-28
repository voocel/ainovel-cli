package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // 注册 PNG 解码器：生图模型出的封面就是 PNG
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // WebP 封面也要能缩略

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// ── 书库 ──
//
// 引擎的"一本书 = 一个输出目录"语义不变；桌面版只是把"用户手动 cd 换书"变成显式的书库。
// 书库清单是纯桌面层概念，存 ~/.ainovel/library.json，不进引擎、不影响 CLI。
// 每本书的真实状态（进度/字数）始终现读该目录下的 store 产物，清单只存路径与显示名，
// 避免两份事实源不一致。

// LibraryBook 是书库列表里的一本书。
type LibraryBook struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	LastOpened string `json:"lastOpened"`
	// 以下为现读的实时状态（不持久化在清单里）
	Chapters int     `json:"chapters"`
	Words    int     `json:"words"`
	Phase    string  `json:"phase"`
	CostUSD  float64 `json:"costUSD"`
	Missing  bool    `json:"missing"` // 目录已不存在
	// CoverURL 是封面缩略图的内联 data URL（无封面则空串）。
	CoverURL string `json:"coverURL"`
}

// libraryEntry 是清单里持久化的部分。
type libraryEntry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	LastOpened time.Time `json:"lastOpened"`
}

type libraryFile struct {
	Version int            `json:"version"`
	Books   []libraryEntry `json:"books"`
}

const libraryVersion = 1

// library 管理书库清单文件的读写。
type library struct {
	mu sync.Mutex
}

func libraryPath() string {
	return filepath.Join(configDir(), "library.json")
}

// booksRoot 是新建书籍的默认落点。
func booksRoot() string {
	return filepath.Join(configDir(), "books")
}

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".ainovel"
	}
	return filepath.Join(home, ".ainovel")
}

func (l *library) load() (libraryFile, error) {
	data, err := os.ReadFile(libraryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return libraryFile{Version: libraryVersion}, nil
		}
		return libraryFile{}, err
	}
	var lf libraryFile
	if err := json.Unmarshal(data, &lf); err != nil {
		// 清单损坏不该让用户进不去应用：当空处理，后续写入会覆盖修复。
		return libraryFile{Version: libraryVersion}, nil
	}
	return lf, nil
}

func (l *library) save(lf libraryFile) error {
	lf.Version = libraryVersion
	if err := os.MkdirAll(filepath.Dir(libraryPath()), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	// 原子写：临时文件 + rename，避免写入中断留下半个清单。
	tmp := libraryPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, libraryPath())
}

// remember 记录（或更新）一本书的打开时间。
func (l *library) remember(path, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.load()
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	found := false
	for i := range lf.Books {
		if sameDir(lf.Books[i].Path, abs) {
			lf.Books[i].LastOpened = time.Now()
			if name != "" {
				lf.Books[i].Name = name
			}
			found = true
			break
		}
	}
	if !found {
		lf.Books = append(lf.Books, libraryEntry{Path: abs, Name: name, LastOpened: time.Now()})
	}
	return l.save(lf)
}

func (l *library) forget(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.load()
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	kept := make([]libraryEntry, 0, len(lf.Books))
	for _, b := range lf.Books {
		if !sameDir(b.Path, abs) {
			kept = append(kept, b)
		}
	}
	lf.Books = kept
	return l.save(lf)
}

// sameDir 判断两个路径是否指向同一目录。
//
// 必须先绝对化：Host 的 store 原样保存传入的 dir（不做绝对化），而书库清单里
// 存的是绝对路径。只做 Clean 的话，相对路径与绝对路径会被判成两本不同的书——
// OpenBook 的"同一本书跳过重建"快路径会因此失效，在途生图又会被 abortAll 干掉。
// Windows 下大小写不敏感，用 EqualFold 比较。
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if absA, err := filepath.Abs(a); err == nil {
		a = absA
	}
	if absB, err := filepath.Abs(b); err == nil {
		b = absB
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ── 绑定方法 ──

// ListBooks 返回书库列表，按最近打开倒序。每本书的进度现读其目录下的 store。
func (a *App) ListBooks() ([]LibraryBook, error) {
	lf, err := a.lib.load()
	if err != nil {
		return nil, fmt.Errorf("读取书库失败: %w", err)
	}
	sort.Slice(lf.Books, func(i, j int) bool {
		return lf.Books[i].LastOpened.After(lf.Books[j].LastOpened)
	})
	out := make([]LibraryBook, 0, len(lf.Books))
	for _, b := range lf.Books {
		book := LibraryBook{Path: b.Path, Name: b.Name}
		if !b.LastOpened.IsZero() {
			book.LastOpened = b.LastOpened.Format(rfc3339)
		}
		if stat, err := os.Stat(b.Path); err != nil || !stat.IsDir() {
			book.Missing = true
			out = append(out, book)
			continue
		}
		fillBookProgress(&book)
		fillBookCover(&book)
		out = append(out, book)
	}
	return out, nil
}

// fillBookProgress 现读书目录下的进度事实（不启动 Host——只读 store 文件）。
func fillBookProgress(book *LibraryBook) {
	store := storepkg.NewStore(book.Path)
	progress, err := store.Progress.Load()
	if err != nil || progress == nil {
		return
	}
	book.Chapters = len(progress.CompletedChapters)
	book.Words = progress.TotalWordCount
	book.Phase = string(progress.Phase)
	if name := strings.TrimSpace(progress.NovelName); name != "" && book.Name == "" {
		book.Name = name
	}
	if book.Name == "" {
		if premise, _ := store.Outline.LoadPremise(); premise != "" {
			book.Name = domain.ExtractNovelNameFromPremise(premise)
		}
	}
}

// fillBookCover 读封面并内联成 data URL 给书库卡片用。
//
// 不能直接内联原图：生图模型出的封面实测 2.1MB（1024x1536 PNG），十几本书就是
// 几十 MB base64 一次性塞给前端。原先的做法是"超过 2MB 就不显示"，结果真实封面
// 恰好越线，卡片永远是纯文字——用户生成了封面却看不到。
//
// 现在改为缩成缩略图再内联：卡片只有 ~150px 宽，用不着原图。缩略图另存一份
// （thumb.jpg），下次直接读，不必每次列表都重新解码 2MB PNG。
const (
	// 缩略图长边像素：书库卡片实际显示约 150px，2 倍图足够清晰。
	thumbMaxDim = 320
	// 原图超过这个大小才值得生成缩略图；小图直接内联更省事。
	thumbSourceThreshold = 256 << 10
)

func fillBookCover(book *LibraryBook) {
	path, mime := findCoverFile(book.Path)
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	// 小图直接用原图，省一次编解码。
	if info.Size() <= thumbSourceThreshold {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			book.CoverURL = toDataURL(data, mime)
		}
		return
	}
	if data, thumbMime, err := cachedThumb(book.Path, path, info.ModTime()); err == nil {
		book.CoverURL = toDataURL(data, thumbMime)
	}
}

// thumbPath 是缩略图缓存位置（与封面元数据同放 meta/）。
func thumbPath(bookDir string) string {
	return filepath.Join(bookDir, "meta", "cover.thumb.jpg")
}

// cachedThumb 返回缩略图字节，必要时重新生成。
// 以封面文件的 mtime 作为缓存有效性依据：换了封面就重新缩。
func cachedThumb(bookDir, coverPath string, coverMod time.Time) ([]byte, string, error) {
	tp := thumbPath(bookDir)
	if st, err := os.Stat(tp); err == nil && st.ModTime().After(coverMod) {
		if data, err := os.ReadFile(tp); err == nil && len(data) > 0 {
			return data, "image/jpeg", nil
		}
	}
	data, err := makeThumb(coverPath)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(tp), 0o700); err == nil {
		// 缓存写失败不影响本次显示，下次再试。
		tmp := tp + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			if err := os.Rename(tmp, tp); err != nil {
				_ = os.Remove(tmp)
			}
		}
	}
	return data, "image/jpeg", nil
}

// makeThumb 把封面缩成长边 thumbMaxDim 的 JPEG。
func makeThumb(coverPath string) ([]byte, error) {
	f, err := os.Open(coverPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("解码封面失败: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("封面尺寸异常")
	}
	// 等比缩放到长边 thumbMaxDim。
	scale := float64(thumbMaxDim) / float64(max(w, h))
	if scale > 1 {
		scale = 1
	}
	dw, dh := int(float64(w)*scale), int(float64(h)*scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// CatmullRom 比最近邻慢但结果干净；一本书只做一次并缓存，代价可接受。
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CreateBook 新建一本书的目录并打开它。name 用于生成目录名；dir 非空则直接用该目录。
func (a *App) CreateBook(name, dir string) (string, error) {
	target := strings.TrimSpace(dir)
	if target == "" {
		slug := slugify(name)
		if slug == "" {
			slug = time.Now().Format("20060102-150405")
		}
		target = filepath.Join(booksRoot(), slug)
		// 目录已存在且非空时加后缀，避免覆盖已有书的进度。
		target = uniqueDir(target)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", fmt.Errorf("创建书目录失败: %w", err)
	}
	if _, err := a.OpenBook(target); err != nil {
		return target, err
	}
	if err := a.lib.remember(target, strings.TrimSpace(name)); err != nil {
		slog.Warn("记录书库失败", "module", "desktop", "err", err)
	}
	return target, nil
}

// ForgetBook 从书库列表移除一本书（不删除磁盘上的文件）。
func (a *App) ForgetBook(path string) error {
	return a.lib.forget(path)
}

// DefaultBooksDir 返回新建书籍的默认根目录（供前端展示）。
func (a *App) DefaultBooksDir() string {
	return booksRoot()
}

// CurrentBookDir 返回当前打开的书目录（未开书为空串）。
func (a *App) CurrentBookDir() string {
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h == nil {
		return ""
	}
	return h.Dir()
}

var slugUnsafe = regexp.MustCompile(`[^\p{Han}\p{L}\p{N}_-]+`)

// slugify 把书名转成安全的目录名（保留中文与字母数字）。
func slugify(name string) string {
	s := slugUnsafe.ReplaceAllString(strings.TrimSpace(name), "-")
	s = strings.Trim(s, "-")
	if len([]rune(s)) > 40 {
		s = string([]rune(s)[:40])
	}
	return s
}

// uniqueDir 若目标目录已存在且非空，追加 -2 / -3 … 直到找到可用目录。
func uniqueDir(base string) string {
	candidate := base
	for i := 2; i < 1000; i++ {
		entries, err := os.ReadDir(candidate)
		if err != nil || len(entries) == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return candidate
}
