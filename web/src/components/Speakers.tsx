import { useMemo, useState } from 'react';
import { Link } from 'react-router';
import { ArrowsClockwise, ArrowsSplit, CaretDown, Check, Info, NotePencil, PencilSimple, Plus, Sparkle, UserCircle, UsersThree, X } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../api/queries';
import type { AssetSummary, PersonSummary, SpeakerRow, SpeakersOut, TranscriptSegment } from '../api/types';
import { timecode } from '../lib/format';
import { pct, speakingTime, useSpeakerColors } from '../lib/speakers';
import { Button, cx, EmptyState, ErrorPanel, Skeleton, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { PersonPicker, type PersonChoice } from './PersonPicker';
import { SampleTile } from './SampleTile';
import { useSpeakerActions } from './speakerActions';

/** label → voice, for coloring transcript rows and the timeline lane. */
export function voiceMap(data: { speakers?: SpeakerRow[] } | undefined): Map<string, SpeakerRow> {
  return new Map((data?.speakers ?? []).map((v) => [v.label, v]));
}

function usePeople(): Map<string, PersonSummary> {
  const people = useOp('people.list', {}, { staleTime: 60_000 });
  return useMemo(() => new Map((people.data?.people ?? []).map((p) => [p.id, p])), [people.data]);
}

/**
 * Who speaks in a video: each voice with a color, video samples to play, its
 * name (or a suggestion to confirm), what people wrote about them, the lines
 * they say (each one can be given to someone else), and ways to listen again.
 */
export function SpeakersPanel({ asset, onSeek, segments }: { asset: AssetSummary; onSeek: (t: number) => void; segments?: TranscriptSegment[] }) {
  const q = useOp('footage.speakers', { assetId: asset.id });
  const data = q.data;
  if (q.isLoading)
    return (
      <div className="space-y-3" aria-busy="true">
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-28" />
        <Skeleton className="h-28" />
      </div>
    );
  if (q.error) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  if (!data) return null;
  const st = data.status;
  const voices = data.speakers;
  const main = voices.filter((v) => !v.minor || v.name);
  const brief = voices.filter((v) => v.minor && !v.name);
  return (
    <div>
      <div className="flex items-start gap-2">
        <UsersThree size={17} className="mt-0.5 shrink-0 text-subtle" aria-hidden />
        <p className="text-[13px] leading-relaxed text-fg">{data.summary}</p>
      </div>
      {st?.state === 'running' ? (
        <div className="mt-3 flex items-center gap-2 rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] text-accent-text" role="status">
          <Spinner size={12} /> Listening for voices…
        </div>
      ) : null}
      {voices.length === 0 ? (
        st?.state === 'running' ? null : (
          <EmptyState compact icon={<UserCircle size={20} />} title="No voices yet" body={st?.error ? 'We could not tell the voices apart in this video.' : 'Voices are found together with the transcript.'} />
        )
      ) : (
        <ul className="mt-4 space-y-2.5" aria-label="Voices">
          {main.map((v) => (
            <VoiceRow key={v.id} asset={asset} voice={v} onSeek={onSeek} segments={segments} />
          ))}
        </ul>
      )}
      {brief.length > 0 ? (
        <details className="group mt-3 rounded-[12px] hairline">
          <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 text-[12.5px] text-muted hover:text-fg">
            <CaretDown size={11} className="transition-transform group-open:rotate-180" />
            {brief.length === 1 ? '1 very brief voice' : `${brief.length} very brief voices`}
            <span className="text-subtle">(too short to tell who it is)</span>
          </summary>
          <ul className="space-y-2 px-3 pb-3" aria-label="Brief voices">
            {brief.map((v) => (
              <VoiceRow key={v.id} asset={asset} voice={v} onSeek={onSeek} segments={segments} />
            ))}
          </ul>
        </details>
      ) : null}
      {main.length > 1 ? (
        <p className="mt-3 text-[12px] leading-relaxed text-subtle">One person showing up as two voices? Give both the same name and they become one (you'll be asked first, and can undo it).</p>
      ) : null}
      {asset.hasAudio && voices.length > 0 ? <SomeoneElse asset={asset} status={st} /> : null}
      {asset.hasAudio ? <CountHint asset={asset} status={st} /> : null}
      <Details data={data} />
    </div>
  );
}

function VoiceRow({ asset, voice: v, onSeek, segments }: { asset: AssetSummary; voice: SpeakerRow; onSeek: (t: number) => void; segments?: TranscriptSegment[] }) {
  const color = useSpeakerColors()(v.color);
  const people = usePeople();
  const person = v.personId ? people.get(v.personId) : undefined;
  const actions = useSpeakerActions();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [showNotes, setShowNotes] = useState(false);
  const sugg = !v.name ? v.suggestions?.[0] : undefined;
  const auto = v.source === 'auto';
  const lines = useMemo(() => (segments ?? []).filter((s) => s.speaker === v.label), [segments, v.label]);

  const run = async (fn: () => Promise<boolean>) => {
    setBusy(true);
    try {
      return await fn();
    } finally {
      setBusy(false);
    }
  };
  const pick = async (c: PersonChoice) => {
    if (c.kind === 'unsure') return;
    if (await run(() => actions.nameVoice(asset.id, v.label, c))) setEditing(false);
  };
  const confirmThem = () => person && void run(() => actions.nameVoice(asset.id, v.label, { kind: 'person', person }));

  return (
    <li className={cx('rounded-[12px] bg-panel p-3 hairline', auto && 'outline-1 outline-dashed outline-info/45 -outline-offset-1')} data-voice={v.label} data-source={v.source || 'none'}>
      <div className="flex items-start gap-2.5">
        <span className="mt-1 h-8 w-1.5 shrink-0 rounded-full" style={{ background: color }} aria-hidden />
        <div className="min-w-0 flex-1">
          {editing ? (
            <div className="flex items-start gap-1.5">
              <PersonPicker
                label={`Who is ${v.displayName}?`}
                value={draft}
                onValueChange={setDraft}
                onPick={(c) => void pick(c)}
                exclude={v.rejected ?? []}
                autoFocus
                onEscape={() => setEditing(false)}
                placeholder="Their name"
                className="flex-1"
              />
              {busy ? <Spinner size={13} className="mt-3 text-subtle" label="Saving" /> : null}
              <Button size="sm" variant="ghost" className="mt-1" onClick={() => setEditing(false)} aria-label="Cancel">
                <X size={13} />
              </Button>
            </div>
          ) : (
            <div className="flex flex-wrap items-center gap-1.5">
              <button
                type="button"
                onClick={() => {
                  setDraft(v.name ?? '');
                  setEditing(true);
                }}
                className="group flex max-w-full items-center gap-1.5 rounded-[6px] text-left"
                aria-label={v.name ? `Rename ${v.name}` : `Name ${v.displayName}`}
              >
                <span className="truncate text-[14px] font-semibold" style={{ color }}>
                  {v.displayName}
                </span>
                <PencilSimple size={12} className="shrink-0 text-subtle opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" />
              </button>
              {person?.notes ? (
                <button
                  type="button"
                  onClick={() => setShowNotes(!showNotes)}
                  aria-expanded={showNotes}
                  aria-label={`Notes about ${person.name}`}
                  className="rounded-[6px] p-0.5 text-subtle hover:text-fg"
                >
                  <Info size={13} />
                </button>
              ) : null}
            </div>
          )}
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-subtle">
            <span>speaks {speakingTime(v.seconds)}</span>
            {v.source === 'person' ? <span>named by you</span> : null}
            {!v.name && v.minor ? <span>too brief to tell who it is</span> : null}
            {!v.name && !v.minor && !sugg ? <span className="text-accent-text">not named yet</span> : null}
            <button type="button" onClick={() => onSeek(v.first)} className="tc text-accent-text hover:underline" aria-label={`Jump to where ${v.displayName} first speaks, ${timecode(v.first)}`}>
              {timecode(v.first, { ms: false })}
            </button>
          </div>
        </div>
      </div>

      {showNotes && person?.notes ? (
        <div className="mt-2.5 rounded-[10px] bg-raised px-3 py-2 text-[12.5px] leading-relaxed text-muted hairline" role="note">
          <div className="mb-0.5 flex items-center gap-1.5 text-[11.5px] font-medium text-subtle">
            <NotePencil size={12} /> About {person.name}
          </div>
          <p className="whitespace-pre-wrap text-fg">{person.notes}</p>
          <Link to={`/library/people?person=${person.id}`} className="mt-1 inline-block text-[12px] text-accent-text hover:underline">
            Edit in Library → People
          </Link>
        </div>
      ) : null}

      {v.samples.length ? (
        <div className="mt-2.5 flex flex-wrap gap-2 pl-4" role="group" aria-label={`Samples of ${v.displayName}`}>
          {v.samples.slice(0, 3).map((s, i) => (
            <SampleTile key={i} assetId={asset.id} from={s.from} to={s.to} hasVideo={asset.kind === 'video'} label={`${v.displayName}, sample ${i + 1}`} size="sm" color={color} />
          ))}
        </div>
      ) : null}

      {auto && !editing ? (
        <div className="mt-2.5 flex flex-wrap items-center gap-2 rounded-[10px] bg-info-soft px-2.5 py-2" data-auto-label>
          <Sparkle size={13} className="shrink-0 text-info" aria-hidden />
          <span className="text-[12.5px] text-fg">
            Recognized by voice, <span className="tc">{pct(v.score)}%</span> voice match. <span className="text-muted">Not confirmed yet.</span>
          </span>
          <div className="ml-auto flex gap-1.5">
            {person ? (
              <Button size="sm" variant="secondary" icon={<Check size={12} weight="bold" />} disabled={busy} onClick={confirmThem}>
                That's {v.name}
              </Button>
            ) : null}
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(() => actions.notThem(asset.id, v.label, { id: v.personId, name: v.name ?? '' }))}>
              Not {v.name}?
            </Button>
          </div>
        </div>
      ) : null}

      {sugg && !editing ? (
        <div className="mt-2.5 flex flex-wrap items-center gap-2 rounded-[10px] bg-accent-soft px-2.5 py-2">
          <span className="text-[12.5px] text-fg">
            Sounds like <span className="font-semibold">{sugg.name}</span> <span className="text-muted">({pct(sugg.score)}% voice match)</span>
          </span>
          <div className="ml-auto flex gap-1.5">
            <Button
              size="sm"
              variant="primary"
              icon={<Check size={12} weight="bold" />}
              disabled={busy}
              onClick={() => {
                const p = people.get(sugg.personId);
                if (p) void run(() => actions.nameVoice(asset.id, v.label, { kind: 'person', person: p }));
              }}
            >
              It's {sugg.name}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(() => actions.notThem(asset.id, v.label, { id: sugg.personId, name: sugg.name }))}>
              Someone else
            </Button>
          </div>
        </div>
      ) : null}

      {v.mergedFrom?.length ? (
        <div className={cx('mt-2.5 flex flex-wrap items-center gap-2 rounded-[10px] px-2.5 py-2 text-[12.5px]', v.mergeWarning ? 'bg-danger-soft text-fg' : 'bg-raised text-muted hairline')}>
          <span>
            {v.mergedFrom.length === 1 ? 'Two voices were combined into this one.' : `${v.mergedFrom.length + 1} voices were combined into this one.`}
            {v.mergeWarning ? <span className="ml-1 font-medium text-danger">{v.mergeWarning}</span> : null}
          </span>
          <Button size="sm" variant="ghost" className="ml-auto" icon={<ArrowsSplit size={13} />} disabled={busy} onClick={() => void run(async () => (await actions.unmerge(asset.id, v.label), true))}>
            Separate them
          </Button>
        </div>
      ) : null}

      {lines.length > 0 ? <VoiceLines asset={asset} voice={v} lines={lines} color={color} onSeek={onSeek} /> : null}
    </li>
  );
}

/** The lines one voice says; each can be given to someone else. */
function VoiceLines({ asset, voice: v, lines, color, onSeek }: { asset: AssetSummary; voice: SpeakerRow; lines: TranscriptSegment[]; color: string; onSeek: (t: number) => void }) {
  const [fixing, setFixing] = useState<string | null>(null);
  return (
    <details className="group mt-2.5">
      <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-[6px] text-[12px] text-muted hover:text-fg">
        <CaretDown size={11} className="transition-transform group-open:rotate-180" />
        {lines.length === 1 ? '1 line' : `${lines.length} lines`}: wrong speaker on one? Fix it here
      </summary>
      <ol className="mt-2 space-y-1">
        {lines.map((s) => (
          <li key={s.id} className="rounded-[9px] px-2 py-1.5 hover:bg-hover/60">
            <div className="flex items-start gap-2">
              <button type="button" onClick={() => onSeek(s.from)} className="tc shrink-0 pt-[1px] text-[11.5px] text-subtle hover:text-fg" aria-label={`Play from ${timecode(s.from)}`}>
                {timecode(s.from, { ms: false })}
              </button>
              <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-muted">{s.text}</p>
              <LineChip name={s.speakerName || v.displayName} color={color} open={fixing === s.id} onClick={() => setFixing(fixing === s.id ? null : s.id)} />
            </div>
            {fixing === s.id ? <LineFix assetId={asset.id} seg={s} current={v} onDone={() => setFixing(null)} /> : null}
          </li>
        ))}
      </ol>
    </details>
  );
}

/** Who says a line, as a small chip that opens "who says this?". */
export function LineChip({ name, color, open, onClick, className }: { name: string; color?: string; open: boolean; onClick: () => void; className?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-expanded={open}
      aria-label={`${name} says this. Change who says it`}
      data-line-chip
      className={cx(
        'inline-flex h-6 max-w-[160px] shrink-0 items-center gap-1.5 rounded-full px-2 text-[11.5px] font-medium transition-colors hairline',
        open ? 'bg-accent-soft text-fg' : 'text-muted hover:bg-hover hover:text-fg',
        className,
      )}
    >
      <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: color ?? 'var(--fg-subtle)' }} aria-hidden />
      <span className="truncate">{name}</span>
      <CaretDown size={9} className={cx('shrink-0 transition-transform', open && 'rotate-180')} aria-hidden />
    </button>
  );
}

