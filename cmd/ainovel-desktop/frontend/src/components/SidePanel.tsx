import { useState } from "react";
import type { UISnapshot } from "../bindings/wails";
import {
  cacheHitRate,
  chapterState,
  flowLabel,
  formatCost,
  formatTokens,
  headline,
  inProgressDisplay,
  phaseLabel,
  runtimeStateLabel,
} from "../lib/labels";

type Tab = "status" | "outline" | "cast" | "usage";

// SidePanel 右侧信息面板，分四个 tab：状态 / 大纲 / 角色 / 用量。
// 相比终端版把所有内容挤在一列，这里分页承载，每页信息更完整。
export function SidePanel({ snap }: { snap: UISnapshot | null }) {
  const [tab, setTab] = useState<Tab>("status");
  if (!snap) return <aside className="side-panel" />;

  return (
    <aside className="side-panel">
      <nav className="tabs">
        {(
          [
            ["status", "状态"],
            ["outline", "大纲"],
            ["cast", "角色"],
            ["usage", "用量"],
          ] as [Tab, string][]
        ).map(([key, label]) => (
          <button
            key={key}
            className={`tab ${tab === key ? "active" : ""}`}
            onClick={() => setTab(key)}
          >
            {label}
          </button>
        ))}
      </nav>
      <div className="side-scroll">
        {tab === "status" && <StatusTab snap={snap} />}
        {tab === "outline" && <OutlineTab snap={snap} />}
        {tab === "cast" && <CastTab snap={snap} />}
        {tab === "usage" && <UsageTab snap={snap} />}
      </div>
    </aside>
  );
}

function Field({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="field">
      <span className="field-label">{label}</span>
      <span className={`field-value ${highlight ? "highlight" : ""}`}>{value}</span>
    </div>
  );
}

function StatusTab({ snap }: { snap: UISnapshot }) {
  const progress = inProgressDisplay(snap);
  const head = headline(snap);
  const rewrites = snap.PendingRewrites ?? [];
  const agents = (snap.Agents ?? []).filter((a) => a.State === "running");
  const idle = (snap.Agents ?? []).filter((a) => a.State !== "running").map((a) => a.Name);

  return (
    <>
      {snap.RecoveryLabel && <div className="note">{snap.RecoveryLabel}</div>}

      <h3>概览</h3>
      <Field label="运行态" value={runtimeStateLabel(snap.RuntimeState)} />
      <Field label="阶段" value={phaseLabel(snap.Phase)} />
      <Field label="流程" value={flowLabel(snap.Flow)} />
      <Field
        label="推进"
        value={
          snap.AdvanceMode === "review"
            ? snap.AdvancePermitChapter > 0
              ? `已放行第 ${snap.AdvancePermitChapter} 章`
              : "逐章验收"
            : "自动"
        }
      />
      {progress && <Field label={progress.label} value={`第 ${progress.chapter} 章`} highlight />}
      {snap.Layered && snap.CurrentVolumeArc && (
        <Field label="位置" value={snap.CurrentVolumeArc} />
      )}
      {head && <div className="note accent">{head}</div>}

      {agents.length > 0 && (
        <>
          <h3>运行角色</h3>
          {agents.map((a) => (
            <div className="agent-row" key={a.Name}>
              <span className="agent-dot" />
              <span className="agent-name">{a.Name}</span>
              <span className="agent-detail subtle">{a.Tool || a.Summary}</span>
              {a.Context.Percent > 0 && (
                <span
                  className={`ctx-pill ${
                    a.Context.Percent > 0.85 ? "danger" : a.Context.Percent > 0.7 ? "warn" : ""
                  }`}
                >
                  {Math.round(a.Context.Percent * 100)}%
                </span>
              )}
            </div>
          ))}
          {idle.length > 0 && <div className="subtle sm">待命：{idle.join(" · ")}</div>}
        </>
      )}

      {rewrites.length > 0 && (
        <>
          <h3>返工</h3>
          <Field label="队列" value={rewrites.join(", ")} highlight />
          {snap.RewriteReason && <div className="note">{snap.RewriteReason}</div>}
        </>
      )}

      {snap.PendingSteer && (
        <>
          <h3>待处理干预</h3>
          <div className="note accent">{snap.PendingSteer}</div>
        </>
      )}

      {snap.HasAdvanceHold && (
        <>
          <h3>验收停靠</h3>
          <div className="note accent">{snap.AdvanceHoldReason}</div>
        </>
      )}

      {(snap.LastCommitSummary || snap.LastReviewSummary) && <h3>最近</h3>}
      {snap.LastCommitSummary && <Field label="提交" value={snap.LastCommitSummary} />}
      {snap.LastReviewSummary && <Field label="审阅" value={snap.LastReviewSummary} />}
      {snap.LastCheckpointName && <Field label="检查点" value={snap.LastCheckpointName} />}
    </>
  );
}

