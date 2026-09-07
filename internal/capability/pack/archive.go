package pack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const ArchiveFormat = "ainovel.novelpack.v1"

type archive struct {
	Format   string              `json:"format"`
	Manifest domain.PackManifest `json:"manifest"`
}

func ExportArchive(path string, manifest domain.PackManifest) error {
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
	return decodeArchive(file, absolute)
}

func LoadURL(ctx context.Context, source string) (Loaded, error) {
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Loaded{}, fmt.Errorf("novelpack URL must use http or https: %w", domain.ErrInvalid)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return Loaded{}, fmt.Errorf("create novelpack request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Loaded{}, fmt.Errorf("download novelpack: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Loaded{}, fmt.Errorf("download novelpack returned %s", response.Status)
	}
	return decodeArchive(response.Body, source)
}

func decodeArchive(reader io.Reader, source string) (Loaded, error) {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return Loaded{}, fmt.Errorf("read novelpack archive: %w", err)
	}
	var value archive
	if err := domain.DecodeStrict(payload, &value); err != nil {
		return Loaded{}, fmt.Errorf("decode novelpack archive: %w", err)
	}
	if value.Format != ArchiveFormat {
		return Loaded{}, fmt.Errorf("unsupported novelpack format %q: %w", value.Format, domain.ErrInvalid)
	}
	if err := value.Manifest.Validate(); err != nil {
		return Loaded{}, err
	}
	if _, err := ParseEvals(value.Manifest); err != nil {
		return Loaded{}, err
	}
	loaded := Loaded{
		Root: source, Manifest: value.Manifest,
		References: value.Manifest.ReferenceData,
		Templates:  value.Manifest.TemplateData,
		Evals:      value.Manifest.EvalData,
	}
	return finalizeLoaded(loaded)
}
