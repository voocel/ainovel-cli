import { useLayoutEffect, useRef } from "react";

// 引擎在思考文本段前插入 \x02（internal/utils.ThinkingSep）作为切换标记，
// 据此把一轮流式文本切成「思考 / 正文」交替的片段，分别渲染。
const THINKING_SEP = "\x02";

function segments(text: string): { thinking: boolean; text: string }[] {
  const parts = text.split(THINKING_SEP);
  // 首段是正文（标记出现在思考段之前），此后交替。
  return parts
    .map((t, i) => ({ thinking: i % 2 === 1, text: t }))
    .filter((s) => s.text.length > 0);
}

// ProsePane 实时输出：每个流式轮次一张卡片（engine:stream:clear 切分），
// 底部跟随滚动。这是预览视图——已提交的正文取自快照，不靠这里拼接。
export function ProsePane({ rounds, running }: { rounds: string[]; running: boolean }) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
  };

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (el && followRef.current) el.scrollTop = el.scrollHeight;
  }, [rounds]);

  const visible = rounds.filter((r) => r.trim().length > 0);

  return (
    <section className="pane prose-pane">
      <div className="pane-title">
        实时输出
        {running && <span className="live-dot" />}
      </div>
      <div className="pane-scroll" ref={scrollRef} onScroll={onScroll}>
        {visible.length === 0 && <div className="subtle">尚无流式内容…</div>}
        {visible.map((round, i) => (
          <div className="prose-round" key={i}>
            {segments(round).map((seg, j) =>
              seg.thinking ? (
                <div className="prose-thinking" key={j}>
                  {seg.text}
                </div>
              ) : (
                <div className="prose-text" key={j}>
                  {seg.text}
                </div>
              ),
            )}
          </div>
        ))}
      </div>
    </section>
  );
}
