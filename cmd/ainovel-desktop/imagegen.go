package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── 生图客户端（OpenAI 兼容同步接口 + JarlessAPI/sub2api 异步任务） ──
//
// 为什么不复用引擎的模型配置：引擎的 LLM 层（agentcore + litellm）是纯文本 chat
// completions，没有任何图像能力。生图是完全不同的 API 形态（一次请求返回图片
// URL 或 base64），因此单独走一个最小 HTTP 客户端，与创作引擎解耦——它不进 agent
// 循环、不消耗引擎的 usage 统计、失败也不影响创作。
//
// 只实现 OpenAI 兼容形状：官方 gpt-image-2，以及绝大多数国内中转和聚合网关
// 都兼容这个形状，一个实现覆盖面最广。

// imageGenTotalBudget 是一次生图从发起到拿到完整图片的总预算。
//
// 实测某些中转网关上 gpt-image-2 单张要 60s 生成 + 数百秒传输 2.9MB base64
// （总计约 9 分钟）。完全不设上限会让用户对着转圈无从判断；设得太短又会白烧
// 一次已经付费的生成。JarlessAPI 的异步任务契约允许最长轮询 15 分钟；同步服务也
// 共用这个总预算，避免任务已经在服务端完成、客户端却在取图前先行超时。
// 用户随时可以点取消（走 ctx），这只是兜底。
const imageGenTotalBudget = 15 * time.Minute

// imageGenHeaderTimeout 不能短于总预算。同步兼容接口通常等图片完整生成后才返回
// 响应头；若这里仍是 5 分钟，服务端在第 6 分钟成功也会被客户端提前断开并白白计费。
const imageGenHeaderTimeout = imageGenTotalBudget

const maxDownloadedImageBytes int64 = 64 << 20

var imageTaskPollInterval = 2500 * time.Millisecond

const jarlessAPIBaseURL = "https://jarlessapi.com"

// imageRequest 是 /v1/images/generations 的请求体。
type imageRequest struct {
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	N            int    `json:"n"`
	Size         string `json:"size,omitempty"`
	Quality      string `json:"quality,omitempty"`
	Stream       *bool  `json:"stream,omitempty"`
	Sub2APIAsync bool   `json:"sub2api_async,omitempty"`
	// ResponseFormat 只给明确需要 URL 的异步适配器使用。gpt-image-2 会自行返回
	// b64_json，官方接口会拒绝旧 DALL-E 的 response_format 参数。
	ResponseFormat string `json:"response_format,omitempty"`
}

// imageResponse 覆盖两种返回形态：b64_json 或 url。
type imageItem struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
	OutputFormat  string `json:"output_format"`
}

type imageAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type imageResponse struct {
	Data  []imageItem    `json:"data"`
	Error *imageAPIError `json:"error"`
}

// imageTask 是 JarlessAPI/sub2api 的异步图片任务形状。
type imageTask struct {
	Object   string         `json:"object"`
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Progress *float64       `json:"progress"`
	Data     []imageItem    `json:"data"`
	Error    *imageAPIError `json:"error"`
}

// imageGenConfig 是生图服务的连接配置（独立于引擎的 provider 配置）。
type imageGenConfig struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	Size    string `json:"size"`
	// Async 只供测试或显式适配器使用，不写入用户配置。JarlessAPI 会按域名自动识别。
	Async bool `json:"-"`
}

