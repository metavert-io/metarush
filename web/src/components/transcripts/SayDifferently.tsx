import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { Play, Sparkle, Stop } from '@phosphor-icons/react';
import { call, media, toApiError } from '../../api/client';
import type { SayEstimate, SayIn, SpokenView } from '../../api/types';
import { estimateWords } from '../../lib/costs';
import { useEstimatesFirst } from '../../lib/preferences';
import { Button, cx, ErrorPanel, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { CostEstimate } from '../CostEstimate';

/*
 * Change a spoken word (D308–D312): a word or short phrase someone said, said
 * differently in their own voice copy, with their consent. The price shows
 * first and a person decides; the new words are made, then checked on this
 * computer (the words heard, the voice), and only then can a person put them
 * in the cut, as a new version with a review copy. Undo puts back what was said.
 */

export interface MenuEntry {
  label: string;
  icon?: ReactNode;
  onSelect: () => void;
  disabled?: boolean;
  hint?: string;
}

/** A menu opened where the pointer is (a right-click on words): arrow keys, Enter, Esc. */
export function WordMenu({ x, y, items, onClose, label = 'Words' }: { x: number; y: number; items: MenuEntry[]; onClose: () => void; label?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [active, setActive] = useState(() => Math.max(0, items.findIndex((i) => !i.disabled)));
  useEffect(() => {
    const away = (e: Event) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    document.addEventListener('mousedown', away);
    window.addEventListener('blur', onClose);
    window.addEventListener('resize', onClose);
    return () => {
      document.removeEventListener('mousedown', away);
      window.removeEventListener('blur', onClose);
      window.removeEventListener('resize', onClose);
    };
  }, [onClose]);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>(`[data-i="${active}"]`)?.focus();
  }, [active]);
  const width = Math.min(260, window.innerWidth - 16);
  const left = Math.max(8, Math.min(x, window.innerWidth - width - 8));
  const top = Math.max(8, Math.min(y, window.innerHeight - (items.length * 40 + 16) - 8));
  const choose = (i: MenuEntry) => {
    if (i.disabled) return;
    onClose();
    i.onSelect();
  };
  return (
    <div
      ref={ref}
      role="menu"
      aria-label={label}
      style={{ left, top, width }}
      className="fixed z-[60] overflow-hidden rounded-[12px] bg-raised py-1 shadow-[var(--shadow-float)] hairline"
      onKeyDown={(e) => {
        if (e.key === 'Escape') {
          e.preventDefault();
          e.stopPropagation();
          onClose();
        } else if (e.key === 'ArrowDown') {
          e.preventDefault();
          setActive((a) => (a + 1) % items.length);
        } else if (e.key === 'ArrowUp') {
          e.preventDefault();
          setActive((a) => (a - 1 + items.length) % items.length);
        } else if (e.key === 'Tab') {
          onClose();
        }
      }}
      onContextMenu={(e) => e.preventDefault()}
      data-word-menu
    >
      {items.map((it, i) => (
        <button
          key={it.label}
          type="button"
          role="menuitem"
          data-i={i}
          tabIndex={i === active ? 0 : -1}
          aria-disabled={it.disabled || undefined}
          onClick={() => choose(it)}
          onMouseEnter={() => setActive(i)}
          className={cx(
            'flex w-full items-start gap-2 px-3 py-2 text-left text-[13px] outline-none',
            it.disabled ? 'cursor-default text-subtle' : 'text-fg hover:bg-hover focus-visible:bg-hover',
          )}
        >
          <span className="mt-[2px] shrink-0 text-muted">{it.icon}</span>
          <span className="min-w-0">
            <span className="block">{it.label}</span>
            {it.hint ? <span className="block text-[11.5px] text-subtle">{it.hint}</span> : null}
          </span>
        </button>
      ))}
    </div>
  );
}

