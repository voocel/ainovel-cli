package version

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReleaseURL(t *testing.T) {
	cases := map[string]string{
		"":       "https://api.github.com/repos/voocel/ainovel-cli/releases/latest",
		"latest": "https://api.github.com/repos/voocel/ainovel-cli/releases/latest",
		"1.2.3":  "https://api.github.com/repos/voocel/ainovel-cli/releases/tags/v1.2.3",
		"v1.2.3": "https://api.github.com/repos/voocel/ainovel-cli/releases/tags/v1.2.3",
	}
	for target, want := range cases {
		if got := releaseURL("voocel/ainovel-cli", target); got != want {
			t.Fatalf("releaseURL(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestSelectAsset(t *testing.T) {
	suffix, err := assetSuffix()
	if err != nil {
		t.Skip(err)
	}
	rel := &release{
		TagName: "v1.2.3",
		Assets: []releaseAsset{
			{Name: "ainovel-cli_v1.2.3_Windows_x86_64.zip", BrowserDownloadURL: "wrong"},
			{Name: "ainovel-cli_v1.2.3" + suffix, BrowserDownloadURL: "right"},
		},
	}
	asset, err := selectAsset(rel, "ainovel-cli")
	if err != nil {
		t.Fatalf("selectAsset: %v", err)
	}
	if asset.BrowserDownloadURL != "right" {
		t.Fatalf("asset = %+v", asset)
	}
}

func TestSelectChecksumAsset(t *testing.T) {
	rel := &release{TagName: "v1.2.3", Assets: []releaseAsset{
		{Name: "ainovel-cli_checksums.txt", BrowserDownloadURL: "checksum"},
	}}
	asset, err := selectChecksumAsset(rel, "ainovel-cli")
	if err != nil {
		t.Fatalf("selectChecksumAsset: %v", err)
	}
	if asset.BrowserDownloadURL != "checksum" {
		t.Fatalf("asset = %+v", asset)
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ainovel-cli_1.2.3_Linux_x86_64.tar.gz")
	content := []byte("release archive")
	if err := os.WriteFile(archive, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	checksums := filepath.Join(dir, "checksums.txt")
	line := fmt.Sprintf("%x  %s\n", sum, filepath.Base(archive))
	if err := os.WriteFile(checksums, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(archive, checksums, filepath.Base(archive)); err != nil {
		t.Fatalf("verifyChecksum: %v", err)
	}
	if err := os.WriteFile(archive, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(archive, checksums, filepath.Base(archive)); err == nil {
		t.Fatal("tampered archive should fail checksum verification")
	}
}

func TestDownloadRejectsAssetSizeMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("abc"))
	}))
	defer server.Close()
	dst := filepath.Join(t.TempDir(), "asset.tar.gz")
	if err := download(context.Background(), server.Client(), server.URL, dst, 4); err == nil {
		t.Fatal("download with mismatched GitHub asset size should fail")
	}
}

func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "ainovel-cli")
	src := filepath.Join(dir, "new")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := replaceExecutable(dst, src)
	if err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	realDst, err := filepath.EvalSymlinks(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got != realDst {
		t.Fatalf("path = %q, want %q", got, realDst)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	// 权限保持断言只在有 POSIX 权限位语义的平台上有意义：Windows 把一切上报为
	// 0666/0444、执行位永不出现（可执行性来自 .exe 扩展名），此断言在该平台恒假。
	// 替换/回滚/备份清理断言与平台相关（Windows rename 语义不同），必须继续运行。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(dst + ".old"); !os.IsNotExist(err) {
		t.Fatalf("backup should be removed, err=%v", err)
	}
}
