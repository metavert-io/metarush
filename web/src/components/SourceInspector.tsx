import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  CornersOut,
  FastForward,
  MusicNotes,
  Pause,
  Play,
  Rewind,
  SpeakerHigh,
  SpeakerSlash,
} from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { AssetSummary, EvidenceOut, TechnicalEvidence } from '../api/types';
import { absolute, bytes, duration as fmtDuration, jobLabel, relative, resolution, shortId, stamp, timecode, viaLabel } from '../lib/format';
import { loadPref, savePref } from '../lib/theme';
import { usePlayerHotkey } from '../lib/players';
import { Badge, cx, Details, EmptyState, ErrorPanel, IconButton, Skeleton, Spinner, StateBadge, Tabs, Theirs } from '../ui/primitives';
import { Transcript, type WordActions } from './Transcript';
import { SpeakerLane, SpeakersPanel, voiceMap } from './Speakers';
import { sourceRows } from './LinkImports';
import { WaveLegend, Waveform, type WaveLayers } from './Waveform';
import { OnScreenPanel, clock } from './OnScreen';
import { FrameGrid } from './FrameGrid';
import { MarkBar } from './studio/SourceMarks';
import { markedRange, setMark, useMarks } from '../lib/marks';
import { useMediaRecovery } from '../player/useMediaRecovery';
import { TroubleLine } from '../player/Trouble';
import { troubleLine } from '../player/recovery';

export interface SeekRequest {
  t: number;
  nonce: number;
  play?: boolean;
}

export type Panel = 'transcript' | 'speakers' | 'onscreen' | 'sheet' | 'evidence' | 'details';

const PANELS: Panel[] = ['transcript', 'speakers', 'onscreen', 'sheet', 'evidence', 'details'];

/** A `panel` deep-link parameter (e.g. `panel=speakers` from job details), if valid. */
export function panelParam(v: string | null | undefined): Panel | undefined {
  return PANELS.includes(v as Panel) ? (v as Panel) : undefined;
}

export function technicalEvidence(ev: EvidenceOut | undefined): TechnicalEvidence | undefined {
  const ins = ev?.insights.find((i) => i.kind === 'technical');
  return ins ? (ins.data as TechnicalEvidence) : undefined;
}

/**
 * The source viewer: a large preview-copy player, a waveform with what MetaRush found,
 * and a transcript that follows playback (plus contact sheet, evidence, details).
 */
