package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// imageGenServer 起一个假的 /v1/images/generations，可控制"算图"耗时与分块传输节奏。
// 用真 httptest 服务器而不是 mock transport：要覆盖的正是响应头已回、
// 响应体慢慢流的形态——这正是真实网关的行为，也是历次 bug 的发生地。
func imageGenServer(t *testing.T, thinkFor time.Duration, chunkDelay time.Duration) *httptest.Server {
	t.Helper()
	png := bigPNG(t, 32, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/generations") {
			http.Error(w, "wrong endpoint: "+r.URL.Path, http.StatusNotFound)
			return
		}
		select {
		case <-time.After(thinkFor): // 模拟服务端算图
		case <-r.Context().Done():
			return
		}
		body, _ := json.Marshal(imageResponse{
			Data: []imageItem{{B64JSON: base64.StdEncoding.EncodeToString(png)}},
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// 分块慢慢吐，模拟低带宽下几 MB base64 的传输过程。
		const chunks = 8
		size := (len(body) + chunks - 1) / chunks
		for start := 0; start < len(body); start += size {
			end := min(start+size, len(body))
			if _, err := w.Write(body[start:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-time.After(chunkDelay):
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGenerateImage_SlowServerSucceeds 慢响应（头回得早、体流得慢）必须能拿到完整图片。
// 曾经用 http.Client.Timeout 兜整个请求周期，这种形态会在读体过程中被砍断。
func TestGenerateImage_SlowServerSucceeds(t *testing.T) {
	srv := imageGenServer(t, 150*time.Millisecond, 20*time.Millisecond)
	cfg := imageGenConfig{BaseURL: srv.URL + "/v1", Model: "test-model", Size: "1024x1024"}

	data, mime, err := generateImage(context.Background(), cfg, "a cover")
	if err != nil {
		t.Fatalf("慢服务端下生图应成功，实际报错: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("mime = %q，想要 image/png", mime)
	}
	if len(data) == 0 {
		t.Error("图片字节为空")
	}
	if got := sniffImageMime(data); got != "image/png" {
		t.Errorf("返回的字节不是 PNG，嗅探为 %q", got)
	}
}

// TestGenerateImage_CancelIsReportedAsCancel 用户取消必须报"已取消"，
// 不能混成服务端故障——否则用户会以为是服务商坏了而反复重试。
func TestGenerateImage_CancelIsReportedAsCancel(t *testing.T) {
	srv := imageGenServer(t, 2*time.Second, 0)
	cfg := imageGenConfig{BaseURL: srv.URL + "/v1", Model: "test-model"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, _, err := generateImage(ctx, cfg, "a cover")
	if err == nil {
		t.Fatal("取消后应返回错误")
	}
	if !strings.Contains(err.Error(), "已取消") {
		t.Errorf("错误信息应说明是取消，实际: %v", err)
	}
	// 不能把取消说成服务端故障。
	if strings.Contains(err.Error(), "请求生图服务失败") {
		t.Errorf("取消被误报为服务端故障: %v", err)
	}
}

// TestGenerateImage_RejectsNonImageB64 有些网关把额度/策略提示塞进 b64_json，
// 解码成功但不是图片。必须在这里拦住，而不是留到 writeCover 报"格式不可识别"。
func TestGenerateImage_RejectsNonImageB64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload := base64.StdEncoding.EncodeToString([]byte("insufficient quota, please recharge"))
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, payload)
	}))
	defer srv.Close()

	cfg := imageGenConfig{BaseURL: srv.URL + "/v1", Model: "test-model"}
	_, _, err := generateImage(context.Background(), cfg, "a cover")
	if err == nil {
		t.Fatal("非图片数据应被拒绝")
	}
	if !strings.Contains(err.Error(), "不是图片") {
		t.Errorf("错误应指出返回的不是图片，实际: %v", err)
	}
}

// TestGenerateImage_ServerErrorSurfacesMessage 服务端错误信息不能被吞掉：
// 模型名错、余额不足、无权限全靠这段文案定位。
func TestGenerateImage_ServerErrorSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model not found: gpt-image-9"}}`)
	}))
	defer srv.Close()

	cfg := imageGenConfig{BaseURL: srv.URL + "/v1", Model: "gpt-image-9"}
	_, _, err := generateImage(context.Background(), cfg, "a cover")
	if err == nil {
		t.Fatal("4xx 应返回错误")
	}
	if !strings.Contains(err.Error(), "model not found: gpt-image-9") {
		t.Errorf("服务端错误信息被吞掉了，实际: %v", err)
	}
}

// TestGenerateImage_EndpointNotDoubled base_url 已含 /v1 时不应再拼一层。
// 拼错会 404，而 404 的报错和"模型不存在"长得很像，很难查。
func TestGenerateImage_EndpointNotDoubled(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	for _, base := range []string{srv.URL + "/v1", srv.URL + "/v1/", srv.URL} {
		gotPath = ""
		cfg := imageGenConfig{BaseURL: base, Model: "m"}
		_, _, _ = generateImage(context.Background(), cfg, "p")
		if gotPath != "/v1/images/generations" {
			t.Errorf("base=%q 打到了 %q，想要 /v1/images/generations", base, gotPath)
		}
	}
}

