import { forwardRef, useState, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { ArrowClockwise, CaretRight, Check, Copy, WarningCircle } from '@phosphor-icons/react';
import { toApiError } from '../api/client';
import { copyText, useNow } from '../lib/hooks';
import { relative } from '../lib/format';

export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(' ');
}

/* ---------- Spinner / skeleton / progress ---------- */

export function Spinner({ size = 14, className, label }: { size?: number; className?: string; label?: string }) {
  return (
    <span
      role={label ? 'status' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      className={cx('inline-block shrink-0 animate-spin rounded-full border-[1.5px] border-current border-r-transparent opacity-80', className)}
      style={{ width: size, height: size }}
    />
  );
}

export function Skeleton({ className, style }: { className?: string; style?: React.CSSProperties }) {
  return <div aria-hidden className={cx('skeleton rounded-[10px]', className)} style={style} />;
}

export function Progress({ value, indeterminate, tone = 'accent', className, label }: {
  value?: number;
  indeterminate?: boolean;
  tone?: 'accent' | 'ok' | 'danger' | 'muted';
  className?: string;
  label?: string;
}) {
  const pct = Math.max(0, Math.min(1, value ?? 0));
  const color = tone === 'ok' ? 'bg-ok' : tone === 'danger' ? 'bg-danger' : tone === 'muted' ? 'bg-subtle' : 'bg-accent';
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={indeterminate ? undefined : Math.round(pct * 100)}
      className={cx('relative h-1 overflow-hidden rounded-full bg-line', className)}
    >
      {indeterminate ? (
        <div className={cx('absolute inset-y-0 w-2/5 rounded-full', color)} style={{ animation: 'mr-progress-indeterminate 1.3s var(--ease-snap) infinite' }} />
      ) : (
        <div className={cx('h-full rounded-full transition-[width] duration-500 ease-[var(--ease-out-expo)]', color)} style={{ width: `${pct * 100}%` }} />
      )}
    </div>
  );
}

/* ---------- Buttons ---------- */

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'quiet';
type Size = 'sm' | 'md' | 'lg';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
  trailing?: ReactNode;
}

const variants: Record<Variant, string> = {
  primary: 'bg-accent text-accent-ink hover:bg-accent-hover shadow-[inset_0_1px_0_rgb(255_255_255/0.25)] font-medium',
  secondary: 'bg-panel text-fg hairline hover:bg-hover',
  ghost: 'text-muted hover:text-fg hover:bg-hover',
  quiet: 'text-accent-text hover:bg-accent-soft',
  danger: 'bg-danger-soft text-danger hover:bg-[color-mix(in_oklab,var(--danger)_22%,transparent)]',
};
const sizes: Record<Size, string> = {
  sm: 'h-7 px-2.5 text-[12.5px] gap-1.5 rounded-[8px]',
  md: 'h-9 px-3.5 text-[13.5px] gap-2 rounded-[10px]',
  lg: 'h-11 px-5 text-[14.5px] gap-2.5 rounded-[12px]',
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', loading, icon, trailing, className, children, disabled, type = 'button', ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cx(
        'inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap transition-[background-color,color,transform,box-shadow] duration-200 ease-[var(--ease-out-expo)] active:scale-[0.98] disabled:opacity-50 disabled:active:scale-100',
        variants[variant],
        sizes[size],
        className,
      )}
      {...rest}
    >
      {loading ? <Spinner size={size === 'sm' ? 12 : 14} /> : icon}
      {children}
      {trailing}
    </button>
  );
});

export const IconButton = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & { label: string; size?: 'sm' | 'md'; active?: boolean }>(
  function IconButton({ label, size = 'md', active, className, children, type = 'button', ...rest }, ref) {
    return (
      <button
        ref={ref}
        type={type}
        aria-label={label}
        title={label}
        className={cx(
          'inline-flex shrink-0 items-center justify-center rounded-[10px] text-muted transition-colors duration-200 hover:bg-hover hover:text-fg active:scale-[0.96] disabled:opacity-40',
          size === 'sm' ? 'h-7 w-7' : 'h-9 w-9',
          active && 'bg-hover text-fg',
          className,
        )}
        {...rest}
      >
        {children}
      </button>
    );
  },
);

