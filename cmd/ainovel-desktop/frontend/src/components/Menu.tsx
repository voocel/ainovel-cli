import { useEffect, useRef, useState, type ReactNode } from "react";

export type MenuItem = {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  title?: string;
};

// MoreMenu 把低频动作收进一个下拉，避免命令栏平铺一长串同等权重的按钮。
// 点外部或 Esc 关闭；上下键在条目间移动。
export function MoreMenu({ items, label = "更多" }: { items: MenuItem[]; label?: string }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        return;
      }
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      const list = listRef.current;
      if (!list) return;
      e.preventDefault();
      const btns = [...list.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
      if (btns.length === 0) return;
      const at = btns.indexOf(document.activeElement as HTMLButtonElement);
      const next =
        e.key === "ArrowDown"
          ? btns[(at + 1 + btns.length) % btns.length]
          : btns[(at - 1 + btns.length) % btns.length];
      next.focus();
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);

  return (
    <div className="more-menu" ref={rootRef}>
      <button aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        {label} <span className="caret" aria-hidden="true">▾</span>
      </button>
      {open && (
        <div className="more-list" role="menu" ref={listRef}>
          {items.map((it) => (
            <button
              key={it.label}
              role="menuitem"
              title={it.title}
              disabled={it.disabled}
              onClick={() => {
                setOpen(false);
                it.onClick();
              }}
            >
              {it.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// Segmented 是分段控件（互斥单选）。用于主区「正文 / 实时输出」这类视图切换。
export function Segmented<T extends string>({
  value,
  options,
  onChange,
  ariaLabel,
}: {
  value: T;
  options: { key: T; label: ReactNode; title?: string }[];
  onChange: (key: T) => void;
  ariaLabel?: string;
}) {
  return (
    <div className="segmented" role="tablist" aria-label={ariaLabel}>
      {options.map((o) => (
        <button
          key={o.key}
          role="tab"
          aria-selected={value === o.key}
          title={o.title}
          className={value === o.key ? "active" : ""}
          onClick={() => onChange(o.key)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