/** "Who says this?": another person, someone new, or not sure. */
export function LineFix({ assetId, seg, current, onDone }: { assetId: string; seg: TranscriptSegment; current?: SpeakerRow; onDone: () => void }) {
  const actions = useSpeakerActions();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const exclude = current?.personId && current.source === 'person' ? [current.personId] : [];
  return (
    <div className="mt-2 rounded-[12px] bg-raised p-2.5 hairline" data-line-fix>
      <div className="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-fg">
        Who says this line?
        {busy ? <Spinner size={11} className="text-subtle" label="Saving" /> : null}
        <button type="button" onClick={onDone} className="ml-auto rounded-[6px] p-0.5 text-subtle hover:text-fg" aria-label="Close">
          <X size={12} />
        </button>
      </div>
      <PersonPicker
        label="Who says this line?"
        value={text}
        onValueChange={setText}
        exclude={exclude}
        allowUnsure
        newLabel="Someone new"
        placeholder="Type a name, or pick below"
        autoFocus
        onEscape={onDone}
        onPick={async (c) => {
          setBusy(true);
          const ok = await actions.assign(assetId, seg.from, seg.to, c);
          setBusy(false);
          if (ok) onDone();
        }}
      />
      <p className="mt-1.5 text-[11.5px] leading-relaxed text-subtle">Only this line changes. Their voice is learned from the lines you confirm.</p>
    </div>
  );
}

