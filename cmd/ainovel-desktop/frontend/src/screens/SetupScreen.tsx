import { useEffect, useMemo, useState } from "react";
import * as api from "../bindings/wails";
import type { ProviderPreset } from "../bindings/wails";

// SetupScreen 首次引导：收集 Provider / API Key / Base URL / 模型四项，
// 与终端版 bootstrap.RunSetup 的四步一致，但一屏填完、可随时回改。
export function SetupScreen({ onDone }: { onDone: () => void }) {
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [provider, setProvider] = useState("");
  const [customName, setCustomName] = useState("");
  const [type, setType] = useState("openai");
  const [apiKey, setApiKey] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api
      .GetProviderPresets()
      .then((list) => {
        setPresets(list);
        if (list.length > 0) {
          setProvider(list[0].name);
          setBaseURL(list[0].baseURL);
        }
      })
      .catch((e) => setErr(String(e?.message ?? e)));
  }, []);

  const current = useMemo(
    () => presets.find((p) => p.name === provider),
    [presets, provider],
  );
  const isCustom = current?.needType ?? false;
  const keyOptional = current?.apiKeyOptional ?? false;

  const pickProvider = (name: string) => {
    setProvider(name);
    const p = presets.find((x) => x.name === name);
    // 预设自带 Base URL 时预填，方便直接用；自定义则清空让用户填。
    setBaseURL(p?.baseURL ?? "");
  };

  const canSubmit =
    !!provider &&
    !!model.trim() &&
    (!isCustom || !!customName.trim()) &&
    (keyOptional || !!apiKey.trim());

  const submit = async () => {
    if (!canSubmit || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.SaveInitialConfig({
        provider,
        customName,
        type: isCustom ? type : "",
        apiKey,
        baseURL,
        model: model.trim(),
      });
      onDone();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  return (
    <div className="welcome">
      <div className="welcome-card wide">
        <h1>初始化设置</h1>
        <p className="subtle">
          还没有检测到配置。选择一个模型服务商并填入凭证，之后可在设置里随时修改。
        </p>

        <label className="form-label">Provider</label>
        <div className="preset-grid">
          {presets.map((p) => (
            <button
              key={p.name}
              className={`preset ${provider === p.name ? "active" : ""}`}
              onClick={() => pickProvider(p.name)}
              disabled={busy}
            >
              {p.label}
            </button>
          ))}
        </div>

        {isCustom && (
          <>
            <label className="form-label">自定义 Provider 名称</label>
            <input
              className="text-input"
              placeholder="例如 my-proxy"
              value={customName}
              onChange={(e) => setCustomName(e.target.value)}
              disabled={busy}
            />
            <label className="form-label">API 协议类型</label>
            <div className="preset-grid">
              {["openai", "anthropic", "gemini"].map((t) => (
                <button
                  key={t}
                  className={`preset ${type === t ? "active" : ""}`}
                  onClick={() => setType(t)}
                  disabled={busy}
                >
                  {t}
                </button>
              ))}
            </div>
          </>
        )}

        <label className="form-label">
          API Key{keyOptional && <span className="subtle sm"> · 该服务商可留空</span>}
        </label>
        <input
          className="text-input"
          type="password"
          placeholder={keyOptional ? "（可留空）" : "sk-…"}
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          disabled={busy}
        />

        <label className="form-label">
          Base URL<span className="subtle sm"> · 留空使用默认</span>
        </label>
        <input
          className="text-input"
          placeholder="https://…"
          value={baseURL}
          onChange={(e) => setBaseURL(e.target.value)}
          disabled={busy}
        />

        <label className="form-label">模型名称</label>
        <input
          className="text-input"
          placeholder="例如 gpt-4o / claude-sonnet-4 / gemini-2.5-pro"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          disabled={busy}
        />

        {err && <div className="error-banner">{err}</div>}

        <div className="welcome-actions">
          <span className="hint">配置保存在 ~/.ainovel/config.json</span>
          <button className="primary" onClick={submit} disabled={busy || !canSubmit}>
            {busy ? "保存中…" : "完成设置"}
          </button>
        </div>
      </div>
    </div>
  );
}
