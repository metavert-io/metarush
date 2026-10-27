import { useEffect, useId, useState, type CSSProperties, type ReactNode } from 'react';
import { CloudArrowUp, Desktop, Sparkle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { call } from '../../../api/client';
import type { CostEstimate as Estimate, SoundEnhanceView, SoundModeView, SoundScore, SoundShowOut } from '../../../api/types';
import { CostEstimate } from '../../../components/CostEstimate';
import { useEstimatesFirst } from '../../../lib/preferences';
import { markJobSeen } from '../../../lib/workbench';
import { useDialog } from '../../../ui/Dialog';
import { Button, cx, Segmented, Spinner } from '../../../ui/primitives';
import { useToast } from '../../../ui/Toast';
import { ownerTitle, useOwnership } from '../../../components/OwnerOnly';
import { checkApplied, errorText, useRefreshCut } from '../voiceover/api';
import { rangeClass, Toggle } from './Mixer';

/*
 * Clearer voices: each person on camera and the narration, made clearer on
 * this computer (clean, studio) or with ElevenLabs (cloud, priced first). The
 * recordings are never changed: a clearer copy plays instead, switched off and
 * on per person (the A/B), at a strength (how much of the clearer voice is
 * heard). Every result is measured, before and after, and said in plain words.
 * The words "DNSMOS", "LUFS" or "de-ess" never appear here; the numbers are
 * behind Details.
 */

type Mode = 'clean' | 'studio' | 'cloud';

const MODE_INFO: Record<Mode, { label: string; icon: ReactNode; words: string }> = {
  clean: { label: 'Clean', icon: <Desktop size={13} />, words: 'On this computer, fast and free: less hiss, hum and room echo, softer harsh s sounds and pops, an even level.' },
  studio: { label: 'Studio', icon: <Sparkle size={13} />, words: 'On this computer, slower: rebuilds the voice for a studio sound. Every word is checked, so nothing anyone said changes.' },
  cloud: { label: 'Cloud', icon: <CloudArrowUp size={13} />, words: 'ElevenLabs voice isolation: the voice is sent to ElevenLabs. You see the price first.' },
};

interface Row {
  key: string;
  target: string;
  label: string;
  sub: string;
  enhance?: SoundEnhanceView;
}

export function Voices({ revisionId, show, original, onOriginal }: {
  revisionId: string;
  show: SoundShowOut;
  /** A/B in the preview: every voice as recorded. */
  original: boolean;
  onOriginal: (v: boolean) => void;
}) {
  const rows: Row[] = show.people
    .filter((p) => p.seconds > 0)
    .map((p) => ({ key: `p:${p.name}`, target: p.name, label: p.name, sub: `Speaks for ${secondsText(p.seconds)} in this cut`, enhance: p.enhance }));
  const n = show.narration;
  if (n && n.recorded > 0) rows.push({ key: 'narration', target: 'narration', label: 'Narration', sub: `${n.recorded} recorded ${n.recorded === 1 ? 'line' : 'lines'} (AI voices are clear already)`, enhance: n.enhance });
  const modes = Object.fromEntries(show.modes.map((m) => [m.mode, m])) as Partial<Record<Mode, SoundModeView>>;
  const anyClearer = rows.some((r) => r.enhance && !r.enhance.off);

  return (
    <section aria-labelledby="mix-voices-h" className="space-y-3 rounded-[14px] p-3.5 hairline" data-sound-voices>
      <div className="flex flex-wrap items-start justify-between gap-x-3 gap-y-2">
        <div className="min-w-0">
          <h3 id="mix-voices-h" className="text-[13.5px] font-semibold">
            Clearer voices
          </h3>
          <p className="text-[12px] leading-relaxed text-subtle">The recordings stay as they are: a clearer version plays instead, and you can switch back any time.</p>
        </div>
        {anyClearer ? (
          <div className="shrink-0">
            <Toggle checked={original} onChange={onOriginal} label="Hear every voice as recorded" help="Compare in the preview (nothing changes)." small />
          </div>
        ) : null}
      </div>
      {rows.length === 0 ? (
        <p className="text-[12.5px] text-subtle">Nobody speaks in this cut yet: once its clips are transcribed, each person shows up here.</p>
      ) : (
        <ul className="space-y-2.5" aria-label="Voices">
          {rows.map((r) => (
            <li key={r.key}>
              <VoiceRow row={r} revisionId={revisionId} modes={modes} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function secondsText(s: number): string {
  if (s < 90) return `${Math.max(1, Math.round(s))} seconds`;
  const m = Math.round(s / 60);
  return m === 1 ? 'a minute' : `${m} minutes`;
}

function VoiceRow({ row, revisionId, modes }: { row: Row; revisionId: string; modes: Partial<Record<Mode, SoundModeView>> }) {
  const e = row.enhance;
  const [mode, setMode] = useState<Mode>((e?.mode as Mode) ?? 'clean');
  const [strength, setStrength] = useState(Math.round((e?.strength ?? 1) * 100));
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useRefreshCut();
  const qc = useQueryClient();
  const strengthId = useId();
  const estimatesFirst = useEstimatesFirst();

  // A new version (made here or elsewhere) shows what it now has.
  useEffect(() => {
    if (e) {
      setMode(e.mode as Mode);
      setStrength(Math.round(e.strength * 100));
    }
  }, [e?.mode, e?.strength]); // eslint-disable-line react-hooks/exhaustive-deps

  const info = modes[mode];
  const studioMissing = mode === 'studio' && info && !info.ready;
  const cloudMissing = mode === 'cloud' && info && !info.ready;
  const same = !!e && e.mode === mode && Math.round(e.strength * 100) === strength;
  const upToDate = same && e?.state === 'ready';

  const run = async (label: string, input: { mode?: Mode; strength?: number; off?: boolean; remove?: boolean }, done: (o: { summary: string }) => string) => {
    setBusy(label);
    setProblem(null);
    try {
      const out = await call('sound.enhance', { revisionId, target: row.target, ...input, apply: true });
      markJobSeen(out.jobId);
      checkApplied(out.applied);
      refresh();
      toast({ tone: 'success', title: done(out) });
    } catch (err) {
      setProblem(errorText(err));
      refresh();
    } finally {
      setBusy(null);
    }
  };

  const make = async () => {
    const s = strength / 100;
    // Price first: nothing is sent or spent until the person says yes — unless
    // they turned estimates off (D208): then the project's budget decides.
    if (mode === 'cloud' && estimatesFirst) {
      setBusy('price');
      let est;
      try {
        est = await call('sound.enhance', { revisionId, target: row.target, mode, strength: s, estimate: true });
      } catch (err) {
        setProblem(errorText(err));
        setBusy(null);
        return;
      } finally {
        setBusy(null);
      }
      if (!est.estimate.allowed) {
        await dialog.alert({ title: 'This can’t be made in the cloud right now', body: est.estimate.problem ?? est.summary });
        return;
      }
      if (est.estimate.credits) {
        const cost = est.estimate.cost;
        const ok = await dialog.confirm({
          title: `Make ${row.label === 'Narration' ? 'the narration' : `${row.label}’s voice`} clearer with ElevenLabs?`,
          body: (
            <CloudPrice
              who={row.label === 'Narration' ? 'the narration' : `${row.label}’s speech`}
              seconds={est.estimate.speechSeconds}
              credits={est.estimate.credits}
              cost={cost}
              budget={est.estimate.budget}
              remaining={est.estimate.accountRemaining}
            />
          ),
          confirmLabel: `Spend ${cost?.about.startsWith('about ') ? cost.about : `about ${est.estimate.credits.toLocaleString()} credits`}`,
        });
        if (!ok) return;
      }
    }
    await run('make', { mode, strength: s }, () => `${row.label === 'Narration' ? 'The narration' : row.label} sounds clearer`);
  };

  const own = useOwnership(); // installing on this computer is the owners' (D191)
  const setUpStudio = async () => {
    setBusy('setup');
    setProblem(null);
    try {
      const out = await call('sound.setup', { components: ['studio'], background: true });
      toast({ tone: 'success', title: 'Setting up studio quality', body: out.summary });
      // Ask again until it is ready (it shows in Activity meanwhile).
      for (let i = 0; i < 400; i++) {
        await new Promise((r) => setTimeout(r, 3000));
        const s = await call('sound.show', { revisionId });
        const st = s.modes.find((m) => m.mode === 'studio');
        qc.setQueryData(['sound.show', { revisionId }], s);
        if (st?.ready) {
          toast({ tone: 'success', title: 'Studio quality is ready on this computer' });
          break;
        }
      }
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(null);
    }
  };

  const pct = strength;
  return (
    <div className="rounded-[12px] p-3 hairline" data-voice={row.label}>
      <div className="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
        <div className="min-w-0">
          <div className="text-[13px] font-medium">{row.label}</div>
          <div className="text-[11.5px] text-subtle">{row.sub}</div>
        </div>
        {e ? (
          <Toggle
            small
            checked={!e.off}
            disabled={!!busy}
            onChange={(on) => void run('ab', { off: !on }, () => (on ? `${row.label === 'Narration' ? 'The narration' : row.label} plays the clearer voice` : `${row.label === 'Narration' ? 'The narration' : row.label} plays as recorded`))}
            label="Clearer voice on"
          />
        ) : null}
      </div>

      {e ? <EnhanceState view={e} /> : null}

      <div className="mt-2.5 space-y-2.5">
        <Segmented<Mode>
          label={`How to make ${row.label === 'Narration' ? 'the narration' : `${row.label}’s voice`} clearer`}
          value={mode}
          onChange={setMode}
          items={(['clean', 'studio', 'cloud'] as Mode[]).map((m) => ({ value: m, label: MODE_INFO[m].label, icon: MODE_INFO[m].icon }))}
        />
        <p className="text-[12px] leading-relaxed text-muted">{MODE_INFO[mode].words}</p>
        {info?.speed && mode !== 'cloud' ? <p className="text-[11.5px] text-subtle">{info.speed}</p> : null}
        {mode === 'clean' && info && !info.ready ? <p className="text-[11.5px] text-subtle">It sets itself up the first time ({info.size}).</p> : null}

        {studioMissing ? (
          <div className="flex flex-wrap items-center gap-2 rounded-[10px] bg-hover/60 px-3 py-2">
            <span className="min-w-0 flex-1 text-[12px] text-muted">Studio quality needs a one-time setup on this computer ({info?.size}).</span>
            <Button size="sm" variant="secondary" loading={busy === 'setup'} disabled={!own.owner} title={ownerTitle(own, 'set up studio quality on this computer')} onClick={() => void setUpStudio()}>
              {busy === 'setup' ? 'Setting up…' : 'Set it up'}
            </Button>
          </div>
        ) : null}
        {cloudMissing ? (
          <p className="rounded-[10px] bg-hover/60 px-3 py-2 text-[12px] text-muted">
            {info?.problem}{' '}
            <Link className="font-medium text-accent-text hover:underline" to="/settings/integrations">
              Open Settings → Integrations
            </Link>
          </p>
        ) : null}

        <div className="max-w-[420px]">
          <div className="flex items-baseline justify-between">
            <label htmlFor={strengthId} className="text-[12px] text-muted">
              How much of the clearer voice you hear
            </label>
            <span className="text-[12px] text-fg">{pct}%</span>
          </div>
          <input
            id={strengthId}
            type="range"
            min={0}
            max={100}
            step={5}
            value={pct}
            aria-valuetext={`${pct}%`}
            onChange={(ev) => setStrength(Number(ev.target.value))}
            style={{ '--pct': `${pct}%` } as CSSProperties}
            className={cx('mt-1', rangeClass)}
          />
          <div className="flex justify-between text-[11px] text-subtle" aria-hidden>
            <span>More of the room</span>
            <span>All of it</span>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant={upToDate ? 'secondary' : 'primary'}
            loading={busy === 'make' || busy === 'price'}
            disabled={!!busy || studioMissing || cloudMissing || (upToDate && !e?.off)}
            onClick={() => void make()}
          >
            {busy === 'make' ? 'Making it clearer…' : upToDate && e?.off ? 'Use the clearer voice' : upToDate ? 'Clearer voice made' : e ? 'Make it again with these settings' : 'Make it clearer'}
          </Button>
          {e ? (
            <Button size="sm" variant="ghost" disabled={!!busy} loading={busy === 'remove'} onClick={() => void run('remove', { remove: true }, () => `${row.label === 'Narration' ? 'The narration' : row.label} plays as recorded`)}>
              Use the recording as it is
            </Button>
          ) : null}
          {busy === 'make' ? <span className="text-[11.5px] text-subtle">Activity shows how far along it is.</span> : null}
        </div>
        {problem ? (
          <p className="text-[12px] leading-relaxed text-danger" role="alert">
            {problem}
          </p>
        ) : null}
      </div>
    </div>
  );
}

/** A voice's clearer version: on or off, its words, its scores and warnings (numbers behind Details). */
function EnhanceState({ view }: { view: SoundEnhanceView }) {
  const sc = view.score;
  return (
    <div className="mt-2 space-y-1">
      <p className={cx('text-[12.5px] leading-relaxed', view.state === 'ready' ? 'text-fg' : 'text-accent-text')}>{view.words}</p>
      {sc?.summary ? <p className="text-[12px] leading-relaxed text-muted">{sc.summary}</p> : null}
      {sc?.warnings?.map((w) => (
        <p key={w} className="text-[12px] leading-relaxed text-accent-text">
          {w}
        </p>
      ))}
      {sc ? <ScoreDetails score={sc} /> : null}
    </div>
  );
}

function ScoreDetails({ score }: { score: SoundScore }) {
  const b = score.before.clarity;
  const a = score.after.clarity;
  return (
    <details className="text-[11.5px] text-subtle">
      <summary className="cursor-pointer select-none font-medium text-accent-text">Details</summary>
      <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
        {b && a ? (
          <>
            <dt>Voice clarity score</dt>
            <dd>
              {b.overall.toFixed(2)} → {a.overall.toFixed(2)} out of 5
            </dd>
            <dt>The voice itself</dt>
            <dd>
              {b.speech.toFixed(2)} → {a.speech.toFixed(2)}
            </dd>
            <dt>The background</dt>
            <dd>
              {b.background.toFixed(2)} → {a.background.toFixed(2)}
            </dd>
          </>
        ) : null}
        {score.before.noise && score.after.noise ? (
          <>
            <dt>Background below the voice</dt>
            <dd>
              {Math.round(score.before.noise.snrDb)} dB → {Math.round(score.after.noise.snrDb)} dB
            </dd>
          </>
        ) : null}
        {score.likeness !== undefined ? (
          <>
            <dt>Still sounds like them</dt>
            <dd>{Math.round(score.likeness * 100)}%</dd>
          </>
        ) : null}
        <dt>Measured over</dt>
        <dd>{score.seconds.toFixed(1)} seconds of speech</dd>
        {score.keptWords?.length ? (
          <>
            <dt>Words kept</dt>
            <dd>{score.keptWords.join(', ')}</dd>
          </>
        ) : null}
      </dl>
    </details>
  );
}

function CloudPrice({ who, seconds, credits, cost, budget, remaining }: {
  who: string;
  seconds: number;
  credits: number;
  cost?: Estimate;
  budget?: { limitCredits?: number; usedCredits: number; remainingCredits?: number };
  remaining?: number;
}) {
  return (
    <div className="space-y-2 text-[13px] leading-relaxed text-muted">
      <p>
        About {secondsText(seconds)} of {who} (with a moment around each stretch) go to ElevenLabs, are made clearer there and come back.
      </p>
      <p>
        It costs {cost ? <strong className="text-fg">{cost.about}</strong> : 'about'}: <strong className="text-fg">{credits.toLocaleString()} ElevenLabs credits</strong> (1,000 a minute of
        speech). The same voice with the same settings is never paid for twice.
      </p>
      {budget?.remainingCredits !== undefined ? <p>The project’s budget has {Math.round(budget.remainingCredits).toLocaleString()} credits left.</p> : null}
      {remaining !== undefined ? <p>Your ElevenLabs plan has {remaining.toLocaleString()} credits left this period.</p> : null}
      {cost ? <CostEstimate estimate={cost} defaultOpen /> : null}
    </div>
  );
}

export function SpinnerLine({ text }: { text: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-[12px] text-subtle">
      <Spinner size={12} />
      {text}
    </span>
  );
}
