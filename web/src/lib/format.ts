/** 0:12.345 / 1:02:03.004 (source-seconds timecode, matches the CLI's M:SS.mmm labels). */
export function timecode(seconds: number | undefined | null, opts: { ms?: boolean } = {}): string {
  if (seconds == null || !Number.isFinite(seconds)) return '-:--';
  const neg = seconds < 0;
  let s = Math.abs(seconds);
  const showMs = opts.ms ?? true;
  if (!showMs) s = Math.floor(s);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const secStr = showMs ? sec.toFixed(3).padStart(6, '0') : String(Math.floor(sec)).padStart(2, '0');
  const body = h > 0 ? `${h}:${String(m).padStart(2, '0')}:${secStr}` : `${m}:${secStr}`;
  return neg ? `-${body}` : body;
}

/** Human duration: 9.8s, 1m 04s, 1h 02m. */
/** A moment as m:ss, with tenths when it isn't on a whole second ("0:12", "0:00.3"). */
export function clock(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null || !Number.isFinite(seconds)) return '0:00';
  const t = Math.max(0, Math.round(seconds * 10) / 10);
  const whole = Math.floor(t);
  const tenth = Math.round((t - whole) * 10);
  const s = `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, '0')}`;
  return tenth ? `${s}.${tenth}` : s;
}

export function duration(seconds: number | undefined | null): string {
  if (seconds == null || !Number.isFinite(seconds)) return '-';
  if (seconds < 59.95) {
    const v = Math.round(seconds * 10) / 10;
    return `${Number.isInteger(v) ? v : v.toFixed(1)}s`;
  }
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m ${String(Math.round(seconds % 60)).padStart(2, '0')}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
}

export function signedDuration(delta: number): string {
  if (!Number.isFinite(delta) || Math.abs(delta) < 0.005) return '±0.0s';
  return `${delta > 0 ? '+' : '-'}${Math.abs(delta).toFixed(1)}s`;
}

