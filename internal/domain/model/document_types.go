package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// SingletonDocumentID 是单例文档（Intent、Approval、Overlay、Assets）的唯一 ID。
const SingletonDocumentID = "root"

// DocumentTypeSpec 是一种权威文档的登记项：属于哪个权威流、是否单例、是否只有
// 用户裁决才能变更（D25/D31/D33）、是否只追加（D43），以及内容校验与依赖提取。
// 读取方（引用校验、内容校验、依赖图、授权、投影加载、重定位）都查这张表，不再
// 各维护一份 switch。
type DocumentTypeSpec struct {
	Kind       DocumentKind
	Authority  AuthorityKind
	Singleton  bool
	UserOnly   bool
	AppendOnly bool
	codec      documentCodec
}

type documentCodec struct {
	validate     func(DocumentRef, json.RawMessage) error
	dependencies func(DocumentRef, json.RawMessage) ([]DocumentRef, error)
}

var documentTypes = []DocumentTypeSpec{
	{Kind: DocumentIntent, Authority: AuthorityProject, Singleton: true,
		codec: codec[Intent]("intent", nil, nil)},
	{Kind: DocumentPlan, Authority: AuthorityProject,
		codec: codec("plan node", func(v PlanNode) string { return v.ID }, func(v PlanNode) []DocumentRef {
			if v.ParentID == "" {
				return v.DependsOn
			}
			return append(slices.Clone(v.DependsOn), DocumentRef{Kind: DocumentPlan, ID: v.ParentID})
		})},
	{Kind: DocumentEntity, Authority: AuthorityProject,
		codec: codec("entity", func(v Entity) string { return v.ID }, nil)},
	// Canon 主体引用实体（D35）：事实随实体失效，实体缺失即结构冲突。
	{Kind: DocumentCanon, Authority: AuthorityProject,
		codec: codec("canon fact", func(v CanonFact) string { return v.ID }, func(v CanonFact) []DocumentRef {
			return append(slices.Clone(v.DependsOn), DocumentRef{Kind: DocumentEntity, ID: v.SubjectID})
		})},
	{Kind: DocumentManuscript, Authority: AuthorityProject,
		codec: codec("manuscript", func(v ManuscriptChapter) string { return v.ID }, func(v ManuscriptChapter) []DocumentRef {
			return append(slices.Clone(v.DependsOn), DocumentRef{Kind: DocumentPlan, ID: v.PlanNodeID})
		})},
	{Kind: DocumentAttachment, Authority: AuthorityProject,
		codec: codec("attachment", func(v Attachment) string { return v.ID }, func(v Attachment) []DocumentRef {
			return append(slices.Clone(v.DependsOn), v.Target)
		})},
	{Kind: DocumentOwnership, Authority: AuthorityProject, UserOnly: true,
		codec: codec("ownership", func(v OwnershipRule) string { return v.Target.Key() }, func(v OwnershipRule) []DocumentRef {
			return []DocumentRef{v.Target}
		})},
	{Kind: DocumentApproval, Authority: AuthorityProject, Singleton: true, UserOnly: true,
		codec: codec[ApprovalSetting]("approval setting", nil, nil)},
	{Kind: DocumentOverlay, Authority: AuthorityProject, Singleton: true, UserOnly: true,
		codec: codec[ProjectOverlay]("project overlay", nil, nil)},
	{Kind: DocumentAssets, Authority: AuthorityProject, Singleton: true, UserOnly: true,
		codec: codec[ProjectAssetRefs]("project asset refs", nil, nil)},
	{Kind: DocumentDirective, Authority: AuthorityProject, UserOnly: true,
		codec: codec("directive", func(v Directive) string { return v.ID }, nil)},
	// 裁决只追加（D43）：接受与撤回都是新记录，历史不变；回滚与投影导入不触碰它。
	{Kind: DocumentAdjudication, Authority: AuthorityProject, UserOnly: true, AppendOnly: true,
		codec: codec("adjudication", func(v Adjudication) string { return v.ID }, nil)},
	{Kind: DocumentCreatorProfile, Authority: AuthorityProfile,
		codec: codec("creator profile", func(v CreatorProfile) string { return v.ID }, nil)},
	{Kind: DocumentPack, Authority: AuthorityPack,
		codec: codec("pack manifest", func(v PackManifest) string { return v.ID }, nil)},
}

func DocumentType(kind DocumentKind) (DocumentTypeSpec, error) {
	for _, spec := range documentTypes {
		if spec.Kind == kind {
			return spec, nil
		}
	}
	return DocumentTypeSpec{}, fmt.Errorf("unknown document kind %q: %w", kind, ErrInvalid)
}

// DocumentKindsFor 列出某个权威流允许承载的文档种类，按登记顺序。
func DocumentKindsFor(authority AuthorityKind) []DocumentKind {
	var kinds []DocumentKind
	for _, spec := range documentTypes {
		if spec.Authority == authority {
			kinds = append(kinds, spec.Kind)
		}
	}
	return kinds
}

func ValidateDocumentContent(ref DocumentRef, content json.RawMessage) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	spec, err := DocumentType(ref.Kind)
	if err != nil {
		return err
	}
	if spec.Singleton && ref.ID != SingletonDocumentID {
		return fmt.Errorf("%s document id must be %s: %w", ref.Kind, SingletonDocumentID, ErrInvalid)
	}
	return spec.codec.validate(ref, content)
}

func DocumentDependencies(ref DocumentRef, content json.RawMessage) ([]DocumentRef, error) {
	if err := ValidateDocumentContent(ref, content); err != nil {
		return nil, err
	}
	spec, _ := DocumentType(ref.Kind)
	dependencies, err := spec.codec.dependencies(ref, content)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(dependencies, func(a, b DocumentRef) int { return strings.Compare(a.Key(), b.Key()) })
	return slices.CompactFunc(dependencies, func(a, b DocumentRef) bool { return a.Key() == b.Key() }), nil
}

// codec 由文档类型的 Go 结构生成校验与依赖提取：identity 非空时要求内容 ID 与
// 文档 ID 一致，dependencies 非空时提供结构依赖。
func codec[T interface{ Validate() error }](
	name string,
	identity func(T) string,
	dependencies func(T) []DocumentRef,
) documentCodec {
	decode := func(ref DocumentRef, content json.RawMessage) (T, error) {
		var value T
		if err := DecodeStrict(content, &value); err != nil {
			return value, fmt.Errorf("decode %s: %w", name, err)
		}
		if identity != nil && identity(value) != ref.ID {
			return value, fmt.Errorf("%s id %q does not match document id %q: %w", name, identity(value), ref.ID, ErrInvalid)
		}
		return value, nil
	}
	return documentCodec{
		validate: func(ref DocumentRef, content json.RawMessage) error {
			value, err := decode(ref, content)
			if err != nil {
				return err
			}
			return value.Validate()
		},
		dependencies: func(ref DocumentRef, content json.RawMessage) ([]DocumentRef, error) {
			if dependencies == nil {
				return nil, nil
			}
			value, err := decode(ref, content)
			if err != nil {
				return nil, err
			}
			return dependencies(value), nil
		},
	}
}
