/*
 * The studio timeline's working copy: the clip ops (and moveBeat, which places
 * a narration line) applied in the browser for instant feedback while a
 * gesture is saved in the background. The server is
 * authoritative: every change is previewed by `edit.preview` (Go applies the
 * same ops, validates them and resolves the frame-accurate RenderPlan) and
 * saved by `edit.apply`, so anything here only decides what to show for the
 * few milliseconds before that answer arrives, and which ops to send.
 *
 * Semantics follow internal/editspec (ops.go, studio.go); keep them in step.
 */
import type { Clip, EditOp, Spec } from '../api/types';
import transitionCatalog from '@metarush/schema/schemas/transitions.json';

const OVERLAP = new Set(transitionCatalog.transitions.filter((t) => t.overlap).map((t) => t.type));

export class OpError extends Error {}

export const round3 = (v: number) => Math.round(v * 1000) / 1000;
export const speedOf = (c: Clip) => (c.speed && c.speed > 0 ? c.speed : 1);
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const fpsOf = (s: Spec) => s.output.fps || 30;

/** Every item ID in a spec (clips, sounds, graphics, narration lines). */
export function specIDs(s: Spec): Set<string> {
  const used = new Set<string>(['captions', 'voiceover', 'mix']);
  for (const c of s.clips) used.add(c.id);
  for (const a of s.audio ?? []) used.add(a.id);
  for (const o of s.overlays ?? []) used.add(o.id);
  for (const b of s.voiceover?.beats ?? []) used.add(b.id);
  return used;
}

/** The name the second part of a clip gets when it is cut in two (editspec.PartID). */
export function partID(s: Spec, id: string, extra?: Set<string>): string {
  let base = id.replace(/-\d+$/, '');
  if (!base) base = 'clip';
  const used = specIDs(s);
  for (const x of extra ?? []) used.add(x);
  for (let n = 2; ; n++) {
    const suf = `-${n}`;
    const b = base.length + suf.length > 64 ? base.slice(0, 64 - suf.length) : base;
    if (!used.has(b + suf)) return b + suf;
  }
}

/** A free clip name made from a footage name. */
export function newClipID(s: Spec, from: string): string {
  let id = from
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^[-_]+|[-_]+$/g, '');
  if (!id || !/^[a-z]/.test(id)) id = `clip-${id}`.replace(/-$/, '');
  if (id.length > 60) id = id.slice(0, 60).replace(/[-_]+$/, '');
  return specIDs(s).has(id) ? partID(s, id) : id;
}

export interface Placed {
  id: string;
  index: number;
  /** Timeline frames of the picture (overlapping transitions included). */
  from: number;
  dur: number;
  /** The part of the timeline the clip owns: overlaps split at their middle. */
  ownFrom: number;
  ownTo: number;
  /** Frames of the transition into it (after clamping). */
  inFrames: number;
  inType: string;
}

/** Where every clip's picture plays, in frames (editspec.Timeline). */
export function place(s: Spec): { placed: Placed[]; total: number; fps: number } {
  const fps = fpsOf(s);
  const placed: Placed[] = [];
  let pos = 0;
  s.clips.forEach((c, i) => {
    let dur = Math.round(((c.out - c.in) / speedOf(c)) * fps);
    if (dur < 1) dur = 1;
    let type = c.transitionIn?.type || 'cut';
    let tf = c.transitionIn?.type ? Math.round((c.transitionIn.duration ?? 0) * fps) : 0;
    if (i === 0 && OVERLAP.has(type)) type = 'fade';
    if (OVERLAP.has(type) && i > 0) {
      const max = Math.floor(Math.min(placed[i - 1]!.dur, dur) / 2);
      if (tf > max) tf = max;
      pos -= tf;
    }
    placed.push({ id: c.id, index: i, from: pos, dur, ownFrom: pos, ownTo: pos + dur, inFrames: tf, inType: type });
    pos += dur;
  });
  placed.forEach((p, i) => {
    if (i > 0 && OVERLAP.has(p.inType)) p.ownFrom = p.from + Math.floor(p.inFrames / 2);
    const nx = placed[i + 1];
    if (nx) p.ownTo = nx.from + (OVERLAP.has(nx.inType) ? Math.floor(nx.inFrames / 2) : 0);
    if (p.ownTo < p.ownFrom) p.ownTo = p.ownFrom;
  });
  return { placed, total: pos, fps };
}

