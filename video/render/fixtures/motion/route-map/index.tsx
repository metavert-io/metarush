import { evolvePath, getPointAtLength, getLength } from '@remotion/paths';
import { zColor } from '@remotion/zod-types';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { z } from 'zod';
import { makeOverlayClock, rgba, type MotionMeta, type MotionProps } from '@metarush/motion';
import { arcPath } from './geometry';

export const schema = z.object({
  from: z.string().min(1).max(40).describe('Where the route starts'),
  to: z.string().min(1).max(40).describe('Where it ends'),
  color: zColor().optional().describe('Line color (default: the brand accent)'),
});

export const meta: MotionMeta = {
  summary: 'An animated route: a line draws from one place to another, with both names.',
  region: 'center',
  minDuration: 2,
  maxDuration: 8,
  checkAt: [1.2],
};

export default function RouteMap({ params, durationInFrames, brand, layout }: MotionProps<typeof schema>) {
  const frame = useCurrentFrame();
  const { content, unit: u, fps } = layout;
  const c = makeOverlayClock(frame, durationInFrames, fps, 1.6);
  const w = content.width * 0.8;
  const h = content.height * 0.28;
  const d = arcPath(w, h);
  const draw = c.enter(0.2, 1.2);
  const evolved = evolvePath(draw, d);
  const len = getLength(d);
  const dot = getPointAtLength(d, len * draw) ?? { x: 0, y: h };
  const color = params.color ?? brand.accent;
  const label = (text: string, x: number, show: number) => (
    <div
      style={{
        position: 'absolute',
        left: x - 200 * u,
        width: 400 * u,
        top: h + 18 * u,
        textAlign: 'center',
        fontFamily: brand.fontFamily,
        fontSize: 40 * u,
        fontWeight: 600,
        color: brand.text,
        opacity: show * c.exit,
        transform: `translateY(${(1 - show) * 12 * u}px)`,
        textShadow: `0 ${2 * u}px ${14 * u}px ${rgba(brand.background, 0.6)}`,
      }}
    >
      {text}
    </div>
  );
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <div style={{ position: 'absolute', left: content.left + (content.width - w) / 2, top: content.top + (content.height - h) / 2, width: w, height: h }}>
        <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} style={{ overflow: 'visible', opacity: c.exit }}>
          <path d={d} fill="none" stroke={rgba(brand.text, 0.25)} strokeWidth={4 * u} strokeDasharray={`${8 * u} ${10 * u}`} />
          <path d={d} fill="none" stroke={color} strokeWidth={7 * u} strokeLinecap="round" strokeDasharray={evolved.strokeDasharray} strokeDashoffset={evolved.strokeDashoffset} />
          <circle cx={0} cy={h} r={11 * u} fill={brand.text} />
          <circle cx={w} cy={h} r={11 * u} fill={draw > 0.98 ? color : rgba(brand.text, 0.5)} />
          <circle cx={dot.x} cy={dot.y} r={15 * u} fill={color} />
        </svg>
        {label(params.from, 0, c.enter(0, 0.5))}
        {label(params.to, w, c.enter(1.1, 0.5))}
      </div>
    </AbsoluteFill>
  );
}
