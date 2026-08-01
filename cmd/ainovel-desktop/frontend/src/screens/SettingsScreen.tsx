import { useEffect, useState } from "react";
import { ProvidersTab } from "../components/ProvidersTab";
import { ModelsTab } from "../components/ModelsTab";
import { ImageGenTab } from "../components/ImageGenTab";

export type SettingsTab = "providers" | "models" | "imagegen";

// SettingsScreen 设置页：Providers（对应 /config，编辑服务商定义与模型库）
// 与 模型/角色（对应 /model，切换当前用哪个模型与推理强度）。两者职责分离，
// 与终端版一致：/config 只定义，/model 才决定“现在用哪个”。
export function SettingsScreen({
  onClose,
  initialTab = "providers",
}: {
  onClose: () => void;
  initialTab?: SettingsTab;
}) {
  const [tab, setTab] = useState<SettingsTab>(initialTab);

  useEffect(() => {
    setTab(initialTab);
  }, [initialTab]);

  return (
    <div className="settings">
      <header className="topbar">
        <div className="topbar-left">
          <strong>设置</strong>
          <nav className="tabs inline">
            <button
              className={`tab ${tab === "providers" ? "active" : ""}`}
              onClick={() => setTab("providers")}
            >
              服务商
            </button>
            <button
              className={`tab ${tab === "models" ? "active" : ""}`}
              onClick={() => setTab("models")}
            >
              模型与角色
            </button>
            <button
              className={`tab ${tab === "imagegen" ? "active" : ""}`}
              onClick={() => setTab("imagegen")}
            >
              图片生成
            </button>
          </nav>
        </div>
        <button className="ghost" onClick={onClose}>
          返回
        </button>
      </header>

      <div className="settings-body">
        {tab === "providers" && <ProvidersTab />}
        {tab === "models" && <ModelsTab />}
        {tab === "imagegen" && <ImageGenTab />}
      </div>
    </div>
  );
}
