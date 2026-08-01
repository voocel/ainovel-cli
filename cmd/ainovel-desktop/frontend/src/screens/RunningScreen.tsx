import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import { useEngineFeed } from "../stores/engineFeed";
import { useBookContents } from "../stores/bookContents";
import { TopBar } from "../components/TopBar";
import { ActivityStrip } from "../components/ActivityStrip";
import { ChapterNav } from "../components/ChapterNav";
import { MainPane } from "../components/MainPane";
import { SidePanel } from "../components/SidePanel";
import { CommandBar } from "../components/CommandBar";
import { AskUserModal } from "../components/AskUserModal";
import { CoCreatePanel } from "../components/CoCreatePanel";
import { ExportPanel } from "../components/ExportPanel";
import { SimulatePanel } from "../components/SimulatePanel";
import { FoundationPanel } from "../components/FoundationPanel";
import { SkillPanel } from "../components/SkillPanel";
import { GateBanner, gateWaiting } from "../components/GateBanner";
import { CoverPanel } from "../components/CoverPanel";
import { ReaderPanel } from "../components/ReaderPanel";
import { RipPanel } from "../components/RipPanel";
import { RankScanPanel } from "../components/RankScanPanel";
import type { SettingsTab } from "./SettingsScreen";

// RunningScreen 创作工作台：顶栏状态 / 验收横幅 / 目录 / 正文 / 信息面板 / 活动条 / 命令栏。
// 叠加层：AskUser 提问、设定审阅、阶段共创、导出、仿写。
export function RunningScreen({
  onOpenSettings,
  onBackToLibrary,
}: {
  onOpenSettings: (tab?: SettingsTab) => void;
  onBackToLibrary: () => void;
}) {
  const { events, rounds, snapshot, ask, clearAsk } = useEngineFeed();
  const [error, setError] = useState<string | null>(null);
  const [staging, setStaging] = useState(false);
  const [overlay, setOverlay] = useState<
    "none" | "export" | "simulate" | "foundation" | "cover" | "reader" | "skills" | "rip" | "scan"
  >("none");
  const [busy, setBusy] = useState(false);
  // 用户在目录里点选的章节；null 表示跟随引擎（始终看最新完成的那章）。
  const [picked, setPicked] = useState<number | null>(null);

  const completed = snapshot?.CompletedCount ?? 0;
  const contents = useBookContents(completed);
  const chapters = contents?.chapters ?? [];
  const latest = chapters.length > 0 ? chapters[chapters.length - 1].chapter : 0;
  // 选中的章若已不存在（换书、回滚），退回最新一章。
  const shown = picked !== null && chapters.some((c) => c.chapter === picked) ? picked : latest;

  // 换书时清掉选中章：否则新书会停在旧书的章号上。
  useEffect(() => {
    setPicked(null);
  }, [contents?.novelName]);

  // 底部浮条要不要占位。两者都没有时不渲染容器，滚动区也不留 padding。
  const alerts = gateWaiting(snapshot) || !!error;

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

  // 全局快捷键。「放行下一章」是分段验收模式下重复频率最高的动作，
  // 之前只能用鼠标点——每章一次，这是最该有快捷键的操作。
  // 有浮层打开时一律不响应：浮层里的 Esc/Tab 由 Overlay 接管，这里不该抢。
  const overlayOpen = overlay !== "none" || !!ask || staging;
  useEffect(() => {
    if (overlayOpen) return;
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      const typing = t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName);
      // Ctrl+Enter 放行下一章：在输入框里也生效（放行与输入内容无关）。
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
        if (snapshot?.AdvanceMode === "review" && !snapshot.IsRunning) {
          e.preventDefault();
          void guard(api.AdvanceOneChapter);
        }
        return;
      }
      if (typing) return;
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "r") {
        if (completed > 0) {
          e.preventDefault();
          setOverlay("reader");
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [overlayOpen, snapshot, busy, completed]);

  return (
    <div className="workbench">
      <TopBar
        snap={snapshot}
        onOpenSettings={() => onOpenSettings()}
        onBackToLibrary={onBackToLibrary}
      />

      <div className={`workbench-body ${alerts ? "has-alerts" : ""}`}>
        <ChapterNav
          snap={snapshot}
          contents={contents}
          current={shown}
          onSelect={setPicked}
        />
        <MainPane
          snap={snapshot}
          rounds={rounds}
          chapter={shown}
          onOpenReader={() => setOverlay("reader")}
        />
        <SidePanel snap={snapshot} />

        {/* 验收横幅与错误条浮在内容底部，不占布局流。
            原先它们插在顶栏和三栏之间——逐章验收模式下每个章节边界出现一次、
            放行后又消失，等于每章把整个工作台上下顶两回。现在只在滚动区末尾
            多留一段 padding（见 .has-alerts），已有内容一格都不会动。 */}
        {alerts && (
          <div className="workbench-alerts">
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
          </div>
        )}
      </div>

      <ActivityStrip events={events} />

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
        onRip={() => setOverlay("rip")}
        onScan={() => setOverlay("scan")}
      />

      {/* 阶段共创是浮层而不是整屏替换。原先它 early-return 掉整个工作台，
          连带把 AskUserModal 和 GateBanner 一起吞掉——共创期间引擎若提问，
          用户根本看不到，而引擎在那儿阻塞着等。 */}
      {staging && (
        <CoCreatePanel
          mode="stage"
          onDone={() => setStaging(false)}
          onCancel={() => setStaging(false)}
        />
      )}

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
        <CoverPanel
          onClose={() => setOverlay("none")}
          onChanged={() => {}}
          onOpenImageSettings={() => {
            setOverlay("none");
            onOpenSettings("imagegen");
          }}
        />
      )}
      {overlay === "reader" && <ReaderPanel onClose={() => setOverlay("none")} />}
      {overlay === "skills" && (
        <SkillPanel
          onClose={() => setOverlay("none")}
          completedChapters={completed}
        />
      )}
      {overlay === "rip" && <RipPanel onClose={() => setOverlay("none")} />}
      {overlay === "scan" && <RankScanPanel onClose={() => setOverlay("none")} />}
    </div>
  );
}
