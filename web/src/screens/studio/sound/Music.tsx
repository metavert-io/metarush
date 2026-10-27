import { useId, useRef, useState, type CSSProperties } from 'react';
import { MusicNotes, Trash, UploadSimple } from '@phosphor-icons/react';
import { call } from '../../../api/client';
import { hasFiles, uploadFraction } from '../../../api/uploads';
import type { ProjectShowOut, SoundMusicOut, SoundMusicView, SoundShowOut, Spec } from '../../../api/types';
import { useDialog } from '../../../ui/Dialog';
import { Button, cx, Progress, Segmented } from '../../../ui/primitives';
import { useToast } from '../../../ui/Toast';
import { checkApplied, clock, errorText, parseClock, useRefreshCut } from '../voiceover/api';
import { uploadRecording } from '../voiceover/recordings';
import { depthText, levelText, MiniSlider, rangeClass, Toggle, type MusicDraft } from './Mixer';

/*
 * Music under the cut. Added from a file (dropped here or on the timeline's
 * music lane) or from the project's audio, it fits the video by default: it
 * starts with it and ends with it where the music starts its next phrase (else
 * a bar, else a beat) near the end, with a smooth fade (a shorter track fades
 * out early, or repeats). The whole track is listened to: a tempo that changes,
 * its bars and phrases (D120). Placed by hand it plays from a
 * point in the track, starting where you say. Its level and getting quieter
 * while someone talks are tried in the preview like every level; the rest is
 * saved at once. Where the music comes from is asked in To-dos.
 */

const AUDIO_TYPES = /^(audio\/|video\/mp4$)|\.(mp3|m4a|aac|wav|aif|aiff|flac|ogg|opus|caf)$/i;

export function isAudioFile(f: File): boolean {
  return AUDIO_TYPES.test(f.type) || AUDIO_TYPES.test(f.name);
}

/** Adds a music file to the project and puts it under the cut (fit to the video unless at is given). */
export async function addMusicFile(file: File, projectId: string, revisionId: () => string | undefined, onProgress?: (f: number) => void): Promise<SoundMusicOut> {
  const assetId = await uploadRecording(file, projectId, 'Music', (it) => onProgress?.(uploadFraction(it)));
  const rev = revisionId();
  if (!rev) throw new Error('The project has no cut yet.');
  const out = await call('sound.music.add', { revisionId: rev, asset: assetId, apply: true });
  checkApplied(out.applied);
  return out;
}

