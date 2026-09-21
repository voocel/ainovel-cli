package change

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func validateTargetDocuments(target model.AuthorityTarget, patches []model.Patch) error {
	allowed := make(map[model.DocumentKind]struct{})
	for _, kind := range model.DocumentKindsFor(target.Kind) {
		allowed[kind] = struct{}{}
	}
	for _, patch := range patches {
		if _, ok := allowed[patch.Document.Kind]; !ok {
			return fmt.Errorf("document %s cannot be written to %s authority: %w", patch.Document.Kind, target.Kind, model.ErrInvalid)
		}
	}
	return nil
}

func validateProjectedState(target model.AuthorityTarget, state documentState) error {
	if target.Kind != model.AuthorityProject {
		return validateAssetState(target, state)
	}

	planNodes := make(map[string]model.PlanNode)
	manuscriptIDs := make(map[string]struct{})
	for _, document := range state {
		switch document.Document.Kind {
		case model.DocumentPlan:
			var node model.PlanNode
			if err := json.Unmarshal(document.Content, &node); err != nil {
				return fmt.Errorf("decode plan node %q: %w", document.Document.ID, err)
			}
			planNodes[node.ID] = node
		case model.DocumentManuscript:
			manuscriptIDs[document.Document.ID] = struct{}{}
		}
	}

	for _, node := range planNodes {
		if node.ParentID == "" {
			continue
		}
		parent, ok := planNodes[node.ParentID]
		if !ok {
			return fmt.Errorf("plan node %q references missing parent %q: %w", node.ID, node.ParentID, ErrStructuralConflict)
		}
		if !validPlanParent(node.Kind, parent.Kind) {
			return fmt.Errorf("plan node %q of kind %s cannot have %s parent: %w", node.ID, node.Kind, parent.Kind, ErrStructuralConflict)
		}
	}
	if err := detectPlanCycles(planNodes); err != nil {
		return err
	}

	for _, document := range state {
		if document.Document.Kind == model.DocumentManuscript {
			var chapter model.ManuscriptChapter
			if err := json.Unmarshal(document.Content, &chapter); err != nil {
				return fmt.Errorf("decode chapter %q: %w", document.Document.ID, err)
			}
			plan, ok := planNodes[chapter.PlanNodeID]
			if !ok || plan.Kind != model.PlanChapter {
				return fmt.Errorf("chapter %q references missing chapter plan %q: %w", chapter.ID, chapter.PlanNodeID, ErrStructuralConflict)
			}
		}
		if document.Document.Kind == model.DocumentCanon {
			var fact model.CanonFact
			if err := json.Unmarshal(document.Content, &fact); err != nil {
				return fmt.Errorf("decode canon %q: %w", document.Document.ID, err)
			}
			for _, chapterID := range []string{fact.SourceChapterID, fact.EffectiveChapterID} {
				if _, ok := manuscriptIDs[chapterID]; chapterID != "" && !ok {
					return fmt.Errorf("canon %q references missing chapter %q: %w", fact.ID, chapterID, ErrStructuralConflict)
				}
			}
		}
		dependencies, err := model.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if _, ok := state[dependency.Key()]; !ok {
				return fmt.Errorf("document %q depends on missing %q: %w", document.Document.Key(), dependency.Key(), ErrStructuralConflict)
			}
		}
	}
	return detectDependencyCycles(state)
}

func validateCanonContinuity(base documentState, patches []model.Patch) error {
	for _, patch := range patches {
		if patch.Document.Kind != model.DocumentCanon || patch.Operation != model.PatchPut {
			continue
		}
		var next model.CanonFact
		if err := json.Unmarshal(patch.Content, &next); err != nil {
			return fmt.Errorf("decode canon continuity candidate %q: %w", patch.Document.ID, err)
		}
		previousDocument, exists := base[patch.Document.Key()]
		if !exists {
			if len(next.PreviousValue) != 0 {
				return fmt.Errorf("new canon %q cannot declare old_value: %w", next.ID, ErrStructuralConflict)
			}
			continue
		}
		var previous model.CanonFact
		if err := json.Unmarshal(previousDocument.Content, &previous); err != nil {
			return fmt.Errorf("decode previous canon %q: %w", next.ID, err)
		}
		if next.Kind != previous.Kind || next.SubjectID != previous.SubjectID || next.Predicate != previous.Predicate {
			return fmt.Errorf("canon %q identity cannot change; create a new stable fact instead: %w", next.ID, ErrStructuralConflict)
		}
		if len(next.PreviousValue) == 0 {
			return fmt.Errorf("updated canon %q requires old_value: %w", next.ID, ErrStructuralConflict)
		}
		same, err := sameJSONValue(next.PreviousValue, previous.Value)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("canon %q old_value does not match the previous new_value: %w", next.ID, ErrStructuralConflict)
		}
	}
	return nil
}

