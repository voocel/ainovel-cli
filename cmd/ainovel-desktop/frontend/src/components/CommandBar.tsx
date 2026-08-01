import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { UISnapshot } from "../bindings/wails";
import { MoreMenu } from "./Menu";

// CommandBar 底部命令栏：干预/继续输入 + 暂停 + 逐章验收控件 + 完本后重开。
//
// 输入框的语义歧义是这里最需要小心的地方。同一个框在三种引擎状态下调三个不同的 API：
//   运行中 → Steer   实时注入干预
//   停机   → Continue 继续创作
//   完本   → Reopen   重开全书
// 原先只按**提交那一刻**的快照决定调哪个：用户在运行中开始打一句干预，打字期间
// 引擎写完停下，回车下去执行的却是 Continue——动作变了而用户毫不知情。
//
// 现在锁定**开始输入那一刻**的意图（intentRef），提交时若引擎状态已经漂移，
// 不静默改语义，而是拦下来让用户确认。
type Intent = "steer" | "continue" | "reopen";

function intentOf(snap: UISnapshot | null): Intent {
  if (snap?.RuntimeState === "completed") return "reopen";
  return snap?.IsRunning ? "steer" : "continue";
}

const INTENT_LABEL: Record<Intent, string> = {
  steer: "干预",
  continue: "继续",
  reopen: "重开",
};

const INTENT_PLACEHOLDER: Record<Intent, string> = {
  steer: "输入干预意见，回车实时注入创作…",
  continue: "输入内容继续创作…",
  reopen: "创作已完成 · 输入续写方向可重开本书…",
};

// 状态漂移时的说明文案：告诉用户"你刚才打算做的事"现在会变成什么。
function driftNote(from: Intent, to: Intent): string {
  if (from === "steer" && to === "continue")
    return "引擎在你输入期间已经停下。这句话将作为「继续创作」提交，而不是实时干预。";
  if (from === "steer" && to === "reopen")
    return "引擎在你输入期间已经完本。这句话将作为「重开本书」的续写方向提交。";
  if (from === "continue" && to === "steer")
    return "引擎在你输入期间已经开始运行。这句话将作为「实时干预」注入，而不是继续创作。";
  if (from === "continue" && to === "reopen")
    return "本书在你输入期间已经完本。这句话将作为「重开本书」的续写方向提交。";
  return `引擎状态已变化，这句话将按「${INTENT_LABEL[to]}」提交。`;
}

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
  onRip,
  onScan,
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
  onRip: () => void;
  onScan: () => void;
}) {
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  // 非空 = 提交时发现状态漂移，等用户确认要不要按新语义提交。
  const [drift, setDrift] = useState<{ from: Intent; to: Intent } | null>(null);
  // 用户开始打这段字时引擎处于什么状态。空输入时随引擎状态走。
  const intentRef = useRef<Intent>(intentOf(snap));
  const inputRef = useRef<HTMLInputElement>(null);

  const live = intentOf(snap);
  const running = snap?.IsRunning ?? false;
  const reviewMode = snap?.AdvanceMode === "review";
  const chapters = snap?.CompletedCount ?? 0;

  // 输入框空着的时候，意图跟随引擎实时状态——只有"已经开始打字"才锁定。
  useEffect(() => {
    if (!input.trim()) intentRef.current = live;
  }, [input, live]);

  const intent = input.trim() ? intentRef.current : live;

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

  const dispatch = (text: string, as: Intent) => {
    setInput("");
    setDrift(null);
    intentRef.current = live;
    void guard(() =>
      as === "reopen" ? api.Reopen(text) : as === "steer" ? api.Steer(text) : api.Continue(text),
    );
  };

  const submit = () => {
    const text = input.trim();
    if (!text) return;
    // 意图漂移：先确认，不静默换语义。
    if (intentRef.current !== live) {
      setDrift({ from: intentRef.current, to: live });
      return;
    }
    dispatch(text, live);
  };

  // 常用动作留在栏上，低频动作收进「更多」——之前 9 个按钮平铺，
  // 窗口一窄就溢出被裁，而且全都是同等视觉权重，找不到重点。
  const moreItems = [
    {
      label: "封面",
      onClick: onCover,
      disabled: busy,
      title: "用生图模型生成小说封面",
    },
    {
      label: "导出",
      onClick: onExport,
      disabled: busy,
      title: "导出已完成章节（运行中也可导出）",
    },
    {
      label: "仿写画像",
      onClick: onSimulate,
      disabled: busy || running,
      title: running ? "创作运行中，请先暂停" : "分析参考文章生成仿写画像",
    },
    {
      label: "专项技能",
      onClick: onSkills,
      disabled: busy,
      title: "调用专项技能处理已写内容（去 AI 味、收紧节奏等）",
    },
    {
      label: "拆文",
      onClick: onRip,
      disabled: busy || running,
      title: running ? "创作运行中，请先暂停" : "把一本对标小说拆成结构化分析（只读，产物落拆文库）",
    },
    {
      label: "扫榜",
      onClick: onScan,
      disabled: busy || running,
      title: running ? "创作运行中，请先暂停" : "把榜单数据整理成趋势报告与选题决策",
    },
  ];

  return (
    <footer className="command-bar">
      {drift && (
        <div className="drift-note">
          <span>{driftNote(drift.from, drift.to)}</span>
          <div className="inline-actions">
            <button
              className="ghost sm"
              onClick={() => {
                setDrift(null);
                inputRef.current?.focus();
              }}
            >
              先不提交
            </button>
            <button className="primary sm" onClick={() => dispatch(input.trim(), drift.to)}>
              按「{INTENT_LABEL[drift.to]}」提交
            </button>
          </div>
        </div>
      )}

      <div className="cmd-row">
        <input
          ref={inputRef}
          className="command-input"
          placeholder={INTENT_PLACEHOLDER[intent]}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
          }}
          disabled={busy}
        />
        <button className="primary" onClick={submit} disabled={busy || !input.trim()}>
          {INTENT_LABEL[intent]}
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
            title={running ? "创作运行中，无需放行" : "放行下一章（Ctrl+Enter）"}
          >
            放行下一章
          </button>
        )}
        <span className="cmd-sep" aria-hidden="true" />
        {/* 阅读放在最前：读自己写完的书是这里最常用的动作。
            已完成章数为 0 时置灰，免得点开一个空阅读器。 */}
        <button
          onClick={onRead}
          disabled={busy || chapters === 0}
          title={chapters === 0 ? "还没有已完成的章节" : "阅读已完成的章节（Ctrl+R）"}
        >
          阅读
        </button>
        <button onClick={onFoundation} disabled={busy} title="查看前提/大纲/人物/世界观，并可提修改意见">
          设定
        </button>
        <button
          onClick={onStageCoCreate}
          disabled={busy || live === "reopen"}
          title="暂停下来一起规划后续方向"
        >
          共创规划
        </button>
        <MoreMenu items={moreItems} />
      </div>
    </footer>
  );
}
