import { useMemo, useState } from 'react';
import { Link } from 'react-router';
import { ArrowElbowDownRight, CaretRight, ChatText, CheckCircle, FilmSlate, FilmStrip, Info, LockSimple, MusicNotes, NotePencil, Play, Quotes, ShieldCheck, SpeakerHigh, Subtitles, TextT, UsersThree, Waveform, XCircle } from '@phosphor-icons/react';
import { media } from '../../../api/client';
import type { AssetSummary, ProjectShowOut, RevisionSummary, Spec } from '../../../api/types';
import { duration as fmtDuration, plural, relative } from '../../../lib/format';
import { Markdown } from '../../../layout/assistant/Markdown';
import { Badge, cx, Details, Theirs, type Tone } from '../../../ui/primitives';
import { AIBadge, isGenerated, madeWith } from '../../../components/generate/ai';
import { ClipWordsFix } from '../../../components/transcripts/ClipWordsFix';
import { useDialog } from '../../../ui/Dialog';
import { motionPins, motionState, motionUse, parseScript, sheetSeconds, templateName, withFields, type BulletItem, type CaptionItem, type ClipItem, type MotionPin, type ScriptDoc, type Section, type Setting, type TransitionItem } from './script';

/*
 * The cut read like a script: what the video says, clip by clip, with who
 * speaks, why each clip is there, how one clip leads into the next, and the
 * narration, graphics, sound and captions in plain words. It is drawn from
 * the cut sheet text (`cut.show`), so it always shows what the sheet says;
 * anything it doesn't recognize is shown as written.
 */

interface Footage {
  alias: string;
  assetId?: string;
  comment?: string;
  asset?: AssetSummary;
}

function child(s: Setting[] | undefined, key: string): Setting | undefined {
  return s?.find((x) => x.key.toLowerCase() === key);
}

/** A pin under `resources:` or `cuts:` (its short name, what it points at, and MetaRush's note). */
interface Pin {
  alias: string;
  ref: string;
  comment?: string;
}

function pinsOf(doc: ScriptDoc, key: 'resources' | 'cuts'): Map<string, Pin> {
  const out = new Map<string, Pin>();
  for (const f of child(doc.settings, key)?.children ?? []) out.set(f.key, { alias: f.key, ref: f.value.split(/\s+/)[0] ?? '', comment: f.comment });
  return out;
}

function footageMap(doc: ScriptDoc, assets: Map<string, AssetSummary>): Map<string, Footage> {
  const out = new Map<string, Footage>();
  for (const f of child(doc.settings, 'footage')?.children ?? []) {
    const id = f.value.split(/\s+/).find((t) => t.startsWith('ast_'));
    out.set(f.key, { alias: f.key, assetId: id, comment: f.comment, asset: id ? assets.get(id) : undefined });
  }
  return out;
}

function nice(name: string): string {
  const s = name.replace(/[-_]+/g, ' ').trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** A narration line's `Take:` line in words: the recording's name and what it is, without the take's ID (the Markdown view keeps it). */
function takeWords(text: string): string {
  const [name, ...rest] = text.split(' · ').filter((p) => !/^id \S+$/.test(p.trim()));
  return [name ? name.replace(/[-_]+/g, ' ').trim() : '', ...rest].filter(Boolean).join(' · ');
}

function unquote(s: string): string {
  const m = /^"((?:[^"\\]|\\.)*)"$/.exec(s.trim());
  if (!m) return s;
  try {
    return JSON.parse(s.trim()) as string;
  } catch {
    return m[1]!;
  }
}

/** `license "CC BY 4.0" · attribution "A composer"` → "License: CC BY 4.0 · Attribution: A composer". */
function readableParts(s: string): string {
  return s
    .split(' · ')
    .map((p) => {
      const m = /^([a-z][a-z ]*?)\s+("(?:[^"\\]|\\.)*")$/.exec(p.trim());
      return m ? `${nice(m[1]!)}: ${unquote(m[2]!)}` : p;
    })
    .join(' · ');
}

function seconds(n: number): string {
  if (n < 60) return `${Math.round(n * 10) / 10} s`;
  return fmtDuration(n);
}

const PLATFORMS: Record<string, string> = { youtube: 'YouTube', shorts: 'YouTube Shorts', reels: 'Instagram Reels', instagram: 'Instagram', tiktok: 'TikTok', linkedin: 'LinkedIn', x: 'X', facebook: 'Facebook' };

