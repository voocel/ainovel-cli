package imp

import "encoding/json"

func boundaryFixture(unitID, anchor, kind, title string) map[string]any {
	var anchorValue, titleValue any
	if anchor != "" {
		anchorValue = anchor
	}
	if title != "" {
		titleValue = title
	}
	return map[string]any{
		"unit_id": unitID, "anchor": anchorValue, "kind": kind, "title": titleValue,
		"uncertain": false, "reason": nil,
	}
}

func boundariesJSON(boundaries ...map[string]any) string {
	data, err := json.Marshal(map[string]any{"boundaries": boundaries})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func rangeDigestJSON(start, end int, plot string) string {
	data, err := json.Marshal(map[string]any{
		"start_chapter":    start,
		"end_chapter":      end,
		"plot":             plot,
		"characters":       []string{},
		"world_facts":      []string{},
		"opened_threads":   []string{},
		"resolved_threads": []string{},
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func synthesisFixtureJSON(endChapter int, status string) string {
	data, err := json.Marshal(map[string]any{
		"premise": "# 测试书\n前提",
		"characters": []any{map[string]any{
			"name": "甲", "aliases": []string{}, "role": "protagonist", "description": "d",
			"arc": "a", "traits": []string{"坚韧"}, "tier": nil,
		}},
		"world_rules": []any{},
		"structure": []any{map[string]any{
			"title": "卷一", "theme": "主题", "arcs": []any{map[string]any{
				"title": "弧一", "goal": "目标", "start_chapter": 1, "end_chapter": endChapter,
			}},
		}},
		"compass": map[string]any{
			"ending_direction": "终局", "open_threads": []string{},
			"estimated_scale": nil, "last_updated": nil,
		},
		"planning_tier": "short",
		"story_status":  status,
		"status_reason": "根据正文判断",
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}