// generateImage 调用生图接口并返回图片字节与实际使用的 mime 类型。
func generateImage(ctx context.Context, cfg imageGenConfig, prompt string) ([]byte, string, error) {
	base := normalizeImageGenBaseURL(cfg.BaseURL)
	if base == "" {
		return nil, "", fmt.Errorf("请先在设置里配置生图服务的 Base URL")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, "", fmt.Errorf("请先在设置里配置生图模型名称")
	}
	endpoint := imageGenerationEndpoint(base)
	async := cfg.Async || isJarlessAPI(base)

	size := strings.TrimSpace(cfg.Size)
	if size == "" {
		size = "1024x1536" // 竖版更接近书籍封面比例
	}

	requestBody := imageRequest{Model: model, Prompt: prompt, N: 1, Size: size}
	if async {
		stream := false
		requestBody.ResponseFormat = "url"
		requestBody.Quality = "high"
		requestBody.Stream = &stream
		requestBody.Sub2APIAsync = true
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, "", err
	}

	// 总预算兜底：ctx 已可被用户取消，这里再加一层时限，避免网关挂住时无限等待。
	ctx, cancelBudget := context.WithTimeout(ctx, imageGenTotalBudget)
	defer cancelBudget()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	// 不用 http.Client.Timeout：它是"整个请求周期"的硬上限（含读完响应体），
	// 一旦越线就退化成 context deadline exceeded，看不出是慢还是断。
	// 实测 gpt-image-2 这类模型单张要 4 分钟以上，再叠加 ~1MB base64 的传输，
	// 很容易撞线。改为只约束建连与响应头等待，生成本身的时长由调用方 ctx 决定
	// （前端可随时取消，见 CancelCover）。
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: imageGenHeaderTimeout,
			ForceAttemptHTTP2:     true,
		},
	}
	started := time.Now()
	slog.Info("提交生图请求", "module", "desktop", "endpoint", endpoint, "model", model, "async", async)
	resp, err := client.Do(req)
	if err != nil {
		// 区分"用户取消"与"真的失败"：ctx 被取消时不该报成服务端故障。
		elapsed := time.Since(started).Round(time.Second)
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			slog.Warn("生图被取消", "module", "desktop", "model", model, "elapsed", elapsed)
			return nil, "", fmt.Errorf("生图已取消（已等待 %s）", elapsed)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			slog.Error("生图超出总预算", "module", "desktop", "model", model,
				"elapsed", elapsed, "budget", imageGenTotalBudget)
			return nil, "", fmt.Errorf("生图超时：%s 内服务端未返回。可改用更小的尺寸或换个生图模型再试",
				imageGenTotalBudget)
		}
		slog.Error("生图请求失败", "module", "desktop", "endpoint", endpoint,
			"model", model, "elapsed", elapsed, "err", err)
		return nil, "", fmt.Errorf("请求生图服务失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		// 响应体读一半断掉最容易被误判成"格式错误"：base64 图片有近 1MB，
		// 中途断流后 json.Unmarshal 报的是语法错误，会把人带偏。这里点明。
		//
		// 三种中断要分开说，否则查因只能靠猜：用户点了取消、超过总预算、
		// 网络真的断了。ctx.Err() 能区分前两者（Canceled vs DeadlineExceeded）。
		elapsed := time.Since(started).Round(time.Second)
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			slog.Warn("生图被取消", "module", "desktop", "got", len(raw), "elapsed", elapsed)
			return nil, "", fmt.Errorf("生图已取消（已收到 %d 字节，等待 %s）", len(raw), elapsed)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			slog.Error("生图超出总预算", "module", "desktop", "got", len(raw),
				"elapsed", elapsed, "budget", imageGenTotalBudget)
			return nil, "", fmt.Errorf("生图超时：%s 内只收到 %d 字节。该服务商这次太慢，"+
				"可改用更小的尺寸或换个生图模型再试", imageGenTotalBudget, len(raw))
		default:
			slog.Error("读取生图响应中断", "module", "desktop", "got", len(raw),
				"elapsed", elapsed, "err", err)
			return nil, "", fmt.Errorf("读取生图响应中断（已收到 %d 字节，耗时 %s）: %w",
				len(raw), elapsed, err)
		}
	}
	slog.Info("生图响应", "module", "desktop", "status", resp.StatusCode,
		"bytes", len(raw), "elapsed", time.Since(started).Round(time.Second), "model", model)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 原样带出服务端错误信息：配置类问题（模型名错、余额不足、无权限）
		// 全靠这段文案定位，不要吞掉。
		return nil, "", fmt.Errorf("生图服务返回 %d：%s", resp.StatusCode, truncateForError(raw))
	}
	if async {
		var task imageTask
		if err := json.Unmarshal(raw, &task); err != nil {
			return nil, "", fmt.Errorf("解析异步生图任务失败: %w（原始响应：%s）", err, truncateForError(raw))
		}
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Status) == "" {
			return nil, "", fmt.Errorf("生图接口未返回有效的异步任务（原始响应：%s）", truncateForError(raw))
		}
		slog.Info("异步生图任务已创建", "module", "desktop", "task_id", task.ID, "status", task.Status)
		return pollImageTask(ctx, client, base, cfg.APIKey, task, imageTaskPollInterval)
	}

	var parsed imageResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "", fmt.Errorf("解析生图响应失败: %w（原始响应：%s）", err, truncateForError(raw))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, "", fmt.Errorf("生图服务报错：%s", parsed.Error.Message)
	}
	if len(parsed.Data) == 0 {
		return nil, "", fmt.Errorf("生图服务未返回图片（原始响应：%s）", truncateForError(raw))
	}

	return imageItemBytes(ctx, client, base, cfg.APIKey, parsed.Data[0])
}