// validateAICanon 落实 AI/扩展提案的 Canon 规则（§4.4 D41）：
//  1. 带正文的提案里，每条事实 put 的来源章必须是本提案的正文之一；
//  2. 每个正文 put 至少一条来源事实，且必须重申报 base 中全部来源于该章的事实（put 或 delete）；
//  3. 带正文的提案只能改动来源章在本提案正文集合内的事件——事件跨章只追加；
//  4. 状态类事实更新的生效位置不得早于现值——插叙不覆盖当前状态，应记录为事件。
//
// 用户提案不受此限，由用户为内容背书。
func validateAICanon(base, projected documentState, change model.Proposal) error {
	if change.Author.Kind != model.AuthorAI && change.Author.Kind != model.AuthorExtension {
		return nil
	}
	chapters := make(map[string]struct{})
	for _, patch := range change.Patches {
		if patch.Document.Kind == model.DocumentManuscript && patch.Operation == model.PatchPut {
			chapters[patch.Document.ID] = struct{}{}
		}
	}
	numbers, err := chapterNumbers(projected)
	if err != nil {
		return err
	}
	covered := make(map[string]struct{})
	touched := make(map[string]struct{})
	for _, patch := range change.Patches {
		if patch.Document.Kind != model.DocumentCanon {
			continue
		}
		touched[patch.Document.Key()] = struct{}{}
		var previous *model.CanonFact
		if document, exists := base[patch.Document.Key()]; exists {
			previous = new(model.CanonFact)
			if err := json.Unmarshal(document.Content, previous); err != nil {
				return fmt.Errorf("decode previous canon %q: %w", patch.Document.ID, err)
			}
			if _, own := chapters[previous.SourceChapterID]; len(chapters) > 0 && previous.IsEvent() && !own {
				return fmt.Errorf("event %q belongs to chapter %q outside this proposal; events are append-only across chapters: %w",
					previous.ID, previous.SourceChapterID, ErrStructuralConflict)
			}
		}
		if patch.Operation != model.PatchPut {
			continue
		}
		var next model.CanonFact
		if err := json.Unmarshal(patch.Content, &next); err != nil {
			return fmt.Errorf("decode canon %q: %w", patch.Document.ID, err)
		}
		if _, own := chapters[next.SourceChapterID]; len(chapters) > 0 && !own {
			return fmt.Errorf("canon %q must be sourced from a chapter in the same proposal: %w", next.ID, ErrStructuralConflict)
		}
		covered[next.SourceChapterID] = struct{}{}
		if previous != nil && !next.IsEvent() && numbers[next.EffectiveChapter()] < numbers[previous.EffectiveChapter()] {
			return fmt.Errorf("canon %q cannot move its effective position back from %q to %q; record flashbacks as events: %w",
				next.ID, previous.EffectiveChapter(), next.EffectiveChapter(), ErrStructuralConflict)
		}
	}
	for chapterID := range chapters {
		if _, ok := covered[chapterID]; !ok {
			return fmt.Errorf("AI manuscript %q requires a Canon Delta in the same Proposal: %w", chapterID, ErrStructuralConflict)
		}
	}
	for key, document := range base {
		if document.Document.Kind != model.DocumentCanon {
			continue
		}
		var fact model.CanonFact
		if err := json.Unmarshal(document.Content, &fact); err != nil {
			return fmt.Errorf("decode canon %q: %w", document.Document.ID, err)
		}
		if _, rewritten := chapters[fact.SourceChapterID]; rewritten {
			if _, ok := touched[key]; !ok {
				return fmt.Errorf("rewritten chapter %q must redeclare canon %q (confirm, update or delete): %w",
					fact.SourceChapterID, fact.ID, ErrStructuralConflict)
			}
		}
	}
	return nil
}

// chapterNumbers 取投影状态里各正文的章号：故事内位置按它比较。
func chapterNumbers(state documentState) (map[string]int, error) {
	numbers := make(map[string]int)
	for _, document := range state {
		if document.Document.Kind != model.DocumentManuscript {
			continue
		}
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(document.Content, &chapter); err != nil {
			return nil, fmt.Errorf("decode chapter %q: %w", document.Document.ID, err)
		}
		numbers[chapter.ID] = chapter.Number
	}
	return numbers, nil
}

// validateChapterAuthors 落实 D34：AI 与扩展提交的章节必须署自己的名，不得冒认
// 用户或他方；用户提交（含导入、回滚）可以携带任何作者，由用户为内容背书。
func validateChapterAuthors(change model.Proposal) error {
	if change.Author.Kind != model.AuthorAI && change.Author.Kind != model.AuthorExtension {
		return nil
	}
	for _, patch := range change.Patches {
		if patch.Document.Kind != model.DocumentManuscript || patch.Operation != model.PatchPut {
			continue
		}
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(patch.Content, &chapter); err != nil {
			return fmt.Errorf("decode chapter author %q: %w", patch.Document.ID, err)
		}
		if chapter.Author != change.Author.Kind {
			return fmt.Errorf("chapter %q author %s does not match proposal author %s: %w",
				chapter.ID, chapter.Author, change.Author.Kind, ErrStructuralConflict)
		}
	}
	return nil
}

