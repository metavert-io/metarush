import { memo, useState } from 'react';
import { media } from '../../api/client';
import type { AssetSummary, Clip, Shot, Spec } from '../../api/types';
import { speedOf } from '../../lib/editops';
import { cx } from '../../ui/primitives';

/*
 * A conversation clip on the timeline (D102): its shots side by side — who is
 * on screen, the layouts, cards and b-roll — each where it starts. When the
 * clip is selected every shot is a button: click it to change the angle.
 */

export const LAYOUT_WORDS: Record<string, string> = { 'side-by-side': 'Side by side', stacked: 'Stacked', pip: 'Picture in picture', grid: 'Grid' };

export interface ShotSpan {
  index: number;
  shot: Shot;
  /** Conversation clock. */
  from: number;
  to: number;
}

/** The clip's shots as spans of the conversation's clock (the one it shows without any). */
export function shotSpans(clip: Clip, spec: Spec): ShotSpan[] {
  const shots: Shot[] = clip.shots?.length ? clip.shots : [defaultShot(clip, spec)];
  const out: ShotSpan[] = [];
  shots.forEach((sh, i) => {
    const from = Math.max(sh.at, clip.in);
    const next = shots[i + 1];
    const to = next ? Math.min(next.at, clip.out) : clip.out;
    if (to > from + 1e-6) out.push({ index: i, shot: sh, from, to });
  });
  return out;
}

export function defaultShot(clip: Clip, spec: Spec): Shot {
  const pin = spec.conversations?.[clip.assetId.slice(5)];
  const pics = (pin?.members ?? []).filter((m) => m.use !== 'sound').map((m) => m.id);
  if (pics.length === 1) return { at: clip.in, member: pics[0] };
  if (pics.length > 3) return { at: clip.in, layout: 'grid' };
  const vertical = (spec.output.height ?? 0) > (spec.output.width ?? 0);
  return { at: clip.in, layout: vertical ? 'stacked' : 'side-by-side' };
}

/** What a shot shows, in plain words. */
export function shotWords(sh: Shot, spec: Spec, clip: Clip): string {
  const pin = spec.conversations?.[clip.assetId.slice(5)];
  const name = (id: string) => pin?.members.find((m) => m.id === id)?.name ?? id;
  if (sh.member) return name(sh.member);
  if (sh.layout) return `${LAYOUT_WORDS[sh.layout] ?? sh.layout}${sh.members?.length ? `: ${sh.members.map(name).join(', ')}` : ''}`;
  if (sh.card) return `Card “${sh.card}”`;
  if (sh.broll) return 'Cutaway';
  return 'Shot';
}

const TINTS = ['#c2410c', '#1d4ed8', '#15803d', '#a21caf', '#b45309', '#0e7490', '#be123c', '#4d7c0f'];

export const ConversationStrip = memo(function ConversationStrip({
  clip,
  spec,
  assets,
  height,
  pps,
  offset,
  active,
  onPick,
}: {
  clip: Clip;
  spec: Spec;
  assets: Map<string, AssetSummary>;
  height: number;
  pps: number;
  /** Seconds from the block's left edge to the clip's picture start (≤ 0 with an overlapping transition). */
  offset: number;
  /** Selected: the shots are buttons. */
  active: boolean;
  onPick: (span: ShotSpan) => void;
}) {
  const pin = spec.conversations?.[clip.assetId.slice(5)];
  const members = pin?.members ?? [];
  const tint = (id: string) => TINTS[Math.max(0, members.findIndex((m) => m.id === id)) % TINTS.length]!;
  const thumb = (id: string) => {
    const m = members.find((x) => x.id === id);
    const a = m ? assets.get(m.assetId) : undefined;
    return a?.variants.includes('thumbnail') ? media.asset(a.id, 'thumbnail') : undefined;
  };
  const speed = speedOf(clip);
  const spans = shotSpans(clip, spec);
  return (
    <div className={cx('absolute inset-0 overflow-hidden', !active && 'pointer-events-none')} data-conversation-strip>
      {spans.map((sp) => {
        const left = ((sp.from - clip.in) / speed + offset) * pps;
        const w = Math.max(2, ((sp.to - sp.from) / speed) * pps - 1);
        const sh = sp.shot;
        const ids = sh.member ? [sh.member] : sh.layout ? (sh.members?.length ? sh.members : members.filter((m) => m.use !== 'sound').map((m) => m.id)) : sh.card ? (sh.members ?? []) : [];
        const words = shotWords(sh, spec, clip);
        const body = (
          <>
            <div className="absolute inset-0 flex">
              {ids.length === 0 ? (
                <div className={cx('h-full flex-1', sh.card ? 'bg-[#2b2620]' : 'bg-[repeating-linear-gradient(135deg,rgb(255_255_255/0.08)_0_6px,transparent_6px_12px)]')} />
              ) : (
                ids.map((id) => {
                  const src = thumb(id);
                  return (
                    <div key={id} className="relative h-full min-w-0 flex-1 border-r border-black/40 last:border-r-0" style={{ background: tint(id) }}>
                      {src ? <img src={src} alt="" draggable={false} className="h-full w-full object-cover opacity-70" /> : null}
                    </div>
                  );
                })
              )}
            </div>
            <div className="absolute inset-x-0 top-0 h-[3px]" style={{ background: sh.member ? tint(sh.member) : 'repeating-linear-gradient(90deg, var(--accent) 0 6px, transparent 6px 9px)' }} />
            {w > 44 ? (
              <span className="absolute left-1 top-1.5 max-w-[calc(100%-8px)] truncate rounded-[5px] bg-black/65 px-1 py-[1px] text-[10px] font-medium text-white">{words}</span>
            ) : null}
          </>
        );
        return active ? (
          <button
            key={sp.index}
            type="button"
            data-shot={sp.index}
            aria-label={`From ${sp.from.toFixed(1)} s on the conversation: ${words}. Change the angle`}
            title={`${words} — click to change the angle`}
            className="absolute top-0 overflow-hidden outline-none ring-inset hover:ring-2 hover:ring-white/60 focus-visible:ring-2 focus-visible:ring-accent"
            style={{ left, width: w, height }}
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation();
              onPick(sp);
            }}
          >
            {body}
          </button>
        ) : (
          <div key={sp.index} className="absolute top-0 overflow-hidden" style={{ left, width: w, height }} aria-hidden>
            {body}
          </div>
        );
      })}
    </div>
  );
});