export function Music({ revisionId, show, data, spec, draft, onDraft }: {
  revisionId: string;
  show: SoundShowOut;
  data: ProjectShowOut;
  spec: Spec;
  draft: Record<string, MusicDraft>;
  onDraft: (id: string, patch: MusicDraft) => void;
}) {
  const toast = useToast();
  const refresh = useRefreshCut();
  const [adding, setAdding] = useState<{ name: string; progress: number } | null>(null);
  const [over, setOver] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const pickId = useId();
  const inUse = new Set((spec.audio ?? []).map((a) => a.assetId));
  const choices = data.assets.filter((a) => a.kind === 'audio' && a.state === 'available' && !inUse.has(a.id) && !(a.tags ?? []).some((t) => t === 'voiceover' || t === 'enhanced' || t.startsWith('cleanup:')));

  const added = (out: SoundMusicOut) => {
    refresh();
    toast({ tone: 'success', title: 'Music added', body: out.placement });
  };

  const addFile = async (file: File) => {
    if (!isAudioFile(file)) {
      setProblem(`“${file.name}” is not a sound file MetaRush can use as music.`);
      return;
    }
    setProblem(null);
    setAdding({ name: file.name, progress: 0 });
    try {
      added(await addMusicFile(file, data.project.id, () => revisionId, (p) => setAdding({ name: file.name, progress: p })));
    } catch (e) {
      setProblem(errorText(e));
      refresh();
    } finally {
      setAdding(null);
    }
  };

  const addAsset = async (id: string) => {
    const a = data.assets.find((x) => x.id === id);
    setProblem(null);
    setAdding({ name: a?.title ?? 'the music', progress: 1 });
    try {
      const out = await call('sound.music.add', { revisionId, asset: id, apply: true });
      checkApplied(out.applied);
      added(out);
    } catch (e) {
      setProblem(errorText(e));
      refresh();
    } finally {
      setAdding(null);
    }
  };

  return (
    <section aria-labelledby="mix-music-h" className="space-y-3 rounded-[14px] p-3.5 hairline" data-sound-music>
      <div>
        <h3 id="mix-music-h" className="text-[13.5px] font-semibold">
          Music
        </h3>
        <p className="text-[12px] leading-relaxed text-subtle">Music fits the video by itself: it starts with it and ends with it where the music finishes a phrase, fading out smoothly.</p>
      </div>
      {show.music.length ? (
        <ul className="space-y-2.5" aria-label="Music tracks">
          {show.music.map((m) => (
            <li key={m.id}>
              <Track m={m} revisionId={revisionId} depth={Math.abs(show.mix?.effective?.ducking.musicUnderDialogueDb ?? 12)} draft={draft[m.id]} onDraft={(p) => onDraft(m.id, p)} />
            </li>
          ))}
        </ul>
      ) : null}

      <div
        className={cx('rounded-[12px] border border-dashed p-3 transition-colors', over ? 'border-accent bg-accent-soft' : 'border-line-strong')}
        data-music-drop
        onDragOver={(e) => {
          if (!hasFiles(e.dataTransfer)) return;
          e.preventDefault();
          e.stopPropagation();
          e.dataTransfer.dropEffect = 'copy';
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(e) => {
          if (!hasFiles(e.dataTransfer)) return;
          e.preventDefault();
          e.stopPropagation();
          setOver(false);
          const f = Array.from(e.dataTransfer.files)[0];
          if (f) void addFile(f);
        }}
      >
        {adding ? (
          <div className="space-y-1.5" role="status">
            <div className="text-[12.5px] text-fg">Adding “{adding.name}” and listening for its beat…</div>
            <Progress value={adding.progress} indeterminate={adding.progress >= 1} label="Adding the music" />
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            <MusicNotes size={18} className="shrink-0 text-subtle" aria-hidden />
            <span className="min-w-0 flex-1 text-[12.5px] text-muted">{show.music.length ? 'Drop another music file here to add it' : 'Drop a music file here to put music under the video'}</span>
            <Button size="sm" variant="secondary" icon={<UploadSimple size={13} />} onClick={() => fileInput.current?.click()}>
              Choose a file
            </Button>
            <input
              ref={fileInput}
              type="file"
              accept="audio/*,.mp3,.m4a,.wav,.aif,.aiff,.flac,.ogg"
              className="sr-only"
              tabIndex={-1}
              aria-label="Choose a music file"
              onChange={(e) => {
                const f = e.target.files?.[0];
                e.target.value = '';
                if (f) void addFile(f);
              }}
            />
          </div>
        )}
        {!adding && choices.length ? (
          <div className="mt-2.5 flex flex-wrap items-center gap-2">
            <label htmlFor={pickId} className="text-[12px] text-muted">
              Or use sound from this project
            </label>
            <select
              id={pickId}
              value=""
              onChange={(e) => e.target.value && void addAsset(e.target.value)}
              className="h-8 min-w-0 max-w-full flex-1 rounded-[9px] bg-panel px-2.5 text-[12.5px] text-fg hairline focus:outline-2 focus:outline-accent"
            >
              <option value="">Choose…</option>
              {choices.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.title} ({clock(a.duration).replace(/\.\d$/, '')})
                </option>
              ))}
            </select>
          </div>
        ) : null}
      </div>
      {problem ? (
        <p className="text-[12px] leading-relaxed text-danger" role="alert">
          {problem}
        </p>
      ) : null}
    </section>
  );
}

