package tui

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/host/scan"
)

// TestParseScanArgs 验证 /scan 的参数解析：位置参数是榜单文件，其余走 --xxx=。
func TestParseScanArgs(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		want  scan.Options
		errIs string // 非空 = 期望报错且错误含此片段
	}{
		{
			name: "只给文件路径",
			args: []string{"月票榜.txt"},
			want: scan.Options{FilePath: "月票榜.txt"},
		},
		{
			name: "全部选项",
			args: []string{"榜单.md", "--platform=qidian", "--rank=月票榜", "--lib=/tmp/lib", "--date=20260101"},
			want: scan.Options{
				FilePath: "榜单.md", Platform: "qidian", RankName: "月票榜",
				LibraryDir: "/tmp/lib", ScanDate: "20260101",
			},
		},
		{
			name: "目录模式",
			args: []string{"--dir=./ranks", "--platform=fanqie"},
			want: scan.Options{DirPath: "./ranks", Platform: "fanqie"},
		},
		{
			name:  "没有数据源",
			args:  []string{"--platform=qidian"},
			errIs: "请给出榜单文件路径",
		},
		{
			name:  "两个位置参数",
			args:  []string{"a.txt", "b.txt"},
			errIs: "只接受一个榜单文件路径",
		},
		{
			name:  "未知选项",
			args:  []string{"a.txt", "--nope=1"},
			errIs: "未知选项",
		},
		{
			name:  "日期格式错误",
			args:  []string{"a.txt", "--date=2026-01-01"},
			errIs: "YYYYMMDD",
		},
		{
			name:  "日期含非数字",
			args:  []string{"a.txt", "--date=2026010x"},
			errIs: "YYYYMMDD",
		},
		{
			name:  "platform 空值",
			args:  []string{"a.txt", "--platform="},
			errIs: "--platform 需要平台名",
		},
		{
			name:  "rank 空值",
			args:  []string{"a.txt", "--rank=  "},
			errIs: "--rank 需要榜单名",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseScanArgs(tt.args)
			if tt.errIs != "" {
				if err == nil {
					t.Fatalf("应该报错（含 %q），得到 %+v", tt.errIs, got)
				}
				if !strings.Contains(err.Error(), tt.errIs) {
					t.Errorf("错误 %q 应包含 %q", err.Error(), tt.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错：%v", err)
			}
			if got != tt.want {
				t.Errorf("parseScanArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestScanSourceLabel 验证数据源回显与 fileFetcher 的优先级一致：
// 粘贴 > 单文件 > 目录。回显跟实际取的数据源不一致会让排查走错方向。
func TestScanSourceLabel(t *testing.T) {
	tests := []struct {
		name string
		opts scan.Options
		want string
	}{
		{"粘贴优先", scan.Options{PastedText: "x", FilePath: "a.txt", DirPath: "d"}, "粘贴文本"},
		{"文件次之", scan.Options{FilePath: "a.txt", DirPath: "d"}, "a.txt"},
		{"目录兜底", scan.Options{DirPath: "d"}, "d（目录）"},
		{"都没有", scan.Options{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanSourceLabel(tt.opts); got != tt.want {
				t.Errorf("scanSourceLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestScanCommandRegistered 验证 /scan 进了注册表并要求 idle，
// 且没有 AutoExecute（它必须带参数）。
func TestScanCommandRegistered(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("scan")
	if !ok {
		t.Fatal("/scan 未注册")
	}
	if !spec.NeedsIdle {
		t.Error("/scan 应要求 NeedsIdle：它与 Engine 互斥")
	}
	if spec.AutoExecute {
		t.Error("/scan 需要参数，不应 AutoExecute")
	}
	if spec.Group != "analysis" {
		t.Errorf("/scan Group = %q, want analysis", spec.Group)
	}
}
