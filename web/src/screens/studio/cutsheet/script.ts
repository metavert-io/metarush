/*
 * Reads a cut sheet (the text `cut.show` returns) for the Script view. This is
 * presentation only: the Go parser (internal/cutsheet) stays the one that
 * decides what a sheet means. Here the structure is recognized (settings,
 * treatment, sections, numbered clips, ↳ lines, derived > lines, Why/Evidence
 * and notes, bullets) and phrases are kept as written, so a new spelling (a
 * split-edit phrase on a ↳ line, a new clip setting) shows up as written
 * instead of breaking the page. Anything not recognized is kept as Markdown.
 *
 * No imports: web/scripts/script-test.mjs runs it on the Go fixtures.
 */

export interface Setting {
  key: string;
  value: string;
  /** The `# …` comment MetaRush writes after a value (file names, sizes, lengths). */
  comment?: string;
  children: Setting[];
}

export interface Speech {
  /** Who says it (as the sheet names them); empty when the sheet doesn't say. */
  speaker: string;
  text: string;
}

export interface Labeled {
  label: string;
  text: string;
}

export interface Body {
  /** Derived lines (`> …`): the words spoken, where a line lands, what a graphic is over. */
  derived: string[];
  why?: string;
  evidence: string[];
  /** Other `Label: text` lines MetaRush writes (Rights, Delivery, Take, Locked…). */
  labeled: Labeled[];
  /** The person's notes, one entry per paragraph. */
  notes: string[];
}

export interface ClipItem extends Body {
  kind: 'clip';
  number: number;
  name?: string;
  footage: string;
  /** Another project's cut played here: its short name under `cuts:` ("cut from brand 0:00 → 0:12"). */
  nested?: string;
  /** A conversation played here: its short name under Conversations ("conversation call 0:00 → 0:51"). */
  conversation?: string;
  range: string;
  settings: string[];
  speech: Speech[];
  /** No words in this clip (the sheet says "(no words spoken here)"). */
  silent: boolean;
  line: number;
}

export interface TransitionItem {
  kind: 'transition';
  /** The picture transition ("dissolve 0.4 s"), then any phrases after it, as written. */
  parts: string[];
  line: number;
}

export interface MarkdownItem {
  kind: 'markdown';
  text: string;
}

export interface BulletItem extends Body {
  kind: 'bullet';
  name?: string;
  /** The rest of the first part (graphics: `template at 0:01 for 3 s`; sound: `footage 0:00 → 0:18`). */
  head: string;
  /** Narration: the words in quotes. */
  words?: string;
  settings: string[];
  line: number;
}

export interface CaptionItem {
  kind: 'caption';
  range: string;
  speaker?: string;
  text: string;
  line: number;
}

export type SectionKind = 'clips' | 'conversations' | 'voiceover' | 'graphics' | 'sound' | 'captions' | 'other';

export interface Section {
  kind: SectionKind;
  title: string;
  items: (ClipItem | TransitionItem | MarkdownItem | BulletItem | CaptionItem)[];
  /** Voiceover: `Recording: …` (one continuous read). */
  recording?: string;
  /** Voiceover: the script exactly as the person gave it (`### Original script` and its fence, D86). */
  original?: string;
}

export interface ScriptDoc {
  settings: Setting[];
  /** The settings block as written (IDs and hashes included), for Details. */
  settingsText: string;
  title?: string;
  /** Notes about the whole cut (between the settings and the first section), as Markdown. */
  treatment: string;
  sections: Section[];
}

const CLIP_RE = /^(\d{1,4})[.)]\s+(.*)$/;
const TRANSITION_RE = /^(?:↳|=>)\s*(.*)$/;
const NAME_RE = /^([a-z][a-z0-9_-]*):\s+(.*)$/;
const LABEL_RE = /^([A-Z][A-Za-z ]{1,24}):\s?(.*)$/;
const CAPTION_RE = /^(\d+:\d{2}(?::\d{2})?(?:\.\d+)?\s*(?:→|->)\s*\d+:\d{2}(?::\d{2})?(?:\.\d+)?)\s+(.*)$/;
const KNOWN_LABELS = new Set(['Why', 'Evidence', 'Rights', 'Delivery', 'Take', 'Locked', 'Recording', 'Notes', 'Source', 'Member', 'Shot']);

