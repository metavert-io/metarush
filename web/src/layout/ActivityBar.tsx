import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import {
  ArrowClockwise,
  ArrowRight,
  BatteryWarning,
  Browser,
  CaretDown,
  Check,
  CloudArrowUp,
  CookingPot,
  FilmStrip,
  Gauge,
  HardDrives,
  Lightning,
  LinkSimple,
  Microphone,
  Pause,
  Question,
  ShieldCheck,
  Sparkle,
  WarningCircle,
  Waveform,
  X,
} from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { useActivity } from '../api/activity';
import { useLiveState } from '../api/events';
import { useStatus } from '../api/queries';
import { call } from '../api/client';
import type { ActivityItem, ActivitySummary, WaitGroup } from '../api/types';
import { bytes, capFirst, plural } from '../lib/format';
import { useActivityOpen } from '../lib/workbench';
import { Button, cx, Details, Freshness, Progress, Spinner, stateWord } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { useBrowserSignIn } from '../components/LinkImports';

/* ---------- formatting ---------- */

export function rateLabel(bps: number | undefined): string | null {
  if (!bps || !Number.isFinite(bps) || bps <= 0) return null;
  return `${bytes(bps)}/s`;
}

export function etaLabel(s: number | undefined | null): string | null {
  if (s == null || !Number.isFinite(s) || s <= 0) return null;
  // Whole seconds first, so 179.6 s reads "3m 00s", never "2m 60s".
  const t = Math.max(1, Math.round(s));
  if (t < 60) return `${t}s left`;
  const m = Math.floor(t / 60);
  if (m < 60) return `${m}m ${String(t % 60).padStart(2, '0')}s left`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m left`;
}

const catIcon: Record<string, (p: { size: number; className?: string }) => ReactNode> = {
  upload: (p) => <CloudArrowUp {...p} />,
  processing: (p) => <Waveform {...p} />,
  render: (p) => <FilmStrip {...p} />,
  qa: (p) => <ShieldCheck {...p} />,
  assistant: (p) => <Sparkle {...p} />,
  narration: (p) => <Microphone {...p} />,
  import: (p) => <LinkSimple {...p} />,
  storage: (p) => <HardDrives {...p} />,
  generate: (p) => <Sparkle {...p} weight="fill" />,
  recipe: (p) => <CookingPot {...p} />, // a run of Recipes (D204)
};

/* ---------- actions ---------- */

function useActivityActions() {
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const done = () => {
    for (const k of ['activity.summary', 'job.list', 'asset.list', 'project.show']) void qc.invalidateQueries({ queryKey: [k] });
  };
  const processNow = async (key: string, ids: string[], what: string) => {
    setBusy(key);
    try {
      const out = await call('job.expedite', { ids });
      toast({ tone: 'success', title: out.expedited.length ? `Processing ${what} now` : 'Nothing to start', body: out.expedited.length ? (what === 'it' ? 'It runs even on battery.' : 'They run even on battery.') : 'They already started or finished.' });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not start them', body: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(null);
      done();
    }
  };
  const retry = async (key: string, ids: string[]) => {
    setBusy(key);
    let failed = 0;
    for (const id of ids) {
      try {
        await call('job.retry', { id });
      } catch {
        failed++;
      }
    }
    setBusy(null);
    done();
    if (failed) toast({ tone: 'error', title: `${plural(failed, 'job')} could not be retried` });
    else toast({ tone: 'success', title: ids.length > 1 ? `Retrying ${ids.length} jobs` : 'Retrying' });
  };
  return { busy, processNow, retry };
}

/* ---------- the strip under the top bar ---------- */

type HeadlinePart = { key: string; icon: ReactNode; text: string; short: string; tone?: 'warn' | 'danger' };

/** Strip chips: `text` on wider screens, `short` on phones. */
function headlineParts(s: ActivitySummary): HeadlinePart[] {
  const c = s.counts;
  const parts: HeadlinePart[] = [];
  if (c.uploads) parts.push({ key: 'up', icon: <CloudArrowUp size={14} />, text: `Uploading ${plural(c.uploads, 'file')}`, short: `${c.uploads} uploading` });
  if (c.imports) {
    // One import says what it is doing ("Downloading 45%").
    const one = c.imports === 1 ? s.items.find((i) => i.category === 'import' && i.state === 'active') : undefined;
    const pct = one?.stagePct != null && one.stage === 'download' ? ` ${Math.round(one.stagePct * 100)}%` : '';
    parts.push({ key: 'imp', icon: <LinkSimple size={14} />, text: one ? `${one.stageLabel}${pct}: ${one.title}` : `Importing ${plural(c.imports, 'video')} from links`, short: `${c.imports} importing` });
  }
  if (c.processing) parts.push({ key: 'proc', icon: <Waveform size={14} />, text: `Processing ${plural(c.processing, 'file')}`, short: `${c.processing} processing` });
  if (c.renders) parts.push({ key: 'rnd', icon: <FilmStrip size={14} />, text: plural(c.renders, 'render'), short: `${c.renders} rendering` });
  if (c.qa) parts.push({ key: 'qa', icon: <ShieldCheck size={14} />, text: plural(c.qa, 'quality check'), short: `${c.qa} checking` });
  if (c.assistant) parts.push({ key: 'ai', icon: <Sparkle size={14} />, text: 'Claude Code working', short: 'Claude Code' });
  if (c.narration) {
    // One piece of narration says exactly what it is doing ("Making the AI voice for line 3").
    const one = c.narration === 1 ? s.items.find((i) => i.category === 'narration' && (i.state === 'active' || i.state === 'waiting')) : undefined;
    parts.push({ key: 'vo', icon: <Microphone size={14} />, text: one?.stageLabel || `Making narration for ${c.narration} cuts`, short: `${c.narration} narrating` });
  }
  if (c.generating) {
    // One generated clip says what it is doing ("Google Gemini is making it").
    const one = c.generating === 1 ? s.items.find((i) => i.category === 'generate' && (i.state === 'active' || i.state === 'waiting')) : undefined;
    parts.push({ key: 'gen', icon: <Sparkle size={14} weight="fill" />, text: one?.stageLabel || `Making ${plural(c.generating, 'clip')} with generative video`, short: `${c.generating} generating` });
  }
  if (c.recipes) { // Recipes
    // Runs of Recipes: one says what it is doing or waiting for ("Run 2 of “Garden story”: waiting for your OK…").
    const one = c.recipes === 1 ? s.items.find((i) => i.category === 'recipe' && (i.state === 'active' || i.state === 'waiting')) : undefined; // Recipes
    const waits = s.items.some((i) => i.category === 'recipe' && i.state === 'waiting' && i.stage !== 'claude'); // a run of Recipes waits for a person
    parts.push({
      key: 'rcp',
      icon: <CookingPot size={14} />,
      text: one ? `${one.title}: ${one.stageLabel.charAt(0).toLowerCase()}${one.stageLabel.slice(1)}` : `${plural(c.recipes, 'run')} of recipes going on`, // Recipes
      short: `${c.recipes} run${c.recipes === 1 ? '' : 's'}`, // Recipes
      tone: waits ? 'warn' : undefined,
    });
  }
  if (c.storage) {
    // Moving the media says how far it is ("Copying 120 of 4,000 files").
    const one = s.items.find((i) => i.category === 'storage' && i.state === 'active');
    parts.push({ key: 'sto', icon: <HardDrives size={14} />, text: one?.stageLabel ? `Moving media: ${one.stageLabel.charAt(0).toLowerCase()}${one.stageLabel.slice(1)}` : 'Moving media to new storage', short: 'Moving media' });
  }
  for (const w of s.waiting) {
    if (w.code === 'battery') parts.push({ key: `w-${w.code}-${w.machine}`, icon: <BatteryWarning size={14} weight="fill" />, text: capFirst(w.text), short: `${w.count} paused`, tone: 'warn' });
    else if (w.code === 'no_worker' || w.code === 'heavy_disabled') parts.push({ key: `w-${w.code}`, icon: <WarningCircle size={14} />, text: capFirst(w.text), short: `${w.count} waiting`, tone: 'warn' });
  }
  if (c.failed) parts.push({ key: 'fail', icon: <WarningCircle size={14} weight="fill" />, text: `${plural(c.failed, 'failure')} to review`, short: `${c.failed} failed`, tone: 'danger' });
  return parts;
}

/**
 * The global activity bar: live counts and an overall progress line for
 * uploads, processing, renders, QA and assistant runs, with the reason anything
 * waits. Hidden when nothing is in flight; the drawer stays reachable from the
 * top bar.
 */
export function ActivityStrip() {
  const act = useActivity();
  const [open, setOpen] = useActivityOpen();
  const { busy, processNow } = useActivityActions();
  const s = act.data;
  if (!s) return null;
  const parts = headlineParts(s);
  const show = s.counts.active > 0 || s.counts.failed > 0 || s.waiting.some((w) => w.code === 'battery' || w.code === 'no_worker' || w.code === 'heavy_disabled');
  if (!show) return null;
  const battery = s.waiting.find((w) => w.code === 'battery' && w.canProcessNow);
  const working = s.counts.active - s.counts.waiting > 0 || s.items.some((i) => i.state === 'active');
  const onlyWaiting = !working && s.counts.waiting > 0;
  return (
    <div data-activity-strip className="relative border-t border-line bg-[color-mix(in_oklab,var(--bg-raised)_92%,transparent)]">
      <div className="mx-auto flex h-10 w-full max-w-[1480px] items-center gap-2 px-4 sm:px-6 lg:px-8">
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          aria-controls="mr-activity-drawer"
          className="group flex min-w-0 flex-1 items-center gap-2 rounded-[8px] py-1 text-left"
        >
          <span className="grid h-5 w-5 shrink-0 place-items-center text-accent-text">{working ? <Spinner size={12} /> : onlyWaiting ? <BatteryWarning size={15} weight="fill" /> : <WarningCircle size={15} className="text-danger" />}</span>
          <span className="sr-only">Activity: {s.headline}.</span>
          <span aria-hidden className="no-scrollbar flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden">
            {parts.map((p, i) => (
              <span
                key={p.key}
                className={cx(
                  'inline-flex h-6 shrink-0 items-center gap-1.5 rounded-full px-2 text-[12px] font-medium',
                  p.tone === 'warn' ? 'bg-accent-soft text-accent-text' : p.tone === 'danger' ? 'bg-danger-soft text-danger' : 'text-muted',
                  i > 2 && 'max-md:hidden',
                  i > 3 && 'max-xl:hidden',
                )}
              >
                {p.icon}
                <span className="sm:hidden">{p.short}</span>
                <span className="max-w-[46ch] truncate max-sm:hidden">{p.text}</span>
              </span>
            ))}
            {parts.length > 3 ? <span className="tc shrink-0 text-[11.5px] text-subtle md:hidden">+{parts.length - 3}</span> : null}
          </span>
          {s.progress != null && working ? <span className="tc hidden shrink-0 text-[12px] text-muted sm:inline">{Math.round(s.progress * 100)}%</span> : null}
        </button>
        {battery ? (
          <Button size="sm" variant="quiet" className="max-sm:hidden" loading={busy === 'strip'} icon={<Lightning size={13} weight="fill" />} onClick={() => void processNow('strip', battery.jobIds, battery.count === 1 ? 'it' : `${battery.count} jobs`)}>
            Process now
          </Button>
        ) : null}
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-label={open ? 'Hide activity details' : 'Show activity details'}
          className="inline-flex h-7 shrink-0 items-center gap-1 rounded-[8px] px-2 text-[12px] font-medium text-muted hover:bg-hover hover:text-fg"
        >
          <span className="hidden sm:inline">Details</span>
          <CaretDown size={12} className={cx('transition-transform duration-200', open && 'rotate-180')} />
        </button>
      </div>
      {s.progress != null ? (
        <div
          className="absolute inset-x-0 bottom-0 h-[2px] overflow-hidden"
          role="progressbar"
          aria-label="Overall progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(s.progress * 100)}
        >
          <div
            className={cx('h-full transition-[width] duration-700 ease-[var(--ease-out-expo)]', onlyWaiting ? 'bg-subtle/50' : 'bg-accent')}
            style={{ width: `${Math.max(2, s.progress * 100)}%` }}
          />
        </div>
      ) : null}
    </div>
  );
}

/**
 * Top-bar button that opens the drawer (also when nothing runs). Its dot is the
 * live state (D184: it was a separate "Live" mark): green while updates arrive,
 * amber while connecting, red when they stopped. When the database isn't
 * answering the button turns into a plain warning.
 */
export function ActivityButton() {
  const act = useActivity();
  const [open, setOpen] = useActivityOpen();
  const live = useLiveState();
  const status = useStatus();
  const n = act.data?.counts.active ?? 0;
  const dbDown = !!status.data && !status.data.dbOk;
  const detail =
    live.state === 'live'
      ? live.mode === 'polling'
        ? 'Live updates (checked every few seconds)'
        : 'Live updates'
      : live.state === 'offline'
        ? `Live updates paused. Retrying${live.retryInMs ? ` in ${Math.round(live.retryInMs / 1000)}s` : ''}.`
        : 'Connecting to live updates';
  const liveLabel = dbDown ? "The database isn't answering" : detail;
  const dbTitle = `The database isn't answering, so changes can't be saved right now${status.data?.dbError ? ` (${status.data.dbError})` : ''}`;
  return (
    <span className="relative inline-flex shrink-0">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-controls="mr-activity-drawer"
        aria-label={[n ? `Activity, ${n} in progress` : 'Activity', dbDown ? "the database isn't answering" : ''].filter(Boolean).join('; ')}
        title={dbDown ? dbTitle : `Activity · ${detail}`}
        className={cx(
          'relative inline-flex h-9 items-center justify-center gap-1.5 rounded-[10px] transition-colors',
          dbDown ? 'bg-danger-soft px-2 text-danger min-[1400px]:px-2.5' : 'w-9 text-muted hover:bg-hover hover:text-fg max-sm:w-8',
          open && !dbDown && 'bg-hover text-fg',
        )}
        data-activity-button
      >
        {dbDown ? <WarningCircle size={18} weight="fill" /> : n ? <Spinner size={15} className="text-accent-text" /> : <Gauge size={18} />}
        {dbDown ? <span className="hidden whitespace-nowrap text-[12.5px] font-medium min-[1400px]:inline">Database offline</span> : null}
      </button>
      <span
        role="status"
        aria-label={liveLabel}
        title={dbDown ? dbTitle : detail}
        className="pointer-events-none absolute right-1 top-1 flex h-2 w-2"
        data-live-state={dbDown ? 'database-down' : live.state}
      >
        {live.state === 'live' && !dbDown ? <span className="absolute inset-0 rounded-full bg-ok opacity-60" style={{ animation: 'mr-pulse 2.4s ease-in-out infinite' }} /> : null}
        <span className={cx('relative h-2 w-2 rounded-full ring-2 ring-[var(--bg)]', dbDown ? 'bg-danger' : live.state === 'live' ? 'bg-ok' : live.state === 'connecting' ? 'bg-accent' : 'bg-danger')} />
      </span>
    </span>
  );
}

/* ---------- the drawer ---------- */

function metaLine(it: ActivityItem): string {
  const parts: string[] = [];
  parts.push(it.stageLabel);
  if (it.state === 'active' && it.bytesTotal && it.stage !== 'processing' && it.stage !== 'ready')
    parts.push(`${bytes(it.bytesDone ?? 0)} of ${bytes(it.bytesTotal)}`);
  const r = it.state === 'active' ? rateLabel(it.rate) : null;
  if (r) parts.push(r);
  const e = it.state === 'active' ? etaLabel(it.etaSeconds) : null;
  if (e) parts.push(e);
  if (it.machine) parts.push(`on ${it.machine}`);
  return parts.join(', ');
}

function StepChips({ it }: { it: ActivityItem }) {
  if (!it.steps?.length) return null;
  // Finished steps fold into a count; what is still going (or stuck) stays visible.
  const ready = it.steps.filter((st) => st.state === 'done');
  const open = it.steps.filter((st) => st.state !== 'done');
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-1">
      {open.map((st) => {
        const tone =
          st.state === 'done' ? 'bg-ok-soft text-ok' : st.state === 'failed' ? 'bg-danger-soft text-danger' : st.state === 'paused' ? 'bg-accent-soft text-accent-text' : st.state === 'running' ? 'bg-info-soft text-info' : 'bg-hover text-subtle';
        const label =
          st.state === 'paused' && st.waiting?.batteryPercent != null
            ? `${st.label} paused (battery ${st.waiting.batteryPercent}%)`
            : st.state === 'running' && st.pct
              ? `${st.label} ${Math.round(st.pct * 100)}%`
              : st.label;
        return (
          <span key={st.kind} title={`${st.label}: ${st.waiting?.text ?? stateWord(st.state)}`} className={cx('inline-flex h-5 items-center gap-1 rounded-full px-1.5 text-[10.5px] font-medium', tone)}>
            {st.state === 'running' ? <Spinner size={8} /> : st.state === 'paused' ? <BatteryWarning size={10} weight="bold" /> : null}
            {label}
            <span className="sr-only">: {stateWord(st.state)}</span>
          </span>
        );
      })}
      {ready.length ? (
        <span className="inline-flex h-5 items-center gap-1 text-[10.5px] text-subtle" title={`Ready: ${ready.map((st) => st.label.toLowerCase()).join(', ')}`}>
          <Check size={10} weight="bold" className="text-ok" />
          {ready.length === it.steps.length ? 'All ready' : `${ready.length} of ${it.steps.length} ready`}
        </span>
      ) : null}
    </div>
  );
}

/** What failed, in plain words; the raw error waits behind Details. */
function ErrorLine({ it }: { it: ActivityItem }) {
  const failed = (it.steps ?? []).filter((st) => st.state === 'failed').map((st) => st.label.toLowerCase());
  const what = failed.length > 1 ? `${failed.slice(0, -1).join(', ')} and ${failed[failed.length - 1]}` : failed[0];
  return (
    <div className="mt-1 text-[11.5px]">
      {it.problem ? (
        <>
          <span className="text-danger">{it.problem}</span>
          {it.hint ? <span className="mt-0.5 block text-muted">{it.hint}</span> : null}
        </>
      ) : (
        <span className="text-danger">{what ? `${capFirst(what)} failed.` : 'It did not finish.'}</span>
      )}
      <Details compact className="mt-0.5">
        <div className="font-mono text-[11px] [overflow-wrap:anywhere]">{it.error}</div>
      </Details>
    </div>
  );
}

function ItemRow({ it, busy, onProcessNow, onRetry, onOpen, onGo, onSignIn }: { it: ActivityItem; busy: boolean; onProcessNow: () => void; onRetry: () => void; onOpen: () => void; onGo: (link: string) => void; onSignIn?: () => void }) {
  const Icon = catIcon[it.category] ?? catIcon.processing!;
  const pct = it.pct != null ? Math.round(it.pct * 100) : null;
  return (
    <li className="rounded-[12px] px-3 py-2.5 hover:bg-hover/60">
      <div className="flex items-start gap-3">
        <span className={cx('mt-[1px] grid h-7 w-7 shrink-0 place-items-center rounded-[8px]', it.state === 'failed' ? 'bg-danger-soft text-danger' : it.state === 'waiting' ? 'bg-accent-soft text-accent-text' : it.state === 'done' ? 'bg-ok-soft text-ok' : 'bg-hover text-muted')}>
          {Icon({ size: 15 })}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            {it.link ? (
              <button type="button" onClick={onOpen} className="min-w-0 truncate text-left text-[13px] font-medium text-fg hover:underline" title={it.name ? `${it.title} (${it.name})` : it.title}>
                {it.title}
              </button>
            ) : (
              <span className="min-w-0 truncate text-[13px] font-medium text-fg" title={it.name ? `${it.title} (${it.name})` : it.title}>
                {it.title}
              </span>
            )}
            {pct != null && it.state !== 'done' ? <span className="tc ml-auto shrink-0 text-[11.5px] text-muted">{pct}%</span> : null}
          </div>
          <div className={cx('mt-0.5 truncate text-[11.5px]', it.state === 'failed' ? 'text-danger' : it.state === 'interrupted' ? 'text-accent-text' : 'text-subtle')} title={metaLine(it)}>
            {metaLine(it)}
            {it.subtitle ? <span className="text-subtle">{`, ${it.subtitle}`}</span> : null}
          </div>
          {it.state === 'active' || it.state === 'waiting' ? (
            <Progress className="mt-1.5" value={it.pct ?? 0} indeterminate={it.pct == null} tone={it.state === 'waiting' ? 'muted' : 'accent'} label={`${it.title} progress`} />
          ) : null}
          <StepChips it={it} />
          {it.waiting && it.waiting.code !== 'starting' && !it.steps?.length ? <div className="mt-1 text-[11.5px] text-accent-text">{it.waiting.text}</div> : null}
          {it.state === 'interrupted' && it.category === 'upload' ? <div className="mt-1 text-[11.5px] text-muted">Drop the same file again to resume from here.</div> : null}
          {it.questions ? (
            <button type="button" onClick={() => onGo(it.questions!.link)} className="mt-1.5 inline-flex items-center gap-1.5 rounded-full bg-accent-soft px-2 py-0.5 text-[11.5px] font-medium text-accent-text hover:underline">
              <Question size={11} weight="bold" aria-hidden />
              {capFirst(it.questions.text)}
            </button>
          ) : null}
          {it.error ? <ErrorLine it={it} /> : null}
          {(it.canProcessNow && it.processJobIds?.length) || (it.canRetry && it.retryJobIds?.length) || (it.signInHelps && onSignIn) ? (
            <div className="mt-2 flex flex-wrap gap-2">
              {it.signInHelps && onSignIn ? (
                <Button size="sm" variant="primary" icon={<Browser size={12} />} onClick={onSignIn}>
                  Use my browser's sign-in
                </Button>
              ) : null}
              {it.canProcessNow && it.processJobIds?.length ? (
                <Button size="sm" variant="quiet" loading={busy} icon={<Lightning size={12} weight="fill" />} onClick={onProcessNow}>
                  Process now
                </Button>
              ) : null}
              {it.canRetry && it.retryJobIds?.length ? (
                <Button size="sm" variant="secondary" loading={busy} icon={<ArrowClockwise size={12} />} onClick={onRetry}>
                  Retry
                </Button>
              ) : null}
            </div>
          ) : null}
        </div>
      </div>
    </li>
  );
}

function WaitBanner({ w, busy, onProcessNow, onSettings }: { w: WaitGroup; busy: boolean; onProcessNow: () => void; onSettings: () => void }) {
  const machine = w.machine || 'This machine';
  return (
    <div className="flex items-start gap-3 rounded-[12px] bg-accent-soft px-3 py-2.5">
      {w.code === 'battery' ? <BatteryWarning size={17} weight="fill" className="mt-[1px] shrink-0 text-accent-text" /> : <Pause size={17} weight="fill" className="mt-[1px] shrink-0 text-accent-text" />}
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium text-fg">{capFirst(w.text)}</div>
        <div className="mt-0.5 text-[11.5px] leading-relaxed text-muted">
          {w.code === 'battery' ? (
            <>
              Big jobs such as transcripts and scene analysis wait while {machine} is on battery below {w.threshold ?? 25}%. They carry on by themselves once it is plugged in.{' '}
              <button type="button" onClick={onSettings} className="font-medium text-accent-text hover:underline">
                Change this
              </button>
            </>
          ) : w.code === 'no_worker' ? (
            'No machine running MetaRush can take this work right now. It starts as soon as one is.'
          ) : (
            <>
              {machine} is set to skip big jobs.{' '}
              <button type="button" onClick={onSettings} className="font-medium text-accent-text hover:underline">
                Change this
              </button>
            </>
          )}
        </div>
      </div>
      {w.canProcessNow ? (
        <Button size="sm" variant="primary" loading={busy} icon={<Lightning size={12} weight="fill" />} onClick={onProcessNow} className="shrink-0">
          Process now
        </Button>
      ) : null}
    </div>
  );
}

export function ActivityDrawer() {
  const [open, setOpen] = useActivityOpen();
  const act = useActivity();
  const navigate = useNavigate();
  const { busy, processNow, retry } = useActivityActions();
  const signIn = useBrowserSignIn();
  const panel = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const [params, setParams] = useSearchParams();

  // Deep link from notifications: ?activity=open
  useEffect(() => {
    if (params.get('activity') === 'open') {
      setOpen(true);
      const next = new URLSearchParams(params);
      next.delete('activity');
      setParams(next, { replace: true });
    }
  }, [params, setParams, setOpen]);

  useEffect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    panel.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('[data-mr-dialog="open"]')) {
        e.preventDefault();
        setOpen(false);
      }
    };
    const onDown = (e: MouseEvent) => {
      const t = e.target as HTMLElement;
      if (panel.current?.contains(t) || t.closest('[aria-controls="mr-activity-drawer"]')) return;
      setOpen(false);
    };
    window.addEventListener('keydown', onKey);
    document.addEventListener('mousedown', onDown);
    return () => {
      window.removeEventListener('keydown', onKey);
      document.removeEventListener('mousedown', onDown);
      prev?.focus?.();
    };
  }, [open, setOpen]);

  const s = act.data;
  const groups = useMemo(() => {
    const items = s?.items ?? [];
    return {
      attention: items.filter((i) => i.state === 'failed' || i.state === 'interrupted'),
      progress: items.filter((i) => i.state === 'active' || i.state === 'waiting'),
      done: items.filter((i) => i.state === 'done'),
    };
  }, [s]);

  if (!open) return null;
  const go = (link?: string) => {
    if (!link) return;
    setOpen(false);
    navigate(link);
  };
  const waits = (s?.waiting ?? []).filter((w) => w.code === 'battery' || w.code === 'no_worker' || w.code === 'heavy_disabled');
  return (
    <div
      ref={panel}
      id="mr-activity-drawer"
      role="dialog"
      aria-modal="false"
      aria-labelledby={titleId}
      tabIndex={-1}
      className="fade-up pointer-events-none absolute inset-x-0 top-[calc(100%+8px)] z-[45] mx-auto flex max-h-[calc(100dvh-120px)] w-full max-w-[1480px] justify-end px-3 outline-none sm:px-6 lg:px-8 [&>*]:pointer-events-auto"
    >
      <div className="flex w-full flex-col overflow-hidden rounded-[16px] bg-raised shadow-[var(--shadow-float)] hairline sm:w-[460px]">
        <div className="flex items-center gap-2 border-b border-line px-4 py-3">
          <h2 id={titleId} className="text-[14px] font-semibold text-fg">
            Activity
          </h2>
          {s ? <span className="truncate text-[12px] text-muted">{s.headline}</span> : null}
          <Freshness className="ml-auto shrink-0" at={act.dataUpdatedAt || undefined} fetching={act.isFetching} onRefresh={() => void act.refetch()} />
          <button type="button" onClick={() => setOpen(false)} aria-label="Close activity" className="rounded-[8px] p-1.5 text-subtle hover:bg-hover hover:text-fg">
            <X size={15} />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {act.isLoading ? (
            <div className="flex items-center justify-center gap-2 py-10 text-[13px] text-muted">
              <Spinner size={13} /> Loading activity
            </div>
          ) : !s ? null : (
            <div className="space-y-3">
              {waits.map((w) => (
                <WaitBanner key={`${w.code}-${w.machine}`} w={w} busy={busy === `w-${w.code}`} onProcessNow={() => void processNow(`w-${w.code}`, w.jobIds, w.count === 1 ? 'it' : `${w.count} jobs`)} onSettings={() => go('/settings/storage#machine')} />
              ))}
              {groups.attention.length ? (
                <Section title="Needs attention">
                  {groups.attention.map((it) => (
                    <ItemRow
                      key={it.key}
                      it={it}
                      busy={busy === it.key}
                      onOpen={() => go(it.link)}
                      onGo={go}
                      onProcessNow={() => void processNow(it.key, it.processJobIds ?? [], 'it')}
                      onRetry={() => void retry(it.key, it.retryJobIds ?? [])}
                      onSignIn={it.signInHelps && it.url ? () => void signIn(it.url!, it.platform, { projectId: it.projectId }) : undefined}
                    />
                  ))}
                </Section>
              ) : null}
              <Section title="In progress" empty={groups.progress.length === 0 ? 'Nothing running. Uploads, footage being prepared, renders, quality checks and Claude Code requests show up here.' : undefined}>
                {groups.progress.map((it) => (
                  <ItemRow key={it.key} it={it} busy={busy === it.key} onOpen={() => go(it.link)} onGo={go} onProcessNow={() => void processNow(it.key, it.processJobIds ?? [], 'it')} onRetry={() => void retry(it.key, it.retryJobIds ?? [])} />
                ))}
              </Section>
              {groups.done.length ? (
                <Section title="Finished recently">
                  {groups.done.map((it) => (
                    <ItemRow key={it.key} it={it} busy={false} onOpen={() => go(it.link)} onGo={go} onProcessNow={() => undefined} onRetry={() => undefined} />
                  ))}
                </Section>
              ) : null}
              {s.workers.length ? (
                <div className="border-t border-line px-3 pb-1 pt-3">
                  <div className="mb-1.5 text-[11.5px] font-medium text-subtle">Machines</div>
                  <ul className="space-y-2">
                    {s.workers.map((w) => (
                      <li key={w.id} className="text-[12px] text-muted">
                        <div className="flex min-w-0 items-baseline gap-1.5">
                          <span className="min-w-0 truncate font-medium text-fg" title={w.name}>
                            {w.name}
                          </span>
                          {w.this ? <span className="shrink-0 text-subtle">(this machine)</span> : null}
                        </div>
                        <div className="text-subtle">
                          {w.batteryPercent != null ? `Battery ${w.batteryPercent}%${w.charging ? ', charging' : w.onBattery ? ', on battery' : ', plugged in'}. ` : 'Plugged in. '}
                          {w.running ? `Doing ${w.running} of the ${w.concurrency} jobs it can run at once.` : 'Not busy.'}
                          {w.deferringHeavy ? ' Big jobs are waiting.' : ''}
                        </div>
                      </li>
                    ))}
                  </ul>
                  <button type="button" onClick={() => go('/settings/storage#machine')} className="mt-2 inline-flex items-center gap-1 text-[12px] font-medium text-accent-text hover:underline">
                    Battery settings <ArrowRight size={12} />
                  </button>
                </div>
              ) : null}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2 border-t border-line px-4 py-2.5">
          <Link to="/settings/log" onClick={() => setOpen(false)} className="inline-flex items-center gap-1 text-[12.5px] font-medium text-accent-text hover:underline" data-activity-log-link>
            See the full log <ArrowRight size={12} />
          </Link>
        </div>
      </div>
    </div>
  );
}

function Section({ title, children, empty }: { title: string; children?: ReactNode; empty?: string }) {
  return (
    <section>
      <h3 className="px-3 pb-1 pt-1 text-[11.5px] font-medium text-subtle">{title}</h3>
      {empty ? <p className="px-3 py-3 text-[12.5px] text-muted">{empty}</p> : <ul>{children}</ul>}
    </section>
  );
}