function OutlineTab({ snap }: { snap: UISnapshot }) {
  const outline = snap.Outline ?? [];
  if (outline.length === 0) return <div className="subtle">大纲尚未生成…</div>;

  return (
    <>
      <h3>
        大纲
        {snap.Layered && <span className="subtle sm"> · 动态规划</span>}
      </h3>
      <ol className="outline-list">
        {outline.map((e) => {
          const st = chapterState(snap, e.Chapter);
          return (
            <li className={`outline-item ${st}`} key={e.Chapter}>
              <span className="outline-marker">
                {st === "done" ? "●" : st === "active" ? "▸" : "○"}
              </span>
              <span className="outline-num">{e.Chapter}</span>
              <span className="outline-title" title={e.CoreEvent}>
                {e.Title}
              </span>
              {st === "active" && <span className="outline-tag">进行中</span>}
            </li>
          );
        })}
      </ol>
      {snap.Layered && (
        <div className="subtle sm outline-foot">
          {snap.NextVolumeTitle && <div>下一卷：{snap.NextVolumeTitle}</div>}
          <div>后续章节随创作推进自动生成</div>
          {snap.CompassDirection && (
            <div>
              终局：{snap.CompassDirection}
              {snap.CompassScale && `（${snap.CompassScale}）`}
            </div>
          )}
        </div>
      )}
    </>
  );
}

function CastTab({ snap }: { snap: UISnapshot }) {
  const chars = snap.Characters ?? [];
  const recent = snap.RecentSupporting ?? [];
  if (chars.length === 0 && !snap.Premise) return <div className="subtle">角色尚未生成…</div>;

  return (
    <>
      {chars.length > 0 && (
        <>
          <h3>主要角色</h3>
          <ul className="plain-list">
            {chars.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
        </>
      )}
      {snap.SupportingCount > 0 && (
        <>
          <h3>配角生态</h3>
          <Field label="已出场" value={`${snap.SupportingCount} 位`} />
          <ul className="plain-list">
            {recent.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        </>
      )}
      {snap.Premise && (
        <>
          <h3>前提</h3>
          <p className="premise">{snap.Premise}</p>
        </>
      )}
    </>
  );
}

function UsageTab({ snap }: { snap: UISnapshot }) {
  const hit = cacheHitRate(snap);
  const budget = snap.BudgetLimitUSD;
  const pct = budget > 0 ? Math.min(1, snap.TotalCostUSD / budget) : 0;
  const perModel = snap.CachePerModel ?? [];

  return (
    <>
      <h3>累计用量</h3>
      <Field label="输入" value={formatTokens(snap.TotalInputTokens)} />
      <Field label="输出" value={formatTokens(snap.TotalOutputTokens)} />
      <Field label="花费" value={formatCost(snap.TotalCostUSD)} highlight />
      {snap.TotalSavedUSD > 0 && (
        <Field label="缓存省下" value={formatCost(snap.TotalSavedUSD)} />
      )}

      {budget > 0 && (
        <>
          <h3>预算</h3>
          <div className="budget-bar">
            <div
              className={`budget-fill ${pct >= 1 ? "danger" : pct >= 0.8 ? "warn" : ""}`}
              style={{ width: `${pct * 100}%` }}
            />
          </div>
          <div className="subtle sm">
            {formatCost(snap.TotalCostUSD)} / {formatCost(budget)}
          </div>
        </>
      )}

      <h3>缓存</h3>
      {hit === null ? (
        <div className="subtle sm">当前模型不支持 prompt cache</div>
      ) : (
        <Field label="命中率" value={`${Math.round(hit * 100)}%`} />
      )}
      {snap.TotalCacheBreaks > 0 && (
        <Field label="链路断裂" value={`${snap.TotalCacheBreaks} 次`} />
      )}
      {snap.MissingAssistantUsage > 0 && (
        <div className="note warn-note">
          上游未返回 usage 数据（{snap.MissingAssistantUsage} 次），成本统计可能偏低，预算上限不会触发。
        </div>
      )}

      {perModel.length > 0 && (
        <>
          <h3>按模型</h3>
          {perModel.map((m) => (
            <div className="model-row" key={m.Model}>
              <span className="model-name">{m.Model}</span>
              <span className="subtle sm">
                {formatTokens(m.Input)}↓ {formatTokens(m.Output)}↑ {formatCost(m.Cost)}
              </span>
            </div>
          ))}
        </>
      )}
    </>
  );
}