function frames(c: Clip, a: number, b: number, fps: number) {
  return Math.round(((b - a) / speedOf(c)) * fps);
}

type Shot = NonNullable<Clip['shots']>[number];

/** The shot showing at conversation time t: the last one starting at or before it, else the first (editspec.ShotAt). */
export function shotAt(shots: Shot[], t: number): number {
  if (shots.length === 0) return -1;
  let i = 0;
  while (i < shots.length && !(shots[i]!.at > t + 1e-9)) i++;
  return i === 0 ? 0 : i - 1;
}

/** A conversation clip's shots cut down to [a, b): the one showing at a (starting there) and those inside (editspec.ShotsFor). */
export function shotsFor(shots: Shot[], a: number, b: number): Shot[] {
  const out: Shot[] = [];
  const i = shotAt(shots, a);
  if (i >= 0) out.push({ ...clone(shots[i]!), at: round3(a) });
  for (const sh of shots) if (sh.at > a + 1e-9 && sh.at < b - 1e-9) out.push(clone(sh));
  return out;
}

function splitParts(c: Clip, at: number, newId: string): [Clip, Clip] {
  const left = clone(c);
  const right = clone(c);
  left.out = at;
  delete left.fadeOut;
  right.id = newId;
  right.in = at;
  delete right.transitionIn;
  delete right.notes;
  if (c.shots?.length) {
    // A conversation clip keeps its picture: each part starts with the shot it starts in (D99).
    left.shots = shotsFor(c.shots, c.in, at);
    right.shots = shotsFor(c.shots, at, c.out);
  }
  return [left, right];
}

/** Removes [from, to) of a clip's footage: nothing, one or two parts are left. */
function cutOut(c: Clip, from: number, to: number, fps: number, newId: string): { parts: Clip[]; head: boolean } {
  from = round3(Math.max(from, c.in));
  to = round3(Math.min(to, c.out));
  if (to <= from) throw new OpError(`nothing of ${c.id} is in that range`);
  const keepHead = frames(c, c.in, from, fps) >= 2;
  const keepTail = frames(c, to, c.out, fps) >= 2;
  if (!keepHead && !keepTail) return { parts: [], head: true };
  if (!keepHead) return { parts: [{ ...clone(c), in: to }], head: true };
  if (!keepTail) return { parts: [{ ...clone(c), out: from }], head: false };
  const [left, right] = splitParts(c, from, newId);
  right.in = to;
  if (c.shots?.length) right.shots = shotsFor(c.shots, to, c.out);
  return { parts: [left, right], head: false };
}

function mergePatch(target: unknown, patch: unknown): unknown {
  if (patch === null || typeof patch !== 'object' || Array.isArray(patch)) return patch;
  const t: Record<string, unknown> = target && typeof target === 'object' && !Array.isArray(target) ? { ...(target as Record<string, unknown>) } : {};
  for (const [k, v] of Object.entries(patch as Record<string, unknown>)) {
    if (v === null) delete t[k];
    else t[k] = mergePatch(t[k], v);
  }
  return t;
}

function indexOf(s: Spec, id: string | undefined) {
  return id ? s.clips.findIndex((c) => c.id === id) : -1;
}

function insertAt(s: Spec, op: EditOp): number {
  if (op.after) {
    const i = indexOf(s, op.after);
    if (i < 0) throw new OpError(`no clip ${op.after}`);
    return i + 1;
  }
  if (op.before) {
    const i = indexOf(s, op.before);
    if (i < 0) throw new OpError(`no clip ${op.before}`);
    return i;
  }
  return s.clips.length;
}

function lockedCheck(c: Clip | undefined) {
  if (c?.locked) throw new OpError(`${c.id} is locked`);
}

/** A narration line can't change when it, or the whole narration, is locked. */
function beatLockedCheck(s: Spec, id: string) {
  const b = s.voiceover?.beats.find((x) => x.id === id);
  if (b?.locked || (b && s.voiceover?.locked)) throw new OpError(`line ${id} is locked`);
}