/* ---------- Badges / state pills ---------- */

export type Tone = 'neutral' | 'accent' | 'ok' | 'danger' | 'info';
const tones: Record<Tone, string> = {
  neutral: 'bg-hover text-muted',
  accent: 'bg-accent-soft text-accent-text',
  ok: 'bg-ok-soft text-ok',
  danger: 'bg-danger-soft text-danger',
  info: 'bg-info-soft text-info',
};

export function Badge({ tone = 'neutral', children, className, icon, title }: { tone?: Tone; children: ReactNode; className?: string; icon?: ReactNode; title?: string }) {
  return (
    <span
      title={title}
      className={cx('inline-flex h-[22px] max-w-full items-center gap-1 whitespace-nowrap rounded-full px-2 text-[11.5px] font-medium leading-none', tones[tone], className)}
    >
      {icon}
      <span className="truncate">{children}</span>
    </span>
  );
}

export function stateTone(state: string | undefined): Tone {
  switch (state) {
    case 'approved':
    case 'available':
    case 'succeeded':
    case 'done':
    case 'ingested':
      return 'ok';
    case 'failed':
    case 'rejected':
    case 'canceled':
      return 'danger';
    case 'running':
    case 'rendering':
    case 'uploading':
    case 'ingesting':
    case 'draft':
      return 'accent';
    case 'queued':
    case 'staging':
    case 'verified':
      return 'info';
    default:
      return 'neutral';
  }
}

/** How a job, render or clip state reads in a badge (the states themselves stay as the server sends them). */
const STATE_WORDS: Record<string, string> = {
  queued: 'waiting',
  succeeded: 'done',
  available: 'ready',
  staging: 'uploading',
  verified: 'checked',
  ingesting: 'adding',
  ingested: 'added',
  draft: 'draft',
  rejected: 'needs changes',
};

/** A state in words: "queued" → "waiting", "available" → "ready". */
export function stateWord(state: string | undefined): string {
  return state ? STATE_WORDS[state] ?? state : 'unknown';
}

export function StateBadge({ state, label }: { state: string | undefined; label?: string }) {
  const tone = stateTone(state);
  const live = state === 'running' || state === 'rendering' || state === 'uploading' || state === 'ingesting';
  return (
    <Badge tone={tone} icon={live ? <Spinner size={9} /> : undefined}>
      {label ?? stateWord(state)}
    </Badge>
  );
}

/* ---------- Empty / error states ---------- */

export function EmptyState({ icon, title, body, action, className, compact }: {
  icon?: ReactNode;
  title: string;
  body?: ReactNode;
  action?: ReactNode;
  className?: string;
  compact?: boolean;
}) {
  return (
    <div className={cx('flex flex-col items-center text-center', compact ? 'gap-2 px-4 py-8' : 'gap-3 px-6 py-14', className)}>
      {icon ? <div className="mb-1 grid h-11 w-11 place-items-center rounded-[12px] bg-hover text-muted">{icon}</div> : null}
      <div className="text-[14.5px] font-medium text-fg">{title}</div>
      {body ? <div className="max-w-[46ch] text-[13px] leading-relaxed text-muted">{body}</div> : null}
      {action ? <div className="mt-2 flex flex-wrap items-center justify-center gap-2">{action}</div> : null}
    </div>
  );
}

/* ---------- "Details" disclosure ---------- */

/**
 * Technical values (IDs, codes, exact numbers, commands) stay out of the way
 * until someone asks for them. `compact` is the small form under an error or a
 * warning; the default is the panel form Settings uses.
 */
