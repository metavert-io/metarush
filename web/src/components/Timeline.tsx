import { useMemo, useRef } from 'react';
import { Link } from 'react-router';
import type { NestedPlan } from '@metarush/schema';
import { ArrowSquareOut, Lock, Play, Quotes, SpeakerSlash, Subtitles, Waveform as WaveIcon } from '@phosphor-icons/react';
import { media } from '../api/client';
import type { AssetSummary, Clip, Evidence, RenderPlan, Spec } from '../api/types';
import { duration as fmtDuration, timecode, titleCase } from '../lib/format';
import { Badge, Button, cx, Theirs } from '../ui/primitives';
import { templateName } from '../screens/studio/cutsheet/script';
import transitionCatalog from '@metarush/schema/schemas/transitions.json';

/** The transition library (schema/transitions.json, generated from Go): names and which ones overlap shots. */
const TRANSITIONS = new Map(transitionCatalog.transitions.map((t) => [t.type, t]));

/** A clip's way in, in plain words: "Dissolve 0.5 s", "Slide left 0.4 s", plus its sound when split. */
export function transitionWords(tr: Clip['transitionIn']): string | undefined {
  if (!tr || (tr.type === 'cut' && !tr.audio)) return undefined;
  const info = TRANSITIONS.get(tr.type);
  let words = info?.name ?? tr.type;
  if (info?.directions?.length) words += ` ${tr.direction ?? info.defaultDirection ?? ''}`.trimEnd();
  if (tr.duration) words += ` ${tr.duration} s`;
  const a = tr.audio;
  if (a?.mode === 'lead') words += `; voice starts ${a.duration ?? 0} s early`;
  else if (a?.mode === 'carry') words += `; the previous voice carries ${a.duration ?? 0} s`;
  else if (a?.mode === 'auto') words += '; sound changes in the nearest pause';
  return words;
}

export type ClipMark = 'added' | 'changed' | 'removed' | undefined;

/** A sound track's role in words (the cut keeps its own role names). */
export const SOUND_ROLE_WORDS: Record<string, string> = { music: 'Music', sfx: 'Sound effect', voiceover: 'Narration', dialogue: 'Voices' };

interface Item {
  clip: Clip;
  start: number; // seconds on the timeline
  end: number;
}

/** Timeline positions: exact frames from the RenderPlan when we have it, else derived from the spec. */
export function layoutClips(spec: Spec, plan?: RenderPlan): { items: Item[]; total: number } {
  if (plan && plan.clips.length) {
    const byId = new Map(plan.clips.map((c) => [c.id, c]));
    const items = spec.clips.map((clip) => {
      const pc = byId.get(clip.id);
      const start = pc ? pc.from / plan.fps : 0;
      const end = pc ? (pc.from + pc.durationInFrames) / plan.fps : start;
      return { clip, start, end };
    });
    return { items, total: plan.durationInFrames / plan.fps };
  }
  let t = 0;
  const items = spec.clips.map((clip, i) => {
    const d = Math.max(0, (clip.out - clip.in) / (clip.speed || 1));
    const overlap = i > 0 && clip.transitionIn && TRANSITIONS.get(clip.transitionIn.type)?.overlap ? Math.min(clip.transitionIn.duration ?? 0, d) : 0;
    const start = Math.max(0, t - overlap);
    t = start + d;
    return { clip, start, end: t };
  });
  return { items, total: t };
}

function niceStep(total: number, width: number): number {
  const target = total / Math.max(1, width / 90);
  for (const s of [0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600]) if (s >= target) return s;
  return 1200;
}

/**
 * The cut as a strip: clips (with source thumbnails), graphics, captions and
 * sound lanes, a ruler and the playhead. Click a clip to inspect it.
 */
