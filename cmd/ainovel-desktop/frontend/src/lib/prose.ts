// 正文文本处理。阅读器和工作台正文视图共用同一套切分规则——
// 两处如果各写一份，早晚会在"空行算不算分段"这种细节上分叉。

// 引擎在思考文本段前插入 \x02（internal/utils.ThinkingSep）作为切换标记。
export const THINKING_SEP = "\x02";

// paragraphs 按空行分段。引擎落盘的是 Markdown，但章节正文实际只有段落，
// 所以不引入 Markdown 渲染器（也避免把小说里的 # * _ 当标记吃掉）。
export function paragraphs(text: string): string[] {
  return text
    .split(/\n\s*\n/)
    .map((p) => p.replace(/\n/g, "").trim())
    .filter((p) => p.length > 0);
}

// streamSegments 把一轮流式文本切成「思考 / 正文」交替的片段。
// 首段是正文（标记出现在思考段之前），此后交替。
export function streamSegments(text: string): { thinking: boolean; text: string }[] {
  return text
    .split(THINKING_SEP)
    .map((t, i) => ({ thinking: i % 2 === 1, text: t }))
    .filter((s) => s.text.length > 0);
}
