package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
	"github.com/voocel/ainovel-cli/internal/domain"
)

const ManifestName = "pack.jsonc"

type FileManifest struct {
	ID         string            `json:"id"`
	Version    string            `json:"version"`
	Name       string            `json:"name"`
	Prompts    map[string]string `json:"prompts,omitempty"`
	Rules      []string          `json:"rules,omitempty"`
	References []string          `json:"references,omitempty"`
	Templates  []string          `json:"templates,omitempty"`
	Evals      []string          `json:"evals,omitempty"`
}

type Loaded struct {
	Root       string              `json:"root"`
	Manifest   domain.PackManifest `json:"manifest"`
	References map[string]string   `json:"references,omitempty"`
	Templates  map[string]string   `json:"templates,omitempty"`
	Evals      map[string]string   `json:"evals,omitempty"`
	Digest     string              `json:"digest"`
}

func LoadDirectory(path string) (Loaded, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("resolve pack directory: %w", err)
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return Loaded{}, fmt.Errorf("inspect pack directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Loaded{}, fmt.Errorf("pack path is not a directory: %w", domain.ErrInvalid)
	}

	manifestBytes, err := readAsset(root, ManifestName)
	if err != nil {
		return Loaded{}, err
	}
	standard, err := hujson.Standardize(manifestBytes)
	if err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", ManifestName, err)
	}
	var manifest FileManifest
	if err := domain.DecodeStrict(standard, &manifest); err != nil {
		return Loaded{}, fmt.Errorf("decode %s: %w", ManifestName, err)
	}
	if strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.Version) == "" || strings.TrimSpace(manifest.Name) == "" {
		return Loaded{}, fmt.Errorf("pack id, version and name are required: %w", domain.ErrInvalid)
	}
	if err := validateFileList(manifest.Rules, manifest.References, manifest.Templates, manifest.Evals); err != nil {
		return Loaded{}, err
	}

	loaded := Loaded{
		Root: root,
		Manifest: domain.PackManifest{
			ID: manifest.ID, Version: manifest.Version, Name: manifest.Name,
			PromptOverlays: make(map[string]string, len(manifest.Prompts)),
			References:     append([]string(nil), manifest.References...),
			ReferenceData:  make(map[string]string, len(manifest.References)),
			Templates:      append([]string(nil), manifest.Templates...),
			TemplateData:   make(map[string]string, len(manifest.Templates)),
			Evals:          append([]string(nil), manifest.Evals...),
			EvalData:       make(map[string]string, len(manifest.Evals)),
		},
		References: make(map[string]string, len(manifest.References)),
		Templates:  make(map[string]string, len(manifest.Templates)),
		Evals:      make(map[string]string, len(manifest.Evals)),
	}
	for slot, assetPath := range manifest.Prompts {
		if strings.TrimSpace(slot) == "" {
			return Loaded{}, fmt.Errorf("prompt slot is required: %w", domain.ErrInvalid)
		}
		content, err := readAsset(root, assetPath)
		if err != nil {
			return Loaded{}, fmt.Errorf("load prompt slot %q: %w", slot, err)
		}
		loaded.Manifest.PromptOverlays[slot] = string(content)
	}
	for _, assetPath := range manifest.Rules {
		content, err := readAsset(root, assetPath)
		if err != nil {
			return Loaded{}, fmt.Errorf("load rule %q: %w", assetPath, err)
		}
		loaded.Manifest.Rules = append(loaded.Manifest.Rules, string(content))
	}
	for _, entry := range []struct {
		paths  []string
		assets map[string]string
		kind   string
	}{
		{manifest.References, loaded.References, "reference"},
		{manifest.Templates, loaded.Templates, "template"},
		{manifest.Evals, loaded.Evals, "eval"},
	} {
		for _, assetPath := range entry.paths {
			content, err := readAsset(root, assetPath)
			if err != nil {
				return Loaded{}, fmt.Errorf("load %s %q: %w", entry.kind, assetPath, err)
			}
			entry.assets[filepath.ToSlash(assetPath)] = string(content)
		}
	}
	loaded.Manifest.ReferenceData = loaded.References
	loaded.Manifest.TemplateData = loaded.Templates
	loaded.Manifest.EvalData = loaded.Evals
	if err := loaded.Manifest.Validate(); err != nil {
		return Loaded{}, err
	}
	if _, err := ParseEvals(loaded.Manifest); err != nil {
		return Loaded{}, err
	}
	slices.Sort(loaded.Manifest.Rules)
	slices.Sort(loaded.Manifest.References)
	slices.Sort(loaded.Manifest.Templates)
	slices.Sort(loaded.Manifest.Evals)
	return finalizeLoaded(loaded)
}

func finalizeLoaded(loaded Loaded) (Loaded, error) {
	identity := loaded
	identity.Root = ""
	identity.Digest = ""
	digest, err := domain.DigestJSON(identity)
	if err != nil {
		return Loaded{}, fmt.Errorf("encode loaded pack: %w", err)
	}
	loaded.Digest = digest
	return loaded, nil
}

func readAsset(root, relative string) ([]byte, error) {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return nil, fmt.Errorf("pack asset path %q is invalid: %w", relative, domain.ErrInvalid)
	}
	cleaned := filepath.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("pack asset path %q escapes the pack: %w", relative, domain.ErrInvalid)
	}
	target := filepath.Join(root, cleaned)
	current := root
	for _, part := range strings.Split(cleaned, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect pack asset %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("pack asset %q uses a symbolic link: %w", relative, domain.ErrInvalid)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("inspect pack asset %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pack asset %q is not a regular file: %w", relative, domain.ErrInvalid)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("read pack asset %q: %w", relative, err)
	}
	return content, nil
}

func validateFileList(lists ...[]string) error {
	seen := make(map[string]struct{})
	for _, list := range lists {
		for _, name := range list {
			key := filepath.Clean(name)
			if _, ok := seen[key]; ok {
				return fmt.Errorf("pack asset %q is listed more than once: %w", name, domain.ErrInvalid)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}