/** Splits on ` · ` (or ` | `), never inside quotes or a {…} block. */
export function splitParts(s: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let quote: string | null = null;
  let cur = '';
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    if (quote) {
      cur += ch;
      if (ch === '\\' && quote === '"' && i + 1 < s.length) {
        cur += s[++i];
        continue;
      }
      if ((quote === '"' && ch === '"') || (quote === '“' && ch === '”')) quote = null;
      continue;
    }
    if (ch === '"' || ch === '“') {
      quote = ch;
      cur += ch;
      continue;
    }
    if (ch === '{' || ch === '[') depth++;
    if ((ch === '}' || ch === ']') && depth > 0) depth--;
    if (depth === 0 && (ch === '·' || ch === '|') && s[i - 1] === ' ' && s[i + 1] === ' ') {
      out.push(cur.trim());
      cur = '';
      i++;
      continue;
    }
    cur += ch;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

/** "Ada: “When I…” Speaker 2: “A robot.”" → who says what. */
export function parseSpeech(s: string): Speech[] {
  const text = s.trim();
  // A name starts with a letter or digit ("Ada", "Speaker 2", "Dr. O'Neil"), never with the "…" between quotes.
  const re = /(?:^|\s)([\p{L}\p{N}][\p{L}\p{N} .'’-]{0,40}?):\s+(?=[“"])/gu;
  const marks: { at: number; end: number; speaker: string }[] = [];
  for (let m = re.exec(text); m; m = re.exec(text)) {
    const at = m.index + (m[0].length - m[0].trimStart().length);
    marks.push({ at, end: m.index + m[0].length, speaker: m[1]!.trim() });
  }
  if (!marks.length) return [{ speaker: '', text }];
  const out: Speech[] = [];
  if (marks[0]!.at > 0) out.push({ speaker: '', text: text.slice(0, marks[0]!.at).trim() });
  // The "…" between one person's words and the next person's is left out.
  marks.forEach((m, i) => out.push({ speaker: m.speaker, text: text.slice(m.end, i + 1 < marks.length ? marks[i + 1]!.at : undefined).trim().replace(/(["”])\s*…$/, '$1') }));
  return out.filter((x) => x.text);
}

function unquoteNote(s: string): string {
  try {
    const v: unknown = JSON.parse(s);
    return typeof v === 'string' ? v : s;
  } catch {
    return s;
  }
}

/** The lines under a clip or bullet (already de-indented). */
function readBody(lines: string[]): Body {
  const body: Body = { derived: [], evidence: [], labeled: [], notes: [] };
  let para: string[] = [];
  const flush = () => {
    if (para.length) body.notes.push(para.join('\n'));
    para = [];
  };
  for (const raw of lines) {
    const l = raw.trimEnd();
    if (!l.trim()) {
      flush();
      continue;
    }
    const t = l.trimStart();
    if (t.startsWith('>')) {
      flush();
      body.derived.push(t.replace(/^>\s?/, ''));
      continue;
    }
    if (t.startsWith('\\')) {
      para.push(t.slice(1));
      continue;
    }
    const m = LABEL_RE.exec(t);
    if (m && KNOWN_LABELS.has(m[1]!)) {
      flush();
      const [, label, text = ''] = m;
      if (label === 'Why') body.why = body.why ? `${body.why}\n${text}` : text;
      else if (label === 'Evidence') body.evidence.push(text);
      else if (label === 'Notes') body.notes.push(unquoteNote(text));
      else body.labeled.push({ label: label!, text });
      continue;
    }
    para.push(t);
  }
  flush();
  return body;
}

/** Groups a section's lines into items: a head line at column 0 plus its indented lines. */
function groups(lines: { text: string; line: number }[], isHead: (t: string) => boolean) {
  const out: { head?: { text: string; line: number }; body: string[]; loose: string[] }[] = [];
  let cur: (typeof out)[number] | null = null;
  for (const l of lines) {
    const indented = /^\s/.test(l.text);
    if (!indented && l.text.trim() && isHead(l.text)) {
      cur = { head: l, body: [], loose: [] };
      out.push(cur);
      continue;
    }
    if (cur && (indented || !l.text.trim())) {
      cur.body.push(l.text);
      continue;
    }
    // Text at column 0 that isn't a head: kept as Markdown.
    cur = null;
    const last = out[out.length - 1];
    if (last && !last.head) last.loose.push(l.text);
    else out.push({ body: [], loose: [l.text] });
  }
  return out;
}

function dedent(lines: string[]): string[] {
  const widths = lines.filter((l) => l.trim()).map((l) => l.length - l.trimStart().length);
  const n = widths.length ? Math.min(...widths) : 0;
  return lines.map((l) => l.slice(Math.min(n, l.length - l.trimStart().length)));
}

function trimBlank(lines: string[]): string {
  return lines.join('\n').replace(/^\s*\n/, '').trimEnd();
}

function parseClips(lines: { text: string; line: number }[]): Section['items'] {
  const items: Section['items'] = [];
  for (const g of groups(lines, (t) => CLIP_RE.test(t) || TRANSITION_RE.test(t))) {
    if (!g.head) {
      const text = trimBlank(g.loose);
      if (text) items.push({ kind: 'markdown', text });
      continue;
    }
    const tm = TRANSITION_RE.exec(g.head.text);
    if (tm) {
      items.push({ kind: 'transition', parts: splitParts(tm[1]!), line: g.head.line });
      const rest = trimBlank(dedent(g.body));
      if (rest) items.push({ kind: 'markdown', text: rest });
    } else {
      const cm = CLIP_RE.exec(g.head.text)!;
      const parts = splitParts(cm[2]!);
      let first = parts[0] ?? '';
      let name: string | undefined;
      const nm = NAME_RE.exec(first);
      if (nm) {
        name = nm[1];
        first = nm[2]!;
      }
      const nestedM = /^cut from (\S+)\s*(.*)$/.exec(first);
      const convM = nestedM ? null : /^conversation (\S+)\s*(.*)$/.exec(first);
      const sp = first.indexOf(' ');
      const footage = nestedM ? '' : convM ? convM[1]! : sp > 0 ? first.slice(0, sp) : first;
      const range = nestedM ? nestedM[2]!.trim() : convM ? convM[2]!.trim() : sp > 0 ? first.slice(sp + 1).trim() : '';
      const body = readBody(dedent(g.body));
      const said = body.derived.filter((d) => !/^\(.*\)$/.test(d.trim()) && !/ from .+, version \d/.test(d));
      const silent = body.derived.some((d) => /^\(.*\)$/.test(d.trim()));
      items.push({ kind: 'clip', number: Number(cm[1]), name, footage, nested: nestedM?.[1], conversation: convM?.[1], range, settings: parts.slice(1), speech: said.flatMap(parseSpeech), silent, line: g.head.line, ...body, derived: body.derived });
    }
    const loose = trimBlank(g.loose);
    if (loose) items.push({ kind: 'markdown', text: loose });
  }
  return items;
}

/**
 * `### Original script` and the text between its fences (three or more
 * backticks; the same fence closes it): the script exactly as the person gave
 * it. The block is taken out of the section's lines.
 */
export function takeOriginal(lines: { text: string; line: number }[]): { lines: { text: string; line: number }[]; original?: string } {
  const h = lines.findIndex((l) => /^###\s+(?:the\s+|your\s+)?original(?:\s+script)?\s*$/i.test(l.text.trim()));
  if (h < 0) return { lines };
  let i = h + 1;
  while (i < lines.length && lines[i]!.text.trim() === '') i++;
  const open = /^(`{3,})[A-Za-z0-9_-]*$/.exec(lines[i]?.text ?? '');
  if (!open) return { lines };
  const fence = open[1]!;
  const body: string[] = [];
  let j = i + 1;
  for (; j < lines.length && lines[j]!.text !== fence; j++) body.push(lines[j]!.text);
  if (j >= lines.length) return { lines };
  return { lines: [...lines.slice(0, h), ...lines.slice(j + 1)], original: body.join('\n') };
}

function parseBullets(lines: { text: string; line: number }[], narration: boolean): { items: Section['items']; recording?: string } {
  const items: Section['items'] = [];
  let recording: string | undefined;
  for (const g of groups(lines, (t) => /^[-*]\s+/.test(t) || (narration && /^Recording:/.test(t)))) {
    if (!g.head) {
      const text = trimBlank(g.loose);
      if (text) items.push({ kind: 'markdown', text });
      continue;
    }
    if (narration && g.head.text.startsWith('Recording:')) {
      recording = g.head.text.replace(/^Recording:\s*/, '');
      continue;
    }
    const parts = splitParts(g.head.text.replace(/^[-*]\s+/, ''));
    let first = parts.shift() ?? '';
    let name: string | undefined;
    const nm = NAME_RE.exec(first);
    if (nm) {
      name = nm[1];
      first = nm[2]!;
    }
    let words: string | undefined;
    if (narration) {
      const q = /^(?:"((?:[^"\\]|\\.)*)"|“([^”]*)”)\s*$/.exec(first);
      if (q) {
        words = q[1] !== undefined ? unquoteNote(`"${q[1]}"`) : q[2];
        first = '';
      }
    }
    items.push({ kind: 'bullet', name, head: first, words, settings: parts, line: g.head.line, ...readBody(dedent(g.body)) });
    const loose = trimBlank(g.loose);
    if (loose) items.push({ kind: 'markdown', text: loose });
  }
  return { items, recording };
}

function parseCaptions(lines: { text: string; line: number }[]): Section['items'] {
  const items: Section['items'] = [];
  let md: string[] = [];
  const flush = () => {
    const text = trimBlank(md);
    if (text) items.push({ kind: 'markdown', text });
    md = [];
  };
  for (const l of lines) {
    const t = l.text.replace(/^[-*]\s+/, '');
    const m = /^[-*]\s+/.test(l.text) ? CAPTION_RE.exec(t) : null;
    if (!m) {
      md.push(l.text);
      continue;
    }
    flush();
    let rest = m[2]!.trim();
    let speaker: string | undefined;
    const sm = /^([^"“:]{1,40}):\s+(?=["“])/.exec(rest);
    if (sm) {
      speaker = sm[1]!.trim();
      rest = rest.slice(sm[0].length);
    }
    const q = /^"((?:[^"\\]|\\.)*)"$|^“([^”]*)”$/.exec(rest);
    items.push({ kind: 'caption', range: m[1]!, speaker, text: q ? (q[1] !== undefined ? unquoteNote(`"${q[1]}"`) : q[2]!) : rest, line: l.line });
  }
  flush();
  return items;
}

/** A small reader for the settings block: `key: value  # comment`, nested by indentation. */
function parseSettings(lines: string[]): Setting[] {
  const root: Setting[] = [];
  const stack: { indent: number; list: Setting[] }[] = [{ indent: -1, list: root }];
  for (const raw of lines) {
    if (!raw.trim() || raw.trimStart().startsWith('#')) continue;
    const indent = raw.length - raw.trimStart().length;
    const m = /^([^:]+):\s*(.*)$/.exec(raw.trim());
    if (!m) continue;
    let value = m[2]!;
    let comment: string | undefined;
    const c = /\s+#\s(.*)$/.exec(value);
    if (c && !/^["']/.test(value)) {
      comment = c[1]!.trim();
      value = value.slice(0, c.index).trim();
    }
    if (/^"(?:[^"\\]|\\.)*"$/.test(value)) value = unquoteNote(value);
    while (stack.length > 1 && stack[stack.length - 1]!.indent >= indent) stack.pop();
    const s: Setting = { key: m[1]!.trim(), value, comment, children: [] };
    stack[stack.length - 1]!.list.push(s);
    stack.push({ indent, list: s.children });
  }
  return root;
}

export function sectionKind(title: string): SectionKind {
  switch (title.trim().toLowerCase()) {
    case 'clips':
      return 'clips';
    case 'conversations':
    case 'conversation':
    case 'calls':
      return 'conversations';
    case 'voiceover':
    case 'narration':
      return 'voiceover';
    case 'graphics':
      return 'graphics';
    case 'sound':
      return 'sound';
    case 'captions':
      return 'captions';
  }
  return 'other';
}

export function parseScript(text: string): ScriptDoc {
  const all = text.replace(/\r\n?/g, '\n').split('\n');
  let start = 0;
  let settingsLines: string[] = [];
  if (all[0]?.trim() === '---') {
    const end = all.findIndex((l, i) => i > 0 && l.trim() === '---');
    if (end > 0) {
      settingsLines = all.slice(1, end);
      start = end + 1;
    }
  }
  const settings = parseSettings(settingsLines);
  const doc: ScriptDoc = { settings, settingsText: settingsLines.join('\n'), title: settings.find((s) => s.key === 'title')?.value || undefined, treatment: '', sections: [] };
  let cur: { title: string; lines: { text: string; line: number }[] } | null = null;
  const treatment: string[] = [];
  const close = () => {
    if (!cur) return;
    const kind = sectionKind(cur.title);
    const sec: Section = { kind, title: cur.title, items: [] };
    if (kind === 'clips') sec.items = parseClips(cur.lines);
    else if (kind === 'voiceover') {
      const o = takeOriginal(cur.lines);
      const r = parseBullets(o.lines, true);
      sec.items = r.items;
      sec.recording = r.recording;
      if (o.original !== undefined) sec.original = o.original;
    } else if (kind === 'graphics' || kind === 'sound' || kind === 'conversations') sec.items = parseBullets(cur.lines, false).items;
    else if (kind === 'captions') sec.items = parseCaptions(cur.lines);
    else {
      const t = trimBlank(cur.lines.map((l) => l.text));
      if (t) sec.items = [{ kind: 'markdown', text: t }];
    }
    doc.sections.push(sec);
  };
  // Inside the original script's fence (### Original script, D86) every line is the script's own.
  let awaitFence = false;
  let fence = '';
  for (let i = start; i < all.length; i++) {
    const l = all[i]!;
    if (fence) {
      if (l === fence) fence = '';
      if (cur) cur.lines.push({ text: l, line: i + 1 });
      continue;
    }
    if (awaitFence && l.trim() !== '') {
      awaitFence = false;
      const f = /^(`{3,})[A-Za-z0-9_-]*$/.exec(l);
      if (f) fence = f[1]!;
    }
    if (cur && /^###\s+(?:the\s+|your\s+)?original(?:\s+script)?\s*$/i.test(l.trim())) awaitFence = true;
    const h = /^##\s+(.+?)\s*#*\s*$/.exec(l);
    if (h) {
      close();
      cur = { title: h[1]!, lines: [] };
      continue;
    }
    if (cur) cur.lines.push({ text: l, line: i + 1 });
    else if (!/^#\s/.test(l)) treatment.push(l);
    else if (!doc.title) doc.title = l.replace(/^#\s+/, '').trim();
  }
  close();
  doc.treatment = trimBlank(treatment);
  return doc;
}

/** Times in a sheet (`0:11.6`, `1:02:03.5`) as seconds. */
export function sheetSeconds(s: string): number | undefined {
  const m = /^(?:(\d+):)?(\d+):(\d{2}(?:\.\d+)?)$/.exec(s.trim());
  if (!m) return undefined;
  return (m[1] ? Number(m[1]) * 3600 : 0) + Number(m[2]) * 60 + Number(m[3]);
}

/** A graphics line that uses one of the project's own motion graphics (D68). */
export interface MotionUse {
  /** The graphic's folder name: `route-map`. */
  graphic: string;
  /** Its version as the line writes it: 8 to 64 hex characters of the SHA-256. */
  version: string;
  /** The rest of the line's first part, as written: `at 0:12 for 4 s`. */
  when: string[];
}

/**
 * `motion route-map@3fa9c2d1 at 0:12 for 4 s` (also `motion: route-map@…` and a
 * bare `route-map@…`, which the Go parser accepts too) → the graphic, its
 * version and the timing. Undefined for a template line.
 */
export function motionUse(head: string): MotionUse | undefined {
  const m = /^(?:motion:?\s+)?([a-z][a-z0-9-]*)@([0-9a-f]{8,64})(?:\s+(.*))?$/.exec(head.trim());
  if (!m) return undefined;
  return { graphic: m[1]!, version: m[2]!, when: m[3] ? m[3].trim().split(/\s+/) : [] };
}

/** One `motion:` setting: the version a cut uses and what MetaRush wrote about it. */
export interface MotionPin {
  /** The full version (64 hex characters). */
  hash: string;
  /** "approved by Ada · 18 Oct 2026", "waiting for a person to approve it", "approval withdrawn"… */
  note?: string;
}

/** The `motion:` settings: each graphic's pinned version and the note written after it. */
export function motionPins(doc: ScriptDoc): Map<string, MotionPin> {
  const out = new Map<string, MotionPin>();
  const block = doc.settings.find((s) => ['motion', 'motion graphics'].includes(s.key.toLowerCase()));
  for (const c of block?.children ?? []) {
    const h = /^(?:sha256:)?([0-9a-f]{64})$/.exec(c.value.trim());
    if (h) out.set(c.key, { hash: h[1]!, note: c.comment });
  }
  return out;
}

export type MotionState = 'approved' | 'waiting' | 'blocked' | 'unknown';

/** Where a motion graphic stands, from the note MetaRush writes after its version. */
export function motionState(note: string | undefined): MotionState {
  const n = (note ?? '').trim().toLowerCase();
  if (!n) return 'unknown';
  if (n.startsWith('approved')) return 'approved';
  if (n.startsWith('waiting')) return 'waiting';
  if (/turned down|withdrawn|not checked/.test(n)) return 'blocked';
  return 'unknown';
}

/** "lower-third.clean" → "Lower third, clean". */
/** The slide family reads as "Code slide", like the cut sheet's own descriptions (cutsheet.templateName). */
const SLIDE_NAMES: Record<string, string> = {
  title: 'Title slide',
  bullets: 'Bullet slide',
  code: 'Code slide',
  architecture: 'Architecture slide',
  screenshot: 'Screenshot slide',
  quote: 'Quote slide',
};

export function templateName(id: string): string {
  const [base = id, variant] = id.split('.');
  if (base === 'slide' && variant && SLIDE_NAMES[variant]) return SLIDE_NAMES[variant];
  const words = base.replace(/[-_]+/g, ' ').trim();
  const nice = words.charAt(0).toUpperCase() + words.slice(1);
  return variant ? `${nice}, ${variant.replace(/[-_]+/g, ' ')}` : nice;
}

/** `with name "Ada", title "Builder"` → [["name","Ada"],["title","Builder"]] (kept as written when it isn't that shape). */
export function withFields(s: string): [string, string][] | undefined {
  const m = /^with\s+(.*)$/.exec(s.trim());
  if (!m) return undefined;
  const out: [string, string][] = [];
  const re = /\s*([A-Za-z][\w ]*?)\s+("(?:[^"\\]|\\.)*"|\[[^\]]*\]|[^,]+)\s*(?:,|$)/y;
  const src = m[1]!;
  let at = 0;
  while (at < src.length) {
    re.lastIndex = at;
    const f = re.exec(src);
    if (!f) return undefined;
    let v = f[2]!.trim();
    if (v.startsWith('"')) v = unquoteNote(v);
    else if (v.startsWith('[')) {
      try {
        const arr: unknown = JSON.parse(v);
        if (Array.isArray(arr)) v = arr.map(String).join(', ');
      } catch {
        // kept as written
      }
    }
    out.push([f[1]!, v]);
    at = re.lastIndex;
  }
  return out;
}
