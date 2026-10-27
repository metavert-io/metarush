import { useEffect, useId, useMemo, useRef, useState } from 'react';
import {
  ArrowDown,
  ArrowUp,
  ChatCircleDots,
  ClipboardText,
  MapPin,
  Microphone,
  PencilSimple,
  Play,
  Plus,
  Robot,
  Sparkle,
  Stack,
  Trash,
  WarningCircle,
} from '@phosphor-icons/react';
import { call, media } from '../../../api/client';
import type { Anchor, AssetSummary, FittedOut, LineFit, NarrationLine, NarrationOut, RenderPlan, Spec, VoiceView } from '../../../api/types';
import { MenuButton } from '../../../layout/assistant/Menu';
import { markJobSeen, openAskWith } from '../../../lib/workbench';
import { useDialog } from '../../../ui/Dialog';
import { Badge, Button, cx, IconButton, Spinner } from '../../../ui/primitives';
import { applyPlan, checkApplied, clock, errorText, useChange } from './api';
import { askFit, FitReview, ScriptChoice, VersionSwitch, type ScriptUse } from './ScriptVersions';
import { useToast } from '../../../ui/Toast';
import { clipName, type Scope } from './AddVoiceover';
import { PlacementEditor } from './Placement';

/*
 * The script: one card per line with its words, where it starts (in words, the
 * way an editor says it), how to say it, its own voice if it has one, its sound,
 * and — when it does not fit before what follows — the one-click fixes.
 */

export function soundLabel(l: NarrationLine): string {
  const t = l.take;
  if (!t) return 'No sound yet';
  const base =
    t.source === 'recorded' ? 'Your recording' : t.source === 'imported' ? 'Imported recording' : t.source === 'tts' ? 'AI voice' : t.source === 'scratch' ? 'Computer voice (for timing)' : 'Sound';
  return t.useProcessed && t.processed ? `${base}, cleaned up` : base;
}

/** A still of the picture under a point of the cut (the latest-starting clip there), for a line's thumbnail. */
function stillAt(plan: RenderPlan | undefined, assets: Map<string, AssetSummary>, t: number): string | undefined {
  if (!plan) return undefined;
  const f = Math.round(t * plan.fps);
  let best: RenderPlan['clips'][number] | undefined;
  for (const c of plan.clips) if (f >= c.from && f < c.from + c.durationInFrames && (!best || c.from >= best.from)) best = c;
  if (!best || assets.get(best.assetId)?.kind === 'audio') return undefined;
  const source = best.sourceIn + ((f - best.from) / plan.fps) * (best.playbackRate || 1);
  return media.frame(best.assetId, source, 160);
}