function Track({ m, revisionId, depth, draft, onDraft }: { m: SoundMusicView; revisionId: string; depth: number; draft?: MusicDraft; onDraft: (p: MusicDraft) => void }) {
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useRefreshCut();
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [times, setTimes] = useState({ at: clock(m.at), from: clock(m.in), to: clock(m.out) });
  const levelId = useId();
  const gain = draft?.gainDb ?? m.gainDb;
  const duckMode = draft?.duck ?? (m.duck as 'on' | 'off' | 'auto');
  const ducks = duckMode !== 'off';
  const dip = Math.abs(draft?.duckDb ?? (m.duckDb || depth));
  const pct = ((Math.max(-40, gain) + 40) / 52) * 100;

  const set = async (label: string, input: Record<string, unknown>, done: string) => {
    setBusy(label);
    setProblem(null);
    try {
      const out = await call('sound.music.set', { revisionId, track: m.id, ...input, apply: true });
      checkApplied(out.applied);
      refresh();
      toast({ tone: 'success', title: done, body: input.remove ? undefined : out.placement });
    } catch (e) {
      setProblem(errorText(e));
      refresh();
    } finally {
      setBusy(null);
    }
  };

  const applyTimes = () => {
    const at = parseClock(times.at);
    const from = parseClock(times.from);
    const to = parseClock(times.to);
    if (at === null || from === null || to === null) {
      setProblem('Write the times like 0:12.5 (minutes:seconds) or 12.5 (seconds).');
      return;
    }
    if (to <= from) {
      setProblem('The music has to stop after it starts.');
      return;
    }
    void set('times', { fit: 'manual', at, from, to }, `“${m.title}” placed by hand`);
  };

  return (
    <div className="space-y-2.5 rounded-[12px] p-3 hairline" data-music-track={m.id}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate text-[13px] font-medium" title={m.title}>
            {m.title}
          </div>
          <div className="text-[12px] leading-relaxed text-muted">{m.placement}</div>
          {m.music ? <div className="text-[12px] leading-relaxed text-subtle" data-music-found>{m.music}</div> : null}
        </div>
        <Button
          size="sm"
          variant="ghost"
          icon={<Trash size={13} />}
          loading={busy === 'remove'}
          disabled={!!busy}
          onClick={async () => {
            const ok = await dialog.confirm({ title: `Take “${m.title}” out of the cut?`, body: 'It stays in the project, so you can add it again.', confirmLabel: 'Take it out', tone: 'danger' });
            if (ok) await set('remove', { remove: true }, `“${m.title}” taken out`);
          }}
        >
          Take out
        </Button>
      </div>
      {!m.rights ? (
        <p className="rounded-[10px] bg-hover/60 px-3 py-2 text-[12px] leading-relaxed text-muted">
          Nobody has said where this music comes from yet. To-dos asks, because platforms can mute or block videos with music they don’t know is licensed.
        </p>
      ) : null}

      <div className="grid grid-cols-[minmax(0,1fr)] items-center gap-x-3 gap-y-1 sm:grid-cols-[118px_minmax(0,1fr)]">
        <div className="min-w-0">
          <label htmlFor={levelId} className="text-[12.5px] font-medium">
            Its level
          </label>
          <div className="text-[11.5px] text-subtle">{levelText(gain)}</div>
        </div>
        <input
          id={levelId}
          type="range"
          min={-40}
          max={12}
          step={0.5}
          value={Math.max(-40, gain)}
          aria-label={`Level of “${m.title}”`}
          aria-valuetext={levelText(gain)}
          onChange={(e) => onDraft({ gainDb: Number(e.target.value) })}
          onDoubleClick={() => onDraft({ gainDb: -12 })}
          style={{ '--pct': `${pct}%` } as CSSProperties}
          className={rangeClass}
        />
      </div>

      <div>
        <Toggle checked={ducks} onChange={(v) => onDraft({ duck: v ? 'on' : 'off' })} label="Gets quieter when someone talks" help="So words are easy to hear over it." />
        {ducks ? (
          <MiniSlider
            label="How much quieter"
            min={4}
            max={30}
            step={1}
            value={Math.min(30, Math.max(4, dip))}
            text={depthText(dip)}
            onChange={(v) => onDraft({ duck: 'on', duckDb: -v })}
            left="A little"
            right="A lot"
          />
        ) : null}
      </div>

      <div className="space-y-2">
        <Segmented<'auto' | 'manual'>
          label={`How “${m.title}” is timed`}
          value={m.fit === 'auto' ? 'auto' : 'manual'}
          onChange={(v) => void set('fit', { fit: v }, v === 'auto' ? `“${m.title}” fits the video` : `“${m.title}” placed by hand`)}
          items={[
            { value: 'auto', label: 'Fits the video' },
            { value: 'manual', label: 'Placed by hand' },
          ]}
        />
        {m.fit === 'auto' ? (
          <Toggle
            checked={m.loop}
            disabled={!!busy}
            onChange={(v) => void set('loop', { loop: v }, v ? `“${m.title}” repeats to fill the video` : `“${m.title}” plays once`)}
            label="Repeat it to fill the video"
            help={m.duration > 0 ? `It is ${clock(m.duration).replace(/\.\d$/, '')} long; repeats blend into each other.` : undefined}
          />
        ) : (
          <div className="flex flex-wrap items-end gap-2" role="group" aria-label={`When “${m.title}” plays`}>
            <TimeField label="Starts in the video at" value={times.at} onChange={(v) => setTimes((t) => ({ ...t, at: v }))} onEnter={applyTimes} />
            <TimeField label="From (in the track)" value={times.from} onChange={(v) => setTimes((t) => ({ ...t, from: v }))} onEnter={applyTimes} />
            <TimeField label="To" value={times.to} onChange={(v) => setTimes((t) => ({ ...t, to: v }))} onEnter={applyTimes} />
            <Button size="sm" variant="secondary" loading={busy === 'times'} onClick={applyTimes}>
              Place it
            </Button>
          </div>
        )}
        {m.fit === 'manual' ? <p className="text-[11.5px] text-subtle">You can also drag it on the timeline’s music lane.</p> : null}
      </div>
      {problem ? (
        <p className="text-[12px] leading-relaxed text-danger" role="alert">
          {problem}
        </p>
      ) : null}
    </div>
  );
}

function TimeField({ label, value, onChange, onEnter }: { label: string; value: string; onChange: (v: string) => void; onEnter: () => void }) {
  const id = useId();
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="block text-[11.5px] text-muted">
        {label}
      </label>
      <input
        id={id}
        value={value}
        inputMode="decimal"
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && onEnter()}
        className="mt-1 h-8 w-[92px] rounded-[9px] bg-panel px-2 text-[12.5px] tabular-nums text-fg hairline focus:outline-2 focus:outline-accent"
      />
    </div>
  );
}