func imageItemBytes(ctx context.Context, client *http.Client, baseURL, apiKey string, item imageItem) ([]byte, string, error) {
	if item.B64JSON != "" {
		encoded := item.B64JSON
		if comma := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:image/") && comma >= 0 {
			encoded = encoded[comma+1:]
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, "", fmt.Errorf("解码图片失败: %w", err)
		}
		mime := sniffImageMime(data)
		if mime == "application/octet-stream" {
			// 有些网关在额度或内容策略问题上会把文本塞进 b64_json 字段，
			// 解码成功但不是图片。在这里拦住，比让 writeCover 报"格式不可识别"更早、更准。
			return nil, "", fmt.Errorf("生图服务返回的不是图片数据（前 120 字节：%s）", truncateForError(data[:min(len(data), 120)]))
		}
		return data, mime, nil
	}
	if item.URL != "" {
		return downloadImage(ctx, client, item.URL, baseURL, apiKey)
	}
	return nil, "", fmt.Errorf("生图服务返回的条目既无 b64_json 也无 url")
}

func pollImageTask(
	ctx context.Context,
	client *http.Client,
	baseURL, apiKey string,
	task imageTask,
	interval time.Duration,
) ([]byte, string, error) {
	lastStatus := ""
	for {
		status := strings.ToLower(strings.TrimSpace(task.Status))
		if status != lastStatus {
			args := []any{"module", "desktop", "task_id", task.ID, "status", status}
			if task.Progress != nil {
				args = append(args, "progress", *task.Progress)
			}
			slog.Info("异步生图任务状态", args...)
			lastStatus = status
		}
		switch status {
		case "succeeded", "completed":
			if len(task.Data) == 0 {
				return nil, "", fmt.Errorf("图片任务成功但未返回图片数据")
			}
			slog.Info("异步生图任务完成，开始下载图片", "module", "desktop", "task_id", task.ID)
			return imageItemBytes(ctx, client, baseURL, apiKey, task.Data[0])
		case "failed", "canceled", "cancelled":
			message := "图片任务" + status
			if task.Error != nil && strings.TrimSpace(task.Error.Message) != "" {
				message = task.Error.Message
			}
			return nil, "", fmt.Errorf("生图服务报错：%s", message)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, "", fmt.Errorf("生图已取消（任务 %s）", task.ID)
			}
			return nil, "", fmt.Errorf("生图超时：图片任务 %s 在 %s 内未完成", task.ID, imageGenTotalBudget)
		case <-timer.C:
		}

		next, err := fetchImageTask(ctx, client, baseURL, apiKey, task.ID)
		if err != nil {
			return nil, "", err
		}
		task = next
	}
}

func fetchImageTask(ctx context.Context, client *http.Client, baseURL, apiKey, taskID string) (imageTask, error) {
	endpoint := imageAPIV1Base(baseURL) + "/images/tasks/" + url.PathEscape(taskID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return imageTask{}, err
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return imageTask{}, fmt.Errorf("查询图片任务失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return imageTask{}, fmt.Errorf("读取图片任务状态失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageTask{}, fmt.Errorf("查询图片任务返回 %d：%s", resp.StatusCode, truncateForError(raw))
	}
	var task imageTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return imageTask{}, fmt.Errorf("解析图片任务状态失败: %w", err)
	}
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Status) == "" {
		return imageTask{}, fmt.Errorf("图片任务状态响应缺少 id/status")
	}
	return task, nil
}

