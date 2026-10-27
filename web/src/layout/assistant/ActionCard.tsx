import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { ArrowSquareOut, CaretDown, CaretRight, CheckCircle, CircleNotch, HandPalm, Lightning, Play, Prohibit, WarningCircle } from '@phosphor-icons/react';
import { useAssistantOp } from '../../api/assistant';
import type { AssistantAction, Conversation } from '../../api/types';
import { absolute, duration, signedDuration, stamp } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Lightbox, type LightboxItem } from '../../ui/Lightbox';
import { Badge, Button, ErrorPanel, InlineCode, Theirs, cx } from '../../ui/primitives';
import { SheetDiff } from '../../screens/studio/cutsheet/SheetDiff';
import { SignoffsWaiting, useSignoffReminder } from '../../components/SignoffReminder';
import { CostEstimate } from '../../components/CostEstimate';
import { RecipeCardBody, useRecipeCardQuestion } from '../../components/recipes/PanelCard';
import { openModeSwitch } from './ModeSwitch';
import { NotQuiteBox } from './NotQuite';
import { forSomeoneElse, nameOf, useViewerId } from './people';

/*
 * A change Claude asked for. Claude itself stays read-only, so its request was
 * refused and shows up here; the person runs it with one click (the dashboard
 * calls the operation as them, like a plan card's Apply) or says "Not now".
 * Safe steps start by themselves under the person's setting (D141); work a
 * card starts (a render and its quality check, a snapshot, an import) shows
 * its progress, and Claude hears once it has finished (D142). A card that
 * saves a version offers to render its review copy in the same click (D144).
 * A card that uses paid outside AI services carries its estimate, which opens
 * to what it draws on (D208).
 */

const verb: Record<string, string> = {
  'render.submit': 'Render',
  'edit.apply': 'Apply',
  'cut.apply': 'Save',
  'qa.run': 'Check it',
  'revision.approve': 'Approve',
  'project.create': 'Create',
  'footage.analyze': 'Analyze',
  'comment.add': 'Add note',
  'asset.link': 'Add',
  'render.cancel': 'Cancel render',
  'motion.check': 'Check it',
  'motion.approve': 'Approve',
  'motion.revoke': 'Withdraw approval',
  'voiceover.fitted': "Use Claude's version",
  'voiceover.use': 'Switch',
  'voiceover.place': 'Put them there',
  'resources.fetch': 'Bring it in',
  'resources.make': 'Make it',
  'resources.refresh': 'Look again',
  'ingest.url': 'Bring it in',
  'conversation.cut': 'Make the cut',
  'conversation.decide': 'Accept',
  'conversation.angle': 'Change it',
  'conversation.short': 'Make the short',
  'conversation.cleanup': 'Look it over',
  'sound.levels': 'Change it',
  'sound.enhance': 'Make it clearer',
  'sound.music.add': 'Add the music',
  'sound.music.set': 'Change it',
  'sound.setup': 'Set it up',
  'transcript.replace': 'Change them',
  'transcript.fix': 'Fix it',
  'transcript.speaker': 'Change it',
  'transcript.undo': 'Undo it',
  'transcript.spelling.add': 'Add it',
  'transcript.spelling.remove': 'Take it off',
  'transcript.spelling.apply': 'Correct them',
  'transcript.pending.apply': 'Make the version',
  'recipe.run': 'Run it',
  'recipe.again': 'Run it again',
  'recipe.save': 'Save the draft',
  'recipe.update': 'Save the draft',
  'recipe.publish': 'Make it current',
  'recipe.picks': 'Keep them',
  'recipe.write': 'Save the draft',
  'recipe.suggestions.save': 'Save the draft',
  'delivery.notes.set': 'Keep them',
  'captions.translate': 'Keep them',
  'captions.translation.remove': 'Take them off',
  // Layout packs (D266): Claude's pack is a person's decision.
  'content.pack.accept': 'Use this pack',
  'content.pack.set': 'Change it',
  'content.pack.decline': 'Set it aside',
  'content.pack.apply': 'Use the pack',
};