func TestJarlessAPIRecognitionAndTaskEndpoint(t *testing.T) {
	for _, base := range []string{
		"https://jarlessapi.com",
		"https://api.jarlessapi.com/v1",
		"https://image.jarlessapi.com/v1",
		"https://API.JARLESSAPI.COM/v1/",
	} {
		if !isJarlessAPI(base) {
			t.Errorf("应识别为 JarlessAPI: %q", base)
		}
		if got := normalizeImageGenBaseURL(base); got != jarlessAPIBaseURL {
			t.Errorf("JarlessAPI 地址应规范化为 %q，输入 %q 得到 %q", jarlessAPIBaseURL, base, got)
		}
	}
	if isJarlessAPI("https://example.com/v1") {
		t.Fatal("普通 OpenAI 兼容服务不应被强制切到异步任务模式")
	}
	if got := normalizeImageGenBaseURL(" https://example.com/v1/ "); got != "https://example.com/v1" {
		t.Fatalf("普通兼容服务不应换域名，实际 %q", got)
	}
	if got := imageAPIV1Base("https://api.jarlessapi.com/v1/images/generations"); got != "https://jarlessapi.com/v1" {
		t.Fatalf("完整生成端点应还原到 v1 base，实际 %q", got)
	}
	if got := imageGenerationEndpoint("https://image.jarlessapi.com/v1"); got != "https://jarlessapi.com/v1/images/generations" {
		t.Fatalf("JarlessAPI 生成端点必须使用根域名，实际 %q", got)
	}
}

func TestResolveImageURL_NormalizesJarlessMediaHost(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		image string
		want  string
		same  bool
	}{
		{
			name:  "相对媒体地址",
			base:  "https://image.jarlessapi.com/v1",
			image: "/v1/draw/media/result.png",
			want:  "https://jarlessapi.com/v1/draw/media/result.png",
			same:  true,
		},
		{
			name:  "绝对慢速子域名",
			base:  "https://api.jarlessapi.com/v1",
			image: "https://image.jarlessapi.com/v1/draw/media/result.png?token=x",
			want:  "https://jarlessapi.com/v1/draw/media/result.png?token=x",
			same:  true,
		},
		{
			name:  "普通兼容服务保持原样",
			base:  "https://images.example.com/v1",
			image: "/files/result.png",
			want:  "https://images.example.com/files/result.png",
			same:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, same, err := resolveImageURL(tt.image, tt.base)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || same != tt.same {
				t.Fatalf("resolveImageURL() = (%q, %v)，想要 (%q, %v)", got, same, tt.want, tt.same)
			}
		})
	}
}

func TestGenerateImage_JarlessAsyncPollsAndDownloadsWithAuth(t *testing.T) {
	png := bigPNG(t, 32, 32)
	const apiKey = "test-secret-key"
	var createSeen, pollSeen, downloadSeen bool

	oldInterval := imageTaskPollInterval
	imageTaskPollInterval = time.Millisecond
	t.Cleanup(func() { imageTaskPollInterval = oldInterval })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations":
			createSeen = true
			var req imageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("解析创建请求失败: %v", err)
			}
			if !req.Sub2APIAsync || req.Stream == nil || *req.Stream || req.ResponseFormat != "url" {
				t.Errorf("异步创建参数不完整: %+v", req)
			}
			fmt.Fprint(w, `{"object":"image.task","id":"task-123","status":"queued","progress":0}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/images/tasks/task-123":
			pollSeen = true
			fmt.Fprint(w, `{"object":"image.task","id":"task-123","status":"succeeded","progress":100,"data":[{"url":"/v1/images/files/result.png","output_format":"png"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/images/files/result.png":
			downloadSeen = true
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := imageGenConfig{
		BaseURL: srv.URL + "/v1", APIKey: apiKey, Model: "gpt-image-2", Size: "1024x1536", Async: true,
	}
	data, mime, err := generateImage(context.Background(), cfg, "a cover")
	if err != nil {
		t.Fatalf("Jarless 异步任务应成功取回图片: %v", err)
	}
	if !createSeen || !pollSeen || !downloadSeen {
		t.Fatalf("异步链路不完整: create=%v poll=%v download=%v", createSeen, pollSeen, downloadSeen)
	}
	if mime != "image/png" || len(data) == 0 {
		t.Fatalf("取回图片不正确: mime=%q bytes=%d", mime, len(data))
	}
}

// TestJobRegistry_AbortAllCancelsCover 锁住导致封面丢失的那条链路的下半段：
// abortAll 确实会取消 cover 作业。上半段（OpenBook 不该无谓触发 abortAll）
// 由 TestOpenBook_SameDirIsNoop 覆盖。
func TestJobRegistry_AbortAllCancelsCover(t *testing.T) {
	r := newJobRegistry()
	ctx, _ := r.begin("cover")
	if ctx.Err() != nil {
		t.Fatal("刚创建的作业不该已被取消")
	}
	r.abortAll()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("abortAll 后 cover ctx 应为 Canceled，实际 %v", ctx.Err())
	}
}

// TestJobRegistry_BeginCancelsPrevious 同类作业重入时，旧的必须被取消，
// 否则两次生图会并行跑、都写同一个封面文件。
func TestJobRegistry_BeginCancelsPrevious(t *testing.T) {
	r := newJobRegistry()
	first, _ := r.begin("cover")
	second, _ := r.begin("cover")
	if !errors.Is(first.Err(), context.Canceled) {
		t.Errorf("重入后前一个作业应被取消，实际 %v", first.Err())
	}
	if second.Err() != nil {
		t.Errorf("新作业不该被取消，实际 %v", second.Err())
	}
}

func TestImageGenHeaderTimeoutCoversTotalBudget(t *testing.T) {
	if imageGenHeaderTimeout < imageGenTotalBudget {
		t.Fatalf("响应头超时 %s 早于总预算 %s，会丢掉已完成的慢速同步生图",
			imageGenHeaderTimeout, imageGenTotalBudget)
	}
}
