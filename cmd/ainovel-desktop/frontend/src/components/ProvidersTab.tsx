import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { APIKeyAction, ConfigView, ProviderPreset, ProviderView } from "../bindings/wails";

// 草稿状态：从 ProviderView 派生，记录原始模型名以便提交 renames
// （Host 只在明确提交 rename 关系时才迁移 default/角色/fallback 引用，不猜“删旧增新”）。
type ModelRow = {
  name: string;
  contextWindow: number;
  originalName: string; // 空串 = 新增行
  references: string[];
};

type Draft = {
  provider: string;
  isNew: boolean;
  type: string;
  api: string;
  baseURL: string;
  models: ModelRow[];
  apiKeyAction: APIKeyAction;
  apiKey: string;
  hasAPIKey: boolean;
  apiKeyHint: string;
  requiresAPIKey: boolean;
};

function toDraft(p: ProviderView): Draft {
  return {
    provider: p.name,
    isNew: false,
    type: p.type,
    api: p.api,
    baseURL: p.baseURL,
    models: (p.models ?? []).map((m) => ({
      name: m.name,
      contextWindow: m.contextWindow,
      originalName: m.name,
      references: m.references ?? [],
    })),
    apiKeyAction: "keep",
    apiKey: "",
    hasAPIKey: p.hasAPIKey,
    apiKeyHint: p.apiKeyHint,
    requiresAPIKey: p.requiresAPIKey,
  };
}

