import type { CostPart } from '../api/types';

/*
 * Estimates of what outside AI services cost (D208), as the dashboard shows
 * them (components/CostEstimate.tsx): the numbers and words come from the
 * server (internal/costs); this groups the parts and says each one's price.
 * Tested by web/scripts/costs-test.mjs.
 */

/**
 * An amount the way people read prices, as Go's costs.Money says it: $0.40,
 * $12.50, $120, $1,234.56, less than a cent, $0 (shared cases:
 * internal/costs/testdata/money.json).
 */
export function money(usd: number): string {
  if (!(usd > 0)) return '$0';
  if (usd < 0.01) return 'less than a cent';
  const cents = Math.round(usd * 100);
  const whole = Math.floor(cents / 100);
  const frac = cents % 100;
  const w = whole.toLocaleString('en-US');
  if (whole >= 100 && frac === 0) return `$${w}`;
  return `$${w}.${String(frac).padStart(2, '0')}`;
}

/** An estimate in words, as Go's costs.About says it: about $0.40, less than a cent, nothing. */
export function estimateWords(usd: number): string {
  if (!(usd > 0)) return 'nothing';
  if (usd < 0.01) return 'less than a cent';
  return `about ${money(usd)}`;
}

/** One part's price: $0.40, less than a cent, free (made before). */
export function partPrice(p: Pick<CostPart, 'usd' | 'free'>): string {
  if (p.free) return 'free (made before)';
  return money(p.usd);
}

export interface PartGroup {
  key: string;
  step?: string;
  service: string;
  model?: string;
  what: string;
  parts: CostPart[];
  /** The prices the group's parts use (usually one). */
  rates: string[];
}

/** The parts grouped by step, service, model and what they make, in order of first appearance. */
export function groupParts(parts: CostPart[]): PartGroup[] {
  const out: PartGroup[] = [];
  const byKey = new Map<string, PartGroup>();
  for (const p of parts) {
    const key = [p.step ?? '', p.service, p.model ?? '', p.what].join('\u0000');
    let g = byKey.get(key);
    if (!g) {
      g = { key, step: p.step, service: p.service, model: p.model, what: p.what, parts: [], rates: [] };
      byKey.set(key, g);
      out.push(g);
    }
    g.parts.push(p);
    if (p.rate && !g.rates.includes(p.rate)) g.rates.push(p.rate);
  }
  return out;
}