/** Applies one op to s (in place). Ops that do not change clips are ignored here. */
function applyOne(s: Spec, op: EditOp) {
  const fps = fpsOf(s);
  const i = indexOf(s, op.id);
  const need = () => {
    if (i < 0) throw new OpError(`no clip ${op.id}`);
    return s.clips[i]!;
  };
  switch (op.op) {
    case 'insertClip': {
      if (!op.clip) throw new OpError('clip is required');
      s.clips.splice(insertAt(s, op), 0, clone(op.clip));
      return;
    }
    case 'removeClip':
      lockedCheck(need());
      s.clips.splice(i, 1);
      return;
    case 'replaceClip':
      lockedCheck(need());
      s.clips[i] = clone(op.clip!);
      return;
    case 'updateClip': {
      const c = need();
      lockedCheck(c);
      s.clips[i] = mergePatch(c, op.set) as Clip;
      return;
    }
    case 'moveClip': {
      const c = need();
      lockedCheck(c);
      s.clips.splice(i, 1);
      s.clips.splice(insertAt(s, op), 0, c);
      return;
    }
    case 'setTransition': {
      const c = need();
      lockedCheck(c);
      if (op.edge === 'end') {
        const t = op.transition;
        if (t && t.type !== 'cut' && t.duration) c.fadeOut = round3(t.duration);
        else delete c.fadeOut;
        return;
      }
      const t = op.transition;
      if (!t || (t.type === 'cut' && !t.audio && !t.duration && !t.direction && !t.easing)) delete c.transitionIn;
      else c.transitionIn = { ...clone(t), type: t.type || 'cut', duration: t.duration ? round3(t.duration) : undefined };
      if (c.transitionIn && c.transitionIn.duration === undefined) delete c.transitionIn.duration;
      return;
    }
    case 'splitClip': {
      const c = need();
      lockedCheck(c);
      const at = round3(op.at ?? NaN);
      if (!(frames(c, c.in, at, fps) >= 1 && frames(c, at, c.out, fps) >= 1)) throw new OpError(`not inside ${c.id}`);
      const id = op.newId || partID(s, c.id);
      const [l, r] = splitParts(c, at, id);
      s.clips.splice(i, 1, l, r);
      return;
    }
    case 'trimClip': {
      const c = need();
      lockedCheck(c);
      const to = round3(op.to ?? NaN);
      if (!Number.isFinite(to) || to < 0) throw new OpError('bad time');
      if (op.mode === 'roll') {
        const j = op.edge === 'end' ? i + 1 : i - 1;
        const nb = s.clips[j];
        if (!nb) throw new OpError('nothing to roll with');
        lockedCheck(nb);
        const old = op.edge === 'end' ? c.out : c.in;
        const delta = (to - old) / speedOf(c);
        if (op.edge === 'in') nb.out = round3(nb.out + delta * speedOf(nb));
        else nb.in = round3(nb.in + delta * speedOf(nb));
        if (nb.out <= nb.in || nb.in < 0) throw new OpError('the cut moved too far');
      }
      if (op.edge === 'in') c.in = to;
      else c.out = to;
      if (c.out <= c.in) throw new OpError('ends before it starts');
      return;
    }
    case 'slipClip': {
      const c = need();
      lockedCheck(c);
      const by = op.by ?? 0;
      if (c.in + by < 0) throw new OpError('before the footage');
      c.in = round3(c.in + by);
      c.out = round3(c.out + by);
      return;
    }
    case 'removeWords': {
      const c = need();
      lockedCheck(c);
      const { parts } = cutOut(c, op.from ?? 0, op.to ?? 0, fps, op.newId || partID(s, c.id));
      if (!parts.length && s.clips.length === 1) throw new OpError('the whole cut');
      s.clips.splice(i, 1, ...parts);
      return;
    }
    case 'deleteRange': {
      const { placed, total } = place(s);
      const F = Math.round((op.from ?? 0) * fps);
      const T = Math.min(total, Math.round((op.to ?? 0) * fps));
      if (F < 0 || T <= F || F >= total) throw new OpError('nothing there');
      const out: Clip[] = [];
      let head = -1;
      let affected = 0;
      s.clips.forEach((c, k) => {
        const p = placed[k]!;
        if (T <= p.ownFrom || F >= p.ownTo || p.ownTo <= p.ownFrom) {
          out.push(c);
          return;
        }
        lockedCheck(c);
        affected++;
        const srcAt = (f: number) => round3(c.in + ((f - p.from) / fps) * speedOf(c));
        const from = F > p.ownFrom ? srcAt(F) : c.in;
        const to = T < p.ownTo ? srcAt(T) : c.out;
        const id = F > p.ownFrom && T < p.ownTo ? op.newId || partID(s, c.id) : '';
        const r = cutOut(c, from, to, fps, id);
        if (r.head && r.parts.length === 1 && F < p.ownFrom) head = out.length;
        out.push(...r.parts);
      });
      if (!affected) throw new OpError('nothing there');
      if (!out.length) throw new OpError('the whole cut');
      s.clips = out;
      if (head >= 0) delete s.clips[head]!.transitionIn;
      return;
    }
    case 'moveBeat': {
      // A narration line's new start (anchor) and/or place in the script (editspec ops.go).
      const beats = s.voiceover?.beats;
      const k = beats && op.id ? beats.findIndex((b) => b.id === op.id) : -1;
      if (!beats || k < 0) throw new OpError(`no line ${op.id}`);
      beatLockedCheck(s, op.id!);
      if (!op.anchor && !op.after && !op.before) throw new OpError('needs a new anchor');
      if (op.anchor) beats[k] = { ...beats[k]!, anchor: clone(op.anchor) as NonNullable<Spec['voiceover']>['beats'][number]['anchor'] };
      if (op.after || op.before) {
        const [b] = beats.splice(k, 1);
        const ref = (op.after || op.before)!;
        const j = beats.findIndex((x) => x.id === ref);
        if (j < 0) throw new OpError(`no line ${ref}`);
        beats.splice(op.after ? j + 1 : j, 0, b!);
      }
      return;
    }
    default:
      return;
  }
}

