package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

type scannedSource struct {
	domain.SimulationSource
	absPath string
	content string
}

func scanSources(root string) ([]scannedSource, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("source dir is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("simulate directory not found: %s", root)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("simulate path is not a directory: %s", root)
	}

	var out []scannedSource
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !isSupportedSource(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		out = append(out, scannedSource{
			SimulationSource: domain.SimulationSource{
				RelativePath: rel,
				SHA256:       sha,
				Fingerprint:  domain.SimulationSourceFingerprint(rel, sha),
				SizeBytes:    info.Size(),
				ModTime:      info.ModTime().Format(time.RFC3339),
			},
			absPath: path,
			content: string(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelativePath < out[j].RelativePath
	})
	return out, nil
}

func isSupportedSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".markdown":
		return true
	default:
		return false
	}
}
