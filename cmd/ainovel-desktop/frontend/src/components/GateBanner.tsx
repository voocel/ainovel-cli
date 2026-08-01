import type { UISnapshot } from "../bindings/wails";

// gateWaiting：引擎是否正停在验收点等用户放行。
// 仅在逐章验收、引擎已停、且处于写作期且无在途许可时才成立。
//
// 导出是因为调用方需要**先知道横幅会不会出现**才能决定要不要给它留位置——
// 让调用方自己抄一遍这个条件，早晚会和这里分叉。
export function gateWaiting(snap: UISnapshot | null): boolean {
  return (
    !!snap &&
    snap.AdvanceMode === "review" &&
    !snap.IsRunning &&
    snap.Phase === "writing" &&
    snap.AdvancePermitChapter === 0
  );
}

// GateBanner 是"引擎正等你"的显式提示条。
//
// 引擎在逐章验收模式下会停在章节边界（规划完成后是第一次停），但停下这件事
// 原先只体现为活动流里的一行事件——很容易被当成"卡住了"或根本没注意到。
// 这条横幅把它变成主界面上不可能忽略的状态 + 一个明确的下一步动作。
export function GateBanner({
  snap,
  onReviewFoundation,
  onAdvance,
  busy,
}: {
  snap: UISnapshot;
  onReviewFoundation: () => void;
  onAdvance: () => void;
  busy: boolean;
}) {
  if (!gateWaiting(snap)) return null;

  // 尚无完成章节 = 这是规划后的第一次停顿，重点引导去审阅设定。
  const firstStop = snap.CompletedCount === 0;
  const next = snap.CurrentChapter > 0 ? snap.CurrentChapter : snap.CompletedCount + 1;

  return (
    <div className="gate-strip">
      <div className="gate-text">
        <strong>{firstStop ? "规划完成，等你确认" : `第 ${snap.CompletedCount} 章已完成`}</strong>
        <span className="subtle">
          {firstStop
            ? "前提、大纲、人物和世界观都已就绪。确认后开始写正文。"
            : `审阅无误后放行第 ${next} 章继续。`}
        </span>
      </div>
      <div className="inline-actions">
        <button onClick={onReviewFoundation} disabled={busy}>
          {firstStop ? "审阅设定" : "查看设定"}
        </button>
        <button className="primary" onClick={onAdvance} disabled={busy}>
          {firstStop ? `开始写第 ${next} 章` : `放行第 ${next} 章`}
        </button>
      </div>
    </div>
  );
}
