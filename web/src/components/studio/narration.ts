import type { EditOp, RenderPlan, Spec } from '../../api/types';

/*
 * The timeline's narration lane (D62): each line as a bar from where its
 * speech starts to where it ends, as Go placed it (the render plan). Dragging a
 * line moves it the way a person would say it: it is tied to what plays where
 * it lands (reanchorAt, the browser's twin of voiceover.ReanchorAt in Go), and
 * the gesture is one moveBeat op that goes through the timeline's working copy,
 * preview, background save, undo and redo like any clip gesture.
 */

export type LineAnchor = NonNullable<Spec['voiceover']>['beats'][number]['anchor'];

export interface LaneLine {
  id: string;
  /** Index in the script (0-based); the line's number is n. */
  index: number;
  n: number;
  text: string;
  /** Timeline seconds: where its speech starts and ends. */
  start: number;
  end: number;
  anchorFrame: number;
  fit: 'ok' | 'tight' | 'over' | 'unresolved';
  hasAudio: boolean;
  estimated: boolean;
  locked: boolean;
  anchor: LineAnchor;
}

/** The lines to draw: the plan's places for the lines the cut has now. */
export function laneLines(spec: Spec | undefined, plan: RenderPlan | undefined): LaneLine[] {
  const beats = spec?.voiceover?.beats ?? [];
  const placed = new Map((plan?.voiceover?.beats ?? []).map((b) => [b.id, b]));
  const fps = plan?.fps || spec?.output.fps || 30;
  const out: LaneLine[] = [];
  beats.forEach((b, i) => {
    const p = placed.get(b.id);
    if (!p || p.problem) return;
    const start = p.from / fps;
    const end = Math.max(start + 0.2, p.to / fps);
    out.push({
      id: b.id,
      index: i,
      n: i + 1,
      text: b.text,
      start,
      end,
      anchorFrame: p.anchorFrame,
      fit: p.fit,
      hasAudio: p.hasAudio,
      estimated: p.estimated,
      locked: !!b.locked || !!spec?.voiceover?.locked,
      anchor: b.anchor,
    });
  });
  return out;
}

const round1 = (v: number) => Math.round(v * 10) / 10;

/**
 * Where a line dropped at timeline second t starts, said conversationally
 * (internal/voiceover ReanchorAt): exact pins it to the clock; otherwise a line
 * that followed a moment still inside its clip keeps following it (new
 * offset), one dropped where the previous line ends stays "after" it, and any
 * other is tied to the latest-starting clip under that point (the incoming one
 * in a transition), so it moves with later edits.
 */
export function reanchorAt(spec: Spec, plan: RenderPlan, index: number, t: number, exact: boolean): LineAnchor {
  const fps = plan.fps || spec.output.fps || 30;
  t = Math.max(0, t);
  if (exact) return { kind: 'time', at: round1(t) };
  const f = Math.round(t * fps);
  const cur = spec.voiceover?.beats[index]?.anchor;
  const placed = plan.voiceover?.beats.find((b) => b.id === spec.voiceover?.beats[index]?.id);
  const inClip = (c: RenderPlan['clips'][number]) => f >= c.from && f < c.from + c.durationInFrames;
  if (cur && placed && !placed.problem) {
    if (cur.kind === 'moment' && cur.at !== undefined) {
      for (const c of plan.clips) {
        if (inClip(c) && c.assetId === cur.assetId && cur.at >= c.sourceIn && cur.at < c.sourceOut) {
          const offset = round1(t - (placed.anchorFrame / fps - (cur.offset ?? 0)));
          if (Math.abs(offset) <= 60) return { ...cur, offset };
        }
      }
    } else if (cur.kind === 'after' && index > 0) {
      if (Math.abs(f - placed.anchorFrame) <= 0.15 * fps) return cur;
    }
  }
  let hit: RenderPlan['clips'][number] | undefined;
  for (const c of plan.clips) if (inClip(c) && (!hit || c.from >= hit.from)) hit = c;
  const last = plan.clips[plan.clips.length - 1];
  if (!hit && last && f >= last.from) hit = last;
  if (!hit) return { kind: 'time', at: round1(t) };
  let offset = round1(t - hit.from / fps);
  if (Math.abs(offset) < 0.05) offset = 0;
  return offset ? { kind: 'clip', clipId: hit.id, offset } : { kind: 'clip', clipId: hit.id };
}

const sameAnchor = (a: LineAnchor | undefined, b: LineAnchor | undefined) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

/** The gesture's op, or none when the line would stay tied to the same thing. */
export function moveLineOps(line: LaneLine, anchor: LineAnchor): EditOp[] {
  if (sameAnchor(line.anchor, anchor)) return [];
  return [{ op: 'moveBeat', id: line.id, anchor }];
}
