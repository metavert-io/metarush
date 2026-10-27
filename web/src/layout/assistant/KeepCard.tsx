import { useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import {
  ArrowSquareOut,
  BookmarkSimple,
  CaretDown,
  CaretRight,
  CheckCircle,
  CircleNotch,
  FilePdf,
  Globe,
  Image as ImageIcon,
  Lightbulb,
  ListChecks,
  Prohibit,
  Seal,
  ShieldCheck,
  Signature,
  Target,
  TextAa,
  User,
  WarningCircle,
} from '@phosphor-icons/react';
import { useAssistantOp } from '../../api/assistant';
import type { AssistantAction, Conversation, KeepKind } from '../../api/types';
import { Lightbox } from '../../ui/Lightbox';
import { Badge, Button, ErrorPanel, InlineCode, Theirs, cx } from '../../ui/primitives';
import { outcomeLine } from './ActionCard';

/*
 * "Keep this?" (D166): something a conversation brought in that the project
 * could keep — a link the person typed, a logo, the brief, a rule, a sign-off,
 * a spelling. MetaRush asks once, never keeps anything by itself, and never asks
 * again after Keep or Not now. Each card is decided on its own (never part of a
 * combined approval); the navigator by the message box counts it like a question.
 */

const ICON: Record<KeepKind, ReactNode> = {
  source: <Globe size={15} />,
  picture: <ImageIcon size={15} />,
  brief: <Target size={15} />,
  rule: <ListChecks size={15} />,
  promise: <Seal size={15} />,
  check: <ShieldCheck size={15} />,
  person: <User size={15} />,
  signoff: <Signature size={15} />,
  improve: <Lightbulb size={15} />,
  spelling: <TextAa size={15} />,
};

/** What keeping a thing can change (refreshed after Keep or Not now). */
const KEPT_QUERIES = ['keep.list', 'project.rules.list', 'cut.signoffs.list', 'project.show', 'resources.list', 'brand.show', 'transcript.spelling.list'];

/** Kinds whose words are the person's (shown quoted under the question). */
const QUOTED = new Set<KeepKind>(['brief', 'rule', 'check', 'person', 'signoff', 'improve']);

/** Whether a colour from the server is one to paint (#rgb, #rrggbb, #rrggbbaa). */
const isColour = (c: string) => /^#(?:[0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(c);

export function KeepCard({ action, conversation, raw, onNavigate }: { action: AssistantAction; conversation: Conversation; raw: boolean; onNavigate: () => void }) {
  const k = action.keeping!;
  const act = useAssistantOp('assistant.act');
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [big, setBig] = useState(false);
  const [details, setDetails] = useState(false);
  const pending = action.state === 'pending';
  const runnable = pending && !!action.op && conversation.here;
  const work = action.work;
  const text = (k.text ?? '').trim();
  const pdf = k.kind === 'picture' && (k.mime === 'application/pdf' || /\.pdf$/i.test(text));
  const quote = QUOTED.has(k.kind) && text ? `“${text}”` : '';
  // The card's other lines (where a logo came from), without the words and the sentence shown above.
  const extra = (action.details ?? []).filter((d) => d !== quote && d !== k.does && d.trim() !== '');
  const colours = (k.colors ?? []).filter(isColour).slice(0, 6);
  const projectPage = conversation.projectId ? `/p/${conversation.projectId}?tab=settings` : undefined;
  const asker = k.from === 'claude' ? 'Claude' : 'MetaRush';

  // What the project keeps changes at once wherever it shows (the project's page behind the panel).
  const decide = (dismiss: boolean, where?: string) =>
    act.mutate(
      { id: conversation.id, actionId: action.id, ...(dismiss ? { dismiss: true } : {}), ...(where ? { keepWhere: where as 'project' | 'settings' } : {}) },
      { onSettled: () => KEPT_QUERIES.forEach((key) => void qc.invalidateQueries({ queryKey: [key] })) },
    );
  const go = (to: string) => {
    onNavigate();
    navigate(to);
  };

  const keeping = act.isPending && !act.variables?.dismiss && pending;
  const badge =
    keeping || action.state === 'running' ? (
      <Badge tone="accent" icon={<CircleNotch size={11} className="animate-spin" />}>
        Keeping it
      </Badge>
    ) : action.state === 'done' && work?.state === 'running' ? (
      <Badge tone="accent" icon={<CircleNotch size={11} className="animate-spin" />}>
        Working on it
      </Badge>
    ) : action.state === 'done' ? (
      <Badge tone="ok" icon={<CheckCircle size={12} weight="fill" />}>
        Kept
      </Badge>
    ) : action.state === 'dismissed' ? (
      <Badge tone="neutral">Not kept</Badge>
    ) : action.state === 'failed' ? (
      <Badge tone="danger">Did not work</Badge>
    ) : action.state === 'unavailable' ? (
      <Badge tone="neutral">Not from here</Badge>
    ) : (
      <Badge tone="accent">Needs you</Badge>
    );

  return (
    <article
      className={cx('rounded-[14px] bg-raised p-3.5 hairline', pending && 'shadow-[inset_0_0_0_1px_var(--accent-line)]')}
      aria-label={`${asker} asks: ${action.title}`}
      data-waiting={runnable ? action.id : undefined}
      data-keep={k.kind}
      data-keep-state={action.state}
    >
      <div className="flex items-start gap-2.5">
        <span className={cx('mt-[1px] grid h-7 w-7 shrink-0 place-items-center rounded-[9px]', pending ? 'bg-accent-soft text-accent-text' : 'bg-hover text-subtle')} aria-hidden>
          {action.state === 'unavailable' ? <Prohibit size={15} /> : pdf ? <FilePdf size={15} /> : (ICON[k.kind] ?? <BookmarkSimple size={15} />)}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h4 className="min-w-0 text-[13.5px] font-semibold leading-snug text-fg">{action.title}</h4>
            {badge}
          </div>
          {quote ? (
            <p className="mt-1 text-[13px] leading-relaxed text-fg" data-keep-words>
              <Theirs>{quote}</Theirs>
            </p>
          ) : null}
          {k.does ? <p className="mt-1 text-[12.5px] leading-relaxed text-muted">{k.does}</p> : null}
          {extra.map((d, i) => (
            <p key={i} className="mt-0.5 text-[12px] leading-relaxed text-subtle">
              {d}
            </p>
          ))}
        </div>
      </div>

      {action.image ? (
        <div className="mt-2.5 flex min-w-0 items-center gap-3">
          <button
            type="button"
            onClick={() => setBig(true)}
            className="grid h-[84px] w-[120px] shrink-0 place-items-center overflow-hidden rounded-[10px] bg-panel p-1.5 hairline focus-visible:outline-2 focus-visible:outline-accent"
            style={{ backgroundImage: 'repeating-conic-gradient(rgb(128 128 128 / 0.16) 0% 25%, transparent 0% 50%)', backgroundSize: '14px 14px' }}
            aria-label="Open the picture, large"
            data-thumb
          >
            <img src={action.image} alt="" className="max-h-full max-w-full object-contain" loading="lazy" draggable={false} />
          </button>
          <div className="min-w-0">
            {k.kind === 'picture' && text ? (
              <Theirs className="block truncate text-[12.5px] text-fg" title={text}>
                “{text}”
              </Theirs>
            ) : null}
            {colours.length ? (
              <div className="mt-1.5" data-keep-colours>
                <span className="block text-[11.5px] text-subtle">Its colours</span>
                <span className="mt-1 flex flex-wrap items-center gap-1.5">
                  {colours.map((c) => (
                    <span key={c} title={c} className="h-5 w-5 rounded-full shadow-[inset_0_0_0_1px_rgb(128_128_128/0.35)]" style={{ background: c }} />
                  ))}
                </span>
              </div>
            ) : null}
          </div>
        </div>
      ) : null}
      {big && action.image ? <Lightbox items={[{ src: action.image, label: k.kind === 'picture' ? text : undefined }]} index={0} title={action.title} onClose={() => setBig(false)} /> : null}
      {!action.image && k.kind === 'picture' && text ? (
        // A PDF (or a picture not on this computer): its name.
        <p className="mt-2.5 inline-flex max-w-full items-center gap-1.5 rounded-[9px] bg-panel px-2.5 py-1.5 text-[12.5px] text-fg hairline" data-keep-file>
          {pdf ? <FilePdf size={15} className="shrink-0 text-subtle" aria-hidden /> : <ImageIcon size={15} className="shrink-0 text-subtle" aria-hidden />}
          <Theirs className="min-w-0 truncate" title={text}>
            “{text}”
          </Theirs>
        </p>
      ) : null}

      {action.state === 'done' && (action.result || work?.outcome) ? (
        <p className="mt-2.5 flex gap-1.5 text-[12.5px] leading-relaxed text-muted" data-keep-outcome>
          <CheckCircle size={14} weight="fill" className="mt-[2px] shrink-0 text-ok" aria-hidden />
          <span className="min-w-0">{outcomeLine(action)}</span>
        </p>
      ) : null}
      {action.state === 'done' && work?.state === 'running' ? (
        <p className="mt-1 flex items-center gap-1.5 text-[12px] text-subtle">
          <CircleNotch size={12} className="animate-spin text-accent-text" aria-hidden />
          Working on {work.what}. Claude hears when it’s done.
        </p>
      ) : null}
      {action.state === 'dismissed' ? (
        <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted" data-keep-outcome>
          Not kept; MetaRush won't ask again.
          {projectPage ? (
            <>
              {' '}
              <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => go(projectPage)}>
                Keep it later from the project's page
              </button>
            </>
          ) : null}
        </p>
      ) : null}
      {action.state === 'failed' && action.error ? (
        <p className="mt-2.5 flex gap-2 text-[12.5px] leading-relaxed text-danger">
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0" aria-hidden />
          <InlineCode text={action.error} />
        </p>
      ) : null}
      {action.state === 'unavailable' && action.unavailable ? <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted">{action.unavailable}</p> : null}

      {raw && action.command ? (
        <div className="mt-2.5">
          <button type="button" onClick={() => setDetails((v) => !v)} aria-expanded={details} className="flex items-center gap-1 text-[12px] font-medium text-muted hover:text-fg">
            {details ? <CaretDown size={11} /> : <CaretRight size={11} />}
            Details
          </button>
          {details ? <pre className="tc mt-2 whitespace-pre-wrap break-words rounded-[10px] bg-inset px-3 py-2 text-[11px] leading-relaxed text-[#e9dcc8]">$ {action.command}</pre> : null}
        </div>
      ) : null}

      {act.error ? <ErrorPanel error={act.error} compact className="mt-2.5" title={act.variables?.dismiss ? 'Could not set it aside' : 'Could not keep that'} /> : null}

      {runnable ? (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {k.where?.length ? (
            // Where a rule goes (D193): just this project, or every project using its settings.
            <>
              <span className="basis-full text-[12px] text-muted">Keep it for:</span>
              {k.where.map((w, i) => (
                <Button
                  key={w.value}
                  size="sm"
                  variant={i === 0 ? 'primary' : 'secondary'}
                  icon={i === 0 ? <BookmarkSimple size={13} weight="bold" /> : undefined}
                  loading={keeping && act.variables?.keepWhere === w.value}
                  disabled={act.isPending}
                  onClick={() => decide(false, w.value)}
                  data-keep-yes={i === 0 ? '' : undefined}
                  data-keep-where={w.value}
                >
                  {w.label}
                </Button>
              ))}
            </>
          ) : (
            <Button size="sm" variant="primary" icon={<BookmarkSimple size={13} weight="bold" />} loading={keeping} disabled={act.isPending} onClick={() => decide(false)} data-keep-yes>
              Keep
            </Button>
          )}
          {/* Keeping's no is for good (D166): MetaRush never asks about it again in this project. */}
          <Button
            size="sm"
            variant="ghost"
            loading={act.isPending && !!act.variables?.dismiss}
            disabled={act.isPending}
            onClick={() => decide(true)}
            title="MetaRush won't ask about this again in this project"
            data-keep-no
          >
            Don't keep
          </Button>
        </div>
      ) : pending && !conversation.here ? (
        <p className="mt-2.5 text-[12px] text-subtle">You can decide this on {conversation.installationName}.</p>
      ) : action.state === 'done' && (work?.link || action.link) && work?.state !== 'running' ? (
        <div className="mt-2.5">
          <Button size="sm" variant="secondary" icon={<ArrowSquareOut size={13} />} onClick={() => go(work?.link || action.link!)}>
            {(work?.link || action.link)?.includes('tab=settings') ? "See the project's settings" : 'Open'}
          </Button>
        </div>
      ) : null}
    </article>
  );
}
