package chief

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/app/novel"
)

// 书况点名标记在案的章与用户能怎么做（D94）；章多时只列前 12 章并给总数。
func TestMarkedChaptersInBrief(t *testing.T) {
	if got := markedChapters(nil); got != "" {
		t.Fatalf("no marks = %q", got)
	}
	got := markedChapters([]novel.Mark{{ChapterID: "c91", Number: 91, Repairs: 2}, {ChapterID: "c120", Number: 120, Repairs: 2}})
	if !strings.HasPrefix(got, "第 91、120 章自动修订用尽仍有审阅意见") || !strings.Contains(got, "按 y 接受") || !strings.Contains(got, "rework") {
		t.Fatalf("marks = %q", got)
	}
	many := make([]novel.Mark, 15)
	for i := range many {
		many[i] = novel.Mark{Number: i + 1}
	}
	if got := markedChapters(many); !strings.HasPrefix(got, "第 1、2、3、4、5、6、7、8、9、10、11、12 章等 15 章") {
		t.Fatalf("many marks = %q", got)
	}
}
