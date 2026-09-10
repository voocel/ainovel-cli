package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// EvidenceBasis 记录一份证据（审阅裁定、生成工件）依据了什么（D48）：文档钉住
// 最后变化 revision，作用域钉住成员摘要，工件钉住内容摘要。证据是否仍然有效只看
// 基线是否成立，与整本 Revision 无关——不相干的改动不使证据失效。
type EvidenceBasis struct {
	Documents []DocumentBasis `json:"documents,omitempty"`
	Scopes    []ScopeBasis    `json:"scopes,omitempty"`
	Artifacts []ArtifactRef   `json:"artifacts,omitempty"`
}

// DocumentBasis 钉住一份文档最后变化的 revision。
type DocumentBasis struct {
	Ref      DocumentRef `json:"ref"`
	Revision Revision    `json:"revision"`
}

// ScopeBasis 钉住一个作用域的成员集合：命中 Target 的 active 要求及其版本的摘要。
type ScopeBasis struct {
	Kind   string          `json:"kind"`
	Target DirectiveTarget `json:"target"`
	Canon  *CanonScope     `json:"canon,omitempty"`
	Digest string          `json:"digest"`
}

const ScopeDirective = "directive"
const ScopeCanon = "canon"

// CanonScope describes the facts relevant to a reviewed range. Chapter IDs include
// facts recorded by those chapters; subjects include earlier facts about the same
// entities. Later story facts are excluded, including those about the same entity.
type CanonScope struct {
	ChapterIDs     []string `json:"chapter_ids"`
	SubjectIDs     []string `json:"subject_ids,omitempty"`
	ThroughChapter int      `json:"through_chapter"`
}

func ReviewCanonScope(chapters []ManuscriptChapter, ids []string) CanonScope {
	scope := CanonScope{ChapterIDs: slices.Clone(ids)}
	for _, chapter := range chapters {
		if !slices.Contains(ids, chapter.ID) {
			continue
		}
		scope.ThroughChapter = max(scope.ThroughChapter, chapter.Number)
		for _, ref := range chapter.DependsOn {
			if ref.Kind == DocumentEntity {
				scope.SubjectIDs = append(scope.SubjectIDs, ref.ID)
			}
		}
	}
	slices.Sort(scope.ChapterIDs)
	scope.ChapterIDs = slices.Compact(scope.ChapterIDs)
	slices.Sort(scope.SubjectIDs)
	scope.SubjectIDs = slices.Compact(scope.SubjectIDs)
	return scope
}

// CanonScopeRefs is shared by prompt assembly and evidence verification. Source
// facts are always included; other facts must be relevant and effective by the
// end of the range. Planning world rules apply to every range.
func CanonScopeRefs(facts []CanonFact, chapters []ManuscriptChapter, scope CanonScope) []DocumentRef {
	numbers := make(map[string]int, len(chapters))
	for _, chapter := range chapters {
		numbers[chapter.ID] = chapter.Number
	}
	var refs []DocumentRef
	for _, fact := range facts {
		own := slices.Contains(scope.ChapterIDs, fact.SourceChapterID)
		relevant := own || fact.Kind == CanonWorldRule || slices.Contains(scope.SubjectIDs, fact.SubjectID)
		position := fact.EffectiveChapter()
		if fact.IsEvent() {
			position = fact.SourceChapterID
		}
		if relevant && (own || position == "" || numbers[position] <= scope.ThroughChapter) {
			refs = append(refs, DocumentRef{Kind: DocumentCanon, ID: fact.ID})
		}
	}
	slices.SortFunc(refs, func(a, b DocumentRef) int { return strings.Compare(a.Key(), b.Key()) })
	return refs
}

// BasisField 是任务输入里承载基线的 JSON 字段名：基线是内核判定有效性的依据，
// 不进入模型提示词。
const BasisField = "basis"

func (b EvidenceBasis) Validate() error {
	seen := make(map[string]struct{}, len(b.Documents)+len(b.Scopes)+len(b.Artifacts))
	unique := func(key string) error {
		if _, exists := seen[key]; exists {
			return fmt.Errorf("basis entry %q is duplicated: %w", key, ErrInvalid)
		}
		seen[key] = struct{}{}
		return nil
	}
	for _, document := range b.Documents {
		if err := document.Ref.Validate(); err != nil {
			return err
		}
		if document.Revision <= InitialRevision {
			return fmt.Errorf("basis document %s requires a positive revision: %w", document.Ref.Key(), ErrInvalid)
		}
		if err := unique("document:" + document.Ref.Key()); err != nil {
			return err
		}
	}
	for _, scope := range b.Scopes {
		switch scope.Kind {
		case ScopeDirective:
			if scope.Canon != nil {
				return fmt.Errorf("directive scope cannot carry a Canon target: %w", ErrInvalid)
			}
		case ScopeCanon:
			if scope.Canon == nil || len(scope.Canon.ChapterIDs) == 0 || scope.Canon.ThroughChapter <= 0 {
				return fmt.Errorf("Canon scope requires chapters and a positive cutoff: %w", ErrInvalid)
			}
			if err := validateDistinctStrings("Canon scope chapters", scope.Canon.ChapterIDs); err != nil {
				return err
			}
			if err := validateDistinctStrings("Canon scope subjects", scope.Canon.SubjectIDs); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown scope kind %q: %w", scope.Kind, ErrInvalid)
		}
		if strings.TrimSpace(scope.Digest) == "" {
			return fmt.Errorf("scope basis requires a digest: %w", ErrInvalid)
		}
		if err := unique("scope:" + scope.targetKey()); err != nil {
			return err
		}
	}
	for _, artifact := range b.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
		if err := unique("artifact:" + artifact.ID); err != nil {
			return err
		}
	}
	return nil
}

