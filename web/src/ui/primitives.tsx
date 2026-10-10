import { LoaderCircle, type LucideIcon } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

/** Opens a dialog, focusing its `data-autofocus` field, or else the dialog
 *  itself: showModal would focus its first button, such as Cancel, and React's
 *  autoFocus runs before the dialog is open. */
export function showModal(d: HTMLDialogElement) {
  d.showModal();
  const target = d.querySelector<HTMLElement>("[data-autofocus]");
  if (target) target.focus();
  else d.focus();
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <LoaderCircle className="spinner" size={size} aria-label="Loading" />;
}

/** A sheet over the page, as the apps' sheets: a full screen on a phone. */
export function Modal({
  title,
  onClose,
  children,
  leading,
  trailing,
  wide = false,
}: {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  /** Toolbar buttons beside the title. Leading defaults to Cancel. */
  leading?: ReactNode;
  trailing?: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    showModal(d);
    const cancel = (e: Event) => {
      e.preventDefault();
      close.current();
    };
    d.addEventListener("cancel", cancel);
    return () => {
      d.removeEventListener("cancel", cancel);
      d.close();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      tabIndex={-1}
      className={"modal" + (wide ? " modal-wide" : "")}
      onMouseDown={(e) => {
        if (e.target === ref.current) onClose(); // the backdrop
      }}
    >
      <div className="modal-body">
        <header className="modal-bar">
          <div className="modal-bar-side">{leading ?? <button className="link" onClick={onClose}>Cancel</button>}</div>
          <h2>{title}</h2>
          <div className="modal-bar-side end">{trailing}</div>
        </header>
        <div className="modal-content">{children}</div>
      </div>
    </dialog>
  );
}

export interface MenuItem {
  label: ReactNode;
  icon?: LucideIcon;
  detail?: ReactNode;
  onSelect?: () => void;
  destructive?: boolean;
  disabled?: boolean;
  checked?: boolean;
}

export type MenuEntry = MenuItem | { section: ReactNode; items: MenuItem[] } | "divider" | null | false | undefined;

/** A button that opens a list of actions, placed to stay in the window. */
export function Menu({
  label,
  entries,
  className,
  title,
  align = "end",
  disabled,
}: {
  label: ReactNode;
  entries: MenuEntry[];
  className?: string;
  title?: string;
  align?: "start" | "end";
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<React.CSSProperties>({ visibility: "hidden" });

  useLayoutEffect(() => {
    if (!open || !button.current || !popup.current) return;
    const b = button.current.getBoundingClientRect();
    const p = popup.current.getBoundingClientRect();
    const margin = 8;
    let left = align === "end" ? b.right - p.width : b.left;
    left = Math.max(margin, Math.min(left, innerWidth - p.width - margin));
    let top = b.bottom + 4;
    if (top + p.height > innerHeight - margin && b.top - p.height - 4 > margin) top = b.top - p.height - 4;
    top = Math.max(margin, Math.min(top, innerHeight - p.height - margin));
    setPos({ left, top });
  }, [open, align]);

  useEffect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => {
      if (!popup.current?.contains(e.target as Node) && !button.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setOpen(false);
        button.current?.focus();
      }
    };
    const away = () => setOpen(false);
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key, true);
    window.addEventListener("resize", away);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key, true);
      window.removeEventListener("resize", away);
    };
  }, [open]);

  useEffect(() => {
    if (open) popup.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    else setPos({ visibility: "hidden" });
  }, [open]);

  const item = (it: MenuItem, i: number) => (
    <button
      key={i}
      role="menuitem"
      className={"menu-item" + (it.destructive ? " destructive" : "")}
      disabled={it.disabled || !it.onSelect}
      onClick={() => {
        setOpen(false);
        it.onSelect?.();
      }}
    >
      <span className="menu-icon">{it.checked ? "✓" : it.icon ? <it.icon size={15} /> : null}</span>
      <span className="menu-label">
        {it.label}
        {it.detail && <span className="menu-detail">{it.detail}</span>}
      </span>
    </button>
  );

  return (
    <>
      <button
        ref={button}
        className={className ?? "icon-button"}
        title={title}
        aria-label={title}
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
      >
        {label}
      </button>
      {open && (
        <div ref={popup} className="menu" role="menu" style={pos}>
          {entries.map((e, i) => {
            if (!e) return null;
            if (e === "divider") return <hr key={i} />;
            if ("section" in e) {
              if (e.items.length === 0) return null;
              return (
                <div key={i} className="menu-section">
                  {e.section && <div className="menu-section-title">{e.section}</div>}
                  {e.items.map(item)}
                </div>
              );
            }
            return item(e, i);
          })}
        </div>
      )}
    </>
  );
}

export interface ConfirmAction {
  label: string;
  destructive?: boolean;
  onSelect: () => void;
}

/** A question with its answers, like the apps' confirmation dialogs. */
export function Confirm({
  title,
  message,
  actions,
  onClose,
  children,
  cancelLabel = "Cancel",
}: {
  title: string;
  message?: ReactNode;
  actions: ConfirmAction[];
  onClose: () => void;
  children?: ReactNode;
  cancelLabel?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    showModal(d);
    const cancel = (e: Event) => {
      e.preventDefault();
      close.current();
    };
    d.addEventListener("cancel", cancel);
    return () => {
      d.removeEventListener("cancel", cancel);
      d.close();
    };
  }, []);
  return (
    <dialog ref={ref} tabIndex={-1} className="confirm" onMouseDown={(e) => e.target === ref.current && onClose()}>
      <h3>{title}</h3>
      {message && <p>{message}</p>}
      {children}
      <div className="confirm-actions">
        <button className={"button" + (actions.length ? "" : " prominent")} onClick={onClose}>
          {cancelLabel}
        </button>
        {actions.map((a) => (
          <button
            key={a.label}
            className={"button " + (a.destructive ? "destructive" : "prominent")}
            onClick={() => {
              onClose();
              a.onSelect();
            }}
          >
            {a.label}
          </button>
        ))}
      </div>
    </dialog>
  );
}

/** A message in place of empty content, like ContentUnavailableView. */
export function Empty({ icon: Icon, title, children }: { icon?: LucideIcon; title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      {Icon && <Icon size={40} strokeWidth={1.5} className="empty-icon" />}
      <h3>{title}</h3>
      {children}
    </div>
  );
}

/** Shows an error until dismissed, like the apps' alerts. */
export function ErrorAlert({ error, onClose }: { error: string | undefined; onClose: () => void }) {
  if (!error) return null;
  return <Confirm title="Error" message={error} actions={[]} onClose={onClose} cancelLabel="OK" />;
}
