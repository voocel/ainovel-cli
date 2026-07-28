package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// ── 封面叠字（本地排版） ──
//
// 为什么不让生图模型把书名画进画面：中文字形在生图模型里几乎必糊（缺笔、错字、
// 糊成装饰纹样），而且想改一个字就得重新花钱生一整张图。所以分工是——
// 画面归生图模型，文字归本地排版：
//
//	meta/cover.base.<ext>  原图（AI 出的或用户导入的），永远不叠字
//	cover.<ext>            叠好字的成品，书库卡片与 EPUB 导出都用它
//
// 换位置 / 改字号 / 换字体只是拿基图重排一遍，不再调生图接口（不花钱、不糊字）。
// 生图提示词那边继续要求"画面内无文字、顶部留白"，给排版留出位置。

// CoverTitleLayout 是封面文字的排版参数（存在 meta/cover.json 里，供下次重排复用）。
type CoverTitleLayout struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
	Author  string `json:"author"`
	// Position: top / center / bottom
	Position string `json:"position"`
	// Scale 是相对基准字号的倍率（基准 = 图宽的 10.5%）。
	Scale float64 `json:"scale"`
	// Theme: light = 浅色字配暗色压边；dark = 深色字配浅色压边。
	Theme string `json:"theme"`
	// Font: hei / song / kai
	Font string `json:"font"`
}

const (
	coverTitleMaxLines = 3
	// coverTitleBaseRatio 是标题基准字号占图宽的比例。1024 宽 → 约 108px，
	// 与主流实体书封面的主标题视觉重量接近。
	coverTitleBaseRatio = 0.105
	// coverTitleSafeWidth 是文字可用宽度占图宽的比例，两侧各留 9% 的页边。
	coverTitleSafeWidth = 0.82
)

// defaultCoverTitleLayout 是没有历史排版时的默认值：默认就把书名排上去——
// 用户反馈的"生成的封面没有书名"正是因为以前默认不排。
func defaultCoverTitleLayout(novelName string) CoverTitleLayout {
	return CoverTitleLayout{
		Enabled:  true,
		Title:    strings.TrimSpace(novelName),
		Position: "top",
		Scale:    1,
		Theme:    "light",
		Font:     "hei",
	}
}

// normalize 把前端传来的草稿收敛到合法取值，避免非法字号/未知枚举传到渲染层。
func (l CoverTitleLayout) normalize() CoverTitleLayout {
	out := l
	out.Title = truncateRunes(strings.TrimSpace(l.Title), 40)
	out.Author = truncateRunes(strings.TrimSpace(l.Author), 30)
	switch strings.ToLower(strings.TrimSpace(l.Position)) {
	case "center":
		out.Position = "center"
	case "bottom":
		out.Position = "bottom"
	default:
		out.Position = "top"
	}
	if strings.ToLower(strings.TrimSpace(l.Theme)) == "dark" {
		out.Theme = "dark"
	} else {
		out.Theme = "light"
	}
	switch strings.ToLower(strings.TrimSpace(l.Font)) {
	case "song":
		out.Font = "song"
	case "kai":
		out.Font = "kai"
	default:
		out.Font = "hei"
	}
	if out.Scale <= 0 {
		out.Scale = 1
	}
	out.Scale = min(max(out.Scale, 0.5), 2)
	return out
}

// hasText 判断这份排版是否真的会画出东西（关掉或没填字都不画）。
func (l CoverTitleLayout) hasText() bool {
	return l.Enabled && (strings.TrimSpace(l.Title) != "" || strings.TrimSpace(l.Author) != "")
}

// ── 字体 ──
//
// 不内嵌字体：一个中文字体 10-20MB，塞进 exe 会让安装包翻倍，而中文 Windows
// 一定有微软雅黑/黑体。按族给出候选清单，逐个尝试，第一个能解析的胜出。