func downloadImage(ctx context.Context, client *http.Client, rawURL, baseURL, apiKey string) ([]byte, string, error) {
	resolved, sameOrigin, err := resolveImageURL(rawURL, baseURL)
	if err != nil {
		return nil, "", fmt.Errorf("图片地址无效: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved, nil)
	if err != nil {
		return nil, "", err
	}
	if sameOrigin {
		if key := strings.TrimSpace(apiKey); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	started := time.Now()
	slog.Info("开始下载生成图片", "module", "desktop", "host", req.URL.Host)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载生成的图片失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("下载图片返回 %d", resp.StatusCode)
	}
	if resp.ContentLength > maxDownloadedImageBytes {
		return nil, "", fmt.Errorf("下载图片过大：%d 字节（上限 %d 字节）",
			resp.ContentLength, maxDownloadedImageBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadedImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("读取图片数据失败: %w", err)
	}
	if int64(len(data)) > maxDownloadedImageBytes {
		return nil, "", fmt.Errorf("下载图片超过 %d 字节上限", maxDownloadedImageBytes)
	}
	slog.Info("生成图片下载完成", "module", "desktop", "host", req.URL.Host,
		"bytes", len(data), "elapsed", time.Since(started).Round(time.Millisecond))
	mime := sniffImageMime(data)
	if mime == "application/octet-stream" {
		return nil, "", fmt.Errorf("下载结果不是图片（前 120 字节：%s）", truncateForError(data[:min(len(data), 120)]))
	}
	return data, mime, nil
}

func resolveImageURL(rawURL, baseURL string) (string, bool, error) {
	baseURL = normalizeImageGenBaseURL(baseURL)
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return "", false, err
	}
	ref, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false, err
	}
	resolved := base.ResolveReference(ref)
	// JarlessAPI 返回相对媒体地址。api/image 子域名虽然能返回文件，但现场实测
	// 只有几百 B/s；契约规定的根域名能在数秒内完成同一文件下载。绝对地址也做
	// 同样归一化，避免服务端将慢速别名写进任务结果。
	if isJarlessAPI(baseURL) && isJarlessAPI(resolved.String()) {
		resolved.Scheme = "https"
		resolved.Host = "jarlessapi.com"
		resolved.User = nil
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false, fmt.Errorf("不支持的协议 %q", resolved.Scheme)
	}
	return resolved.String(), strings.EqualFold(resolved.Scheme, base.Scheme) && strings.EqualFold(resolved.Host, base.Host), nil
}

func isJarlessAPI(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "jarlessapi.com" || strings.HasSuffix(host, ".jarlessapi.com")
}

// normalizeImageGenBaseURL 将 JarlessAPI 的历史/服务子域名收敛到公开契约规定的
// 唯一入口。保留其他 OpenAI 兼容服务的原始 URL，仅清理首尾空白和末尾斜杠。
func normalizeImageGenBaseURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if isJarlessAPI(base) {
		return jarlessAPIBaseURL
	}
	return base
}

func imageAPIV1Base(baseURL string) string {
	base := normalizeImageGenBaseURL(baseURL)
	u, err := url.Parse(base)
	if err == nil {
		path := strings.TrimRight(u.Path, "/")
		if strings.HasSuffix(path, "/v1/images/generations") {
			u.Path = strings.TrimSuffix(path, "/images/generations")
			u.RawPath = ""
			return strings.TrimRight(u.String(), "/")
		}
		if strings.HasSuffix(path, "/v1") {
			return base
		}
	}
	return base + "/v1"
}

func imageGenerationEndpoint(baseURL string) string {
	base := normalizeImageGenBaseURL(baseURL)
	if strings.HasSuffix(base, "/images/generations") {
		return base
	}
	return imageAPIV1Base(base) + "/images/generations"
}

// sniffImageMime 按魔数判断图片类型。EPUB manifest 需要准确的 media-type，
// 靠扩展名或服务端 Content-Type 都不可靠。
func sniffImageMime(data []byte) string {
	switch {
	case len(data) > 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func imageExtForMime(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return "bin"
	}
}

func truncateForError(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
