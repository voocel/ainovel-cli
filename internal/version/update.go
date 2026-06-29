package version

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type UpdateOptions struct {
	Repo           string
	BinaryName     string
	TargetVersion  string
	CurrentVersion string
	Client         *http.Client
}

type UpdateResult struct {
	Version string
	Path    string
	Updated bool
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func Update(ctx context.Context, opts UpdateOptions) (*UpdateResult, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("Windows Không hỗ trợ原地自更Mới，Vui lòng到 https://github.com/%s/releases 下载Mới版", opts.Repo)
	}
	if opts.Repo == "" {
		return nil, fmt.Errorf("missing repo")
	}
	if opts.BinaryName == "" {
		return nil, fmt.Errorf("missing binary name")
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	rel, err := fetchRelease(ctx, client, opts.Repo, opts.TargetVersion)
	if err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release Thiếu tag_name")
	}
	if sameVersion(opts.CurrentVersion, rel.TagName) {
		return &UpdateResult{Version: rel.TagName, Path: executablePath()}, nil
	}
	asset, err := selectAsset(rel, opts.BinaryName)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "ainovel-cli-update-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	archivePath := filepath.Join(tmp, "pkg.tar.gz")
	if err := download(ctx, client, asset.BrowserDownloadURL, archivePath); err != nil {
		return nil, err
	}
	extracted, err := extractBinary(archivePath, tmp, opts.BinaryName)
	if err != nil {
		return nil, err
	}
	dst, err := replaceCurrentExecutable(extracted)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{Version: rel.TagName, Path: dst, Updated: true}, nil
}

func fetchRelease(ctx context.Context, client *http.Client, repo, target string) (*release, error) {
	url := releaseURL(repo, target)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("query release: %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &rel, nil
}

func releaseURL(repo, target string) string {
	target = strings.TrimSpace(target)
	if target == "" || target == "latest" {
		return "https://api.github.com/repos/" + repo + "/releases/latest"
	}
	if !strings.HasPrefix(target, "v") {
		target = "v" + target
	}
	return "https://api.github.com/repos/" + repo + "/releases/tags/" + target
}

func selectAsset(rel *release, binaryName string) (releaseAsset, error) {
	suffix, err := assetSuffix()
	if err != nil {
		return releaseAsset{}, err
	}
	for _, asset := range rel.Assets {
		if strings.Contains(asset.Name, binaryName+"_") && strings.HasSuffix(asset.Name, suffix) && asset.BrowserDownloadURL != "" {
			return asset, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("release %s Không tìm thấyHiện tại平台安装包 *%s", rel.TagName, suffix)
}

func assetSuffix() (string, error) {
	var osName string
	switch runtime.GOOS {
	case "darwin":
		osName = "Darwin"
	case "linux":
		osName = "Linux"
	default:
		return "", fmt.Errorf("Không hỗ trợ的系统 %s", runtime.GOOS)
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("Không hỗ trợ的架构 %s", runtime.GOARCH)
	}
	return "_" + osName + "_" + arch + ".tar.gz", nil
}

func download(ctx context.Context, client *http.Client, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download release asset: %s", resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

func extractBinary(archivePath, dstDir, binaryName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("read archive gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binaryName {
			continue
		}
		out := filepath.Join(dstDir, binaryName)
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return "", fmt.Errorf("extract binary: %w", err)
		}
		if _, err := io.Copy(w, tr); err != nil {
			_ = w.Close()
			return "", fmt.Errorf("extract binary: %w", err)
		}
		if err := w.Close(); err != nil {
			return "", fmt.Errorf("extract binary: %w", err)
		}
		return out, nil
	}
	return "", fmt.Errorf("安装包中Không tìm thấy %s", binaryName)
}

func replaceCurrentExecutable(src string) (string, error) {
	return replaceExecutable(executablePath(), src)
}

func replaceExecutable(dst, src string) (string, error) {
	if dst == "" {
		return "", fmt.Errorf("Không thể定位Hiện tại可执行Tập tin")
	}
	if real, err := filepath.EvalSymlinks(dst); err == nil {
		dst = real
	}
	perm := os.FileMode(0o755)
	if info, err := os.Stat(dst); err == nil {
		perm = info.Mode().Perm()
	}
	stage, err := stageExecutable(filepath.Dir(dst), filepath.Base(dst), src, perm)
	if err != nil {
		return "", err
	}
	stageInstalled := false
	defer func() {
		if !stageInstalled {
			_ = os.Remove(stage)
		}
	}()

	backup := dst + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(dst, backup); err != nil {
		return "", fmt.Errorf("backup current executable: %w", err)
	}
	if err := os.Rename(stage, dst); err != nil {
		_ = os.Rename(backup, dst)
		return "", fmt.Errorf("replace executable: %w", err)
	}
	stageInstalled = true
	if err := os.Remove(backup); err != nil {
		return "", fmt.Errorf("remove backup executable: %w", err)
	}
	return dst, nil
}

func stageExecutable(dir, base, src string, perm os.FileMode) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open new executable: %w", err)
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, base+".new-*")
	if err != nil {
		return "", fmt.Errorf("create staged executable: %w", err)
	}
	stage := out.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(stage)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("write staged executable: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close staged executable: %w", err)
	}
	if err := os.Chmod(stage, perm); err != nil {
		return "", fmt.Errorf("chmod staged executable: %w", err)
	}
	ok = true
	return stage, nil
}

func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func sameVersion(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	return a != "" && b != "" && a == b
}