var coverFontFiles = map[string][]string{
	"hei":  {"msyhbd.ttc", "msyh.ttc", "simhei.ttf", "Deng.ttf", "PingFang.ttc", "NotoSansCJKsc-Bold.otf", "NotoSansCJK-Bold.ttc"},
	"song": {"simsun.ttc", "STSONG.TTF", "simsunb.ttf", "Songti.ttc", "NotoSerifCJKsc-Bold.otf", "NotoSerifCJK-Bold.ttc"},
	"kai":  {"STKAITI.TTF", "simkai.ttf", "Kaiti.ttc"},
}

// coverFontFallback 是所有族共用的兜底顺序：指定族缺失时不要报错，
// 换一个能用的中文字体照样出图——封面能出来比字形完全符合期待更重要。
var coverFontFallback = []string{"hei", "song", "kai"}

func fontSearchDirs() []string {
	var dirs []string
	if runtime.GOOS == "windows" {
		if win := strings.TrimSpace(os.Getenv("WINDIR")); win != "" {
			dirs = append(dirs, filepath.Join(win, "Fonts"))
		}
		dirs = append(dirs, `C:\Windows\Fonts`)
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			// 用户自己装的字体在这里（未提权安装时系统目录写不进去）。
			dirs = append(dirs, filepath.Join(local, "Microsoft", "Windows", "Fonts"))
		}
		return dirs
	}
	return []string{
		"/System/Library/Fonts",
		"/System/Library/Fonts/Supplemental",
		"/Library/Fonts",
		"/usr/share/fonts/opentype/noto",
		"/usr/share/fonts/truetype/noto",
		"/usr/share/fonts",
	}
}

var (
	fontMu      sync.Mutex
	fontByPath  = map[string]*opentype.Font{}
	fontByFamly = map[string]*opentype.Font{}
)

// loadCoverFont 解析（并缓存）指定族的字体。
//
// 用 ParseCollectionReaderAt 而不是先 ReadFile：雅黑是 19MB 的 .ttc，
// 全读进内存会常驻，而 sfnt 按需 view 只碰用到的表。缓存的 *os.File 故意不关——
// 它跟着进程走，关掉后字体就废了。
func loadCoverFont(family string) (*opentype.Font, error) {
	family = strings.ToLower(strings.TrimSpace(family))
	if family == "" {
		family = "hei"
	}

	fontMu.Lock()
	defer fontMu.Unlock()
	if f, ok := fontByFamly[family]; ok && f != nil {
		return f, nil
	}

	families := []string{family}
	for _, fb := range coverFontFallback {
		if fb != family {
			families = append(families, fb)
		}
	}

	dirs := fontSearchDirs()
	for _, fam := range families {
		for _, name := range coverFontFiles[fam] {
			for _, dir := range dirs {
				path := filepath.Join(dir, name)
				if f, ok := fontByPath[path]; ok {
					if f != nil {
						fontByFamly[family] = f
						return f, nil
					}
					continue
				}
				f, err := openFontFile(path)
				if err != nil {
					continue
				}
				fontByPath[path] = f
				fontByFamly[family] = f
				return f, nil
			}
		}
	}
	return nil, fmt.Errorf("系统里找不到可用的中文字体（找过 %s），无法在封面上排版书名",
		strings.Join(dirs, "、"))
}

func openFontFile(path string) (*opentype.Font, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	coll, err := opentype.ParseCollectionReaderAt(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if coll.NumFonts() == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("字体集合为空: %s", path)
	}
	// .ttc 里第一个通常就是常规体；不做进一步挑选，避免为字重去解析全部子字体。
	parsed, err := coll.Font(0)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return parsed, nil
}

