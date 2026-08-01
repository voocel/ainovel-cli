import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { SkillCatalog } from "../bindings/wails";
import { Overlay } from "./Overlay";

// SkillPanel 专项技能中心：列出当前生效的技能（内置 / 全局 / 本书三层合并），
// 让用户挑一个、划定范围、发起执行。
//
// 执行走引擎既有的用户干预通道（Arbiter 裁定 → 派给技能声明的 Worker），
// 不直接派单——绕过裁定就同时绕过阶段校验、暂停判断与审计记录。
export function SkillPanel({
  onClose,
  completedChapters,
}: {
  onClose: () => void;
  completedChapters: number;
}) {
  const [cat, setCat] = useState<SkillCatalog | null>(null);
  const [selected, setSelected] = useState<string>("");
  const [range, setRange] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [sent, setSent] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);

  useEffect(() => {
    api
      .ListSkills()
      .then(setCat)
      .catch((e) => setErr(String((e as Error)?.message ?? e)));
  }, []);

  const list = cat?.skills ?? [];
  const problems = cat?.problems ?? [];
  const current = list.find((s) => s.name === selected) ?? null;
  const takesChapters = current?.scope === "chapters";

  // 技能目录只在启动时扫一次，所以放完文件需要一条不重启就能生效的路径。
  const reload = async () => {
    if (busy) return;
    setBusy(true);
    setErr(null);
    setNote(null);
    try {
      const next = await api.ReloadSkills();
      setCat(next);
      // 选中的技能可能已被删掉或改名。
      if (selected && !(next.skills ?? []).some((s) => s.name === selected)) {
        setSelected("");
      }
      setNote(`已重载：当前 ${(next.skills ?? []).length} 个技能可用`);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const openDir = async () => {
    setErr(null);
    try {
      await api.OpenSkillsDir();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const run = async () => {
    if (!current || busy) return;
    let chapters: number[] | null = null;
    if (takesChapters && range.trim()) {
      try {
        chapters = parseChapters(range);
      } catch (e) {
        setErr((e as Error).message);
        return;
      }
    }
    setBusy(true);
    setErr(null);
    try {
      await api.RunSkill(current.name, chapters);
      setSent(skillDisplayName(current.name));
      setRange("");
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Overlay layer="sheet" onClose={busy ? undefined : onClose} labelledBy="skill-title">
      <div className="modal wide foundation">
        <h2 id="skill-title">专项技能</h2>
        <p className="subtle sm">
          技能是一份可复用的专项处理方法，挑一个就会挂到本次改稿任务上。选定后可划定章节范围；
          留空则按技能声明的范围处理。
        </p>

        <div className="inline-actions skill-toolbar">
          <button className="ghost sm" onClick={openDir} disabled={busy} title={cat?.dir ?? ""}>
            打开技能目录
          </button>
          <button className="ghost sm" onClick={reload} disabled={busy}>
            {busy ? "重载中…" : "重新扫描"}
          </button>
          <span className="subtle sm">放好 .md 文件后点「重新扫描」即可生效，无需重启</span>
        </div>

        {/* 写坏的技能文件必须显式告知：只写日志的话，用户只会看到"清单里少一条"。 */}
        {problems.length > 0 && (
          <div className="error-banner">
            以下技能文件格式不合法，已跳过：
            {problems.map((p) => (
              <div key={p.source}>
                {p.source} — {p.err}
              </div>
            ))}
          </div>
        )}

        {cat === null ? (
          err ? (
            <div className="error-banner">{err}</div>
          ) : (
            <p className="subtle">读取中…</p>
          )
        ) : list.length === 0 ? (
          <p className="subtle">
            当前没有可用技能。点「打开技能目录」，按其中 README.txt 的说明新建 .md 文件，
            再点「重新扫描」。
          </p>
        ) : (
          <div className="found-scroll">
            {list.map((sk) => (
              <div
                className={`found-item skill-item ${selected === sk.name ? "active" : ""}`}
                key={sk.name}
                onClick={() => setSelected(selected === sk.name ? "" : sk.name)}
              >
                <div className="found-item-head">
                  <strong>{skillDisplayName(sk.name)}</strong>
                  <span className="tag">{scopeLabel(sk.scope)}</span>
                  <span className="subtle sm">
                    {sk.agent} · {sk.source} · {sk.name}
                  </span>
                </div>
                <div className="found-hook">{sk.description}</div>
                {selected === sk.name && sk.body && <div className="found-prose">{sk.body}</div>}
              </div>
            ))}
          </div>
        )}

        {sent && (
          <div className="ok-banner">
            已提交技能 {sent}，AI 正在裁定范围并派单处理。回到活动流可看执行过程。
          </div>
        )}
        {note && <div className="ok-banner">{note}</div>}
        {cat !== null && err && <div className="error-banner">{err}</div>}

        {current && takesChapters && (
          <>
            <label className="form-label">章节范围（可选）</label>
            <input
              className="text-input"
              value={range}
              onChange={(e) => setRange(e.target.value)}
              placeholder={
                completedChapters > 0
                  ? `例如 3 / 3-5 / 3,5,7（已完成 ${completedChapters} 章，留空按最近几章）`
                  : "例如 3 / 3-5 / 3,5,7（留空按技能默认范围）"
              }
              disabled={busy}
              onKeyDown={(e) => {
                if (e.key === "Enter") void run();
              }}
            />
          </>
        )}

        <div className="modal-actions">
          <button className="ghost" onClick={onClose} disabled={busy}>
            关闭
          </button>
          <button className="primary" onClick={run} disabled={busy || !current}>
            {busy ? "提交中…" : current ? `执行 ${skillDisplayName(current.name)}` : "请选择技能"}
          </button>
        </div>
      </div>
    </Overlay>
  );
}

function skillDisplayName(name: string): string {
  switch (name) {
    case "anti-ai-tone":
      return "去 AI 味";
    case "tighten-pacing":
      return "收紧节奏";
    default:
      return name;
  }
}

function scopeLabel(scope: string): string {
  switch (scope) {
    case "chapters":
      return "已完成章节";
    case "forward":
      return "后续写作";
    case "foundation":
      return "设定层";
    default:
      return scope;
  }
}

// parseChapters 解析 "3" / "3-5" / "3,5,7"。解析失败必须抛错而不是当成"未指定"，
// 否则技能会作用到用户没打算改的章节上。
function parseChapters(text: string): number[] {
  const out: number[] = [];
  for (const raw of text.split(/[,，\s]+/)) {
    const part = raw.trim();
    if (!part) continue;
    const dash = part.match(/^(\d+)\s*[-~]\s*(\d+)$/);
    if (dash) {
      const start = Number(dash[1]);
      const end = Number(dash[2]);
      if (start < 1 || end < start) {
        throw new Error(`章节范围 ${part} 无效（起始须 ≥1 且不大于结束）`);
      }
      for (let ch = start; ch <= end; ch++) out.push(ch);
      continue;
    }
    if (!/^\d+$/.test(part) || Number(part) < 1) {
      throw new Error(`无法识别章节 ${part}（示例：3 / 3-5 / 3,5,7）`);
    }
    out.push(Number(part));
  }
  return out;
}