/** Listen to a stretch of a clip (a few seconds at most). */
function Listen({ src, children, data }: { src: string; children: ReactNode; data?: string }) {
  const audio = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState(false);
  useEffect(() => () => audio.current?.pause(), []);
  return (
    <Button
      size="sm"
      variant="secondary"
      icon={playing ? <Stop size={13} /> : <Play size={13} />}
      {...(data ? { [data]: true } : {})}
      onClick={() => {
        if (playing) {
          audio.current?.pause();
          setPlaying(false);
          return;
        }
        const a = new Audio(src);
        audio.current = a;
        a.onended = () => setPlaying(false);
        a.onerror = () => setPlaying(false);
        setPlaying(true);
        void a.play().catch(() => setPlaying(false));
      }}
    >
      {children}
    </Button>
  );
}

/**
 * The dialog: the new words, the price, and — once made and checked — the
 * old and new to listen to and "Put it in the cut". It closes with a line for
 * a toast (or nothing when the person stopped).
 */
export function SayDialog({ projectId, assetId, from, to, before, onClose }: {
  projectId: string;
  assetId: string;
  from: number;
  to: number;
  before: string;
  onClose: (summary?: string) => void;
}) {
  const uid = useId();
  const toast = useToast();
  const estimatesFirst = useEstimatesFirst();
  const [words, setWords] = useState('');
  const [branch, setBranch] = useState('');
  const [est, setEst] = useState<SayEstimate | null>(null);
  const [pricing, setPricing] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [change, setChange] = useState<SpokenView | null>(null);
  const input = (): SayIn => ({ projectId, assetId, from, to, words: words.trim(), ...(branch ? { branch } : {}) });

  // Price it as the words are typed (nothing is sent or spent).
  useEffect(() => {
    if (change || !words.trim()) {
      setEst(null);
      return;
    }
    let live = true;
    setPricing(true);
    const t = setTimeout(() => {
      call('cut.say.estimate', input())
        .then((e) => live && (setEst(e), setError(null)))
        .catch((e) => live && (setEst(null), setError(e)))
        .finally(() => live && setPricing(false));
    }, 400);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [words, branch]); // eslint-disable-line react-hooks/exhaustive-deps

  // While it is made and checked, follow it.
  useEffect(() => {
    if (!change || change.state !== 'making') return;
    const t = setInterval(() => {
      call('cut.say.show', { id: change.id })
        .then((v) => setChange(v))
        .catch(() => undefined);
    }, 1500);
    return () => clearInterval(t);
  }, [change?.id, change?.state]); // eslint-disable-line react-hooks/exhaustive-deps

  const make = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await call('cut.say.make', { ...input(), ...(estimatesFirst && est ? { maxUsd: est.usd } : {}) });
      setChange(out.change);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const place = async () => {
    if (!change) return;
    setBusy(true);
    setError(null);
    try {
      const out = await call('cut.say.place', { id: change.id });
      const id = change.id;
      toast({
        tone: 'success',
        title: 'The new words are in the cut',
        body: out.summary,
        action: {
          label: 'Undo',
          onClick: () => {
            void call('cut.say.undo', { id })
              .then((u) => toast({ tone: 'success', title: 'What was said is back', body: u.summary }))
              .catch((e) => toast({ tone: 'error', title: 'Not undone', body: toApiError(e).message }));
          },
        },
      });
      onClose();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  const who = est?.speaker?.name ?? change?.speaker.name ?? 'the speaker';
  const ready = change?.state === 'ready' || change?.state === 'undone';
  const check = change?.check;
  return (
    <div className="space-y-4" data-say-dialog>
      <p className="text-[13px] text-muted">
        In the cut, <Theirs>{who}</Theirs> says something else in place of <Theirs className="text-fg">“{before}”</Theirs>, in their own voice copy, with their consent. The clip itself
        stays as it was, and the change is marked as made with AI everywhere.
      </p>
      {!change ? (
        <>
          <div>
            <label htmlFor={`${uid}-w`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
              Say instead
            </label>
            <input
              id={`${uid}-w`}
              data-autofocus
              data-say-words
              value={words}
              maxLength={200}
              onChange={(e) => setWords(e.target.value)}
              placeholder="The new words"
              className="h-9 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
            />
          </div>
          {est && est.cuts && est.cuts.length > 1 ? (
            <div>
              <label htmlFor={`${uid}-c`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
                In which cut
              </label>
              <select id={`${uid}-c`} value={branch || est.branch} onChange={(e) => setBranch(e.target.value)} className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline">
                {est.cuts.map((c) => (
                  <option key={c.branch} value={c.branch}>
                    {c.title}
                  </option>
                ))}
              </select>
            </div>
          ) : null}
          {pricing ? <p className="text-[12.5px] text-subtle">Working out the price…</p> : null}
          {est ? (
            <div className="space-y-2 rounded-[12px] px-3 py-2.5 text-[13px] hairline" data-say-estimate>
              {est.allowed ? (
                <>
                  <p>
                    <Sparkle size={13} className="mr-1 inline text-accent-text" />
                    <Theirs>“{est.words}”</Theirs> in {est.speaker?.name}'s own voice copy{est.voice ? ` at ${est.voice.providerTitle}` : ''}.
                  </p>
                  {est.context ? (
                    <p className="text-[12px] text-subtle">
                      Sends the words <Theirs>“{est.context}”</Theirs>, so they sound like part of the sentence. Only the new words are kept.
                    </p>
                  ) : null}
                  <p className="text-[12px] text-subtle">Checked on this computer (the words heard, the voice) before anyone can put it in the cut.</p>
                  <CostEstimate estimate={est.cost} defaultOpen={false} />
                </>
              ) : (
                <div data-say-refused>
                  {(est.problems ?? []).map((p) => (
                    <p key={p} className="text-[13px] text-fg">
                      {p}
                    </p>
                  ))}
                </div>
              )}
            </div>
          ) : null}
        </>
      ) : change.state === 'making' ? (
        <p className="rounded-[12px] px-3 py-2.5 text-[13px] hairline" data-say-making>
          Making <Theirs>“{change.words}”</Theirs> in {change.speaker.name}'s own voice copy, then checking it on this computer…
        </p>
      ) : ready ? (
        <div className="space-y-3 rounded-[12px] px-3 py-2.5 text-[13px] hairline" data-say-ready>
          <p>
            Ready to hear. {check?.passed ? <>Heard as <Theirs>“{check.heard}”</Theirs>; the voice is {Math.round((check.voice ?? 0) * 100)}% like {change.speaker.name}.</> : null}
          </p>
          <div className="flex flex-wrap gap-2">
            {change.fit ? (
              <Listen src={media.snippet(change.assetId, change.fit.from, change.fit.to)} data="data-say-before">
                What was said
              </Listen>
            ) : null}
            {change.sound && change.fit ? (
              <Listen src={media.snippet(change.sound, 0, change.fit.to - change.fit.from)} data="data-say-after">
                The new words
              </Listen>
            ) : null}
          </div>
          <p className="text-[12px] text-subtle">Putting it in makes a new version of {change.cutTitle} and a review copy.</p>
        </div>
      ) : (
        <div className="rounded-[12px] px-3 py-2.5 text-[13px] hairline" data-say-failed>
          <p className="text-fg">{change.why || 'It couldn’t be made.'}</p>
        </div>
      )}
      {error ? <ErrorPanel compact error={error} /> : null}
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()} disabled={busy}>
          {change && change.state !== 'ready' ? 'Close' : 'Cancel'}
        </Button>
        {!change ? (
          <Button variant="primary" loading={busy} disabled={!est?.allowed || pricing || !words.trim()} onClick={() => void make()} data-say-make>
            {estimatesFirst && est ? `Make it (${estimateWords(est.usd)})` : 'Make it'}
          </Button>
        ) : ready ? (
          <Button variant="primary" loading={busy} onClick={() => void place()} data-say-place>
            Put it in the cut
          </Button>
        ) : change.state === 'failed' ? (
          <Button variant="primary" onClick={() => setChange(null)} data-say-again>
            Try again
          </Button>
        ) : null}
      </div>
    </div>
  );
}
