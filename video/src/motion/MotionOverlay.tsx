import type { PlanOverlay, RenderPlan, renderPlan } from '@metarush/schema';
import React, { useEffect, useMemo, useState } from 'react';
import { AbsoluteFill, Sequence, getRemotionEnvironment, useCurrentFrame, useDelayRender } from 'remotion';
import type { FrameLayout } from '../core/layout';
import { MOTION_LOWER_BAND } from '../templates/reserve';
import { segmentAt } from '../templates/shared';
import { rgba, type ResolvedBrand } from '../theme/brand';
import { motionHost, type MotionEntry } from './contract';
import { loadMotionBundle } from './load';

type Props = { overlay: PlanOverlay; plan: RenderPlan; brand: ResolvedBrand; layout: FrameLayout };
type PlanMotionStills = renderPlan.PlanMotionStills;
type PlanMotionStill = renderPlan.PlanMotionStill;

/**
 * A project motion graphic (D68). On the render page it loads the bundle the
 * renderer built from the approved archive and draws the component with
 * parameters its own schema accepted. Anywhere else (the dashboard's preview)
 * it draws the still its check drew nearest the moment (D187), or a labeled
 * placeholder in the graphic's region when it was never drawn: the dashboard never
 * runs component code.
 */
export const MotionOverlay: React.FC<Props> = ({ overlay, plan, brand, layout }) => {
  const m = overlay.motion!;
  const url = plan.motionSources?.[m.hash];
  const live = !!motionHost() && !!url;
  // The dashboard's preview (D187): a checked version's still pictures, drawn
  // in the render jail, shown in the graphic's place. Never in renders.
  const stills = live ? undefined : plan.motionStills?.[m.hash];
  return (
    <Sequence from={overlay.from} durationInFrames={overlay.durationInFrames} name={`motion ${m.name} ${overlay.id}`}>
      {live ? (
        <LiveMotion overlay={overlay} plan={plan} brand={brand} layout={layout} url={url!} />
      ) : stills && stills.frames.length ? (
        <MotionStill stills={stills} region={m.region} overlay={overlay} fps={plan.fps} layout={layout} />
      ) : (
        <MotionPlaceholder name={m.name} region={m.region} brand={brand} layout={layout} />
      )}
    </Sequence>
  );
};

/**
 * The still to show at t seconds into a graphic that is on screen for dur
 * seconds (D187): its check drew frames over its own length, so the moment is
 * placed on that length — the way in and the way out at their own pace, the
 * rest in proportion — and the nearest frame wins. The very first and last
 * frames (often still blank) only when there is nothing else.
 */
export function stillAt(stills: PlanMotionStills, t: number, dur: number): PlanMotionStill | undefined {
  const frames = stills.frames;
  if (!frames.length) return undefined;
  const inner = frames.filter((f) => f.label !== 'first' && f.label !== 'last');
  const pool = inner.length ? inner : frames;
  const total = stills.seconds > 0 ? stills.seconds : Math.max(dur, ...frames.map((f) => f.at));
  const edge = Math.min(0.4, dur / 4);
  const edgeAt = Math.min(0.4, total / 4);
  let at: number;
  if (t <= edge) at = edge > 0 ? (t / edge) * edgeAt : 0;
  else if (dur - t <= edge) at = total - (edge > 0 ? ((dur - t) / edge) * edgeAt : 0);
  else at = edgeAt + ((t - edge) * (total - 2 * edgeAt)) / Math.max(dur - 2 * edge, 1e-6);
  return pool.reduce((best, f) => (Math.abs(f.at - at) < Math.abs(best.at - at) ? f : best));
}

/** The graphic's box in the frame: the lower band, the middle, or the whole frame. */
function regionBox(region: string, layout: FrameLayout) {
  const { content } = layout;
  return region === 'full'
    ? { left: 0, top: 0, width: layout.width, height: layout.height }
    : region === 'lower'
      ? { left: content.left, top: content.top + content.height * (1 - MOTION_LOWER_BAND), width: content.width, height: content.height * MOTION_LOWER_BAND }
      : { left: content.left, top: content.top + content.height * 0.3, width: content.width, height: content.height * 0.4 };
}

/**
 * A checked graphic in the dashboard's preview (D187): the still its check
 * drew nearest this moment, in the graphic's place and shape (a still drawn
 * in the cut's own shape covers the frame exactly as the graphic will; one
 * from another shape fits its region), with a small label. Pictures only:
 * the dashboard never runs the graphic's code. Every still is loaded at once
 * so moving through it never flickers.
 */