/** "There's someone else talking here": listen again for one more voice. */
function SomeoneElse({ asset, status }: { asset: AssetSummary; status: SpeakersOut['status'] | undefined }) {
  const toast = useToast();
  const hint = useOpMutation('footage.speakers.hint', { invalidate: ['footage.speakers', 'job.list', 'activity.summary'] });
  return (
    <div className="mt-4 flex flex-wrap items-center gap-2 rounded-[12px] px-3 py-2.5 hairline">
      <div className="min-w-0 flex-1">
        <div className="text-[12.5px] font-medium text-fg">There's someone else talking here</div>
        <p className="text-[12px] text-subtle">We'll listen again for one more voice. Names you gave stay with their voices.</p>
      </div>
      <Button
        size="sm"
        variant="secondary"
        icon={<Plus size={13} />}
        loading={hint.isPending}
        disabled={status?.state === 'running'}
        onClick={() =>
          hint.mutate(
            { assetId: asset.id, count: 0, more: true },
            {
              onSuccess: (o) => toast({ tone: 'success', title: 'Listening again', body: o.note }),
              onError: (e) => toast({ tone: 'error', title: 'Could not start', body: [e.message, e.hint].filter(Boolean).join(' ') }),
            },
          )
        }
      >
        Listen for one more
      </Button>
    </div>
  );
}

