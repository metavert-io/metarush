import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useQueries } from '@tanstack/react-query';
import {
  ArrowClockwise,
  ArrowCounterClockwise,
  ArrowsInLineHorizontal,
  CheckCircle,
  Lock,
  MagnetStraight,
  Keyboard,
  Minus,
  Plus,
  Scissors,
  SpeakerSlash,
  Trash,
  WarningCircle,
} from '@phosphor-icons/react';
import { call } from '../../api/client';
import type { AssetSummary, Clip, EditOp, RenderPlan, Spec, TranscriptOut } from '../../api/types';
import { applyOps, newClipID, partID, place, round3, speedOf } from '../../lib/editops';
import { draggedAssetIds, isAssetDrag } from '../../lib/dragAssets';
import { markedRange, timelineKeysActive } from '../../lib/marks';
import { duration as fmtDuration, timecode } from '../../lib/format';
import { isDialogOpen, isTypingTarget, useMediaQuery } from '../../lib/hooks';
import { useDialog } from '../../ui/Dialog';
import { Button, cx, IconButton, Spinner } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { Filmstrip, NestedStrip, WaveLane } from './lanes';
import { AngleMenu, ConversationStrip, shotsAfter, shotWords, type AngleChoice, type ShotSpan } from './ConversationStrip';
import { laneLines, moveLineOps, reanchorAt, type LaneLine } from './narration';
import { anchorWords } from '../../screens/studio/voiceover/Placement';
import { alt, mod, ShortcutSheet } from './shortcuts';
import type { TimelineEditor as Editor } from './useTimelineEditor';
import transitionCatalog from '@metarush/schema/schemas/transitions.json';

/*
 * The studio timeline (D58): a magnetic, gap-free storyline you edit by hand.
 * Drag clips to reorder (an insertion marker shows where they land), drag one
 * off to remove it, drag edges to trim (⌥ on a shared edge moves the cut
 * between two clips), S splits at the playhead, a stretch marked on the ruler
 * (drag, or I and O) is cut out with Delete, footage is dragged in from the
 * sources, and the marker between two clips opens the transition picker.
 * Narration lines sit in their own lane: drag one (or use the arrow keys on
 * it) and it is tied to what plays where it lands (⌥: the exact time).
 * Snapping (playhead, cuts, word boundaries; ⇧ turns it off), zoom (pinch,
 * ⌘-scroll, slider), keyboard alternatives for every drag, and touch
 * (long-press to drag, 44 px handles). Every gesture goes to the editor
 * (useTimelineEditor), which previews it with Go and saves it in the background.
 */

const TRANSITIONS = new Map(transitionCatalog.transitions.map((t) => [t.type, t]));
const PAD = 14; // px before time 0
const END_ROOM = 160; // px after the end (drop footage there)
const SNAP_PX = 8;

export interface PlayerControls {
  toggle(): void;
  play(): void;
  pause(): void;
  isPlaying(): boolean;
  rate: number;
  setRate(r: number): void;
}

export interface LaneSpec {
  key: string;
  label: string;
  height: number;
  render(ctx: { x: (t: number) => number; pps: number; total: number }): ReactNode;
}

interface Item {
  clip: Clip;
  index: number;
  start: number; // seconds: the picture (overlaps included)
  end: number;
  own0: number; // the part of the timeline it owns
  own1: number;
  inType: string;
  inSec: number;
}

function buildLayout(spec: Spec | undefined): { items: Item[]; total: number; fps: number } {
  if (!spec) return { items: [], total: 0, fps: 30 };
  const { placed, total, fps } = place(spec);
  const items = placed.map((p) => ({
    clip: spec.clips[p.index]!,
    index: p.index,
    start: p.from / fps,
    end: (p.from + p.dur) / fps,
    own0: p.ownFrom / fps,
    own1: p.ownTo / fps,
    inType: p.inType,
    inSec: p.inFrames / fps,
  }));
  return { items, total: total / fps, fps };
}

type Drag =
  | { kind: 'trim'; id: string; edge: 'in' | 'end'; roll: boolean; startX: number; to: number; moved: boolean; snap?: SnapHit }
  | { kind: 'move'; id: string; startX: number; startY: number; x: number; y: number; grabDX: number; index: number; remove: boolean; moved: boolean }
  | { kind: 'range'; anchor: number; cur: number; startX: number; moved: boolean; snap?: SnapHit }
  | { kind: 'scrub' }
  | { kind: 'drop'; index: number }
  | { kind: 'line'; id: string; startX: number; t0: number; t: number; exact: boolean; moved: boolean; snap?: SnapHit };

interface SnapHit {
  t: number;
  label: string;
}

/** A clip in plain words: its label, else its name in the cut sheet. */
export function clipName(c: Clip, assets?: Map<string, AssetSummary>): string {
  if (c.label) return `“${c.label}”`;
  const a = assets?.get(c.assetId);
  return a && c.id === slugish(a.title) ? `the ${a.title} clip` : `the ${c.id} clip`;
}
function slugish(s: string) {
  return s
    .replace(/\s*\((\d+ h )?\d+ (s|min)\)\s*$/, '') // a descriptive name's length (D150), as cut sheets leave it out
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^[-_]+|[-_]+$/g, '');
}

/** Durations as people say them, with the exact value. */
export function sayDuration(s: number): string {
  if (s <= 0.2) return 'a blink';
  if (s <= 0.4) return 'a moment';
  if (s <= 0.6) return 'half a second';
  if (s <= 0.85) return 'three quarters of a second';
  if (s <= 1.2) return 'a second';
  if (s <= 1.7) return 'a second and a half';
  if (s <= 2.4) return 'two seconds';
  return `${Math.round(s)} seconds`;
}