export function Details({ children, label = 'Details', className, compact }: { children: ReactNode; label?: string; className?: string; compact?: boolean }) {
  if (compact) {
    return (
      <details className={cx('group/details text-[11.5px] text-subtle', className)}>
        <summary className="inline-flex w-fit cursor-pointer select-none list-none items-center gap-1 rounded-[6px] hover:text-fg focus-visible:outline-2 focus-visible:outline-accent [&::-webkit-details-marker]:hidden">
          <CaretRight size={10} className="transition-transform duration-200 group-open/details:rotate-90" aria-hidden />
          {label}
        </summary>
        <div className="mt-1 space-y-0.5 break-words leading-relaxed">{children}</div>
      </details>
    );
  }
  return (
    <details className={cx('group mt-4 rounded-[10px] hairline', className)}>
      <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-[10px] px-3 py-2 text-[12.5px] text-muted hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-accent [&::-webkit-details-marker]:hidden">
        <CaretRight size={12} className="transition-transform duration-200 group-open:rotate-90" aria-hidden />
        {label}
      </summary>
      <div className="border-t border-line px-3 py-2.5 text-[12px] leading-relaxed text-subtle">{children}</div>
    </details>
  );
}

/**
 * People's own words: a clip's title or file name, a person's, project's or
 * resource's name, what someone said. They are theirs, not MetaRush's, so they
 * may look like anything (riverside_sam_raw-video-cfr_garden_show_0002); the
 * plain-words guards skip what is marked this way, except internal IDs (D113).
 * MetaRush's own sentences that quote them use curly quotes, the same mark.
 */
export function Theirs({ children, className, title }: { children: ReactNode; className?: string; title?: string }) {
  return (
    <span data-user-content="" className={className} title={title}>
      {children}
    </span>
  );
}

/** Text the server marked up with `backticks`, shown as plain words (no code styling). */
export function plainText(text: string): string {
  return text.replace(/`([^`]+)`/g, '$1');
}

/** The command the server suggested for an error (`details.command`), if any: for people who use the terminal. */
export function errorCommand(e: { details?: unknown }): string | undefined {
  const d = e.details;
  if (d && typeof d === 'object' && 'command' in d) {
    const c = (d as { command?: unknown }).command;
    if (typeof c === 'string' && c.trim()) return c.trim();
  }
  return undefined;
}

/**
 * Polished error feedback: what happened and what to do next, in words, and a
 * retry. The stable error code (and the server's suggested command, when it
 * sent one) wait behind a small Details disclosure.
 */
export function ErrorPanel({ error, onRetry, className, compact, title }: { error: unknown; onRetry?: () => void; className?: string; compact?: boolean; title?: string }) {
  const e = toApiError(error);
  const command = errorCommand(e);
  return (
    <div role="alert" className={cx('flex gap-3 rounded-[14px] bg-danger-soft text-left', compact ? 'p-3' : 'p-4', className)}>
      <WarningCircle size={18} className="mt-[1px] shrink-0 text-danger" weight="fill" />
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-medium text-fg">{title ?? e.message}</div>
        {title ? <div className="mt-0.5 text-[13px] text-muted">{e.message}</div> : null}
        {e.hint ? <div className="mt-1 text-[13px] leading-relaxed text-muted">{plainText(e.hint)}</div> : null}
        <div className="mt-2 flex flex-wrap items-start gap-x-4 gap-y-1">
          {onRetry ? (
            <button type="button" onClick={onRetry} className="inline-flex items-center gap-1 text-[12.5px] font-medium text-accent-text hover:underline">
              <ArrowClockwise size={13} /> Retry
            </button>
          ) : null}
          {e.code ? (
            <Details compact className="pt-[2px]">
              <div>
                Error code <span className="tc text-muted">{e.code}</span>
              </div>
              {command ? (
                <div className="flex flex-wrap items-center gap-1.5">
                  <span>In a terminal:</span>
                  <code className="tc rounded-[5px] bg-hover px-1 py-[1px] text-fg">{command}</code>
                  <CopyButton text={command} label="Copy" className="h-6 px-2 text-[11.5px]" />
                </div>
              ) : null}
            </Details>
          ) : null}
        </div>
      </div>
    </div>
  );
}

/** Renders `backticked` spans as inline code. */
export function InlineCode({ text }: { text: string }) {
  const parts = text.split(/(`[^`]+`)/g);
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith('`') && p.endsWith('`') ? (
          <code key={i} className="tc rounded-[5px] bg-hover px-1 py-[1px] text-[0.92em] text-fg">
            {p.slice(1, -1)}
          </code>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  );
}

