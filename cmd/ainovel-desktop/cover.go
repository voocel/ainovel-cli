package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // 导入的封面可能是 JPEG/WebP，解码器要注册进 image.Decode
	_ "image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	_ "golang.org/x/image/webp"
)

// ── 小说封面 ──
//
// 封面是桌面层的增强能力，与创作引擎解耦：
//   - 生图配置存 ~/.ainovel/imagegen.json（不进引擎的 config.json，因为引擎的
//     provider 配置是纯文本 chat 语义，混进生图字段会让两边都变脏）
//   - 封面文件存 <书目录>/cover.<ext>，与该书同生共死
//   - 绘画提示词用引擎现有的文本模型生成（复用 CoCreateStream 的模型档位），
//     生图本身走独立 HTTP 客户端（见 imagegen.go）

const (
	coverBaseName           = "cover"
	defaultCoverStylePreset = "tomato"
	defaultCoverGenre       = "auto"
	defaultCoverComposition = "auto"
	defaultImageGenModel    = "gpt-image-2"
	defaultImageGenSize     = "1024x1536"
	// coverSourceBaseName 是"未叠字的原图"，放在 meta/ 下：writeCoverComposite 会清掉书目录里
	// 其他扩展名的 cover.*，原图必须躲开那次清理，否则改一次排版就得重新花钱生图。
	coverSourceBaseName = "cover.base"
	// coverPreviewMaxDim 是排版预览的长边上限。原图通常 1024x1536，全尺寸栅格化
	// 每次要几百毫秒并产出兆级 data URL，调字号的滑杆会明显拖手。
	coverPreviewMaxDim = 640
)

var coverImageExts = []string{"png", "jpg", "jpeg", "webp"}

// imageGenSettingsPath 是生图配置的落盘位置。
func imageGenSettingsPath() string {
	return filepath.Join(configDir(), "imagegen.json")
}

// coverStore 串行化封面读写，避免并发生成互相覆盖。
type coverStore struct {
	mu sync.Mutex

	// jobMu 单独保护 activeDir，不能复用 mu：mu 在整个生图期间（可能十分钟）
	// 都被持有，用它读状态会把查询一起阻塞十分钟。
	jobMu     sync.Mutex
	activeDir string
}

func (c *coverStore) setActive(dir string) {
	c.jobMu.Lock()
	defer c.jobMu.Unlock()
	c.activeDir = dir
}

func (c *coverStore) active() string {
	c.jobMu.Lock()
	defer c.jobMu.Unlock()
	return c.activeDir
}

// CoverJobDir 返回正在生图的那本书的目录（无在途生图则空串）。
//
// 界面挂载时查一次。生图可能是在别的界面发起的，界面切换会把原来的组件卸载掉，
// 只靠每秒一次的 cover:progress 心跳，新界面在头一秒内不知道有图在生——
// 而这一秒里点一下别的书，就足以把这张已经计费的图取消掉。
func (a *App) CoverJobDir() string {
	return a.cover.active()
}

// ── 生图设置 ──

// ImageGenSettings 是投影给前端的生图配置（API Key 脱敏）。
type ImageGenSettings struct {
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
	Size       string `json:"size"`
	HasAPIKey  bool   `json:"hasAPIKey"`
	APIKeyHint string `json:"apiKeyHint"`
	Path       string `json:"path"`
}

// GetImageGenSettings 读取生图配置（不返回完整 API Key）。
func (a *App) GetImageGenSettings() (ImageGenSettings, error) {
	cfg, err := loadImageGenConfig()
	if err != nil {
		return ImageGenSettings{}, err
	}
	out := ImageGenSettings{
		BaseURL: cfg.BaseURL, Model: cfg.Model, Size: cfg.Size,
		HasAPIKey: cfg.APIKey != "", Path: imageGenSettingsPath(),
	}
	if out.Model == "" {
		out.Model = defaultImageGenModel
	}
	if out.Size == "" {
		out.Size = defaultImageGenSize
	}
	if cfg.APIKey != "" {
		out.APIKeyHint = maskKey(cfg.APIKey)
	}
	return out, nil
}

// ImageGenDraft 是前端提交的生图配置。apiKeyAction: keep / replace / clear。
type ImageGenDraft struct {
	BaseURL      string `json:"baseURL"`
	Model        string `json:"model"`
	Size         string `json:"size"`
	APIKeyAction string `json:"apiKeyAction"`
	APIKey       string `json:"apiKey"`
}

