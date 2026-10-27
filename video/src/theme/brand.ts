import type { Brand } from '@metarush/schema';
import { fontStackFor, type BrandFont } from './fonts';

/** Mirrors Go's editspec.DefaultBrand so a partial brand renders identically. */
export const DEFAULT_BRAND = {
  primary: '#F2EDE4',
  accent: '#E0A458',
  text: '#F7F5F0',
  background: '#0B0B0E',
  font: 'Inter' as BrandFont,
};

export type ResolvedBrand = {
  primary: string;
  accent: string;
  text: string;
  background: string;
  font: BrandFont;
  /** CSS font stack for `font`. */
  fontFamily: string;
  /** Text color with guaranteed contrast on an `accent` fill (CTA pills). */
  onAccent: string;
};

const HEX = /^#[0-9a-fA-F]{6}$/;

const pickHex = (value: string | undefined, fallback: string) =>
  value && HEX.test(value) ? value : fallback;

export const hexToRgb = (hex: string): [number, number, number] => {
  const h = HEX.test(hex) ? hex : '#000000';
  return [parseInt(h.slice(1, 3), 16), parseInt(h.slice(3, 5), 16), parseInt(h.slice(5, 7), 16)];
};

/** `rgba()` string from a #RRGGBB token. */
export const rgba = (hex: string, alpha: number): string => {
  const [r, g, b] = hexToRgb(hex);
  return `rgba(${r}, ${g}, ${b}, ${Math.max(0, Math.min(1, alpha))})`;
};

const channel = (c: number) => {
  const s = c / 255;
  return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
};

export const luminance = (hex: string): number => {
  const [r, g, b] = hexToRgb(hex);
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
};

export const contrastRatio = (a: string, b: string): number => {
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
};

/** First candidate reaching 4.5:1 on `bg`, else whichever contrasts most. */
export const readableOn = (bg: string, candidates: string[]): string => {
  let best = candidates[0] ?? '#000000';
  let bestRatio = 0;
  for (const c of candidates) {
    const r = contrastRatio(bg, c);
    if (r >= 4.5) return c;
    if (r > bestRatio) {
      best = c;
      bestRatio = r;
    }
  }
  return best;
};

export const resolveBrand = (brand: Brand | undefined): ResolvedBrand => {
  const primary = pickHex(brand?.primary, DEFAULT_BRAND.primary);
  const accent = pickHex(brand?.accent, DEFAULT_BRAND.accent);
  const text = pickHex(brand?.text, DEFAULT_BRAND.text);
  const background = pickHex(brand?.background, DEFAULT_BRAND.background);
  const font: BrandFont =
    brand?.font === 'Fraunces' || brand?.font === 'JetBrains Mono' || brand?.font === 'Inter'
      ? brand.font
      : DEFAULT_BRAND.font;
  return {
    primary,
    accent,
    text,
    background,
    font,
    fontFamily: fontStackFor(font),
    onAccent: readableOn(accent, [background, '#0B0B0E', text, '#F7F5F0']),
  };
};

/**
 * The brand with a loaded font file (a font resource, `brand.fontFile`) put
 * first in its font stack; the built-in font stays the fallback.
 */
export const withFontFile = (brand: ResolvedBrand, family: string | null): ResolvedBrand =>
  family ? { ...brand, fontFamily: `"${family}", ${brand.fontFamily}` } : brand;

/** Validates a CSS hex color from the plan, with a fallback. */
export const safeColor = (value: string | undefined, fallback: string): string =>
  pickHex(value, fallback);