/* ---------- Freshness ("updated 12s ago") ---------- */

export function Freshness({ at, fetching, onRefresh, className }: { at: number | undefined; fetching?: boolean; onRefresh?: () => void; className?: string }) {
  const now = useNow(5000);
  return (
    <span className={cx('inline-flex items-center gap-1.5 text-[11.5px] text-subtle', className)} aria-live="off">
      {fetching ? <Spinner size={10} /> : null}
      <span title={at ? new Date(at).toLocaleTimeString() : undefined}>{at ? `updated ${relative(at, now)}` : 'loading'}</span>
      {onRefresh ? (
        <button type="button" onClick={onRefresh} aria-label="Refresh" title="Refresh" className="rounded-[6px] p-0.5 text-subtle hover:bg-hover hover:text-fg">
          <ArrowClockwise size={12} />
        </button>
      ) : null}
    </span>
  );
}

/* ---------- Copyable code ---------- */

export function CopyButton({ text, label = 'Copy', className, size = 'sm', onCopied }: { text: string; label?: string; className?: string; size?: Size; onCopied?: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      size={size}
      variant="secondary"
      className={className}
      icon={copied ? <Check size={13} weight="bold" /> : <Copy size={13} />}
      onClick={async () => {
        if (await copyText(text)) {
          setCopied(true);
          onCopied?.();
          setTimeout(() => setCopied(false), 1600);
        }
      }}
    >
      {copied ? 'Copied' : label}
    </Button>
  );
}