// SaveImageGenSettings 持久化生图配置。
func (a *App) SaveImageGenSettings(draft ImageGenDraft) error {
	cur, err := loadImageGenConfig()
	if err != nil {
		return err
	}
	cur, err = mergeImageGenConfig(cur, draft)
	if err != nil {
		return err
	}
	return saveImageGenConfig(cur)
}

func mergeImageGenConfig(cur imageGenConfig, draft ImageGenDraft) (imageGenConfig, error) {
	cur.BaseURL = normalizeImageGenBaseURL(draft.BaseURL)
	if cur.BaseURL == "" {
		return imageGenConfig{}, fmt.Errorf("请填写生图服务的 Base URL")
	}
	parsed, err := url.ParseRequestURI(cur.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return imageGenConfig{}, fmt.Errorf("生图服务 Base URL 无效，请填写完整的 http:// 或 https:// 地址")
	}
	cur.Model = strings.TrimSpace(draft.Model)
	if cur.Model == "" {
		return imageGenConfig{}, fmt.Errorf("请填写生图模型名称")
	}
	cur.Size = strings.TrimSpace(draft.Size)
	if cur.Size == "" {
		cur.Size = defaultImageGenSize
	}
	switch cur.Size {
	case "768x1024", "1024x1536", "1024x1024", "1536x1024":
	default:
		return imageGenConfig{}, fmt.Errorf("不支持的生图尺寸 %q", cur.Size)
	}
	switch strings.TrimSpace(draft.APIKeyAction) {
	case "", "keep":
	case "replace":
		cur.APIKey = strings.TrimSpace(draft.APIKey)
		if cur.APIKey == "" {
			return imageGenConfig{}, fmt.Errorf("更换 API Key 时不能提交空值")
		}
	case "clear":
		cur.APIKey = ""
	default:
		return imageGenConfig{}, fmt.Errorf("未知的 API Key 操作: %q", draft.APIKeyAction)
	}
	return cur, nil
}

func loadImageGenConfig() (imageGenConfig, error) {
	data, err := os.ReadFile(imageGenSettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return imageGenConfig{Model: defaultImageGenModel, Size: defaultImageGenSize}, nil
		}
		return imageGenConfig{}, fmt.Errorf("读取生图配置失败: %w", err)
	}
	var cfg imageGenConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return imageGenConfig{}, fmt.Errorf("生图配置格式错误: %w", err)
	}
	cfg.BaseURL = normalizeImageGenBaseURL(cfg.BaseURL)
	if cfg.Model == "" {
		cfg.Model = defaultImageGenModel
	}
	if cfg.Size == "" {
		cfg.Size = defaultImageGenSize
	}
	return cfg, nil
}

func saveImageGenConfig(cfg imageGenConfig) error {
	if err := os.MkdirAll(filepath.Dir(imageGenSettingsPath()), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 含 API Key，权限 0600；原子替换避免写坏。
	tmp := imageGenSettingsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, imageGenSettingsPath())
}

func maskKey(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) == 0 {
		return ""
	}
	if len(r) < 16 {
		return "******"
	}
	return string(r[:4]) + "******" + string(r[len(r)-4:])
}

// ── 封面 ──

// CoverInfo 描述当前书的封面状态。
type CoverInfo struct {
	Exists bool `json:"exists"`
	// DataURL 是可直接用于 <img src> 的 base64 内联数据（避免前端读本地文件的权限问题）。
	DataURL   string `json:"dataURL"`
	Path      string `json:"path"`
	Prompt    string `json:"prompt"`
	UpdatedAt string `json:"updatedAt"`
	// HasBase 表示存在未叠字的原图，也即"可以只改排版、不重新生图"。
	HasBase bool             `json:"hasBase"`
	Layout  CoverTitleLayout `json:"layout"`
	// Preset 保留给上一版前端，值与 Platform 相同。
	Preset              string `json:"preset"`
	Platform            string `json:"platform"`
	Genre               string `json:"genre"`
	ResolvedGenre       string `json:"resolvedGenre"`
	Composition         string `json:"composition"`
	PlatformPath        string `json:"platformPath"`
	HasPlatformArtifact bool   `json:"hasPlatformArtifact"`
}

