import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { UISnapshot, VersionInfo } from "../bindings/wails";
import { formatCost, formatNumber, progressText, runtimeStateLabel } from "../lib/labels";
import { Overlay } from "./Overlay";

// TopBar 顶栏：书名 + 状态 + 进度 + 花费/预算 + 模型，右侧是书库/设置/关于入口。
//
// 收缩策略：窗口变窄时**先丢模型名、再丢字数**，书名和花费永远保留。
// 之前所有右侧元素都 flex-shrink:0，被挤没的反而是最左边的书名——
// 最该保留的信息最先消失。现在书名可省略号截断但不消失，次要指标按优先级隐藏。
export function TopBar({
  snap,
  onOpenSettings,
  onBackToLibrary,
}: {
  snap: UISnapshot | null;
  onOpenSettings: () => void;
  onBackToLibrary: () => void;
}) {
  const [about, setAbout] = useState<VersionInfo | null>(null);
  const [showAbout, setShowAbout] = useState(false);

  useEffect(() => {
    api.Version().then(setAbout).catch(() => {});
  }, []);

  const nav = (
    <>
      <button className="ghost sm" onClick={onBackToLibrary} title="返回书库切换其它书">
        书库
      </button>
      <button className="ghost sm" onClick={onOpenSettings}>
        设置
      </button>
      <button className="ghost sm" onClick={() => setShowAbout(true)} title="关于">
        ?
      </button>
    </>
  );

  if (!snap) {
    return (
      <header className="topbar">
        <div className="topbar-left">
          <strong>加载中…</strong>
        </div>
        <div className="topbar-right">{nav}</div>
      </header>
    );
  }

  const budget = snap.BudgetLimitUSD;
  const cost = snap.TotalCostUSD;
  const overBudget = budget > 0 && cost >= budget;
  const nearBudget = budget > 0 && !overBudget && cost >= budget * 0.8;

  return (
    <>
      <header className="topbar">
        <div className="topbar-left">
          <strong className="novel-name">{snap.NovelName || "未命名"}</strong>
          <span className={`badge status-${snap.StatusLabel.toLowerCase()}`}>
            {snap.StatusLabel}
          </span>
          <span className="subtle sm">{runtimeStateLabel(snap.RuntimeState)}</span>
        </div>

        <div className="topbar-right">
          <div className="metrics">
            <span className="metric">{progressText(snap)}</span>
            <span className="metric drop-md">{formatNumber(snap.TotalWordCount)} 字</span>
            <span
              className={`metric ${overBudget ? "danger" : nearBudget ? "warn" : ""}`}
              title={budget > 0 ? `预算上限 ${formatCost(budget)}` : "未设预算上限"}
            >
              {formatCost(cost)}
              {budget > 0 && <span className="subtle"> / {formatCost(budget)}</span>}
            </span>
            <span
              className="metric subtle drop-lg"
              title={`上下文窗口 ${snap.ModelContextWindow}`}
            >
              {snap.Provider}/{snap.ModelName}
              {snap.ThinkingLevel && ` · ${snap.ThinkingLevel}`}
            </span>
          </div>
          {nav}
        </div>
      </header>

      {showAbout && (
        <Overlay layer="sheet" onClose={() => setShowAbout(false)} labelledBy="about-title">
          <div className="modal narrow">
            <h2 id="about-title">AINovel Studio</h2>
            <p className="subtle sm">AI 小说创作工作台</p>
            <div className="field">
              <span className="field-label">版本</span>
              <span className="field-value">{about?.Version ?? "-"}</span>
            </div>
            <div className="field">
              <span className="field-label">提交</span>
              <span className="field-value">{about?.Commit ?? "-"}</span>
            </div>
            <div className="field">
              <span className="field-label">构建时间</span>
              <span className="field-value">{about?.Date ?? "-"}</span>
            </div>
            <div className="modal-actions">
              <button className="primary" onClick={() => setShowAbout(false)}>
                关闭
              </button>
            </div>
          </div>
        </Overlay>
      )}
    </>
  );
}
