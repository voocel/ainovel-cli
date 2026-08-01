import { useEffect, useRef, type ReactNode } from "react";

// Overlay 是所有浮层的统一容器。
//
// 之前每个面板各写一遍 <div className="modal-overlay">，导致三件事各不相同：
// 键盘能不能关、点空白能不能关、层级谁压谁。这里收敛成一处，并按**语义**分层，
// 而不是按"谁后写谁在上"：
//
//   sheet    任务面板（导出/封面/仿写/技能/导入/设定）——点空白可关、Esc 可关
//   settings 设置页——整屏替换级
//   reader   阅读器——沉浸式全屏
//   blocking 引擎阻塞式提问（AskUser）——**必须压在所有东西之上**
//
// blocking 高于 settings 是关键：AskUser 弹出时引擎在阻塞等待，如果它被设置页盖住，
// 用户会看到一个"卡死"的应用而不知道有人在问他话。
export type OverlayLayer = "sheet" | "settings" | "reader" | "blocking";

const LAYER_Z: Record<OverlayLayer, number> = {
  sheet: 100,
  settings: 200,
  reader: 300,
  blocking: 900,
};

// 可聚焦元素选择器，用于焦点环绕（focus trap）。
const FOCUSABLE =
  'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])';

// 当前挂载中的浮层栈。所有浮层都把 keydown 挂在 window 上，而同一个 target 上的
// 多个监听器之间 stopPropagation 是无效的——只有 stopImmediatePropagation 才拦得住，
// 且那要求"先注册的一定在下层"，并不成立。所以显式维护栈，只让最上层那个响应键盘。
//
// 这不是理论问题：共创面板（settings 层）开着时引擎抛出 AskUser（blocking 层），
// 按 Esc 会先走到 AskUser——它没有 onClose，直接放行——再走到共创面板，把共创关掉。
// 引擎却还阻塞着等那个没人回答的问题。
type StackEntry = { id: symbol; z: number };
const stack: StackEntry[] = [];

function isTopmost(id: symbol): boolean {
  if (stack.length === 0) return false;
  // 视觉最上层 = z 最大者；同 z 取最后挂载的那个。
  let top = stack[0];
  for (const e of stack) if (e.z >= top.z) top = e;
  return top.id === id;
}

export function Overlay({
  layer,
  onClose,
  labelledBy,
  className,
  backdrop = true,
  children,
}: {
  layer: OverlayLayer;
  /** 省略表示不可由用户主动关闭（阻塞式浮层必须走自己的提交/跳过路径）。 */
  onClose?: () => void;
  labelledBy?: string;
  className?: string;
  /** false 时不画半透明背景（整屏替换级浮层用，如设置页与阅读器）。 */
  backdrop?: boolean;
  children: ReactNode;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  // 打开前的焦点，关闭时归还——不然焦点会掉到 body，键盘用户直接迷路。
  const restoreRef = useRef<Element | null>(null);
  const idRef = useRef<symbol>(Symbol("overlay"));

  const z = LAYER_Z[layer];

  useEffect(() => {
    const id = idRef.current;
    stack.push({ id, z });
    restoreRef.current = document.activeElement;
    // 焦点移入浮层：优先第一个可聚焦控件，否则聚焦容器本身。
    const box = boxRef.current;
    const first = box?.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? box)?.focus();
    return () => {
      const at = stack.findIndex((e) => e.id === id);
      if (at >= 0) stack.splice(at, 1);
      const prev = restoreRef.current;
      if (prev instanceof HTMLElement && document.contains(prev)) prev.focus();
    };
  }, [z]);

  // Esc 关闭 + Tab 环绕。用捕获阶段，保证浮层优先于底层界面的全局快捷键。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // 只有最上层的浮层响应键盘：下面盖着的那些既不该被 Esc 关掉，
      // 也不该把 Tab 焦点抢回自己的框里。
      if (!isTopmost(idRef.current)) return;
      if (e.key === "Escape") {
        // 无论关不关得掉都吃掉这个 Esc：阻塞式浮层压在最上面时，
        // Esc 不能穿透下去误关别的东西。
        e.stopPropagation();
        e.preventDefault();
        onClose?.();
        return;
      }
      if (e.key !== "Tab") return;
      const box = boxRef.current;
      if (!box) return;
      const items = [...box.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
        (el) => el.offsetParent !== null || el === document.activeElement,
      );
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      // 焦点不在本浮层内（比如被下层元素抢走了）时，先拽回来。
      if (!box.contains(document.activeElement)) {
        e.preventDefault();
        first.focus();
        return;
      }
      if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      } else if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [onClose]);

  return (
    <div
      className={`overlay-root ${backdrop ? "has-backdrop" : ""} layer-${layer}`}
      style={{ zIndex: LAYER_Z[layer] }}
      // 点空白关闭只对"任务面板"成立：整屏级浮层没有空白可点，
      // 阻塞式浮层则根本不该被误触关掉。
      onMouseDown={
        backdrop && onClose
          ? (e) => {
              if (e.target === e.currentTarget) onClose();
            }
          : undefined
      }
    >
      <div
        ref={boxRef}
        className={className}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        tabIndex={-1}
      >
        {children}
      </div>
    </div>
  );
}