const MotionStill: React.FC<{ stills: PlanMotionStills; region: string; overlay: PlanOverlay; fps: number; layout: FrameLayout }> = ({ stills, region, overlay, fps, layout }) => {
  const frame = useCurrentFrame();
  const rate = fps > 0 ? fps : 30;
  const shown = stillAt(stills, frame / rate, overlay.durationInFrames / rate);
  const same = stills.width > 0 && stills.height > 0 && Math.abs(stills.width / stills.height - layout.width / layout.height) < 0.01;
  const box = regionBox(region, layout);
  const place = same ? { left: 0, top: 0, width: layout.width, height: layout.height } : box;
  const u = layout.unit;
  const labelAt = region === 'full' ? { left: layout.content.left, top: layout.content.top } : { left: box.left, top: Math.max(0, box.top - Math.round(56 * u)) };
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }} data-motion-still={overlay.id}>
      {stills.frames.map((f) => (
        <img
          key={f.url}
          src={f.url}
          alt=""
          draggable={false}
          style={{ position: 'absolute', ...place, objectFit: 'contain', display: f === shown ? 'block' : 'none' }}
        />
      ))}
      <div
        style={{
          position: 'absolute',
          ...labelAt,
          padding: `${Math.round(6 * u)}px ${Math.round(16 * u)}px`,
          borderRadius: Math.round(999 * u),
          background: 'rgba(0,0,0,0.6)',
          color: 'rgba(255,255,255,0.92)',
          fontFamily: 'system-ui, sans-serif',
          fontSize: Math.max(12, Math.round(32 * u)),
          lineHeight: 1.3,
          whiteSpace: 'nowrap',
        }}
      >
        still — moves in renders
      </div>
    </AbsoluteFill>
  );
};

const LiveMotion: React.FC<Props & { url: string }> = ({ overlay, plan, brand, layout, url }) => {
  const m = overlay.motion!;
  const { delayRender, continueRender, cancelRender } = useDelayRender();
  const [entry, setEntry] = useState<MotionEntry | null>(() => motionHost()?.entries[m.hash] ?? null);
  const [handle] = useState<number | null>(() => (entry ? null : delayRender(`Loading the motion graphic ${m.name}`)));
  useEffect(() => {
    if (entry) return;
    loadMotionBundle(m.hash, url).then(
      (e) => {
        setEntry(e);
        if (handle !== null) continueRender(handle);
      },
      (err: unknown) => cancelRender(err instanceof Error ? err : new Error(String(err))),
    );
  }, [entry, m.hash, url, handle, continueRender, cancelRender]);
  // A graphic whose fields change on screen (D163): the segment on screen now.
  const frame = useCurrentFrame();
  const seg = segmentAt(overlay.segments ?? undefined, frame);
  const fields = seg ? seg.params : overlay.params;
  // Go validated the parameters already; the component's own schema is the last word.
  const parsed = useMemo(() => (entry ? entry.parse(fields ?? {}) : null), [entry, fields]);
  const problem = !entry
    ? ''
    : !entry.component
      ? `the motion graphic ${m.name} (${m.hash.slice(0, 8)}) has no default export`
      : parsed && !parsed.success
        ? `the motion graphic ${m.name} (${m.hash.slice(0, 8)}) refused its parameters: ${parsed.error}`
        : '';
  useEffect(() => {
    if (problem) cancelRender(new Error(problem));
  }, [problem, cancelRender]);
  const Component = entry?.component;
  if (!entry || !Component || !parsed || !parsed.success) return null;
  return (
    <MotionBoundary label={`${m.name} ${overlay.id}`} cancel={cancelRender}>
      <Component
        params={parsed.data as Record<string, unknown>}
        durationInFrames={overlay.durationInFrames}
        from={overlay.from}
        planDurationInFrames={plan.durationInFrames}
        brand={brand}
        layout={layout}
        segments={overlay.segments ?? undefined}
        segmentFrom={seg ? seg.from : 0}
      />
    </MotionBoundary>
  );
};

/** An approved component that throws fails the render (a missing graphic would be silent). */
class MotionBoundary extends React.Component<{ label: string; cancel: (e: Error) => void; children: React.ReactNode }, { failed: boolean }> {
  override state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  override componentDidCatch(error: unknown) {
    const err = new Error(`the motion graphic ${this.props.label} failed while drawing: ${error instanceof Error ? error.message : String(error)}`);
    if (getRemotionEnvironment().isRendering) this.props.cancel(err);
    else console.warn('[metarush]', err.message);
  }
  override render() {
    return this.state.failed ? null : this.props.children;
  }
}

/** Where the graphic will be, in the dashboard's preview. */
const MotionPlaceholder: React.FC<{ name: string; region: string; brand: ResolvedBrand; layout: FrameLayout }> = ({ name, region, brand, layout }) => {
  const u = layout.unit;
  const box = regionBox(region, layout);
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <div
        style={{
          position: 'absolute',
          ...box,
          border: `${Math.max(2, Math.round(3 * u))}px dashed ${rgba(brand.accent, 0.85)}`,
          borderRadius: Math.round(18 * u),
          background: rgba(brand.background, 0.45),
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          color: brand.text,
          fontFamily: brand.fontFamily,
          fontSize: Math.round(34 * u),
          textAlign: 'center',
          padding: Math.round(16 * u),
          boxSizing: 'border-box',
        }}
      >
        <span>
          Motion graphic “{name}”
          <br />
          <span style={{ fontSize: Math.round(24 * u), opacity: 0.75 }}>shows in renders</span>
        </span>
      </div>
    </AbsoluteFill>
  );
};
