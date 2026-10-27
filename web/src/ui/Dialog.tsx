import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { X } from '@phosphor-icons/react';
import { Button, cx } from './primitives';

/*
 * The only dialog system in the dashboard: styled, focus-trapped, accessible
 * replacements for alert / confirm / prompt (native ones are never used).
 */

export interface AlertOptions {
  title: string;
  body?: ReactNode;
  okLabel?: string;
}
export interface ConfirmOptions {
  title: string;
  body?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: 'primary' | 'danger';
}
export interface PromptOptions {
  title: string;
  body?: ReactNode;
  label: string;
  placeholder?: string;
  defaultValue?: string;
  multiline?: boolean;
  required?: boolean;
  maxLength?: number;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: 'primary' | 'danger';
  helper?: ReactNode;
  validate?: (v: string) => string | null;
}

export interface DialogApi {
  alert(o: AlertOptions): Promise<void>;
  confirm(o: ConfirmOptions): Promise<boolean>;
  prompt(o: PromptOptions): Promise<string | null>;
  /** Custom content. Resolve with close(value); Esc / backdrop resolve undefined. */
  open<T>(render: (close: (v?: T) => void) => ReactNode, o?: { title?: string; size?: 'sm' | 'md' | 'lg'; label?: string }): Promise<T | undefined>;
}

type Entry =
  | { id: number; kind: 'alert'; o: AlertOptions; resolve: (v: void) => void }
  | { id: number; kind: 'confirm'; o: ConfirmOptions; resolve: (v: boolean) => void }
  | { id: number; kind: 'prompt'; o: PromptOptions; resolve: (v: string | null) => void }
  | { id: number; kind: 'custom'; render: (close: (v?: unknown) => void) => ReactNode; o: { title?: string; size?: 'sm' | 'md' | 'lg'; label?: string }; resolve: (v: unknown) => void };

const Ctx = createContext<DialogApi | null>(null);

export function useDialog(): DialogApi {
  const api = useContext(Ctx);
  if (!api) throw new Error('useDialog must be used inside <DialogProvider>');
  return api;
}

let seq = 0;

export function DialogProvider({ children }: { children: ReactNode }) {
  const [stack, setStack] = useState<Entry[]>([]);
  const remove = useCallback((id: number) => setStack((s) => s.filter((e) => e.id !== id)), []);

  const api = useMemo<DialogApi>(
    () => ({
      alert: (o) => new Promise((resolve) => setStack((s) => [...s, { id: ++seq, kind: 'alert', o, resolve }])),
      confirm: (o) => new Promise((resolve) => setStack((s) => [...s, { id: ++seq, kind: 'confirm', o, resolve }])),
      prompt: (o) => new Promise((resolve) => setStack((s) => [...s, { id: ++seq, kind: 'prompt', o, resolve }])),
      open: <T,>(render: (close: (v?: T) => void) => ReactNode, o: { title?: string; size?: 'sm' | 'md' | 'lg'; label?: string } = {}) =>
        new Promise<T | undefined>((resolve) =>
          setStack((s) => [
            ...s,
            { id: ++seq, kind: 'custom', render: render as (close: (v?: unknown) => void) => ReactNode, o, resolve: resolve as (v: unknown) => void },
          ]),
        ),
    }),
    [],
  );

  return (
    <Ctx.Provider value={api}>
      {children}
      {stack.map((e, i) => (
        <DialogHost key={e.id} entry={e} top={i === stack.length - 1} onDone={() => remove(e.id)} />
      ))}
    </Ctx.Provider>
  );
}

function DialogHost({ entry, top, onDone }: { entry: Entry; top: boolean; onDone: () => void }) {
  const [value, setValue] = useState(entry.kind === 'prompt' ? entry.o.defaultValue ?? '' : '');
  const [error, setError] = useState<string | null>(null);

  const finish = (result: 'ok' | 'cancel', custom?: unknown) => {
    switch (entry.kind) {
      case 'alert':
        entry.resolve();
        break;
      case 'confirm':
        entry.resolve(result === 'ok');
        break;
      case 'prompt': {
        if (result === 'ok') {
          const v = value.trim();
          if (entry.o.required && !v) {
            setError('This field is required.');
            return;
          }
          const msg = entry.o.validate?.(v) ?? null;
          if (msg) {
            setError(msg);
            return;
          }
          entry.resolve(v);
        } else entry.resolve(null);
        break;
      }
      case 'custom':
        entry.resolve(result === 'ok' ? custom : undefined);
        break;
    }
    onDone();
  };

  const title = entry.kind === 'custom' ? entry.o.title : entry.o.title;
  const size = entry.kind === 'custom' ? entry.o.size ?? 'md' : 'sm';
  const tone = entry.kind === 'confirm' || entry.kind === 'prompt' ? entry.o.tone ?? 'primary' : 'primary';
  const role = entry.kind === 'alert' || (entry.kind === 'confirm' && tone === 'danger') ? 'alertdialog' : 'dialog';

  return (
    <ModalFrame
      top={top}
      title={title}
      ariaLabel={entry.kind === 'custom' ? entry.o.label ?? entry.o.title : undefined}
      size={size}
      role={role}
      onCancel={() => finish('cancel')}
      onEnter={entry.kind === 'alert' || entry.kind === 'confirm' ? () => finish('ok') : undefined}
    >
      {entry.kind === 'custom' ? (
        entry.render((v) => (v === undefined ? finish('cancel') : finish('ok', v)))
      ) : (
        <form
          onSubmit={(ev) => {
            ev.preventDefault();
            finish('ok');
          }}
        >
          {entry.o.body ? <div className="text-[13.5px] leading-relaxed text-muted">{entry.o.body}</div> : null}
          {entry.kind === 'prompt' ? (
            <PromptField
              o={entry.o}
              value={value}
              error={error}
              onChange={(v) => {
                setValue(v);
                setError(null);
              }}
              onSubmit={() => finish('ok')}
            />
          ) : null}
          <div className="mt-6 flex flex-wrap justify-end gap-2">
            {entry.kind !== 'alert' ? (
              <Button variant="ghost" onClick={() => finish('cancel')}>
                {entry.o.cancelLabel ?? 'Cancel'}
              </Button>
            ) : null}
            <Button type="submit" variant={tone === 'danger' ? 'danger' : 'primary'} data-autofocus={entry.kind !== 'prompt' ? true : undefined}>
              {entry.kind === 'alert' ? entry.o.okLabel ?? 'OK' : entry.o.confirmLabel ?? (entry.kind === 'prompt' ? 'Save' : 'Confirm')}
            </Button>
          </div>
        </form>
      )}
    </ModalFrame>
  );
}