// coverMeta 记录生成封面时用的提示词，便于用户回看与微调后重生成。
// Layout 是上一次的叠字排版，重开面板时照原样恢复（不然每次都要重新调一遍）。
type coverMeta struct {
	Prompt    string            `json:"prompt"`
	Model     string            `json:"model"`
	UpdatedAt time.Time         `json:"updatedAt"`
	File      string            `json:"file"`
	Layout    *CoverTitleLayout `json:"layout,omitempty"`
	// Preset 是旧字段。新版本用 Platform，读取时仍兼容。
	Preset        string `json:"preset,omitempty"`
	Platform      string `json:"platform,omitempty"`
	Genre         string `json:"genre,omitempty"`
	ResolvedGenre string `json:"resolvedGenre,omitempty"`
	Composition   string `json:"composition,omitempty"`
}

type coverSpec struct {
	Prompt        string
	Model         string
	Platform      string
	Genre         string
	ResolvedGenre string
	Composition   string
}

func (s coverSpec) normalize() coverSpec {
	s.Platform = normalizeCoverPlatform(s.Platform)
	s.Genre = normalizeCoverGenre(s.Genre)
	s.ResolvedGenre = normalizeCoverGenre(s.ResolvedGenre)
	if s.ResolvedGenre == "auto" {
		if s.Genre == "auto" {
			s.ResolvedGenre = inferCoverGenre(s.Prompt)
		} else {
			s.ResolvedGenre = s.Genre
		}
	}
	s.Composition = normalizeCoverComposition(s.Composition)
	return s
}

func coverSpecFromMeta(meta coverMeta) coverSpec {
	platform := meta.Platform
	if strings.TrimSpace(platform) == "" {
		platform = meta.Preset
	}
	return (coverSpec{
		Prompt: meta.Prompt, Model: meta.Model, Platform: platform,
		Genre: meta.Genre, ResolvedGenre: meta.ResolvedGenre, Composition: meta.Composition,
	}).normalize()
}

func coverMetaPath(bookDir string) string {
	return filepath.Join(bookDir, "meta", "cover.json")
}

func readCoverMeta(bookDir string) coverMeta {
	var meta coverMeta
	if raw, err := os.ReadFile(coverMetaPath(bookDir)); err == nil {
		_ = json.Unmarshal(raw, &meta)
	}
	return meta
}

// findCoverFile 返回书目录下已存在的封面文件路径与 mime（无则空串）。
func findCoverFile(bookDir string) (string, string) {
	return findImageByBase(bookDir, coverBaseName)
}

// findCoverSourceFile 返回未叠字的原图（老书没有这个文件，返回空串）。
func findCoverSourceFile(bookDir string) (string, string) {
	return findImageByBase(filepath.Join(bookDir, "meta"), coverSourceBaseName)
}

func findImageByBase(dir, base string) (string, string) {
	for _, ext := range coverImageExts {
		p := filepath.Join(dir, base+"."+ext)
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			return p, sniffImageMime(data)
		}
	}
	return "", ""
}

// GetCover 返回当前书的封面（含可直接显示的 data URL）。
func (a *App) GetCover() (CoverInfo, error) {
	h, err := a.reqHost()
	if err != nil {
		return CoverInfo{}, err
	}
	return readCover(h.Dir(), h.Snapshot().NovelName)
}

func readCover(bookDir, novelName string) (CoverInfo, error) {
	out := CoverInfo{
		Layout: defaultCoverTitleLayout(novelName), Preset: defaultCoverStylePreset,
		Platform: defaultCoverStylePreset, Genre: defaultCoverGenre,
		ResolvedGenre: "urban", Composition: defaultCoverComposition,
	}
	basePath, _ := findCoverSourceFile(bookDir)
	out.HasBase = basePath != ""

	meta := readCoverMeta(bookDir)
	spec := coverSpecFromMeta(meta)
	out.Prompt = spec.Prompt
	out.Preset = spec.Platform
	out.Platform = spec.Platform
	out.Genre = spec.Genre
	out.ResolvedGenre = spec.ResolvedGenre
	out.Composition = spec.Composition
	if platformPath := coverPlatformArtifactPath(bookDir, spec.Platform); platformPath != "" {
		if info, err := os.Stat(platformPath); err == nil && info.Size() > 0 {
			out.PlatformPath = platformPath
			out.HasPlatformArtifact = true
		}
	}
	if !meta.UpdatedAt.IsZero() {
		out.UpdatedAt = meta.UpdatedAt.Format(rfc3339)
	}
	if meta.Layout != nil {
		// 沿用上次排版，但书名仍以引擎里的当前书名为准（改过书名要跟着变）；
		// 用户手填过别的标题时以他填的为准。
		saved := meta.Layout.normalize()
		if saved.Title == "" {
			saved.Title = out.Layout.Title
		}
		out.Layout = saved
	}

	path, mime := findCoverFile(bookDir)
	if path == "" {
		return out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out, nil
	}
	out.Exists = true
	out.Path = path
	out.DataURL = toDataURL(data, mime)
	return out, nil
}

