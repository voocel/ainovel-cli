package pack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const ArchiveFormat = "ainovel.novelpack.v1"

const (
	maxDownloadBytes = 16 << 20
	downloadTimeout  = time.Minute
)

type archive struct {
	Format   string             `json:"format"`
	Manifest model.PackManifest `json:"manifest"`
}

func ExportArchive(path string, manifest model.PackManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if _, err := ParseEvals(manifest); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(archive{Format: ArchiveFormat, Manifest: manifest}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode novelpack archive: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write novelpack archive: %w", err)
	}
	return nil
}

func LoadArchive(path string) (Loaded, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("resolve novelpack archive: %w", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return Loaded{}, fmt.Errorf("open novelpack archive: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(file)
	if err != nil {
		return Loaded{}, fmt.Errorf("read novelpack archive: %w", err)
	}
	return decodeArchive(payload, absolute)
}

// LoadURL limits the complete download, including redirects and reading the body.
// expectedSHA256, when supplied, identifies the archive bytes, not Loaded.Digest.
func LoadURL(ctx context.Context, source, expectedSHA256 string) (Loaded, error) {
	return loadURL(ctx, source, expectedSHA256, downloadTimeout)
}

func loadURL(ctx context.Context, source, expectedSHA256 string, timeout time.Duration) (Loaded, error) {
	var expected []byte
	if expectedSHA256 != "" {
		var err error
		expected, err = hex.DecodeString(expectedSHA256)
		if err != nil || len(expected) != sha256.Size {
			return Loaded{}, fmt.Errorf("novelpack SHA256 must contain 64 hexadecimal characters: %w", model.ErrInvalid)
		}
	}
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Loaded{}, fmt.Errorf("novelpack URL must use http or https: %w", model.ErrInvalid)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return Loaded{}, fmt.Errorf("create novelpack request: %w", err)
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return Loaded{}, fmt.Errorf("download novelpack: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Loaded{}, fmt.Errorf("download novelpack returned %s", response.Status)
	}
	if response.ContentLength > maxDownloadBytes {
		return Loaded{}, fmt.Errorf("novelpack download exceeds %d bytes: %w", maxDownloadBytes, model.ErrInvalid)
	}
	payload, err := readDownload(response.Body)
	if err != nil {
		return Loaded{}, err
	}
	if len(expected) > 0 {
		actual := sha256.Sum256(payload)
		if !bytes.Equal(actual[:], expected) {
			return Loaded{}, fmt.Errorf("novelpack SHA256 mismatch (got %x): %w", actual, model.ErrInvalid)
		}
	}
	return decodeArchive(payload, source)
}

func readDownload(reader io.Reader) ([]byte, error) {
	// Read one byte beyond the limit so an oversized response cannot be accepted
	// as a valid JSON prefix, even without Content-Length or after decompression.
	payload, err := io.ReadAll(io.LimitReader(reader, maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read novelpack archive: %w", err)
	}
	if len(payload) > maxDownloadBytes {
		return nil, fmt.Errorf("novelpack download exceeds %d bytes: %w", maxDownloadBytes, model.ErrInvalid)
	}
	return payload, nil
}

func decodeArchive(payload []byte, source string) (Loaded, error) {
	var value archive
	if err := model.DecodeStrict(payload, &value); err != nil {
		return Loaded{}, fmt.Errorf("decode novelpack archive: %w", err)
	}
	if value.Format != ArchiveFormat {
		return Loaded{}, fmt.Errorf("unsupported novelpack format %q: %w", value.Format, model.ErrInvalid)
	}
	if err := value.Manifest.Validate(); err != nil {
		return Loaded{}, err
	}
	if _, err := ParseEvals(value.Manifest); err != nil {
		return Loaded{}, err
	}
	return finalizeLoaded(Loaded{Root: source, Manifest: value.Manifest})
}
