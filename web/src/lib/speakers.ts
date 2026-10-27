import { useTheme } from './theme';

/*
 * One stable color per voice in a video (by order of appearance). Each hue has a
 * dark-theme and a light-theme value chosen for WCAG AA contrast as text on the
 * page background; swatches and the timeline lane use the same value.
 */
const PALETTE: [dark: string, light: string][] = [
  ['#5ec2b4', '#17695f'],
  ['#a3adf7', '#3f4cb0'],
  ['#e9a0bd', '#9c3561'],
  ['#7ab8ec', '#22609b'],
  ['#acd27a', '#46711b'],
  ['#cfa2ec', '#7139a0'],
  ['#dcbd8f', '#74531f'],
  ['#f1a48f', '#9e3f25'],
];

export function speakerColor(index: number, theme: 'dark' | 'light'): string {
  const c = PALETTE[((index % PALETTE.length) + PALETTE.length) % PALETTE.length]!;
  return theme === 'dark' ? c[0] : c[1];
}

/** Returns a function from a voice's color index to its CSS color in the current theme. */
export function useSpeakerColors(): (index: number) => string {
  const [theme] = useTheme();
  return (i: number) => speakerColor(i, theme);
}

/** "12 s", "about a minute", "3 min": how long someone speaks, in plain words. */
export function speakingTime(seconds: number): string {
  if (seconds < 1.5) return 'a second';
  if (seconds < 90) return `${Math.round(seconds)} s`;
  const m = Math.round(seconds / 60);
  return m === 1 ? 'a minute' : `${m} min`;
}

export function pct(score: number | undefined): number {
  return Math.max(0, Math.round((score ?? 0) * 100));
}
