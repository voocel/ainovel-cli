package scan

import (
	"testing"
)

// TestCleanEntriesRemoveInvalid 验证必填字段缺失时剔除条目。
func TestCleanEntriesRemoveInvalid(t *testing.T) {
	raw := []Entry{
		{Rank: 1, Title: "书名1", Author: "作者1", Description: "简介1"},
		{Rank: 0, Title: "书名2", Author: "作者2"}, // 排名缺失
		{Rank: 3, Title: "", Author: "作者3"},      // 书名缺失
		{Rank: 4, Title: "书名4", Author: ""},      // 作者缺失
		{Rank: 5, Title: "书名5", Author: "作者5"},
	}

	valid, report := CleanEntries(raw, "qidian")

	if len(valid) != 2 {
		t.Errorf("CleanEntries() valid count = %d, want 2", len(valid))
	}
	if report.TotalRaw != 5 {
		t.Errorf("report.TotalRaw = %d, want 5", report.TotalRaw)
	}
	if report.ValidEntries != 2 {
		t.Errorf("report.ValidEntries = %d, want 2", report.ValidEntries)
	}

	// 验证有效条目
	if valid[0].Rank != 1 || valid[0].Title != "书名1" {
		t.Errorf("valid[0] = %+v, want Rank=1 Title=书名1", valid[0])
	}
	if valid[1].Rank != 5 || valid[1].Title != "书名5" {
		t.Errorf("valid[1] = %+v, want Rank=5 Title=书名5", valid[1])
	}
}

// TestCleanEntriesTruncateDescription 验证简介按句号截断到 100 字。
func TestCleanEntriesTruncateDescription(t *testing.T) {
	longDesc := ""
	for i := 0; i < 150; i++ {
		longDesc += "字"
	}
	longDesc += "。后续内容。"

	raw := []Entry{
		{Rank: 1, Title: "书1", Author: "作者1", Description: longDesc},
		{Rank: 2, Title: "书2", Author: "作者2", Description: "短简介。"},
	}

	valid, _ := CleanEntries(raw, "qidian")

	if len([]rune(valid[0].Description)) > descMaxRunes+1 { // +1 允许句号
		t.Errorf("Description length = %d runes, want <= %d", len([]rune(valid[0].Description)), descMaxRunes)
	}

	// 短简介不应被截断
	if valid[1].Description != "短简介。" {
		t.Errorf("Short description = %q, want unchanged", valid[1].Description)
	}
}

// TestCleanEntriesEmptyFieldsMarked 验证空值标记为 [待补]。
func TestCleanEntriesEmptyFieldsMarked(t *testing.T) {
	raw := []Entry{
		{Rank: 1, Title: "书名", Author: "作者", Category: "", Description: "", Stats: ""},
	}

	valid, _ := CleanEntries(raw, "qidian")

	if valid[0].Category != "[待补]" {
		t.Errorf("Category = %q, want [待补]", valid[0].Category)
	}
	if valid[0].Description != "[待补]" {
		t.Errorf("Description = %q, want [待补]", valid[0].Description)
	}
	if valid[0].Stats != "[待补]" {
		t.Errorf("Stats = %q, want [待补]", valid[0].Stats)
	}
}

// TestCleanEntriesSparseDetection 验证稀疏标记逻辑。
func TestCleanEntriesSparseDetection(t *testing.T) {
	tests := []struct {
		name         string
		validCount   int
		platform     string
		wantSparse   bool
		wantIssueMsg string
	}{
		{
			name:       "正常平台：15 条正好达标",
			validCount: 15,
			platform:   "qidian",
			wantSparse: false,
		},
		{
			name:         "正常平台：14 条不足",
			validCount:   14,
			platform:     "qidian",
			wantSparse:   true,
			wantIssueMsg: "有效条目不足 15 条",
		},
		{
			name:       "小平台：10 条达标",
			validCount: 10,
			platform:   "qimao",
			wantSparse: false,
		},
		{
			name:         "小平台：9 条不足",
			validCount:   9,
			platform:     "ciweimao",
			wantSparse:   true,
			wantIssueMsg: "有效条目不足 10 条",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := make([]Entry, tt.validCount)
			for i := 0; i < tt.validCount; i++ {
				raw[i] = Entry{Rank: i + 1, Title: "书", Author: "作者"}
			}

			_, report := CleanEntries(raw, tt.platform)

			if report.Sparse != tt.wantSparse {
				t.Errorf("Sparse = %v, want %v", report.Sparse, tt.wantSparse)
			}

			if tt.wantSparse && tt.wantIssueMsg != "" {
				found := false
				for _, issue := range report.Issues {
					if containsSubstr(issue, tt.wantIssueMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Issues = %v, want to contain %q", report.Issues, tt.wantIssueMsg)
				}
			}
		})
	}
}

// containsSubstr 检查字符串是否包含子串
func containsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[:len(substr)] == substr || containsSubstr(s[1:], substr))))
}
