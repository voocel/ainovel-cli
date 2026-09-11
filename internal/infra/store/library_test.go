package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestLibrarySummaryCountsLatestLiveChapters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "long-book"}
	intent := json.RawMessage(`{"premise":"雨夜来信","target_chapters":500}`)
	patches := []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, Operation: model.PatchPut, Content: intent}}
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("雨夜来信", 500)})
	for i := 1; i <= 500; i++ {
		patches = append(patches, model.Patch{Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: fmt.Sprint(i)}, Operation: model.PatchPut, Content: body})
	}
	if _, err := commitTestProposal(ctx, s, testChange("initial", target, 0, patches...)); err != nil {
		t.Fatal(err)
	}
	revisions := []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: "1"}, Operation: model.PatchPut, Content: body}, {Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: "2"}, Operation: model.PatchDelete}}
	if _, err := commitTestProposal(ctx, s, testChange("revision", target, 1, revisions...)); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.ListProjectSummaries(ctx)
	if err != nil || len(summaries) != 1 || summaries[0].Written != 499 || string(summaries[0].Intent) != string(intent) {
		t.Fatalf("summary = %+v, %v", summaries, err)
	}
}