// SuggestCoverPrompt 根据本书设定拼一段英文绘画提示词草稿。
// 生图模型对英文理解更准，所以画面描述用英文；人名等专有信息原样带入。
func (a *App) SuggestCoverPrompt(platform, genre, composition string) (string, error) {
	ctx, err := a.coverPromptDraft(platform, genre, composition)
	return ctx.Draft, err
}

type coverPromptContext struct {
	Facts         string
	Draft         string
	ResolvedGenre string
}

// coverPromptDraft 同时返回给模型看的设定摘要与确定性草稿，两个入口共用一次数据读取。
func (a *App) coverPromptDraft(platform, genre, composition string) (coverPromptContext, error) {
	h, err := a.reqHost()
	if err != nil {
		return coverPromptContext{}, err
	}
	found, err := a.GetFoundation()
	if err != nil {
		return coverPromptContext{}, err
	}
	snap := h.Snapshot()

	if len(found.Characters) == 0 && found.Premise == "" {
		return coverPromptContext{}, fmt.Errorf("本书还没有设定，无法生成封面提示词草稿；请先完成规划")
	}

	genre = normalizeCoverGenre(genre)
	if genre == "auto" {
		var inferText strings.Builder
		inferText.WriteString(snap.NovelName)
		inferText.WriteString("\n")
		inferText.WriteString(found.Premise)
		for i, entry := range found.Outline {
			if i >= 3 {
				break
			}
			inferText.WriteString("\n")
			inferText.WriteString(entry.Title)
			inferText.WriteString(" ")
			inferText.WriteString(entry.CoreEvent)
		}
		genre = inferCoverGenre(inferText.String())
	}
	platform = normalizeCoverPlatform(platform)
	resolvedComposition := resolveCoverComposition(composition, genre)

	var parts []string
	parts = append(parts, "Commercial digital-painting cover for a Chinese web novel.")

	// 主角外形是画面主体最有用的一句；取第一位核心角色的描述。
	if len(found.Characters) > 0 {
		lead := found.Characters[0]
		desc := truncateRunes(lead.Description, 180)
		if desc != "" {
			parts = append(parts, "Central figure: "+desc)
		}
	}
	// 世界观里的场景/地理类规则能提供环境信息。
	for _, r := range found.WorldRules {
		if r.Category == "geography" || r.Category == "society" {
			if rule := truncateRunes(r.Rule, 160); rule != "" {
				parts = append(parts, "Setting: "+rule)
				break
			}
		}
	}
	if premise := truncateRunes(found.Premise, 220); premise != "" {
		parts = append(parts, "Story hook: "+premise)
	}
	parts = append(parts,
		coverPlatformDefinitions[platform].Direction,
		coverGenreDefinitions[genre].Direction,
		coverCompositionDefinitions[resolvedComposition].Direction,
	)

	// 给模型的这份是中文原始设定，不是上面那段英文草稿：让它自己从设定里提炼画面，
	// 比让它去改写一段已经被压扁的英文更容易出好结果。
	var facts_ []string
	if name := strings.TrimSpace(snap.NovelName); name != "" {
		facts_ = append(facts_, "书名："+name)
	}
	facts_ = append(facts_, "封面题材："+genre)
	if p := truncateRunes(found.Premise, 400); p != "" {
		facts_ = append(facts_, "故事前提："+p)
	}
	for i, c := range found.Characters {
		if i >= 3 {
			break
		}
		if desc := truncateRunes(c.Description, 200); desc != "" {
			facts_ = append(facts_, "角色 "+c.Name+"："+desc)
		}
	}
	for _, r := range found.WorldRules {
		if r.Category != "geography" && r.Category != "society" {
			continue
		}
		if rule := truncateRunes(r.Rule, 200); rule != "" {
			facts_ = append(facts_, "世界观："+rule)
		}
		if len(facts_) > 8 {
			break
		}
	}

	return coverPromptContext{
		Facts: strings.Join(facts_, "\n"), Draft: strings.Join(parts, " "), ResolvedGenre: genre,
	}, nil
}