export function bytes(n: number | undefined | null): string {
  if (n == null || !Number.isFinite(n)) return '-';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

const rtf = typeof Intl !== 'undefined' ? new Intl.RelativeTimeFormat('en', { numeric: 'auto', style: 'narrow' }) : null;

/** "12s ago", "3 min ago", "yesterday". */
export function relative(when: string | number | Date | undefined | null, now = Date.now()): string {
  if (when == null) return '-';
  const t = typeof when === 'number' ? when : new Date(when).getTime();
  if (!Number.isFinite(t) || t <= 0) return '-';
  const diff = (t - now) / 1000;
  const abs = Math.abs(diff);
  if (abs < 5) return 'just now';
  if (abs < 60) return diff < 0 ? `${Math.round(abs)}s ago` : `in ${Math.round(abs)}s`;
  if (!rtf) return new Date(t).toLocaleString();
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day');
  return new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
}

export function absolute(when: string | undefined | null): string {
  if (!when) return '';
  const d = new Date(when);
  if (!Number.isFinite(d.getTime())) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

/**
 * When something was made or added, readable at a glance (D188): "Today 3:04 PM",
 * "Yesterday 9:12 AM", "Mon, Oct 20, 3:04 PM", with the year when it isn't this
 * year. Shown as text, not only on hover; pair it with absolute() as the title.
 */
export function stamp(when: string | number | Date | undefined | null, now: number | Date = Date.now()): string {
  if (when == null) return '';
  const d = when instanceof Date ? when : new Date(when);
  if (!Number.isFinite(d.getTime()) || d.getTime() <= 0) return '';
  const today = new Date(now);
  const time = d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((startOf(today) - startOf(d)) / 86_400_000);
  if (days === 0) return `Today ${time}`;
  if (days === 1) return `Yesterday ${time}`;
  const sameYear = d.getFullYear() === today.getFullYear();
  return d.toLocaleString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    ...(sameYear ? {} : { year: 'numeric' }),
    hour: 'numeric',
    minute: '2-digit',
  });
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/**
 * The article before a number as it is said aloud: "an 8-second shot", "an 11-…", "an 18-…", "an 80-…",
 * also when they start a larger number ("an 800-…", "an 8000-…"); "a 6-second shot", "a 100-…" otherwise.
 * Go's rule is the same (plainwords.AOrAn); both are tested against internal/plainwords/testdata/articles.json.
 */
export function aOrAn(n: number): 'a' | 'an' {
  if (!Number.isFinite(n)) return 'a'; // never loops on Infinity
  let x = Math.trunc(n);
  while (x >= 1000) x = Math.trunc(x / 1000);
  return x === 8 || x === 11 || x === 18 || (x >= 80 && x <= 89) || (x >= 800 && x <= 899) ? 'an' : 'a';
}

export function shortId(id: string | undefined): string {
  if (!id) return '';
  const i = id.indexOf('_');
  const body = i >= 0 ? id.slice(i + 1) : id;
  return body.slice(-6);
}

/** "1920×1080 · 30 fps" */
export function resolution(w?: number, h?: number, fps?: number): string {
  const parts: string[] = [];
  if (w && h) parts.push(`${w}×${h}`);
  if (fps) parts.push(`${Number.isInteger(fps) ? fps : fps.toFixed(2)} fps`);
  return parts.join(' / ');
}

/** An output preset as people say it: "vertical-1080p" → "Vertical, 1080p". */
export function presetLabel(preset: string | undefined): string {
  if (!preset) return '';
  const [shape = preset, ...size] = preset.split('-');
  const nice = shape.charAt(0).toUpperCase() + shape.slice(1);
  return size.length ? `${nice}, ${size.join(' ')}` : nice;
}

const JOB_LABELS: Record<string, string> = {
  proxy: 'Preview copy',
  thumbnail: 'Thumbnail',
  contact_sheet: 'Frame grid',
  waveform: 'Waveform',
  mezzanine: 'Steady copy',
  evidence: 'Scene analysis',
  transcribe: 'Transcript',
  render: 'Render',
  qa: 'Quality check',
  diarize: 'Telling voices apart',
  voiceover: 'Narration',
  import: 'Import from a link',
  visual: "What's on screen",
  describe: 'Scene descriptions',
  fetch: 'Repository or web page',
  storage_move: 'Moving the media',
  'resource-upload': 'Saving a logo or picture',
};

/** A kind of job in words: "mezzanine" → "Steady copy". */
export function jobLabel(type: string): string {
  return JOB_LABELS[type] ?? titleCase(type);
}

/** A folder as people say it: "/Users/you/Videos/MetaRush" → "Videos › MetaRush" (the full path stays in Details). */
export function folderLabel(dir: string | undefined): string {
  if (!dir) return '';
  const parts = dir.replace(/^\/(?:Users|home)\/[^/]+/, '').split(/[\\/]/).filter(Boolean);
  if (!parts.length) return 'your home folder';
  return parts.slice(-2).join(' › ');
}

/** "darwin"/"arm64" → "Mac, Apple chip" (the exact values stay in Details). */
export function platformLabel(os: string, arch: string): string {
  const system = ({ darwin: 'Mac', linux: 'Linux', windows: 'Windows' } as Record<string, string>)[os] ?? os;
  const chip = os === 'darwin' ? (arch === 'arm64' ? 'Apple chip' : 'Intel') : arch === 'amd64' ? 'Intel or AMD' : arch === 'arm64' ? 'ARM' : arch;
  return chip ? `${system}, ${chip}` : system;
}

export function orientation(w?: number, h?: number): 'vertical' | 'landscape' | 'square' | undefined {
  if (!w || !h) return undefined;
  if (Math.abs(w - h) / Math.max(w, h) < 0.05) return 'square';
  return h > w ? 'vertical' : 'landscape';
}

export function titleCase(s: string): string {
  return s.replace(/[_.-]+/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

/** "paused: battery at 18%" -> "Paused: battery at 18%". */
export function capFirst(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}

/** Where a change came from, for "Ada via …": "Ada via Claude Code or the terminal". */
export function viaLabel(via: string | undefined, origin?: string, agentName?: string): string {
  // A change Claude suggested in the dashboard and a person applied.
  if (origin?.startsWith('assistant:')) return 'a change Claude suggested, applied in the dashboard';
  // An AI app that named itself (D248): Codex, Cursor… and how it reached MetaRush.
  if (agentName) return via === 'mcp' ? `${agentName}, through MetaRush's tools for AI apps` : via === 'cli' ? `${agentName}, from the command line` : agentName;
  // Gestures on the studio timeline, saved in the background.
  if (origin?.startsWith('timeline:')) return 'the timeline';
  // A run of Recipes, as the person who started it (D204).
  if (origin?.startsWith('recipe:')) return 'a recipe run';
  switch (via) {
    case 'assistant':
      return 'Claude Code in the dashboard';
    case 'cli':
      return 'Claude Code or the terminal';
    case 'dashboard':
      return 'the dashboard';
    case 'watch':
      return 'a watched folder';
    case 'extension':
      return 'the browser extension';
    case 'worker':
      return 'MetaRush';
    case 'mcp':
      return 'another AI app';
    case 'recipe': // the Recipes channel (models.ViaRecipe)
      return 'a recipe run'; // Recipes
    default:
      return via ? titleCase(via) : 'somewhere unknown';
  }
}

/** Tokens as people read them: 62.5M, 480K (D181). */
export function tokenWords(n: number): string {
  if (n >= 10_000_000) return `${Math.round(n / 1e6)}M`;
  if (n >= 1_000_000) return `${(n / 1e6).toFixed(1).replace(/\.0$/, '')}M`;
  if (n >= 1_000) return `${Math.round(n / 1e3)}K`;
  return String(n);
}

/** Up to two letters for a person's round badge ("Maya Lindqvist" → "ML"; words without letters are skipped). */
export function initials(name: string): string {
  const words = name
    .trim()
    .split(/\s+/)
    .map((w) => w.match(/[\p{L}\p{N}]/u)?.[0] ?? '')
    .filter(Boolean);
  if (words.length === 0) return '?';
  return (words[0]! + (words.length > 1 ? words[words.length - 1]! : '')).toUpperCase();
}