export function ScriptEditor({ revisionId, spec, show, plan, assets, scope, voices, onSeek, onPlayLine, onTakes, onRecord, onAIVoice, focusId, composing, onComposed, onFitStarted }: {
  revisionId: string;
  spec: Spec;
  show: NarrationOut;
  plan?: RenderPlan;
  assets: Map<string, AssetSummary>;
  scope: Scope;
  /** ElevenLabs voices for a line of its own (empty when no key is set up). */
  voices: VoiceView[];
  onSeek: (s: number) => void;
  onPlayLine: (l: NarrationLine) => void;
  onTakes: (l: NarrationLine) => void;
  onRecord: (l?: NarrationLine) => void;
  onAIVoice: () => void;
  focusId?: string;
  /** Open with the box for writing a script (Add voiceover → Write my own). */
  composing?: boolean;
  onComposed?: () => void;
  /** Claude was asked to fit the script to the video (its conversation). */
  onFitStarted?: (conversationId: string) => void;
}) {
  const change = useChange();
  const dialog = useDialog();
  const [comparing, setComparing] = useState<FittedOut | null>(null);
  const compare = async () => {
    try {
      setComparing(await call('voiceover.fitted', { revisionId }));
    } catch {
      setComparing(null);
    }
  };
  const fits = useMemo(() => new Map(show.fit.map((f) => [f.beatId, f])), [show.fit]);
  const lines = show.beats;
  const only = scope.clipId;
  const inScope = only ? lines.filter((l) => l.anchor.clipId === only || startsIn(plan, l, only)) : lines;
  const over = show.fit.filter((f) => f.status === 'over' || f.status === 'unresolved').length;
  const recorded = lines.filter((l) => l.take).length;

  const scratch = () =>
    change(
      async () => {
        const out = await call('voiceover.scratch', { revisionId, apply: true });
        markJobSeen(out.jobId);
        return checkApplied(out.applied);
      },
      'Every line now has a free computer voice, so you can hear the timing',
      'The computer voice could not be made',
    );

  const paste = async () => {
    await dialog.open<true>((close) => <PasteScript revisionId={revisionId} spec={spec} hasLines={lines.length > 0} scope={scope} onDone={() => close(true)} onCancel={() => close()} onFitStarted={onFitStarted} />, {
      title: scope.clipId ? `Write the narration for ${clipName(spec.clips, scope.clipId)}` : lines.length ? 'Paste a script' : 'Write your script',
      size: 'lg',
    });
  };

  if (lines.length === 0 || composing) {
    return (
      <div>
        <ComposeScript
          revisionId={revisionId}
          spec={spec}
          scope={scope}
          hasLines={lines.length > 0}
          onDone={() => onComposed?.()}
          onCancel={lines.length ? () => onComposed?.() : undefined}
          onFitStarted={onFitStarted}
        />
      </div>
    );
  }

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2">
        <p className="text-[12.5px] text-muted" aria-live="polite">
          {lines.length === 1 ? '1 line' : `${lines.length} lines`}
          {recorded ? `, ${recorded} with sound` : ', none with sound yet'}
          {over ? (
            <span className="text-danger">
              , {over === 1 ? '1 does' : `${over} do`} not fit yet
            </span>
          ) : null}
        </p>
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          <Button size="sm" variant="ghost" icon={<ClipboardText size={13} />} onClick={() => void paste()}>
            Paste a script
          </Button>
          <AddLineButton revisionId={revisionId} spec={spec} plan={plan} scope={scope} lines={lines} />
        </div>
      </div>

      <VersionSwitch show={show} revisionId={revisionId} onCompare={() => void compare()} />
      {comparing ? (
        <div className="mb-3">
          <FitReview fit={comparing} revisionId={revisionId} onDone={() => setComparing(null)} />
        </div>
      ) : null}

      {recorded < lines.length ? (
        <div className="mb-3 flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-3 py-2.5 text-[12.5px] text-fg">
          <span className="min-w-0 flex-1">Give your lines a sound:</span>
          <Button size="sm" variant="secondary" icon={<Microphone size={13} />} onClick={() => onRecord()}>
            Record
          </Button>
          <Button size="sm" variant="secondary" icon={<Robot size={13} />} onClick={onAIVoice}>
            AI voice
          </Button>
          <Button size="sm" variant="ghost" onClick={() => void scratch()} title="A free computer voice reads the lines, so you can check the timing">
            Try a free computer voice
          </Button>
        </div>
      ) : null}

      <ol className="space-y-2.5" aria-label="Narration lines">
        {inScope.map((l) => (
          <LineCard
            key={l.id}
            line={l}
            fit={fits.get(l.id)}
            lines={lines}
            revisionId={revisionId}
            spec={spec}
            plan={plan}
            assets={assets}
            voices={voices}
            focused={focusId === l.id}
            onSeek={onSeek}
            onPlay={() => onPlayLine(l)}
            onTakes={() => onTakes(l)}
            onRecord={() => onRecord(l)}
          />
        ))}
      </ol>
      {scope.clipId && inScope.length < lines.length ? (
        <p className="mt-3 text-[12px] text-subtle">
          Showing the lines over {clipName(spec.clips, scope.clipId)}; the other {lines.length - inScope.length === 1 ? 'line stays' : `${lines.length - inScope.length} lines stay`} as they are.
        </p>
      ) : null}
    </div>
  );
}

function startsIn(plan: RenderPlan | undefined, l: NarrationLine, clipId: string): boolean {
  const c = plan?.clips.find((x) => x.id === clipId);
  if (!c || !plan) return false;
  const f = Math.round(l.start * plan.fps);
  return f >= c.from && f < c.from + c.durationInFrames;
}