func (a *App) resolveCoverGenre(requested, fallbackText string) string {
	requested = normalizeCoverGenre(requested)
	if requested != "auto" {
		return requested
	}
	var text strings.Builder
	text.WriteString(fallbackText)
	if h, err := a.reqHost(); err == nil {
		text.WriteString("\n")
		text.WriteString(h.Snapshot().NovelName)
	}
	if found, err := a.GetFoundation(); err == nil {
		text.WriteString("\n")
		text.WriteString(found.Premise)
		for i, entry := range found.Outline {
			if i >= 3 {
				break
			}
			text.WriteString("\n")
			text.WriteString(entry.Title)
			text.WriteString(" ")
			text.WriteString(entry.CoreEvent)
		}
	}
	return inferCoverGenre(text.String())
}

// coverPromptSystem 是润色提示词用的系统提示。
//
// 要求只输出提示词本身：这段文本会直接进 <textarea>，任何"好的，这是您的提示词"
// 一类前言都会被原样送进生图接口，污染画面。
const coverPromptSystem = `你是资深书籍封面美术指导，负责把中文小说设定翻译成给 AI 生图模型的英文提示词。

要求：
1. 只输出提示词正文，一段连续的英文，不要标题、不要编号、不要解释、不要引号包裹。
2. 描述具体可画的东西：主体人物的外形与姿态、环境、光线、色调、材质、镜头感。避免"史诗""震撼"这类无法落到画面的空词。
3. 画面里绝对不能出现任何文字、字母、标题、水印、签名——书名会在后期用排版叠上去。
4. 竖版封面构图，标题安全区仍要保留有颜色、有光线、有纹理的氛围，禁止纯黑或纯色空块。
5. 控制在 120 个英文单词以内。
6. 不要臆造设定里没有的关键人物或情节。
7. 明确使用商业数字插画，不要照片感或电影剧照感。`

// OptimizeCoverPrompt 用本书已配置的文本模型把设定润色成一段绘画提示词。
//
// 与 SuggestCoverPrompt 的关系：后者是确定性拼装（即时、免费、必然成功），
// 这里是"花一次 token 换更好的画面描述"。失败不降级为静默——直接把错误交给
// 前端，由用户决定是重试还是继续用草稿。
//
// current 非空时表示用户手上已有一版（可能是草稿，也可能是他自己改的），
// 一并交给模型作为参考，避免润色把他刚写的方向抹掉。
func (a *App) OptimizeCoverPrompt(current, platform, genre, composition string) (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	promptCtx, err := a.coverPromptDraft(platform, genre, composition)
	if err != nil {
		return "", err
	}

	var req strings.Builder
	req.WriteString("本书设定如下：\n")
	req.WriteString(promptCtx.Facts)
	if cur := truncateRunes(current, 1200); cur != "" {
		req.WriteString("\n\n用户当前使用的提示词（请在此基础上改进，保留他明确表达的方向）：\n")
		req.WriteString(cur)
	}
	req.WriteString("\n\n请输出一段用于生成本书封面的英文提示词。")

	// 与共创同档位（thinking）：这是"写文案"而非"照规则填表"，弱模型出来的
	// 提示词经常是设定的直译，画面感反而不如确定性草稿。
	ctx, jobID := a.jobs.begin("coverprompt")
	defer a.jobs.end("coverprompt", jobID)

	out, err := h.GenerateText(ctx, "thinking",
		coverPromptSystemFor(platform, promptCtx.ResolvedGenre, composition), req.String(), 900)
	if err != nil {
		return "", fmt.Errorf("润色封面提示词失败: %w", err)
	}
	out = sanitizeCoverPrompt(out)
	if out == "" {
		// 模型只回了寒暄/空串时不要把空提示词塞回界面。
		return "", fmt.Errorf("模型没有返回可用的提示词，请重试或直接使用草稿")
	}
	return out, nil
}

