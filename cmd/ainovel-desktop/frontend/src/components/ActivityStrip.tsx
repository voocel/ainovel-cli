import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { EngineEvent } from "../bindings/wails";

// ActivityStrip 引擎活动，默认收成一行。
//
// 事件流原先独占左边一整列。它讲的是"引擎内部在干什么"（派发了哪个子代理、
// 调了什么工具、裁定通过没有）——排查问题时很有用，但那是偶发需求，
// 不值一整列的常驻空间，而"这本书写到哪了"才是。
//
// 所以：常态一行（最新那条 + 在跑的角色），需要时展开成抽屉看完整历史。

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

function formatDur(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.floor(ms / 60_000)}m${Math.round((ms % 60_000) / 1000)}s`;
}

function EventRow({ ev }: { ev: EngineEvent }) {
  return (
    <div
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
  );
}

export function ActivityStrip({ events }: { events: EngineEvent[] }) {
  const [open, setOpen] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  useLayoutEffect(() => {
    if (!open) return;
    const el = scrollRef.current;
    if (el && followRef.current) el.scrollTop = el.scrollHeight;
  }, [events, open]);

  // 摘要行优先显示"正在跑"的那条——它是当下唯一还会变的信息；
  // 都跑完了就显示最后一条。
  const latest = [...events].reverse().find((e) => e.running) ?? events[events.length - 1];
  const errors = events.filter((e) => e.level === "error" || e.failed).length;

  return (
    <div className={`activity-strip ${open ? "open" : ""}`}>
      {open && (
        <div className="activity-log" ref={scrollRef} onScroll={onScroll}>
          {events.length === 0 && <div className="subtle sm">等待引擎事件…</div>}
          {events.map((ev, i) => (
            <EventRow key={ev.id || `i${i}`} ev={ev} />
          ))}
        </div>
      )}
      <button
        className="activity-summary"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        title={open ? "收起引擎活动" : "展开引擎活动明细"}
      >
        <span className="activity-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        {latest ? (
          <>
            <span className="event-icon">{categoryIcon(latest)}</span>
            {latest.agent && <span className="event-agent">{latest.agent}</span>}
            <span className="event-summary">{latest.summary}</span>
            {latest.running && <span className="event-spin" />}
          </>
        ) : (
          <span className="subtle">等待引擎事件…</span>
        )}
        <span className="activity-meta subtle sm">
          {errors > 0 && <span className="activity-errors">{errors} 个错误</span>}
          {events.length} 条
        </span>
      </button>
    </div>
  );
}
