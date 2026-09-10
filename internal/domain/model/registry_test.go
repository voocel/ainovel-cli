package model

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDocumentTypeTableCoversEveryKindAndAuthority(t *testing.T) {
	all := []DocumentKind{
		DocumentIntent, DocumentPlan, DocumentEntity, DocumentCanon, DocumentManuscript, DocumentAttachment,
		DocumentOwnership, DocumentApproval, DocumentOverlay, DocumentAssets, DocumentDirective, DocumentAdjudication,
		DocumentCreatorProfile, DocumentPack,
	}
	var listed []DocumentKind
	for _, authority := range []AuthorityKind{AuthorityProject, AuthorityProfile, AuthorityPack} {
		listed = append(listed, DocumentKindsFor(authority)...)
	}
	slices.Sort(all)
	slices.Sort(listed)
	if !slices.Equal(all, listed) {
		t.Fatalf("registered kinds = %v, want %v", listed, all)
	}
	if _, err := DocumentType("unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind err = %v", err)
	}
	var userOnly, singleton []DocumentKind
	for _, kind := range all {
		spec, err := DocumentType(kind)
		if err != nil {
			t.Fatalf("document type %s: %v", kind, err)
		}
		if spec.UserOnly {
			userOnly = append(userOnly, kind)
		}
		if spec.Singleton {
			singleton = append(singleton, kind)
		}
	}
	// 用户专属文档（D25/D31/D33）与单例文档的集合由登记表定型，授权与校验只读表。
	if want := []DocumentKind{DocumentAdjudication, DocumentApproval, DocumentAssets, DocumentDirective, DocumentOverlay, DocumentOwnership}; !slices.Equal(userOnly, want) {
		t.Fatalf("user-only kinds = %v, want %v", userOnly, want)
	}
	if want := []DocumentKind{DocumentApproval, DocumentAssets, DocumentIntent, DocumentOverlay}; !slices.Equal(singleton, want) {
		t.Fatalf("singleton kinds = %v, want %v", singleton, want)
	}
	if err := ValidateDocumentContent(DocumentRef{Kind: DocumentIntent, ID: "other"}, []byte(`{"premise":"x"}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-root singleton err = %v", err)
	}
}

func TestCanonFactDependsOnEntity(t *testing.T) {
	fact, _ := json.Marshal(CanonFact{
		ID: "hero-origin", Kind: CanonState, SubjectID: "hero", Predicate: "state.origin", Value: json.RawMessage(`"农家子"`),
		DependsOn: []DocumentRef{{Kind: DocumentPlan, ID: "chapter-plan-1"}},
	})
	dependencies, err := DocumentDependencies(DocumentRef{Kind: DocumentCanon, ID: "hero-origin"}, fact)
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	if got := refKeys(dependencies); !slices.Equal(got, []string{"entity:hero", "plan:chapter-plan-1"}) {
		t.Fatalf("canon dependencies = %v", got)
	}
	attachment, _ := json.Marshal(Attachment{
		ID: "cover-1", Target: DocumentRef{Kind: DocumentManuscript, ID: "chapter-1"}, Role: "cover",
		Artifact: ArtifactRef{ID: "op/cover", Digest: Digest([]byte("abc"))},
	})
	dependencies, err = DocumentDependencies(DocumentRef{Kind: DocumentAttachment, ID: "cover-1"}, attachment)
	if err != nil {
		t.Fatalf("attachment dependencies: %v", err)
	}
	if got := refKeys(dependencies); !slices.Equal(got, []string{"manuscript:chapter-1"}) {
		t.Fatalf("attachment dependencies = %v", got)
	}
}

func TestManuscriptAuthorRequired(t *testing.T) {
	chapter := ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
		Blocks: []ManuscriptBlock{{ID: "p-1", Text: "第一段"}},
	}
	if err := chapter.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing author err = %v", err)
	}
	chapter.Author = AuthorSystem
	if err := chapter.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("system author err = %v", err)
	}
	for _, author := range []AuthorKind{AuthorUser, AuthorAI, AuthorExtension} {
		chapter.Author = author
		if err := chapter.Validate(); err != nil {
			t.Fatalf("author %s: %v", author, err)
		}
	}
}

func TestDecodeTaskInputRejectsUnknownFieldsPerKind(t *testing.T) {
	for _, spec := range OperationKinds() {
		if spec.Label == "" || spec.NewInput == nil {
			t.Fatalf("kind %s is registered without label or input", spec.Kind)
		}
		if _, err := DecodeTaskInput(spec.Kind, []byte(`{"unexpected":1}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("kind %s accepted an unknown field: %v", spec.Kind, err)
		}
		if _, err := DecodeTaskInput(spec.Kind, []byte(`{}`)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("kind %s accepted an empty input: %v", spec.Kind, err)
		}
	}
	if _, err := DecodeTaskInput("unknown", []byte(`{}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind err = %v", err)
	}
	operation := Operation{Kind: OperationReviewRange, Input: json.RawMessage(`{"chapter_ids":["chapter-1"],"basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":2}]}}`)}
	if _, err := TaskInputAs[WriteChapterInput](operation); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched input type err = %v", err)
	}
	input, err := TaskInputAs[ReviewRangeInput](operation)
	if err != nil || !slices.Equal(input.ChapterIDs, []string{"chapter-1"}) {
		t.Fatalf("typed input = %#v, %v", input, err)
	}
}

func TestBindChapterDependenciesFollowsCanonDelta(t *testing.T) {
	chapter, _ := json.Marshal(ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: AuthorAI,
		Blocks:    []ManuscriptBlock{{ID: "p-1", Text: "第一段"}},
		DependsOn: []DocumentRef{{Kind: DocumentCanon, ID: "model-declared"}},
	})
	fact := func(id, subject, chapterID string) json.RawMessage {
		content, _ := json.Marshal(CanonFact{
			ID: id, Kind: CanonEvent, SubjectID: subject, Predicate: "event." + id,
			Value: json.RawMessage(`true`), SourceChapterID: chapterID,
		})
		return content
	}
	patches, err := BindChapterDependencies([]Patch{
		{Document: DocumentRef{Kind: DocumentManuscript, ID: "chapter-1"}, Operation: PatchPut, Content: chapter},
		{Document: DocumentRef{Kind: DocumentCanon, ID: "meet"}, Operation: PatchPut, Content: fact("meet", "villain", "chapter-1")},
		{Document: DocumentRef{Kind: DocumentCanon, ID: "arrive"}, Operation: PatchPut, Content: fact("arrive", "hero", "chapter-1")},
		{Document: DocumentRef{Kind: DocumentCanon, ID: "leave"}, Operation: PatchPut, Content: fact("leave", "hero", "chapter-1")},
		{Document: DocumentRef{Kind: DocumentCanon, ID: "other"}, Operation: PatchPut, Content: fact("other", "mentor", "chapter-2")},
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	var bound ManuscriptChapter
	if err := json.Unmarshal(patches[0].Content, &bound); err != nil {
		t.Fatalf("decode bound chapter: %v", err)
	}
	// 宿主写入的依赖覆盖模型声明：只含本章 Canon Delta 的实体，排序去重。
	if got := refKeys(bound.DependsOn); !slices.Equal(got, []string{"entity:hero", "entity:villain"}) {
		t.Fatalf("bound dependencies = %v", got)
	}
}

func refKeys(refs []DocumentRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.Key())
	}
	return keys
}
