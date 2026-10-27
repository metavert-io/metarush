import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { Check, DotsThree } from '@phosphor-icons/react';
import { cx } from '../../ui/primitives';

export interface MenuItem {
  label: string;
  icon?: ReactNode;
  onSelect: () => void;
  checked?: boolean;
  danger?: boolean;
  disabled?: boolean;
  hint?: string;
}

/** A keyboard-accessible menu button (arrow keys, Enter, Esc). */
export function MenuButton({ items, label = 'More' }: { items: (MenuItem | 'separator')[]; label?: string }) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  // Where the menu opens: right-aligned with the button, moved so it stays on screen (a phone).
  const [place, setPlace] = useState<{ width: number; right: number }>({ width: 260, right: 0 });
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const id = useId();
  const real = items.filter((i): i is MenuItem => i !== 'separator');

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!menu.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);
  useEffect(() => {
    if (open) menu.current?.querySelector<HTMLElement>(`[data-i="${active}"]`)?.focus();
  }, [open, active]);

  const choose = (i: MenuItem) => {
    if (i.disabled) return;
    setOpen(false);
    btn.current?.focus();
    i.onSelect();
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      btn.current?.focus();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((a) => (a + 1) % real.length);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((a) => (a - 1 + real.length) % real.length);
    } else if (e.key === 'Home') {
      e.preventDefault();
      setActive(0);
    } else if (e.key === 'End') {
      e.preventDefault();
      setActive(real.length - 1);
    } else if (e.key === 'Tab') {
      setOpen(false);
    }
  };

  const placeMenu = () => {
    const r = btn.current?.getBoundingClientRect();
    if (!r) return;
    const vw = window.innerWidth;
    const width = Math.min(260, vw - 16);
    let shift = Math.max(0, 8 - (r.right - width)); // its left edge on screen
    shift -= Math.max(0, r.right + shift - (vw - 8)); // and its right edge
    setPlace({ width, right: -shift });
  };

  let n = -1;
  return (
    <div className="relative">
      <button
        ref={btn}
        type="button"
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => {
          setActive(0);
          placeMenu();
          setOpen((v) => !v);
        }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && !open) {
            e.preventDefault();
            setActive(0);
            placeMenu();
            setOpen(true);
          }
        }}
        className={cx('grid h-8 w-8 place-items-center rounded-[9px] text-muted transition-colors hover:bg-hover hover:text-fg', open && 'bg-hover text-fg')}
      >
        <DotsThree size={18} weight="bold" />
      </button>
      {open ? (
        <div
          ref={menu}
          id={id}
          role="menu"
          aria-label={label}
          onKeyDown={onKey}
          style={{ width: place.width, right: place.right }}
          // Above sticky bars under the top bar (a project's tabs, z-30), below the top bar itself (z-40).
          className="absolute top-full z-[35] mt-1.5 overflow-hidden rounded-[12px] bg-raised py-1 shadow-[var(--shadow-float)] hairline"
        >
          {items.map((it, k) => {
            if (it === 'separator') return <div key={`s${k}`} role="separator" className="my-1 h-px bg-line" />;
            n++;
            const idx = n;
            return (
              <button
                key={it.label}
                type="button"
                role={it.checked === undefined ? 'menuitem' : 'menuitemcheckbox'}
                aria-checked={it.checked === undefined ? undefined : it.checked}
                aria-disabled={it.disabled || undefined}
                title={it.disabled ? it.hint : undefined}
                data-i={idx}
                tabIndex={idx === active ? 0 : -1}
                onMouseEnter={() => setActive(idx)}
                onClick={() => choose(it)}
                className={cx(
                  'flex w-full items-center gap-2.5 px-3 py-[7px] text-left text-[13px] outline-none',
                  it.disabled ? 'cursor-not-allowed text-subtle' : it.danger ? 'text-danger hover:bg-danger-soft focus:bg-danger-soft' : 'text-fg hover:bg-hover focus:bg-hover',
                )}
              >
                <span className="grid w-4 shrink-0 place-items-center text-subtle" aria-hidden>
                  {it.checked !== undefined ? it.checked ? <Check size={13} weight="bold" className="text-accent-text" /> : null : it.icon}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{it.label}</span>
                  {it.hint ? <span className="block truncate text-[11.5px] text-subtle">{it.hint}</span> : null}
                </span>
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