// validateAppendOnly 落实只追加文档（D43 裁决）：不得覆盖已存在的记录，也不得删除。
func validateAppendOnly(base documentState, patches []model.Patch) error {
	for _, patch := range patches {
		spec, err := model.DocumentType(patch.Document.Kind)
		if err != nil {
			return err
		}
		if !spec.AppendOnly {
			continue
		}
		if _, exists := base[patch.Document.Key()]; exists || patch.Operation == model.PatchDelete {
			return fmt.Errorf("%s is append-only: %w", patch.Document.Key(), ErrStructuralConflict)
		}
	}
	return nil
}

func sameJSONValue(left, right json.RawMessage) (bool, error) {
	canonical := func(raw json.RawMessage) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	leftValue, err := canonical(left)
	if err != nil {
		return false, fmt.Errorf("decode canon old_value: %w", err)
	}
	rightValue, err := canonical(right)
	if err != nil {
		return false, fmt.Errorf("decode previous canon new_value: %w", err)
	}
	return bytes.Equal(leftValue, rightValue), nil
}

func validateAssetState(target model.AuthorityTarget, state documentState) error {
	if len(state) > 1 {
		return fmt.Errorf("%s authority may contain only one root document: %w", target.Kind, ErrStructuralConflict)
	}
	for _, document := range state {
		switch target.Kind {
		case model.AuthorityProfile:
			var profile model.CreatorProfile
			if err := json.Unmarshal(document.Content, &profile); err != nil {
				return err
			}
			if profile.ID != target.ID || profile.Scope != target.Scope {
				return fmt.Errorf("creator profile does not match authority target: %w", ErrStructuralConflict)
			}
		case model.AuthorityPack:
			var pack model.PackManifest
			if err := json.Unmarshal(document.Content, &pack); err != nil {
				return err
			}
			if pack.ID != target.ID {
				return fmt.Errorf("pack does not match authority target: %w", ErrStructuralConflict)
			}
		}
	}
	return nil
}

func validPlanParent(child, parent model.PlanNodeKind) bool {
	return child == model.PlanArc && parent == model.PlanVolume ||
		child == model.PlanChapter && parent == model.PlanArc ||
		child == model.PlanBeat && parent == model.PlanChapter
}

func detectPlanCycles(nodes map[string]model.PlanNode) error {
	for id := range nodes {
		seen := make(map[string]struct{})
		current := id
		for current != "" {
			if _, ok := seen[current]; ok {
				return fmt.Errorf("plan parent cycle at %q: %w", current, ErrStructuralConflict)
			}
			seen[current] = struct{}{}
			current = nodes[current].ParentID
		}
	}
	return nil
}

func detectDependencyCycles(state documentState) error {
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("document dependency cycle at %q: %w", key, ErrStructuralConflict)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		document := state[key]
		dependencies, err := model.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if err := visit(dependency.Key()); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for key := range state {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func buildStructuralImpact(state documentState, direct []model.DocumentRef) (StructuralImpact, error) {
	slices.SortFunc(direct, compareDocumentRef)
	direct = slices.CompactFunc(direct, func(a, b model.DocumentRef) bool { return a.Key() == b.Key() })
	directSet := make(map[string]struct{}, len(direct))
	for _, ref := range direct {
		directSet[ref.Key()] = struct{}{}
	}

	reverse := make(map[string][]model.DocumentRef)
	for _, document := range state {
		dependencies, err := model.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return StructuralImpact{}, err
		}
		for _, dependency := range dependencies {
			reverse[dependency.Key()] = append(reverse[dependency.Key()], document.Document)
		}
	}

	queue := append([]model.DocumentRef(nil), direct...)
	affected := make(map[string]model.DocumentRef)
	seen := make(map[string]struct{}, len(direct))
	for _, ref := range direct {
		seen[ref.Key()] = struct{}{}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range reverse[current.Key()] {
			if _, ok := seen[dependent.Key()]; ok {
				continue
			}
			seen[dependent.Key()] = struct{}{}
			queue = append(queue, dependent)
			if _, direct := directSet[dependent.Key()]; !direct {
				affected[dependent.Key()] = dependent
			}
		}
	}
	affectedRefs := make([]model.DocumentRef, 0, len(affected))
	for _, ref := range affected {
		affectedRefs = append(affectedRefs, ref)
	}
	slices.SortFunc(affectedRefs, compareDocumentRef)
	return StructuralImpact{Direct: direct, Affected: affectedRefs}, nil
}

func cloneState(source documentState) documentState {
	result := make(documentState, len(source))
	for key, value := range source {
		value.Content = append(json.RawMessage(nil), value.Content...)
		result[key] = value
	}
	return result
}

func compareDocumentRef(a, b model.DocumentRef) int {
	return strings.Compare(a.Key(), b.Key())
}
