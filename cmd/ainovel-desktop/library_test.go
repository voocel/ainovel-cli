package main

import (
	"os"
	"path/filepath"
	"testing"
)

// sameDir 是 OpenBook"同一本书跳过重建"快路径的判据，判错的后果很重：
// 判成不同 → 重建 Host → abortAll 干掉在途生图（实测丢过一张已计费的封面）。
// Host 的 store 原样保存传入的 dir 而不绝对化，所以相对路径必须能和
// 书库里的绝对路径对上。
func TestSameDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(wd, "output", "novel")

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"完全相同", abs, abs, true},
		{"相对 vs 绝对", "output/novel", abs, true},
		{"点开头的相对路径", "./output/novel", abs, true},
		{"尾部斜杠", abs + string(filepath.Separator), abs, true},
		{"多余的 . 段", filepath.Join(wd, "output", ".", "novel"), abs, true},
		{"回退再进入", filepath.Join(wd, "output", "x", "..", "novel"), abs, true},
		{"确实不同的书", filepath.Join(wd, "output", "other"), abs, false},
		{"空串不匹配任何路径", "", abs, false},
		{"两个空串也不匹配", "", "", false},
		{"前缀相同但不同目录", abs + "-2", abs, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameDir(tc.a, tc.b); got != tc.want {
				t.Errorf("sameDir(%q, %q) = %v，想要 %v", tc.a, tc.b, got, tc.want)
			}
			// 必须对称，否则 remember/forget 的去重会依赖参数顺序。
			if got := sameDir(tc.b, tc.a); got != tc.want {
				t.Errorf("反向 sameDir(%q, %q) = %v，想要 %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// Windows 路径大小写不敏感，书库里存的大小写可能与用户这次传入的不一致。
func TestSameDir_CaseInsensitive(t *testing.T) {
	wd, _ := os.Getwd()
	a := filepath.Join(wd, "Output", "Novel")
	b := filepath.Join(wd, "output", "novel")
	if !sameDir(a, b) {
		t.Errorf("大小写不同的同一路径应判为相同: %q vs %q", a, b)
	}
}

// 正反斜杠混用在 Windows 上很常见（前端传的路径、手输的路径）。
func TestSameDir_MixedSeparators(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("仅 Windows 相关")
	}
	wd, _ := os.Getwd()
	a := wd + "\\output\\novel"
	b := wd + "/output/novel"
	if !sameDir(a, b) {
		t.Errorf("正反斜杠混用应判为相同: %q vs %q", a, b)
	}
}
