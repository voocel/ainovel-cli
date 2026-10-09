package workbench

import (
	"encoding/json"
	"fmt"
	"strings"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
)

// ProposalView 是待裁决稿件里正文以外的变更，用故事语言描述（D66）：大纲按提案生效后
// 的序号成树，人物用名称，设定写成种类+主体+内容，不露文档 ID、谓词键与原始内容。正文候选
// 另见 Candidates，罗盘的前后对比由入口按当前罗盘呈现。
type ProposalView struct {
	// Summary 是规模摘要的各段，如「新增 1 卷 · 5 个故事弧 · 7 章」「设定 21 条」。
	Summary  []string               `json:"summary,omitempty"`
	Outline  []narrative.VolumeView `json:"outline,omitempty"`  // 新增或修订的卷弧章，祖先补齐
	Entities []narrative.EntityView `json:"entities,omitempty"` // 新增或修订的人物、地点、物品、组织
	Facts    []Fact                 `json:"facts,omitempty"`    // 新增或修订的设定
	Compass  *model.Compass         `json:"compass,omitempty"`  // 提案后的故事罗盘，未改动为空
	Removed  []string               `json:"removed,omitempty"`  // 删除的内容
	Other    []string               `json:"other,omitempty"`    // 其余种类的改动
}

func proposalView(project projectdoc.Snapshot, proposal model.Proposal) (ProposalView, error) {
	base := storyOf(project)
	next, err := base.Apply(proposal.Patches)
	if err != nil {
		return ProposalView{}, err
	}
	var view ProposalView
	var plans, entities []string
	added := make(map[model.PlanNodeKind]int)
	revisedPlans, addedEntities, revisedEntities := 0, 0, 0
	for _, patch := range proposal.Patches {
		ref := patch.Document
		if patch.Operation != model.PatchPut {
			view.Removed = append(view.Removed, describe(base, ref))
			continue
		}
		_, existed := base.Label(ref)
		switch ref.Kind {
		case model.DocumentManuscript:
		case model.DocumentPlan:
			var node model.PlanNode
			if err := json.Unmarshal(patch.Content, &node); err != nil {
				return ProposalView{}, fmt.Errorf("decode proposed plan node: %w", err)
			}
			plans = append(plans, node.ID)
			if existed {
				revisedPlans++
			} else {
				added[node.Kind]++
			}
		case model.DocumentEntity:
			entities = append(entities, ref.ID)
			if existed {
				revisedEntities++
			} else {
				addedEntities++
			}
		case model.DocumentCanon:
			var fact model.CanonFact
			if err := json.Unmarshal(patch.Content, &fact); err != nil {
				return ProposalView{}, fmt.Errorf("decode proposed fact: %w", err)
			}
			view.Facts = append(view.Facts, factOf(next, fact))
		case model.DocumentCompass:
			view.Compass = new(model.Compass)
			if err := json.Unmarshal(patch.Content, view.Compass); err != nil {
				return ProposalView{}, fmt.Errorf("decode proposed compass: %w", err)
			}
		default:
			view.Other = append(view.Other, describe(next, ref))
		}
	}
	view.Outline, view.Entities = next.Outline(plans), next.Entities(entities)

	var nodes []string
	for _, kind := range []model.PlanNodeKind{model.PlanVolume, model.PlanArc, model.PlanChapter} {
		if added[kind] > 0 {
			nodes = append(nodes, fmt.Sprintf("%d %s", added[kind], planNoun[kind]))
		}
	}
	summary := func(format string, count int) {
		if count > 0 {
			view.Summary = append(view.Summary, fmt.Sprintf(format, count))
		}
	}
	if len(nodes) > 0 {
		view.Summary = append(view.Summary, "新增 "+strings.Join(nodes, " · "))
	}
	summary("修订大纲 %d 处", revisedPlans)
	summary("新增人物地点 %d 个", addedEntities)
	summary("修订人物地点 %d 个", revisedEntities)
	summary("设定 %d 条", len(view.Facts))
	summary("删除 %d 项", len(view.Removed))
	summary("其他变更 %d 项", len(view.Other))
	return view, nil
}

var planNoun = map[model.PlanNodeKind]string{model.PlanVolume: "卷", model.PlanArc: "个故事弧", model.PlanChapter: "章"}

// documentNoun 是故事索引之外的文档种类名：它们没有章号或名称可指称。
var documentNoun = map[model.DocumentKind]string{
	model.DocumentIntent: "创作意图", model.DocumentCompass: "故事罗盘", model.DocumentOwnership: "锁定规则", model.DocumentApproval: "审批方式",
	model.DocumentOverlay: "书级创作规则", model.DocumentAssets: "启用的创作包", model.DocumentDirective: "创作要求",
	model.DocumentAdjudication: "裁决记录", model.DocumentAttachment: "附件",
}

// describe 用故事语言指称一份文档：故事里有的用章号、名称；其余用种类名，不露 ID。
func describe(story *narrative.Story, ref model.DocumentRef) string {
	if label, ok := story.Label(ref); ok {
		return label
	}
	if noun, ok := documentNoun[ref.Kind]; ok {
		return noun
	}
	return string(ref.Kind)
}

func storyOf(project projectdoc.Snapshot) *narrative.Story {
	return narrative.New(narrative.Content{Plan: project.Plan, Entities: project.Entities, Canon: project.Canon, Manuscript: project.Manuscript})
}