export function ScriptView({ data, text, spec, revision, starter, onPlayClip, onOpenSource }: {
  data: ProjectShowOut;
  text: string;
  spec?: Spec;
  revision?: RevisionSummary;
  starter?: boolean;
  onPlayClip: (clipId: string) => void;
  onOpenSource: (assetId: string, t: number) => void;
}) {
  const doc = useMemo(() => parseScript(text), [text]);
  const assets = useMemo(() => new Map(data.assets.map((a) => [a.id, a])), [data.assets]);
  const footage = useMemo(() => footageMap(doc, assets), [doc, assets]);
  const pins = useMemo(() => motionPins(doc), [doc]);
  const cuts = useMemo(() => pinsOf(doc, 'cuts'), [doc]);
  const resources = useMemo(() => pinsOf(doc, 'resources'), [doc]);
  const clipCount = doc.sections.find((s) => s.kind === 'clips')?.items.filter((i) => i.kind === 'clip').length ?? 0;

  return (
    <article className="mx-auto max-w-[900px] space-y-8" aria-label="The cut as a script" data-script-view>
      <SummaryCard doc={doc} footage={footage} resources={resources} cuts={cuts} revision={revision} clips={clipCount} starter={starter} />
      {doc.sections.map((s, i) => (
        <SectionView key={`${s.title}-${i}`} section={s} footage={footage} cuts={cuts} pins={pins} spec={spec} onPlayClip={onPlayClip} onOpenSource={onOpenSource} />
      ))}
      {starter && clipCount === 0 ? (
        <p className="rounded-[14px] px-4 py-5 text-[13.5px] leading-relaxed text-muted hairline">
          No clips yet. Ask Claude Code (top right) for a first cut, or switch to Edit and write the clips yourself.
        </p>
      ) : null}
    </article>
  );
}

/* ---------- the summary at the top ---------- */

