import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { APIKeyAction, ImageGenSettings } from "../bindings/wails";

const SIZE_OPTIONS = [
  ["1024x1536", "竖版"],
  ["768x1024", "3:4 竖版"],
  ["1024x1024", "方形"],
  ["1536x1024", "横版"],
] as const;

export function ImageGenTab() {
  const [settings, setSettings] = useState<ImageGenSettings | null>(null);
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("gpt-image-2");
  const [size, setSize] = useState("1024x1536");
  const [apiKeyAction, setAPIKeyAction] = useState<APIKeyAction>("keep");
  const [apiKey, setAPIKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [invalid, setInvalid] = useState<string[]>([]);
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  const load = async () => {
    try {
      const next = await api.GetImageGenSettings();
      setSettings(next);
      setBaseURL(next.baseURL);
      setModel(next.model || "gpt-image-2");
      setSize(next.size || "1024x1536");
      setAPIKeyAction("keep");
      setAPIKey("");
    } catch (e) {
      setMsg({ kind: "err", text: String((e as Error)?.message ?? e) });
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const usePreset = (url: string) => {
    setBaseURL(url);
    setModel("gpt-image-2");
    setSize("1024x1536");
    setInvalid([]);
    setMsg(null);
  };

  const save = async () => {
    if (busy) return;
    const bad: string[] = [];
    if (!baseURL.trim()) bad.push("baseURL");
    if (!model.trim()) bad.push("model");
    if (apiKeyAction === "replace" && !apiKey.trim()) bad.push("apiKey");
    setInvalid(bad);
    if (bad.length > 0) {
      setMsg({ kind: "err", text: "请填写所有必填项后再保存" });
      return;
    }

    setBusy(true);
    setMsg(null);
    try {
      await api.SaveImageGenSettings({
        baseURL: baseURL.trim(),
        model: model.trim(),
        size,
        apiKeyAction,
        apiKey: apiKey.trim(),
      });
      await load();
      setMsg({ kind: "ok", text: "图片生成配置已保存" });
    } catch (e) {
      setMsg({ kind: "err", text: String((e as Error)?.message ?? e) });
    } finally {
      setBusy(false);
    }
  };

  const configured = !!settings?.baseURL && !!settings?.model;

  return (
    <div className="settings-detail wide imagegen-settings">
      <div className="settings-heading">
        <div>
          <h3 className="section-label">图片生成服务</h3>
          <p className="subtle sm">用于所有书籍的封面生成。</p>
        </div>
        <span className={`status-dot ${configured ? "ready" : ""}`}>
          {configured ? "已配置" : "未配置"}
        </span>
      </div>

      <label className="form-label">快速配置</label>
      <div className="preset-grid">
        <button onClick={() => usePreset("https://jarlessapi.com")} disabled={busy}>
          JarlessAPI · gpt-image-2
        </button>
        <button onClick={() => usePreset("https://api.openai.com/v1")} disabled={busy}>
          OpenAI 官方 · gpt-image-2
        </button>
      </div>

      <label className="form-label">
        Base URL<span className="req"> *必填</span>
      </label>
      <input
        className={`text-input ${invalid.includes("baseURL") ? "invalid" : ""}`}
        value={baseURL}
        onChange={(e) => {
          setBaseURL(e.target.value);
          setInvalid((cur) => cur.filter((v) => v !== "baseURL"));
        }}
        placeholder="https://api.openai.com/v1"
        disabled={busy}
      />

      <label className="form-label">
        模型<span className="req"> *必填</span>
      </label>
      <input
        className={`text-input ${invalid.includes("model") ? "invalid" : ""}`}
        value={model}
        onChange={(e) => {
          setModel(e.target.value);
          setInvalid((cur) => cur.filter((v) => v !== "model"));
        }}
        placeholder="gpt-image-2"
        disabled={busy}
      />

      <label className="form-label">API Key</label>
      {apiKeyAction === "keep" ? (
        <div className="key-control">
          <span className="subtle sm">
            {settings?.hasAPIKey ? `当前 ${settings.apiKeyHint}` : "尚未填写"}
          </span>
          <div className="inline-actions">
            <button onClick={() => setAPIKeyAction("replace")} disabled={busy}>
              {settings?.hasAPIKey ? "更换 Key" : "填写 Key"}
            </button>
            {settings?.hasAPIKey && (
              <button onClick={() => setAPIKeyAction("clear")} disabled={busy}>
                清除 Key
              </button>
            )}
          </div>
        </div>
      ) : apiKeyAction === "clear" ? (
        <div className="key-control">
          <span className="subtle sm">保存后将清除当前 Key</span>
          <button onClick={() => setAPIKeyAction("keep")} disabled={busy}>
            取消
          </button>
        </div>
      ) : (
        <div className="inline-actions">
          <input
            className={`text-input ${invalid.includes("apiKey") ? "invalid" : ""}`}
            type="password"
            value={apiKey}
            onChange={(e) => {
              setAPIKey(e.target.value);
              setInvalid((cur) => cur.filter((v) => v !== "apiKey"));
            }}
            placeholder="sk-..."
            disabled={busy}
            autoFocus
          />
          <button
            onClick={() => {
              setAPIKeyAction("keep");
              setAPIKey("");
              setInvalid((cur) => cur.filter((v) => v !== "apiKey"));
            }}
            disabled={busy}
          >
            取消
          </button>
        </div>
      )}

      <label className="form-label">图片尺寸</label>
      <div className="preset-grid">
        {SIZE_OPTIONS.map(([value, label]) => (
          <button
            key={value}
            className={`preset ${size === value ? "active" : ""}`}
            onClick={() => setSize(value)}
            disabled={busy}
          >
            {value} · {label}
          </button>
        ))}
      </div>

      {msg && <div className={msg.kind === "ok" ? "ok-banner" : "error-banner"}>{msg.text}</div>}

      <div className="detail-actions">
        <span className="subtle sm">写入 {settings?.path || "imagegen.json"}</span>
        <button className="primary" onClick={save} disabled={busy}>
          {busy ? "保存中…" : "保存图片生成配置"}
        </button>
      </div>
    </div>
  );
}
