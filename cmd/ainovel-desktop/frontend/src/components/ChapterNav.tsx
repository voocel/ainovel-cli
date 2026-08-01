import { useEffect, useMemo, useRef } from "react";
import type { BookContents, ChapterMeta, UISnapshot } from "../bindings/wails";
import { chapterState, formatNumber } from "../lib/labels";

// ChapterNav 左栏：整本书的章节导航。
//
// 这里原先是从 TUI 搬过来的事件流——DISPATCH / TOOL / DECISION 那套。终端里它是
// 唯一的进度反馈所以合理，但在桌面版它占着一整列，讲的却是"引擎内部在干什么"，
// 而不是"这本书写到哪了"。事件流降级成底部一行状态条（ActivityStrip），
// 这一列换成用户真正需要的东西：能点的目录。
//
// 已完成的章可点开读；未完成的来自大纲，显示但不可点。
export function ChapterNav({
  snap,
  contents,
  current,
  onSelect,
}: {
  snap: UISnapshot | null;
  contents: BookContents | null;
  /** 当前在主区展示的章节号。 */
  current: number;
  onSelect: (chapter: number) => void;
}) {
  const listRef = useRef<HTMLDivElement>(null);
  const done = useMemo(() => contents?.chapters ?? [], [contents]);
  const doneWords = useMemo(() => {
    const m = new Map<number, ChapterMeta>();
    for (const c of done) m.set(c.chapter, c);
    return m;
  }, [done]);

  // 合并"已完成章节"与"大纲里尚未写的章节"，得到全书视图。
  // 大纲章号可能与已完成章号重叠（已写的章大纲里也有），用 Map 去重，
  // 已完成的那份优先——它带真实字数和落盘标题。
  const rows = useMemo(() => {
    const m = new Map<number, { chapter: number; title: string; words: number; volume: number; volumeTitle: string }>();
    for (const e of snap?.Outline ?? []) {
      m.set(e.Chapter, {
        chapter: e.Chapter,
        title: e.Title,
        words: 0,
        volume: 0,
        volumeTitle: "",
      });
    }
    for (const c of done) {
      m.set(c.chapter, {
        chapter: c.chapter,
        title: c.title || m.get(c.chapter)?.title || "",
        words: c.words,
        volume: c.volume,
        volumeTitle: c.volumeTitle,
      });
    }
    return [...m.values()].sort((a, b) => a.chapter - b.chapter);
  }, [snap?.Outline, done]);

  // 正在写的那一章自动滚进视野：长篇写到几十章后，用户不该每次都手动去找。
  const active = snap?.InProgressChapter ?? 0;
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(".nav-item.active, .nav-item.current");
    el?.scrollIntoView({ block: "nearest" });
  }, [active, current]);

  // 分卷分组。只有已完成的章带卷号——大纲条目没有，所以未写的章统一归入尾部
  // 「后续」一组，不能让它们落进 volume=0 渲染成"第 0 卷"。
  // 卷号映射不完整时（已写的章也没卷号）干脆不分组。
  const grouped = useMemo(() => {
    if (!contents?.layered || done.length === 0) return null;
    if (done.some((c) => c.volume <= 0)) return null;
    const groups: { key: string; title: string; items: typeof rows }[] = [];
    for (const r of rows) {
      // 未落盘的章（大纲里有、还没写）没有卷号。
      const planned = !doneWords.has(r.chapter);
      const key = planned ? "planned" : `v${r.volume}`;
      const last = groups[groups.length - 1];
      if (last && last.key === key) last.items.push(r);
      else {
        groups.push({
          key,
          title: planned ? "后续" : `第 ${r.volume} 卷${r.volumeTitle ? ` · ${r.volumeTitle}` : ""}`,
          items: [r],
        });
      }
    }
    return groups;
  }, [contents?.layered, done, rows, doneWords]);

  const item = (r: (typeof rows)[number]) => {
    const st = snap ? chapterState(snap, r.chapter) : "pending";
    const readable = doneWords.has(r.chapter);
    return (
      <button
        key={r.chapter}
        className={`nav-item ${st} ${current === r.chapter ? "current" : ""}`}
        disabled={!readable}
        onClick={() => onSelect(r.chapter)}
        title={readable ? `读第 ${r.chapter} 章` : "尚未写到这一章"}
      >
        <span className="nav-marker">{st === "done" ? "●" : st === "active" ? "▸" : "○"}</span>
        <span className="nav-num">{r.chapter}</span>
        <span className="nav-title">{r.title || `第 ${r.chapter} 章`}</span>
        {r.words > 0 && <span className="nav-words subtle">{formatNumber(r.words)}</span>}
      </button>
    );
  };

  return (
    <nav className="pane chapter-nav">
      <div className="pane-title">
        目录
        {rows.length > 0 && (
          <span className="subtle sm">
            {done.length}/{rows.length}
          </span>
        )}
      </div>
      <div className="pane-scroll" ref={listRef}>
        {rows.length === 0 && (
          <div className="pane-empty subtle sm">大纲生成后，章节目录会显示在这里。</div>
        )}
        {grouped
          ? grouped.map((g, i) => (
              <div className="nav-group" key={`${g.key}-${i}`}>
                <div className="nav-group-title subtle">{g.title}</div>
                {g.items.map(item)}
              </div>
            ))
          : rows.map(item)}
        {snap?.Layered && rows.length > 0 && (
          <div className="nav-foot subtle sm">后续章节随创作推进自动规划</div>
        )}
      </div>
    </nav>
  );
}