func newFace(f *opentype.Font, size float64) (font.Face, error) {
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// ── 渲染 ──

// renderCoverWithTitle 把排版好的文字叠到基图上，返回新图。
//
// maxDim > 0 时先把基图等比缩到长边 maxDim 再排版：所有尺寸都是相对图宽的比例，
// 缩小后观感一致，但要栅格化的像素少一个数量级——预览滑杆才跟得上手。
func renderCoverWithTitle(base image.Image, layout CoverTitleLayout, maxDim int) (image.Image, error) {
	b := base.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("封面尺寸异常（%dx%d）", w, h)
	}

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if maxDim > 0 && max(w, h) > maxDim {
		scale := float64(maxDim) / float64(max(w, h))
		w = max(int(float64(w)*scale), 1)
		h = max(int(float64(h)*scale), 1)
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), base, b, xdraw.Src, nil)
	} else {
		draw.Draw(dst, dst.Bounds(), base, b.Min, draw.Src)
	}

	layout = layout.normalize()
	if !layout.hasText() {
		return dst, nil
	}

	parsed, err := loadCoverFont(layout.Font)
	if err != nil {
		return nil, err
	}
	titleSize := float64(w) * coverTitleBaseRatio * layout.Scale
	titleFace, err := newFace(parsed, titleSize)
	if err != nil {
		return nil, fmt.Errorf("创建字体失败: %w", err)
	}
	defer titleFace.Close()

	maxWidth := fixed.I(int(float64(w) * coverTitleSafeWidth))
	lines := wrapCoverText(titleFace, layout.Title, maxWidth, coverTitleMaxLines)

	titleMetrics := titleFace.Metrics()
	titleLineH := titleMetrics.Height.Ceil()
	blockH := len(lines) * titleLineH

	var authorFace font.Face
	authorGap, authorLineH := 0, 0
	if layout.Author != "" {
		authorFace, err = newFace(parsed, titleSize*0.34)
		if err != nil {
			return nil, fmt.Errorf("创建字体失败: %w", err)
		}
		defer authorFace.Close()
		authorLineH = authorFace.Metrics().Height.Ceil()
		authorGap = int(titleSize * 0.5)
		blockH += authorGap + authorLineH
	}

	blockTop := 0
	switch layout.Position {
	case "center":
		blockTop = (h - blockH) / 2
	case "bottom":
		blockTop = h - int(float64(h)*0.075) - blockH
	default:
		blockTop = int(float64(h) * 0.075)
	}
	blockTop = min(max(blockTop, 0), max(h-blockH, 0))

	fg, shadow := coverTextColors(layout.Theme)
	drawCoverScrim(dst, layout, blockTop, blockH, titleSize)

	// 阴影单独描一遍：AI 出的画面明暗不可控，纯色字压在亮部会糊掉，
	// 一层偏移的半透明描边能保证任何底图上都读得清。
	offset := max(int(titleSize*0.035), 1)
	baseline := blockTop + titleMetrics.Ascent.Ceil()
	for _, line := range lines {
		drawCentered(dst, titleFace, line, w, baseline+offset, offset, shadow)
		drawCentered(dst, titleFace, line, w, baseline, 0, fg)
		baseline += titleLineH
	}

	if authorFace != nil {
		ruleY := blockTop + len(lines)*titleLineH + authorGap/2
		drawCoverRule(dst, w, ruleY, titleSize, fg)
		ab := blockTop + len(lines)*titleLineH + authorGap + authorFace.Metrics().Ascent.Ceil()
		drawCentered(dst, authorFace, layout.Author, w, ab+offset, offset, shadow)
		drawCentered(dst, authorFace, layout.Author, w, ab, 0, fg)
	}
	return dst, nil
}

func coverTextColors(theme string) (fg, shadow color.Color) {
	if theme == "dark" {
		return color.NRGBA{R: 0x1c, G: 0x18, B: 0x14, A: 0xff}, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x8c}
	}
	return color.NRGBA{R: 0xff, G: 0xfd, B: 0xf8, A: 0xff}, color.NRGBA{A: 0x9e}
}

