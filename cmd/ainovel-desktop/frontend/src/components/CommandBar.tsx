import { useState } from "react";
import * as api from "../bindings/wails";
import type { UISnapshot } from "../bindings/wails";

// CommandBar 底部命令栏：干预/继续输入 + 暂停 + 逐章验收控件 + 完本后重开。
export function CommandBar({
  snap,
  onError,
  onStageCoCreate,
  onExport,
  onSimulate,
  onFoundation,
  onCover,
  onRead,
  onSkills,
}: {
  snap: UISnapshot | null;
  onError: (msg: string) => void;
  onStageCoCreate: () => void;
  onExport: () => void;
  onSimulate: () => void;
  onFoundation: () => void;
  onCover: () => void;
  onRead: () => void;
  onSkills: () => void;
}) {
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);

  const running = snap?.IsRunning ?? false;
  const completed = snap?.RuntimeState === "completed";
  const reviewMode = snap?.AdvanceMode === "review";
  const chapters = snap?.CompletedCount ?? 0;

  const guard = async (fn: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      onError(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const submit = () => {
    const text = input.trim();
    if (!text) return;
    setInput("");
    // 完本态下的输入按“重开并给方向”处理；运行中=实时干预；停机=继续。
    void guard(() =>
      completed ? api.Reopen(text) : running ? api.Steer(text) : api.Continue(text),
    );
  };

  const placeholder = completed
    ? "创作已完成 · 输入续写方向可重开本书…"
    : running
      ? "输入干预意见，回车实时注入创作…"
      : "输入内容继续创作…";

  return (
    <footer className="command-bar">
      <div className="cmd-row">
        <input
          className="command-input"
          placeholder={placeholder}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
          }}
          disabled={busy}
        />
        <button className="primary" onClick={submit} disabled={busy || !input.trim()}>
          {completed ? "重开" : running ? "干预" : "继续"}
        </button>
        {running && (
          <button className="ghost" onClick={() => void guard(api.Abort)} disabled={busy}>
            暂停
          </button>
        )}
      </div>

      <div className="cmd-row secondary">
        <label className="switch">
          <input
            type="checkbox"
            checked={reviewMode}
            disabled={busy}
            onChange={(e) =>
              void guard(() => api.SetAdvanceMode(e.target.checked ? "review" : "auto"))
            }
          />
          <span>逐章验收</span>
        </label>
        {reviewMode && (
          <button
            onClick={() => void guard(api.AdvanceOneChapter)}
            disabled={busy || running}
            title={running ? "创作运行中，无需放行" : "放行下一章"}
          >
            放行下一章
          </button>
        )}
        {/* 阅读放在这一排最前：读自己写完的书是这里最常用的动作。
            已完成章数为 0 时置灰，免得点开一个空阅读器。 */}
        <button
          onClick={onRead}
          disabled={busy || chapters === 0}
          title={chapters === 0 ? "还没有已完成的章节" : "阅读已完成的章节"}
        >
          阅读
        </button>
        <button onClick={onFoundation} disabled={busy} title="查看前提/大纲/人物/世界观，并可提修改意见">
          设定
        </button>
        <button onClick={onStageCoCreate} disabled={busy || completed} title="暂停下来一起规划后续方向">
          共创规划
        </button>
        {/* 技能与「设定」并列：两者都是"挑一件事让 AI 去做"，而非查看类动作。
            运行中也可用——技能经干预通道注入，Steer 会即时送达。 */}
        <button onClick={onSkills} disabled={busy} title="调用专项技能处理已写内容（去 AI 味、收紧节奏等）">
          技能
        </button>
        <button onClick={onCover} disabled={busy} title="用生图模型生成小说封面">
          封面
        </button>
        <button onClick={onExport} disabled={busy} title="导出已完成章节（运行中也可导出）">
          导出
        </button>
        <button
          onClick={onSimulate}
          disabled={busy || running}
          title={running ? "创作运行中，请先暂停" : "分析参考文章生成仿写画像"}
        >
          仿写画像
        </button>
      </div>
    </footer>
  );
}