// sanitizeCoverPrompt 清掉模型爱加的包装：代码块围栏、整段引号、"Prompt:" 前缀。
// 这些字符会被生图模型当成画面内容的一部分。
func sanitizeCoverPrompt(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"prompt:", "提示词：", "提示词:", "英文提示词：", "英文提示词:"} {
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			s = strings.TrimSpace(s[len(prefix):])
		}
	}
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	// 提示词进的是单行 textarea 语义，多余换行统一压成空格。
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// GenerateCover 调生图服务生成封面并存入书目录，返回新的封面信息。
// 生图很慢（实测 1-9 分钟，取决于服务商与下行带宽），前端显示进度条并可取消。
func (a *App) GenerateCover(prompt, platform, genre, composition string) (CoverInfo, error) {
	h, err := a.reqHost()
	if err != nil {
		return CoverInfo{}, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return CoverInfo{}, fmt.Errorf("请填写封面提示词")
	}
	cfg, err := loadImageGenConfig()
	if err != nil {
		return CoverInfo{}, err
	}

	a.cover.mu.Lock()
	defer a.cover.mu.Unlock()

	// 目录必须在开始时就定下来：生图要几分钟，期间用户可能切到别的书。
	// 若等拿到图再读 h.Dir()，就会把这本书的封面写进另一本书。
	// 书名同理——它决定叠上去的标题，也必须是发起时这本书的名字。
	bookDir := h.Dir()
	novelName := strings.TrimSpace(h.Snapshot().NovelName)

	// 让 CoverJobDir 能查到在途生图（前端挂载时补齐心跳之外的空窗）。
	a.cover.setActive(bookDir)
	defer a.cover.setActive("")

	ctx, jobID := a.jobs.begin("cover")
	defer a.jobs.end("cover", jobID)

	// 生图可能要几分钟（模型算完还要传几 MB base64），期间每秒播报已等待秒数。
	// 没有这个心跳，前端只有一个转圈，用户无法判断是在等还是已经卡死。
	//
	// 带上 bookDir：书库页据此拦住"生图期间切到别的书"——切书会重建 Host 并
	// abortAll 掉在途生图，而那张图服务端已经计费了。
	a.emit("cover:progress", map[string]any{
		"elapsedSec": 0,
		"budgetSec":  int(imageGenTotalBudget.Seconds()),
		"bookDir":    bookDir,
	})
	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		started := time.Now()
		for {
			select {
			case <-t.C:
				a.emit("cover:progress", map[string]any{
					"elapsedSec": int(time.Since(started).Seconds()),
					"budgetSec":  int(imageGenTotalBudget.Seconds()),
					"bookDir":    bookDir,
				})
			case <-stopTick:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	platform = normalizeCoverPlatform(platform)
	genre = normalizeCoverGenre(genre)
	resolvedGenre := a.resolveCoverGenre(genre, novelName+"\n"+prompt)
	composition = normalizeCoverComposition(composition)
	generationPrompt := buildCoverGenerationPrompt(prompt, platform, resolvedGenre, composition)
	cfg.Size = imageGenSizeForPlatform(cfg.Size, platform)
	data, mime, err := generateImage(ctx, cfg, generationPrompt)
	close(stopTick)
	// 无论成败都要通知前端收尾，否则书库页会一直以为还在生图而拦着切书。
	a.emit("cover:done", map[string]any{"bookDir": bookDir})
	if err != nil {
		return CoverInfo{}, err
	}

	// 沿用上次排版（首次生图则用默认：书名居上）。用户反馈"封面没有书名"就是
	// 因为以前生完直接落盘，这里改成生完立刻叠字。
	layout := readCoverMeta(bookDir).Layout
	if layout == nil {
		l := defaultCoverTitleLayout(novelName)
		layout = &l
	}
	styledLayout := applyCoverTitleStyle(*layout, resolvedGenre)
	layout = &styledLayout
	spec := (coverSpec{
		Prompt: prompt, Model: cfg.Model, Platform: platform, Genre: genre,
		ResolvedGenre: resolvedGenre, Composition: composition,
	}).normalize()
	if err := commitCover(bookDir, data, mime, spec, layout.normalize()); err != nil {
		return CoverInfo{}, err
	}
	return readCover(bookDir, novelName)
}

// ── 封面叠字 ──

// PreviewCoverTitle 用给定排版重排一版预览（不落盘），供前端边调边看。
// 返回 data URL；缩到 coverPreviewMaxDim 以内以保证滑杆跟手。
func (a *App) PreviewCoverTitle(layout CoverTitleLayout) (string, error) {
	h, err := a.reqHost()
	if err != nil {
		return "", err
	}
	base, err := loadCoverBase(h.Dir())
	if err != nil {
		return "", err
	}
	img, err := renderCoverWithTitle(base, layout, coverPreviewMaxDim)
	if err != nil {
		return "", err
	}
	data, err := encodeCoverPreview(img)
	if err != nil {
		return "", err
	}
	return toDataURL(data, "image/jpeg"), nil
}

// ApplyCoverTitle 用给定排版重排并落盘为正式封面。
//
// 这条路径不碰生图接口：原图（meta/cover.base.*）一直留着，改字号、换位置、
// 换字体都只是重排一次，既不花钱也不会把中文糊掉。
func (a *App) ApplyCoverTitle(layout CoverTitleLayout) (CoverInfo, error) {
	h, err := a.reqHost()
	if err != nil {
		return CoverInfo{}, err
	}
	bookDir := h.Dir()

	a.cover.mu.Lock()
	defer a.cover.mu.Unlock()

	base, err := loadCoverBase(bookDir)
	if err != nil {
		return CoverInfo{}, err
	}
	layout = layout.normalize()
	img, err := renderCoverWithTitle(base, layout, 0)
	if err != nil {
		return CoverInfo{}, err
	}
	data, err := encodeCoverPNG(img)
	if err != nil {
		return CoverInfo{}, err
	}

	meta := readCoverMeta(bookDir)
	if err := writeCoverComposite(bookDir, data, "image/png", coverSpecFromMeta(meta), &layout); err != nil {
		return CoverInfo{}, err
	}
	return readCover(bookDir, h.Snapshot().NovelName)
}

// loadCoverBase 读未叠字的原图。老书（本次改动之前生成的封面）没有 base 文件，
// 退回用成品当底图——它上面本来就没有字，当底图是安全的。
func loadCoverBase(bookDir string) (image.Image, error) {
	path, _ := findCoverSourceFile(bookDir)
	if path == "" {
		path, _ = findCoverFile(bookDir)
	}
	if path == "" {
		return nil, fmt.Errorf("还没有封面图，请先生成或导入一张")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取封面原图失败: %w", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("解析封面原图失败: %w", err)
	}
	return img, nil
}

// CancelCover 取消在途的封面生成。
func (a *App) CancelCover() {
	a.jobs.abort("cover")
}

// ImportCoverFile 用本地图片文件作为封面（不想用 AI 生图时的手动路径）。
func (a *App) ImportCoverFile() (CoverInfo, error) {
	h, err := a.reqHost()
	if err != nil {
		return CoverInfo{}, err
	}
	path, err := wailsOpenImage(a.ctx)
	if err != nil {
		return CoverInfo{}, err
	}
	novelName := strings.TrimSpace(h.Snapshot().NovelName)
	if path == "" {
		return readCover(h.Dir(), novelName) // 用户取消，保持原样
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CoverInfo{}, fmt.Errorf("读取图片失败: %w", err)
	}
	mime := sniffImageMime(data)
	if mime == "application/octet-stream" {
		return CoverInfo{}, fmt.Errorf("不支持的图片格式（请用 PNG / JPG / WebP）")
	}
	a.cover.mu.Lock()
	defer a.cover.mu.Unlock()

	// 导入的图同样走叠字：用户自己找的图往上面排书名的需求和 AI 图一样。
	// 沿用上次排版，没有则用默认。
	meta := readCoverMeta(h.Dir())
	spec := coverSpecFromMeta(meta)
	layout := meta.Layout
	if layout == nil {
		l := defaultCoverTitleLayout(novelName)
		layout = &l
	}
	note := "（本地导入：" + filepath.Base(path) + "）"
	spec.Prompt = note
	spec.Model = ""
	layout = ptrCoverTitleLayout(applyCoverTitleStyle(*layout, spec.ResolvedGenre))
	if err := commitCover(h.Dir(), data, mime, spec, layout.normalize()); err != nil {
		return CoverInfo{}, err
	}
	return readCover(h.Dir(), novelName)
}

// RemoveCover 删除当前书的封面（成品、原图、元数据一起清掉）。
func (a *App) RemoveCover() error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	a.cover.mu.Lock()
	defer a.cover.mu.Unlock()
	if path, _ := findCoverFile(h.Dir()); path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除封面失败: %w", err)
		}
	}
	// 原图留着没有意义：没有成品时它既不显示也不会被重排，只是占几 MB。
	for _, ext := range coverImageExts {
		_ = os.Remove(filepath.Join(h.Dir(), "meta", coverSourceBaseName+"."+ext))
	}
	for _, name := range coverPlatformArtifactNames {
		_ = os.Remove(filepath.Join(h.Dir(), name))
	}
	if err := os.Remove(coverMetaPath(h.Dir())); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除封面元数据失败: %w", err)
	}
	return nil
}

