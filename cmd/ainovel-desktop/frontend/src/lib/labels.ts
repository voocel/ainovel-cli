import type { UISnapshot } from "../bindings/wails";

// 这些标签与 internal/entry/tui/panels_sidebar.go 的映射保持一致，避免桌面版和终端版
// 对同一状态给出不同措辞。

export function runtimeStateLabel(state: string): string {
  switch (state) {
    case "running":
      return "运行中";
    case "pausing":
      return "暂停中";
    case "paused":
      return "已暂停";
    case "completed":
      return "已完成";
    default:
      return "空闲";
  }
}

export function phaseLabel(phase: string): string {
  switch (phase) {
    case "init":
      return "初始化";
    case "premise":
      return "前提";
    case "outline":
      return "大纲";
    case "writing":
      return "写作";
    case "complete":
      return "完成";
    default:
      return phase || "-";
  }
}

export function flowLabel(flow: string): string {
  switch (flow) {
    case "writing":
      return "写作";
    case "reviewing":
      return "评审";
    case "rewriting":
      return "重写";
    case "polishing":
      return "打磨";
    case "steering":
      return "干预";
    default:
      return flow || "-";
  }
}

// inProgressDisplay 复刻 TUI 的同名逻辑：打磨/重写流程下，若 InProgressChapter 不在
// 返工队列里，则回退到队列首项；正常写作则直接用 InProgressChapter。
export function inProgressDisplay(snap: UISnapshot): { label: string; chapter: number } | null {
  let ch = snap.InProgressChapter;
  const pending = snap.PendingRewrites ?? [];
  const fallback = (name: string) => {
    if (ch <= 0 || !pending.includes(ch)) {
      if (pending.length === 0) return null;
      ch = pending[0];
    }
    return { label: name, chapter: ch };
  };
  switch (snap.Flow) {
    case "polishing":
      return fallback("打磨中");
    case "rewriting":
      return fallback("重写中");
    default:
      return ch > 0 ? { label: "写作中", chapter: ch } : null;
  }
}

// headline 是“当前在等什么”的一句话概括（与 TUI snapshotHeadline 一致）。
export function headline(snap: UISnapshot): string {
  if (snap.PendingSteer) {
    return snap.IsRunning ? "等待处理用户干预" : "待恢复：处理用户干预";
  }
  if ((snap.PendingRewrites ?? []).length > 0) {
    return snap.IsRunning ? "等待返工处理" : "待恢复：返工处理";
  }
  if (snap.AdvanceMode === "review" && !snap.IsRunning && snap.Phase === "writing") {
    return "逐章验收：等待放行下一章";
  }
  return "";
}

// progressText 处理分层模式的口径差异：分层动态规划下不能暴露 TotalChapters
// （那是骨架弧粗估，会和可见大纲对不上），改用“已完成 / 已规划(=大纲条数)”。
export function progressText(snap: UISnapshot): string {
  const planned = (snap.Outline ?? []).length;
  if (snap.Layered) {
    return planned > 0
      ? `已完成 ${snap.CompletedCount} 章 · 已规划 ${planned} 章`
      : `已完成 ${snap.CompletedCount} 章`;
  }
  if (snap.TotalChapters > 0) {
    return `${snap.CompletedCount} / ${snap.TotalChapters} 章`;
  }
  return `已完成 ${snap.CompletedCount} 章`;
}

export type ChapterState = "done" | "active" | "pending";

// chapterState 复刻 TUI 的章节标记规则。
export function chapterState(snap: UISnapshot, chapter: number): ChapterState {
  if (snap.CompletedCount >= chapter) return "done";
  if (snap.InProgressChapter === chapter) return "active";
  return "pending";
}

export function formatNumber(n: number): string {
  return n.toLocaleString("zh-CN");
}

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

export function formatCost(usd: number): string {
  if (usd <= 0) return "$0.00";
  if (usd < 0.01) return "<$0.01";
  return `$${usd.toFixed(2)}`;
}

// cacheHitRate 用滑动窗优先（更能反映稳态），无样本时回退累计。
export function cacheHitRate(snap: UISnapshot): number | null {
  if (!snap.OverallCacheCapable) return null;
  if (snap.OverallRecentSamples > 0 && snap.OverallRecentInput > 0) {
    return snap.OverallRecentCacheRead / snap.OverallRecentInput;
  }
  if (snap.TotalInputTokens > 0) {
    return snap.TotalCacheReadTokens / snap.TotalInputTokens;
  }
  return null;
}