export function ClipStrip({ spec, plan, assets, time, onSeek, selectedClipId, onSelectClip, marks, compact }: {
  spec: Spec;
  plan?: RenderPlan;
  assets: Map<string, AssetSummary>;
  time?: number;
  onSeek?: (seconds: number) => void;
  selectedClipId?: string;
  onSelectClip?: (id: string) => void;
  marks?: Map<string, ClipMark>;
  compact?: boolean;
}) {
  const { items, total } = useMemo(() => layoutClips(spec, plan), [spec, plan]);
  const ruler = useRef<HTMLDivElement>(null);
  const len = Math.max(total, 0.001);
  const pct = (t: number) => `${(Math.max(0, Math.min(len, t)) / len) * 100}%`;
  const minWidth = Math.max(560, Math.min(4000, total * 22));
  const step = niceStep(total, minWidth);
  const ticks: number[] = [];
  for (let t = 0; t <= total + 1e-6; t += step) ticks.push(t);

  const overlays = plan
    ? plan.overlays.map((o) => ({ id: o.id, name: o.motion ? o.motion.name : templateName(o.template ?? ''), start: o.from / plan.fps, end: (o.from + o.durationInFrames) / plan.fps }))
    : (spec.overlays ?? []).map((o) => ({ id: o.id, name: o.motion ? o.motion.name : templateName(o.template ?? ''), start: o.at, end: o.at + o.duration }));
  const audio = plan
    ? plan.audio.map((a) => ({ id: a.id, assetId: a.assetId, role: a.role, start: a.from / plan.fps, end: (a.from + a.durationInFrames) / plan.fps }))
    : (spec.audio ?? []).map((a) => ({ id: a.id, assetId: a.assetId, role: a.role, start: a.at, end: a.at + (a.out - a.in) }));
  const captionLines = plan?.captions?.lines.map((l) => ({ start: l.from / plan.fps, end: l.to / plan.fps, text: l.text })) ?? [];
  const captionsOn = !!spec.captions?.enabled;

  const seekFromEvent = (clientX: number) => {
    const el = ruler.current;
    if (!el || !onSeek) return;
    const r = el.getBoundingClientRect();
    onSeek(Math.max(0, Math.min(total, ((clientX - r.left) / r.width) * total)));
  };

  // Crossfades overlap clips in time; draw each block up to the next clip's start so labels stay readable.
  const sorted = [...items].sort((a, b) => a.start - b.start);
  const shownEnd = new Map(sorted.map((it, i) => [it.clip.id, Math.min(it.end, sorted[i + 1]?.start ?? it.end)]));
  const clipH = compact ? 'h-14' : 'h-[76px]';

  return (
    <div className="overflow-x-auto pb-1" role="group" aria-label="Cut timeline">
      <div className="flex" style={{ minWidth: minWidth + 56 }}>
        <div className="w-14 shrink-0 pr-2 text-[10.5px] text-subtle" aria-hidden>
          <div className="h-6" />
          <div className={cx('mt-2 flex items-center', clipH)}>Clips</div>
          {overlays.length ? <LaneLabel>Graphics</LaneLabel> : null}
          {captionsOn ? <LaneLabel icon={<Subtitles size={11} />}>Captions</LaneLabel> : null}
          {audio.length ? <LaneLabel icon={<WaveIcon size={11} />}>Sound</LaneLabel> : null}
        </div>
        <div className="relative min-w-0 flex-1">
          <div
            ref={ruler}
            className="relative h-6 cursor-pointer border-b border-line"
            onPointerDown={(e) => {
              (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
              seekFromEvent(e.clientX);
            }}
            onPointerMove={(e) => e.buttons === 1 && seekFromEvent(e.clientX)}
            aria-hidden
          >
            {ticks.map((t) => (
              <div key={t} className="absolute bottom-0 flex h-full items-end" style={{ left: pct(t) }}>
                <div className="h-1.5 w-px bg-line-strong" />
                <span className="tc absolute bottom-2 left-1.5 whitespace-nowrap text-[10px] text-subtle">{timecode(t, { ms: false })}</span>
              </div>
            ))}
          </div>

          <div className={cx('relative mt-2', clipH)}>
            {items.map(({ clip, start, end }) => {
              const asset = assets.get(clip.assetId);
              const sel = clip.id === selectedClipId;
              const mark = marks?.get(clip.id);
              const drawEnd = shownEnd.get(clip.id) ?? end;
              const tr = clip.transitionIn && clip.transitionIn.type !== 'cut' ? clip.transitionIn : undefined;
              return (
                <button
                  key={clip.id}
                  type="button"
                  onClick={() => {
                    onSelectClip?.(clip.id);
                    onSeek?.(start + 0.001);
                  }}
                  aria-pressed={sel}
                  aria-label={`Clip ${clip.label || clip.id}, ${fmtDuration(end - start)}${mark ? `, ${mark}` : ''}`}
                  className={cx(
                    'group absolute inset-y-0 overflow-hidden rounded-[9px] text-left transition-[box-shadow,transform] duration-200',
                    sel ? 'z-10 ring-2 ring-accent' : 'ring-1 ring-inset ring-white/10 hover:ring-white/30',
                    mark === 'added' && !sel && 'ring-2 ring-ok',
                    mark === 'changed' && !sel && 'ring-2 ring-info',
                    mark === 'removed' && !sel && 'ring-2 ring-danger opacity-80',
                  )}
                  style={{ left: `calc(${pct(start)} + 1px)`, width: `max(10px, calc(${pct(drawEnd)} - ${pct(start)} - 2px))` }}
                >
                  {asset?.variants.includes('thumbnail') ? (
                    <img src={media.asset(asset.id, 'thumbnail')} alt="" className="absolute inset-0 h-full w-full object-cover opacity-70 transition-opacity group-hover:opacity-90" loading="lazy" />
                  ) : null}
                  <div className="absolute inset-0 bg-gradient-to-t from-black/85 via-black/35 to-black/10" />
                  {tr ? <div title={transitionWords(tr) ?? tr.type} className="absolute inset-y-0 left-0 w-3 bg-gradient-to-r from-white/45 to-transparent" /> : null}
                  <div className="relative flex h-full flex-col justify-between p-1.5 pl-2">
                    <div className="flex items-center gap-1 text-white/85">
                      {clip.locked ? <Lock size={11} weight="fill" /> : null}
                      {clip.muted ? <SpeakerSlash size={11} /> : null}
                      {mark ? <span className={cx('rounded-full px-1.5 text-[9.5px] font-semibold uppercase tracking-wide text-black', mark === 'added' ? 'bg-ok' : mark === 'removed' ? 'bg-danger' : 'bg-info')}>{mark}</span> : null}
                    </div>
                    <div className="min-w-0">
                      <div className="truncate text-[11.5px] font-medium text-white">{clip.label || clip.id}</div>
                      <div className="tc truncate text-[10px] text-white/65">{fmtDuration(end - start)}</div>
                    </div>
                  </div>
                </button>
              );
            })}
          </div>

          {overlays.length ? (
            <Lane label="Graphics">
              {overlays.map((o) => (
                <div
                  key={o.id}
                  title={`${o.name}, ${timecode(o.start)} to ${timecode(o.end)}`}
                  className={cx('absolute inset-y-0 truncate rounded-[6px] bg-accent-soft px-1.5 text-[10.5px] leading-[20px] text-accent-text ring-1 ring-inset ring-accent-line', marks?.get(`overlay:${o.id}`) === 'added' && 'ring-ok')}
                  style={{ left: pct(o.start), width: `max(6px, calc(${pct(o.end)} - ${pct(o.start)}))` }}
                >
                  {o.name}
                </div>
              ))}
            </Lane>
          ) : null}

          {captionsOn ? (
            <Lane label="Captions">
              {captionLines.length ? (
                captionLines.map((l, i) => (
                  <div key={i} title={l.text} className="absolute inset-y-[4px] rounded-[3px] bg-fg/25" style={{ left: pct(l.start), width: `max(2px, calc(${pct(l.end)} - ${pct(l.start)} - 1px))` }} />
                ))
              ) : (
                <div className="absolute inset-y-[4px] left-0 right-0 rounded-[3px] bg-fg/10" />
              )}
            </Lane>
          ) : null}

          {audio.length ? (
            <Lane label="Sound">
              {audio.map((a) => {
                const role = SOUND_ROLE_WORDS[a.role] ?? a.role;
                const name = assets.get(a.assetId)?.title ?? a.id;
                return (
                  <div key={a.id} title={`${role}: ${name}`} className="absolute inset-y-0 truncate rounded-[6px] bg-info-soft px-1.5 text-[10.5px] leading-[20px] text-info ring-1 ring-inset ring-info/30" style={{ left: pct(a.start), width: `max(6px, calc(${pct(a.end)} - ${pct(a.start)}))` }}>
                    {role}: <Theirs>{name}</Theirs>
                  </div>
                );
              })}
            </Lane>
          ) : null}

          {time !== undefined ? (
            <div className="pointer-events-none absolute bottom-0 top-0 z-20 w-[2px] -translate-x-1/2 bg-fg" style={{ left: pct(time) }}>
              <div className="absolute -left-[4px] top-0 h-2.5 w-2.5 rounded-[2px] bg-fg" />
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function LaneLabel({ icon, children }: { icon?: React.ReactNode; children: React.ReactNode }) {
  return <div className="mt-1.5 flex h-5 items-center gap-1">{icon}{children}</div>;
}

function Lane({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="relative mt-1.5 h-5" role="group" aria-label={label}>
      {children}
    </div>
  );
}

const EVIDENCE_LABEL: Record<Evidence['kind'], string> = {
  transcript: 'Transcript',
  insight: 'MetaRush noticed',
  frame: 'Frame',
  scene: 'Scene',
  user: 'Your note',
  analysis: 'Analysis',
};

/** Why a clip is in the cut: rationale plus evidence citations linking back to source timecodes. */
export function ClipDetail({ clip, asset, timelineStart, onOpenSource, onPlayFrom, assets, nested, onUpdateNested }: {
  clip: Clip;
  asset?: AssetSummary;
  timelineStart?: number;
  onOpenSource?: (assetId: string, t: number) => void;
  onPlayFrom?: (seconds: number) => void;
  assets: Map<string, AssetSummary>;
  /** A cut from another project played here (undefined: footage; null: not resolved yet). */
  nested?: NestedPlan | null;
  /** "Update to version N" for a nested cut with a newer version. */
  onUpdateNested?: () => void;
}) {
  const isNested = clip.assetId.startsWith('cut:');
  const facts: [string, string][] = [
    ['From the footage', `${timecode(clip.in)} to ${timecode(clip.out)}`],
    ['Length', fmtDuration((clip.out - clip.in) / (clip.speed || 1))],
  ];
  if (clip.speed && clip.speed !== 1) facts.push(['Speed', `${clip.speed}×`]);
  if (clip.framing?.mode) facts.push(['Framing', `${clip.framing.mode}${clip.framing.focusX !== undefined ? `, focus ${clip.framing.focusX}` : ''}${clip.framing.zoom ? `, zoom ${clip.framing.zoom}` : ''}`]);
  const way = transitionWords(clip.transitionIn);
  if (way) facts.push(['Starts with', way]);
  if (clip.fadeOut) facts.push(['Fade out', `${clip.fadeOut}s`]);
  if (clip.muted) facts.push(['Sound', 'muted']);
  else if (clip.volume !== undefined && clip.volume !== 1) facts.push(['Volume', `${clip.volume}×`]);

  return (
    <div className="fade-in">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-[15px] font-semibold tracking-[-0.01em] text-fg">{clip.label || clip.id}</h3>
        <span className="tc text-[11.5px] text-subtle">{clip.id}</span>
        {clip.locked ? (
          <Badge tone="info" icon={<Lock size={10} weight="fill" />}>
            locked
          </Badge>
        ) : null}
        <div className="ml-auto flex gap-1.5">
          {onPlayFrom && timelineStart !== undefined ? (
            <Button size="sm" variant="ghost" icon={<Play size={12} weight="fill" />} onClick={() => onPlayFrom(timelineStart)}>
              Play from here
            </Button>
          ) : null}
          {isNested && nested ? (
            <Link
              to={`/p/${nested.projectId}/r/${nested.revisionId}`}
              className="inline-flex h-8 items-center gap-1.5 rounded-[9px] px-3 text-[12.5px] font-medium text-fg hairline hover:bg-hover"
              title="Opens that version of the cut, read-only"
            >
              <ArrowSquareOut size={12} /> Open the cut
            </Link>
          ) : onOpenSource && !isNested ? (
            <Button size="sm" variant="secondary" icon={<ArrowSquareOut size={12} />} onClick={() => onOpenSource(clip.assetId, clip.in)}>
              Open the footage
            </Button>
          ) : null}
        </div>
      </div>
      {isNested ? (
        <div className="mt-1 text-[12.5px] text-muted">
          {nested ? (
            <>
              A cut from {nested.projectName ?? 'another project'}
              {nested.title ? `: “${nested.title}”` : ''}, version {nested.number}. It plays as one block, as that version is.
              {nested.newerNumber ? (
                <span className="mt-1.5 flex flex-wrap items-center gap-2">
                  <Badge tone="accent">version {nested.newerNumber} is newer</Badge>
                  {onUpdateNested ? (
                    <Button size="sm" variant="ghost" onClick={onUpdateNested}>
                      Update to version {nested.newerNumber}
                    </Button>
                  ) : null}
                </span>
              ) : null}
            </>
          ) : (
            'A cut from another project.'
          )}
        </div>
      ) : (
        <div className="mt-1 truncate text-[12.5px] text-muted" title={asset ? `${asset.title} (${asset.name})` : undefined}>
          {asset ? <Theirs>{asset.title}</Theirs> : 'Footage that is no longer in this project'}
        </div>
      )}
      <dl className="mt-3 flex flex-wrap gap-x-5 gap-y-1.5">
        {facts.map(([k, v]) => (
          <div key={k} className="text-[12.5px]">
            <dt className="text-[11px] text-subtle">{k}</dt>
            <dd className="tc text-fg">{v}</dd>
          </div>
        ))}
      </dl>
      <div className="mt-4">
        <div className="text-[11.5px] font-medium text-subtle">Why it is in the cut</div>
        <p className="mt-1 text-[13.5px] leading-relaxed text-fg">{clip.rationale || <span className="text-subtle">No reason written down for this clip.</span>}</p>
      </div>
      <div className="mt-4">
        <div className="text-[11.5px] font-medium text-subtle">What supports it {clip.evidence?.length ? `(${clip.evidence.length})` : ''}</div>
        {clip.evidence && clip.evidence.length > 0 ? (
          <ul className="mt-1.5 space-y-1.5">
            {clip.evidence.map((ev, i) => (
              <EvidenceRow key={i} ev={ev} assetName={ev.assetId ? assets.get(ev.assetId)?.title : undefined} onOpen={onOpenSource} />
            ))}
          </ul>
        ) : (
          <p className="mt-1 text-[13px] text-subtle">Nothing points back to the footage yet. Ask Claude Code which words or moments this clip is based on.</p>
        )}
      </div>
    </div>
  );
}

export function EvidenceRow({ ev, assetName, onOpen }: { ev: Evidence; assetName?: string; onOpen?: (assetId: string, t: number) => void }) {
  const linkable = !!ev.assetId && ev.from !== undefined && !!onOpen;
  const body = (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={ev.kind === 'transcript' ? 'accent' : ev.kind === 'user' ? 'ok' : 'info'} icon={ev.kind === 'transcript' ? <Quotes size={10} weight="fill" /> : undefined}>
          {EVIDENCE_LABEL[ev.kind] ?? titleCase(ev.kind)}
        </Badge>
        {ev.from !== undefined ? (
          <span className="tc text-[11.5px] text-accent-text">
            {timecode(ev.from)}
            {ev.to !== undefined ? ` to ${timecode(ev.to)}` : ''}
          </span>
        ) : null}
        {assetName ? <span className="truncate text-[11.5px] text-subtle">{assetName}</span> : null}
        {linkable ? <ArrowSquareOut size={11} className="ml-auto text-subtle" /> : null}
      </div>
      {ev.quote ? <blockquote className="mt-1.5 border-l-2 border-accent-line pl-2.5 text-[13px] italic leading-relaxed text-fg">“{ev.quote}”</blockquote> : null}
      {ev.note ? <div className="mt-1 text-[12.5px] text-muted">{ev.note}</div> : null}
    </>
  );
  return (
    <li>
      {linkable ? (
        <button type="button" onClick={() => onOpen!(ev.assetId!, ev.from!)} className="block w-full rounded-[10px] p-2.5 text-left hairline transition-colors hover:bg-hover">
          {body}
        </button>
      ) : (
        <div className="rounded-[10px] p-2.5 hairline">{body}</div>
      )}
    </li>
  );
}
