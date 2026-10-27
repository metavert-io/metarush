// The dashboard's copy of internal/plainwords (Go): what a person should not
// have to read in text meant for them — internal IDs, command flags and
// commands, file paths, JSON, internal codes and unexplained jargon. The
// jargon list is internal/plainwords/rules.json, shared with Go.
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const rules = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'plainwords', 'rules.json'), 'utf8'));

const quote = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const isWord = (ch) => /[\p{L}\p{N}_]/u.test(ch);

const terms = rules.jargon.map((t) => {
  let q = quote(t.term);
  q = (isWord(t.term[0]) ? '(?:^|[^\\p{L}\\p{N}_-])(' : '(') + q;
  q += isWord(t.term[t.term.length - 1]) ? ')(?=$|[^\\p{L}\\p{N}_-])' : ')';
  return { ...t, re: new RegExp(q, t.case ? 'gu' : 'giu') };
});

const urlRe = /\b(?:https?|file):\/\/[^\s)>\]"'`]+/gi;
const idRe = /\b[a-z]{1,4}_[0-9a-z]{26}\b|\bct_\d+_[0-9a-f]{16,}\b|\bmrx_[0-9A-Za-z_-]{8,}|\b[0-9a-f]{40,64}\b/g;
const flagRe = /(?:^|[\s(\[`"'=])(--[a-z][a-z0-9-]{1,40})\b/g;
const cmdRe = /\b(metarush\s+[a-z][a-z-]*(?:\s+[a-z][a-z-]*)?|(?:brew|npm|npx|pip3?|git|gh|yt-dlp|ffmpeg|ffprobe)\s+(?:install|upgrade|update|run|clone|auth|-[a-z]))/g;
const pathRe = /(?:^|[\s(\[`"'=:])((?:~|<home>|\.\.?|\/(?:Users|home|private|tmp|var|opt|etc|Volumes|Applications|Library|usr|mnt|srv|root))\/[^\s"'`)\]]*|[A-Z]:\\[^\s"']+)/g;
const jsonRe = /\{\s*"[A-Za-z_][A-Za-z0-9_]*"\s*:|\[\s*\{\s*"/;
const errCode = /\bE_[A-Z][A-Z_]{2,}\b/g;
const snakeRe = /\b[a-z]{2,}(?:_[a-z]{2,})+\b/g;

/** Reports whether a term is followed by a short plain-words explanation. */
export function explained(after) {
  const a = after.slice(0, 80).replace(/^[  ]+/, '').toLowerCase();
  return ['(', '— ', '- ', ', which', ', meaning', ', that is', 'means', 'is when', 'is the', 'are the', ': '].some((p) => a.startsWith(p));
}

/*
 * People's own words — clip titles, file names, people's, projects' and
 * resources' names, what someone said — are theirs (D113), so they may look
 * like codes (riverside_sam_raw-video-cfr_garden_show_0002). What is shown
 * marks them: the dashboard wraps them in <Theirs> (data-user-content), and
 * plain text puts them in curly quotes. checkShown skips what is marked except
 * internal IDs, tokens and missing values; MetaRush's own words around them are
 * checked as check does. Same rules as internal/plainwords (CheckShown).
 */
export const THEIRS_OPEN = '“';
export const THEIRS_CLOSE = '”';

/** MetaRush's words (people's words replaced by “it”) and the people's words that were marked. */
export function splitTheirs(text) {
  const theirs = [];
  let ours = '';
  for (;;) {
    const i = text.indexOf(THEIRS_OPEN);
    if (i < 0) break;
    const j = text.indexOf(THEIRS_CLOSE, i + 1);
    if (j < 0) break; // an opening quote without its closing one marks nothing
    ours += text.slice(0, i) + THEIRS_OPEN + 'it' + THEIRS_CLOSE;
    theirs.push(text.slice(i + 1, j));
    text = text.slice(j + 1);
  }
  return { ours: ours + text, theirs };
}

/** What people's own words may not hold either: internal IDs and tokens, missing values. */
export function inTheirs(text) {
  return check(text).filter((f) => f.kind === 'id' || f.kind === 'leak');
}

/** Lists what in text shown to a person they should not have to read, people's marked words checked only by inTheirs. */
export function checkShown(text) {
  const { ours, theirs } = splitTheirs(text);
  return [...check(ours), ...theirs.flatMap(inTheirs)];
}

/*
 * The Claude Code panel's stricter rules (D140, rules.json "panel"): its step
 * lines, cards and notes say what Claude's work means for the video, never its
 * files, tools or versions. Same as internal/plainwords CheckPanel.
 */
const panelRules = (rules.panel ?? []).map((r) => ({ ...r, re: new RegExp(r.pattern, 'g') }));

/** checkShown plus the panel's stricter rules over MetaRush's own words. */
export function checkPanel(text) {
  const out = checkShown(text);
  const ours = splitTheirs(text).ours.replace(urlRe, ' ');
  const seen = new Set();
  for (const r of panelRules) {
    r.re.lastIndex = 0;
    for (const m of ours.matchAll(r.re)) {
      const t = m[0].trim();
      const k = r.kind + '\0' + t;
      if (seen.has(k)) continue;
      seen.add(k);
      out.push({ kind: r.kind, text: t, plain: r.plain });
    }
  }
  return out;
}

/** Lists what in text a person should not have to read: [{kind, text, plain}]. */
export function check(text) {
  const out = [];
  const seen = new Set();
  const add = (kind, s, plain = '') => {
    const k = kind + '\0' + s;
    if (!seen.has(k)) {
      seen.add(k);
      out.push({ kind, text: s, plain });
    }
  };
  text = text.replace(urlRe, ' ');
  for (const m of text.matchAll(idRe)) add('id', m[0]);
  for (const m of text.matchAll(flagRe)) add('flag', m[1]);
  for (const m of text.matchAll(cmdRe)) add('command', m[0]);
  for (const m of text.matchAll(pathRe)) add('path', m[1].replace(/[.,;:]+$/, ''));
  const j = text.match(jsonRe);
  if (j) add('json', j[0]);
  for (const m of text.matchAll(errCode)) add('code', m[0]);
  for (const m of text.matchAll(snakeRe)) {
    const rest = text.slice(m.index + m[0].length);
    if (rest.startsWith('.') && rest.length > 1 && isWord(rest[1])) continue;
    if (m.index > 0 && './@#-'.includes(text[m.index - 1])) continue;
    add('code', m[0]);
  }
  for (const t of terms) {
    t.re.lastIndex = 0;
    for (const m of text.matchAll(t.re)) {
      const at = m.index + m[0].indexOf(m[1]);
      const end = at + m[1].length;
      const before = text.slice(Math.max(0, at - 60), at);
      const inParens = before.lastIndexOf('(') > before.lastIndexOf(')');
      if (inParens || explained(text.slice(end))) continue;
      add(t.kind || 'jargon', m[1], t.plain);
      break;
    }
  }
  return out;
}
