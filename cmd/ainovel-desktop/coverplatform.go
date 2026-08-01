package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	xdraw "golang.org/x/image/draw"
)

const tomatoCoverArtifactName = "cover-fanqie.png"

var coverPlatformArtifactNames = []string{tomatoCoverArtifactName}

// coverPlatformArtifactPath 返回平台可直接上传的封面文件路径。
// 通用成品仍保存在 cover.*，平台文件只负责满足平台的精确尺寸要求。
func coverPlatformArtifactPath(bookDir, platform string) string {
	if normalizeCoverPlatform(platform) != "tomato" {
		return ""
	}
	return filepath.Join(bookDir, tomatoCoverArtifactName)
}

func writeCoverPlatformArtifact(bookDir string, data []byte, mime, platform string) error {
	target := coverPlatformArtifactPath(bookDir, platform)
	if target == "" {
		for _, name := range coverPlatformArtifactNames {
			_ = os.Remove(filepath.Join(bookDir, name))
		}
		return nil
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("解析平台封面失败: %w", err)
	}
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return fmt.Errorf("平台封面尺寸异常（%dx%d）", b.Dx(), b.Dy())
	}

	// 番茄要求 600x800。先在原图中央裁出 3:4，再缩放，避免拉伸人物。
	crop := centeredAspectRect(b, 3, 4)
	dst := image.NewRGBA(image.Rect(0, 0, 600, 800))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, xdraw.Src, nil)

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return fmt.Errorf("编码平台封面失败: %w", err)
	}
	if err := atomicWriteFile(target, out.Bytes(), 0o644); err != nil {
		return fmt.Errorf("保存番茄平台封面失败: %w", err)
	}
	return nil
}

func centeredAspectRect(bounds image.Rectangle, ratioW, ratioH int) image.Rectangle {
	w, h := bounds.Dx(), bounds.Dy()
	wantW := h * ratioW / ratioH
	wantH := w * ratioH / ratioW
	if wantW <= w {
		left := bounds.Min.X + (w-wantW)/2
		return image.Rect(left, bounds.Min.Y, left+wantW, bounds.Max.Y)
	}
	top := bounds.Min.Y + (h-wantH)/2
	return image.Rect(bounds.Min.X, top, bounds.Max.X, top+wantH)
}

// archiveCurrentCover 保存被替换前的成品。调用方会忽略归档错误，确保已计费的新图
// 始终优先落盘。
func archiveCurrentCover(bookDir string) error {
	path, mime := findCoverFile(bookDir)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ext := imageExtForMime(mime)
	if ext == "bin" {
		ext = filepath.Ext(path)
		ext = string(bytes.TrimPrefix([]byte(ext), []byte(".")))
	}
	if ext == "" {
		ext = "img"
	}
	historyDir := filepath.Join(bookDir, "meta", "cover-history")
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("cover-%s.%s", time.Now().Format("20060102-150405.000"), ext)
	return atomicWriteFile(filepath.Join(historyDir, name), data, 0o644)
}