export function TimelineEditor({
  projectId,
  editor,
  assets,
  time,
  onSeek,
  player,
  selectedId,
  onSelectClip,
  onOpenTransition,
  extraLanes,
  narrationEmpty,
  onAddRequest,
}: {
  projectId: string;
  editor: Editor;
  assets: Map<string, AssetSummary>;
  /** The playhead, in seconds on the timeline. */
  time: number;
  onSeek: (t: number) => void;
  player: PlayerControls;
  selectedId?: string;
  onSelectClip: (id: string | undefined) => void;
  /** The marker between two clips: open the transition picker for the cut into this clip. */
  onOpenTransition: (clipId: string) => void;
  /** More lanes (graphics…) drawn under the clips, their sound and the narration. */
  extraLanes?: LaneSpec[];
  /** Shown in the narration lane while the cut has no narration (an "Add voiceover" button). */
  narrationEmpty?: ReactNode;
  /** "Add footage" button: the sources panel. */
  onAddRequest?: () => void;
}) {
  const toast = useToast();
  const dialog = useDialog();
  // Touch screens: a coarse pointer, or no hover at all (some report the pointer late).
  const coarsePointer = useMediaQuery('(pointer: coarse)');
  const noHover = useMediaQuery('(hover: none)');
  const coarse = coarsePointer || noHover;
  const narrow = useMediaQuery('(max-width: 639px)');
  const spec = editor.spec;
  const [drag, setDrag] = useState<Drag | null>(null);
  const [range, setRange] = useState<{ from?: number; to?: number }>({});
  const [snapping, setSnapping] = useState(true);
  const [message, setMessage] = useState<string>();
  const [focusEdge, setFocusEdge] = useState<{ id: string; edge: 'in' | 'end' }>();
  const scroller = useRef<HTMLDivElement | null>(null);
  const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null);
  const setScroller = useCallback((el: HTMLDivElement | null) => {
    scroller.current = el;
    setScrollEl(el);
  }, []);
  const [viewW, setViewW] = useState(900);
  const [zoomPps, setZoomPps] = useState<number>();
  const busyKeys = useRef(false);

  useEffect(() => {
    if (!scrollEl) return;
    const ro = new ResizeObserver(([e]) => setViewW(Math.floor(e?.contentRect.width ?? 900)));
    ro.observe(scrollEl);
    return () => ro.disconnect();
  }, [scrollEl]);

  // During a drag the timeline shows the gesture's result at once (a ghost made
  // here); after letting go it shows what Go makes of it.
  const ghostOps = useMemo<EditOp[]>(() => {
    if (!drag || !spec) return [];
    if (drag.kind === 'trim' && drag.moved) return [{ op: 'trimClip', id: drag.id, edge: drag.edge, to: drag.to, mode: drag.roll ? 'roll' : 'ripple' }];
    if (drag.kind === 'move' && drag.moved) {
      if (drag.remove) return [{ op: 'removeClip', id: drag.id }];
      return moveOps(spec, drag.id, drag.index);
    }
    return [];
  }, [drag, spec]);
  const shown = useMemo(() => {
    if (!spec || !ghostOps.length) return spec;
    try {
      return applyOps(spec, ghostOps);
    } catch {
      return spec;
    }
  }, [spec, ghostOps]);
  const layout = useMemo(() => buildLayout(shown), [shown]);
  const base = useMemo(() => buildLayout(spec), [spec]);
  const total = Math.max(layout.total, 0.001);
  const fitPps = Math.max(2, (viewW - PAD - 40) / Math.max(1, base.total));
  const pps = Math.min(600, Math.max(fitPps * 0.5, zoomPps ?? fitPps));
  const x = useCallback((t: number) => PAD + t * pps, [pps]);
  const tAt = useCallback(
    (clientX: number) => {
      const el = scroller.current;
      if (!el) return 0;
      const r = el.getBoundingClientRect();
      return (clientX - r.left + el.scrollLeft - PAD) / pps;
    },
    [pps],
  );
  const contentW = Math.max(viewW, PAD + total * pps + END_ROOM);
  const frame = 1 / (layout.fps || 30);

  // Words, for snapping to what people say.
  const assetIds = useMemo(() => [...new Set((spec?.clips ?? []).map((c) => c.assetId))].filter((id) => assets.get(id)?.hasAudio), [spec, assets]);
  const transcripts = useQueries({
    queries: assetIds.map((id) => ({
      queryKey: ['footage.transcript', { assetId: id, words: true }],
      queryFn: ({ signal }: { signal: AbortSignal }) => call('footage.transcript', { assetId: id, words: true }, signal),
      staleTime: 5 * 60_000,
    })),
  });
  const wordsKey = assetIds.map((id, i) => `${id}:${transcripts[i]?.dataUpdatedAt ?? 0}`).join('|');
  const words = useMemo(() => {
    const m = new Map<string, { f: number; t: number; w: string }[]>();
    assetIds.forEach((id, i) => {
      const d = transcripts[i]?.data as TranscriptOut | undefined;
      if (d)
        m.set(
          id,
          d.segments.flatMap((s) => (s.words ?? []).map((w) => ({ f: w.f, t: w.t, w: w.w.trim() }))),
        );
    });
    return m;
  }, [wordsKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const selected = selectedId ? layout.items.find((it) => it.clip.id === selectedId) : undefined;
  const under = (t: number) => base.items.find((it) => t >= it.own0 && t < it.own1) ?? (t >= base.total ? base.items[base.items.length - 1] : undefined);

  // Narration lines where Go placed them; a line just dropped stays where it
  // landed until Go's answer (a new plan) arrives.
  const lines = useMemo(() => laneLines(spec, editor.plan), [spec, editor.plan]);
  const [landed, setLanded] = useState<{ id: string; t: number; plan?: RenderPlan }>();
  const lineStart = (l: LaneLine) => (drag?.kind === 'line' && drag.id === l.id && drag.moved ? drag.t : landed?.id === l.id && landed.plan === editor.plan ? landed.t : l.start);

  const say = (m: string | null | undefined) => {
    if (m) setMessage(m);
  };
  const act = (label: string, ops: EditOp[]) => {
    const err = editor.apply(label, ops);
    if (err) say(err);
    else setMessage(undefined);
    return !err;
  };

  /* ---------- conversation clips: change the angle (D102) ---------- */

  const pickAngle = async (it: Item, span: ShotSpan) => {
    if (!spec) return;
    const clip = it.clip;
    const at = clip.in + (time - it.start) * speedOf(clip); // the playhead on the conversation's clock
    const here = at > span.from + 0.3 && at < span.to - 0.3 ? at : undefined;
    const choice = await dialog.open<AngleChoice>((close) => <AngleMenu clip={clip} spec={spec} span={span} here={here} onChoose={(c) => close(c)} />, { title: 'Change the angle', size: 'sm' });
    if (!choice) return;
    const shots = shotsAfter(clip, spec, span, choice, here);
    const what = choice.remove ? 'Remove a switch' : `Show ${shotWords({ at: 0, ...choice.shot }, spec, clip)}`;
    act(what, [{ op: 'updateClip', id: clip.id, set: { shots } }]);
  };

  /* ---------- snapping ---------- */

  const snapTo = (candidates: SnapHit[], t: number, shift: boolean): { t: number; hit?: SnapHit } => {
    if (!snapping || shift) return { t };
    let best: SnapHit | undefined;
    for (const c of candidates) if (Math.abs(c.t - t) * pps <= SNAP_PX && (!best || Math.abs(c.t - t) < Math.abs(best.t - t))) best = c;
    return best ? { t: best.t, hit: best } : { t };
  };
  const timelineCandidates = (exclude?: string): SnapHit[] => {
    const out: SnapHit[] = [
      { t: time, label: 'playhead' },
      { t: 0, label: 'start' },
      { t: base.total, label: 'end' },
    ];
    for (const it of base.items) {
      if (it.clip.id !== exclude) out.push({ t: it.own0, label: 'cut' });
      for (const w of words.get(it.clip.assetId) ?? []) {
        const ts = it.start + (w.f - it.clip.in) / speedOf(it.clip);
        const te = it.start + (w.t - it.clip.in) / speedOf(it.clip);
        if (ts >= it.own0 && ts <= it.own1) out.push({ t: ts, label: `before “${w.w}”` });
        if (te >= it.own0 && te <= it.own1) out.push({ t: te, label: `after “${w.w}”` });
      }
    }
    return out;
  };

  /* ---------- narration lines ---------- */

  const lineLocked = () => say('That line is locked, so it stays where it is.');
  const startLine = (e: React.PointerEvent, l: LaneLine) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    if (l.locked) return lineLocked();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    editor.setBusy(true);
    setDrag({ kind: 'line', id: l.id, startX: e.clientX, t0: l.start, t: l.start, exact: e.altKey, moved: false });
  };
  /** Puts a line at timeline second t: tied to what plays there, or (exact) to the clock. */
  const placeLine = (l: LaneLine, t: number, exact: boolean) => {
    const plan = editor.plan;
    if (!spec || !plan) return;
    const anchor = reanchorAt(spec, plan, l.index, t, exact);
    const ops = moveLineOps(l, anchor);
    if (!ops.length) return;
    if (act(`Moved line ${l.n}: it starts ${anchorWords(spec, anchor)}`, ops)) setLanded({ id: l.id, t, plan });
  };
  const lineKeys = (e: React.KeyboardEvent, l: LaneLine) => {
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      // Handled here, so the timeline's own arrow keys (the playhead) stay out of it.
      e.preventDefault();
      e.stopPropagation();
      if (l.locked) return lineLocked();
      const step = (e.key === 'ArrowLeft' ? -1 : 1) * (e.shiftKey ? 1 : 0.1);
      placeLine(l, Math.max(0, Math.min(base.total - 0.1, round3(l.start + step))), e.altKey);
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      e.stopPropagation();
      onSeek(l.start);
    }
  };

  /* ---------- trims ---------- */

  const minLen = (c: Clip) => 2 * frame * speedOf(c);
  const startTrim = (e: React.PointerEvent, it: Item, edge: 'in' | 'end') => {
    if (it.clip.locked) return say('That clip is locked, so it stays as it is.');
    e.stopPropagation();
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    onSelectClip(it.clip.id);
    editor.setBusy(true);
    setDrag({ kind: 'trim', id: it.clip.id, edge, roll: e.altKey, startX: e.clientX, to: edge === 'in' ? it.clip.in : it.clip.out, moved: false });
  };
  const trimTo = (it: Item, edge: 'in' | 'end', roll: boolean, dx: number, shift: boolean): { to: number; hit?: SnapHit } => {
    const c = it.clip;
    const sp = speedOf(c);
    const asset = assets.get(c.assetId);
    const dur = asset?.duration ?? Infinity;
    const edgeT0 = edge === 'in' ? it.own0 : it.own1; // where the edge is on the timeline now
    const want = edgeT0 + dx / pps;
    // Candidates on the timeline for this edge: the playhead, other cuts, and this footage's words.
    const cands: SnapHit[] = [{ t: time, label: 'playhead' }];
    for (const o of base.items) if (o.clip.id !== c.id) cands.push({ t: o.own0, label: 'cut' }, { t: o.own1, label: 'cut' });
    for (const w of words.get(c.assetId) ?? []) {
      cands.push({ t: it.start + (w.f - c.in) / sp, label: `before “${w.w}”` }, { t: it.start + (w.t - c.in) / sp, label: `after “${w.w}”` });
    }
    const s = snapTo(cands, want, shift);
    let to = edge === 'in' ? c.in + (s.t - it.start) * sp : c.out + (s.t - it.own1) * sp;
    if (edge === 'in') to = Math.min(Math.max(0, to), c.out - minLen(c));
    else to = Math.max(Math.min(dur, to), c.in + minLen(c));
    if (roll) {
      const nb = base.items[it.index + (edge === 'end' ? 1 : -1)];
      if (nb) {
        const delta = (to - (edge === 'in' ? c.in : c.out)) / sp; // timeline seconds the cut moves
        const nsp = speedOf(nb.clip);
        if (edge === 'end') {
          const nin = nb.clip.in + delta * nsp;
          if (nin < 0) to = c.out - (nb.clip.in / nsp) * sp;
          if (nin > nb.clip.out - minLen(nb.clip)) to = c.out + ((nb.clip.out - minLen(nb.clip) - nb.clip.in) / nsp) * sp;
        } else {
          const nout = nb.clip.out + delta * nsp;
          const ndur = assets.get(nb.clip.assetId)?.duration ?? Infinity;
          if (nout > ndur) to = c.in + ((ndur - nb.clip.out) / nsp) * sp;
          if (nout < nb.clip.in + minLen(nb.clip)) to = c.in - ((nb.clip.out - nb.clip.in - minLen(nb.clip)) / nsp) * sp;
        }
      }
    }
    return { to: round3(to), hit: s.hit };
  };

  /* ---------- moving ---------- */

  const longPress = useRef<{ timer: number; x: number; y: number; it: Item } | null>(null);
  const startMove = (e: React.PointerEvent, it: Item) => {
    if (e.button !== 0) return;
    const el = e.currentTarget as HTMLElement;
    const begin = () => {
      if (it.clip.locked) {
        say('That clip is locked, so it stays where it is.');
        return;
      }
      el.setPointerCapture(e.pointerId);
      editor.setBusy(true);
      setDrag({
        kind: 'move',
        id: it.clip.id,
        startX: e.clientX,
        startY: e.clientY,
        x: e.clientX,
        y: e.clientY,
        grabDX: e.clientX - el.getBoundingClientRect().left,
        index: it.index,
        remove: false,
        moved: e.pointerType === 'touch',
      });
    };
    if (e.pointerType === 'touch') {
      // Long-press to pick a clip up; a plain swipe scrolls the timeline.
      const timer = window.setTimeout(() => {
        longPress.current = null;
        navigator.vibrate?.(12);
        begin();
      }, 380);
      longPress.current = { timer, x: e.clientX, y: e.clientY, it };
      return;
    }
    begin();
  };

  const insertIndexAt = (clientX: number, exclude?: string) => {
    const t = tAt(clientX);
    let n = 0;
    for (const it of base.items) {
      if (it.clip.id === exclude) continue;
      if ((it.own0 + it.own1) / 2 < t) n++;
    }
    return n;
  };

  /* ---------- pointer moves and releases (the scroller captures them) ---------- */

  const lanesRef = useRef<HTMLDivElement>(null);
  // A finger that moves before the long-press fires is scrolling, not dragging;
  // one that lifts without moving was a tap: it selects the clip.
  const cancelLongPress = (e: { clientX: number; clientY: number }, lifted = false) => {
    const lp = longPress.current;
    if (!lp) return;
    const still = Math.hypot(e.clientX - lp.x, e.clientY - lp.y) <= 10;
    if (lifted || !still) {
      clearTimeout(lp.timer);
      longPress.current = null;
      if (lifted && still) {
        onSelectClip(lp.it.clip.id);
        onSeek(Math.max(lp.it.own0, Math.min(lp.it.own1 - frame, tAt(e.clientX))));
      }
    }
  };
  type Ptr = { clientX: number; clientY: number; altKey: boolean; shiftKey: boolean };
  const onPointerMove = (e: Ptr) => {
    if (!drag) return;
    if (drag.kind === 'trim') {
      const it = base.items.find((i) => i.clip.id === drag.id);
      if (!it) return;
      const dx = e.clientX - drag.startX;
      const r = trimTo(it, drag.edge, drag.roll || e.altKey, dx, e.shiftKey);
      setDrag({ ...drag, roll: drag.roll || e.altKey, to: r.to, snap: r.hit, moved: drag.moved || Math.abs(dx) > 2 });
    } else if (drag.kind === 'move') {
      const box = lanesRef.current?.getBoundingClientRect();
      const remove = !!box && (e.clientY < box.top - 56 || e.clientY > box.bottom + 56);
      const moved = drag.moved || Math.hypot(e.clientX - drag.startX, e.clientY - drag.startY) > 4;
      setDrag({ ...drag, x: e.clientX, y: e.clientY, remove, moved, index: insertIndexAt(e.clientX - drag.grabDX + 20, drag.id) });
    } else if (drag.kind === 'range') {
      const s = snapTo(timelineCandidates(), Math.max(0, Math.min(base.total, tAt(e.clientX))), e.shiftKey);
      setDrag({ ...drag, cur: s.t, snap: s.hit, moved: drag.moved || Math.abs(e.clientX - drag.startX) > 3 });
    } else if (drag.kind === 'scrub') {
      onSeek(Math.max(0, Math.min(base.total, tAt(e.clientX))));
    } else if (drag.kind === 'line') {
      // The line's start follows the pointer and snaps to clip starts, words and the playhead.
      const want = Math.max(0, Math.min(base.total - 0.1, drag.t0 + (e.clientX - drag.startX) / pps));
      const s = snapTo(timelineCandidates(), want, e.shiftKey);
      setDrag({ ...drag, t: s.t, snap: s.hit, exact: e.altKey, moved: drag.moved || Math.abs(e.clientX - drag.startX) > 3 });
    }
  };

  const onPointerUp = (e: Ptr) => {
    if (!drag) return;
    const d = drag;
    setDrag(null);
    editor.setBusy(false);
    if (d.kind === 'trim') {
      if (!d.moved) return;
      const it = base.items.find((i) => i.clip.id === d.id);
      if (!it) return;
      if (Math.abs(d.to - (d.edge === 'in' ? it.clip.in : it.clip.out)) < 0.0005) {
        say(
          d.roll
            ? "The cut can't move that way: one of the two clips has no more footage on that side."
            : d.edge === 'in'
              ? "The clip can't start earlier: that's where its footage begins."
              : "The clip can't end later: that's where its footage ends.",
        );
        return;
      }
      const verb = d.roll ? 'Moved the cut' : d.edge === 'in' ? 'Trimmed the start of' : 'Trimmed the end of';
      act(d.roll ? `${verb} next to ${clipName(it.clip, assets)}` : `${verb} ${clipName(it.clip, assets)}`, [
        { op: 'trimClip', id: d.id, edge: d.edge, to: d.to, ...(d.roll ? { mode: 'roll' as const } : {}) },
      ]);
    } else if (d.kind === 'move') {
      const it = base.items.find((i) => i.clip.id === d.id);
      if (!it) return;
      if (!d.moved) {
        onSelectClip(it.clip.id);
        onSeek(Math.max(it.own0, Math.min(it.own1 - frame, tAt(e.clientX))));
        return;
      }
      if (d.remove) removeClips([it.clip.id]);
      else {
        const ops = moveOps(spec!, d.id, d.index);
        if (ops.length) act(`Moved ${clipName(it.clip, assets)}`, ops);
      }
    } else if (d.kind === 'range') {
      if (!d.moved) {
        setRange({});
        onSeek(Math.max(0, Math.min(base.total, d.anchor)));
        return;
      }
      setRange({ from: Math.min(d.anchor, d.cur), to: Math.max(d.anchor, d.cur) });
    } else if (d.kind === 'line') {
      const l = lines.find((x) => x.id === d.id);
      if (!l) return;
      if (!d.moved) {
        onSeek(l.start);
        return;
      }
      placeLine(l, d.t, d.exact || e.altKey);
    }
  };

  // While dragging, the window follows the pointer (the element that started a
  // drag may leave the page, as a clip dragged off the timeline does).
  const handlers = useRef({ move: onPointerMove, up: onPointerUp });
  handlers.current = { move: onPointerMove, up: onPointerUp };
  const pointerDrag = !!drag && drag.kind !== 'drop';
  useEffect(() => {
    if (!pointerDrag) return;
    const mv = (e: PointerEvent) => handlers.current.move(e);
    const up = (e: PointerEvent) => handlers.current.up(e);
    const cancel = () => {
      setDrag(null);
      editor.setBusy(false);
    };
    window.addEventListener('pointermove', mv);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', cancel);
    return () => {
      window.removeEventListener('pointermove', mv);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', cancel);
    };
  }, [pointerDrag]); // eslint-disable-line react-hooks/exhaustive-deps

  /* ---------- gestures as edits ---------- */

  const removeClips = (ids: string[]) => {
    if (!spec) return;
    const clips = ids.map((id) => spec.clips.find((c) => c.id === id)).filter((c): c is Clip => !!c);
    if (!clips.length) return;
    if (clips.length >= spec.clips.length) return say('A cut needs at least one clip.');
    const label = clips.length === 1 ? `Removed ${clipName(clips[0]!, assets)}` : `Removed ${clips.length} clips`;
    if (
      act(
        label,
        clips.map((c) => ({ op: 'removeClip', id: c.id })),
      )
    ) {
      onSelectClip(undefined);
      toast({ title: label, body: 'Earlier versions keep it.', action: { label: 'Undo', onClick: () => say(editor.undo()) } });
    }
  };

  const split = () => {
    const it = under(time);
    if (!it || !spec) return;
    if (it.clip.locked) return say('That clip is locked, so it stays as it is.');
    const at = round3(it.clip.in + (time - it.start) * speedOf(it.clip));
    if (time - it.own0 < frame || it.own1 - time < frame) return say('Move the playhead inside a clip to split it there.');
    const id = partID(spec, it.clip.id);
    if (act(`Split ${clipName(it.clip, assets)}`, [{ op: 'splitClip', id: it.clip.id, at, newId: id }])) onSelectClip(id);
  };

  const trimToPlayhead = (edge: 'in' | 'end') => {
    const it = (selected && time >= selected.own0 && time <= selected.own1 ? selected : undefined) ?? under(time);
    if (!it) return;
    if (it.clip.locked) return say('That clip is locked, so it stays as it is.');
    const src = round3(it.clip.in + (time - it.start) * speedOf(it.clip));
    act(edge === 'in' ? `Trimmed the start of ${clipName(it.clip, assets)}` : `Trimmed the end of ${clipName(it.clip, assets)}`, [{ op: 'trimClip', id: it.clip.id, edge, to: src }]);
    if (edge === 'in') onSeek(it.own0);
  };

  const deleteRange = () => {
    const { from, to } = range;
    if (from === undefined || to === undefined || to - from < frame) return say('Mark a stretch first: drag on the ruler, or press I and O.');
    if (act(`Cut out ${timecode(from, { ms: false })} to ${timecode(to, { ms: false })}`, [{ op: 'deleteRange', from: round3(from), to: round3(to) }])) {
      setRange({});
      onSeek(from);
      toast({ title: `Cut out ${fmtDuration(to - from)}`, body: 'What came after moved up.', action: { label: 'Undo', onClick: () => say(editor.undo()) } });
    }
  };

  const nudgeEdge = (it: Item, edge: 'in' | 'end', frames: number, roll: boolean) => {
    if (it.clip.locked) return say('That clip is locked, so it stays as it is.');
    const r = trimTo(it, edge, roll, frames * frame * pps, true);
    const label = roll ? `Moved the cut next to ${clipName(it.clip, assets)}` : `Trimmed ${clipName(it.clip, assets)}`;
    act(label, [{ op: 'trimClip', id: it.clip.id, edge, to: r.to, ...(roll ? { mode: 'roll' as const } : {}) }]);
  };

  const moveBy = (it: Item, d: -1 | 1) => {
    if (!spec) return;
    if (it.clip.locked) return say('That clip is locked, so it stays where it is.');
    const others = spec.clips.filter((c) => c.id !== it.clip.id).length;
    const target = Math.max(0, Math.min(others, it.index + d));
    const ops = moveOps(spec, it.clip.id, target);
    if (ops.length) act(`Moved ${clipName(it.clip, assets)}`, ops);
  };

  const slip = (it: Item, by: number) => {
    if (it.clip.locked) return say('That clip is locked, so it stays as it is.');
    const dur = assets.get(it.clip.assetId)?.duration ?? Infinity;
    const b = Math.max(-it.clip.in, Math.min(dur - it.clip.out, by));
    if (Math.abs(b) < 0.0005) return say("The footage doesn't go further that way.");
    act(`Slid the footage in ${clipName(it.clip, assets)}`, [{ op: 'slipClip', id: it.clip.id, by: round3(b) }]);
  };

  /* ---------- footage dragged in from the sources ---------- */

  const onDragOver = (e: React.DragEvent) => {
    if (!isAssetDrag(e.dataTransfer)) return;
    e.preventDefault();
    // Library drags allow "link" only (lib/dragAssets); a different effect would refuse the drop.
    e.dataTransfer.dropEffect = e.dataTransfer.effectAllowed === 'link' ? 'link' : 'copy';
    const index = insertIndexAt(e.clientX);
    if (drag?.kind !== 'drop' || drag.index !== index) setDrag({ kind: 'drop', index });
  };
  const onDrop = async (e: React.DragEvent) => {
    const ids = draggedAssetIds(e.dataTransfer);
    if (!ids?.length || !spec) return;
    e.preventDefault();
    const index = insertIndexAt(e.clientX);
    setDrag(null);
    await addFootage(ids, index);
  };
  const addFootage = async (ids: string[], index: number) => {
    if (!spec) return;
    const ops: EditOp[] = [];
    let s = spec;
    let at = index;
    for (const id of ids) {
      const a =
        assets.get(id) ??
        (await call('asset.show', { id })
          .then((o) => o.asset as unknown as AssetSummary)
          .catch(() => undefined));
      if (!a) continue;
      if (a.kind !== 'video') {
        say(`${a.title} has no picture, so it can't be a clip.`);
        continue;
      }
      if (!assets.has(id)) {
        // Footage from the inbox joins the project first (a cut only uses its own footage).
        const ok = await call('asset.link', { assetId: id, projectId }).then(
          () => true,
          () => false,
        );
        if (!ok) {
          say(`${a.title} couldn't be added to this project.`);
          continue;
        }
      }
      const r = markedRange(a);
      const clip: Clip = { id: newClipID(s, a.title || a.name), assetId: a.id, in: round3(r.in), out: round3(r.out), speed: 1 };
      const op: EditOp = at > 0 ? { op: 'insertClip', after: s.clips[at - 1]!.id, clip } : { op: 'insertClip', before: s.clips[0]?.id, clip };
      ops.push(op);
      s = applyOps(s, [op]);
      at++;
    }
    if (ops.length) act(ops.length === 1 ? `Added ${ops[0]!.clip!.id}` : `Added ${ops.length} clips`, ops);
  };

  /* ---------- keyboard ---------- */

  const keys = useRef<(e: KeyboardEvent) => void>(() => undefined);
  keys.current = (e: KeyboardEvent) => {
    if (isTypingTarget(e.target) || isDialogOpen() || !spec) return;
    const target = e.target as HTMLElement | null;
    if (target?.closest('[data-no-timeline-keys]')) return;
    const cmd = e.metaKey || e.ctrlKey;
    const k = e.key;
    const fs = e.shiftKey ? 1 / frame : 1; // frames per press
    const focused = focusEdge ? base.items.find((i) => i.clip.id === focusEdge.id) : undefined;
    const sel = selectedId ? base.items.find((i) => i.clip.id === selectedId) : undefined;
    const handled = () => {
      e.preventDefault();
      e.stopPropagation();
    };
    if (cmd && (k === 'z' || k === 'Z')) {
      handled();
      say(e.shiftKey ? editor.redo() : editor.undo());
      return;
    }
    if (cmd && (k === 'y' || k === 'Y')) {
      handled();
      say(editor.redo());
      return;
    }
    if (!timelineKeysActive()) return; // the source viewer was used last: its keys (I, O…) are its own
    if (k === 'ArrowLeft' || k === 'ArrowRight') {
      const d = k === 'ArrowLeft' ? -1 : 1;
      if (focused && focusEdge && !cmd) {
        handled();
        nudgeEdge(focused, focusEdge.edge, d * fs, e.altKey);
        return;
      }
      if (cmd && sel) {
        handled();
        moveBy(sel, d as -1 | 1);
        return;
      }
      if (e.altKey && sel) {
        handled();
        slip(sel, d * fs * frame * speedOf(sel.clip));
        return;
      }
      if (cmd || e.altKey) return;
      handled();
      onSeek(Math.max(0, Math.min(base.total, time + d * fs * frame)));
      return;
    }
    if (cmd || e.altKey) return;
    switch (k) {
      case 'ArrowUp':
      case 'ArrowDown': {
        handled();
        const cuts = [0, ...base.items.slice(1).map((i) => i.own0), base.total];
        const next = k === 'ArrowDown' ? cuts.find((c) => c > time + frame / 2) : [...cuts].reverse().find((c) => c < time - frame / 2);
        if (next !== undefined) onSeek(next);
        return;
      }
      case 'Home':
        handled();
        onSeek(0);
        return;
      case 'End':
        handled();
        onSeek(base.total);
        return;
      case 's':
      case 'S':
        handled();
        split();
        return;
      case '[':
        handled();
        trimToPlayhead('in');
        return;
      case ']':
        handled();
        trimToPlayhead('end');
        return;
      case 'i':
      case 'I':
        handled();
        setRange((r) => ({ from: time, to: r.to !== undefined && r.to > time ? r.to : undefined }));
        return;
      case 'o':
      case 'O':
        handled();
        setRange((r) => ({ from: r.from !== undefined && r.from < time ? r.from : 0, to: time }));
        return;
      case 'Escape':
        if (range.from !== undefined || range.to !== undefined || selectedId) {
          handled();
          setRange({});
          onSelectClip(undefined);
          setFocusEdge(undefined);
        }
        return;
      case 'Delete':
      case 'Backspace':
        handled();
        if (range.from !== undefined && range.to !== undefined) deleteRange();
        else if (selectedId) removeClips([selectedId]);
        else say('Select a clip (or mark a stretch on the ruler) to remove it.');
        return;
      case 'j':
      case 'J':
        handled();
        player.setRate(player.isPlaying() && player.rate < 0 ? Math.max(-4, player.rate * 2) : -1);
        player.play();
        return;
      case 'k':
      case 'K':
        handled();
        player.pause();
        player.setRate(1);
        return;
      case 'l':
      case 'L':
        handled();
        player.setRate(player.isPlaying() && player.rate > 0 ? Math.min(4, player.rate * 2) : 1);
        player.play();
        return;
      case '=':
      case '+':
        handled();
        setZoomPps(Math.min(600, pps * 1.5));
        return;
      case '-':
      case '_':
        handled();
        setZoomPps(Math.max(fitPps * 0.5, pps / 1.5));
        return;
      case '\\':
        handled();
        setZoomPps(undefined);
        return;
      case '?':
        handled();
        void dialog.open(() => <ShortcutSheet />, { title: 'Timeline shortcuts', size: 'md' });
        return;
    }
  };
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (busyKeys.current) return;
      keys.current(e);
    };
    window.addEventListener('keydown', on);
    return () => window.removeEventListener('keydown', on);
  }, []);

  // While a clip or an edge is being dragged with a finger, the page must not scroll.
  const dragRef = useRef(drag);
  dragRef.current = drag;
  useEffect(() => {
    if (!scrollEl) return;
    const stop = (e: TouchEvent) => {
      if (dragRef.current && dragRef.current.kind !== 'drop') e.preventDefault();
    };
    scrollEl.addEventListener('touchmove', stop, { passive: false });
    return () => scrollEl.removeEventListener('touchmove', stop);
  }, [scrollEl]);

  /* ---------- zoom: pinch, ⌘/Ctrl-scroll, slider ---------- */

  const zoomAround = (factor: number, clientX: number) => {
    const el = scroller.current;
    if (!el) return;
    const t = tAt(clientX);
    const next = Math.min(600, Math.max(fitPps * 0.5, pps * factor));
    setZoomPps(next);
    requestAnimationFrame(() => {
      const r = el.getBoundingClientRect();
      el.scrollLeft = PAD + t * next - (clientX - r.left);
    });
  };
  const zoomRef = useRef(zoomAround);
  zoomRef.current = zoomAround;
  useEffect(() => {
    if (!scrollEl) return;
    const onWheel = (e: WheelEvent) => {
      if (!(e.ctrlKey || e.metaKey)) return;
      e.preventDefault();
      zoomRef.current(Math.exp(-e.deltaY * 0.01), e.clientX);
    };
    scrollEl.addEventListener('wheel', onWheel, { passive: false });
    return () => scrollEl.removeEventListener('wheel', onWheel);
  }, [scrollEl]);
  const pinch = useRef<{ d: number; pps: number } | null>(null);
  const onTouchStart = (e: React.TouchEvent) => {
    if (e.touches.length === 2) {
      const [a, b] = [e.touches[0]!, e.touches[1]!];
      pinch.current = { d: Math.abs(a.clientX - b.clientX) || 1, pps };
    }
  };
  const onTouchMove = (e: React.TouchEvent) => {
    if (e.touches.length !== 2 || !pinch.current) return;
    const [a, b] = [e.touches[0]!, e.touches[1]!];
    const d = Math.abs(a.clientX - b.clientX) || 1;
    setZoomPps(Math.min(600, Math.max(fitPps * 0.5, (pinch.current.pps * d) / pinch.current.d)));
  };

  // Keep the playhead in view while playing.
  useEffect(() => {
    const el = scroller.current;
    if (!el || drag) return;
    const px = x(time);
    if (px < el.scrollLeft + 20 || px > el.scrollLeft + el.clientWidth - 40) el.scrollLeft = Math.max(0, px - el.clientWidth * 0.3);
  }, [time, x, drag]);

  /* ---------- drawing ---------- */

  const videoH = narrow ? 58 : 72;
  const waveH = narrow ? 26 : 34;
  const cutsH = 24;
  const rulerH = 26;
  const lineH = narrow ? 28 : 32;
  const ticks = useMemo(() => {
    const target = 90 / pps;
    let step = 1200;
    for (const s of [0.1, 0.25, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600]) {
      if (s >= target) {
        step = s;
        break;
      }
    }
    const out: number[] = [];
    for (let t = 0; t <= total + 1e-6; t += step) out.push(Math.round(t * 1000) / 1000);
    return { step, out };
  }, [pps, total]);

  const dragging = drag?.kind === 'move' && drag.moved ? drag : undefined;
  const draggedItem = dragging ? base.items.find((i) => i.clip.id === dragging.id) : undefined;
  const insertX = (() => {
    if (drag?.kind === 'drop') {
      const items = base.items;
      return drag.index >= items.length ? x(base.total) : x(items[drag.index]!.own0);
    }
    if (dragging && !dragging.remove) {
      const placed = layout.items.find((i) => i.clip.id === dragging.id);
      return placed ? x(placed.own0) : undefined;
    }
    return undefined;
  })();
  const rangeNow: { from: number; to: number } | undefined =
    drag?.kind === 'range' && drag.moved
      ? { from: Math.min(drag.anchor, drag.cur), to: Math.max(drag.anchor, drag.cur) }
      : range.from !== undefined && range.to !== undefined
        ? { from: range.from, to: range.to }
        : undefined;
  const snapLine = drag?.kind === 'trim' || drag?.kind === 'range' || drag?.kind === 'line' ? drag.snap : undefined;
  const lanesH = cutsH + videoH + 6 + waveH + 6 + lineH + (extraLanes ?? []).reduce((n, l) => n + l.height + 6, 0);
  const lineDrag = drag?.kind === 'line' && drag.moved ? drag : undefined;

  if (!spec) {
    return (
      <div className="grid h-40 place-items-center">
        <Spinner size={16} label="Loading the cut" />
      </div>
    );
  }

  return (
    <section aria-label="Timeline editor" className="select-none" data-timeline-editor>
      <Toolbar
        editor={editor}
        snapping={snapping}
        onSnapping={setSnapping}
        pps={pps}
        fitPps={fitPps}
        onZoom={(v) => setZoomPps(v)}
        onSplit={split}
        onDelete={() => (rangeNow ? deleteRange() : selectedId ? removeClips([selectedId]) : say('Select a clip (or mark a stretch on the ruler) to remove it.'))}
        canDelete={!!rangeNow || !!selectedId}
        onShortcuts={() => void dialog.open(() => <ShortcutSheet />, { title: 'Timeline shortcuts', size: 'md' })}
        onUndo={() => say(editor.undo())}
        onRedo={() => say(editor.redo())}
        onAdd={onAddRequest}
        rangeLabel={rangeNow ? `${timecode(rangeNow.from)} to ${timecode(rangeNow.to)} (${fmtDuration(rangeNow.to - rangeNow.from)})` : undefined}
        onClearRange={() => setRange({})}
        narrow={narrow}
        coarse={coarse}
      />
      <Status editor={editor} message={message} onDismiss={() => setMessage(undefined)} />
      <div className="flex">
        <div className="w-[52px] shrink-0 pr-1.5 text-[10.5px] text-subtle sm:w-[62px]" aria-hidden>
          <div style={{ height: rulerH + cutsH }} />
          <div className="flex items-center" style={{ height: videoH }}>
            Picture
          </div>
          <div className="mt-1.5 flex items-center" style={{ height: waveH }}>
            Sound
          </div>
          <div className="mt-1.5 flex items-center" style={{ height: lineH }}>
            Narration
          </div>
          {(extraLanes ?? []).map((l) => (
            <div key={l.key} className="mt-1.5 flex items-center truncate" style={{ height: l.height }}>
              {l.label}
            </div>
          ))}
        </div>
        <div
          ref={setScroller}
          className="relative min-w-0 flex-1 overflow-x-auto overflow-y-hidden rounded-[12px] bg-inset pb-2"
          style={{ touchAction: drag ? 'none' : 'pan-x' }}
          onPointerMove={(e) => cancelLongPress(e)}
          onPointerUp={(e) => cancelLongPress(e, true)}
          onPointerCancel={() => {
            const lp = longPress.current;
            if (lp) clearTimeout(lp.timer);
            longPress.current = null;
          }}
          onDragOver={onDragOver}
          onDragLeave={(e) => {
            if (drag?.kind === 'drop' && !e.currentTarget.contains(e.relatedTarget as Node)) setDrag(null);
          }}
          onDrop={(e) => void onDrop(e)}
          onTouchStart={onTouchStart}
          onTouchMove={onTouchMove}
          onTouchEnd={() => (pinch.current = null)}
        >
          <div className="relative" style={{ width: contentW, height: rulerH + lanesH + 8 }}>
            {/* ruler: click to seek, drag to mark a stretch */}
            <div
              role="presentation"
              data-ruler
              className="absolute inset-x-0 top-0 cursor-text border-b border-white/10"
              style={{ height: rulerH }}
              onPointerDown={(e) => {
                if (e.button !== 0) return;
                (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
                const s = snapTo(timelineCandidates(), Math.max(0, Math.min(base.total, tAt(e.clientX))), e.shiftKey);
                setDrag({ kind: 'range', anchor: s.t, cur: s.t, startX: e.clientX, moved: false });
              }}
            >
              {ticks.out.map((t) => (
                <div key={t} className="absolute bottom-0 h-full" style={{ left: x(t) }}>
                  <div className="absolute bottom-0 h-1.5 w-px bg-white/25" />
                  <span className="tc absolute bottom-2 left-1 whitespace-nowrap text-[10px] text-white/50">{timecode(t, { ms: ticks.step < 1 })}</span>
                </div>
              ))}
            </div>
            {/* the marked stretch */}
            {rangeNow ? (
              <div
                className="pointer-events-none absolute z-[5] bg-accent/20 ring-1 ring-inset ring-accent/70"
                style={{ left: x(rangeNow.from), width: Math.max(2, (rangeNow.to - rangeNow.from) * pps), top: 0, height: rulerH + lanesH }}
                aria-hidden
              >
                <div className="absolute left-0 top-0 h-[26px] w-full bg-accent/30" />
              </div>
            ) : null}
            <div ref={lanesRef} className="absolute inset-x-0" style={{ top: rulerH }}>
              {/* cuts row: the marker between two clips opens the transition picker */}
              <div className="relative" style={{ height: cutsH }}>
                {(() => {
                  // Markers closer than a finger (or a pointer) apart would cover each other:
                  // the hidden ones are reached by zooming in, or from the clip's panel.
                  const gap = coarse ? 36 : 22;
                  let last = -Infinity;
                  return layout.items.slice(1).filter((it) => {
                    const px = x(it.own0);
                    if (px - last < gap) return false;
                    last = px;
                    return true;
                  });
                })().map((it) => {
                  const tr = it.clip.transitionIn;
                  const named = tr && (tr.type !== 'cut' || tr.audio) ? (TRANSITIONS.get(tr.type)?.name ?? tr.type) : undefined;
                  const prev = layout.items[it.index - 1];
                  return (
                    <button
                      key={it.clip.id}
                      type="button"
                      data-cut={it.clip.id}
                      onClick={() => onOpenTransition(it.clip.id)}
                      aria-label={`Transition between ${prev ? clipName(prev.clip, assets) : 'the start'} and ${clipName(it.clip, assets)}: ${named ?? 'straight cut'}. Choose another`}
                      title={named ? `${named}${tr?.duration ? ` ${tr.duration} s` : ''}${tr?.audio ? ' · split sound' : ''}` : 'Straight cut: choose a transition'}
                      className={cx(
                        'absolute top-[3px] z-10 flex h-[18px] -translate-x-1/2 items-center gap-1 rounded-full px-1.5 text-[10px] font-medium transition-colors',
                        named ? 'bg-accent text-accent-ink' : 'bg-white/12 text-white/70 hover:bg-white/25 hover:text-white',
                        coarse && 'h-[22px] min-w-[32px] justify-center',
                      )}
                      style={{ left: x(it.own0) }}
                    >
                      <span aria-hidden className="block h-[7px] w-[7px] rotate-45 rounded-[1px] border border-current" />
                      {named && !narrow ? <span className="max-w-[90px] truncate">{named}</span> : null}
                    </button>
                  );
                })}
              </div>
              {/* picture lane */}
              <div className="relative" style={{ height: videoH }} role="list" aria-label="Clips in order">
                {layout.items.map((it) => {
                  const isDragged = dragging?.id === it.clip.id;
                  const w = Math.max(6, (it.own1 - it.own0) * pps - 2);
                  const sel = it.clip.id === selectedId;
                  const asset = assets.get(it.clip.assetId);
                  // A cut from another project, played here as one block (D84).
                  const nested = it.clip.assetId.startsWith('cut:') ? (editor.plan?.nested?.[it.clip.assetId] ?? null) : undefined;
                  return (
                    <div key={it.clip.id} role="listitem" className="absolute inset-y-0" style={{ left: x(it.own0) + 1, width: w }} data-nested={nested !== undefined || undefined}>
                      <div
                        role="button"
                        tabIndex={0}
                        data-clip={it.clip.id}
                        aria-pressed={sel}
                        aria-label={`Clip ${it.index + 1}: ${nested !== undefined ? `the cut ${nested?.title ? `“${nested.title}” ` : ''}from ${nested?.projectName ?? 'another project'}` : clipName(it.clip, assets)}, ${fmtDuration(it.own1 - it.own0)}${it.clip.locked ? ', locked' : ''}`}
                        onPointerDown={(e) => startMove(e, it)}
                        onFocus={() => setFocusEdge(undefined)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            onSelectClip(it.clip.id);
                            onSeek(it.own0);
                          }
                        }}
                        className={cx(
                          'group absolute inset-0 overflow-hidden rounded-[8px] bg-[#211e1a] outline-none transition-shadow',
                          isDragged ? 'opacity-25 outline-dashed outline-1 outline-white/40' : sel ? 'ring-2 ring-accent' : 'ring-1 ring-inset ring-white/10 hover:ring-white/30',
                          'focus-visible:ring-2 focus-visible:ring-accent',
                          it.clip.locked ? 'cursor-not-allowed' : 'cursor-grab active:cursor-grabbing',
                        )}
                        style={{ touchAction: 'pan-x' }}
                      >
                        {it.clip.assetId.startsWith('conv:') && shown ? (
                          <ConversationStrip clip={it.clip} spec={shown} assets={assets} height={videoH} pps={pps} offset={it.start - it.own0} active={sel && !isDragged} onPick={(sp) => void pickAngle(it, sp)} />
                        ) : nested !== undefined ? (
                          <NestedStrip nested={nested} width={w} />
                        ) : (
                          <Filmstrip clip={it.clip} asset={asset} width={w} height={videoH} pps={pps} />
                        )}
                        <div className="pointer-events-none absolute inset-0 bg-gradient-to-t from-black/80 via-black/15 to-transparent" />
                        {it.clip.locked ? <div className="pointer-events-none absolute inset-0 bg-[repeating-linear-gradient(135deg,rgb(255_255_255/0.07)_0_6px,transparent_6px_12px)]" /> : null}
                        <div className="pointer-events-none absolute inset-x-1.5 bottom-1 flex min-w-0 items-center gap-1 text-white">
                          {it.clip.locked ? <Lock size={11} weight="fill" className="shrink-0" /> : null}
                          {it.clip.muted ? <SpeakerSlash size={11} className="shrink-0" /> : null}
                          {w > 46 ? <span className="truncate text-[11px] font-medium">{it.clip.label || it.clip.id}</span> : null}
                          {w > 120 ? <span className="tc ml-auto shrink-0 text-[10px] text-white/70">{fmtDuration(it.own1 - it.own0)}</span> : null}
                        </div>
                      </div>
                      {!it.clip.locked && !isDragged && (!coarse || sel) ? (
                        <>
                          <TrimHandle
                            clipId={it.clip.id}
                            edge="in"
                            coarse={coarse}
                            active={sel}
                            label={`Start of ${clipName(it.clip, assets)}`}
                            value={it.clip.in}
                            onPointerDown={(e) => startTrim(e, it, 'in')}
                            onFocus={() => setFocusEdge({ id: it.clip.id, edge: 'in' })}
                            onBlur={() => setFocusEdge(undefined)}
                          />
                          <TrimHandle
                            clipId={it.clip.id}
                            edge="end"
                            coarse={coarse}
                            active={sel}
                            label={`End of ${clipName(it.clip, assets)}`}
                            value={it.clip.out}
                            onPointerDown={(e) => startTrim(e, it, 'end')}
                            onFocus={() => setFocusEdge({ id: it.clip.id, edge: 'end' })}
                            onBlur={() => setFocusEdge(undefined)}
                          />
                        </>
                      ) : null}
                    </div>
                  );
                })}
                {insertX !== undefined ? (
                  <div
                    className="pointer-events-none absolute -top-1 bottom-[-4px] z-20 w-[3px] -translate-x-1/2 rounded-full bg-accent shadow-[0_0_0_3px_rgb(224_164_88/0.25)]"
                    style={{ left: insertX }}
                    aria-hidden
                  />
                ) : null}
              </div>
              {/* sound lane */}
              <div className="relative mt-1.5" style={{ height: waveH }} aria-label="Sound" role="img">
                {layout.items.map((it) => {
                  const w = Math.max(4, (it.end - it.start) * pps - 2);
                  return (
                    <div
                      key={it.clip.id}
                      className={cx('absolute inset-y-0 overflow-hidden rounded-[5px] bg-white/[0.04]', dragging?.id === it.clip.id && 'opacity-25')}
                      style={{ left: x(it.start) + 1, width: w }}
                    >
                      <WaveLane clip={it.clip} asset={assets.get(it.clip.assetId)} width={w} height={waveH} muted={it.clip.muted} />
                    </div>
                  );
                })}
              </div>
              {/* narration lane: each line from where its speech starts to where it ends */}
              <div className="relative mt-1.5" style={{ height: lineH }} role={lines.length ? 'list' : 'group'} aria-label="Narration on the timeline" data-narration-lane>
                {lines.length === 0 ? (
                  narrationEmpty ? <div className="absolute inset-y-0 left-2 flex items-center">{narrationEmpty}</div> : null
                ) : (
                  lines.map((l) => {
                    const start = lineStart(l);
                    const w = Math.max(18, (l.end - l.start) * pps - 2);
                    const moving = lineDrag?.id === l.id;
                    const bad = l.fit === 'over' || l.fit === 'unresolved';
                    return (
                      <div
                        key={l.id}
                        role="listitem"
                        tabIndex={0}
                        data-line={l.id}
                        aria-label={`Narration line ${l.n}: “${l.text}”, starts ${anchorWords(spec, l.anchor)} (${timecode(l.start)})${l.locked ? ', locked' : ''}. Drag it or use the arrow keys to move it`}
                        title={`${l.n}. ${l.text}`}
                        onPointerDown={(e) => startLine(e, l)}
                        onKeyDown={(e) => lineKeys(e, l)}
                        className={cx(
                          'absolute inset-y-[2px] flex items-center gap-1 overflow-hidden rounded-[7px] px-1.5 text-[11px] font-medium outline-none transition-shadow',
                          'focus-visible:ring-2 focus-visible:ring-white',
                          l.locked ? 'cursor-not-allowed' : 'cursor-grab active:cursor-grabbing',
                          bad
                            ? 'bg-[color-mix(in_oklab,var(--danger)_42%,#1a1513)] text-white'
                            : l.hasAudio
                              ? 'bg-accent text-accent-ink'
                              : 'bg-[color-mix(in_oklab,var(--accent)_30%,#1a1513)] text-[#f2ece4] ring-1 ring-inset ring-accent/60',
                          moving && 'z-10 shadow-[0_8px_24px_-6px_rgb(0_0_0/0.6)] ring-2 ring-white',
                        )}
                        style={{ left: x(start) + 1, width: w, touchAction: 'none' }}
                      >
                        {l.locked ? <Lock size={10} weight="fill" className="shrink-0" /> : null}
                        <span className="tc shrink-0 opacity-80">{l.n}</span>
                        {w > 40 ? <span className="truncate">{l.text}</span> : null}
                      </div>
                    );
                  })
                )}
                {lineDrag && spec && editor.plan ? (
                  <div
                    className="pointer-events-none absolute bottom-full z-30 mb-1 -translate-x-1/2 whitespace-nowrap rounded-[7px] bg-black/85 px-2 py-0.5 text-[11px] text-white"
                    style={{ left: Math.max(90, x(lineDrag.t)) }}
                    role="status"
                  >
                    {(() => {
                      const l = lines.find((x2) => x2.id === lineDrag.id);
                      return l ? `Starts ${anchorWords(spec, reanchorAt(spec, editor.plan!, l.index, lineDrag.t, lineDrag.exact))}` : '';
                    })()}
                  </div>
                ) : null}
              </div>
              {(extraLanes ?? []).map((l) => (
                <div key={l.key} className="relative mt-1.5" style={{ height: l.height }} aria-label={l.label}>
                  {l.render({ x, pps, total })}
                </div>
              ))}
            </div>
            {/* snap guide */}
            {snapLine ? (
              <div className="pointer-events-none absolute z-30 w-px bg-ok" style={{ left: x(snapLine.t), top: 0, height: rulerH + lanesH }} aria-hidden>
                <span className="absolute left-1 top-0.5 whitespace-nowrap rounded-[4px] bg-ok px-1 text-[10px] font-medium text-black">{snapLine.label}</span>
              </div>
            ) : null}
            {/* trim readout */}
            {drag?.kind === 'trim' && drag.moved ? <TrimReadout drag={drag} items={base.items} layout={layout.items} x={x} /> : null}
            {/* playhead (drag its head to scrub) */}
            <div className="pointer-events-none absolute z-40 w-[2px] -translate-x-1/2 bg-white" style={{ left: x(Math.min(time, total)), top: 0, height: rulerH + lanesH }} aria-hidden>
              <div
                className="pointer-events-auto absolute -left-[6px] top-0 h-3.5 w-3.5 cursor-ew-resize rounded-b-[4px] rounded-t-[2px] bg-white"
                onPointerDown={(e) => {
                  e.stopPropagation();
                  (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
                  setDrag({ kind: 'scrub' });
                }}
              />
            </div>
            {/* the clip being moved, under the finger or pointer */}
            {dragging && draggedItem ? (
              <div
                className={cx('pointer-events-none fixed z-[60] overflow-hidden rounded-[8px] shadow-[var(--shadow-float)] ring-2', dragging.remove ? 'opacity-70 ring-danger' : 'ring-accent')}
                style={{ left: dragging.x - dragging.grabDX, top: dragging.y - videoH / 2, width: Math.max(40, (draggedItem.own1 - draggedItem.own0) * pps), height: videoH }}
                aria-hidden
              >
                <Filmstrip clip={draggedItem.clip} asset={assets.get(draggedItem.clip.assetId)} width={Math.max(40, (draggedItem.own1 - draggedItem.own0) * pps)} height={videoH} pps={pps} />
                <div className={cx('absolute inset-0 grid place-items-center text-[11px] font-semibold text-white', dragging.remove ? 'bg-danger/70' : 'bg-black/30')}>
                  {dragging.remove ? 'Release to remove' : draggedItem.clip.label || draggedItem.clip.id}
                </div>
              </div>
            ) : null}
          </div>
        </div>
      </div>
      <p className="mt-2 px-1 text-[11.5px] leading-relaxed text-subtle">
        {coarse
          ? 'Tap a clip to select it, hold it to drag it. Drag its ends to trim. Pinch to zoom.'
          : `Drag clips to reorder, off the timeline to remove. Drag an end to trim (hold ${alt} on a shared end to move the cut). Drag a narration line to move it (hold ${alt} for an exact time). Drag on the ruler to mark a stretch. Hold ⇧ to stop snapping. Press ? for keys.`}
        {selected && !coarse ? ` ${mod}← / ${mod}→ moves the selected clip.` : ''}
      </p>
    </section>
  );
}