export function SourceInspector({ asset, seek, onTime, range, compact, mini, highlight, panel: openPanel, markable, onAdd, cutWords }: {
  asset: AssetSummary;
  /** Smaller still: a short player and transcript (the editor's footage panel). */
  mini?: boolean;
  seek?: SeekRequest;
  onTime?: (t: number) => void;
  range?: { from: number; to: number };
  compact?: boolean;
  /** Words to highlight in the transcript (from search). */
  highlight?: string[];
  /** Open this tab (deep links, e.g. the Speakers tab from job details). */
  panel?: Panel;
  /** In and out marks (I / O, or the handles): the part that goes into the cut. */
  markable?: boolean;
  /** Buttons that put the marked part into the cut. */
  onAdd?: { label: string; run: (range: { in: number; out: number }) => void }[];
  /** Selected words of the transcript can be taken out of the current cut. */
  cutWords?: WordActions['cut'];
}) {
  const el = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const [time, setTime] = useState(seek?.t ?? 0);
  const [playing, setPlaying] = useState(false);
  const [buffering, setBuffering] = useState(false);
  const [muted, setMuted] = useState(false);
  const [rate, setRate] = useState(1);
  // A tab this clip does not have (Speakers without sound, Frame grid for audio) falls back to the transcript.
  // Words to highlight (a search hit) open the transcript, whatever tab was used last.
  const fits = (p: Panel | undefined): p is Panel => !!p && !(p === 'speakers' && !asset.hasAudio) && !((p === 'sheet' || p === 'onscreen') && asset.kind !== 'video');
  const wanted = (): Panel | undefined => (fits(openPanel) ? openPanel : highlight?.length && asset.hasAudio ? 'transcript' : undefined);
  const [panel, setPanel] = useState<Panel>(() => wanted() ?? (fits(loadPref<Panel>('sourcePanel', 'transcript')) ? loadPref<Panel>('sourcePanel', 'transcript') : 'transcript'));
  const hlKey = highlight?.join('|') ?? '';
  useEffect(() => {
    const w = wanted();
    if (w) setPanel(w);
  }, [openPanel, hlKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const [layers, setLayers] = useState<WaveLayers>(() => loadPref('waveLayers', { pauses: true, scenes: true, black: true, freezes: true }));

  const marks = useMarks(markable ? asset.id : undefined);
  // Selected words: cut them out of the cut, or mark them (to add just those words).
  const wordActions = useMemo<WordActions | undefined>(() => {
    const mark =
      markable && asset.kind === 'video'
        ? (inAt: number, outAt: number) => {
            setMark(asset.id, 'in', inAt);
            setMark(asset.id, 'out', outAt);
          }
        : undefined;
    return cutWords || mark ? { cut: cutWords, mark } : undefined;
  }, [cutWords, markable, asset.kind, asset.id]);
  const marked = markable && (marks.in !== undefined || marks.out !== undefined) ? markedRange(asset) : undefined;
  const isVideo = asset.kind === 'video';
  const hasProxy = asset.variants.includes('proxy');
  const src = isVideo ? (hasProxy ? media.asset(asset.id, 'proxy') : undefined) : asset.state === 'available' ? media.asset(asset.id, 'original') : undefined;
  // Through a short outage it says so and plays on where it was (D180); play,
  // pause and seeks meanwhile are kept for when it is back.
  const recovery = useMediaRecovery(el, src, isVideo ? 'The preview copy could not be loaded.' : 'The audio could not be loaded.');
  const trouble = recovery.trouble;
  const shownPlaying = recovery.willPlay ?? playing;
  const transcript = useOp('footage.transcript', asset.hasAudio ? { assetId: asset.id, words: true } : null);
  const evidence = useOp('footage.evidence', { assetId: asset.id });
  const tech = useMemo(() => technicalEvidence(evidence.data), [evidence.data]);
  const dur = asset.duration || el.current?.duration || 0;

  const toggle = useCallback(() => {
    const v = el.current;
    if (!v || recovery.toggle()) return;
    if (v.paused) void v.play().catch(() => undefined);
    else v.pause();
  }, [recovery]);
  const activate = usePlayerHotkey({ toggle }, !!src);

  const seekTo = useCallback(
    (t: number, play?: boolean) => {
      const v = el.current;
      const clamped = Math.max(0, Math.min(dur || t, t));
      setTime(clamped);
      if (v && recovery.seek(clamped)) {
        if (play && !recovery.willPlay) recovery.toggle();
      } else if (v) {
        v.currentTime = clamped;
        if (play) void v.play().catch(() => undefined);
      }
      activate();
    },
    [dur, activate, recovery],
  );

  // External seek requests (evidence citations, transcript search, deep links).
  // Applied once per request (nonce), never re-applied on later renders.
  const seekRef = useRef(seekTo);
  seekRef.current = seekTo;
  useEffect(() => {
    if (!seek) return;
    const v = el.current;
    if (!v) {
      setTime(seek.t);
      return;
    }
    const apply = () => seekRef.current(seek.t, seek.play);
    if (v.readyState >= 1) apply();
    else {
      v.addEventListener('loadedmetadata', apply, { once: true });
      return () => v.removeEventListener('loadedmetadata', apply);
    }
  }, [seek]);

  // Smooth playhead while playing (state only changes when the frame moves).
  useEffect(() => {
    if (!playing) return;
    let raf = 0;
    const tick = () => {
      const v = el.current;
      if (v) setTime((prev) => (Math.abs(prev - v.currentTime) > 0.015 ? v.currentTime : prev));
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [playing]);

  const lastReported = useRef(-1);
  useEffect(() => {
    if (!onTime) return;
    if (Math.abs(time - lastReported.current) < 0.25 && playing) return;
    lastReported.current = time;
    onTime(time);
  }, [time, onTime, playing]);

  useEffect(() => savePref('sourcePanel', panel), [panel]);
  useEffect(() => savePref('waveLayers', layers), [layers]);

  const portrait = (asset.height ?? 0) > (asset.width ?? 0);

  return (
    <div className={cx('grid gap-5', compact ? 'grid-cols-1' : 'grid-cols-1 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,1fr)]')}>
      <div className="min-w-0">
        <div
          className={cx(
            'relative overflow-hidden rounded-[14px] bg-inset',
            isVideo ? (portrait ? (mini ? 'mx-auto aspect-[9/16] max-h-[260px]' : 'mx-auto aspect-[9/16] max-h-[min(62dvh,640px)]') : mini ? 'mx-auto aspect-video max-h-[240px]' : 'aspect-video') : 'h-[180px]',
          )}
          data-player-surface
          onClick={() => activate()}
        >
          {isVideo && src ? (
            <video
              key={asset.id}
              ref={el}
              src={src}
              poster={asset.variants.includes('thumbnail') ? media.asset(asset.id, 'thumbnail') : undefined}
              preload="metadata"
              playsInline
              muted={muted}
              className="h-full w-full object-contain"
              onPlay={() => setPlaying(true)}
              onPause={() => setPlaying(false)}
              onEnded={() => setPlaying(false)}
              onWaiting={() => setBuffering(true)}
              onPlaying={() => setBuffering(false)}
              onCanPlay={() => setBuffering(false)}
              onSeeked={(e) => setTime(e.currentTarget.currentTime)}
              onTimeUpdate={(e) => !playing && setTime(e.currentTarget.currentTime)}
              onClick={toggle}
              onLoadedMetadata={(e) => {
                e.currentTarget.playbackRate = rate;
              }}
            />
          ) : isVideo ? (
            <div className="absolute inset-0">
              {asset.variants.includes('thumbnail') ? <img src={media.asset(asset.id, 'thumbnail')} alt="" className="h-full w-full object-contain opacity-50" /> : null}
              <div className="absolute inset-0 grid place-items-center">
                <div className="flex items-center gap-2 rounded-full bg-black/60 px-3 py-1.5 text-[12.5px] text-white">
                  <Spinner size={12} /> Making the preview copy…
                </div>
              </div>
            </div>
          ) : (
            <div className="flex h-full items-center gap-5 px-6">
              <div className="grid h-14 w-14 shrink-0 place-items-center rounded-[14px] bg-white/5 text-[#e8b169]">
                <MusicNotes size={26} />
              </div>
              <div className="min-w-0">
                <div className="truncate text-[15px] font-medium text-white"><Theirs>{asset.title}</Theirs></div>
                <div className="text-[12.5px] text-[#a69d93]">Audio clip, {fmtDuration(asset.duration)}</div>
              </div>
              {src ? (
                <audio
                  key={asset.id}
                  ref={el}
                  src={src}
                  preload="metadata"
                  muted={muted}
                  onPlay={() => setPlaying(true)}
                  onPause={() => setPlaying(false)}
                  onEnded={() => setPlaying(false)}
                  onSeeked={(e) => setTime(e.currentTarget.currentTime)}
                  onTimeUpdate={(e) => !playing && setTime(e.currentTarget.currentTime)}
                />
              ) : null}
            </div>
          )}
          {buffering && playing && !trouble ? (
            <div className="pointer-events-none absolute right-3 top-3 rounded-full bg-black/60 p-1.5 text-white">
              <Spinner size={14} />
            </div>
          ) : null}
          {trouble && trouble.kind !== 'failed' ? <TroubleLine trouble={trouble} /> : null}
          {trouble?.kind === 'failed' ? (
            <div className="absolute inset-x-3 bottom-3 rounded-[10px] bg-black/75 px-3 py-2 text-[12.5px] text-white" role="status" data-player-trouble="failed">
              {troubleLine(trouble)}
            </div>
          ) : null}
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-1.5">
          <IconButton label={shownPlaying ? 'Pause (Space)' : 'Play (Space)'} onClick={toggle} disabled={!src} className="bg-panel text-fg hairline">
            {shownPlaying ? <Pause size={17} weight="fill" /> : <Play size={17} weight="fill" />}
          </IconButton>
          <IconButton label="Back 5 seconds" className="hidden sm:inline-flex" onClick={() => seekTo(time - 5)} disabled={!src}>
            <Rewind size={16} />
          </IconButton>
          <IconButton label="Forward 5 seconds" className="hidden sm:inline-flex" onClick={() => seekTo(time + 5)} disabled={!src}>
            <FastForward size={16} />
          </IconButton>
          <div className="tc ml-1 text-[13px] text-fg">
            {timecode(time)} <span className="text-subtle">/ {timecode(dur)}</span>
          </div>
          <div className="flex-1" />
          <select
            aria-label="Playback speed"
            value={rate}
            onChange={(e) => {
              const r = Number(e.target.value);
              setRate(r);
              if (el.current) el.current.playbackRate = r;
            }}
            className="tc h-8 rounded-[8px] bg-transparent px-2 text-[12px] text-muted hairline hover:text-fg"
          >
            {[0.5, 0.75, 1, 1.25, 1.5, 2].map((r) => (
              <option key={r} value={r}>
                {r}×
              </option>
            ))}
          </select>
          <IconButton label={muted ? 'Unmute' : 'Mute'} onClick={() => setMuted(!muted)} disabled={!src}>
            {muted ? <SpeakerSlash size={16} /> : <SpeakerHigh size={16} />}
          </IconButton>
          {isVideo ? (
            <IconButton label="Full screen" disabled={!src} onClick={() => void el.current?.requestFullscreen?.().catch(() => undefined)}>
              <CornersOut size={16} />
            </IconButton>
          ) : null}
        </div>

        <div className="mt-3">
          <Waveform assetId={asset.id} duration={dur} time={time} onSeek={(t) => seekTo(t)} evidence={tech} layers={layers} range={marked ? { from: marked.in, to: marked.out } : range} enabled={asset.variants.includes('peaks')} />
          <WaveLegend layers={layers} onChange={setLayers} evidence={tech} onJump={(t) => seekTo(t)} />
          {markable && isVideo ? <MarkBar asset={asset} time={time} onSeek={(t) => seekTo(t)} onAdd={onAdd} /> : null}
          {(transcript.data?.speakers.length ?? 0) > 1 ? (
            <SpeakerLane segments={transcript.data?.segments ?? []} voices={voiceMap(transcript.data)} duration={dur} time={time} onSeek={(t) => seekTo(t)} />
          ) : null}
        </div>
      </div>

      <div className={cx('flex min-w-0 flex-col', compact ? '' : 'xl:max-h-[min(78dvh,760px)]')}>
        <Tabs<Panel>
          label="About this clip"
          value={panel}
          onChange={setPanel}
          items={[
            { value: 'transcript', label: 'Transcript' },
            ...(asset.hasAudio ? [{ value: 'speakers' as Panel, label: 'Speakers', count: transcript.data?.speakers.filter((v) => !v.minor || v.name).length || undefined }] : []),
            ...(isVideo ? [{ value: 'onscreen' as Panel, label: 'On screen' }, { value: 'sheet' as Panel, label: 'Frame grid' }] : []),
            { value: 'evidence', label: 'Scenes & pauses' },
            { value: 'details', label: 'Details' },
          ]}
        />
        <div className={cx('mt-3 flex-1', mini ? 'min-h-[120px]' : 'min-h-[240px]', panel === 'transcript' ? 'flex min-h-0 flex-col' : 'overflow-y-auto')}>
          {panel === 'transcript' ? (
            asset.hasAudio ? (
              <Transcript
                className={cx('min-h-0 flex-1', mini ? 'max-h-[220px]' : compact ? 'max-h-[360px]' : 'max-h-[420px] xl:max-h-none')}
                data={transcript.data}
                loading={transcript.isLoading}
                error={transcript.error}
                onRetry={() => void transcript.refetch()}
                time={time}
                onSeek={(t) => seekTo(t)}
                range={range}
                highlight={highlight}
                assetId={asset.id}
                wordActions={wordActions}
                duration={dur}
              />
            ) : (
              <EmptyState compact title="No sound" body="This clip has no sound, so there is nothing to transcribe." />
            )
          ) : panel === 'speakers' ? (
            <SpeakersPanel asset={asset} onSeek={(t) => seekTo(t)} segments={transcript.data?.segments} />
          ) : panel === 'onscreen' ? (
            <OnScreenPanel asset={asset} onSeek={(t) => seekTo(t)} />
          ) : panel === 'sheet' ? (
            <FrameGrid asset={asset} onSeek={(t) => seekTo(t)} />
          ) : panel === 'evidence' ? (
            <EvidencePanel loading={evidence.isLoading} error={evidence.error} onRetry={() => void evidence.refetch()} data={evidence.data} tech={tech} onSeek={(t) => seekTo(t)} />
          ) : (
            <DetailsPanel asset={asset} />
          )}
        </div>
      </div>
    </div>
  );
}

function EvidencePanel({ data, tech, loading, error, onRetry, onSeek }: {
  data: EvidenceOut | undefined;
  tech: TechnicalEvidence | undefined;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  onSeek: (t: number) => void;
}) {
  if (loading)
    return (
      <div className="space-y-2">
        <Skeleton className="h-16" />
        <Skeleton className="h-9" />
        <Skeleton className="h-9" />
      </div>
    );
  if (error) return <ErrorPanel error={error} onRetry={onRetry} compact />;
  if (!tech)
    return <EmptyState compact title="Scene analysis not ready yet" body={data?.analysis?.evidence === 'failed' ? 'The scene analysis failed. You can retry it from the activity panel.' : 'Scene changes and pauses are found on your computer after the clip is added.'} />;
  const changeLabel: Record<string, [string, string]> = {
    cut: ['Cut', 'a new shot'],
    gradual: ['Change', 'the picture changes slowly (a pan, zoom, light, or someone coming into view)'],
    motion: ['Movement', 'a burst of movement'],
  };
  const events: { at: number; to?: number; kind: string; detail: string }[] = [
    ...(tech.silences ?? []).map((s) => ({ at: s.from, to: s.to, kind: 'Pause', detail: `${(s.to - s.from).toFixed(2)}s of silence` })),
    ...(tech.visualChanges
      ? tech.visualChanges.map((c) => ({
          at: c.from,
          to: c.kind === 'cut' ? undefined : c.to,
          kind: changeLabel[c.kind]?.[0] ?? 'Change',
          detail: c.kind === 'cut' ? 'a new shot' : `${changeLabel[c.kind]?.[1] ?? 'a change'}, until ${clock(c.to)}`,
        }))
      : (tech.scenes ?? []).map((s) => ({ at: s.at, kind: 'Scene', detail: 'the picture changes' }))),
    ...(tech.black ?? []).map((s) => ({ at: s.from, to: s.to, kind: 'Black', detail: `${(s.to - s.from).toFixed(2)}s of black frames` })),
    ...(tech.freezes ?? []).map((s) => ({ at: s.from, to: s.to, kind: 'Freeze', detail: `${(s.to - s.from).toFixed(2)}s frozen picture` })),
  ].sort((a, b) => a.at - b.at);
  const l = tech.loudness;
  const measured = !!l?.measured;
  return (
    <div>
      <div className="grid grid-cols-3 gap-2">
        <Metric label="How loud overall" value={measured && l?.integratedLufs != null ? overallWords(l.integratedLufs) : '-'} />
        <Metric label="The loudest moments" value={measured && l?.truePeakDbfs != null ? peakWords(l.truePeakDbfs) : '-'} />
        <Metric label="Ups and downs" value={measured && l?.rangeLu != null ? rangeWords(l.rangeLu) : '-'} />
      </div>
      {measured ? (
        <Details compact label="Exact measurements" className="mt-2">
          <span className="tc">
            {l?.integratedLufs != null ? `${l.integratedLufs.toFixed(1)} LUFS integrated` : ''}
            {l?.truePeakDbfs != null ? ` · ${l.truePeakDbfs.toFixed(1)} dBFS true peak` : ''}
            {l?.rangeLu != null ? ` · ${l.rangeLu.toFixed(1)} LU range` : ''}
          </span>
        </Details>
      ) : null}
      <div className="mt-4 text-[12px] font-medium text-muted">What MetaRush noticed</div>
      {events.length === 0 ? (
        <div className="mt-2 rounded-[10px] px-3 py-3 text-[13px] text-muted hairline">No pauses, cuts, changes, movement, black or frozen frames detected.</div>
      ) : (
        <ul className="mt-2 space-y-0.5">
          {events.map((e, i) => (
            <li key={i}>
              <button type="button" onClick={() => onSeek(e.at)} className="flex w-full items-center gap-3 rounded-[9px] px-2.5 py-2 text-left hover:bg-hover">
                <span className="tc w-[74px] shrink-0 text-[12px] text-accent-text">{timecode(e.at)}</span>
                <Badge tone={e.kind === 'Scene' || e.kind === 'Cut' || e.kind === 'Change' || e.kind === 'Movement' ? 'accent' : e.kind === 'Pause' ? 'info' : 'danger'}>{e.kind}</Badge>
                <span className="truncate text-[12.5px] text-muted">{e.detail}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-[11.5px] leading-relaxed text-subtle">{data?.note}</p>
    </div>
  );
}

/** How loud a clip is overall, in words (the exact measurement is in Details). */
function overallWords(lufs: number): string {
  if (lufs >= -10) return 'Very loud';
  if (lufs >= -16) return 'About right for social video';
  if (lufs >= -23) return 'A little quiet';
  if (lufs >= -35) return 'Quiet';
  return 'Very quiet';
}

/** Whether the loudest moments reach the ceiling (where sound starts to crackle). */
function peakWords(dbfs: number): string {
  if (dbfs >= -0.1) return 'Hit the ceiling, may crackle';
  if (dbfs >= -1) return 'Close to the ceiling';
  return 'Room to spare';
}

/** How much the loudness goes up and down. */
function rangeWords(lu: number): string {
  if (lu < 5) return 'Steady';
  if (lu < 12) return 'Some ups and downs';
  return 'Big ups and downs';
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-[12px] bg-panel px-3 py-2.5 hairline">
      <div className="text-[11px] text-subtle">{label}</div>
      <div className="tc mt-0.5 truncate text-[13.5px] text-fg">{value}</div>
    </div>
  );
}

function DetailsPanel({ asset }: { asset: AssetSummary }) {
  const show = useOp('asset.show', { id: asset.id });
  const a = show.data?.asset;
  const v = a?.media.streams.find((s) => s.type === 'video');
  const au = a?.media.streams.find((s) => s.type === 'audio');
  const rows: [string, React.ReactNode][] = [
    ['Title', <Theirs className="break-words">{asset.title}</Theirs>],
    ['Original file', <Theirs className="tc break-all text-[12px]">{asset.name}</Theirs>],
    ...(asset.aliases?.length ? ([['Also arrived as', <span className="tc break-all text-[12px]">{asset.aliases.join(', ')}</span>]] as [string, React.ReactNode][]) : []),
    ['Duration', <span className="tc">{timecode(asset.duration)}</span>],
    ['Picture', resolution(asset.width, asset.height, asset.fps) || '-'],
    ['Sound', asset.hasAudio ? (au?.channels === 1 ? 'yes, one channel' : au?.channels === 2 ? 'yes, stereo' : 'yes') : 'none'],
    ['Good to know', [asset.vfr && 'changes its frame rate while it plays', asset.hdr && 'HDR', a?.media.rotation && `rotated ${a.media.rotation}°`].filter(Boolean).join(', ') || 'nothing special'],
    ['Size', bytes(asset.size)],
    [
      'Added',
      <span title={absolute(asset.createdAt)}>
        <span data-stamp>
          {stamp(asset.createdAt)}
        </span>{' '}
        by {asset.createdBy.userName ?? 'someone'} ({viaLabel(asset.createdBy.via)})
      </span>,
    ],
    ...sourceRows(asset.source),
    ['On this computer', show.data ? (show.data.cached ? 'yes, the original is here' : 'no') : '…'],
  ];
  return (
    <div>
      <dl className="divide-y divide-line overflow-hidden rounded-[12px] hairline">
        {rows.map(([k, val]) => (
          <div key={k} className="grid grid-cols-[96px_1fr] gap-3 px-3.5 py-2.5 text-[12.5px]">
            <dt className="text-subtle">{k}</dt>
            <dd className="min-w-0 text-fg">{val}</dd>
          </div>
        ))}
      </dl>
      <Details label="File details">
        <div>Video: {v ? `${v.codec}${v.profile ? ` ${v.profile}` : ''}${v.pixFmt ? `, ${v.pixFmt}` : ''}` : '-'}</div>
        <div>Audio: {au ? `${au.codec}, ${au.sampleRate ? `${au.sampleRate / 1000} kHz` : ''}${au.channels ? `, ${au.channels} ch` : ''}` : 'none'}</div>
        <div className="tc break-all">SHA-256: {asset.sha256}</div>
        <div className="tc">
          {asset.id} · {shortId(asset.sha256)}
        </div>
      </Details>
      <div className="mt-4 text-[12px] font-medium text-muted">What MetaRush made from it</div>
      {show.isLoading ? <Skeleton className="mt-2 h-24" /> : null}
      {show.error ? <ErrorPanel className="mt-2" compact error={show.error} onRetry={() => void show.refetch()} /> : null}
      <ul className="mt-2 space-y-1">
        {(show.data?.jobs ?? []).map((j) => (
          <li key={j.id} className="flex items-center gap-3 rounded-[9px] px-2.5 py-1.5 text-[12.5px] hover:bg-hover">
            <span className="w-[96px] shrink-0 text-muted">{jobLabel(j.type)}</span>
            <StateBadge state={j.state} />
            <span className="ml-auto truncate text-[11.5px] text-subtle" title={absolute(j.finishedAt ?? j.updatedAt)}>
              {j.error ? j.error : j.finishedAt ? relative(j.finishedAt) : j.progress.message || `${Math.round(j.progress.pct * 100)}%`}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