const COUNTS = [0, 1, 2, 3, 4, 5, 6];

function CountHint({ asset, status }: { asset: AssetSummary; status: SpeakersOut['status'] | undefined }) {
  const toast = useToast();
  const current = status?.hint ?? 0;
  const [n, setN] = useState(current);
  const hint = useOpMutation('footage.speakers.hint', { invalidate: ['footage.speakers', 'job.list', 'activity.summary'] });
  return (
    <div className="mt-3 rounded-[12px] p-3 hairline">
      <div className="text-[12.5px] font-medium text-fg">How many people talk in this video?</div>
      <p className="mt-0.5 text-[12px] text-subtle">If the voices above look wrong, tell us and we'll listen again. The words and the names you gave stay.</p>
      <div className="mt-2.5 flex flex-wrap items-center gap-1.5" role="radiogroup" aria-label="Number of people who talk">
        {COUNTS.map((c) => (
          <button
            key={c}
            type="button"
            role="radio"
            aria-checked={n === c}
            onClick={() => setN(c)}
            className={cx('h-8 min-w-8 rounded-full px-2.5 text-[12.5px] transition-colors hairline', n === c ? 'bg-accent text-accent-ink' : 'text-muted hover:bg-hover hover:text-fg')}
          >
            {c === 0 ? 'Not sure' : c === 6 ? '6+' : c}
          </button>
        ))}
        <Button
          size="sm"
          variant="secondary"
          className="ml-auto"
          icon={<ArrowsClockwise size={13} />}
          loading={hint.isPending}
          disabled={status?.state === 'running'}
          onClick={() =>
            hint.mutate(
              { assetId: asset.id, count: n },
              {
                onSuccess: (o) => toast({ tone: 'success', title: 'Listening again', body: o.note }),
                onError: (e) => toast({ tone: 'error', title: 'Could not start', body: [e.message, e.hint].filter(Boolean).join(' ') }),
              },
            )
          }
        >
          Listen again
        </Button>
      </div>
    </div>
  );
}