export function CopyBlock({ text, className, caption }: { text: string; className?: string; caption?: string }) {
  return (
    <div className={cx('overflow-hidden rounded-[12px] bg-inset', className)}>
      <div className="flex items-start gap-3 p-3 pl-4">
        <pre className="tc min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-words py-1 text-[12.5px] leading-relaxed text-[#efe8de]">{text}</pre>
        <CopyButton text={text} />
      </div>
      {caption ? (
        <div className="border-t border-white/5 px-4 py-2 text-[12px] text-[#a69d93]">
          {caption.split(/(`[^`]+`)/g).map((p, i) =>
            p.startsWith('`') && p.endsWith('`') ? (
              <code key={i} className="tc text-[#efe8de]">
                {p.slice(1, -1)}
              </code>
            ) : (
              <span key={i}>{p}</span>
            ),
          )}
        </div>
      ) : null}
    </div>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="tc inline-flex h-[18px] min-w-[18px] items-center justify-center rounded-[5px] border border-line-strong px-1 text-[10.5px] font-medium text-subtle">
      {children}
    </kbd>
  );
}

/* ---------- Section heading ---------- */

export function SectionTitle({ children, meta, action, className, id }: { children: ReactNode; meta?: ReactNode; action?: ReactNode; className?: string; id?: string }) {
  return (
    <div className={cx('mb-3 flex min-h-8 flex-wrap items-center gap-x-3 gap-y-1', className)}>
      <h2 id={id} className="text-[15px] font-semibold tracking-[-0.01em] text-fg">
        {children}
      </h2>
      {meta ? <div className="text-[12.5px] text-subtle">{meta}</div> : null}
      {action ? <div className="ml-auto flex items-center gap-2">{action}</div> : null}
    </div>
  );
}

/* ---------- Tabs ---------- */

export function Tabs<T extends string>({ value, onChange, items, className, label }: {
  /** The open tab; null when none of these is open (the page shows something next to them). */
  value: T | null;
  onChange: (v: T) => void;
  items: { value: T; label: ReactNode; count?: number }[];
  className?: string;
  label: string;
}) {
  const none = !items.some((x) => x.value === value);
  return (
    <div role="tablist" aria-label={label} className={cx('no-scrollbar flex items-center gap-1 overflow-x-auto', className)}>
      {items.map((it, idx) => {
        const selected = it.value === value;
        return (
          <button
            key={it.value}
            role="tab"
            type="button"
            aria-selected={selected}
            tabIndex={selected || (none && idx === 0) ? 0 : -1}
            onClick={() => onChange(it.value)}
            onKeyDown={(e) => {
              if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
              e.preventDefault();
              const i = Math.max(0, items.findIndex((x) => x.value === value));
              const next = items[(i + (e.key === 'ArrowRight' ? 1 : items.length - 1)) % items.length];
              if (next) {
                onChange(next.value);
                const el = e.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[items.indexOf(next)];
                el?.focus();
              }
            }}
            className={cx(
              'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-[9px] px-3 text-[13px] font-medium transition-colors duration-200',
              selected ? 'bg-panel text-fg hairline' : 'text-muted hover:bg-hover hover:text-fg',
            )}
          >
            {it.label}
            {it.count !== undefined ? <span className={cx('tc text-[11px]', selected ? 'text-muted' : 'text-subtle')}>{it.count}</span> : null}
          </button>
        );
      })}
    </div>
  );
}

/* ---------- Segmented control (one of a few ways to show the same thing) ---------- */

export function Segmented<T extends string>({ value, onChange, items, className, label }: {
  value: T;
  onChange: (v: T) => void;
  items: { value: T; label: ReactNode; icon?: ReactNode }[];
  className?: string;
  label: string;
}) {
  return (
    <div role="radiogroup" aria-label={label} className={cx('inline-flex shrink-0 items-center gap-0.5 rounded-[11px] bg-hover/70 p-[3px] hairline', className)}>
      {items.map((it, idx) => {
        const on = it.value === value;
        return (
          <button
            key={it.value}
            role="radio"
            type="button"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            data-value={it.value}
            onClick={() => onChange(it.value)}
            onKeyDown={(e) => {
              const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0;
              if (!step) return;
              e.preventDefault();
              const next = items[(idx + step + items.length) % items.length];
              if (!next) return;
              onChange(next.value);
              e.currentTarget.parentElement?.querySelector<HTMLButtonElement>(`[data-value="${next.value}"]`)?.focus();
            }}
            className={cx(
              'inline-flex h-8 min-w-0 items-center gap-1.5 rounded-[8px] px-3 text-[13px] font-medium transition-[background-color,color,box-shadow] duration-200 max-sm:h-10',
              on ? 'bg-panel text-fg shadow-[0_1px_2px_rgb(var(--shadow-color)/0.14)] hairline' : 'text-muted hover:text-fg',
            )}
          >
            {it.icon}
            {it.label}
          </button>
        );
      })}
    </div>
  );
}

/* ---------- Media thumbnail with skeleton + graceful fallback ---------- */

export function Thumb({ src, alt, className, imgClassName, fallback, fit = 'cover' }: {
  src: string | undefined;
  alt: string;
  className?: string;
  imgClassName?: string;
  fallback?: ReactNode;
  fit?: 'cover' | 'contain';
}) {
  const [state, setState] = useState<'loading' | 'ok' | 'error'>(src ? 'loading' : 'error');
  const [prevSrc, setPrevSrc] = useState(src);
  if (src !== prevSrc) {
    setPrevSrc(src);
    setState(src ? 'loading' : 'error');
  }
  return (
    <div className={cx('relative overflow-hidden bg-inset', className)}>
      {state === 'loading' ? <div className="skeleton absolute inset-0" /> : null}
      {src && state !== 'error' ? (
        <img
          src={src}
          alt={alt}
          loading="lazy"
          decoding="async"
          draggable={false}
          onLoad={() => setState('ok')}
          onError={() => setState('error')}
          className={cx(
            'h-full w-full transition-[opacity,transform] duration-500 ease-[var(--ease-out-expo)]',
            fit === 'cover' ? 'object-cover' : 'object-contain',
            state === 'ok' ? 'opacity-100' : 'opacity-0',
            imgClassName,
          )}
        />
      ) : null}
      {state === 'error' ? <div className="absolute inset-0 grid place-items-center text-[#7d756c]">{fallback}</div> : null}
    </div>
  );
}