/** Ops that move clip id to place index (0 = first) among the others. */
function moveOps(spec: Spec, id: string, index: number): EditOp[] {
  const others = spec.clips.filter((c) => c.id !== id);
  const cur = spec.clips.findIndex((c) => c.id === id);
  if (cur < 0 || index === cur) return [];
  if (index <= 0) return others.length ? [{ op: 'moveClip', id, before: others[0]!.id }] : [];
  return [{ op: 'moveClip', id, after: others[Math.min(index, others.length) - 1]!.id }];
}

function TrimHandle({
  clipId,
  edge,
  coarse,
  active,
  label,
  value,
  onPointerDown,
  onFocus,
  onBlur,
}: {
  clipId: string;
  edge: 'in' | 'end';
  coarse: boolean;
  active: boolean;
  label: string;
  value: number;
  onPointerDown: (e: React.PointerEvent) => void;
  onFocus: () => void;
  onBlur: () => void;
}) {
  const w = coarse ? 44 : 12;
  return (
    <div
      role="slider"
      tabIndex={0}
      data-edge={edge}
      data-clip-edge={clipId}
      aria-label={label}
      aria-valuemin={0}
      aria-valuenow={Math.round(value * 1000) / 1000}
      aria-valuetext={`${timecode(value)} in the footage`}
      aria-orientation="horizontal"
      onPointerDown={onPointerDown}
      onFocus={onFocus}
      onBlur={onBlur}
      className={cx('group/h absolute inset-y-0 z-10 flex cursor-ew-resize items-center outline-none', edge === 'in' ? 'justify-start' : 'justify-end')}
      style={
        coarse
          ? { left: edge === 'in' ? -w / 2 : undefined, right: edge === 'end' ? -w / 2 : undefined, width: w, justifyContent: 'center', touchAction: 'none' }
          : { [edge === 'in' ? 'left' : 'right']: 0, width: w, touchAction: 'none' }
      }
    >
      <div
        className={cx(
          'h-[70%] w-[5px] rounded-full transition-[opacity,background-color]',
          active ? 'bg-accent opacity-100' : 'bg-white/80 opacity-0 group-hover/h:opacity-100',
          'group-focus-visible/h:bg-accent group-focus-visible/h:opacity-100 group-focus-visible/h:ring-2 group-focus-visible/h:ring-accent/40',
        )}
      />
    </div>
  );
}