function Details({ data }: { data: SpeakersOut }) {
  const st = data.status;
  if (!st) return null;
  return (
    <details className="group mt-4 text-[12px] text-subtle">
      <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-[6px] hover:text-fg">
        <CaretDown size={11} className="transition-transform group-open:rotate-180" /> Details
      </summary>
      <dl className="mt-2 grid grid-cols-[120px_1fr] gap-x-3 gap-y-1">
        <dt>Detection</dt>
        <dd className="text-muted">{st.diarizer || 'not run'}</dd>
        <dt>Before cleanup</dt>
        <dd className="text-muted">
          {st.raw} voice group(s) found, {st.count} kept{st.minor ? `, ${st.minor} brief` : ''}
        </dd>
        <dt>Voice matching</dt>
        <dd className="text-muted">{st.fingerprints ? 'on (recognizes people across videos)' : 'off on the machine that listened'}</dd>
        {st.hint ? (
          <>
            <dt>People</dt>
            <dd className="text-muted">{st.hint}, as you said</dd>
          </>
        ) : null}
        {data.speakers.some((v) => v.pitch) ? (
          <>
            <dt>Pitch</dt>
            <dd className="text-muted">{data.speakers.filter((v) => v.pitch).map((v) => `${v.displayName} ${Math.round(v.pitch ?? 0)} Hz`).join(', ')}</dd>
          </>
        ) : null}
        {(st.notes ?? []).map((n, i) => (
          <dd key={i} className="col-span-2 text-muted">
            {n}
          </dd>
        ))}
        {st.error ? <dd className="col-span-2 text-danger">{st.error}</dd> : null}
      </dl>
      <p className="mt-2">
        <Link to="/library/people" className="text-accent-text hover:underline">
          Manage people
        </Link>
      </p>
    </details>
  );
}

