import { useQueryClient } from '@tanstack/react-query';
import { FilmStrip, HandPalm } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { ops } from '@metarush/schema';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { Badge, Button, cx, Details, EmptyState, ErrorPanel, Skeleton, Spinner, Theirs } from '../ui/primitives';

/*
 * What a video does (D399): the card MetaRush reads from the evidence it keeps
 * (footage.whatItDoes) — its sections with times on a strip, pacing, voices,
 * burned-in captions, words on screen, what plays under the voices and how it
 * ends — in plain words, with the numbers behind Details. The clip's page shows
 * it ("What it does"), and so does the result of "Start from a video you like".
 * A reference clip (someone else's video, kept to learn from, D398) says so,
 * with "This is mine…" for a person who made it.
 */

export type WhatItDoesCard = ops.WhatItDoes;
export type WhatClip = ops.WhatClip;

const KIND_TONE: Record<string, string> = {
  hook: 'bg-[#e8b169]',
  intro: 'bg-[#8fb3d9]',
  body: 'bg-[#9fc29a]',
  'call-to-action': 'bg-[#d99a8f]',
  'end-card': 'bg-[#b8a6d9]',
};

/** webLink is a link someone may open: an http or https address, else nothing. */
export function webLink(u?: string | null): string | undefined {
  const h = (u ?? '').trim();
  return /^https?:\/\/[^\s/?#@]+/i.test(h) ? h : undefined;
}

/** clock says a time in the video (0:03.2). */
export function clock(t: number): string {
  const m = Math.floor(t / 60);
  const s = t - m * 60;
  const whole = Math.abs(s - Math.round(s)) < 0.05;
  return `${m}:${whole ? String(Math.round(s)).padStart(2, '0') : s.toFixed(1).padStart(4, '0')}`;
}

function secs(s: number): string {
  if (s >= 60) {
    const m = Math.floor(s / 60);
    const r = Math.round(s - m * 60);
    return r ? `${m} min ${r} s` : `${m} min`;
  }
  return `${s >= 10 || Number.isInteger(s) ? s.toFixed(0) : s.toFixed(1)} s`;
}

/** The card itself (already loaded). */
export function WhatItDoesView({ card, onSeek, className }: { card: WhatItDoesCard; onSeek?: (t: number) => void; className?: string }) {
  const total = card.length || card.sections.reduce((t, s) => Math.max(t, s.to), 0) || 1;
  const graphics = card.graphics ?? [];
  return (
    <div className={cx('space-y-4', className)} data-what-it-does>
      <p className="text-[13.5px] leading-relaxed text-fg" data-what-summary>
        {card.summary}
      </p>
      {card.waiting.length ? (
        <div className="flex items-center gap-2 text-[12.5px] text-muted" data-what-waiting>
          <Spinner size={12} /> Still looking at {card.waiting.join(', ')}; the card fills in as each is done.
        </div>
      ) : null}
      {card.sections.length ? (
        <div>
          <div className="flex h-7 w-full overflow-hidden rounded-[8px] hairline" aria-hidden>
            {card.sections.map((s) => (
              <button
                key={s.name}
                type="button"
                tabIndex={-1}
                title={`${s.title}, ${clock(s.from)}–${clock(s.to)}`}
                className={cx('h-full border-r border-black/20 last:border-r-0', KIND_TONE[s.kind] ?? 'bg-subtle')}
                style={{ width: `${Math.max(2, ((s.to - s.from) / total) * 100)}%` }}
                onClick={() => onSeek?.(s.from)}
              />
            ))}
          </div>
          <ol className="mt-3 space-y-2" aria-label="Its sections">
            {card.sections.map((s) => (
              <li key={s.name} className="flex gap-3 text-[13px] leading-snug" data-what-section={s.name}>
                <span className={cx('mt-[5px] h-2.5 w-2.5 shrink-0 rounded-full', KIND_TONE[s.kind] ?? 'bg-subtle')} aria-hidden />
                <div className="min-w-0">
                  <div>
                    <span className="font-medium text-fg">{s.title}</span>{' '}
                    {onSeek ? (
                      <button type="button" className="tc text-[12px] text-accent-text hover:underline" onClick={() => onSeek(s.from)}>
                        {clock(s.from)}–{clock(s.to)}
                      </button>
                    ) : (
                      <span className="tc text-[12px] text-subtle">
                        {clock(s.from)}–{clock(s.to)}
                      </span>
                    )}{' '}
                    <span className="text-[12px] text-subtle">({secs(s.length)})</span>
                  </div>
                  <div className="text-muted">{s.does}</div>
                </div>
              </li>
            ))}
          </ol>
        </div>
      ) : null}
      <ul className="space-y-1.5 text-[13px] leading-relaxed text-muted" aria-label="How it is made">
        <li>
          <span className="font-medium text-fg">Pacing.</span> {card.pacing.said}
        </li>
        {card.speakers.length ? (
          <li>
            <span className="font-medium text-fg">Who speaks.</span>{' '}
            {card.speakers.length === 1
              ? `One voice${card.speakers[0]?.name ? ` (${card.speakers[0].name})` : ''}, all the way.`
              : card.speakers.map((sp, i) => `${sp.name || ['the first voice', 'the second voice', 'the third voice'][i] || 'another voice'} ${Math.round(sp.share * 100)}%`).join(', ') + '.'}
          </li>
        ) : null}
        <li>
          <span className="font-medium text-fg">Captions.</span> {card.captions.said}
        </li>
        {graphics.length ? (
          <li data-what-graphics>
            <span className="font-medium text-fg">On screen.</span>{' '}
            {graphics.slice(0, 6).map((g, i) => (
              <span key={`${g.kind}-${g.from}-${i}`}>
                {i ? '; ' : ''}
                {graphicWords(g.kind)} “<Theirs>{g.text}</Theirs>” at {clock(g.from)} ({g.where})
              </span>
            ))}
            {graphics.length > 6 ? `; ${graphics.length - 6} more` : ''}.
          </li>
        ) : null}
        <li>
          <span className="font-medium text-fg">Sound.</span> {card.sound.said}
        </li>
        {card.end ? (
          <li>
            <span className="font-medium text-fg">The end.</span> {card.end.said}
          </li>
        ) : null}
        {card.missing.length ? <li className="text-subtle">Not looked at here: {card.missing.join(', ')}.</li> : null}
      </ul>
      <Details compact>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12px] text-muted">
          <dt>Length</dt>
          <dd className="tc">
            {card.length} s{card.width ? ` · ${card.width}×${card.height}` : ''}
          </dd>
          <dt>Shots</dt>
          <dd className="tc">
            {card.pacing.shots} · average {card.pacing.shotSeconds} s · {card.pacing.shortest}–{card.pacing.longest} s
          </dd>
          <dt>Words</dt>
          <dd className="tc">
            {card.pacing.words} · {card.pacing.wordsPerMinute} a minute · speaking {Math.round(card.pacing.talking * 100)}% · {card.pacing.pauses} pauses
          </dd>
          {card.captions.burned ? (
            <>
              <dt>Captions</dt>
              <dd className="tc">
                {Math.round(card.captions.covers * 100)}% · {card.captions.wordsAtATime} at a time · {card.captions.position} · {card.captions.size} · look {card.captions.look}
              </dd>
            </>
          ) : null}
          {card.sound.speech != null && card.sound.under != null ? (
            <>
              <dt>Sound</dt>
              <dd className="tc">
                voices {card.sound.speech} dB · pauses {card.sound.under} dB · {card.sound.below} dB under
              </dd>
            </>
          ) : null}
          {card.sections.map((s) => (
            <SectionNumbers key={s.name} s={s} />
          ))}
        </dl>
      </Details>
    </div>
  );
}

