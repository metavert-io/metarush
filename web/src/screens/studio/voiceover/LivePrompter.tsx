import { useMemo } from 'react';
import type { NarrationLine, RenderPlan } from '../../../api/types';
import { cx } from '../../../ui/primitives';
import { clock } from './api';
import { CUE_LEAD, promptAt, type PromptLine } from './prompter';

/*
 * The live teleprompter (D85): it follows the video. The line to say now is
 * large; the next one waits dimmed and, about two seconds before its moment,
 * counts down. A mini timeline shows the cut, where each line is due and the
 * playhead. Lines without timing of their own come one after another at the
 * narrator's pace; Space or a clicker brings the next one in early.
 */

/** The script lines as the prompter needs them. */
export function promptLines(lines: NarrationLine[]): PromptLine[] {
  return lines
    .filter((l) => !l.problem)
    .map((l) => ({ id: l.id, n: l.line, text: l.text, delivery: l.delivery, pinned: l.pinned, start: l.start, dur: Math.max(0.4, l.end - l.start) }));
}

export function LivePrompter({ lines, due, time, from, until, recording, counting, count, plan, duration, timed }: {
  lines: PromptLine[];
  due: number[];
  /** The playhead (timeline seconds). */
  time: number;
  /** Where the take starts and (a re-recorded stretch) stops. */
  from: number;
  until?: number;
  recording: boolean;
  counting: boolean;
  count: number;
  plan?: RenderPlan;
  duration: number;
  /** Some lines have a moment of their own (otherwise they follow the pace). */
  timed: boolean;
}) {
  const p = promptAt(due, time);
  const cur = p.current >= 0 ? lines[p.current] : undefined;
  const next = p.next >= 0 ? lines[p.next] : undefined;
  // The countdown runs only while recording (before that the playhead stands still).
  const countdown = recording ? p.countdown : null;
  const lead = countdown !== null ? Math.max(0, Math.min(1, 1 - countdown / CUE_LEAD)) : 0;
  return (
    <div className="relative flex min-h-[260px] flex-1 flex-col overflow-hidden rounded-[18px] bg-white/[0.035] ring-1 ring-inset ring-white/[0.06]" aria-live="polite" data-prompter>
      <MiniTimeline plan={plan} duration={duration} lines={lines} due={due} time={time} from={from} until={until} current={p.current} />
      <div className="relative flex flex-1 flex-col justify-center p-5 sm:p-7">
        {counting ? (
          <div className="absolute inset-0 z-10 grid place-items-center bg-black/55 backdrop-blur-[2px]" role="status" aria-label={`Recording in ${count}`}>
            <span key={count} className="fade-up tc text-[96px] font-semibold leading-none text-white">
              {count}
            </span>
          </div>
        ) : null}
        {recording ? (
          <div className="absolute right-4 top-3 flex items-center gap-1.5 text-[12px] font-medium text-[#ef8d80]">
            <span className="h-2.5 w-2.5 animate-pulse rounded-full bg-[#ef8d80]" /> Recording
          </div>
        ) : null}
        {lines.length === 0 ? (
          <div>
            <p className="text-[clamp(20px,2.6vw,28px)] font-medium leading-snug text-white">No script yet: just talk while the video plays.</p>
            <p className="mt-3 text-[14px] text-[#a69d93]">Every sentence becomes a line, right where you said it.</p>
          </div>
        ) : !cur ? (
          // Before the first line: it waits, dimmed, and counts down from two seconds before its moment.
          <div data-prompt-current="" data-prompt-next={next?.id ?? ''} data-countdown={countdown ?? ''}>
            <div className="mb-2 flex min-h-[24px] items-center gap-2 text-[12px] font-medium uppercase tracking-[0.08em] text-[#8f877e]" data-prompt-state="waiting">
              {countdown !== null && next ? (
                <span className="tc grid h-6 min-w-6 place-items-center rounded-full bg-[#e0a458] px-1.5 text-[12px] font-semibold normal-case tracking-normal text-[#1c1307]" aria-label={`Line ${next.n} in ${Math.ceil(countdown)} seconds`}>
                  {Math.max(1, Math.ceil(countdown))}
                </span>
              ) : null}
              {next ? `First, line ${next.n}${due[p.next] !== undefined ? ` at ${clock(due[p.next]!)}` : ''}` : 'No line after this point'}
            </div>
            {next?.delivery ? <div className="mb-2 text-[14px] italic text-[#e8b169]/70">({next.delivery})</div> : null}
            <p className={cx('text-[clamp(22px,3.2vw,36px)] font-medium leading-[1.3] tracking-[-0.015em] transition-colors duration-200', countdown !== null ? 'text-white/75' : recording ? 'text-white/35' : 'text-white/60')}>
              {next?.text ?? ''}
            </p>
            {countdown !== null ? (
              <div className="mt-4 h-1 overflow-hidden rounded-full bg-white/10" aria-hidden>
                <div className="h-full rounded-full bg-[#e0a458] transition-[width] duration-100" style={{ width: `${lead * 100}%` }} />
              </div>
            ) : null}
            {!recording ? (
              <p className="mt-6 text-[13px] text-[#8f877e]">{timed ? 'Each line shows about two seconds before its moment.' : 'Your lines come one after another at your pace; Space brings the next one in.'}</p>
            ) : null}
          </div>
        ) : (
          <>
            <div className="mb-2 min-h-[18px] text-[12px] font-medium uppercase tracking-[0.08em] text-[#8f877e]" data-prompt-state="line">
              Line {cur.n} of {lines.length}
            </div>
            {cur.delivery ? <div className="mb-2 text-[14px] italic text-[#e8b169]">({cur.delivery})</div> : null}
            <p className="text-[clamp(22px,3.2vw,36px)] font-medium leading-[1.3] tracking-[-0.015em] text-white" data-prompt-current={cur.id}>
              {cur.text}
            </p>
            {next ? (
              <div className="mt-6" data-prompt-next={next.id} data-countdown={countdown ?? ''}>
                <div className="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-[#a69d93]">
                  {countdown !== null ? (
                    <>
                      <span className="tc grid h-6 min-w-6 place-items-center rounded-full bg-[#e0a458] px-1.5 text-[12px] font-semibold text-[#1c1307]" aria-label={`Line ${next.n} in ${Math.ceil(countdown)} seconds`}>
                        {Math.max(1, Math.ceil(countdown))}
                      </span>
                      Line {next.n} is next
                    </>
                  ) : (
                    <>Then line {next.n}{lines[p.next]?.pinned ? `, at ${clock(due[p.next] ?? 0)}` : ''}</>
                  )}
                </div>
                {countdown !== null ? (
                  <div className="mb-2 h-1 overflow-hidden rounded-full bg-white/10" aria-hidden>
                    <div className="h-full rounded-full bg-[#e0a458] transition-[width] duration-100" style={{ width: `${lead * 100}%` }} />
                  </div>
                ) : null}
                <p className={cx('text-[17px] leading-relaxed transition-colors duration-200', countdown !== null ? 'text-white/70' : 'text-white/35')}>{next.text}</p>
              </div>
            ) : cur && recording ? (
              <p className="mt-6 text-[14px] text-[#8f877e]">That's the last line. Stop when you're done.</p>
            ) : null}
          </>
        )}
      </div>
    </div>
  );
}

