import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { CheckCircle, Info, WarningCircle, X } from '@phosphor-icons/react';
import { cx } from './primitives';

export interface ToastOptions {
  title: string;
  body?: ReactNode;
  tone?: 'info' | 'success' | 'error';
  action?: { label: string; onClick: () => void };
  /** More than one action ("Undo", "Move…"); shown after `action`. */
  actions?: { label: string; onClick: () => void }[];
  durationMs?: number;
}
interface ToastEntry extends ToastOptions {
  id: number;
}

const Ctx = createContext<((t: ToastOptions) => void) | null>(null);

export function useToast(): (t: ToastOptions) => void {
  const fn = useContext(Ctx);
  if (!fn) throw new Error('useToast must be used inside <ToastProvider>');
  return fn;
}

let seq = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastEntry[]>([]);
  const dismiss = useCallback((id: number) => setToasts((t) => t.filter((x) => x.id !== id)), []);
  const push = useCallback((t: ToastOptions) => {
    setToasts((list) => [...list.slice(-3), { ...t, id: ++seq }]);
  }, []);
  const value = useMemo(() => push, [push]);
  return (
    <Ctx.Provider value={value}>
      {children}
      {createPortal(
        <div data-toasts aria-live="polite" aria-relevant="additions" className="pointer-events-none fixed inset-x-0 bottom-0 z-[80] flex flex-col items-center gap-2 p-4 sm:items-end sm:p-5">
          {toasts.map((t) => (
            <ToastItem key={t.id} t={t} onDismiss={() => dismiss(t.id)} />
          ))}
        </div>,
        document.body,
      )}
    </Ctx.Provider>
  );
}

function ToastItem({ t, onDismiss }: { t: ToastEntry; onDismiss: () => void }) {
  const [paused, setPaused] = useState(false);
  const remaining = useRef(t.durationMs ?? (t.tone === 'error' ? 9000 : 5200));
  const started = useRef(Date.now());
  useEffect(() => {
    if (paused) return;
    started.current = Date.now();
    const timer = setTimeout(onDismiss, remaining.current);
    return () => {
      clearTimeout(timer);
      remaining.current -= Date.now() - started.current;
    };
  }, [paused, onDismiss]);
  const icon =
    t.tone === 'success' ? <CheckCircle size={18} weight="fill" className="text-ok" /> : t.tone === 'error' ? <WarningCircle size={18} weight="fill" className="text-danger" /> : <Info size={18} weight="fill" className="text-accent" />;
  return (
    <div
      role={t.tone === 'error' ? 'alert' : 'status'}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
      className={cx('fade-up pointer-events-auto flex w-full max-w-[380px] items-start gap-3 rounded-[14px] bg-raised p-3.5 pr-2.5 shadow-[var(--shadow-float)] hairline')}
    >
      <div className="mt-[1px] shrink-0">{icon}</div>
      <div className="min-w-0 flex-1">
        <div className="break-words text-[13.5px] font-medium text-fg">{t.title}</div>
        {t.body ? <div className="mt-0.5 break-words text-[12.5px] leading-relaxed text-muted">{t.body}</div> : null}
        {t.action || t.actions?.length ? (
          <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1">
            {[...(t.action ? [t.action] : []), ...(t.actions ?? [])].map((a) => (
              <button
                key={a.label}
                type="button"
                onClick={() => {
                  a.onClick();
                  onDismiss();
                }}
                className="text-[12.5px] font-medium text-accent-text hover:underline"
              >
                {a.label}
              </button>
            ))}
          </div>
        ) : null}
      </div>
      <button type="button" onClick={onDismiss} aria-label="Dismiss notification" className="rounded-[7px] p-1 text-subtle hover:bg-hover hover:text-fg">
        <X size={14} />
      </button>
    </div>
  );
}