/* ---------- the angle menu ---------- */

export interface AngleChoice {
  /** shot: change the shot clicked; here: a new switch at the playhead. */
  where: 'shot' | 'here';
  shot?: Partial<Shot>;
  remove?: boolean;
}

const LAYOUT_RANGE: Record<string, [number, number]> = { 'side-by-side': [2, 3], stacked: [2, 3], pip: [2, 2], grid: [2, 9] };

export function AngleMenu({ clip, spec, span, here, onChoose }: {
  clip: Clip;
  spec: Spec;
  span: ShotSpan;
  /** The playhead on the conversation's clock, when it is inside this shot (a new switch can go there). */
  here?: number;
  onChoose: (c: AngleChoice) => void;
}) {
  const pin = spec.conversations?.[clip.assetId.slice(5)];
  const pics = (pin?.members ?? []).filter((m) => m.use !== 'sound');
  const [where, setWhere] = useState<'shot' | 'here'>(here !== undefined ? 'here' : 'shot');
  const layouts = Object.entries(LAYOUT_RANGE).filter(([, [lo, hi]]) => pics.length >= lo && pics.length <= hi);
  const sh = span.shot;
  const opt = (label: string, shot: Partial<Shot>, current: boolean) => (
    <button
      key={label}
      type="button"
      onClick={() => onChoose({ where, shot })}
      className={cx('rounded-[10px] px-3 py-2 text-left text-[13px] font-medium hairline', current && where === 'shot' ? 'bg-accent-soft text-accent-text' : 'bg-panel hover:bg-hover')}
    >
      {label}
    </button>
  );
  return (
    <div className="space-y-3" data-angle-menu>
      <p className="text-[12.5px] text-muted">
        Now: <span className="font-medium text-fg">{shotWords(sh, spec, clip)}</span>, from {span.from.toFixed(1)} s of the conversation.
      </p>
      {here !== undefined ? (
        <div role="radiogroup" aria-label="Where" className="grid grid-cols-2 gap-1.5">
          {(
            [
              ['here', `From the playhead (${here.toFixed(1)} s)`],
              ['shot', 'This whole shot'],
            ] as const
          ).map(([v, label]) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={where === v}
              onClick={() => setWhere(v)}
              className={cx('rounded-[9px] px-2 py-1.5 text-[12px] hairline', where === v ? 'bg-panel font-medium text-fg' : 'text-muted hover:bg-hover')}
            >
              {label}
            </button>
          ))}
        </div>
      ) : null}
      <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
        {pics.map((m) => opt(m.name, { member: m.id }, sh.member === m.id))}
        {layouts.map(([id]) => opt(LAYOUT_WORDS[id] ?? id, { layout: id as Shot['layout'] }, sh.layout === id && !sh.members?.length))}
      </div>
      {span.index > 0 && where === 'shot' ? (
        <button type="button" onClick={() => onChoose({ where: 'shot', remove: true })} className="text-[12.5px] font-medium text-danger hover:underline">
          Remove this switch (the shot before plays on)
        </button>
      ) : null}
    </div>
  );
}

/** New shots for a choice (sorted; each keeps a reason). */
export function shotsAfter(clip: Clip, spec: Spec, span: ShotSpan, c: AngleChoice, here?: number): Shot[] {
  const shots: Shot[] = (clip.shots?.length ? clip.shots : [defaultShot(clip, spec)]).map((s) => ({ ...s }));
  if (c.remove) {
    shots.splice(span.index, 1);
    return shots;
  }
  const next: Shot = { at: 0, ...c.shot, rationale: 'Chosen on the timeline.' };
  if (c.where === 'here' && here !== undefined) {
    shots.push({ ...next, at: Math.round(here * 1000) / 1000 });
  } else {
    shots[span.index] = { ...next, at: shots[span.index]!.at };
  }
  return shots.sort((a, b) => a.at - b.at);
}
