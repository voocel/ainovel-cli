import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { LibraryBook } from "../bindings/wails";
import { formatNumber, phaseLabel } from "../lib/labels";
import { CoverPanel } from "../components/CoverPanel";
import { ReaderPanel } from "../components/ReaderPanel";

// samePath 判断两个书目录是否是同一本书。
//
// 对应 Go 侧的 sameDir（library.go），但只做前端够用的那部分：这里两边的路径都来自
// 后端（书库清单 / cover 事件里的 h.Dir()），不需要处理相对路径。统一分隔符 +
// 去尾部斜杠 + 忽略大小写（Windows 路径大小写不敏感）。
function samePath(a: string, b: string): boolean {
  const norm = (p: string) =>
    p.replace(/[\\/]+/g, "/").replace(/\/+$/, "").toLowerCase();
  return !!a && !!b && norm(a) === norm(b);
}

// LibraryScreen 书库首页：每本书绑定一个目录，卡片展示进度。
// 引擎"一本书=一个输出目录"的语义不变，这里只是把"手动换目录"变成显式的选择。
export function LibraryScreen({
  onOpened,
  onOpenSettings,
}: {
  onOpened: (hasProgress: boolean) => void;
  onOpenSettings: () => void;
}) {
  const [books, setBooks] = useState<LibraryBook[]>([]);
  const [booksDir, setBooksDir] = useState("");
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  // 自定义输出目录：留空则落在默认 books 根目录下（按书名生成子目录）。
  const [newDir, setNewDir] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // 非空 = 正在给该目录的书配封面
  const [coverFor, setCoverFor] = useState<string | null>(null);
  // 非空 = 正在阅读该目录的书
  const [readFor, setReadFor] = useState<string | null>(null);
  // 生图进行中的书目录。切到别的书会重建 Host 并取消在途生图（那张图已经计费），
  // 所以这期间要拦住切书动作，而不是让用户在不知情的情况下把图丢掉。
  const [genFor, setGenFor] = useState<string | null>(null);

  const load = async () => {
    try {
      const [list, dir] = await Promise.all([api.ListBooks(), api.DefaultBooksDir()]);
      setBooks(list ?? []);
      setBooksDir(dir);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  useEffect(() => {
    void load();
  }, []);

  // 跟踪在途生图。用全局事件而不是 CoverPanel 的内部状态：面板关掉后生图仍在跑，
  // 这时候切书同样会取消它。
  //
  // 挂载时先查一次：心跳每秒才一次，而且生图可能是在创作台发起、之后才切回书库的，
  // 光等事件会留下一个"看起来没在生图"的空窗，点一下就把图丢了。
  useEffect(() => {
    let alive = true;
    void api
      .CoverJobDir()
      .then((d) => {
        // 期间事件已经给出更准的答案就别覆盖回去（尤其别把 cover:done 的清空覆盖掉）。
        if (alive && d) setGenFor((cur) => cur ?? d);
      })
      .catch(() => {});
    const offProgress = api.on("cover:progress", (d: api.CoverProgress) => {
      if (d?.bookDir) setGenFor(d.bookDir);
    });
    const offDone = api.on("cover:done", () => setGenFor(null));
    return () => {
      alive = false;
      offProgress();
      offDone();
    };
  }, []);

  // 生图期间切书会重建 Host 并取消在途生图。拦下来并说清原因，
  // 而不是让用户在不知情的情况下丢掉一张已经计费的图。
  // 后端 OpenBook 也拦了同一条规则，这里只是把提示做得更早、更好看。
  const blockedBySwitch = (path: string): boolean => {
    if (genFor && !samePath(genFor, path)) {
      setErr("正在生成封面，切换到其他书会中断它。请等生成结束，或先在封面面板点取消。");
      return true;
    }
    return false;
  };

  const open = async (path: string) => {
    if (busy) return;
    if (blockedBySwitch(path)) return;
    setBusy(true);
    setErr(null);
    try {
      const result = await api.OpenBook(path);
      // 点击书卡表示明确进入创作；只有确有恢复点且尚未运行时才启动引擎。
      // 已完结书没有恢复标签，但仍进入工作台供阅读、导出或重开。
      if (result.recoveryLabel && !result.isRunning && result.phase !== "complete") {
        await api.ResumeBook();
      }
      onOpened(result.hasProgress);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  const create = async () => {
    if (busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.CreateBook(newName.trim(), newDir.trim());
      onOpened(false);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  const openFolder = async () => {
    if (busy) return;
    try {
      const dir = await api.PickDirectory("选择一本书的目录");
      if (dir) await open(dir);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  // 在书库直接配封面：封面接口都需要"当前已打开的书"，所以先打开这本书再开面板。
  // 已经是这本书时 OpenBook 是空操作（后端走同书快路径，不重建 Host），
  // 所以这里点开只是切上下文，不会启动创作、也不会打断在途作业。
  const openCoverFor = async (path: string) => {
    if (busy) return;
    if (blockedBySwitch(path)) return;
    setBusy(true);
    setErr(null);
    try {
      await api.OpenBook(path);
      setCoverFor(path);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  // 在书库直接阅读：与配封面同理，阅读接口需要"当前已打开的书"，先切上下文再开面板。
  // OpenBook 只挂载上下文，所以点开只是读，不会触发写作。
  const openReaderFor = async (path: string) => {
    if (busy) return;
    if (blockedBySwitch(path)) return;
    setBusy(true);
    setErr(null);
    try {
      await api.OpenBook(path);
      setReadFor(path);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const forget = async (path: string) => {
    try {
      await api.ForgetBook(path);
      await load();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  return (
    <div className="library">
      <header className="topbar">
        <div className="topbar-left">
          <strong>我的书库</strong>
          <span className="subtle sm">{books.length} 本</span>
        </div>
        <button className="ghost sm" onClick={onOpenSettings}>
          设置
        </button>
      </header>

      <div className="library-body">
        {err && <div className="error-banner">{err}</div>}

        {creating ? (
          <div className="create-card">
            <h3>新建一本书</h3>
            <label className="form-label">书名（用于目录名，可留空）</label>
            <input
              className="text-input"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder="例如：光斑"
              disabled={busy}
              autoFocus
              onKeyDown={(e) => {
                if (e.key === "Enter") void create();
              }}
            />
            <label className="form-label">
              输出目录<span className="subtle sm"> · 留空用默认位置</span>
            </label>
            <div className="inline-actions">
              <input
                className="text-input"
                value={newDir}
                onChange={(e) => setNewDir(e.target.value)}
                placeholder={booksDir ? `${booksDir}\\<书名>` : "默认位置"}
                disabled={busy}
              />
              <button
                onClick={async () => {
                  try {
                    const d = await api.PickDirectory("选择这本书的输出目录");
                    if (d) setNewDir(d);
                  } catch (e) {
                    setErr(String((e as Error)?.message ?? e));
                  }
                }}
                disabled={busy}
              >
                浏览…
              </button>
            </div>
            <p className="subtle sm">
              这本书的所有产物（章节、大纲、封面）都会存在这个目录里，方便你自己备份或放到网盘。
            </p>
            <div className="inline-actions">
              <button className="ghost" onClick={() => setCreating(false)} disabled={busy}>
                取消
              </button>
              <button className="primary" onClick={create} disabled={busy}>
                {busy ? "创建中…" : "创建并开始"}
              </button>
            </div>
          </div>
        ) : (
          <div className="library-actions">
            <button className="primary" onClick={() => setCreating(true)} disabled={busy}>
              + 新建一本书
            </button>
            <button className="ghost" onClick={openFolder} disabled={busy}>
              打开已有目录…
            </button>
          </div>
        )}

        {books.length === 0 && !creating && (
          <div className="empty-note subtle">
            还没有书。点「新建一本书」开始，或打开一个已有的创作目录
            （比如你之前用命令行版创作的 output/novel）。
          </div>
        )}

        {/* 书架：每本书是一个立体书壳（封面 + 左侧书脊 + 投影），下面挂书名与进度。
            无封面时用题名首字做一张排版封面，而不是留一个灰块——书架上不该有空壳。 */}
        <div className="shelf">
          {books.map((b) => {
            const generating = !!genFor && samePath(genFor, b.path);
            return (
              <div key={b.path} className={`shelf-item ${b.missing ? "missing" : ""}`}>
                <div
                  className="book-3d"
                  role="button"
                  tabIndex={b.missing ? -1 : 0}
                  title={b.missing ? "目录已不存在" : `打开《${b.name || "未命名"}》`}
                  onClick={() => !b.missing && void open(b.path)}
                  onKeyDown={(e) => {
                    if (b.missing) return;
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      void open(b.path);
                    }
                  }}
                >
                  <div className="book-face">
                    {b.coverURL ? (
                      <img src={b.coverURL} alt="" />
                    ) : (
                      // 没有封面图时本地拼一张：竖排书名 + 纸色底，比灰底占位更像书。
                      <div className="book-face-blank">
                        <span className="blank-title">{b.name || "未命名"}</span>
                        <span className="blank-rule" />
                      </div>
                    )}
                    <span className="book-spine" aria-hidden="true" />
                    <span className="book-gloss" aria-hidden="true" />
                    {generating && <span className="book-badge">生成封面中…</span>}
                    {b.missing && <span className="book-badge danger">目录丢失</span>}
                  </div>
                  <span className="book-shadow" aria-hidden="true" />
                </div>

                <div className="shelf-info">
                  <div className="book-title" title={b.name || "未命名"}>
                    {b.name || "未命名"}
                  </div>
                  {b.missing ? (
                    <div className="book-meta danger">目录已不存在</div>
                  ) : (
                    <div className="book-meta">
                      {b.chapters > 0 ? `${b.chapters} 章` : "尚未开始"}
                      {b.words > 0 && ` · ${formatNumber(b.words)} 字`}
                      {b.phase && ` · ${phaseLabel(b.phase)}`}
                      {b.lastOpened &&
                        ` · ${new Date(b.lastOpened).toLocaleDateString("zh-CN")}`}
                    </div>
                  )}
                  <div className="shelf-actions">
                    {!b.missing && (
                      <button
                        className="link sm"
                        onClick={() => void openCoverFor(b.path)}
                        title={b.coverURL ? "更换封面" : "生成封面"}
                      >
                        {b.coverURL ? "换封面" : "配封面"}
                      </button>
                    )}
                    {b.chapters > 0 && (
                      <button
                        className="link sm"
                        onClick={() => void openReaderFor(b.path)}
                        title="阅读已完成的章节"
                      >
                        阅读
                      </button>
                    )}
                    <button
                      className="link sm subtle"
                      onClick={() => void forget(b.path)}
                      title={`从书库移除（不删除文件）\n${b.path}`}
                    >
                      移除
                    </button>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      </div>

      {coverFor && (
        <CoverPanel
          onClose={() => setCoverFor(null)}
          onChanged={() => void load()}
        />
      )}

      {readFor && <ReaderPanel onClose={() => setReadFor(null)} />}
    </div>
  );
}