function SectionNumbers({ s }: { s: ops.WhatSection }) {
  return (
    <>
      <dt>{s.name}</dt>
      <dd className="tc">
        {s.from}–{s.to} s · {s.shots} shots · {s.words} words · speech {Math.round(s.speech * 100)}% · {s.found}
      </dd>
    </>
  );
}

function graphicWords(kind: string): string {
  switch (kind) {
    case 'title':
      return 'a title';
    case 'lower-third':
      return 'a lower third';
    case 'end-card':
      return "the end card's words";
    case 'label':
      return 'a label that stays';
  }
  return 'words';
}

/** The clip page's "What it does" tab: the card, and a reference's note. */
export function WhatItDoesPanel({ assetId, onSeek }: { assetId: string; onSeek?: (t: number) => void }) {
  const q = useOp('footage.whatItDoes', { assetId }, { refetchInterval: (query) => (query.state.data && !query.state.data.card.ready ? 5000 : false) });
  if (q.isLoading) return <Skeleton className="h-48 rounded-[12px]" />;
  if (q.error) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <EmptyState compact icon={<FilmStrip size={20} />} title="Nothing to say yet" body="MetaRush says what a video does once it has looked at it." />;
  return (
    <div className="space-y-4">
      {q.data.clip.reference ? <ReferenceNote clip={q.data.clip} /> : null}
      <WhatItDoesView card={q.data.card} onSeek={onSeek} />
    </div>
  );
}

/**
 * A reference clip's note (D398): someone else's video, kept to learn from;
 * no cut plays it. "This is mine…" (a person's step, after a styled question)
 * says it's theirs.
 */
export function ReferenceNote({ clip, className }: { clip: { id: string; title: string; link?: string; who?: string }; className?: string }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const mine = async () => {
    const ok = await dialog.confirm({
      title: 'Is this your own video?',
      body: 'Say so only if you or your team made it. Cuts can play it then, and Recipes can take it as footage.',
      confirmLabel: 'It’s mine',
    });
    if (!ok) return;
    try {
      const out = await call('footage.reference.mine', { assetId: clip.id });
      toast({ tone: 'success', title: out.message });
      for (const k of ['footage.whatItDoes', 'asset.list', 'asset.show', 'library.list']) void qc.invalidateQueries({ queryKey: [k] });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: err.message, body: err.hint });
    }
  };
  return (
    <div className={cx('flex flex-wrap items-start gap-3 rounded-[12px] bg-hover px-3 py-2.5 text-[12.5px] leading-relaxed text-muted', className)} data-reference-note>
      <HandPalm size={16} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
      <div className="min-w-0 flex-1">
        <Badge tone="neutral">Reference</Badge> Someone else’s video, kept to learn from: no cut plays it.
        {clip.who ? (
          <>
            {' '}
            Posted by <Theirs>{clip.who}</Theirs>.
          </>
        ) : null}
      </div>
      <Button size="sm" variant="ghost" onClick={() => void mine()} data-reference-mine>
        This is mine…
      </Button>
    </div>
  );
}