/** The button's words where one operation does different things (its card's title says which). */
export function labelFor(action: AssistantAction): string {
  const t = action.title;
  if ((action.op === 'recipe.run' || action.op === 'recipe.again') && action.estimate && action.estimate.usd > 0) return `${verb[action.op]} (${action.estimate.about})`;
  if (action.op === 'recipe.picks' && !/^Keep/.test(t)) return /^Drop/.test(t) ? 'Drop it' : /^Add/.test(t) ? 'Add it' : 'Change it';
  if (action.op === 'revision.approve' && /^Reject/.test(t)) return 'Reject';
  if (action.op === 'motion.approve' && /^Turn down/.test(t)) return 'Turn down';
  if (action.op === 'conversation.decide' && /^Skip/.test(t)) return 'Skip';
  if (action.op === 'conversation.decide' && /^Bring back/.test(t)) return 'Bring back';
  if (action.op === 'sound.enhance' && /^Play/.test(t)) return 'Switch';
  if (action.op === 'sound.enhance' && /^Stop/.test(t)) return 'Stop';
  if (action.op === 'sound.enhance' && action.setUp?.length) return 'Set up and make it clearer';
  if (action.op === 'sound.music.set' && /^Take/.test(t)) return 'Take it out';
  if (action.op === 'delivery.notes.set' && /^Take/.test(t)) return 'Take them off';
  return verb[action.op ?? ''] ?? 'Do it';
}

/** Whether a card approves a version (sign-offs still waiting are a reminder first, D166). */
export function approvesVersion(a: AssistantAction): boolean {
  return a.op === 'revision.approve' && !/^Reject/.test(a.title);
}

/**
 * Who decided a card and when, for a decided card (D186): "Approved by
 * Maya", "You said not quite: “shorter”", with the time beside it.
 */
export function decidedWords(a: AssistantAction, me: string): { text: string; note?: string } | null {
  if (a.state === 'pending' || a.state === 'running' || a.state === 'unavailable') return null;
  const mine = !!me && a.byId === me;
  const who = mine ? 'you' : a.by || '';
  const Who = mine ? 'You' : a.by || '';
  switch (a.state) {
    case 'done': {
      if (a.autoMode || (a.auto && !a.byId)) return null; // said by its own line ("Done by itself")
      const verb = a.question
        ? 'Answered'
        : a.op === 'revision.approve'
          ? /^Reject/.test(a.title)
            ? 'Rejected'
            : 'Approved'
          : a.op === 'motion.approve'
            ? /^Turn down/.test(a.title)
              ? 'Turned down'
              : 'Approved'
            : 'Done';
      return { text: who ? `${verb} by ${who}` : verb };
    }
    case 'dismissed':
      return { text: Who ? `${Who} said not now` : 'Not now' };
    case 'not_quite':
      return { text: Who ? `${Who} said not quite` : 'Not quite', note: a.note };
    case 'failed':
      return { text: who ? `Tried by ${who}` : 'Tried' };
  }
  return null;
}

/** When a card was decided: "Today 3:10 PM" (the full date on hover). */
export function DecidedLine({ action, me }: { action: AssistantAction; me: string }) {
  const w = decidedWords(action, me);
  if (!w) return null;
  return (
    <p className="mt-0.5 text-[12px] text-subtle" data-decided-by={action.by} data-decided-state={action.state}>
      {w.text}
      {w.note ? (
        <>
          : <Theirs className="text-muted">“{w.note}”</Theirs>
        </>
      ) : null}
      {action.decidedAt ? (
        <>
          {' · '}
          <time dateTime={action.decidedAt} title={absolute(action.decidedAt)} data-card-time>
            {stamp(action.decidedAt)}
          </time>
        </>
      ) : null}
    </p>
  );
}

/** Whether a card can't be run as it stands (its check found problems, or nothing changes). */
export function blockedCard(action: AssistantAction): boolean {
  const check = action.check;
  const sheet = action.sheetCheck;
  return (
    ((action.op === 'edit.apply' || !!action.op?.match(/^(conversation|sound)\./)) && check?.valid === false) ||
    (action.op === 'cut.apply' && (sheet?.valid === false || sheet?.noChanges === true))
  );
}

/** How a card's outcome reads once its work has finished (the same line Claude's note gives the person). */
export function outcomeLine(a: AssistantAction): string {
  const w = a.work;
  if (w && w.state !== 'running' && w.outcome) {
    if (a.savesVersion && a.result?.startsWith('Saved as ')) return `${a.result.split('. ')[0]!.replace(/\.$/, '')}. ${w.outcome}`;
    return w.outcome;
  }
  return plainResult(a.result ?? '');
}

