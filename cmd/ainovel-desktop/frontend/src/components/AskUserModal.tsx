import { useState } from "react";
import * as api from "../bindings/wails";
import type { AskQuestion } from "../bindings/wails";
import { Overlay } from "./Overlay";

// 每题的作答状态：选中的 label 集合 + 自由补充。
type Answer = { selected: string[]; note: string };

// AskUserModal 引擎中途提问的弹窗。引擎在此期间是**阻塞**的，所以：
//   - 必须让用户能明确提交或跳过（跳过=提交空答案，引擎按“未提供回答”自行决策）
//   - 关闭时一定要回传，否则引擎会一直等
// 答案以问题全文为 key 提交（与 Go 侧 tools.formatAnswers 的取值方式一致）。
export function AskUserModal({
  id,
  questions,
  onClose,
}: {
  id: string;
  questions: AskQuestion[];
  onClose: () => void;
}) {
  const [answers, setAnswers] = useState<Record<string, Answer>>(() =>
    Object.fromEntries(questions.map((q) => [q.question, { selected: [], note: "" }])),
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const patch = (q: string, p: Partial<Answer>) =>
    setAnswers((prev) => ({ ...prev, [q]: { ...prev[q], ...p } }));

  const toggle = (q: AskQuestion, label: string) => {
    const cur = answers[q.question]?.selected ?? [];
    if (q.multiSelect) {
      patch(q.question, {
        selected: cur.includes(label) ? cur.filter((x) => x !== label) : [...cur, label],
      });
    } else {
      patch(q.question, { selected: [label] });
    }
  };

  const submit = async (skip: boolean) => {
    if (busy) return;
    setBusy(true);
    setErr(null);
    const payloadAnswers: Record<string, string> = {};
    const payloadNotes: Record<string, string> = {};
    if (!skip) {
      for (const q of questions) {
        const a = answers[q.question];
        if (!a) continue;
        if (a.selected.length > 0) payloadAnswers[q.question] = a.selected.join("、");
        if (a.note.trim()) {
          payloadNotes[q.question] = a.note.trim();
          // 只填了补充没选项时，把补充本身当作答案，避免这题被视为未回答。
          if (a.selected.length === 0) payloadAnswers[q.question] = a.note.trim();
        }
      }
    }
    try {
      await api.AnswerAskUser(id, payloadAnswers, payloadNotes);
      onClose();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  const answeredAll = questions.every((q) => {
    const a = answers[q.question];
    return (a?.selected.length ?? 0) > 0 || !!a?.note.trim();
  });

  return (
    // blocking 层：引擎此刻停在等待里，这个弹窗必须压过设置页和阅读器。
    // 不给 onClose——Esc / 点空白都不能关，只能走「提交」或「跳过」，
    // 否则引擎会一直等一个永远不来的回答。
    <Overlay layer="blocking" labelledBy="ask-title">
      <div className="modal">
        <h2 id="ask-title">创作引擎需要你确认</h2>
        <p className="subtle sm">创作已暂停等待你的回答。也可以跳过，让引擎自行判断。</p>

        {questions.map((q) => (
          <div className="ask-question" key={q.question}>
            <div className="ask-header">
              <span className="tag">{q.header}</span>
              {q.multiSelect && <span className="subtle sm">可多选</span>}
            </div>
            <div className="ask-text">{q.question}</div>
            <div className="ask-options">
              {q.options.map((o) => {
                const on = (answers[q.question]?.selected ?? []).includes(o.label);
                return (
                  <button
                    key={o.label}
                    className={`ask-option ${on ? "active" : ""}`}
                    onClick={() => toggle(q, o.label)}
                    disabled={busy}
                  >
                    <span className="ask-option-label">{o.label}</span>
                    <span className="ask-option-desc subtle sm">{o.description}</span>
                  </button>
                );
              })}
            </div>
            <input
              className="text-input"
              placeholder="其他 / 补充说明（可选）"
              value={answers[q.question]?.note ?? ""}
              onChange={(e) => patch(q.question, { note: e.target.value })}
              disabled={busy}
            />
          </div>
        ))}

        {err && <div className="error-banner">{err}</div>}

        <div className="modal-actions">
          <button className="ghost" onClick={() => void submit(true)} disabled={busy}>
            跳过
          </button>
          <button className="primary" onClick={() => void submit(false)} disabled={busy || !answeredAll}>
            {busy ? "提交中…" : "提交回答"}
          </button>
        </div>
      </div>
    </Overlay>
  );
}