function TrimReadout({ drag, items, layout, x }: { drag: Extract<Drag, { kind: 'trim' }>; items: Item[]; layout: Item[]; x: (t: number) => number }) {
  const it = items.find((i) => i.clip.id === drag.id);
  const now = layout.find((i) => i.clip.id === drag.id);
  if (!it || !now) return null;
  const was = drag.edge === 'in' ? it.clip.in : it.clip.out;
  const delta = drag.to - was;
  const at = drag.edge === 'in' ? now.own0 : now.own1;
  return (
    <div
      className="pointer-events-none absolute z-50 -translate-x-1/2 whitespace-nowrap rounded-[6px] bg-black/85 px-2 py-0.5 text-[11px] text-white"
      style={{ left: x(at), top: 2 }}
      aria-live="polite"
    >
      <span className="tc">
        {delta >= 0 ? '+' : '−'}
        {Math.abs(delta).toFixed(2)} s
      </span>
      <span className="ml-1.5 text-white/70">{drag.roll ? 'moving the cut' : drag.edge === 'in' ? 'start' : 'end'}</span>
    </div>
  );
}

function Toolbar({
  editor,
  snapping,
  onSnapping,
  pps,
  fitPps,
  onZoom,
  onSplit,
  onDelete,
  canDelete,
  onShortcuts,
  onUndo,
  onRedo,
  onAdd,
  rangeLabel,
  onClearRange,
  narrow,
  coarse,
}: {
  narrow: boolean;
  coarse: boolean;
  editor: Editor;
  snapping: boolean;
  onSnapping: (v: boolean) => void;
  pps: number;
  fitPps: number;
  onZoom: (pps: number | undefined) => void;
  onSplit: () => void;
  onDelete: () => void;
  canDelete: boolean;
  onShortcuts: () => void;
  onUndo: () => void;
  onRedo: () => void;
  onAdd?: () => void;
  rangeLabel?: string;
  onClearRange: () => void;
}) {
  const lo = Math.log(fitPps * 0.5);
  const hi = Math.log(600);
  const v = Math.round(((Math.log(pps) - lo) / (hi - lo)) * 100);
  return (
    <div className="mb-2 flex flex-wrap items-center gap-1.5" role="toolbar" aria-label="Timeline tools" data-no-timeline-keys>
      <IconButton label={editor.undoLabel ? `Undo: ${editor.undoLabel} (${mod}Z)` : `Undo (${mod}Z)`} onClick={onUndo} disabled={!editor.canUndo}>
        <ArrowCounterClockwise size={17} />
      </IconButton>
      <IconButton label={editor.redoLabel ? `Redo: ${editor.redoLabel} (⇧${mod}Z)` : `Redo (⇧${mod}Z)`} onClick={onRedo} disabled={!editor.canRedo}>
        <ArrowClockwise size={17} />
      </IconButton>
      <span className="mx-0.5 h-5 w-px bg-line" aria-hidden />
      {narrow ? (
        <>
          <IconButton label="Split the clip under the playhead" onClick={onSplit} className="hairline">
            <Scissors size={16} />
          </IconButton>
          <IconButton label={rangeLabel ? 'Cut out the marked stretch' : 'Remove the selected clip'} onClick={onDelete} disabled={!canDelete} className="hairline">
            <Trash size={16} />
          </IconButton>
          {onAdd ? (
            <IconButton label="Add footage" onClick={onAdd} className="hairline">
              <Plus size={16} />
            </IconButton>
          ) : null}
        </>
      ) : (
        <>
          <Button size="sm" variant="secondary" icon={<Scissors size={14} />} onClick={onSplit} title="Split the clip under the playhead (S)">
            Split
          </Button>
          <Button size="sm" variant="secondary" icon={<Trash size={14} />} onClick={onDelete} disabled={!canDelete} title="Remove the selected clip or the marked stretch (Delete)">
            {rangeLabel ? 'Cut out' : 'Remove'}
          </Button>
          {onAdd ? (
            <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={onAdd} title="Add footage from the sources">
              Add footage
            </Button>
          ) : null}
        </>
      )}
      {rangeLabel ? (
        <span className="inline-flex h-7 items-center gap-1.5 rounded-full bg-accent-soft pl-2.5 pr-1 text-[12px] text-accent-text">
          <span className="tc">{rangeLabel}</span>
          <button type="button" onClick={onClearRange} className="rounded-full px-1.5 hover:bg-accent/20" aria-label="Clear the marked stretch">
            ×
          </button>
        </span>
      ) : null}
      <div className="flex-1" />
      <IconButton label={snapping ? 'Snapping on (hold ⇧ while dragging to turn it off)' : 'Snapping off'} active={snapping} onClick={() => onSnapping(!snapping)}>
        <MagnetStraight size={17} weight={snapping ? 'fill' : 'regular'} />
      </IconButton>
      <div className="flex items-center gap-1">
        <IconButton label="Zoom out (-)" size="sm" onClick={() => onZoom(Math.max(fitPps * 0.5, pps / 1.5))}>
          <Minus size={14} />
        </IconButton>
        <input
          type="range"
          min={0}
          max={100}
          value={Math.max(0, Math.min(100, v))}
          onChange={(e) => onZoom(Math.exp(lo + (Number(e.target.value) / 100) * (hi - lo)))}
          aria-label="Zoom"
          className="h-1 w-16 cursor-pointer accent-[var(--accent)] sm:w-28"
        />
        <IconButton label="Zoom in (=)" size="sm" onClick={() => onZoom(Math.min(600, pps * 1.5))}>
          <Plus size={14} />
        </IconButton>
        <IconButton label="Fit the whole cut (\)" size="sm" onClick={() => onZoom(undefined)}>
          <ArrowsInLineHorizontal size={15} />
        </IconButton>
      </div>
      {coarse ? null : (
        <IconButton label="Keyboard shortcuts (?)" onClick={onShortcuts}>
          <Keyboard size={17} />
        </IconButton>
      )}
      <SaveState editor={editor} short={narrow} />
    </div>
  );
}