/** The cut as a strip: the shots, where each line is due, the stretch being recorded and the playhead. */
function MiniTimeline({ plan, duration, lines, due, time, from, until, current }: {
  plan?: RenderPlan;
  duration: number;
  lines: PromptLine[];
  due: number[];
  time: number;
  from: number;
  until?: number;
  current: number;
}) {
  const total = Math.max(0.1, duration);
  const pct = (t: number) => `${Math.max(0, Math.min(100, (t / total) * 100))}%`;
  const shots = useMemo(() => (plan ? plan.clips.map((c) => ({ id: c.id, from: c.from / plan.fps, to: (c.from + c.durationInFrames) / plan.fps })) : []), [plan]);
  return (
    <div className="border-b border-white/[0.06] px-5 pb-3 pt-4" aria-label="Where you are in the video" data-mini-timeline data-playhead={time.toFixed(3)}>
      <div className="relative h-7">
        {shots.map((s, i) => (
          <div key={s.id} className={cx('absolute inset-y-[9px] rounded-[3px]', i % 2 ? 'bg-white/[0.10]' : 'bg-white/[0.16]')} style={{ left: pct(s.from), width: `calc(${pct(s.to)} - ${pct(s.from)} - 1px)` }} aria-hidden />
        ))}
        <div className="absolute inset-y-[6px] rounded-[4px] ring-1 ring-[#ef8d80]/70" style={{ left: pct(from), width: `calc(${pct(until ?? total)} - ${pct(from)})` }} aria-hidden />
        {lines.map((l, i) => (
          <div
            key={l.id}
            className={cx('absolute top-0 h-7 w-[3px] -translate-x-1/2 rounded-full', i === current ? 'bg-[#e0a458]' : l.pinned ? 'bg-white/70' : 'bg-white/35')}
            style={{ left: pct(due[i] ?? l.start) }}
            title={`Line ${l.n}`}
            aria-hidden
          />
        ))}
        <div className="absolute -top-0.5 h-8 w-[2px] -translate-x-1/2 bg-[#ef8d80]" style={{ left: pct(time) }} aria-hidden />
      </div>
      <div className="mt-1 flex justify-between text-[11px] text-[#8f877e]">
        <span className="tc">{clock(time)}</span>
        <span className="tc">{clock(total)}</span>
      </div>
    </div>
  );
}
