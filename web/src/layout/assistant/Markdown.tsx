import { Fragment, memo, useState, type ReactNode } from 'react';
import { CaretDown, CaretRight } from '@phosphor-icons/react';
import { CopyButton, cx } from '../../ui/primitives';

/*
 * A small, safe Markdown renderer for Claude's replies: headings, paragraphs,
 * nested bullet/numbered/task lists, block quotes, tables, rules, fenced code
 * with syntax highlighting, and inline code, bold, italic, strike-through and
 * links. Everything becomes React elements; raw HTML is shown as text, and
 * links are limited to http(s), mailto and dashboard routes.
 */

type Block =
  | { kind: 'heading'; level: number; text: string }
  | { kind: 'para'; lines: string[] }
  | { kind: 'code'; lang: string; text: string }
  | { kind: 'quote'; blocks: Block[] }
  | { kind: 'list'; ordered: boolean; start: number; items: ListItem[] }
  | { kind: 'table'; head: string[]; align: ('left' | 'right' | 'center' | undefined)[]; rows: string[][] }
  | { kind: 'rule' };

interface ListItem {
  task?: boolean;
  done?: boolean;
  blocks: Block[];
}

const fenceRe = /^(\s*)(`{3,}|~{3,})\s*([\w+#.-]*)\s*$/;
const headingRe = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const ruleRe = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const quoteRe = /^\s{0,3}>\s?(.*)$/;
const itemRe = /^(\s*)([-*+]|\d{1,4}[.)])\s+(.*)$/;
const tableSepRe = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;

function indentOf(s: string): number {
  let n = 0;
  for (const ch of s) {
    if (ch === ' ') n++;
    else if (ch === '\t') n += 4;
    else break;
  }
  return n;
}

function splitRow(line: string): string[] {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|')) s = s.slice(0, -1);
  return s.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, '|'));
}

export function parseBlocks(text: string): Block[] {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const out: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.trim() === '') {
      i++;
      continue;
    }
    const fence = fenceRe.exec(line);
    if (fence) {
      const marker = fence[2]!;
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i]!.trim().startsWith(marker)) body.push(lines[i++]!);
      i++; // closing fence (or end of text while streaming)
      out.push({ kind: 'code', lang: (fence[3] ?? '').toLowerCase(), text: body.join('\n') });
      continue;
    }
    const h = headingRe.exec(line);
    if (h) {
      out.push({ kind: 'heading', level: h[1]!.length, text: h[2]! });
      i++;
      continue;
    }
    if (ruleRe.test(line)) {
      out.push({ kind: 'rule' });
      i++;
      continue;
    }
    if (quoteRe.test(line)) {
      const inner: string[] = [];
      while (i < lines.length && lines[i]!.trim() !== '' && quoteRe.test(lines[i]!)) inner.push(quoteRe.exec(lines[i++]!)![1]!);
      out.push({ kind: 'quote', blocks: parseBlocks(inner.join('\n')) });
      continue;
    }
    if (line.includes('|') && i + 1 < lines.length && tableSepRe.test(lines[i + 1]!)) {
      const head = splitRow(line);
      const align = splitRow(lines[i + 1]!).map((c) => (c.startsWith(':') && c.endsWith(':') ? 'center' : c.endsWith(':') ? 'right' : c.startsWith(':') ? 'left' : undefined));
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && lines[i]!.includes('|') && lines[i]!.trim() !== '') rows.push(splitRow(lines[i++]!));
      out.push({ kind: 'table', head, align, rows });
      continue;
    }
    const item = itemRe.exec(line);
    if (item) {
      const [list, next] = parseList(lines, i);
      out.push(list);
      i = next;
      continue;
    }
    const para: string[] = [];
    while (i < lines.length) {
      const l = lines[i]!;
      if (l.trim() === '' || fenceRe.test(l) || headingRe.test(l) || ruleRe.test(l) || quoteRe.test(l) || (para.length > 0 && itemRe.test(l))) break;
      if (l.includes('|') && i + 1 < lines.length && tableSepRe.test(lines[i + 1]!)) break;
      para.push(l);
      i++;
    }
    if (para.length) out.push({ kind: 'para', lines: para });
    else i++;
  }
  return out;
}

function parseList(lines: string[], start: number): [Block, number] {
  const first = itemRe.exec(lines[start]!)!;
  const baseIndent = indentOf(first[1]!);
  const ordered = /\d/.test(first[2]!);
  const items: ListItem[] = [];
  let i = start;
  let cur: string[] | null = null;
  const flush = () => {
    if (!cur) return;
    let text = cur.join('\n');
    let task: boolean | undefined;
    let done: boolean | undefined;
    const t = /^\[( |x|X)\]\s+/.exec(text);
    if (t) {
      task = true;
      done = t[1] !== ' ';
      text = text.slice(t[0].length);
    }
    items.push({ task, done, blocks: parseBlocks(text) });
    cur = null;
  };
  while (i < lines.length) {
    const l = lines[i]!;
    const m = itemRe.exec(l);
    if (m && indentOf(m[1]!) <= baseIndent + 1 && /\d/.test(m[2]!) === ordered) {
      flush();
      cur = [m[3]!];
      i++;
      continue;
    }
    if (m && indentOf(m[1]!) <= baseIndent + 1) break; // a different list type at the same level
    if (l.trim() === '') {
      // A blank line ends the list unless the next line continues it (indented).
      const nextLine = lines[i + 1];
      if (nextLine !== undefined && (indentOf(nextLine) > baseIndent || (itemRe.test(nextLine) && indentOf(itemRe.exec(nextLine)![1]!) <= baseIndent + 1))) {
        cur?.push('');
        i++;
        continue;
      }
      break;
    }
    if (indentOf(l) > baseIndent && cur) {
      cur.push(l.slice(Math.min(indentOf(l), baseIndent + 2)));
      i++;
      continue;
    }
    if (cur && !itemRe.test(l) && !fenceRe.test(l) && !headingRe.test(l)) {
      cur.push(l.trim()); // lazy continuation
      i++;
      continue;
    }
    break;
  }
  flush();
  return [{ kind: 'list', ordered, start: ordered ? parseInt(first[2]!, 10) || 1 : 1, items }, i];
}

/* ---------- Inline ---------- */

const inlineRe = /(`+)([\s\S]*?[^`])\1(?!`)|\*\*([^*\n]+?)\*\*|__([^_\n]+?)__|~~([^~\n]+?)~~|(?<![\w*])\*(?!\s)([^*\n]+?)(?<!\s)\*(?![\w*])|(?<![\w_])_(?!\s)([^_\n]+?)(?<!\s)_(?![\w_])|\[([^\]\n]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)|(https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g;

/** Only these links are made clickable. */
export function safeHref(href: string): string | undefined {
  const h = href.trim();
  if (/^https?:\/\//i.test(h) || /^mailto:/i.test(h)) return h;
  if (/^\/(p|renders|settings)(\/|$|\?)/.test(h)) return h;
  return undefined;
}

export function Inline({ text }: { text: string }): ReactNode {
  const parts: ReactNode[] = [];
  let last = 0;
  let k = 0;
  inlineRe.lastIndex = 0;
  for (let m = inlineRe.exec(text); m; m = inlineRe.exec(text)) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const key = k++;
    if (m[1]) {
      parts.push(
        <code key={key} className="tc rounded-[5px] bg-hover px-[0.3em] py-[0.08em] text-[0.88em] text-fg">
          {m[2]!.trim() === '' ? m[2] : m[2]!.replace(/^ (.*) $/, '$1')}
        </code>,
      );
    } else if (m[3] || m[4]) {
      parts.push(
        <strong key={key} className="font-semibold text-fg">
          <Inline text={(m[3] ?? m[4])!} />
        </strong>,
      );
    } else if (m[5]) {
      parts.push(
        <del key={key} className="text-muted">
          <Inline text={m[5]} />
        </del>,
      );
    } else if (m[6] || m[7]) {
      parts.push(
        <em key={key}>
          <Inline text={(m[6] ?? m[7])!} />
        </em>,
      );
    } else if (m[8]) {
      const href = safeHref(m[9]!);
      parts.push(
        href ? (
          <a key={key} href={href} target={href.startsWith('/') ? undefined : '_blank'} rel="noreferrer noopener" className="text-accent-text underline decoration-accent-line underline-offset-2 hover:decoration-accent-text">
            <Inline text={m[8]} />
          </a>
        ) : (
          <span key={key}>
            <Inline text={m[8]} />
          </span>
        ),
      );
    } else if (m[10]) {
      parts.push(
        <a key={key} href={m[10]} target="_blank" rel="noreferrer noopener" className="break-all text-accent-text underline decoration-accent-line underline-offset-2">
          {m[10]}
        </a>,
      );
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

/* ---------- Code highlighting ---------- */

type Tok = { t: string; c?: 'kw' | 'str' | 'num' | 'com' | 'key' | 'flag' | 'fn' | 'punct' | 'lit' };

const langRules: Record<string, RegExp> = {
  json: /(?<key>"(?:[^"\\]|\\.)*"(?=\s*:))|(?<str>"(?:[^"\\]|\\.)*")|(?<num>-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|(?<lit>\b(?:true|false|null)\b)|(?<punct>[{}[\],:])/g,
  sh: /(?<com>#[^\n]*)|(?<str>"(?:[^"\\]|\\.)*"|'[^']*')|(?<flag>(?<=\s|^)--?[\w-]+)|(?<kw>\b(?:if|then|else|fi|for|do|done|while|case|esac|export|echo|cd)\b)|(?<num>\b\d+(?:\.\d+)?\b)|(?<fn>(?<=^|\n|\|\s?|&&\s?)[\w./-]+)/g,
  js: /(?<com>\/\/[^\n]*|\/\*[\s\S]*?\*\/)|(?<str>`(?:[^`\\]|\\.)*`|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|(?<kw>\b(?:const|let|var|function|return|if|else|for|while|import|export|from|class|new|async|await|type|interface|extends|default|switch|case|break|throw|try|catch)\b)|(?<lit>\b(?:true|false|null|undefined)\b)|(?<num>\b\d+(?:\.\d+)?\b)|(?<fn>\b[A-Za-z_$][\w$]*(?=\())/g,
  go: /(?<com>\/\/[^\n]*|\/\*[\s\S]*?\*\/)|(?<str>`[^`]*`|"(?:[^"\\]|\\.)*")|(?<kw>\b(?:func|package|import|return|if|else|for|range|var|const|type|struct|interface|map|chan|go|defer|switch|case|default|break|continue)\b)|(?<lit>\b(?:true|false|nil)\b)|(?<num>\b\d+(?:\.\d+)?\b)|(?<fn>\b[A-Za-z_]\w*(?=\())/g,
  py: /(?<com>#[^\n]*)|(?<str>"""[\s\S]*?"""|'''[\s\S]*?'''|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|(?<kw>\b(?:def|class|return|if|elif|else|for|while|import|from|as|with|try|except|raise|lambda|in|not|and|or)\b)|(?<lit>\b(?:True|False|None)\b)|(?<num>\b\d+(?:\.\d+)?\b)|(?<fn>\b[A-Za-z_]\w*(?=\())/g,
  yaml: /(?<com>#[^\n]*)|(?<key>^[ \t-]*[\w.-]+(?=\s*:))|(?<str>"(?:[^"\\]|\\.)*"|'[^']*')|(?<lit>\b(?:true|false|null)\b)|(?<num>\b\d+(?:\.\d+)?\b)/gm,
};
const langAlias: Record<string, string> = {
  bash: 'sh', shell: 'sh', zsh: 'sh', console: 'sh', ts: 'js', tsx: 'js', jsx: 'js', javascript: 'js', typescript: 'js', golang: 'go',
  python: 'py', yml: 'yaml', jsonc: 'json', json5: 'json',
};

export function tokenize(code: string, lang: string): Tok[] {
  let l = langAlias[lang] ?? lang;
  if (!langRules[l]) {
    const t = code.trim();
    l = /^[[{]/.test(t) ? 'json' : /^(\$ |metarush |npm |go |git |cd |ls )/m.test(t) ? 'sh' : '';
  }
  const re = langRules[l];
  if (!re || code.length > 40_000) return [{ t: code }];
  const out: Tok[] = [];
  let last = 0;
  re.lastIndex = 0;
  for (let m = re.exec(code); m; m = re.exec(code)) {
    if (m[0] === '') {
      re.lastIndex++;
      continue;
    }
    if (m.index > last) out.push({ t: code.slice(last, m.index) });
    const g = m.groups ?? {};
    const c = (Object.keys(g).find((k) => g[k] !== undefined) as Tok['c']) ?? undefined;
    out.push({ t: m[0], c });
    last = m.index + m[0].length;
  }
  if (last < code.length) out.push({ t: code.slice(last) });
  return out;
}

const tokClass: Record<NonNullable<Tok['c']>, string> = {
  kw: 'text-[#e8b169]',
  str: 'text-[#b9d39a]',
  num: 'text-[#f0b48a]',
  com: 'text-[#8f877e] italic',
  key: 'text-[#a9c4e8]',
  flag: 'text-[#a9c4e8]',
  fn: 'text-[#e9dcc8]',
  punct: 'text-[#9a9086]',
  lit: 'text-[#d7a8d8]',
};

/** Code longer than this folds away behind "See code (N lines)" (D186): replies are for a creator. */
const FOLD_LINES = 6;

function CodeBlock({ lang, text }: { lang: string; text: string }) {
  const lines = text.replace(/\n+$/, '').split('\n').length;
  const [open, setOpen] = useState(false);
  if (lines > FOLD_LINES && !open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-expanded={false}
        className="flex items-center gap-1.5 rounded-[9px] bg-panel px-2.5 py-1.5 text-[12px] font-medium text-muted hairline hover:bg-hover hover:text-fg"
        data-code-fold={lines}
      >
        <CaretRight size={11} aria-hidden />
        See code ({lines} lines)
      </button>
    );
  }
  return (
    <div className="space-y-1">
      {lines > FOLD_LINES ? (
        <button type="button" onClick={() => setOpen(false)} aria-expanded className="flex items-center gap-1.5 text-[12px] font-medium text-muted hover:text-fg" data-code-fold={lines}>
          <CaretDown size={11} aria-hidden />
          Hide the code
        </button>
      ) : null}
      <CodeBody lang={lang} text={text} />
    </div>
  );
}

function CodeBody({ lang, text }: { lang: string; text: string }) {
  const toks = tokenize(text, lang);
  return (
    <div className="group/code relative">
      <pre className="tc overflow-x-auto rounded-[10px] bg-inset px-3.5 py-3 text-[12px] leading-[1.6] text-[#ece4d9]">
        <code>
          {toks.map((t, i) => (t.c ? <span key={i} className={tokClass[t.c]}>{t.t}</span> : <Fragment key={i}>{t.t}</Fragment>))}
        </code>
      </pre>
      <div className="absolute right-1.5 top-1.5 opacity-0 transition-opacity group-hover/code:opacity-100 focus-within:opacity-100">
        <CopyButton text={text} label="Copy" className="!h-6 !px-2 !text-[11px]" />
      </div>
    </div>
  );
}

/* ---------- Blocks ---------- */

function BlockView({ b, cursor }: { b: Block; cursor?: boolean }): ReactNode {
  switch (b.kind) {
    case 'heading': {
      const cls = b.level <= 2 ? 'text-[14.5px] font-semibold tracking-[-0.01em]' : 'text-[13.5px] font-semibold';
      return (
        <div role="heading" aria-level={Math.min(6, b.level + 2)} className={cx('pt-1 text-fg', cls)}>
          <Inline text={b.text} />
        </div>
      );
    }
    case 'para':
      return (
        <p className="break-words">
          {b.lines.map((l, i) => (
            <Fragment key={i}>
              {i > 0 ? <br /> : null}
              <Inline text={l.replace(/\s+$/, '')} />
            </Fragment>
          ))}
          {cursor ? <span className="mr-cursor" aria-hidden /> : null}
        </p>
      );
    case 'code':
      return <CodeBlock lang={b.lang} text={b.text} />;
    case 'quote':
      return (
        <blockquote className="space-y-2 border-l-2 border-line-strong pl-3 text-muted">
          {b.blocks.map((x, i) => (
            <BlockView key={i} b={x} />
          ))}
        </blockquote>
      );
    case 'rule':
      return <hr className="border-line" />;
    case 'table':
      return (
        <div className="overflow-x-auto rounded-[10px] hairline">
          <table className="w-full border-collapse text-[12.5px]">
            <thead>
              <tr className="bg-hover/60">
                {b.head.map((h, i) => (
                  <th key={i} className="px-2.5 py-1.5 text-left font-medium text-muted" style={{ textAlign: b.align[i] }}>
                    <Inline text={h} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {b.rows.map((r, ri) => (
                <tr key={ri} className="border-t border-line">
                  {b.head.map((_, ci) => (
                    <td key={ci} className="px-2.5 py-1.5 align-top text-fg" style={{ textAlign: b.align[ci] }}>
                      <Inline text={r[ci] ?? ''} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    case 'list': {
      const List = b.ordered ? 'ol' : 'ul';
      return (
        <List start={b.ordered && b.start !== 1 ? b.start : undefined} className={cx('space-y-1', b.ordered ? 'list-decimal pl-5 marker:text-subtle' : 'pl-0')}>
          {b.items.map((it, i) => (
            <li key={i} className={b.ordered ? 'pl-0.5' : 'flex gap-2'}>
              {b.ordered ? null : it.task ? (
                <span
                  aria-hidden
                  className={cx('mt-[5px] grid h-3.5 w-3.5 shrink-0 place-items-center rounded-[4px] text-[9px]', it.done ? 'bg-ok-soft text-ok' : 'hairline')}
                >
                  {it.done ? '✓' : ''}
                </span>
              ) : (
                <span className="mt-[9px] h-1 w-1 shrink-0 rounded-full bg-subtle" aria-hidden />
              )}
              <div className="min-w-0 flex-1 space-y-1.5">
                {it.task ? <span className="sr-only">{it.done ? 'Done: ' : 'To do: '}</span> : null}
                {it.blocks.map((x, j) => (
                  <BlockView key={j} b={x} />
                ))}
              </div>
            </li>
          ))}
        </List>
      );
    }
  }
}

/** Claude's prose as formatted text. `streaming` adds a cursor at the end. */
export const Markdown = memo(function Markdown({ text, streaming, className }: { text: string; streaming?: boolean; className?: string }) {
  const blocks = parseBlocks(text);
  const lastIsPara = blocks.length > 0 && blocks[blocks.length - 1]!.kind === 'para';
  return (
    <div className={cx('space-y-2.5 text-[13.5px] leading-[1.65] text-fg', className)}>
      {blocks.map((b, i) => (
        <BlockView key={i} b={b} cursor={streaming && lastIsPara && i === blocks.length - 1} />
      ))}
      {streaming && !lastIsPara ? <span className="mr-cursor" aria-hidden /> : null}
    </div>
  );
});