/** Applies ops to a copy of spec; throws OpError when one is refused. */
export function applyOps(spec: Spec, ops: EditOp[]): Spec {
  const s = clone(spec);
  for (const op of ops) applyOne(s, op);
  return s;
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/**
 * Ops that turn the clips of a into the clips of b (undo and redo across
 * saves): removals, whole-clip replacements, then inserts and moves in b's
 * order. Only the clips that differ are touched, so a change someone else made
 * to other clips is kept.
 */
export function clipDiffOps(a: Spec, b: Spec): EditOp[] {
  const ops: EditOp[] = [];
  const inB = new Map(b.clips.map((c) => [c.id, c]));
  const inA = new Map(a.clips.map((c) => [c.id, c]));
  for (const c of a.clips) if (!inB.has(c.id)) ops.push({ op: 'removeClip', id: c.id });
  for (const c of b.clips) {
    const old = inA.get(c.id);
    if (old && !same(old, c)) ops.push({ op: 'replaceClip', id: c.id, clip: clone(c) });
  }
  let cur = a.clips.map((c) => c.id).filter((id) => inB.has(id));
  const keep = lcs(
    cur,
    b.clips.map((c) => c.id).filter((id) => inA.has(id)),
  );
  b.clips.forEach((c, j) => {
    const existed = inA.has(c.id);
    if (existed && keep.has(c.id)) return;
    const pos = cur.indexOf(c.id);
    const prev = j > 0 ? b.clips[j - 1]!.id : undefined;
    if (existed && (j === 0 ? pos === 0 : pos >= 0 && pos === cur.indexOf(prev!) + 1)) return;
    const op: EditOp = existed ? { op: 'moveClip', id: c.id } : { op: 'insertClip', clip: clone(c) };
    if (existed) cur = cur.filter((x) => x !== c.id);
    let at = cur.length;
    if (j > 0) {
      op.after = prev;
      at = cur.indexOf(prev!) + 1;
    } else if (cur.length) {
      op.before = cur[0];
      at = 0;
    }
    cur.splice(at, 0, c.id);
    ops.push(op);
  });
  return ops;
}

const anchors = (s: Spec) => (s.voiceover?.beats ?? []).map((b) => [b.id, b.anchor] as const);

/** Whether two cuts have the same clips and narration lines in the same places. */
export function sameCut(a: Spec, b: Spec): boolean {
  return same(a.clips, b.clips) && same(anchors(a), anchors(b));
}

/**
 * Ops that put a's narration lines where b has them (moveBeat with b's anchor)
 * for the lines both have. The timeline only ever moves lines; their words,
 * order and sound change elsewhere (the Voiceover tab), as their own versions.
 */
export function beatDiffOps(a: Spec, b: Spec): EditOp[] {
  const inA = new Map((a.voiceover?.beats ?? []).map((x) => [x.id, x]));
  const ops: EditOp[] = [];
  for (const x of b.voiceover?.beats ?? []) {
    const old = inA.get(x.id);
    if (old && !same(old.anchor, x.anchor)) ops.push({ op: 'moveBeat', id: x.id, anchor: clone(x.anchor) });
  }
  return ops;
}

/** Ops that turn the cut a into b: its clips, then where its narration lines start. */
export function cutDiffOps(a: Spec, b: Spec): EditOp[] {
  return [...clipDiffOps(a, b), ...beatDiffOps(a, b)];
}

function lcs(a: string[], b: string[]): Set<string> {
  const n = a.length;
  const m = b.length;
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i]![j] = a[i] === b[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
  const keep = new Set<string>();
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      keep.add(a[i]!);
      i++;
      j++;
    } else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) i++;
    else j++;
  }
  return keep;
}