// commitCover 落盘一张新的原图：先存 meta/cover.base.<ext>（供以后免费重排），
// 再按 layout 叠字生成成品 cover.png。
//
// 顺序有讲究：先写原图，叠字失败时用户至少还能在面板里重试排版，不用重新花钱生图。

func ptrCoverTitleLayout(layout CoverTitleLayout) *CoverTitleLayout { return &layout }

func commitCover(bookDir string, data []byte, mime string, spec coverSpec, layout CoverTitleLayout) error {
	_ = archiveCurrentCover(bookDir)
	if err := writeCoverSource(bookDir, data, mime); err != nil {
		return err
	}

	out, outMime := data, mime
	if layout.hasText() {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("解析封面图片失败: %w", err)
		}
		composed, err := renderCoverWithTitle(img, layout, 0)
		if err != nil {
			return err
		}
		if out, err = encodeCoverPNG(composed); err != nil {
			return err
		}
		outMime = "image/png"
	}
	return writeCoverComposite(bookDir, out, outMime, spec, &layout)
}

// writeCoverSource 落盘未叠字的原图到 meta/ 下，并清掉其他扩展名的旧原图。
func writeCoverSource(bookDir string, data []byte, mime string) error {
	ext := imageExtForMime(mime)
	if ext == "bin" {
		return fmt.Errorf("生成结果不是可识别的图片格式")
	}
	metaDir := filepath.Join(bookDir, "meta")
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		return err
	}
	if err := atomicWriteFile(filepath.Join(metaDir, coverSourceBaseName+"."+ext), data, 0o644); err != nil {
		return fmt.Errorf("保存封面原图失败: %w", err)
	}
	for _, old := range coverImageExts {
		if old != ext {
			_ = os.Remove(filepath.Join(metaDir, coverSourceBaseName+"."+old))
		}
	}
	return nil
}

