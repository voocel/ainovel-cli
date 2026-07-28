import { useEffect, useState } from "react";
import * as api from "./bindings/wails";
import { WelcomeScreen } from "./screens/WelcomeScreen";
import { RunningScreen } from "./screens/RunningScreen";
import { SetupScreen } from "./screens/SetupScreen";
import { SettingsScreen } from "./screens/SettingsScreen";
import { LibraryScreen } from "./screens/LibraryScreen";

// 屏幕模型：
//   setup    首次引导（无配置时）
//   library  书库首页（选书 / 新建）
//   welcome  已打开一本空书，等待起书（快速 / 共创 / 导入）
//   running  创作工作台
// settings 是叠加层，从任意界面打开后返回原处。
type Screen = "loading" | "setup" | "library" | "welcome" | "running";

export function App() {
  const [screen, setScreen] = useState<Screen>("loading");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [booted, setBooted] = useState(false);

  // 启动：需要引导 → setup；否则进书库。
  // 不自动打开上次的书——书库是显式入口，用户自己选，避免误在错误的书上继续。
  const boot = async () => {
    try {
      setScreen((await api.NeedsSetup()) ? "setup" : "library");
    } catch (e) {
      console.error("启动失败", e);
      setScreen("library");
    }
  };

  useEffect(() => {
    if (booted) return;
    setBooted(true);
    void boot();
  }, [booted]);

  let content;
  if (screen === "loading") {
    content = <div className="center-note">正在启动…</div>;
  } else if (screen === "setup") {
    // 引导完成后配置已落盘，重走启动流程进书库。
    content = <SetupScreen onDone={() => void boot()} />;
  } else if (screen === "library") {
    content = (
      <LibraryScreen
        // 有任何持久进度（包括已完结）都进工作台；空书进起书页。
        onOpened={(hasProgress) => setScreen(hasProgress ? "running" : "welcome")}
        onOpenSettings={() => setSettingsOpen(true)}
      />
    );
  } else if (screen === "welcome") {
    content = (
      <WelcomeScreen
        onStarted={() => setScreen("running")}
        onOpenSettings={() => setSettingsOpen(true)}
        onBackToLibrary={() => setScreen("library")}
      />
    );
  } else {
    content = (
      <RunningScreen
        onOpenSettings={() => setSettingsOpen(true)}
        onBackToLibrary={() => setScreen("library")}
      />
    );
  }

  return (
    <>
      {content}
      {settingsOpen && (
        <div className="settings-overlay">
          <SettingsScreen onClose={() => setSettingsOpen(false)} />
        </div>
      )}
    </>
  );
}
