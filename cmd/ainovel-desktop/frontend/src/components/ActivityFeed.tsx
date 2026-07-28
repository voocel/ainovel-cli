import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { EngineEvent } from "../bindings/wails";

// 分类图标沿用 TUI 的语义（DISPATCH 派发 / TOOL 工具 / DECISION 裁定 / …）。
function categoryIcon(ev: EngineEvent): string {
  switch (ev.category) {
    case "DISPATCH":
      return "▸";
    case "TOOL":
      return "├";
    case "DECISION":
      return ev.failed ? "✕" : "✓";
    case "ERROR":
      return "✕";
    case "USER":
      return "✎";
    case "CONTEXT":
    case "COMPACT":
      return "⚙";
    default:
      return "·";
  }
}

// RetryCountdown 按 retryAt 逐秒倒计时，到点即消失（请求已在途）。
function RetryCountdown({ retryAt }: { retryAt: string }) {
  const [left, setLeft] = useState(() => remain(retryAt));
  useEffect(() => {
    setLeft(remain(retryAt));
    const t = setInterval(() => setLeft(remain(retryAt)), 500);
    return () => clearInterval(t);
  }, [retryAt]);
  if (left <= 0) return null;
  return <span className="event-retry">{left}s 后重试</span>;
}

function remain(iso: string): number {
  const ms = new Date(iso).getTime() - Date.now();
  return ms > 0 ? Math.ceil(ms / 1000) : 0;
}

// ActivityFeed 事件流。滚到底部时自动跟随；用户向上滚则停止跟随（阅读历史不被打断）。
export function ActivityFeed({ events }: { events: EngineEvent[] }) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (el && followRef.current) el.scrollTop = el.scrollHeight;
  }, [events]);

  return (
    <section className="pane">
      <div className="pane-title">活动</div>
      <div className="pane-scroll" ref={scrollRef} onScroll={onScroll}>
        {events.length === 0 && <div className="subtle">等待引擎事件…</div>}
        {events.map((ev, i) => (
          <div
            key={ev.id || `i${i}`}
            className={`event lvl-${ev.level || "info"} ${ev.running ? "is-running" : ""}`}
            style={{ paddingLeft: `${ev.depth * 14}px` }}
          >
            <span className="event-icon">{categoryIcon(ev)}</span>
            {ev.agent && <span className="event-agent">{ev.agent}</span>}
            <span className="event-summary">{ev.summary}</span>
            {ev.retryAt && <RetryCountdown retryAt={ev.retryAt} />}
            {ev.running && <span className="event-spin" />}
            {ev.finishedAt && ev.durationMs > 0 && (
              <span className="event-dur">{formatDur(ev.durationMs)}</span>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}

function formatDur(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.floor(ms / 60_000)}m${Math.round((ms % 60_000) / 1000)}s`;
}
