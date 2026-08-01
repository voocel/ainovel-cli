import { useEffect, useLayoutEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { ChapterText, UISnapshot } from "../bindings/wails";
import { Segmented } from "./Menu";
import { formatNumber } from "../lib/labels";
import { paragraphs, streamSegments } from "../lib/prose";

// MainPane 是工作台主区，两个视图二选一：
//
//   正文     已完成章节的终稿（取自 store 的 chapters/NN.md，与导出同源）
//   实时输出 当前流式轮次的预览（engine:stream，有损）
//
// 原先这里**只有**实时输出。问题是引擎一停，流就没了——而逐章验收模式下
// 每个章节边界引擎都会停，也就是每次用户真正需要看内容做决定的时刻，
// 最大的那块面板恰好是空的，成品正文却锁在全屏阅读器里。这里把它反过来。
type View = "prose" | "stream";

export function MainPane({
  snap,
  rounds,
  chapter,
  onOpenReader,
}: {
  snap: UISnapshot | null;
  rounds: string[];
  /** 要展示的章节号；0 表示还没有已完成章节。 */
  chapter: number;
  onOpenReader: () => void;
}) {
  const running = snap?.IsRunning ?? false;
  // 用户手动切过视图之后就固定下来，不再跟着引擎起停跳——
  // 正读着一段话被切走是很烦的事。没切过时按引擎状态自动选。
  const [pinned, setPinned] = useState<View | null>(null);
  const view: View = pinned ?? (running || chapter === 0 ? "stream" : "prose");

  return (
    <section className="pane main-pane">
      <div className="pane-title">
        <Segmented<View>
          value={view}
          onChange={setPinned}
          ariaLabel="主区视图"
          options={[
            { key: "prose", label: "正文", title: "已完成章节的终稿" },
            {
              key: "stream",
              label: (
                <>
                  实时输出
                  {running && view !== "stream" && <span className="live-dot inline" />}
                </>
              ),
              title: "当前正在生成的内容（预览）",
            },
          ]}
        />
        {view === "prose" && chapter > 0 && (
          <button className="ghost sm pane-title-action" onClick={onOpenReader}>
            全屏阅读
          </button>
        )}
        {view === "stream" && running && <span className="live-dot" />}
      </div>

      {view === "prose" ? (
        <ChapterView chapter={chapter} />
      ) : (
        <StreamView rounds={rounds} />
      )}
    </section>
  );
}

// ChapterView 读一章终稿。章号变了才重新拉，正在写的那章不在这里（它还没落盘）。
function ChapterView({ chapter }: { chapter: number }) {
  const [text, setText] = useState<ChapterText | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (chapter <= 0) {
      setText(null);
      return;
    }
    let alive = true;
    setLoading(true);
    setErr(null);
    api
      .ReadChapter(chapter)
      .then((c) => {
        if (!alive) return;
        setText(c);
        // 换章回到顶部：沿用上一章的滚动位置会让人以为内容缺了一截。
        if (scrollRef.current) scrollRef.current.scrollTop = 0;
      })
      .catch((e) => alive && setErr(String((e as Error)?.message ?? e)))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [chapter]);

  if (chapter <= 0) {
    return (
      <div className="pane-scroll">
        <div className="pane-empty subtle">
          还没有已完成的章节。
          <br />
          写完第一章后，终稿会显示在这里。
        </div>
      </div>
    );
  }

  return (
    <div className="pane-scroll" ref={scrollRef}>
      {err && <div className="error-banner">{err}</div>}
      {!err && !text && loading && <div className="pane-empty subtle">读取中…</div>}
      {text && (
        <article className="prose-round chapter-view">
          <h1 className="reader-chapter-title">
            <span className="subtle sm">第 {text.chapter} 章</span>
            {text.title && <span>{text.title}</span>}
          </h1>
          {paragraphs(text.text).map((p, i) => (
            <p key={i}>{p}</p>
          ))}
          <div className="chapter-foot subtle sm">{formatNumber(text.words)} 字</div>
        </article>
      )}
    </div>
  );
}

// StreamView 实时输出：每个流式轮次一张卡片（engine:stream:clear 切分），底部跟随滚动。
function StreamView({ rounds }: { rounds: string[] }) {
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
    <div className="pane-scroll" ref={scrollRef} onScroll={onScroll}>
      {visible.length === 0 && (
        <div className="pane-empty subtle">
          引擎当前没有输出。
          <br />
          开始创作后，正在生成的内容会实时显示在这里。
        </div>
      )}
      {visible.map((round, i) => (
        <div className="prose-round" key={i}>
          {streamSegments(round).map((seg, j) =>
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
  );
}
