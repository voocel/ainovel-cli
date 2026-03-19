package domain

import (
	"fmt"
	"unicode/utf8"
)

// ReviewInterval 全局审阅间隔（每 N 章触发一次）。
const ReviewInterval = 5

// ShouldReview 根据已完成章节数判断是否需要全局审阅（短篇/中篇模式）。
func ShouldReview(completedCount int) (bool, string) {
	if completedCount > 0 && completedCount%ReviewInterval == 0 {
		return true, fmt.Sprintf("已完成 %d 章，触发全局审阅", completedCount)
	}
	return false, ""
}

// ShouldArcReview 长篇模式下判断是否需要弧级/卷级评审。
func ShouldArcReview(isArcEnd, isVolumeEnd bool, volume, arc int) (bool, string) {
	if isVolumeEnd {
		return true, fmt.Sprintf("第 %d 卷第 %d 弧结束（卷结束），触发弧级+卷级评审", volume, arc)
	}
	if isArcEnd {
		return true, fmt.Sprintf("第 %d 卷第 %d 弧结束，触发弧级评审", volume, arc)
	}
	return false, ""
}

// WordCount 按 rune 计算字数。
func WordCount(content string) int {
	return utf8.RuneCountInString(content)
}
