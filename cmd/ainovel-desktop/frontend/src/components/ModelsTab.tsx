import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { ModelsView, RoleSelection } from "../bindings/wails";

const ROLE_LABELS: Record<string, string> = {
  default: "默认",
  architect: "Architect · 规划",
  writer: "Writer · 写作",
  editor: "Editor · 评审",
};

const THINKING_LABELS: Record<string, string> = {
  "": "继承",
  off: "关闭",
  low: "低",
  medium: "中",
  high: "高",
  xhigh: "极高",
  max: "最高",
};

// ModelsTab 对应终端版 /model：为每个角色选 provider / 模型 / 推理强度。
// 未单独配置的角色继承 default（Explicit=false），界面上明示。
export function ModelsTab() {
  const [view, setView] = useState<ModelsView | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  const load = async () => {
    try {
      setView(await api.GetModels());
    } catch (e) {
      setMsg({ kind: "err", text: String((e as Error)?.message ?? e) });
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const run = async (fn: () => Promise<unknown>, okText: string) => {
    if (busy) return;
    setBusy(true);
    setMsg(null);
    try {
      await fn();
      setMsg({ kind: "ok", text: okText });
      await load();
    } catch (e) {
      setMsg({ kind: "err", text: String((e as Error)?.message ?? e) });
    } finally {
      setBusy(false);
    }
  };

  if (!view) return <div className="settings-detail subtle">加载中…</div>;

  const providers = view.providers ?? [];
  const modelsByProvider = view.models ?? {};
  const roles = view.roles ?? [];

  return (
    <div className="settings-detail wide">
      <h3>角色模型分配</h3>
      <p className="subtle sm">
        为不同角色分配不同模型可以平衡质量与成本。未单独设置的角色继承「默认」。
      </p>

      {msg && <div className={msg.kind === "ok" ? "ok-banner" : "error-banner"}>{msg.text}</div>}

      <table className="role-table">
        <thead>
          <tr>
            <th>角色</th>
            <th>Provider</th>
            <th>模型</th>
            <th>推理强度</th>
          </tr>
        </thead>
        <tbody>
          {roles.map((r) => (
            <RoleRow
              key={r.role}
              row={r}
              providers={providers}
              modelsByProvider={modelsByProvider}
              busy={busy}
              onSwitch={(provider, model) =>
                run(
                  () => api.SwitchModel(r.role, provider, model),
                  `${ROLE_LABELS[r.role] ?? r.role} 已切换为 ${provider}/${model}`,
                )
              }
              onThinking={(level) =>
                run(
                  () => api.SetRoleThinking(r.role, level),
                  `${ROLE_LABELS[r.role] ?? r.role} 推理强度已设为 ${THINKING_LABELS[level] ?? level}`,
                )
              }
            />
          ))}
        </tbody>
      </table>

      <p className="subtle sm">
        推理强度按「意图 × 能力」生效：存储的是你选定的意图，实际下发时按该角色当前模型的能力钳制。
        换到能力较低的模型只是当次生效值被钳低，切回强模型即自动恢复。
      </p>
    </div>
  );
}

function RoleRow({
  row,
  providers,
  modelsByProvider,
  busy,
  onSwitch,
  onThinking,
}: {
  row: RoleSelection;
  providers: string[];
  modelsByProvider: Record<string, api.ModelOption[]>;
  busy: boolean;
  onSwitch: (provider: string, model: string) => void;
  onThinking: (level: string) => void;
}) {
  const models = modelsByProvider[row.provider] ?? [];
  const available = row.available ?? [];
  // 继承项("")总可选；其余按当前模型能力过滤。
  const thinkingOptions = ["", ...available.filter((a) => a !== "")];

  return (
    <tr>
      <td>
        <div className="role-name">{ROLE_LABELS[row.role] ?? row.role}</div>
        {!row.explicit && row.role !== "default" && (
          <span className="tag">继承默认</span>
        )}
      </td>
      <td>
        <select
          className="select"
          value={row.provider}
          disabled={busy}
          onChange={(e) => {
            const p = e.target.value;
            const first = (modelsByProvider[p] ?? [])[0]?.name;
            if (first) onSwitch(p, first);
          }}
        >
          {providers.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      </td>
      <td>
        <select
          className="select"
          value={row.model}
          disabled={busy}
          onChange={(e) => onSwitch(row.provider, e.target.value)}
        >
          {models.length === 0 && <option value={row.model}>{row.model}</option>}
          {models.map((m) => (
            <option key={m.name} value={m.name}>
              {m.name}
              {m.contextWindow > 0 && ` (${Math.round(m.contextWindow / 1000)}K)`}
            </option>
          ))}
        </select>
      </td>
      <td>
        <select
          className="select"
          value={row.thinking}
          disabled={busy}
          onChange={(e) => onThinking(e.target.value)}
        >
          {thinkingOptions.map((t) => (
            <option key={t || "inherit"} value={t}>
              {THINKING_LABELS[t] ?? t}
            </option>
          ))}
        </select>
      </td>
    </tr>
  );
}
