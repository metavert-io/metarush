import { useEffect, useRef } from 'react';
import { CaretDown } from '@phosphor-icons/react';
import { cx } from '../../ui/primitives';
import { CUE_LEAD, promptAt, type PromptLine } from '../studio/voiceover/prompter';

/*
 * The teleprompter while recording the screen (D261): the script scrolls so
 * the line to say sits near the top, large; the next one waits below and
 * counts down for its last two seconds. Lines come one after another at the
 * narrator's pace on the recording's clock (prompter.ts schedule), which
 * stops while paused; "Next line" (or Space, an arrow key or a clicker when
 * this window has focus) brings the next one in early.
 */

export function ScreenPrompter({ lines, due, time, recording, paused, onNext, compact }: {
  lines: PromptLine[];
  due: number[];
  /** Seconds recorded (pauses left out). */
  time: number;
  recording: boolean;
  paused: boolean;
  onNext: () => void;
  /** The floating window: smaller type, no hints. */
  compact?: boolean;
}) {
  const p = promptAt(due, time);
  const box = useRef<HTMLDivElement>(null);
  const cur = recording ? p.current : -1;
  useEffect(() => {
    const el = box.current?.querySelector<HTMLElement>(`[data-line="${Math.max(0, cur)}"]`);
    const c = box.current;
    if (!el || !c) return;
    c.scrollTo({ top: Math.max(0, el.offsetTop - c.clientHeight * 0.22), behavior: 'smooth' });
  }, [cur]);
  if (!lines.length) {
    return (
      <div className="flex flex-1 flex-col justify-center rounded-[18px] bg-white/[0.035] p-6 ring-1 ring-inset ring-white/[0.06]" data-screen-prompter="">
        <p className={cx('font-medium leading-snug text-white', compact ? 'text-[18px]' : 'text-[clamp(20px,2.6vw,28px)]')}>No script: just talk while you show it.</p>
        {!compact ? <p className="mt-3 text-[14px] text-[#a69d93]">Your words are written out once it is in the Library, so you can cut by them.</p> : null}
      </div>
    );
  }
  const countdown = recording && !paused ? p.countdown : null;
  return (
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden rounded-[18px] bg-white/[0.035] ring-1 ring-inset ring-white/[0.06]" data-screen-prompter={cur} aria-live="polite">
      <div ref={box} className={cx('min-h-0 flex-1 overflow-y-auto scroll-smooth', compact ? 'px-4 py-3' : 'px-6 py-5 sm:px-8')} style={{ scrollbarWidth: 'none' }}>
        {lines.map((l, i) => {
          const said = i < cur;
          const now = i === cur || (cur < 0 && i === 0);
          const next = i === cur + 1 && countdown !== null;
          return (
            <div key={l.id} data-line={i} data-now={now ? '' : undefined} className={cx('transition-[color,opacity] duration-200', compact ? 'mb-3' : 'mb-5')}>
              {l.delivery ? <div className={cx('italic text-[#e8b169]', compact ? 'text-[12px]' : 'text-[14px]', !now && 'opacity-50')}>({l.delivery})</div> : null}
              <p
                className={cx(
                  'font-medium leading-[1.3] tracking-[-0.01em]',
                  compact ? 'text-[19px]' : 'text-[clamp(22px,3vw,36px)]',
                  now ? 'text-white' : said ? 'text-white/25' : next ? 'text-white/70' : 'text-white/40',
                )}
              >
                {next ? (
                  <span className="tc mr-2 inline-grid h-6 min-w-6 place-items-center rounded-full bg-[#e0a458] px-1.5 align-middle text-[12px] font-semibold text-[#1c1307]" aria-label={`Line ${l.n} in ${Math.ceil(countdown!)} seconds`}>
                    {Math.max(1, Math.ceil(countdown!))}
                  </span>
                ) : null}
                {l.text}
              </p>
            </div>
          );
        })}
        <div style={{ height: '60%' }} aria-hidden />
      </div>
      {recording ? (
        <div className={cx('flex items-center gap-3 border-t border-white/[0.06]', compact ? 'px-3 py-2' : 'px-5 py-3')}>
          <button
            type="button"
            onClick={onNext}
            disabled={p.next < 0}
            className="inline-flex h-8 items-center gap-1.5 rounded-full bg-white/[0.08] px-3 text-[12.5px] font-medium text-white hover:bg-white/[0.14] disabled:opacity-40"
            data-next-line
          >
            <CaretDown size={13} /> Next line
          </button>
          <span className="text-[12px] text-[#8f877e]">
            {paused ? 'Paused: the teleprompter waits too.' : p.next < 0 ? "That's the last line. Stop when you're done." : `Line ${Math.max(1, cur + 1)} of ${lines.length}`}
          </span>
          {countdown !== null && !compact ? (
            <div className="ml-auto h-1 w-24 overflow-hidden rounded-full bg-white/10" aria-hidden>
              <div className="h-full rounded-full bg-[#e0a458] transition-[width] duration-100" style={{ width: `${Math.max(0, Math.min(1, 1 - countdown / CUE_LEAD)) * 100}%` }} />
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
