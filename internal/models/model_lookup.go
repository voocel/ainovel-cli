package models

import "strings"

// SameModelID 判断两个模型标识是否指向同一个规范模型（忽略日期后缀、大小写、点/横线差异）。
func SameModelID(a, b string) bool {
	return modelLookupMatches(normalizeModelLookupID(a), normalizeModelLookupID(b))
}

func lookupModelEntry(models []ModelEntry, providerName, modelID string) (ModelEntry, bool) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	targetID := normalizeModelLookupID(modelID)
	for _, m := range models {
		if providerName != "" && !strings.EqualFold(m.Provider, providerName) {
			continue
		}
		if modelLookupMatches(normalizeModelLookupID(m.ID), targetID) {
			return m, true
		}
	}
	return ModelEntry{}, false
}

func normalizeModelLookupID(modelID string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return strings.ReplaceAll(modelID, ".", "-")
}

// modelLookupMatches 精确匹配或带日期后缀的匹配。
// e.g. "claude-sonnet-4" 匹配 "claude-sonnet-4-20250514"。
func modelLookupMatches(knownID, targetID string) bool {
	if knownID == targetID {
		return true
	}
	if strings.HasPrefix(targetID, knownID) && isDatedModelSuffix(targetID[len(knownID):]) {
		return true
	}
	if strings.HasPrefix(knownID, targetID) && isDatedModelSuffix(knownID[len(targetID):]) {
		return true
	}
	return false
}

// isDatedModelSuffix 判断字符串是否形如 "-20250514"（连字符 + 8 位数字）。
func isDatedModelSuffix(s string) bool {
	if len(s) != 9 || s[0] != '-' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func hasDatedSuffix(id string) bool {
	if len(id) < 9 {
		return false
	}
	return isDatedModelSuffix(id[len(id)-9:])
}