// Normalize 返回排序去重后的副本，让相同基线有唯一表示。
func (b EvidenceBasis) Normalize() EvidenceBasis {
	documents := slices.Clone(b.Documents)
	slices.SortFunc(documents, func(left, right DocumentBasis) int {
		if key := strings.Compare(left.Ref.Key(), right.Ref.Key()); key != 0 {
			return key
		}
		return int(left.Revision - right.Revision)
	})
	scopes := slices.Clone(b.Scopes)
	slices.SortFunc(scopes, func(left, right ScopeBasis) int {
		return strings.Compare(left.targetKey()+"\x00"+left.Digest, right.targetKey()+"\x00"+right.Digest)
	})
	artifacts := slices.Clone(b.Artifacts)
	slices.SortFunc(artifacts, func(left, right ArtifactRef) int {
		return strings.Compare(left.ID+"\x00"+left.Digest, right.ID+"\x00"+right.Digest)
	})
	return EvidenceBasis{
		Documents: slices.Compact(documents),
		Scopes:    slices.CompactFunc(scopes, scopeEqual),
		Artifacts: slices.Compact(artifacts),
	}
}

func (b EvidenceBasis) Equal(other EvidenceBasis) bool {
	left, right := b.Normalize(), other.Normalize()
	return slices.Equal(left.Documents, right.Documents) &&
		slices.EqualFunc(left.Scopes, right.Scopes, scopeEqual) &&
		slices.Equal(left.Artifacts, right.Artifacts)
}

// Covers 防止执行器漏报任务声明的来源；它可以补充实际读取的依赖，但不能丢弃
// 冻结输入里的文档、作用域或工件。
func (b EvidenceBasis) Covers(required EvidenceBasis) bool {
	for _, entry := range required.Documents {
		if !slices.Contains(b.Documents, entry) {
			return false
		}
	}
	for _, entry := range required.Scopes {
		if !slices.ContainsFunc(b.Scopes, func(scope ScopeBasis) bool { return scopeEqual(scope, entry) }) {
			return false
		}
	}
	for _, entry := range required.Artifacts {
		if !slices.Contains(b.Artifacts, entry) {
			return false
		}
	}
	return true
}

func (s ScopeBasis) targetKey() string {
	if s.Kind == ScopeCanon {
		encoded, _ := json.Marshal(s.Canon)
		return s.Kind + "\x00" + string(encoded)
	}
	return s.Kind + "\x00" + strconv.Itoa(s.Target.ChapterNumber) + "\x00" + strings.Join(s.Target.PlanNodeIDs, ",")
}

func scopeEqual(left, right ScopeBasis) bool {
	return left.targetKey() == right.targetKey() && left.Digest == right.Digest
}

// ScopeMembers 列出命中 target 的 active 要求及其最后变化 revision：作用域基线的成员
// 集合，装配层与内核核对基线时用同一定义。
func ScopeMembers(directives []Directive, target DirectiveTarget, revisionOf func(DocumentRef) Revision) []DocumentBasis {
	matched := ActiveDirectivesFor(directives, target)
	members := make([]DocumentBasis, 0, len(matched))
	for _, directive := range matched {
		ref := DocumentRef{Kind: DocumentDirective, ID: directive.ID}
		members = append(members, DocumentBasis{Ref: ref, Revision: revisionOf(ref)})
	}
	return members
}

// ScopeDigest 对作用域成员（文档及其最后变化 revision）取摘要：成员增减或改版都改变摘要。
func ScopeDigest(members []DocumentBasis) string {
	normalized := EvidenceBasis{Documents: members}.Normalize().Documents
	lines := make([]string, len(normalized))
	for i, member := range normalized {
		lines[i] = member.Ref.Key() + "@" + strconv.FormatInt(int64(member.Revision), 10)
	}
	return Digest([]byte(strings.Join(lines, "\n")))
}
