import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { BookContents, ChapterMeta, ChapterText } from "../bindings/wails";
import { formatNumber } from "../lib/labels";

// ReaderPanel 章节阅读器：读已完成的终稿。
//
// 数据源是 store 的 chapters/NN.md（与导出同一份），不是 engine:stream——
// 那条流是有损的，只能当"正在写什么"的预览。所以这里所见即成品。
//
// 阅读体验上刻意按"读书"而非"看日志"来设计：单栏定宽衬线正文、可调字号行距、
// 键盘翻页、进度记忆。目录与正文分栏，长篇按卷分组。

// 阅读偏好存 localStorage：换书、重开应用都该保持用户调好的字号。
const PREF_KEY = "ainovel.reader.prefs";
// 每本书的阅读位置各存一份，键里带书名。
const POS_KEY = (book: string) => `ainovel.reader.pos.${book}`;

type Prefs = { fontSize: number; lineHeight: number; width: number };
const DEFAULT_PREFS: Prefs = { fontSize: 18, lineHeight: 2, width: 38 };

function loadPrefs(): Prefs {
  try {
    const raw = localStorage.getItem(PREF_KEY);
    if (!raw) return DEFAULT_PREFS;
    const p = JSON.parse(raw) as Partial<Prefs>;
    return {
      fontSize: clamp(p.fontSize ?? DEFAULT_PREFS.fontSize, 14, 26),
      lineHeight: clamp(p.lineHeight ?? DEFAULT_PREFS.lineHeight, 1.5, 2.6),
      width: clamp(p.width ?? DEFAULT_PREFS.width, 28, 52),
    };
  } catch {
    return DEFAULT_PREFS;
  }
}

function clamp(v: number, lo: number, hi: number) {
  return Math.min(hi, Math.max(lo, v));
}

// 正文按空行分段。引擎落盘的是 Markdown，但章节正文实际只有段落，
// 所以不引入 Markdown 渲染器（也避免把小说里的 # * _ 当标记吃掉）。
function paragraphs(text: string): string[] {
  return text
    .split(/\n\s*\n/)
    .map((p) => p.replace(/\n/g, "").trim())
    .filter((p) => p.length > 0);
}