/** The pictures of a card to show large: test frames, then the preview. */
export function cardPictures(a: AssistantAction): LightboxItem[] {
  const items: LightboxItem[] = (a.frames ?? []).map((f) => ({ src: f.url, label: f.label }));
  if (!items.length && a.image) items.push({ src: a.image, label: 'Test frames' });
  if (a.preview) items.push({ src: a.preview, kind: 'video', label: 'Preview' });
  return items;
}

export function ActionCard({ action, conversation, raw, onNavigate }: { action: AssistantAction; conversation: Conversation; raw: boolean; onNavigate: () => void }) {
  const act = useAssistantOp('assistant.act');
  const dialog = useDialog();
  const signoffs = useSignoffReminder();
  const navigate = useNavigate();
  const me = useViewerId();
  // Whose it is (D179): for the person whose message Claude was answering;
  // anyone here may decide it, and the card says who did.
  const forOther = forSomeoneElse(action, me) ? nameOf(action.forName) : '';
  const [details, setDetails] = useState(false);
  const [more, setMore] = useState(false);
  const [box, setBox] = useState<number | null>(null);
  const [notQuite, setNotQuite] = useState(false);
  const pending = action.state === 'pending';
  const running = action.state === 'running';
  const runnable = pending && !!action.op && conversation.here;
  const check = action.check;
  const diff = check?.diff;
  // A cut sheet Claude wrote: the changes in words and the changed lines.
  const sheet = action.sheetCheck;
  const sheetProblems = sheet?.problems ?? [];
  const blocked = blockedCard(action);
  const label = labelFor(action);
  const pictures = useMemo(() => cardPictures(action), [action]);
  const work = action.work;
  const askFirst = useRecipeCardQuestion(); // Recipes: a run's agreement, what making a version current changes (D211)

  const run = async (dismiss: boolean, reviewCopy = false) => {
    if (!dismiss && action.recipe) { // Recipes (D211)
      if (!(await askFirst(action))) return;
    } else if (!dismiss && action.humanOnly) {
      // Approving while sign-offs are still waiting: the same dialog reminds first (never a lock).
      const waiting = approvesVersion(action) && conversation.projectId ? await signoffs.waiting(conversation.projectId) : [];
      const why = action.details?.[0] ?? 'This is your decision. Claude only suggested it.';
      const ok = await dialog.confirm({
        title: action.title + '?',
        body: waiting.length ? (
          <div className="space-y-2">
            <p className="text-fg">
              <SignoffsWaiting waiting={waiting} question="Approve anyway?" />
            </p>
            <p>{why}</p>
          </div>
        ) : (
          <p>{why}</p>
        ),
        confirmLabel: waiting.length ? 'Approve anyway' : label,
        cancelLabel: waiting.length ? 'Not yet' : undefined,
      });
      if (!ok) return;
    }
    // The card itself says how it went (done, working on it, or what went
    // wrong): no toast over the message box (D140).
    act.mutate({ id: conversation.id, actionId: action.id, dismiss, reviewCopy: reviewCopy || undefined });
  };
  // Not quite (D186): the note goes to Claude at once as the person's message.
  const sendNotQuite = (note: string) => act.mutate({ id: conversation.id, actionId: action.id, notQuite: note }, { onSuccess: () => setNotQuite(false) });
  const go = (to: string) => {
    onNavigate();
    navigate(to);
  };

  const badge =
    running ? (
      <Badge tone="accent" icon={<CircleNotch size={11} className="animate-spin" />}>
        Running by itself
      </Badge>
    ) : action.state === 'done' && work?.state === 'running' ? (
      <Badge tone="accent" icon={<CircleNotch size={11} className="animate-spin" />}>
        Working on it
      </Badge>
    ) : action.state === 'done' ? (
      <Badge tone="ok" icon={<CheckCircle size={12} weight="fill" />}>
        Done
      </Badge>
    ) : action.state === 'dismissed' ? (
      <Badge tone="neutral">Not now</Badge>
    ) : action.state === 'not_quite' ? (
      <Badge tone="neutral">Not quite</Badge>
    ) : action.state === 'failed' ? (
      <Badge tone="danger">Did not work</Badge>
    ) : action.state === 'unavailable' ? (
      <Badge tone="neutral">Not from here</Badge>
    ) : forOther ? (
      <Badge tone="neutral">Waiting for {forOther}</Badge>
    ) : (
      <Badge tone="accent">Needs you</Badge>
    );

  return (
    <article
      className={cx('rounded-[14px] p-3.5 hairline transition-shadow', pending ? 'bg-raised shadow-[inset_0_0_0_1px_var(--accent-line)]' : 'bg-raised')}
      aria-label={`Claude asks: ${action.title}`}
      data-waiting={runnable ? action.id : undefined}
      data-card-id={action.id}
    >
      <div className="flex items-start gap-2.5">
        <span className={cx('mt-[1px] grid h-7 w-7 shrink-0 place-items-center rounded-[9px]', pending || running ? 'bg-accent-soft text-accent-text' : 'bg-hover text-subtle')} aria-hidden>
          {action.state === 'unavailable' ? <Prohibit size={15} /> : action.auto ? <Lightning size={15} /> : <HandPalm size={15} />}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h4 className="min-w-0 text-[13.5px] font-semibold leading-snug text-fg">{action.title}</h4>
            {badge}
            {action.setUp?.length && pending ? <Badge tone="accent">One-time setup first</Badge> : null}
          </div>
          {action.autoWaits && pending ? (
            <p className="mt-0.5 text-[12px] text-subtle" data-auto-waits>
              {action.autoWaits}{' '}
              {action.estimate ? (
                <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => go('/settings/integrations')}>
                  Set the limits
                </button>
              ) : null}
            </p>
          ) : null}
          {action.autoMode ? (
            <p className="mt-0.5 text-[12px] text-subtle" data-auto-done data-auto-priced={action.autoPriced ? '' : undefined}>
              {action.state === 'running' ? 'Running by itself' : action.state === 'failed' ? 'Tried by itself' : 'Done by itself'} — Auto is on
              {action.autoPriced ? ', and your estimates are off' : ''}.{' '}
              <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => (action.autoPriced ? go('/settings/preferences') : openModeSwitch())}>
                Change
              </button>
              {action.decidedAt ? (
                <>
                  {' · '}
                  <time dateTime={action.decidedAt} title={absolute(action.decidedAt)} data-card-time>
                    {stamp(action.decidedAt)}
                  </time>
                </>
              ) : null}
            </p>
          ) : action.auto ? (
            <p className="mt-0.5 text-[12px] text-subtle">
              Started by itself: a safe step, as your setting allows.{' '}
              <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => go('/settings/claude#claude-code')}>
                Change
              </button>
              {action.decidedAt ? (
                <>
                  {' · '}
                  <time dateTime={action.decidedAt} title={absolute(action.decidedAt)} data-card-time>
                    {stamp(action.decidedAt)}
                  </time>
                </>
              ) : null}
            </p>
          ) : null}
          <DecidedLine action={action} me={me} />
          {action.recipe ? <RecipeCardBody action={action} onOpen={go} /> /* Recipes: the plan, the proposal or what changes, instead of the lines (D211) */ : action.details?.length ? (
            <ul className="mt-1 space-y-0.5">
              {action.details.map((d, i) => (
                <li key={i} className="text-[12.5px] leading-relaxed text-muted">
                  {d}
                </li>
              ))}
            </ul>
          ) : null}
          {action.estimate ? <CostEstimate estimate={action.estimate} className="mt-1.5" /> : null}
        </div>
      </div>
      {action.rationale ? (
        <div className="mt-2.5">
          <p className={cx('text-[12.5px] leading-relaxed text-muted', !more && 'line-clamp-3')}>{action.rationale}</p>
          {action.rationale.length > 220 ? (
            <button type="button" className="mt-0.5 text-[12px] font-medium text-accent-text hover:underline" onClick={() => setMore((v) => !v)} aria-expanded={more}>
              {more ? 'Show less' : 'Show more'}
            </button>
          ) : null}
        </div>
      ) : null}

      {pictures.length ? <CardPictures action={action} pictures={pictures} onOpen={setBox} /> : null}
      {box != null ? <Lightbox items={pictures} index={box} title={action.title} onClose={() => setBox(null)} /> : null}
      {action.review && pending ? (
        <div className="mt-2">
          <Button size="sm" variant="secondary" icon={<ArrowSquareOut size={13} />} onClick={() => go(action.review!)}>
            {action.op === 'voiceover.fitted' ? 'Review it line by line' : 'Review it first'}
          </Button>
        </div>
      ) : null}

      {action.state === 'unavailable' && action.unavailable ? <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted">{action.unavailable}</p> : null}
      {running ? <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted">Running now. Claude hears how it went when it’s done.</p> : null}
      {action.state === 'done' && (action.result || work?.outcome) ? <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted">{outcomeLine(action)}</p> : null}
      {action.state === 'done' && work?.state === 'running' ? (
        <p className="mt-1 flex items-center gap-1.5 text-[12px] text-subtle">
          <CircleNotch size={12} className="animate-spin text-accent-text" aria-hidden />
          Working on {work.what}. Claude hears when it’s done.
        </p>
      ) : null}
      {action.state === 'failed' && action.error ? (
        <p className="mt-2.5 flex gap-2 text-[12.5px] leading-relaxed text-danger">
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0" aria-hidden />
          <InlineCode text={action.error} />
        </p>
      ) : null}

      {diff || sheet || check?.warnings?.length || check?.errors?.length || (raw && action.command) ? (
        <div className="mt-2.5">
          <button type="button" onClick={() => setDetails((v) => !v)} aria-expanded={details} className="flex items-center gap-1 text-[12px] font-medium text-muted hover:text-fg">
            {details ? <CaretDown size={11} /> : <CaretRight size={11} />}
            Details
          </button>
          {details ? (
            <div className="mt-2 space-y-2">
              {diff ? (
                <div className="rounded-[10px] bg-panel px-3 py-2.5 hairline">
                  <div className="tc flex flex-wrap items-baseline gap-x-3 text-[12px]">
                    <span className="text-fg">
                      {duration(diff.durationBefore)} → {duration(diff.durationAfter)}
                    </span>
                    <span className={cx(diff.durationAfter < diff.durationBefore - 0.05 ? 'text-ok' : 'text-subtle')}>{signedDuration(diff.durationAfter - diff.durationBefore)}</span>
                  </div>
                  {diff.summary?.filter((s) => !/^duration /.test(s)).length ? (
                    <ul className="mt-1.5 space-y-0.5">
                      {diff.summary
                        .filter((s) => !/^duration /.test(s))
                        .map((s, i) => (
                          <li key={i} className="flex gap-2 text-[12.5px] leading-relaxed text-fg">
                            <span className="mt-[8px] h-1 w-1 shrink-0 rounded-full bg-accent" aria-hidden />
                            {s}
                          </li>
                        ))}
                    </ul>
                  ) : null}
                </div>
              ) : null}
              {sheet ? (
                <div className="space-y-2">
                  {sheet.changes?.length ? (
                    <ul className="space-y-0.5 rounded-[10px] bg-panel px-3 py-2.5 hairline" aria-label="What changes">
                      {sheet.changes.map((c, i) => (
                        <li key={i} className="flex gap-2 text-[12.5px] leading-relaxed text-fg">
                          <span className="mt-[8px] h-1 w-1 shrink-0 rounded-full bg-accent" aria-hidden />
                          {c}
                        </li>
                      ))}
                    </ul>
                  ) : null}
                  {sheetProblems.map((x, k) => (
                    <p key={k} className={cx('flex gap-2 text-[12px] leading-relaxed', x.severity === 'error' ? 'text-danger' : 'text-accent-text')}>
                      <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" aria-hidden />
                      <span className="min-w-0">
                        <span className="sr-only">{x.severity === 'error' ? 'Problem: ' : 'Note: '}</span>
                        {x.line ? `Line ${x.line}: ` : ''}
                        {x.message}
                        {x.hint ? <span className="text-muted"> {x.hint}</span> : null}
                      </span>
                    </p>
                  ))}
                  {action.sheetLines?.length ? <SheetDiff lines={action.sheetLines} /> : null}
                </div>
              ) : null}
              {[...(check?.errors ?? []).map((x) => ({ ...x, tone: 'danger' as const })), ...(check?.warnings ?? []).map((x) => ({ ...x, tone: 'warn' as const }))].map((x, k) => (
                <p key={k} className={cx('flex gap-2 text-[12px] leading-relaxed', x.tone === 'danger' ? 'text-danger' : 'text-accent-text')}>
                  <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" aria-hidden />
                  <span className="min-w-0">
                    <span className="sr-only">{x.tone === 'danger' ? 'Problem: ' : 'Note: '}</span>
                    <InlineCode text={x.message} />
                  </span>
                </p>
              ))}
              {raw && action.command ? <pre className="tc whitespace-pre-wrap break-words rounded-[10px] bg-inset px-3 py-2 text-[11px] leading-relaxed text-[#e9dcc8]">$ {action.command}</pre> : null}
            </div>
          ) : null}
        </div>
      ) : null}

      {action.state === 'not_quite' ? <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted">Sent back to Claude to change. Its new version comes as a new card.</p> : null}

      {act.error && !notQuite ? <ErrorPanel error={act.error} compact className="mt-2.5" title="Could not do that" /> : null}

      {runnable && notQuite ? (
        <NotQuiteBox onSend={sendNotQuite} onCancel={() => setNotQuite(false)} busy={act.isPending} error={act.variables?.notQuite ? act.error : null} />
      ) : runnable ? (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {action.savesVersion ? (
            <>
              <Button size="sm" variant="primary" loading={act.isPending && !!act.variables?.reviewCopy} disabled={act.isPending || blocked} onClick={() => void run(false, true)}>
                Save and render a review copy
              </Button>
              <Button size="sm" variant="secondary" loading={act.isPending && !act.variables?.reviewCopy && !act.variables?.dismiss} disabled={act.isPending || blocked} onClick={() => void run(false)}>
                Save only
              </Button>
            </>
          ) : (
            <Button size="sm" variant="primary" loading={act.isPending && !act.variables?.dismiss} disabled={act.isPending || blocked} onClick={() => void run(false)}>
              {label}
            </Button>
          )}
          <Button size="sm" variant="ghost" disabled={act.isPending} onClick={() => setNotQuite(true)} title="Say what should change: Claude makes a new version" data-not-quite>
            Not quite…
          </Button>
          <Button size="sm" variant="ghost" disabled={act.isPending} onClick={() => void run(true)} title="Leave it for now: Claude waits, and may bring it up again">
            Not now
          </Button>
        </div>
      ) : pending && !conversation.here ? (
        <p className="mt-2.5 text-[12px] text-subtle">You can do this on {conversation.installationName}.</p>
      ) : action.state === 'done' && (work?.link || action.link) && work?.state !== 'running' ? (
        <div className="mt-2.5">
          <Button size="sm" variant="secondary" icon={<ArrowSquareOut size={13} />} onClick={() => go(work?.link || action.link!)}>
            Open
          </Button>
        </div>
      ) : null}
    </article>
  );
}

/** A frame's shape when the graphic was drawn in several ("vertical · start", D164). */
const SHAPE_LABEL = /^(widescreen|vertical|square) · (.*)$/;
const SHAPE_RATIO: Record<string, string> = { widescreen: '16 / 9', vertical: '9 / 16', square: '1 / 1' };
const SHAPE_MIN: Record<string, number> = { widescreen: 92, vertical: 52, square: 70 };

/** The stills grouped by the shape they were drawn in, or undefined when they carry none. */
function shapeGroups(stills: LightboxItem[]): { shape: string; items: { p: LightboxItem; i: number; label: string }[] }[] | undefined {
  if (stills.length < 2 || !stills.every((p) => SHAPE_LABEL.test(p.label ?? ''))) return undefined;
  const out: { shape: string; items: { p: LightboxItem; i: number; label: string }[] }[] = [];
  stills.forEach((p, i) => {
    const m = SHAPE_LABEL.exec(p.label ?? '')!;
    let g = out.find((x) => x.shape === m[1]);
    if (!g) out.push((g = { shape: m[1]!, items: [] }));
    g.items.push({ p, i, label: m[2]! });
  });
  return out;
}

/** A card's pictures: a strip of test frames (and the preview), each opening large. */
export function CardPictures({ action, pictures, onOpen }: { action: AssistantAction; pictures: LightboxItem[]; onOpen: (i: number) => void }) {
  const stills = pictures.filter((p) => p.kind !== 'video');
  const video = pictures.findIndex((p) => p.kind === 'video');
  const groups = shapeGroups(stills);
  if (groups) {
    // Drawn in the shapes of the cuts it is for: a row per shape, each frame at its own shape.
    return (
      <div className="mt-2.5 space-y-2" aria-label="Test frames" data-card-shapes>
        {groups.map((g) => (
          <div key={g.shape} data-card-shape={g.shape}>
            <div className="mb-1 text-[11.5px] font-medium text-subtle">{g.shape.charAt(0).toUpperCase() + g.shape.slice(1)}</div>
            <div className="grid gap-1.5" style={{ gridTemplateColumns: `repeat(auto-fill, minmax(${SHAPE_MIN[g.shape] ?? 80}px, 1fr))` }} role="list">
              {g.items.slice(0, 8).map(({ p, i, label }) => (
                <button
                  key={p.src}
                  type="button"
                  role="listitem"
                  onClick={() => onOpen(i)}
                  className="group relative overflow-hidden rounded-[8px] bg-inset hairline focus-visible:outline-2 focus-visible:outline-accent"
                  style={{ aspectRatio: SHAPE_RATIO[g.shape] ?? '16 / 9' }}
                  aria-label={`Open the ${g.shape} test frame ${label}, large`}
                  title={label}
                  data-thumb
                >
                  <img src={p.src} alt="" loading="lazy" className="h-full w-full object-cover transition-transform duration-300 group-hover:scale-[1.03]" />
                </button>
              ))}
            </div>
          </div>
        ))}
        {video >= 0 ? (
          <button
            type="button"
            onClick={() => onOpen(video)}
            className="inline-flex h-8 items-center gap-1.5 rounded-[8px] bg-inset px-3 text-[12px] font-medium text-[#efe8de] hairline hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-accent"
            aria-label="Play the preview, large"
            data-thumb
          >
            <Play size={13} weight="fill" aria-hidden /> Preview
          </button>
        ) : null}
      </div>
    );
  }
  if (!action.frames?.length && action.image && stills.length === 1) {
    // Only the contact sheet: one picture, as before, but it opens large.
    return (
      <button type="button" onClick={() => onOpen(0)} className="mt-2.5 block w-full overflow-hidden rounded-[10px] bg-inset hairline focus-visible:outline-2 focus-visible:outline-accent" aria-label="Open the test frames, large" data-thumb>
        <img src={stills[0]!.src} alt="Test frames" className="block h-auto w-full" loading="lazy" />
      </button>
    );
  }
  return (
    <div className="mt-2.5 grid grid-cols-3 gap-1.5 sm:grid-cols-4" role="list" aria-label="Test frames">
      {stills.slice(0, 7).map((p, i) => (
        <button
          key={p.src}
          type="button"
          role="listitem"
          onClick={() => onOpen(i)}
          className="group relative aspect-video overflow-hidden rounded-[8px] bg-inset hairline focus-visible:outline-2 focus-visible:outline-accent"
          aria-label={`Open ${p.label ?? 'this test frame'}, large`}
          title={p.label}
          data-thumb
        >
          <img src={p.src} alt="" loading="lazy" className="h-full w-full object-cover transition-transform duration-300 group-hover:scale-[1.03]" />
          {i === 6 && stills.length > 7 ? <span className="tc absolute inset-0 grid place-items-center bg-[rgb(0_0_0/0.5)] text-[13px] font-medium text-white">+{stills.length - 7}</span> : null}
        </button>
      ))}
      {video >= 0 ? (
        <button
          type="button"
          role="listitem"
          onClick={() => onOpen(video)}
          className="grid aspect-video place-items-center rounded-[8px] bg-inset text-[12px] font-medium text-fg hairline hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent"
          aria-label="Play the preview, large"
          data-thumb
        >
          <span className="flex items-center gap-1.5">
            <Play size={14} weight="fill" aria-hidden /> Preview
          </span>
        </button>
      ) : null}
    </div>
  );
}

/** Results are written for Claude ("render rnd_… of r2 is queued"); show the plain part. */
export function plainResult(r: string): string {
  return r
    .replace(/\b(rnd|rev|prj|ast)_[0-9a-z]{26}\b\s*/g, '')
    .replace(/\s*\(version [a-z0-9-]+@[0-9a-f]{8,64}\)/g, '')
    .replace(/@[0-9a-f]{8,64}\b/g, '')
    .replace(/\(\s*\)/g, '')
    .replace(/\s{2,}/g, ' ')
    .replace(/^\w/, (c) => c.toUpperCase())
    .trim();
}
