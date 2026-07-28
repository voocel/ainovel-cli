import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";

// useEngineFeed 订阅事件泵推来的四类信号，聚合成 M1 界面所需的状态：
//   - engine:event        → 事件列表（按 id 原地合并：同 id 的完成事件更新进行中的那条）
//   - engine:stream       → 当前流式轮次追加文本
//   - engine:stream:clear → 开启新的流式轮次
//   - engine:snapshot/done→ 快照（权威状态）
//
// 事件流有损（Go 侧满则丢最旧），因此只用于展示；权威状态一律取自 snapshot。
export type AskRequest = api.AskRequest;

export function useEngineFeed() {
  const [events, setEvents] = useState<api.EngineEvent[]>([]);
  const [rounds, setRounds] = useState<string[]>([""]);
  const [snapshot, setSnapshot] = useState<api.UISnapshot | null>(null);
  const [ask, setAsk] = useState<AskRequest | null>(null);

  // 事件 id → 数组下标，O(1) 合并。
  const indexRef = useRef<Map<string, number>>(new Map());

  useEffect(() => {
    const offs: Array<() => void> = [];

    offs.push(
      api.on("engine:event", (ev: api.EngineEvent) => {
        setEvents((prev) => {
          const idx = indexRef.current;
          if (ev.id && idx.has(ev.id)) {
            const at = idx.get(ev.id)!;
            const next = prev.slice();
            next[at] = ev;
            return next;
          }
          const next = [...prev, ev];
          if (ev.id) idx.set(ev.id, next.length - 1);
          // 限制上限，避免长跑内存膨胀。
          if (next.length > 800) {
            const trimmed = next.slice(next.length - 800);
            idx.clear();
            trimmed.forEach((e, i) => e.id && idx.set(e.id, i));
            return trimmed;
          }
          return next;
        });
      }),
    );

    offs.push(
      api.on("engine:stream", (delta: string) => {
        setRounds((prev) => {
          const next = prev.slice();
          next[next.length - 1] = (next[next.length - 1] ?? "") + delta;
          return next;
        });
      }),
    );

    offs.push(
      api.on("engine:stream:clear", () => {
        setRounds((prev) => {
          const next = [...prev, ""];
          // 只保留最近 32 轮。
          return next.length > 32 ? next.slice(next.length - 32) : next;
        });
      }),
    );

    offs.push(
      api.on("engine:askuser", (req: AskRequest) => {
        setAsk(req);
      }),
    );

    const applySnap = (snap: api.UISnapshot) => setSnapshot(snap);
    offs.push(api.on("engine:snapshot", applySnap));
    offs.push(
      api.on("engine:done", (snap: api.UISnapshot) => {
        applySnap(snap);
        // 引擎已停（含 abort）：此时后端的提问桥已因 ctx 取消而解除阻塞，
        // 弹窗留在界面上只会误导用户，直接收起。
        setAsk(null);
      }),
    );

    // 首屏拉一次快照。
    api.GetSnapshot().then(applySnap).catch(() => {});
    api.GetPendingAskUser().then((pending) => pending && setAsk(pending)).catch(() => {});

    return () => offs.forEach((off) => off());
  }, []);

  return { events, rounds, snapshot, ask, clearAsk: () => setAsk(null) };
}