export function ReaderPanel({ onClose }: { onClose: () => void }) {
  const [contents, setContents] = useState<BookContents | null>(null);
  const [chapter, setChapter] = useState<ChapterText | null>(null);
  const [loading, setLoading] = useState(true);
  const [chapterLoading, setChapterLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [tocOpen, setTocOpen] = useState(true);
  const [prefs, setPrefs] = useState<Prefs>(loadPrefs);
  const [prefsOpen, setPrefsOpen] = useState(false);

  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    localStorage.setItem(PREF_KEY, JSON.stringify(prefs));
  }, [prefs]);

  const open = useCallback(async (ch: number, book: string) => {
    setChapterLoading(true);
    setErr(null);
    try {
      const c = await api.ReadChapter(ch);
      setChapter(c);
      if (book) localStorage.setItem(POS_KEY(book), String(ch));
      // 换章后回到顶部：沿用上一章的滚动位置会让人以为内容缺了一截。
      requestAnimationFrame(() => {
        if (scrollRef.current) scrollRef.current.scrollTop = 0;
      });
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setChapterLoading(false);
    }
  }, []);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const c = await api.GetContents();
        if (!alive) return;
        setContents(c);
        const list = c.chapters ?? [];
        if (list.length > 0) {
          // 优先回到上次读的那一章，失效则从第一章开始。
          const saved = Number(localStorage.getItem(POS_KEY(c.novelName)) ?? "");
          const target = list.some((x) => x.chapter === saved) ? saved : list[0].chapter;
          await open(target, c.novelName);
        }
      } catch (e) {
        if (alive) setErr(String((e as Error)?.message ?? e));
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, [open]);

  const list = contents?.chapters ?? [];
  const bookName = contents?.novelName ?? "";

  const go = useCallback(
    (ch: number) => {
      if (ch > 0 && !chapterLoading) void open(ch, bookName);
    },
    [bookName, chapterLoading, open],
  );

  // 键盘操作是"成熟阅读器"的基本要求：左右翻章，Esc 退出，空格/PgDn 翻屏由浏览器负责。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
        return;
      }
      // 输入框里按方向键不该翻章。
      const t = e.target as HTMLElement | null;
      if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
      if (e.key === "ArrowLeft" && chapter?.prevChapter) go(chapter.prevChapter);
      if (e.key === "ArrowRight" && chapter?.nextChapter) go(chapter.nextChapter);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [chapter, go, onClose]);

  // 目录按卷分组（仅长篇分层模式）。
  const grouped = useMemo(() => {
    // 卷映射不完整时保持后端给出的全局章号顺序，不能让 volume=0 单独成组后
    // 被排到第 1 卷前面。正常分组也按章节首次出现顺序展示，不按卷号二次重排。
    if (!contents?.layered || list.some((c) => c.volume <= 0)) return null;
    const groups = new Map<number, { title: string; items: ChapterMeta[] }>();
    for (const c of list) {
      const g = groups.get(c.volume) ?? { title: c.volumeTitle, items: [] };
      g.items.push(c);
      groups.set(c.volume, g);
    }
    return [...groups.entries()];
  }, [contents?.layered, list]);

  const paras = useMemo(() => (chapter ? paragraphs(chapter.text) : []), [chapter]);

  const tocItem = (c: ChapterMeta) => (
    <button
      key={c.chapter}
      className={`toc-item ${chapter?.chapter === c.chapter ? "active" : ""}`}
      onClick={() => go(c.chapter)}
    >
      <span className="toc-num">{c.chapter}</span>
      <span className="toc-title">{c.title || `第 ${c.chapter} 章`}</span>
      {c.words > 0 && <span className="toc-words subtle">{formatNumber(c.words)}</span>}
    </button>
  );

  return (
    <div className="reader-overlay">
      <div className="reader">
        <header className="reader-top">
          <button
            className="ghost sm"
            onClick={() => setTocOpen((v) => !v)}
            title={tocOpen ? "收起目录" : "展开目录"}
          >
            目录
          </button>
          <div className="reader-heading">
            <strong>{bookName || "阅读"}</strong>
            {list.length > 0 && (
              <span className="subtle sm">
                {list.length} 章 · {formatNumber(contents?.totalWords ?? 0)} 字
              </span>
            )}
          </div>
          <div className="reader-top-right">
            <button className="ghost sm" onClick={() => setPrefsOpen((v) => !v)} title="排版设置">
              Aa
            </button>
            <button className="ghost sm" onClick={onClose}>
              关闭
            </button>
          </div>
        </header>

        {prefsOpen && (
          <div className="reader-prefs">
            <label>
              字号 <span className="subtle sm">{prefs.fontSize}px</span>
              <input
                type="range"
                min={14}
                max={26}
                step={1}
                value={prefs.fontSize}
                onChange={(e) => setPrefs({ ...prefs, fontSize: Number(e.target.value) })}
              />
            </label>
            <label>
              行距 <span className="subtle sm">{prefs.lineHeight.toFixed(1)}</span>
              <input
                type="range"
                min={1.5}
                max={2.6}
                step={0.1}
                value={prefs.lineHeight}
                onChange={(e) => setPrefs({ ...prefs, lineHeight: Number(e.target.value) })}
              />
            </label>
            <label>
              版面宽度 <span className="subtle sm">{prefs.width}rem</span>
              <input
                type="range"
                min={28}
                max={52}
                step={1}
                value={prefs.width}
                onChange={(e) => setPrefs({ ...prefs, width: Number(e.target.value) })}
              />
            </label>
            <button className="ghost sm" onClick={() => setPrefs(DEFAULT_PREFS)}>
              恢复默认
            </button>
          </div>
        )}

        <div className="reader-body">
          {tocOpen && (
            <nav className="reader-toc">
              {loading && <div className="subtle sm">读取目录…</div>}
              {!loading && list.length === 0 && (
                <div className="subtle sm">还没有已完成的章节。</div>
              )}
              {grouped
                ? grouped.map(([vol, g]) => (
                    <div className="toc-group" key={vol}>
                      <div className="toc-group-title subtle">
                        第 {vol} 卷{g.title ? ` · ${g.title}` : ""}
                      </div>
                      {g.items.map(tocItem)}
                    </div>
                  ))
                : list.map(tocItem)}
            </nav>
          )}

          <main className="reader-main" ref={scrollRef}>
            {err && <div className="error-banner">{err}</div>}

            {!err && list.length === 0 && !loading && (
              <div className="reader-empty subtle">
                这本书还没有已完成的章节。
                <br />
                写完一章后就能在这里读到终稿。
              </div>
            )}

            {chapter && (
              <article
                className="reader-page"
                style={{
                  maxWidth: `${prefs.width}rem`,
                  fontSize: `${prefs.fontSize}px`,
                  lineHeight: prefs.lineHeight,
                }}
              >
                <h1 className="reader-chapter-title">
                  <span className="subtle sm">第 {chapter.chapter} 章</span>
                  {chapter.title && <span>{chapter.title}</span>}
                </h1>
                {paras.map((p, i) => (
                  <p key={i}>{p}</p>
                ))}

                <div className="reader-nav">
                  <button
                    className="ghost"
                    disabled={!chapter.prevChapter || chapterLoading}
                    onClick={() => go(chapter.prevChapter)}
                  >
                    ← 上一章
                  </button>
                  <span className="subtle sm">
                    {formatNumber(chapter.words)} 字
                    {chapterLoading && " · 载入中…"}
                  </span>
                  <button
                    className="ghost"
                    disabled={!chapter.nextChapter || chapterLoading}
                    onClick={() => go(chapter.nextChapter)}
                  >
                    下一章 →
                  </button>
                </div>
              </article>
            )}
          </main>
        </div>
      </div>
    </div>
  );
}