/** A thin lane under the waveform: who speaks when, in each voice's color. */
export function SpeakerLane({ segments, voices, duration, time, onSeek, className }: {
  segments: TranscriptSegment[];
  voices: Map<string, SpeakerRow>;
  duration: number;
  time: number;
  onSeek: (t: number) => void;
  className?: string;
}) {
  const colorOf = useSpeakerColors();
  const runs = useMemo(() => {
    const out: { from: number; to: number; label: string }[] = [];
    for (const s of segments) {
      if (!s.speaker) continue;
      const last = out[out.length - 1];
      if (last && last.label === s.speaker && s.from - last.to < 1.2) last.to = s.to;
      else out.push({ from: s.from, to: s.to, label: s.speaker });
    }
    return out;
  }, [segments]);
  if (runs.length === 0 || voices.size === 0 || duration <= 0) return null;
  const pctOf = (t: number) => `${(Math.max(0, Math.min(duration, t)) / duration) * 100}%`;
  const present = [...voices.values()].filter((v) => runs.some((r) => r.label === v.label));
  return (
    <div className={cx('mt-2', className)}>
      <div className="relative h-3 overflow-hidden rounded-full bg-inset" aria-hidden>
        {runs.map((r, i) => {
          const v = voices.get(r.label);
          return (
            <button
              key={i}
              type="button"
              tabIndex={-1}
              onClick={() => onSeek(r.from)}
              title={`${v?.displayName ?? r.label}, ${timecode(r.from, { ms: false })}`}
              className="absolute inset-y-0 transition-opacity hover:opacity-80"
              style={{ left: pctOf(r.from), width: `max(3px, calc(${pctOf(r.to)} - ${pctOf(r.from)}))`, background: colorOf(v?.color ?? 0) }}
            />
          );
        })}
        <div className="pointer-events-none absolute inset-y-0 w-[2px] -translate-x-1/2 bg-fg/90" style={{ left: pctOf(time) }} />
      </div>
      <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-[11.5px] text-muted">
        {present.map((v) => (
          <span key={v.label} className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: colorOf(v.color) }} aria-hidden />
            {v.displayName}
          </span>
        ))}
      </div>
    </div>
  );
}
