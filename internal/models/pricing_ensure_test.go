package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// stubTransport 替换 http.DefaultTransport（fetchModels 用零值 http.Client，
// 因此走 DefaultTransport），把网络访问变成可断言的本地行为，保证测试不触网。
type stubTransport struct {
	calls int32
	fn    func(req *http.Request) (*http.Response, error)
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.fn != nil {
		return s.fn(req)
	}
	return nil, errors.New("stub transport: unexpected network request")
}

func (s *stubTransport) callCount() int { return int(atomic.LoadInt32(&s.calls)) }

func installTransport(t *testing.T, s *stubTransport) {
	t.Helper()
	orig := http.DefaultTransport
	http.DefaultTransport = s
	t.Cleanup(func() { http.DefaultTransport = orig })
}

func writeModelCacheFile(t *testing.T, dir string, entries []ModelEntry) {
	t.Helper()
	data, err := json.Marshal(modelCache{FetchedAt: time.Now().Add(-time.Minute), Models: entries})
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}

// TestEnsureLoadedCacheHit 固化"TTL 内的磁盘缓存直接合并、不发任何请求"。
func TestEnsureLoadedCacheHit(t *testing.T) {
	transport := &stubTransport{}
	installTransport(t, transport)

	dir := t.TempDir()
	want := ModelEntry{
		Provider:      "testprov",
		ID:            "ensure-loaded-cache-probe",
		Name:          "Ensure Loaded Cache Probe",
		ContextWindow: 424242,
		MaxTokens:     8192,
	}
	writeModelCacheFile(t, dir, []ModelEntry{want})

	registry := NewModelRegistry()
	if err := EnsureLoaded(context.Background(), registry, dir); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	got, ok := registry.Resolve("testprov/ensure-loaded-cache-probe")
	if !ok || got.ContextWindow != want.ContextWindow {
		t.Fatalf("cache entry not merged: ok=%v entry=%+v", ok, got)
	}
	if n := transport.callCount(); n != 0 {
		t.Fatalf("cache hit must not hit network, got %d request(s)", n)
	}
}

// TestEnsureLoadedCancelledContext 固化"ctx 已取消时返回 error 且注册表保持原样"。
func TestEnsureLoadedCancelledContext(t *testing.T) {
	transport := &stubTransport{fn: func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	}}
	installTransport(t, transport)

	registry := NewModelRegistry()
	before := len(registry.models)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := EnsureLoaded(ctx, registry, t.TempDir())
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if after := len(registry.models); after != before {
		t.Fatalf("registry mutated on failure: before=%d after=%d", before, after)
	}
}

// TestEnsureLoadedCorruptCache 固化"缓存损坏时不 panic，降级去拉网络；
// 网络也失败时返回 error 且注册表保持基线"。
func TestEnsureLoadedCorruptCache(t *testing.T) {
	transport := &stubTransport{fn: func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("stub transport: upstream unavailable")
	}}
	installTransport(t, transport)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), []byte(`{"fetched_at": "not-a-time"`), 0o644); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}

	registry := NewModelRegistry()
	before := len(registry.models)

	err := EnsureLoaded(context.Background(), registry, dir)
	if err == nil {
		t.Fatal("expected error when corrupt cache forces network fallback and fetch fails, got nil")
	}
	if after := len(registry.models); after != before {
		t.Fatalf("registry mutated by corrupt cache path: before=%d after=%d", before, after)
	}
	if n := transport.callCount(); n != 1 {
		t.Fatalf("corrupt cache should fall back to exactly one fetch, got %d", n)
	}
}
