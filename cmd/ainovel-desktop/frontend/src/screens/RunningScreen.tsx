import { useState } from "react";
import * as api from "../bindings/wails";
import { useEngineFeed } from "../stores/engineFeed";
import { TopBar } from "../components/TopBar";
import { ActivityFeed } from "../components/ActivityFeed";
import { ProsePane } from "../components/ProsePane";
import { SidePanel } from "../components/SidePanel";
import { CommandBar } from "../components/CommandBar";
import { AskUserModal } from "../components/AskUserModal";
import { CoCreatePanel } from "../components/CoCreatePanel";
import { ExportPanel } from "../components/ExportPanel";
import { SimulatePanel } from "../components/SimulatePanel";
import { FoundationPanel } from "../components/FoundationPanel";
import { SkillPanel } from "../components/SkillPanel";
import { GateBanner } from "../components/GateBanner";
import { CoverPanel } from "../components/CoverPanel";
import { ReaderPanel } from "../components/ReaderPanel";

// RunningScreen 创作工作台：顶栏状态 / 验收横幅 / 活动流 / 实时正文 / 信息面板 / 命令栏。
// 叠加层：AskUser 提问、设定审阅、阶段共创、导出、仿写。
export function RunningScreen({
  onOpenSettings,
  onBackToLibrary,
}: {
  onOpenSettings: () => void;
  onBackToLibrary: () => void;
}) {
  const { events, rounds, snapshot, ask, clearAsk } = useEngineFeed();
  const [error, setError] = useState<string | null>(null);
  const [staging, setStaging] = useState(false);
  const [overlay, setOverlay] = useState<
    "none" | "export" | "simulate" | "foundation" | "cover" | "reader" | "skills"
  >("none");
  const [busy, setBusy] = useState(false);

  const guard = async (fn: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const openStageCoCreate = async () => {
    try {
      const ok = await api.PauseForCoCreate();
      if (!ok) {
        setError("当前无法进入阶段共创（全书已完成或已在共创中）");
        return;
      }
      setStaging(true);
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
    }
  };

  if (staging) {
    return (
      <CoCreatePanel
        mode="stage"
        onDone={() => setStaging(false)}
        onCancel={() => setStaging(false)}
      />
    );
  }

  return (
    <div className="workbench">
      <TopBar
        snap={snapshot}
        onOpenSettings={onOpenSettings}
        onBackToLibrary={onBackToLibrary}
      />

      {snapshot && (
        <GateBanner
          snap={snapshot}
          busy={busy}
          onReviewFoundation={() => setOverlay("foundation")}
          onAdvance={() => void guard(api.AdvanceOneChapter)}
        />
      )}

      {error && (
        <div className="error-banner inline">
          <span>{error}</span>
          <button className="ghost sm" onClick={() => setError(null)}>
            关闭
          </button>
        </div>
      )}

      <div className="workbench-body">
        <ActivityFeed events={events} />
        <ProsePane rounds={rounds} running={snapshot?.IsRunning ?? false} />
        <SidePanel snap={snapshot} />
      </div>

      <CommandBar
        snap={snapshot}
        onError={setError}
        onStageCoCreate={() => void openStageCoCreate()}
        onExport={() => setOverlay("export")}
        onSimulate={() => setOverlay("simulate")}
        onFoundation={() => setOverlay("foundation")}
        onCover={() => setOverlay("cover")}
        onRead={() => setOverlay("reader")}
        onSkills={() => setOverlay("skills")}
      />

      {ask && <AskUserModal id={ask.id} questions={ask.questions} onClose={clearAsk} />}
      {overlay === "foundation" && (
        <FoundationPanel
          onClose={() => setOverlay("none")}
          onApprove={() => setOverlay("none")}
        />
      )}
      {overlay === "export" && (
        <ExportPanel
          novelName={snapshot?.NovelName ?? ""}
          onClose={() => setOverlay("none")}
        />
      )}
      {overlay === "simulate" && <SimulatePanel onClose={() => setOverlay("none")} />}
      {overlay === "cover" && (
        <CoverPanel onClose={() => setOverlay("none")} onChanged={() => {}} />
      )}
      {overlay === "reader" && <ReaderPanel onClose={() => setOverlay("none")} />}
      {overlay === "skills" && (
        <SkillPanel
          onClose={() => setOverlay("none")}
          completedChapters={snapshot?.CompletedCount ?? 0}
        />
      )}
    </div>
  );
}