// writeCoverComposite 落盘成品封面与元数据。先完整提交新文件，再清理其他扩展名；
// 写入失败时旧封面仍然保留，避免磁盘满或权限变化导致新旧两张都丢失。
func writeCoverComposite(bookDir string, data []byte, mime string, spec coverSpec, layout *CoverTitleLayout) error {
	spec = spec.normalize()
	ext := imageExtForMime(mime)
	if ext == "bin" {
		return fmt.Errorf("生成结果不是可识别的图片格式")
	}

	file := coverBaseName + "." + ext
	if err := atomicWriteFile(filepath.Join(bookDir, file), data, 0o644); err != nil {
		return err
	}

	for _, old := range coverImageExts {
		if old == ext {
			continue
		}
		_ = os.Remove(filepath.Join(bookDir, coverBaseName+"."+old))
	}
	if err := writeCoverPlatformArtifact(bookDir, data, mime, spec.Platform); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(bookDir, "meta"), 0o700); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(coverMeta{
		Prompt: spec.Prompt, Model: spec.Model, UpdatedAt: time.Now(), File: file, Layout: layout,
		Preset: spec.Platform, Platform: spec.Platform, Genre: spec.Genre,
		ResolvedGenre: spec.ResolvedGenre, Composition: spec.Composition,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(coverMetaPath(bookDir), meta, 0o600)
}

// atomicWriteFile 同目录临时文件 + Sync + Rename。封面动辄几 MB，直接 WriteFile
// 到目标路径时一旦写一半失败（磁盘满、被杀进程），旧图和新图会一起丢。
func atomicWriteFile(target string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(target)
	tmpFile, err := os.CreateTemp(dir, ".cover-*"+filepath.Ext(target))
	if err != nil {
		return fmt.Errorf("写入封面失败: %w", err)
	}
	tmp := tmpFile.Name()
	committed := false
	defer func() {
		_ = tmpFile.Close()
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("设置封面权限失败: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("写入封面失败: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("同步封面失败: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("关闭封面临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("保存封面失败: %w", err)
	}
	committed = true
	return nil
}

// wailsOpenImage 弹原生图片选择对话框。
func wailsOpenImage(ctx context.Context) (string, error) {
	return wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title: "选择封面图片",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "图片 (*.png;*.jpg;*.jpeg;*.webp)", Pattern: "*.png;*.jpg;*.jpeg;*.webp"},
		},
	})
}

func toDataURL(data []byte, mime string) string {
	if mime == "" || mime == "application/octet-stream" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func truncateRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}
