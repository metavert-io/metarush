import type { RenderPlan } from '@metarush/schema';
import type { CalculateMetadataFunction } from 'remotion';
import type { MetaRushEditProps } from './MetaRushEdit';

const positiveInt = (v: unknown, fallback: number) =>
  typeof v === 'number' && Number.isFinite(v) && v >= 1 ? Math.round(v) : fallback;

/**
 * Player props for a plan:
 * `<Player component={MetaRushEdit} inputProps={{ plan }} {...planMetadata(plan)} />`
 */
export const planMetadata = (plan: RenderPlan) => ({
  durationInFrames: positiveInt(plan.durationInFrames, 1),
  fps: positiveInt(plan.fps, 30),
  compositionWidth: positiveInt(plan.width, 1080),
  compositionHeight: positiveInt(plan.height, 1920),
});

/** Composition metadata comes from the plan, never from the composition defaults. */
export const calculatePlanMetadata: CalculateMetadataFunction<MetaRushEditProps> = ({ props }) => {
  const m = planMetadata(props.plan);
  return { durationInFrames: m.durationInFrames, fps: m.fps, width: m.compositionWidth, height: m.compositionHeight };
};