function SummaryCard({ doc, footage, resources, cuts, revision, clips, starter }: {
  doc: ScriptDoc;
  footage: Map<string, Footage>;
  resources: Map<string, Pin>;
  cuts: Map<string, Pin>;
  revision?: RevisionSummary;
  clips: number;
  starter?: boolean;
}) {
  const out = child(doc.settings, 'output')?.children;
  const caps = child(doc.settings, 'captions')?.children;
  const vo = child(doc.settings, 'voiceover')?.children;
  const mix = child(doc.settings, 'mix')?.children;
  const facts: string[] = [];
  const shape = child(out, 'shape');
  if (shape) facts.push(`${nice(shape.value)}${shape.comment ? `, ${shape.comment}` : ''}`);
  const size = child(out, 'size');
  if (size && !shape) facts.push(size.value);
  const fps = child(out, 'frame rate');
  if (fps) facts.push(`${fps.value} frames a second`);
  const platform = child(out, 'platform');
  if (platform) facts.push(`For ${PLATFORMS[platform.value.toLowerCase()] ?? nice(platform.value)}`);
  const framing = child(out, 'framing');
  if (framing) {
    const f = framing.value.toLowerCase();
    facts.push(f === 'center' || f === 'middle' ? 'Crops keep the middle' : ['follow', 'follow movement', 'follow the action', 'follow the subject', 'moving'].includes(f) ? 'Crops follow movement' : 'Crops keep faces in frame');
  }
  if (revision?.duration) facts.push(`${fmtDuration(revision.duration)} long`);
  if (clips) facts.push(plural(clips, 'clip'));
  const capsOn = child(caps, 'on')?.value === 'true';
  if (caps) facts.push(capsOn ? `Captions: ${[child(caps, 'style')?.value, child(caps, 'position')?.value].filter(Boolean).join(', ') || 'on'}` : 'No captions');
  if (vo) facts.push(`Narration: ${child(vo, 'layout')?.value ?? 'line by line'}`);
  const levels = (mix ?? []).filter((m) => m.value && !m.children.length).map((m) => `${m.key} ${m.value}`);
  const footageList = [...footage.values()];

  return (
    <section className="panel overflow-hidden" aria-label="About this cut">
      <div className="p-5 sm:p-6">
        <div className="flex flex-wrap items-center gap-2 text-[12px] font-medium text-subtle">
          <FilmSlate size={14} />
          {starter ? 'No cut yet' : revision ? `Version ${revision.number}${revision.isHead ? ', the latest' : ''}` : 'Script'}
          {revision ? (
            <span className="font-normal">
              · {revision.createdBy.userName ?? 'Someone'}, {relative(revision.createdAt)}
            </span>
          ) : null}
        </div>
        <h2 className="mt-2 text-[22px] font-semibold leading-snug tracking-[-0.02em] text-fg sm:text-[26px]">{doc.title || 'Untitled cut'}</h2>
        {facts.length ? (
          <ul className="mt-3 flex flex-wrap gap-1.5" aria-label="The video">
            {facts.map((f) => (
              <li key={f} className="rounded-full bg-hover px-2.5 py-1 text-[12px] text-muted">
                {f}
              </li>
            ))}
          </ul>
        ) : null}
        {doc.treatment ? (
          <div className="mt-4 rounded-[12px] bg-accent-soft px-4 py-3">
            <div className="mb-1 text-[11.5px] font-medium text-accent-text">About the cut</div>
            <Markdown text={doc.treatment} className="text-[14px]" />
          </div>
        ) : null}
        {levels.length ? <p className="mt-3 text-[12.5px] text-muted">Sound levels: {levels.join(', ')}</p> : null}
      </div>
      {footageList.length ? (
        <div className="border-t border-line px-5 py-4 sm:px-6">
          <h3 className="mb-2 text-[12.5px] font-semibold text-fg">Footage</h3>
          <ul className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
            {footageList.map((f) => (
              <li key={f.alias} className="flex min-w-0 items-baseline gap-2 text-[13px]">
                <span className="max-w-[60%] shrink-0 truncate font-medium text-fg">{f.asset ? <Theirs>{f.asset.title}</Theirs> : nice(f.alias)}</span>
                {f.comment ? <span className="min-w-0 truncate text-[12px] text-subtle"><FootageNote text={f.comment} /></span> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {resources.size ? (
        <div className="border-t border-line px-5 py-4 sm:px-6" data-resource-pins>
          <h3 className="mb-2 text-[12.5px] font-semibold text-fg">Logos, pictures and fonts</h3>
          <ul className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
            {[...resources.values()].map((r) => (
              <li key={r.alias} className="flex min-w-0 items-baseline gap-2 text-[13px]">
                <span className="tc shrink-0 font-medium text-fg">{r.alias}</span>
                {r.comment ? <span className="min-w-0 truncate text-[12px] text-subtle">{r.comment}</span> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {cuts.size ? (
        <div className="border-t border-line px-5 py-4 sm:px-6" data-cut-pins>
          <h3 className="mb-2 text-[12.5px] font-semibold text-fg">Cuts from other projects</h3>
          <ul className="space-y-1.5">
            {[...cuts.values()].map((c) => (
              <li key={c.alias} className="flex min-w-0 items-baseline gap-2 text-[13px]">
                <span className="tc shrink-0 font-medium text-fg">{c.alias}</span>
                {c.comment ? <span className="min-w-0 truncate text-[12px] text-subtle">{c.comment}</span> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <details className="group border-t border-line">
        <summary className="flex cursor-pointer list-none items-center gap-1.5 px-5 py-3 text-[12.5px] font-medium text-muted hover:text-fg sm:px-6 [&::-webkit-details-marker]:hidden">
          <CaretRight size={11} className="transition-transform group-open:rotate-90" /> Details
          <span className="font-normal text-subtle">the settings as written, with IDs</span>
        </summary>
        <pre className="tc mx-5 mb-5 max-h-[340px] overflow-auto whitespace-pre rounded-[10px] bg-raised p-3 text-[12px] leading-relaxed text-muted hairline sm:mx-6" data-settings-text>
          {doc.settingsText}
        </pre>
      </details>
    </section>
  );
}

/* ---------- sections ---------- */

const SECTION_ICON: Record<Section['kind'], React.ReactNode> = {
  clips: <FilmStrip size={16} />,
  conversations: <UsersThree size={16} />,
  voiceover: <Waveform size={16} />,
  graphics: <TextT size={16} />,
  sound: <MusicNotes size={16} />,
  captions: <Subtitles size={16} />,
  other: <Info size={16} />,
};

const SECTION_NAME: Record<Section['kind'], string> = { clips: 'Clips', conversations: 'Conversations', voiceover: 'Narration', graphics: 'Graphics', sound: 'Sound', captions: 'Captions', other: '' };

function SectionView({ section: s, footage, cuts, pins, spec, onPlayClip, onOpenSource }: {
  section: Section;
  footage: Map<string, Footage>;
  cuts: Map<string, Pin>;
  pins: Map<string, MotionPin>;
  spec?: Spec;
  onPlayClip: (clipId: string) => void;
  onOpenSource: (assetId: string, t: number) => void;
}) {
  const count = s.items.filter((i) => i.kind !== 'markdown' && i.kind !== 'transition').length;
  let clipIndex = -1;
  let seenClip = false;
  return (
    <section aria-label={SECTION_NAME[s.kind] || s.title} data-section={s.kind}>
      <h3 className="mb-3 flex items-center gap-2 text-[15px] font-semibold tracking-[-0.01em] text-fg">
        <span className="text-subtle">{SECTION_ICON[s.kind]}</span>
        {SECTION_NAME[s.kind] || s.title}
        {count ? <span className="tc text-[12px] font-normal text-subtle">{count}</span> : null}
        {s.kind === 'voiceover' ? (
          <Link to={{ search: '?tab=voiceover' }} className="ml-auto text-[12.5px] font-normal text-accent-text hover:underline">
            Edit on the Voiceover tab
          </Link>
        ) : null}
      </h3>
      {s.recording ? (
        <p className="mb-3 flex items-start gap-2 rounded-[12px] bg-hover/60 px-3 py-2 text-[13px] text-muted">
          <Waveform size={14} className="mt-[3px] shrink-0" /> One recording: {s.recording}
        </p>
      ) : null}
      {s.original !== undefined ? (
        <details className="mb-3 rounded-[12px] px-4 py-3 hairline" data-original-script>
          <summary className="cursor-pointer text-[13px] font-medium text-muted">Your original script, exactly as you gave it</summary>
          <pre className="mt-2 whitespace-pre-wrap break-words font-[inherit] text-[13.5px] leading-relaxed text-fg">{s.original}</pre>
        </details>
      ) : null}
      <div className={cx(s.kind === 'clips' ? 'space-y-0' : 'space-y-3')}>
        {s.items.map((it, i) => {
          switch (it.kind) {
            case 'clip': {
              clipIndex++;
              seenClip = true;
              const sc = spec?.clips.find((c) => c.id === it.name) ?? spec?.clips[clipIndex];
              return <ClipRow key={i} clip={it} specClip={sc} footage={footage} cuts={cuts} onPlay={onPlayClip} onOpenSource={onOpenSource} />;
            }
            case 'transition':
              return <TransitionRow key={i} item={it} opening={!seenClip} />;
            case 'bullet':
              return s.kind === 'voiceover' ? (
                <NarrationLine key={i} line={it} />
              ) : s.kind === 'graphics' ? (
                <GraphicRow key={i} item={it} pins={pins} />
              ) : (
                <SoundRow key={i} item={it} footage={footage} />
              );
            case 'caption':
              return <CaptionRow key={i} item={it} />;
            case 'markdown':
              return (
                <div key={i} className="rounded-[12px] px-4 py-3 hairline" data-as-written>
                  <Markdown text={it.text} />
                </div>
              );
          }
          return null;
        })}
      </div>
    </section>
  );
}

/* ---------- clips ---------- */

function ClipRow({ clip: c, specClip, footage, cuts, onPlay, onOpenSource }: {
  clip: ClipItem;
  specClip?: Spec['clips'][number];
  footage: Map<string, Footage>;
  cuts: Map<string, Pin>;
  onPlay: (clipId: string) => void;
  onOpenSource: (assetId: string, t: number) => void;
}) {
  const f = footage.get(c.footage);
  const nested = c.nested ? cuts.get(c.nested) : undefined;
  const nestedRef = nested?.ref.match(/^(prj_[0-9a-z]+)\/(rev_[0-9a-z]+)$/);
  const assetId = c.nested ? undefined : (specClip?.assetId ?? f?.assetId);
  const asset = f?.asset;
  const [a, b] = c.range.split(/\s*(?:→|->)\s*/);
  const inT = specClip?.in ?? sheetSeconds(a ?? '');
  const outT = specClip?.out ?? sheetSeconds(b ?? '');
  const length = inT !== undefined && outT !== undefined ? (outT - inT) / (specClip?.speed || 1) : undefined;
  const labelSetting = c.settings.find((s) => /^label\s+"/.test(s));
  const title = labelSetting ? unquote(labelSetting.replace(/^label\s+/, '')) : c.name ? nice(c.name) : (asset?.title ?? nice(c.footage));
  const chips = c.settings.filter((s) => s !== labelSetting);
  const clipId = specClip?.id ?? c.name;
  const audioOnly = asset ? asset.kind === 'audio' : false;
  const [thumbFailed, setThumbFailed] = useState(false);
  const at = inT !== undefined ? inT + Math.min(0.3, Math.max(0, (length ?? 1) / 4)) : undefined;
  const dialog = useDialog();
  // What the clip says can be fixed from here (D145): its footage's transcript.
  const fixable = !!assetId && !c.conversation && inT !== undefined && outT !== undefined && c.speech.length > 0;

  return (
    <div className="group/clip grid grid-cols-[88px_minmax(0,1fr)] gap-x-4 rounded-[16px] px-3 py-4 transition-colors hover:bg-hover/40 sm:grid-cols-[132px_minmax(0,1fr)] sm:px-4" data-clip={clipId}>
      <button
        type="button"
        onClick={() => clipId && onPlay(clipId)}
        disabled={!clipId}
        aria-label={`Play the cut from clip ${c.number}, ${title}`}
        className="relative block h-[58px] w-[88px] self-start overflow-hidden rounded-[10px] bg-inset hairline outline-none transition-transform duration-200 hover:scale-[1.03] focus-visible:outline-2 focus-visible:outline-accent motion-reduce:hover:scale-100 sm:h-[86px] sm:w-[132px]"
        data-play-clip
      >
        {assetId && at !== undefined && !audioOnly && !thumbFailed ? (
          <img src={media.frame(assetId, at, 320)} alt="" loading="lazy" decoding="async" onError={() => setThumbFailed(true)} className="absolute inset-0 h-full w-full object-cover" />
        ) : (
          <span className="absolute inset-0 grid place-items-center text-[#7d756c]" aria-hidden>
            {audioOnly ? <SpeakerHigh size={20} /> : <FilmStrip size={20} />}
          </span>
        )}
        <span className="absolute inset-0 grid place-items-center bg-black/0 transition-colors group-hover/clip:bg-black/25" aria-hidden>
          <span className="grid h-8 w-8 place-items-center rounded-full bg-black/55 text-white opacity-0 backdrop-blur-[2px] transition-opacity group-hover/clip:opacity-100 group-focus-within/clip:opacity-100">
            <Play size={13} weight="fill" className="translate-x-[1px]" />
          </span>
        </span>
        <span className="tc absolute left-1.5 top-1.5 rounded-[6px] bg-black/60 px-1.5 text-[11px] font-semibold leading-[18px] text-white">{c.number}</span>
      </button>

      <div className="min-w-0">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
          <h4 className="min-w-0 text-[15px] font-semibold leading-snug text-fg">
            <button type="button" onClick={() => clipId && onPlay(clipId)} className="text-left hover:underline" disabled={!clipId}>
              {title}
            </button>
          </h4>
          <span className="tc text-[12px] text-subtle">
            {c.range}
            {length !== undefined ? ` · ${seconds(length)}` : ''}
          </span>
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[12.5px] text-muted">
          <span className="truncate">{c.nested ? `A cut from another project${nested?.comment ? `: ${nested.comment}` : ''}` : asset ? <Theirs>{asset.title}</Theirs> : nice(c.footage)}</span>
          {nestedRef ? (
            <Link to={`/p/${nestedRef[1]}/r/${nestedRef[2]}`} className="text-[12px] font-medium text-accent-text hover:underline">
              Open the cut
            </Link>
          ) : null}
          {assetId && inT !== undefined ? (
            <button type="button" onClick={() => onOpenSource(assetId, inT)} className="text-[12px] font-medium text-accent-text hover:underline">
              Open the footage
            </button>
          ) : null}
        </div>
        {asset && isGenerated(asset.source) ? (
          // AI-generated footage says so in the Script (D127); the prompt is behind Details.
          <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[12.5px]" data-generated-line>
            <AIBadge source={asset.source} label="Generated" />
            <span className="text-muted">with {madeWith(asset.source)}</span>
            {asset.source?.generated?.prompt ? (
              <Details compact label="Prompt" className="w-full">
                <p className="max-w-[70ch] text-[12.5px] text-muted">“{asset.source.generated.prompt}”</p>
              </Details>
            ) : null}
          </div>
        ) : null}
        {chips.length ? (
          <ul className="mt-2 flex flex-wrap gap-1" aria-label="Clip settings">
            {chips.map((s) => (
              <li key={s} className={cx('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11.5px]', s === 'locked' ? 'bg-hover text-fg' : 'bg-hover/70 text-muted', s.startsWith('{') && 'tc')}>
                {s === 'locked' ? <LockSimple size={10} weight="fill" /> : null}
                {s}
              </li>
            ))}
          </ul>
        ) : null}

        {c.speech.length || c.silent ? (
          <div className="mt-3 space-y-2.5 sm:pl-6" data-dialogue>
            {c.speech.map((sp, i) => (
              <div key={i} className="max-w-[56ch]">
                {sp.speaker ? <div className="text-[11.5px] font-semibold uppercase tracking-[0.08em] text-muted">{sp.speaker}</div> : null}
                <p className="text-[15px] leading-relaxed text-fg">{sp.text}</p>
              </div>
            ))}
            {c.silent && !c.speech.length ? <p className="text-[13.5px] italic text-subtle">No one speaks here</p> : null}
            {fixable ? (
              <button
                type="button"
                onClick={() => void dialog.open((close) => <ClipWordsFix assetId={assetId!} from={inT!} to={outT!} onDone={() => close()} />, { title: `Fix the words of ${title}`, size: 'lg' })}
                className="inline-flex items-center gap-1 text-[12px] font-medium text-accent-text opacity-80 hover:underline hover:opacity-100"
                data-script-fix
              >
                <NotePencil size={12} /> Fix the words
              </button>
            ) : null}
          </div>
        ) : null}

        <BodyView body={c} />
      </div>
    </div>
  );
}

function BodyView({ body, skip = [] }: { body: Pick<ClipItem, 'why' | 'notes' | 'labeled' | 'evidence'>; skip?: string[] }) {
  const labeled = body.labeled.filter((l) => !skip.includes(l.label));
  if (!body.why && !body.notes.length && !labeled.length && !body.evidence.length) return null;
  return (
    <div className="mt-3 space-y-2">
      {body.why ? (
        <p className="flex items-start gap-2 text-[13.5px] leading-relaxed text-fg" data-why>
          <span className="mt-[1px] shrink-0 rounded-[6px] bg-accent-soft px-1.5 text-[11px] font-semibold leading-[19px] text-accent-text">Why</span>
          <span>{body.why}</span>
        </p>
      ) : null}
      {labeled.map((l, i) => (
        <p key={i} className="flex gap-2 text-[13px] leading-relaxed text-muted">
          {l.label === 'Locked' ? <LockSimple size={13} weight="fill" className="mt-[4px] shrink-0 text-subtle" /> : <span className="shrink-0 font-medium text-fg">{l.label}:</span>}
          <span className="min-w-0">{l.label === 'Locked' ? `Locked: ${l.text}` : readableParts(l.text)}</span>
        </p>
      ))}
      {body.notes.map((n, i) => (
        <p key={i} className="flex gap-2 whitespace-pre-line border-l-2 border-line-strong pl-3 text-[13px] leading-relaxed text-muted" data-note>
          <NotePencil size={13} className="mt-[4px] shrink-0 text-subtle" aria-label="Note" />
          <span>{n}</span>
        </p>
      ))}
      {body.evidence.length ? (
        <details className="group text-[12.5px]">
          <summary className="inline-flex cursor-pointer list-none items-center gap-1 text-subtle hover:text-fg [&::-webkit-details-marker]:hidden">
            <CaretRight size={10} className="transition-transform group-open:rotate-90" />
            <Quotes size={12} /> {body.evidence.length === 1 ? 'What supports it' : `What supports it (${body.evidence.length})`}
          </summary>
          <ul className="mt-1.5 space-y-1 pl-4">
            {body.evidence.map((e, i) => (
              <li key={i} className="leading-relaxed text-muted">
                {readableParts(e)}
              </li>
            ))}
          </ul>
        </details>
      ) : null}
    </div>
  );
}

function TransitionRow({ item, opening }: { item: TransitionItem; opening: boolean }) {
  const [picture = 'cut', ...sound] = item.parts;
  return (
    <div className="flex flex-wrap items-center gap-1.5 py-1 pl-3 sm:pl-[164px]" data-transition aria-label={`${opening ? 'Starts with' : 'Then'} ${item.parts.join(', ')}`}>
      <ArrowElbowDownRight size={14} className="text-subtle" aria-hidden />
      {opening ? <span className="text-[12px] text-subtle">Starts with</span> : null}
      <span className="rounded-full bg-info-soft px-2.5 py-0.5 text-[12px] font-medium text-info">{nice(picture)}</span>
      {sound.map((p) => (
        <span key={p} className="inline-flex items-center gap-1 rounded-full bg-hover px-2.5 py-0.5 text-[12px] text-muted" data-transition-phrase>
          <SpeakerHigh size={11} aria-hidden /> {p}
        </span>
      ))}
    </div>
  );
}

/* ---------- narration, graphics, sound, captions ---------- */

function NarrationLine({ line: l }: { line: BulletItem }) {
  const delivery = l.labeled.find((x) => x.label === 'Delivery');
  const take = l.labeled.find((x) => x.label === 'Take');
  const [start, ...rest] = l.words !== undefined ? l.settings : [l.head, ...l.settings].filter(Boolean);
  const placement = l.derived.join(' · ');
  const tight = /doesn't fit|does not fit|too long|overlaps/i.test(placement);
  return (
    <div className="rounded-[14px] px-4 py-3 hairline" data-narration={l.name}>
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span className="text-[11.5px] font-semibold uppercase tracking-[0.08em] text-muted">Narration</span>
        {l.name ? <span className="text-[12px] text-subtle">{nice(l.name)}</span> : null}
        {start ? <span className="ml-auto text-[12px] text-subtle">{start}</span> : null}
      </div>
      {delivery ? <p className="mt-1 text-[13px] italic text-muted">({delivery.text})</p> : null}
      <p className="mt-1 max-w-[60ch] text-[15px] leading-relaxed text-fg">{l.words !== undefined ? `“${l.words}”` : l.head}</p>
      <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-[12px]">
        {placement ? <span className={cx(tight ? 'text-accent-text' : 'text-subtle')}>{nice(placement)}</span> : null}
        {rest.map((s) => (
          <span key={s} className="rounded-full bg-hover/70 px-2 py-0.5 text-[11.5px] text-muted">
            {s === 'locked' ? <LockSimple size={10} weight="fill" className="mr-1 inline" /> : null}
            {s}
          </span>
        ))}
      </div>
      {take ? <p className="mt-1 text-[12px] text-subtle">Plays: {takeWords(take.text)}</p> : null}
      <BodyView body={l} skip={['Delivery', 'Take']} />
    </div>
  );
}

function GraphicRow({ item: g, pins }: { item: BulletItem; pins: Map<string, MotionPin> }) {
  // A project's own motion graphic ("motion route-map@71799722 at …") is named without its version.
  const own = motionUse(g.head);
  const [template = '', ...rest] = g.head.split(/\s+/);
  const title = own ? `Motion graphic: ${nice(own.graphic)}` : templateName(template);
  const when = own ? own.when : rest;
  // "- motion: route-map@… at …" (no name of its own) reads "motion" as the name.
  const name = own && g.name === 'motion' ? undefined : g.name;
  const fields = g.settings.map((s) => ({ s, f: withFields(s) }));
  const over = g.derived.join(' · ');
  return (
    <div className="rounded-[14px] px-4 py-3 hairline" data-graphic={name} data-motion={own ? own.graphic : undefined}>
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span className="text-[14px] font-semibold text-fg">{title}</span>
        <span className="tc text-[12px] text-subtle">{when.join(' ')}</span>
        {name ? <span className="ml-auto text-[12px] text-subtle">{nice(name)}</span> : null}
      </div>
      {own ? <MotionStatus graphic={own.graphic} version={own.version} pin={pins.get(own.graphic)} /> : null}
      {fields.map(({ s, f }) =>
        f ? (
          <dl key={s} className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-[13px]">
            {f.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted">{nice(k)}</dt>
                <dd className="min-w-0 break-words text-fg">{v}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <span key={s} className="mr-1 mt-2 inline-flex items-center gap-1 rounded-full bg-hover/70 px-2 py-0.5 text-[11.5px] text-muted">
            {s === 'locked' ? <LockSimple size={10} weight="fill" /> : null}
            {s}
          </span>
        ),
      )}
      {over ? <p className="mt-1.5 text-[12.5px] text-subtle">{nice(over)}</p> : null}
      <BodyView body={g} />
    </div>
  );
}

const MOTION_TONE: Record<ReturnType<typeof motionState>, Tone> = { approved: 'ok', waiting: 'accent', blocked: 'danger', unknown: 'neutral' };

/**
 * Whether a person approved the version this line uses (from the note MetaRush
 * writes after the version under `motion:` in the settings), with a link to its
 * review page. Only approved versions render.
 */
function MotionStatus({ graphic, version, pin }: { graphic: string; version: string; pin?: MotionPin }) {
  const mine = pin && pin.hash.startsWith(version) ? pin : undefined;
  const state = motionState(mine?.note);
  const note = mine?.note?.trim();
  const label = note ? note.charAt(0).toUpperCase() + note.slice(1) : 'Made for this project';
  const icon = state === 'approved' ? <CheckCircle size={12} weight="fill" /> : state === 'waiting' ? <ShieldCheck size={12} /> : state === 'blocked' ? <XCircle size={12} weight="fill" /> : null;
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1" data-motion-state={state}>
      <Badge tone={MOTION_TONE[state]} icon={icon} title={`${graphic}@${(mine?.hash ?? version).slice(0, 8)}`}>
        {label}
      </Badge>
      <Link to={`/motion/${mine?.hash ?? version}`} className="text-[12px] font-medium text-accent-text hover:underline">
        {state === 'approved' ? 'See it' : state === 'unknown' ? 'Details' : 'Review it'}
      </Link>
    </div>
  );
}

function SoundRow({ item: s, footage }: { item: BulletItem; footage: Map<string, Footage> }) {
  const sp = s.head.indexOf(' ');
  const alias = sp > 0 ? s.head.slice(0, sp) : s.head;
  const range = sp > 0 ? s.head.slice(sp + 1) : '';
  const f = footage.get(alias);
  const kinds = ['music', 'voiceover', 'sound effect', 'dialogue'];
  const kind = s.settings.find((x) => kinds.includes(x));
  return (
    <div className="rounded-[14px] px-4 py-3 hairline" data-sound={s.name}>
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        {kind ? <span className="rounded-full bg-info-soft px-2 py-0.5 text-[11.5px] font-medium text-info">{nice(kind)}</span> : null}
        <span className="min-w-0 truncate text-[14px] font-semibold text-fg">{f?.asset ? <Theirs>{f.asset.title}</Theirs> : nice(alias)}</span>
        {range ? <span className="tc text-[12px] text-subtle">{range}</span> : null}
        {s.name ? <span className="ml-auto text-[12px] text-subtle">{nice(s.name)}</span> : null}
      </div>
      <div className="mt-1.5 flex flex-wrap gap-1">
        {s.settings
          .filter((x) => x !== kind)
          .map((x) => (
            <span key={x} className="inline-flex items-center gap-1 rounded-full bg-hover/70 px-2 py-0.5 text-[11.5px] text-muted">
              {x === 'locked' ? <LockSimple size={10} weight="fill" /> : null}
              {x}
            </span>
          ))}
      </div>
      <BodyView body={s} />
    </div>
  );
}

function CaptionRow({ item: c }: { item: CaptionItem }) {
  return (
    <div className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] items-baseline gap-x-4 rounded-[12px] px-4 py-2 hairline" data-caption>
      <span className="tc text-[12px] text-subtle">{c.range}</span>
      <span className="min-w-0 text-[14px] text-fg">
        {c.speaker ? <span className="mr-2 text-[11.5px] font-semibold uppercase tracking-[0.08em] text-muted">{c.speaker}</span> : null}
        <ChatText size={12} className="mr-1 inline text-subtle" aria-hidden />
        {c.text}
      </span>
    </div>
  );
}

/** A footage line's note in the sheet: its file name (the person's own), then its length and who speaks. */
function FootageNote({ text }: { text: string }) {
  const [file, ...rest] = text.split(' · ');
  return (
    <>
      <Theirs>{file}</Theirs>
      {rest.length ? ` · ${rest.join(' · ')}` : ''}
    </>
  );
}