/* ---------- Writing or pasting a script ---------- */

const EXAMPLE = `Out here, the light arrives slowly.

(warmer) And then it is everywhere.`;

function ComposeScript({ revisionId, spec, scope, hasLines, onDone, onCancel, onFitStarted }: {
  revisionId: string;
  spec: Spec;
  scope: Scope;
  hasLines: boolean;
  onDone: () => void;
  onCancel?: () => void;
  onFitStarted?: (conversationId: string) => void;
}) {
  return (
    <section className="rounded-[14px] bg-panel p-4 hairline" aria-label="Write your script">
      <h3 className="text-[15px] font-semibold tracking-[-0.01em]">{scope.clipId ? `What should be said over ${clipName(spec.clips, scope.clipId)}?` : 'What should the narrator say?'}</h3>
      <p className="mt-1 max-w-[62ch] text-[12.5px] leading-relaxed text-muted">
        Type or paste your script. Leave an empty line between lines.
        {scope.clipId ? ' The first line starts with the shot, the rest follow it.' : ' Your script is kept exactly as you give it.'}
      </p>
      <PasteScript revisionId={revisionId} spec={spec} hasLines={hasLines} scope={scope} onDone={onDone} onCancel={onCancel} onFitStarted={onFitStarted} inline />
    </section>
  );
}

