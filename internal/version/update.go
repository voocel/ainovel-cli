package version

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
	Size               int64  `json:"size"`
}

func Update(ctx context.Context, opts UpdateOptions) (*UpdateResult, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("Windows 不支持原地自更新，请到 https://github.com/%s/releases 下载新版", opts.Repo)
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
		return nil, fmt.Errorf("release 缺少 tag_name")
	}
	if sameVersion(opts.CurrentVersion, rel.TagName) {
		return &UpdateResult{Version: rel.TagName, Path: executablePath()}, nil
	}
	asset, err := selectAsset(rel, opts.BinaryName)
	if err != nil {
		return nil, err
	}
	checksumAsset, err := selectChecksumAsset(rel, opts.BinaryName)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "ainovel-cli-update-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	archivePath := filepath.Join(tmp, "pkg.tar.gz")
	if err := download(ctx, client, asset.BrowserDownloadURL, archivePath, asset.Size); err != nil {
		return nil, err
	}
	checksumPath := filepath.Join(tmp, "checksums.txt")
	if err := download(ctx, client, checksumAsset.BrowserDownloadURL, checksumPath, checksumAsset.Size); err != nil {
		return nil, err
	}
	if err := verifyChecksum(archivePath, checksumPath, asset.Name); err != nil {
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
	versions := []string{strings.TrimPrefix(rel.TagName, "v"), rel.TagName}
	for _, version := range versions {
		expected := binaryName + "_" + version + suffix
		for _, asset := range rel.Assets {
			if asset.Name == expected && asset.BrowserDownloadURL != "" {
				return asset, nil
			}
		}
	}
	return releaseAsset{}, fmt.Errorf("release %s 未找到当前平台的精确安装包 %s_<version>%s", rel.TagName, binaryName, suffix)
}

func selectChecksumAsset(rel *release, binaryName string) (releaseAsset, error) {
	expected := binaryName + "_checksums.txt"
	for _, asset := range rel.Assets {
		if asset.Name == expected && asset.BrowserDownloadURL != "" {
			return asset, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("release %s 缺少校验文件 %s，拒绝自更新", rel.TagName, expected)
}

func assetSuffix() (string, error) {
	var osName string
	switch runtime.GOOS {
	case "darwin":
		osName = "Darwin"
	case "linux":
		osName = "Linux"
	default:
		return "", fmt.Errorf("不支持的系统 %s", runtime.GOOS)
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("不支持的架构 %s", runtime.GOARCH)
	}
	return "_" + osName + "_" + arch + ".tar.gz", nil
}

func download(ctx context.Context, client *http.Client, url, dst string, expectedSize int64) error {
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
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if expectedSize > 0 && written != expectedSize {
		return fmt.Errorf("download size mismatch: got %d bytes, expected %d", written, expectedSize)
	}
	return nil
}

// verifyChecksum 校验 GoReleaser 生成的 SHA256 清单。清单与安装包必须来自同一个
// release；缺项、格式错误或摘要不匹配均拒绝替换当前可执行文件。
func verifyChecksum(archivePath, checksumPath, assetName string) error {
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("read checksum file: %w", err)
	}
	var expected string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == assetName {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum 清单中未找到 %s", assetName)
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("%s 的 SHA256 格式非法", assetName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive for checksum: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	actual := fmt.Sprintf("%x", h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("%s SHA256 校验失败：got %s, expected %s", assetName, actual, expected)
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
	return "", fmt.Errorf("安装包中未找到 %s", binaryName)
}

func replaceCurrentExecutable(src string) (string, error) {
	return replaceExecutable(executablePath(), src)
}

func replaceExecutable(dst, src string) (string, error) {
	if dst == "" {
		return "", fmt.Errorf("无法定位当前可执行文件")
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
