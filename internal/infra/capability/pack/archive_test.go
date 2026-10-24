package pack

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const testArchive = `{"format":"ainovel.novelpack.v1","manifest":{"id":"style","version":"1","name":"Style"}}`

func TestLoadURLVerifiesArchiveSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte(testArchive))
	checksum := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name, body, checksum string
		wantError            bool
	}{
		{name: "without checksum", body: testArchive},
		{name: "matching checksum", body: testArchive, checksum: checksum},
		{name: "uppercase checksum", body: testArchive, checksum: strings.ToUpper(checksum)},
		{name: "changed archive", body: testArchive + "\n", checksum: checksum, wantError: true},
		{name: "checksum before decoding", body: "invalid JSON", checksum: checksum, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			loaded, err := LoadURL(context.Background(), server.URL, tc.checksum)
			if tc.wantError {
				if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "SHA256 mismatch") || loaded.Digest != "" {
					t.Fatalf("checksum mismatch must fail before decoding: loaded=%#v err=%v", loaded, err)
				}
				return
			}
			if err != nil || loaded.Manifest.ID != "style" || loaded.Digest == "" {
				t.Fatalf("loaded=%#v err=%v", loaded, err)
			}
		})
	}
}

func TestLoadURLRejectsMalformedSHA256BeforeDownloading(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, testArchive)
	}))
	defer server.Close()
	for _, checksum := range []string{"abc", strings.Repeat("g", 64), strings.Repeat("a", 66)} {
		if _, err := LoadURL(context.Background(), server.URL, checksum); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "SHA256") {
			t.Errorf("invalid checksum %q: %v", checksum, err)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("malformed checksums initiated %d downloads", got)
	}
}

func TestLoadURLRejectsOversizedResponses(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.Copy(writer, oversizedArchive()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"content length", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "16777217")
			w.WriteHeader(http.StatusOK)
		}},
		{"chunked body", func(w http.ResponseWriter, _ *http.Request) {
			w.(http.Flusher).Flush()
			_, _ = io.Copy(w, oversizedArchive())
		}},
		{"decompressed body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(compressed.Bytes())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.serve)
			defer server.Close()
			loaded, err := LoadURL(context.Background(), server.URL, "")
			if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "exceeds") || loaded.Digest != "" {
				t.Fatalf("oversized archive accepted: loaded=%#v err=%v", loaded, err)
			}
		})
	}
}

func TestDownloadAcceptsExactLimitAndBoundsEndlessInput(t *testing.T) {
	padding := &spaceReader{}
	reader := io.MultiReader(strings.NewReader(testArchive), io.LimitReader(padding, maxDownloadBytes-int64(len(testArchive))))
	payload, err := readDownload(reader)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := decodeArchive(payload, "exact limit"); err != nil || loaded.Manifest.ID != "style" {
		t.Fatalf("archive at the limit: loaded=%#v err=%v", loaded, err)
	}
	padding = &spaceReader{}
	reader = io.MultiReader(strings.NewReader(testArchive), padding)
	if _, err := readDownload(reader); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("endless input: %v", err)
	}
	if got, want := padding.read, int64(maxDownloadBytes+1-len(testArchive)); got != want {
		t.Fatalf("reader consumed %d padding bytes, want %d", got, want)
	}
}

func TestLoadArchiveDoesNotApplyDownloadLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.novelpack")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := io.Copy(file, oversizedArchive()); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if loaded, err := LoadArchive(path); err != nil || loaded.Manifest.ID != "style" {
		t.Fatalf("local files must not inherit the download limit: loaded=%#v err=%v", loaded, err)
	}
}

func TestLoadURLTimeoutCoversHeadersAndBody(t *testing.T) {
	for _, body := range []bool{false, true} {
		name := "headers"
		if body {
			name = "body"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body {
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			if _, err := loadURL(context.Background(), server.URL, "", 100*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("download must time out while waiting for %s: %v", name, err)
			}
		})
	}
}

func TestLoadURLHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	if _, err := LoadURL(ctx, server.URL, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("download must honor caller cancellation: %v", err)
	}
}

func oversizedArchive() io.Reader {
	return io.MultiReader(strings.NewReader(testArchive), io.LimitReader(&spaceReader{}, int64(maxDownloadBytes+1-len(testArchive))))
}

type spaceReader struct{ read int64 }

func (r *spaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.read += int64(len(p))
	return len(p), nil
}