function PasteScript({ revisionId, spec, hasLines, scope, onDone, onCancel, onFitStarted, inline }: {
  revisionId: string;
  spec: Spec;
  hasLines: boolean;
  scope: Scope;
  onDone: () => void;
  onCancel?: () => void;
  /** "Fit it to the video with Claude": the conversation that does it. */
  onFitStarted?: (conversationId: string) => void;
  inline?: boolean;
}) {
  const id = useId();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState<'replace' | 'append' | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  // A whole script (not one shot's lines) is used as-is, or fitted to the video by Claude (D86).
  const [use, setUse] = useState<ScriptUse>('as-is');
  const whole = !scope.clipId;
  const fit = whole && use === 'fit';
  const change = useChange();
  const toast = useToast();
  const paragraphs = text
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .filter((p) => p && !p.startsWith('#'));

  const save = async (mode: 'replace' | 'append') => {
    setBusy(mode);
    setProblem(null);
    const out = await change(
      async () => {
        const parsed = await call('voiceover.parse', {
          revisionId,
          text,
          ...(scope.clipId ? { clip: scope.clipId } : mode === 'append' ? { append: true } : {}),
        });
        if (!parsed.result) {
          setProblem(parsed.issues[0]?.message ?? parsed.summary);
          throw new Error(parsed.issues[0]?.message ?? parsed.summary);
        }
        const res = await applyPlan(parsed.result);
        return { parsed, rev: res.revision.id };
      },
      (p) => (fit && mode === 'replace' ? `${p.parsed.summary.replace(/\.$/, '')}. Claude is fitting it to the video now.` : p.parsed.summary),
      'The script was not saved',
    );
    setBusy(null);
    if (!out) return;
    if (fit && mode === 'replace') {
      try {
        onFitStarted?.(await askFit(out.rev));
      } catch (e) {
        toast({ tone: 'error', title: "Claude can't fit it right now", body: `${errorText(e)} Your script is saved as you wrote it.` });
      }
    }
    onDone();
  };

  return (
    <div className={inline ? 'mt-3' : ''}>
      <label htmlFor={id} className="sr-only">
        Script
      </label>
      <textarea
        id={id}
        data-autofocus
        rows={inline ? 6 : 10}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={EXAMPLE}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && paragraphs.length) {
            e.preventDefault();
            void save(hasLines && whole && !fit ? 'append' : 'replace');
          }
        }}
        className="w-full resize-y rounded-[12px] bg-raised px-3.5 py-3 text-[14px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-offset-0 focus:outline-accent"
      />
      {whole && paragraphs.length ? <ScriptChoice value={use} onChange={setUse} /> : null}
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <span className="text-[12px] text-subtle" aria-live="polite">
          {paragraphs.length ? (paragraphs.length === 1 ? '1 paragraph' : `${paragraphs.length} paragraphs`) : 'Words in brackets like (warmer) are notes for the narrator, never spoken.'}
        </span>
        {problem ? <span className="text-[12px] text-danger">{problem}</span> : null}
        <div className="ml-auto flex flex-wrap gap-2">
          {onCancel ? (
            <Button size="sm" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
          ) : null}
          {fit ? (
            <Button size="sm" variant="primary" icon={<Sparkle size={13} />} disabled={!paragraphs.length} loading={busy !== null} onClick={() => void save('replace')}>
              {hasLines ? 'Replace it and fit it with Claude' : 'Fit it to the video with Claude'}
            </Button>
          ) : hasLines && whole ? (
            <>
              <Button size="sm" variant="secondary" disabled={!paragraphs.length} loading={busy === 'replace'} onClick={() => void save('replace')}>
                Replace the script
              </Button>
              <Button size="sm" variant="primary" disabled={!paragraphs.length} loading={busy === 'append'} onClick={() => void save('append')}>
                Add after the last line
              </Button>
            </>
          ) : (
            <Button size="sm" variant="primary" disabled={!paragraphs.length} loading={busy !== null} onClick={() => void save('replace')}>
              {scope.clipId ? `Use these lines for ${clipName(spec.clips, scope.clipId)}` : 'Use my script'}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

function AddLineButton({ revisionId, spec, plan, scope, lines }: { revisionId: string; spec: Spec; plan?: RenderPlan; scope: Scope; lines: NarrationLine[] }) {
  const dialog = useDialog();
  const change = useChange();
  const add = async () => {
    const text = await dialog.prompt({
      title: 'Add a line',
      label: 'What should be said?',
      multiline: true,
      required: true,
      maxLength: 2000,
      confirmLabel: 'Add line',
      helper: scope.clipId ? `It starts with ${clipName(spec.clips, scope.clipId)}.` : 'It follows the last line; you can move it after.',
    });
    if (!text) return;
    const c = scope.clipId ? plan?.clips.find((x) => x.id === scope.clipId) : undefined;
    const anchor: Anchor | undefined = scope.clipId ? { kind: 'clip', clipId: scope.clipId, offset: 0.3 } : undefined;
    // In the script, a line for one clip goes after the lines that start before it.
    const after = c && plan ? [...lines].reverse().find((l) => Math.round(l.start * plan.fps) < c.from)?.id : undefined;
    await change(
      async () =>
        checkApplied(
          (await call('voiceover.line', { revisionId, action: 'add', text, ...(anchor ? { anchor } : {}), ...(after ? { after } : scope.clipId ? { before: lines[0]?.id } : {}), apply: true }))
            .applied,
        ),
      'Line added',
      'The line was not added',
    );
  };
  return (
    <Button size="sm" variant="secondary" icon={<Plus size={13} />} onClick={() => void add()}>
      Add a line
    </Button>
  );
}

/* ---------- One line ---------- */

function LineCard({ line, fit, lines, revisionId, spec, plan, assets, voices, focused, onSeek, onPlay, onTakes, onRecord }: {
  line: NarrationLine;
  fit?: LineFit;
  lines: NarrationLine[];
  revisionId: string;
  spec: Spec;
  plan?: RenderPlan;
  assets: Map<string, AssetSummary>;
  voices: VoiceView[];
  focused: boolean;
  onSeek: (s: number) => void;
  onPlay: () => void;
  onTakes: () => void;
  onRecord: () => void;
}) {
  const change = useChange();
  const dialog = useDialog();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(line.text);
  const [placing, setPlacing] = useState(false);
  const [delivery, setDelivery] = useState(line.delivery ?? '');
  const [busy, setBusy] = useState<string | null>(null);
  const ref = useRef<HTMLLIElement>(null);
  const textId = useId();
  const delId = useId();
  const voiceId = useId();
  useEffect(() => setText(line.text), [line.text]);
  useEffect(() => setDelivery(line.delivery ?? ''), [line.delivery]);
  useEffect(() => {
    if (focused) ref.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [focused]);

  const run = async (key: string, fn: () => Promise<unknown>, done: string, failed?: string) => {
    setBusy(key);
    const out = await change(fn, done, failed);
    setBusy(null);
    return out;
  };
  const edit = (input: Record<string, unknown>, done: string) =>
    run(Object.keys(input)[0] ?? 'edit', async () => checkApplied((await call('voiceover.line', { revisionId, line: line.id, apply: true, action: 'change', ...input })).applied), done);

  const saveText = async () => {
    const t = text.trim();
    if (!t || t === line.text) {
      setEditing(false);
      setText(line.text);
      return;
    }
    if ((await edit({ text: t }, 'Line changed')) !== null) setEditing(false);
  };
  const saveDelivery = () => {
    if (delivery.trim() === (line.delivery ?? '')) return;
    void edit({ delivery: delivery.trim() }, delivery.trim() ? 'Delivery note saved' : 'Delivery note removed');
  };
  const move = (anchor: Anchor) =>
    run('place', async () => checkApplied((await call('voiceover.line', { revisionId, action: 'move', line: line.id, anchor, apply: true })).applied), 'Start point saved').then(
      (ok) => ok !== null && setPlacing(false),
    );
  const order = (dir: -1 | 1) => {
    const i = lines.findIndex((l) => l.id === line.id);
    const other = lines[i + dir];
    if (!other) return;
    void run(
      'order',
      async () => checkApplied((await call('voiceover.line', { revisionId, action: 'move', line: line.id, ...(dir < 0 ? { before: other.id } : { after: other.id }), apply: true })).applied),
      dir < 0 ? 'Line moved up in the script' : 'Line moved down in the script',
    );
  };
  const remove = async () => {
    const ok = await dialog.confirm({
      title: `Remove line ${line.line}?`,
      body: `“${line.text.length > 120 ? `${line.text.slice(0, 119)}…` : line.text}” leaves the narration. Its recordings stay in the project, and earlier versions of the cut still have it.`,
      confirmLabel: 'Remove line',
      tone: 'danger',
    });
    if (ok) void run('remove', async () => checkApplied((await call('voiceover.line', { revisionId, action: 'remove', line: line.id, apply: true })).applied), 'Line removed');
  };
  const fix = (strategy: string, summary: string) =>
    run(
      `fix:${strategy}`,
      async () => {
        const out = await call('voiceover.fit', { revisionId, strategy: strategy as 'extend-shot', beats: [line.id] });
        if (!out.result) throw new Error(out.summary);
        return applyPlan(out.result);
      },
      `Fixed: ${summary.charAt(0).toLowerCase()}${summary.slice(1)}`,
      'That fix could not be made',
    );

  const bad = fit && (fit.status === 'over' || fit.status === 'unresolved');
  const tight = fit?.status === 'tight';
  const shorten = fit?.fixes?.find((f) => f.strategy === 'shorten');
  const still = useMemo(() => stillAt(plan, assets, line.start), [plan, assets, line.start]);
  const [stillFailed, setStillFailed] = useState(false);
  useEffect(() => setStillFailed(false), [still]);
  const ownVoice = line.voice?.voiceId;

  return (
    <li
      ref={ref}
      className={cx('rounded-[14px] bg-panel p-3.5 hairline transition-shadow duration-300', focused && 'ring-2 ring-accent', bad && 'ring-1 ring-danger/50')}
      aria-label={`Line ${line.line}`}
      data-line={line.id}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="tc grid h-6 min-w-6 place-items-center rounded-full bg-hover px-1.5 text-[11.5px] font-semibold text-muted">{line.line}</span>
        {still && !stillFailed ? (
          <button
            type="button"
            onClick={() => onSeek(line.start)}
            title="The picture when this line starts. Go there in the preview."
            aria-label={`Show where line ${line.line} starts in the preview`}
            className="relative h-[27px] w-12 shrink-0 overflow-hidden rounded-[6px] bg-inset hairline transition-transform duration-200 hover:scale-[1.04] focus-visible:outline-2 focus-visible:outline-accent motion-reduce:hover:scale-100"
          >
            <img src={still} alt="" loading="lazy" decoding="async" onError={() => setStillFailed(true)} className="absolute inset-0 h-full w-full object-cover" />
          </button>
        ) : null}
        <button type="button" onClick={() => onSeek(line.start)} className="tc text-[12px] text-accent-text hover:underline" title="Go there in the preview">
          {clock(line.start)}–{clock(line.end)}
        </button>
        <Badge tone={line.take ? (line.take.source === 'scratch' ? 'info' : 'ok') : 'neutral'}>{soundLabel(line)}</Badge>
        {line.takeCount > 1 ? (
          <button type="button" onClick={onTakes} className="text-[11.5px] text-muted hover:text-fg hover:underline">
            {line.takeCount} takes
          </button>
        ) : null}
        {line.locked ? <Badge>locked</Badge> : null}
        <div className="ml-auto flex items-center gap-0.5">
          <IconButton size="sm" label={`Play line ${line.line}`} onClick={onPlay}>
            <Play size={13} weight="fill" />
          </IconButton>
          <IconButton size="sm" label={`Record line ${line.line}`} onClick={onRecord}>
            <Microphone size={14} />
          </IconButton>
          <MenuButton
            label={`More for line ${line.line}`}
            items={[
              { label: 'Takes and clean-up', icon: <Stack size={14} />, onSelect: onTakes },
              { label: 'Move up in the script', icon: <ArrowUp size={14} />, disabled: line.line === 1, onSelect: () => order(-1) },
              { label: 'Move down in the script', icon: <ArrowDown size={14} />, disabled: line.line === lines.length, onSelect: () => order(1) },
              'separator',
              { label: 'Remove line', icon: <Trash size={14} />, danger: true, onSelect: () => void remove() },
            ]}
          />
        </div>
      </div>

      {editing ? (
        <div className="mt-2.5">
          <label htmlFor={textId} className="sr-only">
            Words of line {line.line}
          </label>
          <textarea
            id={textId}
            autoFocus
            rows={Math.min(6, Math.max(2, Math.ceil(text.length / 70)))}
            value={text}
            maxLength={2000}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                void saveText();
              }
              if (e.key === 'Escape') {
                e.stopPropagation();
                setEditing(false);
                setText(line.text);
              }
            }}
            className="w-full resize-y rounded-[10px] bg-raised px-3 py-2 text-[14.5px] leading-relaxed text-fg hairline focus:outline-2 focus:outline-offset-0 focus:outline-accent"
          />
          <div className="mt-1.5 flex flex-wrap items-center justify-end gap-2">
            {line.take && text.trim() !== line.text ? (
              <span className="mr-auto text-[11.5px] text-subtle">The words change; the sound stays until you record or make it again.</span>
            ) : null}
            <Button size="sm" variant="ghost" onClick={() => (setEditing(false), setText(line.text))}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" loading={busy === 'text'} onClick={() => void saveText()}>
              Save words
            </Button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setEditing(true)}
          className="group mt-2 block w-full rounded-[8px] text-left text-[15px] leading-relaxed text-fg"
          aria-label={`Edit the words of line ${line.line}: ${line.text}`}
        >
          {line.text}
          <PencilSimple size={13} className="ml-1.5 inline-block align-[-1px] text-subtle opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" aria-hidden />
        </button>
      )}

      <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-2 text-[12.5px]">
        <span className="inline-flex min-w-0 items-center gap-1.5 text-muted">
          <MapPin size={13} className="shrink-0 text-subtle" aria-hidden />
          <span>
            Starts <span className="text-fg">{line.placement}</span>
          </span>
          {!placing ? (
            <button type="button" onClick={() => setPlacing(true)} className="ml-1 font-medium text-accent-text hover:underline">
              Change
            </button>
          ) : null}
        </span>
        <span className="inline-flex items-center gap-1.5">
          <label htmlFor={delId} className="text-subtle">
            How to say it
          </label>
          <input
            id={delId}
            value={delivery}
            maxLength={300}
            placeholder="warm, a little slower"
            onChange={(e) => setDelivery(e.target.value)}
            onBlur={saveDelivery}
            onKeyDown={(e) => e.key === 'Enter' && (e.currentTarget as HTMLInputElement).blur()}
            className="h-7 w-[180px] rounded-[8px] bg-raised px-2 text-[12.5px] text-fg hairline placeholder:text-subtle"
          />
          {busy === 'delivery' ? <Spinner size={11} /> : null}
        </span>
        {voices.length || ownVoice ? (
          <span className="inline-flex items-center gap-1.5">
            <label htmlFor={voiceId} className="text-subtle">
              Voice
            </label>
            <select
              id={voiceId}
              value={ownVoice ?? ''}
              onChange={(e) => {
                const v = voices.find((x) => x.id === e.target.value);
                void edit({ voice: e.target.value, voiceName: v?.name ?? '' }, e.target.value ? `Line ${line.line} uses ${v?.name ?? 'its own voice'}` : `Line ${line.line} uses the project's voice again`);
              }}
              className="h-7 max-w-[180px] rounded-[8px] bg-raised px-1.5 text-[12.5px] text-fg hairline"
            >
              <option value="">Same as the other lines</option>
              {ownVoice && !voices.some((v) => v.id === ownVoice) ? <option value={ownVoice}>{line.voice?.name || 'Its own voice'}</option> : null}
              {voices.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name}
                </option>
              ))}
            </select>
            {busy === 'voice' ? <Spinner size={11} /> : null}
          </span>
        ) : null}
      </div>

      {placing ? <PlacementEditor line={line} spec={spec} plan={plan} assets={assets} busy={busy === 'place'} onSave={(a) => void move(a)} onCancel={() => setPlacing(false)} /> : null}

      {bad && fit ? (
        <div className="mt-3 rounded-[12px] bg-danger-soft px-3 py-2.5" role="group" aria-label={`Line ${line.line} does not fit`}>
          <p className="flex items-start gap-1.5 text-[12.5px] leading-relaxed text-fg">
            <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0 text-danger" aria-hidden />
            <span>{fit.short || fit.message}.</span>
          </p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {(fit.fixes ?? [])
              .filter((f) => f.strategy !== 'shorten')
              .map((f) => (
                <Button
                  key={f.strategy}
                  size="sm"
                  // A fix says what it does in a sentence: let it wrap inside the card.
                  className="min-h-7 max-w-full !h-auto py-1 text-left leading-snug !whitespace-normal"
                  variant={f.strategy === fit.preferred ? 'primary' : 'secondary'}
                  loading={busy === `fix:${f.strategy}`}
                  onClick={() => void fix(f.strategy, f.summary)}
                >
                  {f.summary}
                </Button>
              ))}
            {shorten ? (
              <>
                <Button size="sm" variant="secondary" icon={<PencilSimple size={12} />} onClick={() => setEditing(true)}>
                  Shorten it to about {shorten.targetWords} words
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<ChatCircleDots size={13} />}
                  onClick={() =>
                    openAskWith(
                      `Line ${line.line} of the narration is too long for its spot: “${line.text}”. Suggest a shorter version of about ${shorten.targetWords} words that says the same thing, as a change to that line for me to review.`,
                      { newConversation: true },
                    )
                  }
                >
                  Ask Claude to shorten it
                </Button>
              </>
            ) : null}
          </div>
        </div>
      ) : fit && (fit.intoNextShot ?? 0) > 0 ? (
        // It fits before the next line but runs into the next shot (after a live take, D85).
        <div className="mt-3 rounded-[12px] bg-accent-soft px-3 py-2.5" role="group" aria-label={`Line ${line.line} runs into the next shot`} data-into-next-shot={fit.intoNextShot}>
          <p className="text-[12.5px] leading-relaxed text-fg">{fit.short || fit.message}.</p>
          {fit.fixes?.length ? (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {fit.fixes.map((f) => (
                <Button
                  key={f.strategy}
                  size="sm"
                  variant="secondary"
                  className="min-h-7 max-w-full !h-auto py-1 text-left leading-snug !whitespace-normal"
                  loading={busy === `fix:${f.strategy}`}
                  onClick={() => void fix(f.strategy, f.summary)}
                >
                  {f.summary}
                </Button>
              ))}
            </div>
          ) : null}
        </div>
      ) : tight && fit ? (
        <p className="mt-2 text-[12px] text-subtle">{fit.short || fit.message}.</p>
      ) : null}
    </li>
  );
}