// drawCoverScrim 在文字区域压一层渐变，让文字从画面里"浮"出来。
// 渐变而非实色块：实色块会在封面上切出一条生硬的横带，像贴纸而不像印刷。
func drawCoverScrim(dst *image.RGBA, layout CoverTitleLayout, blockTop, blockH int, titleSize float64) {
	h := dst.Bounds().Dy()
	pad := int(titleSize * 0.9)
	bandTop := max(blockTop-pad, 0)
	bandBottom := min(blockTop+blockH+pad, h)
	if bandBottom <= bandTop {
		return
	}

	var peak float64 = 0.52
	scrim := color.NRGBA{}
	if layout.Theme == "dark" {
		scrim = color.NRGBA{R: 0xff, G: 0xff, B: 0xff}
		peak = 0.58
	}

	band := float64(bandBottom - bandTop)
	center := float64(blockTop) + float64(blockH)/2
	for y := bandTop; y < bandBottom; y++ {
		var t float64 // 0 = 最浓，1 = 完全透明
		switch layout.Position {
		case "top":
			t = float64(y-bandTop) / band
		case "bottom":
			t = float64(bandBottom-y) / band
		default:
			half := band / 2
			t = min(abs(float64(y)-center)/half, 1)
		}
		alpha := peak * pow14(1-t)
		if alpha <= 0.002 {
			continue
		}
		scrim.A = uint8(min(alpha, 1) * 255)
		row := image.Rect(0, y, dst.Bounds().Dx(), y+1)
		draw.Draw(dst, row, image.NewUniform(scrim), image.Point{}, draw.Over)
	}
}

// drawCoverRule 画标题与作者之间的细分隔线（有作者名时才画）。
func drawCoverRule(dst *image.RGBA, w, y int, titleSize float64, fg color.Color) {
	length := int(titleSize * 2.2)
	thick := max(int(titleSize*0.03), 1)
	if length <= 0 || y < 0 || y+thick > dst.Bounds().Dy() {
		return
	}
	r, g, b, _ := fg.RGBA()
	line := color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0x9e}
	rect := image.Rect((w-length)/2, y, (w+length)/2, y+thick)
	draw.Draw(dst, rect, image.NewUniform(line), image.Point{}, draw.Over)
}

func drawCentered(dst *image.RGBA, face font.Face, text string, w, baseline, dx int, col color.Color) {
	if text == "" {
		return
	}
	adv := font.MeasureString(face, text)
	x := (fixed.I(w) - adv) / 2
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.Point26_6{X: x + fixed.I(dx), Y: fixed.I(baseline)},
	}
	d.DrawString(text)
}

// wrapCoverText 按可用宽度折行。中文逐字折，英文优先在空格处折。
// 超过 maxLines 的部分截断并加省略号——封面上塞不下的书名硬排只会更难看。
func wrapCoverText(face font.Face, text string, maxWidth fixed.Int26_6, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var lines []string
	for _, seg := range strings.Split(text, "\n") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		var cur []rune
		for _, r := range seg {
			cand := append(append([]rune{}, cur...), r)
			if len(cur) > 0 && font.MeasureString(face, string(cand)) > maxWidth {
				if idx := lastSpace(cur); idx > 0 {
					lines = append(lines, strings.TrimSpace(string(cur[:idx])))
					cur = append(append([]rune{}, cur[idx+1:]...), r)
				} else {
					lines = append(lines, string(cur))
					cur = []rune{r}
				}
				continue
			}
			cur = cand
		}
		if len(cur) > 0 {
			lines = append(lines, string(cur))
		}
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = truncateRunes(lines[maxLines-1], max(len([]rune(lines[maxLines-1]))-1, 1))
	}
	return lines
}

func lastSpace(runes []rune) int {
	for i := len(runes) - 1; i > 0; i-- {
		if runes[i] == ' ' {
			return i
		}
	}
	return -1
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// pow14 返回 x^1.4：渐变收得比线性快一点，边缘过渡更自然。
func pow14(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Pow(x, 1.4)
}

// ── 编码 ──

// encodeCoverPNG 成品统一编 PNG：文字边缘不能被 JPEG 的块效应啃掉。
func encodeCoverPNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("编码封面失败: %w", err)
	}
	return buf.Bytes(), nil
}

// encodeCoverPreview 预览走 JPEG：只在界面上显示一眼，体积比清晰度更要紧
// （data URL 要经 Wails 的 JSON 通道传给前端）。
func encodeCoverPreview(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 86}); err != nil {
		return nil, fmt.Errorf("编码预览失败: %w", err)
	}
	return buf.Bytes(), nil
}