/**
 * Keep mine: my version of every clip I changed since base, theirs of
 * everything else (their new clips stay where they put them). Ops against head.
 */
export function keepMine(base: Spec, head: Spec, mine: Spec): EditOp[] {
  const baseClips = new Map(base.clips.map((c) => [c.id, c]));
  const mineClips = new Map(mine.clips.map((c) => [c.id, c]));
  const touched = new Set<string>();
  for (const c of mine.clips) if (!same(baseClips.get(c.id), c)) touched.add(c.id);
  for (const c of base.clips) if (!mineClips.has(c.id)) touched.add(c.id);
  const orderChanged = !same(
    base.clips.map((c) => c.id).filter((id) => mineClips.has(id)),
    mine.clips.map((c) => c.id).filter((id) => baseClips.has(id)),
  );
  const target = clone(head);
  // Their clips I did not touch stay; mine replace or join.
  const theirs = new Map(head.clips.map((c) => [c.id, c]));
  let clips: Clip[] = orderChanged
    ? mine.clips.filter((c) => touched.has(c.id) || theirs.has(c.id)).map((c) => (touched.has(c.id) ? c : theirs.get(c.id)!))
    : head.clips.filter((c) => !touched.has(c.id) || mineClips.has(c.id)).map((c) => (touched.has(c.id) ? mineClips.get(c.id)! : c));
  // Clips only I have (added or split by me) and, without a reorder, in my place.
  if (!orderChanged) {
    mine.clips.forEach((c, j) => {
      if (clips.some((x) => x.id === c.id) || !touched.has(c.id)) return;
      const prev = mine.clips[j - 1]?.id;
      const at = prev ? clips.findIndex((x) => x.id === prev) + 1 : 0;
      clips.splice(at, 0, c);
    });
  }
  // Their new clips (not in base), after the clip they follow.
  head.clips.forEach((c, j) => {
    if (baseClips.has(c.id) || clips.some((x) => x.id === c.id)) return;
    const prev = head.clips[j - 1]?.id;
    const at = prev ? clips.findIndex((x) => x.id === prev) + 1 : 0;
    clips.splice(at, 0, c);
  });
  clips = clips.filter((c, k) => clips.findIndex((x) => x.id === c.id) === k);
  target.clips = clips;
  // Narration lines I moved keep my place; the others stay where they put them.
  const baseAnchors = new Map(anchors(base));
  const mineAnchors = new Map(anchors(mine));
  for (const b of target.voiceover?.beats ?? []) {
    const m = mineAnchors.get(b.id);
    if (m && baseAnchors.has(b.id) && !same(baseAnchors.get(b.id), m)) b.anchor = clone(m);
  }
  return cutDiffOps(head, target);
}

/** The working copy's refusals, in plain words. */
export function plainRefusal(m: string): string {
  if (/^line .* is locked/.test(m)) return 'That line is locked, so it stays where it is.';
  if (/^no line/.test(m)) return 'That line is not in the narration any more.';
  if (/locked/.test(m)) return 'That clip is locked, so it stays as it is.';
  if (/whole cut/.test(m)) return 'A cut needs at least one clip.';
  if (/nothing/.test(m)) return 'There is nothing there to change.';
  if (/before it starts|ends before/.test(m)) return 'A clip has to end after it starts.';
  if (/roll/.test(m)) return 'There is no clip on that side to move the cut with.';
  if (/before the footage|bad time/.test(m)) return "The footage doesn't go back that far.";
  if (/moved too far/.test(m)) return 'The cut would move past the end of the clip next to it.';
  if (/not inside/.test(m)) return 'The playhead has to be inside a clip to split it.';
  return 'That change could not be made.';
}
