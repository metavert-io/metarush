import { useMemo, useState } from 'react';
import { CaretUpDown } from '@phosphor-icons/react';
import type { CutDiffLine } from '../../../api/types';
import { cx } from '../../../ui/primitives';

type Row = { kind: 'line'; line: CutDiffLine } | { kind: 'gap'; id: number; lines: CutDiffLine[] };

/** Unchanged stretches longer than this collapse into an expandable row. */
const CONTEXT = 3;

function rows(lines: CutDiffLine[], open: Set<number>): Row[] {
  const changed = lines.map((l) => l.op !== 'same');
  const near = lines.map((_, i) => {
    for (let k = Math.max(0, i - CONTEXT); k <= Math.min(lines.length - 1, i + CONTEXT); k++) if (changed[k]) return true;
    return false;
  });
  const out: Row[] = [];
  let gap: CutDiffLine[] = [];
  let gapID = 0;
  const flush = () => {
    if (!gap.length) return;
    if (open.has(gapID) || gap.length <= 2) gap.forEach((line) => out.push({ kind: 'line', line }));
    else out.push({ kind: 'gap', id: gapID, lines: gap });
    gap = [];
    gapID++;
  };
  lines.forEach((l, i) => {
    if (near[i]) {
      flush();
      out.push({ kind: 'line', line: l });
    } else gap.push(l);
  });
  flush();
  return out;
}

/** The lines of two cut sheets side by side in one column: removed, added and nearby unchanged lines. */
export function SheetDiff({ lines, className }: { lines: CutDiffLine[]; className?: string }) {
  const [open, setOpen] = useState<Set<number>>(new Set());
  const list = useMemo(() => rows(lines, open), [lines, open]);
  if (!lines.some((l) => l.op !== 'same'))
    return <div className={cx('rounded-[12px] px-4 py-5 text-[13px] text-muted hairline', className)}>The two scripts are the same, line for line.</div>;
  return (
    <div className={cx('overflow-hidden rounded-[12px] bg-panel hairline', className)} role="table" aria-label="Changed lines">
      {list.map((r, i) =>
        r.kind === 'gap' ? (
          <button
            key={`gap-${r.id}`}
            type="button"
            onClick={() => setOpen((s) => new Set(s).add(r.id))}
            className="flex w-full items-center gap-2 border-y border-line bg-raised px-3 py-1.5 text-left text-[12px] text-subtle transition-colors hover:bg-hover hover:text-fg"
          >
            <CaretUpDown size={12} /> {r.lines.length} unchanged lines
          </button>
        ) : (
          <div
            key={i}
            role="row"
            className={cx(
              'tc grid grid-cols-[2.2rem_minmax(0,1fr)] text-[12.5px] leading-[1.7] sm:grid-cols-[2.6rem_2.6rem_minmax(0,1fr)]',
              r.line.op === 'added' && 'bg-ok-soft',
              r.line.op === 'removed' && 'bg-danger-soft',
            )}
          >
            <span role="cell" className="hidden select-none pr-2 text-right text-[11px] text-subtle sm:block">{r.line.left ?? ''}</span>
            <span role="cell" className="select-none pr-2 text-right text-[11px] text-subtle">{r.line.right ?? r.line.left ?? ''}</span>
            <span role="cell" className="flex min-w-0 gap-2 pr-3">
              <span aria-hidden className={cx('w-3 shrink-0 select-none', r.line.op === 'added' ? 'text-ok' : r.line.op === 'removed' ? 'text-danger' : 'text-subtle')}>
                {r.line.op === 'added' ? '+' : r.line.op === 'removed' ? '-' : ''}
              </span>
              <span className="sr-only">{r.line.op === 'added' ? 'Added: ' : r.line.op === 'removed' ? 'Removed: ' : ''}</span>
              <span className={cx('min-w-0 whitespace-pre-wrap break-words', r.line.op === 'same' ? 'text-muted' : 'text-fg')}>{r.line.text || ' '}</span>
            </span>
          </div>
        ),
      )}
    </div>
  );
}
