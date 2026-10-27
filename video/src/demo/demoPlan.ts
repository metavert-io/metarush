import type { PlanCaptionLine, PlanOverlay, RenderPlan } from '@metarush/schema';

/**
 * Small media-free plans for Remotion Studio (`npm run studio`): every
 * template once plus word-timed captions over the plain background. Real
 * footage is exercised by `npm run render:selftest`, which serves synthetic
 * media over HTTP.
 */
const FPS = 30;

/** Mirrors Go's platformSafe["generic"] margins (top, bottom, left, right). */
const safeFor = (w: number, h: number) => ({
  top: Math.floor(h * 0.06),
  bottom: Math.floor(h * 0.08),
  left: Math.floor(w * 0.06),
  right: Math.floor(w * 0.06),
});

const overlay = (id: string, template: string, from: number, durationInFrames: number, params: unknown): PlanOverlay => ({
  id,
  template,
  templateVersion: '1',
  from,
  durationInFrames,
  params,
});

const captionLines = (script: string[], startFrame: number, wordFrames: number, emphasis: string[]): PlanCaptionLine[] => {
  const lines: PlanCaptionLine[] = [];
  let t = startFrame;
  for (const text of script) {
    const words = text.split(' ').map((w) => {
      const from = t;
      t += wordFrames;
      return { text: w, from, to: t - 1, emphasis: emphasis.includes(w.toLowerCase().replace(/[^a-z-]/g, '')) };
    });
    lines.push({ from: words[0]!.from, to: t, text, words });
    t += 8;
  }
  return lines;
};

export const makeDemoPlan = (width: number, height: number): RenderPlan => {
  const durationInFrames = 18 * FPS;
  return {
    schemaVersion: 1,
    revisionId: 'rev_demo',
    projectId: 'prj_demo',
    specHash: 'demo',
    media: 'proxy',
    width,
    height,
    fps: FPS,
    durationInFrames,
    background: '#16181D',
    brand: { primary: '#F2EDE4', accent: '#E0A458', text: '#F7F5F0', background: '#0B0B0E', font: 'Inter' },
    safeArea: safeFor(width, height),
    clips: [],
    audio: [],
    dialogueRanges: [],
    captions: {
      style: 'emphasis',
      position: 'lower',
      uppercase: false,
      precision: 'word',
      lines: captionLines(
        [
          'Every cut is resolved before',
          'a single frame is rendered.',
          'The preview and the export',
          'share one exact plan.',
        ],
        3.2 * FPS,
        9,
        ['exact', 'plan'],
      ),
    },
    overlays: [
      overlay('ov_title', 'title.card', 0, 3 * FPS, { title: 'The Long Way Round', subtitle: 'A field recording in three parts' }),
      overlay('ov_lt', 'lower-third.clean', 3 * FPS, 3 * FPS, { name: 'Maren Okafor', title: 'Sound recordist, Lofoten' }),
      overlay('ov_quote', 'quote.kinetic', 6 * FPS, 3.5 * FPS, {
        text: 'You stop chasing the light. You learn to wait for it.',
        attribution: 'Maren Okafor',
        emphasis: ['wait'],
      }),
      overlay('ov_cine', 'lower-third.cinematic', 9.5 * FPS, 3 * FPS, { name: 'Henningsvær', title: 'Nordland, Norway' }),
      overlay('ov_stat', 'datacard.stat', 12.5 * FPS, 3 * FPS, {
        value: '1,840',
        label: 'hours of tape recorded on the island',
        source: 'Production log (sample data)',
        trend: 'up',
      }),
      overlay('ov_end', 'endcard.cta', 15.5 * FPS, 2.5 * FPS, {
        headline: 'Hear the full recording',
        subline: 'New episodes every other Thursday.',
        cta: 'Listen now',
        handle: 'longwayround',
      }),
    ],
    sources: {},
  };
};

export const DEMO_PLAN_VERTICAL = makeDemoPlan(1080, 1920);
export const DEMO_PLAN_LANDSCAPE = makeDemoPlan(1920, 1080);
export const DEMO_PLAN_SQUARE = makeDemoPlan(1080, 1080);