function PromptField({ o, value, error, onChange, onSubmit }: { o: PromptOptions; value: string; error: string | null; onChange: (v: string) => void; onSubmit: () => void }) {
  const id = useId();
  const cls =
    'mt-1.5 w-full rounded-[10px] bg-panel px-3 py-2 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';
  return (
    <div className="mt-4">
      <label htmlFor={id} className="text-[12.5px] font-medium text-muted">
        {o.label}
      </label>
      {o.multiline ? (
        <textarea
          id={id}
          data-autofocus
          rows={4}
          value={value}
          maxLength={o.maxLength}
          placeholder={o.placeholder}
          aria-invalid={!!error}
          aria-describedby={`${id}-help`}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              onSubmit();
            }
          }}
          className={cx(cls, 'resize-y leading-relaxed')}
        />
      ) : (
        <input
          id={id}
          data-autofocus
          value={value}
          maxLength={o.maxLength}
          placeholder={o.placeholder}
          aria-invalid={!!error}
          aria-describedby={`${id}-help`}
          onChange={(e) => onChange(e.target.value)}
          className={cx(cls, 'h-10')}
        />
      )}
      <div id={`${id}-help`} className="mt-1.5 min-h-[18px] text-[12px]">
        {error ? <span className="text-danger">{error}</span> : <span className="text-subtle">{o.helper ?? (o.multiline ? 'Cmd/Ctrl + Enter to submit' : null)}</span>}
      </div>
    </div>
  );
}

const FOCUSABLE = 'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Shared modal chrome: backdrop, focus trap, Esc, focus restore, scroll lock. */
export function ModalFrame({ top, title, ariaLabel, size = 'md', role = 'dialog', onCancel, onEnter, children }: {
  top: boolean;
  title?: string;
  ariaLabel?: string;
  size?: 'sm' | 'md' | 'lg';
  role?: 'dialog' | 'alertdialog';
  onCancel: () => void;
  onEnter?: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const restoreTo = useRef<Element | null>(null);
  const cancelRef = useRef(onCancel);
  cancelRef.current = onCancel;
  const enterRef = useRef(onEnter);
  enterRef.current = onEnter;

  useEffect(() => {
    restoreTo.current = document.activeElement;
    const el = ref.current;
    const first = el?.querySelector<HTMLElement>('[data-autofocus]') ?? el?.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? el)?.focus();
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = prevOverflow;
      const r = restoreTo.current;
      if (r instanceof HTMLElement && document.contains(r)) r.focus();
    };
  }, []);

  useEffect(() => {
    if (!top) return;
    const onKey = (e: KeyboardEvent) => {
      const el = ref.current;
      if (!el) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        cancelRef.current();
        return;
      }
      if (e.key === 'Enter' && enterRef.current && !(e.target instanceof HTMLTextAreaElement) && !(e.target instanceof HTMLButtonElement)) {
        e.preventDefault();
        enterRef.current();
        return;
      }
      if (e.key === 'Tab') {
        const nodes = Array.from(el.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((n) => n.offsetParent !== null || n === document.activeElement);
        if (nodes.length === 0) {
          e.preventDefault();
          return;
        }
        const first = nodes[0]!;
        const last = nodes[nodes.length - 1]!;
        if (e.shiftKey && (document.activeElement === first || !el.contains(document.activeElement))) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && (document.activeElement === last || !el.contains(document.activeElement))) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  }, [top]);

  const width = size === 'sm' ? 'max-w-[440px]' : size === 'lg' ? 'max-w-[760px]' : 'max-w-[560px]';

  return createPortal(
    <div className="fixed inset-0 z-[70] flex items-end justify-center p-0 sm:items-center sm:p-6" data-mr-dialog="open">
      <div className="fade-in absolute inset-0 bg-[var(--overlay)] backdrop-blur-[6px]" onMouseDown={() => top && cancelRef.current()} aria-hidden />
      <div
        ref={ref}
        role={role}
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        aria-label={!title ? ariaLabel : undefined}
        tabIndex={-1}
        className={cx(
          'fade-up relative max-h-[92dvh] w-full overflow-y-auto rounded-t-[20px] bg-raised p-5 shadow-[var(--shadow-float)] hairline outline-none sm:rounded-[18px] sm:p-6',
          width,
        )}
      >
        {title ? (
          <div className="mb-3 flex items-start gap-3">
            <h2 id={titleId} className="min-w-0 flex-1 text-[17px] font-semibold tracking-[-0.015em] text-fg">
              {title}
            </h2>
            <button type="button" onClick={() => cancelRef.current()} aria-label="Close" className="-mr-1.5 -mt-1 rounded-[8px] p-1.5 text-subtle hover:bg-hover hover:text-fg">
              <X size={16} />
            </button>
          </div>
        ) : null}
        {children}
      </div>
    </div>,
    document.body,
  );
}
