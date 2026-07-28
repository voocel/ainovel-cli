import { useEffect, useLayoutEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { CoCreateMsg } from "../bindings/wails";

// 界面上展示的一轮对话。assistant 轮同时保留 raw（回传给模型）与 message（给用户看）。
type Turn = { role: "user" | "assistant"; text: string; raw?: string };

// CoCreatePanel 共创面板：左侧多轮对话，右侧实时草稿。
//
// mode="new"   起新书：Ready 后「开始创作」→ StartFromCoCreate
// mode="stage" 阶段规划：Ready 后「应用并继续」→ ResumeFromCoCreate
//
// 关键：history 里 assistant 消息必须回传模型原始四段输出（raw），否则模型看不到
// 自己上一轮的 <draft>，会每轮重新归纳而非累积更新。
export function CoCreatePanel({
  mode,
  reviewFirst = true,
  onDone,
  onCancel,
}: {
  mode: "new" | "stage";
  // 仅 mode="new" 有意义：起书时的写作节奏（true=分段创作/逐章验收）。
  reviewFirst?: boolean;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState("");
  const [draft, setDraft] = useState("");
  const [ready, setReady] = useState(false);
  const [suggestions, setSuggestions] = useState<string[]>([]);
  const [streaming, setStreaming] = useState("");
  const [thinking, setThinking] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement>(null);

  // 订阅流式进度：thinking 单独展示，reply 作为“正在生成”的预览。
  useEffect(() => {
    const off = api.on("cocreate:progress", (p: { kind: string; text: string }) => {
      if (p.kind === "thinking") setThinking(p.text);
      else if (p.kind === "reply") setStreaming(p.text);
    });
    return () => {
      off();
      void api.CancelCoCreateTurn();
    };
  }, []);

  useLayoutEffect(() => {
    const el = chatRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [turns, streaming]);

  const send = async (text: string) => {
    const content = text.trim();
    if (!content || busy) return;

    // history 用 raw 回传 assistant 轮；user 轮用原文。
    const history: CoCreateMsg[] = [
      ...turns.map((t) => ({
        role: t.role,
        content: t.role === "assistant" ? (t.raw ?? t.text) : t.text,
      })),
      { role: "user" as const, content },
    ];

    setTurns((prev) => [...prev, { role: "user", text: content }]);
    setInput("");
    setSuggestions([]);
    setStreaming("");
    setThinking("");
    setBusy(true);
    setErr(null);

    try {
      const turn = mode === "stage" ? await api.StageCoCreate(history) : await api.CoCreate(history);
      setTurns((prev) => [
        ...prev,
        { role: "assistant", text: turn.message, raw: turn.raw },
      ]);
      if (turn.prompt) setDraft(turn.prompt);
      setReady(turn.ready);
      setSuggestions(turn.suggestions ?? []);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
      setStreaming("");
      setThinking("");
    }
  };

  const finish = async () => {
    if (!draft.trim() || busy) return;
    setBusy(true);
    setErr(null);
    try {
      if (mode === "stage") await api.ResumeFromCoCreate(draft);
      else await api.StartFromCoCreate(draft, reviewFirst);
      onDone();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  const cancel = async () => {
    void api.CancelCoCreateTurn();
    if (mode === "stage") {
      try {
        await api.CancelCoCreate();
      } catch {
        /* 放弃阶段共创失败不阻塞关闭面板 */
      }
    }
    onCancel();
  };

  const firstTurn = turns.length === 0;

  return (
    <div className="cocreate">
      <header className="topbar">
        <div className="topbar-left">
          <strong>{mode === "stage" ? "阶段共创 · 规划后续方向" : "共创 · 一起想清楚要写什么"}</strong>
          {ready && <span className="badge status-complete">已就绪</span>}
        </div>
        <div className="inline-actions">
          <button className="ghost" onClick={cancel} disabled={busy}>
            退出
          </button>
          <button className="primary" onClick={finish} disabled={busy || !draft.trim()}>
            {mode === "stage" ? "应用并继续创作" : "开始创作"}
          </button>
        </div>
      </header>

      <div className="cocreate-body">
        <section className="pane">
          <div className="pane-title">对话</div>
          <div className="pane-scroll" ref={chatRef}>
            {firstTurn && (
              <div className="cocreate-intro subtle">
                {mode === "stage"
                  ? "说说接下来想让故事怎么走，助手会结合已写内容一起梳理后续方向。"
                  : "先说一句你想写的故事，助手会通过几轮追问帮你把设定想清楚。"}
              </div>
            )}
            {turns.map((t, i) => (
              <div key={i} className={`chat-msg ${t.role}`}>
                <div className="chat-role">{t.role === "user" ? "你" : "助手"}</div>
                <div className="chat-text">{t.text}</div>
              </div>
            ))}
            {busy && (
              <div className="chat-msg assistant">
                <div className="chat-role">助手</div>
                {thinking && <div className="chat-thinking">{thinking}</div>}
                <div className="chat-text">
                  {streaming || <span className="subtle">正在思考…</span>}
                </div>
              </div>
            )}
          </div>

          {err && <div className="error-banner inline">{err}</div>}

          {suggestions.length > 0 && !busy && (
            <div className="suggestions">
              {suggestions.map((s, i) => (
                <button key={i} className="chip" onClick={() => setInput(s)}>
                  {s}
                </button>
              ))}
            </div>
          )}

          <div className="cocreate-input">
            <textarea
              className="prompt-input compact"
              placeholder={busy ? "生成中…" : "输入你的想法，Ctrl+Enter 发送"}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void send(input);
              }}
              rows={3}
              disabled={busy}
            />
            <button className="primary" onClick={() => void send(input)} disabled={busy || !input.trim()}>
              发送
            </button>
          </div>
        </section>

        <section className="pane draft-pane">
          <div className="pane-title">
            {mode === "stage" ? "后续方向草稿" : "创作指令草稿"}
            {ready && <span className="subtle sm">· 可以开始了</span>}
          </div>
          <div className="pane-scroll">
            {draft ? (
              <pre className="draft-text">{draft}</pre>
            ) : (
              <div className="subtle">草稿会随对话逐轮累积更新…</div>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
