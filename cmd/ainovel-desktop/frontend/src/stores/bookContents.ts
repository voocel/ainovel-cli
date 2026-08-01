import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { BookContents } from "../bindings/wails";

// useBookContents 拉取已完成章节目录（书名 / 章节列表 / 总字数 / 是否分层）。
//
// 以 completedCount 为刷新键：写完一章后目录自动多一条，不需要手动刷新。
// 目录与正文取自 store 的 chapters/NN.md（与导出同源），不是 engine:stream——
// 那条流是有损的，只能当"正在写什么"的预览。
export function useBookContents(completedCount: number): BookContents | null {
  const [contents, setContents] = useState<BookContents | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .GetContents()
      .then((c) => {
        if (alive) setContents(c);
      })
      .catch(() => {
        /* 目录拉取失败不该打断创作，正文区自己会报错 */
      });
    return () => {
      alive = false;
    };
  }, [completedCount]);

  return contents;
}