export function ProvidersTab() {
  const [cfg, setCfg] = useState<ConfigView | null>(null);
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  const load = async (keep?: string) => {
    try {
      const [c, p] = await Promise.all([api.GetConfig(), api.GetProviderPresets()]);
      setCfg(c);
      setPresets(p);
      const list = c.providers ?? [];
      const pick = keep ?? selected ?? list[0]?.name ?? null;
      setSelected(pick);
      const found = list.find((x) => x.name === pick);
      setDraft(found ? toDraft(found) : null);
    } catch (e) {
      setMsg({ kind: "err", text: String((e as Error)?.message ?? e) });
    }
  };

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const select = (name: string) => {
    setMsg(null);
    setSelected(name);
    const found = (cfg?.providers ?? []).find((x) => x.name === name);
    setDraft(found ? toDraft(found) : null);
  };

  const addProvider = (preset: ProviderPreset) => {
    setMsg(null);
    const name = preset.needType ? "" : preset.name;
    setSelected(null);
    setDraft({
      provider: name,
      isNew: true,
      type: preset.needType ? "openai" : "",
      api: "",
      baseURL: preset.baseURL,
      models: [{ name: "", contextWindow: 0, originalName: "", references: [] }],
      apiKeyAction: "replace",
      apiKey: "",
      hasAPIKey: false,
      apiKeyHint: "",
      requiresAPIKey: !preset.apiKeyOptional,
    });
  };

  const patch = (p: Partial<Draft>) => setDraft((d) => (d ? { ...d, ...p } : d));

  const setModel = (i: number, p: Partial<ModelRow>) =>
    setDraft((d) =>
      d ? { ...d, models: d.models.map((m, j) => (j === i ? { ...m, ...p } : m)) } : d,
    );

  const addModel = () =>
    setDraft((d) =>
      d
        ? { ...d, models: [...d.models, { name: "", contextWindow: 0, originalName: "", references: [] }] }
        : d,
    );

  const removeModel = (i: number) =>
    setDraft((d) => (d ? { ...d, models: d.models.filter((_, j) => j !== i) } : d));

  const buildPayload = (d: Draft): api.ProviderDraft => ({
    provider: d.provider.trim(),
    type: d.type,
    api: d.api,
    baseURL: d.baseURL.trim(),
    models: d.models
      .filter((m) => m.name.trim())
      .map((m) => ({ name: m.name.trim(), contextWindow: m.contextWindow || 0 })),
    // 只提交真正改名的行（原名非空且与新名不同）。
    renames: d.models
      .filter((m) => m.originalName && m.name.trim() && m.originalName !== m.name.trim())
      .map((m) => ({ from: m.originalName, to: m.name.trim() })),
    apiKeyAction: d.apiKeyAction,
    apiKey: d.apiKey,
  });

  const run = async (fn: () => Promise<unknown>, okText: string) => {
    if (!draft || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      await fn();
      setMsg({ kind: "ok", text: okText });
      return true;
    } catch (e) {
      setMsg({ kind: "err", text: String((e as Error)?.message ?? e) });
      return false;
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    if (!draft) return;
    const payload = buildPayload(draft);
    const ok = await run(() => api.SaveProvider(payload), "已保存并生效");
    if (ok) await load(payload.provider);
  };

  const test = async () => {
    if (!draft) return;
    const payload = buildPayload(draft);
    const first = payload.models[0]?.name;
    if (!first) {
      setMsg({ kind: "err", text: "请先填写至少一个模型" });
      return;
    }
    await run(() => api.TestConnection(payload, first), `连接正常（${first}）`);
  };

  const providers = cfg?.providers ?? [];

  return (
    <div className="settings-split">
      <aside className="settings-list">
        {providers.map((p) => (
          <button
            key={p.name}
            className={`list-row ${selected === p.name ? "active" : ""}`}
            onClick={() => select(p.name)}
          >
            <span>{p.name}</span>
            {p.name === cfg?.defaultProvider && <span className="tag">默认</span>}
          </button>
        ))}
        <div className="list-divider">新增</div>
        {presets.map((p) => (
          <button key={p.name} className="list-row add" onClick={() => addProvider(p)}>
            + {p.label}
          </button>
        ))}
      </aside>

      <div className="settings-detail">
        {!draft && <div className="subtle">选择左侧一个服务商进行编辑，或新增一个。</div>}

        {draft && (
          <>
            <h3 className="section-label">{draft.isNew ? "新增服务商" : `编辑 ${draft.provider}`}</h3>

            {draft.isNew && (
              <>
                <label className="form-label">Provider 名称</label>
                <input
                  className="text-input"
                  value={draft.provider}
                  onChange={(e) => patch({ provider: e.target.value })}
                  placeholder="配置中的 key，例如 my-proxy"
                  disabled={busy}
                />
              </>
            )}

            <label className="form-label">
              API 协议类型<span className="subtle sm"> · 内置服务商可留空</span>
            </label>
            <div className="preset-grid">
              {["", "openai", "anthropic", "gemini"].map((t) => (
                <button
                  key={t || "auto"}
                  className={`preset ${draft.type === t ? "active" : ""}`}
                  onClick={() => patch({ type: t })}
                  disabled={busy}
                >
                  {t || "自动"}
                </button>
              ))}
            </div>

            {(draft.type === "openai" || (!draft.type && draft.provider === "openai")) && (
              <>
                <label className="form-label">
                  OpenAI Endpoint<span className="subtle sm"> · Codex 类代理常需 responses</span>
                </label>
                <div className="preset-grid">
                  {["", "chat", "responses"].map((v) => (
                    <button
                      key={v || "default"}
                      className={`preset ${draft.api === v ? "active" : ""}`}
                      onClick={() => patch({ api: v })}
                      disabled={busy}
                    >
                      {v || "默认(chat)"}
                    </button>
                  ))}
                </div>
              </>
            )}

            <label className="form-label">Base URL</label>
            <input
              className="text-input"
              value={draft.baseURL}
              onChange={(e) => patch({ baseURL: e.target.value })}
              placeholder="留空使用默认"
              disabled={busy}
            />

            <label className="form-label">
              API Key
              {draft.hasAPIKey && draft.apiKeyAction === "keep" && (
                <span className="subtle sm"> · 当前 {draft.apiKeyHint}</span>
              )}
              {!draft.requiresAPIKey && <span className="subtle sm"> · 该服务商可留空</span>}
            </label>
            {draft.apiKeyAction === "keep" ? (
              <div className="inline-actions">
                <button onClick={() => patch({ apiKeyAction: "replace" })} disabled={busy}>
                  {draft.hasAPIKey ? "更换 Key" : "填写 Key"}
                </button>
                {draft.hasAPIKey && !draft.requiresAPIKey && (
                  <button onClick={() => patch({ apiKeyAction: "clear" })} disabled={busy}>
                    清空 Key
                  </button>
                )}
              </div>
            ) : draft.apiKeyAction === "clear" ? (
              <div className="inline-actions">
                <span className="subtle">保存后将清空该 Key</span>
                <button onClick={() => patch({ apiKeyAction: "keep" })} disabled={busy}>
                  取消
                </button>
              </div>
            ) : (
              <div className="inline-actions">
                <input
                  className="text-input"
                  type="password"
                  value={draft.apiKey}
                  onChange={(e) => patch({ apiKey: e.target.value })}
                  placeholder="sk-…"
                  disabled={busy}
                  autoFocus
                />
                {!draft.isNew && (
                  <button
                    onClick={() => patch({ apiKeyAction: "keep", apiKey: "" })}
                    disabled={busy}
                  >
                    取消
                  </button>
                )}
              </div>
            )}

            <label className="form-label">
              模型库<span className="subtle sm"> · 上下文窗口留空表示自动解析</span>
            </label>
            <table className="model-table">
              <thead>
                <tr>
                  <th>模型 ID</th>
                  <th className="num">上下文窗口</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {draft.models.map((m, i) => (
                  <tr key={i}>
                    <td>
                      <input
                        className="cell-input"
                        value={m.name}
                        onChange={(e) => setModel(i, { name: e.target.value })}
                        placeholder="例如 gpt-4o"
                        disabled={busy}
                      />
                      {m.references.length > 0 && (
                        <span className="tag ref" title={`被 ${m.references.join("、")} 引用`}>
                          {m.references.join(" · ")}
                        </span>
                      )}
                    </td>
                    <td className="num">
                      <input
                        className="cell-input num"
                        type="number"
                        min={0}
                        value={m.contextWindow || ""}
                        onChange={(e) =>
                          setModel(i, { contextWindow: Number(e.target.value) || 0 })
                        }
                        placeholder="自动"
                        disabled={busy}
                      />
                    </td>
                    <td>
                      <button
                        className="ghost sm"
                        onClick={() => removeModel(i)}
                        disabled={busy || m.references.length > 0}
                        title={
                          m.references.length > 0
                            ? "该模型仍被引用，请先在「模型与角色」中切走"
                            : "删除"
                        }
                      >
                        删除
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <button className="ghost sm" onClick={addModel} disabled={busy}>
              + 添加模型
            </button>

            {msg && (
              <div className={msg.kind === "ok" ? "ok-banner" : "error-banner"}>{msg.text}</div>
            )}

            <div className="detail-actions">
              <span className="subtle sm">写入 {cfg?.configPath}</span>
              <div className="inline-actions">
                <button onClick={test} disabled={busy}>
                  测试连接
                </button>
                <button className="primary" onClick={save} disabled={busy}>
                  {busy ? "处理中…" : "保存"}
                </button>
              </div>
            </div>
            <p className="subtle sm">
              测试连接会发送一个最小真实请求，可能产生少量 API 用量。
            </p>
          </>
        )}
      </div>
    </div>
  );
}
