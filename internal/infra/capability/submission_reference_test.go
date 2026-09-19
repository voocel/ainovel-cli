package capability

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestManuscriptDifferencePinpointsPunctuation(t *testing.T) {
	saved := model.ManuscriptChapter{Blocks: []model.ManuscriptBlock{{ID: "ch3-b10", Text: "是赌场那头的动静……得快点。"}}}
	proposed := model.ManuscriptChapter{Blocks: []model.ManuscriptBlock{{ID: "ch3-b10", Text: "是赌场那头的动静......得快点。"}}}
	diff := manuscriptDifference(saved, proposed)
	for _, want := range []string{"ch3-b10", "……", "......", "character 9"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("missing %q: %s", want, diff)
		}
	}
}
