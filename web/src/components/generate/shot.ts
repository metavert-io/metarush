import type { GenEstimate, GenShotIn } from '../../api/types';

/** A frame a shot starts or ends on: a moment of a clip, or a resources-library picture. */
export interface FramePick {
  assetId?: string;
  at?: number;
  picture?: string;
}

/** The shape a person chose for a shot: none yet ('') lets MetaRush choose. */
export type ShotAspect = '' | '16:9' | '9:16';

export interface ShotChoices {
  projectId: string;
  prompt: string;
  seconds: number;
  aspect: ShotAspect;
  quality: 'draft' | 'final';
  resolution: string;
  takes: string;
  look?: string;
  start?: FramePick;
  end?: FramePick;
  noPeople?: boolean;
}

export const frameRef = (f: FramePick) => (f.picture ? { picture: f.picture } : { assetId: f.assetId!, at: f.at ?? 0 });

/**
 * The request for a shot (D385). It names a shape only when the person chose
 * one: otherwise MetaRush makes it in the shape of the cut it is for (the
 * project's), and the estimate says which and why before the price is agreed.
 */
export function shotRequest(c: ShotChoices): GenShotIn {
  return {
    projectId: c.projectId,
    prompt: c.prompt.trim(),
    seconds: c.seconds,
    ...(c.aspect ? { aspectRatio: c.aspect } : {}),
    quality: c.quality,
    resolution: c.resolution as GenShotIn['resolution'],
    takes: Number(c.takes),
    ...(c.look && !c.start ? { look: c.look } : {}),
    ...(c.start ? { from: frameRef(c.start) } : {}),
    ...(c.start && c.end ? { to: frameRef(c.end) } : {}),
    ...(c.noPeople ? { noPeople: true } : {}),
  };
}

/**
 * What is sent when the price is agreed (D394): the request with that price and
 * the shape the estimate showed, so the shot is made in exactly the shape that
 * was priced — never one MetaRush chooses again after the person said yes.
 */
export function agreedRequest(input: GenShotIn, est: Pick<GenEstimate, 'usd' | 'aspectRatio'>): GenShotIn {
  const shape = est.aspectRatio === '9:16' || est.aspectRatio === '16:9' ? est.aspectRatio : input.aspectRatio;
  return { ...input, maxUsd: est.usd, ...(shape ? { aspectRatio: shape } : {}) };
}

/**
 * What AI work (a picture, a presenter's clip…) sends when its price is agreed
 * (D394): the request with that price and the shape the estimate showed, when
 * it has one — the shape a person asked for stays the one sent.
 */
export function agreedMake<T extends { options?: { aspectRatio?: string } }>(input: T, est: { usd: number; aspectRatio?: string }): T & { maxUsd: number } {
  const shape = input.options?.aspectRatio || est.aspectRatio;
  return { ...input, maxUsd: est.usd, ...(shape ? { options: { ...input.options, aspectRatio: shape } } : {}) };
}

/** The shape the panel shows: the person's choice, else the one MetaRush will make. */
export const shownAspect = (aspect: ShotAspect, est?: Pick<GenEstimate, 'aspectRatio'> | null): ShotAspect =>
  aspect || ((est?.aspectRatio === '9:16' || est?.aspectRatio === '16:9' ? est.aspectRatio : '') as ShotAspect);

/** Whether the shape the person chose isn't the one that fits the cut it is for (the estimate says which would). */
export const chosenAgainstCut = (aspect: ShotAspect, est?: Pick<GenEstimate, 'fitsCut'> | null): boolean =>
  !!aspect && !!est?.fitsCut && est.fitsCut !== aspect;