function SaveState({ editor, short }: { editor: Editor; short?: boolean }) {
  const s = editor.saveState;
  const body =
    s === 'saved' ? (
      <>
        <CheckCircle size={13} weight="fill" className="text-ok" /> {editor.base && !short ? `Saved as version ${editor.base.number}` : 'Saved'}
      </>
    ) : s === 'saving' ? (
      <>
        <Spinner size={11} /> Saving…
      </>
    ) : s === 'unsaved' ? (
      <>
        <span className="h-2 w-2 rounded-full bg-accent" aria-hidden /> {short ? 'Saving soon' : 'Saving in a moment'}
      </>
    ) : s === 'conflict' ? (
      <>
        <WarningCircle size={13} weight="fill" className="text-danger" /> Someone else changed it
      </>
    ) : (
      <>
        <WarningCircle size={13} weight="fill" className="text-danger" /> Not saved
      </>
    );
  return (
    <span className="inline-flex h-7 items-center gap-1.5 rounded-full px-2.5 text-[12px] text-muted hairline" role="status" aria-live="polite" data-save-state={s}>
      {body}
    </span>
  );
}

function Status({ editor, message, onDismiss }: { editor: Editor; message?: string; onDismiss: () => void }) {
  const [busy, setBusy] = useState<'mine' | 'theirs'>();
  if (editor.conflict) {
    return (
      <div role="alert" className="mb-2 flex flex-wrap items-center gap-2 rounded-[12px] bg-danger-soft px-3 py-2 text-[12.5px] text-fg">
        <WarningCircle size={15} weight="fill" className="shrink-0 text-danger" />
        <span className="min-w-0 flex-1">{editor.conflict.message} Your changes since then aren't saved yet.</span>
        <Button size="sm" variant="primary" loading={busy === 'mine'} onClick={() => (setBusy('mine'), void editor.keepMine().finally(() => setBusy(undefined)))}>
          Keep mine
        </Button>
        <Button size="sm" variant="secondary" loading={busy === 'theirs'} onClick={() => (setBusy('theirs'), void editor.keepTheirs().finally(() => setBusy(undefined)))}>
          Keep theirs
        </Button>
      </div>
    );
  }
  const problem = editor.problems[0];
  if (problem) {
    return (
      <div role="alert" className="mb-2 flex flex-wrap items-center gap-2 rounded-[12px] bg-danger-soft px-3 py-2 text-[12.5px] text-fg">
        <WarningCircle size={15} weight="fill" className="shrink-0 text-danger" />
        <span className="min-w-0 flex-1">
          {problem.message} {problem.hint ? <span className="text-muted">{problem.hint}</span> : null} This change isn't saved until it's fixed.
        </span>
        <Button size="sm" variant="secondary" disabled={!editor.canUndo} onClick={() => editor.undo()}>
          Undo it
        </Button>
      </div>
    );
  }
  if (editor.saveError) {
    return (
      <div role="alert" className="mb-2 flex flex-wrap items-center gap-2 rounded-[12px] bg-danger-soft px-3 py-2 text-[12.5px] text-fg">
        <WarningCircle size={15} weight="fill" className="shrink-0 text-danger" />
        <span className="min-w-0 flex-1">Not saved: {editor.saveError}</span>
        <Button size="sm" variant="secondary" onClick={() => (editor.clearError(), void editor.flush())}>
          Try again
        </Button>
      </div>
    );
  }
  if (message) {
    return (
      <div role="status" className="mb-2 flex items-center gap-2 rounded-[12px] bg-panel px-3 py-2 text-[12.5px] text-muted hairline">
        <span className="min-w-0 flex-1">{message}</span>
        <button type="button" className="rounded-[6px] px-1.5 text-subtle hover:bg-hover hover:text-fg" onClick={onDismiss} aria-label="Dismiss">
          ×
        </button>
      </div>
    );
  }
  const warn = editor.warnings.find((w) => w.severity !== 'error' && w.code !== 'heavy_crop' && w.code !== 'vfr_source');
  return warn ? (
    <div role="status" className="mb-2 flex items-center gap-2 rounded-[12px] bg-accent-soft px-3 py-2 text-[12.5px] text-accent-text">
      <WarningCircle size={14} className="shrink-0" />
      <span className="min-w-0 flex-1">{warn.message}</span>
    </div>
  ) : null;
}
