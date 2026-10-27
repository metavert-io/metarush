import { lazy, Suspense, useEffect, useId, useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { ContentPackOut, OpInput, PackCutOffer, PackPreview, PackRoleOut, RemoteOp } from '../../api/types';
import { relative } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, ErrorPanel, Skeleton, Spinner, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

const LoopPreview = lazy(() => import('../../player/LoopPreview'));

/*
 * The layout pack (D263–D267): a Content Settings part. Which template — or
 * which approved motion graphic — draws each title card, lower third, end
 * screen, quote card and chapter card of every project using the settings,
 * and how call recordings' picture-in-picture and side-by-side layouts look,
 * each previewed in the brand and house style (the vetted templates play in
 * the player; a motion graphic shows the stills its check drew; layouts are
 * the boxes Go places). A pack Claude proposed waits here for a person to
 * accept it once its graphics are approved. Every control is one op, the
 * same as `metarush content pack show|set|propose|accept|decline|apply|preview`.
 */

const PACK_QUERIES: RemoteOp[] = ['content.pack.show', 'content.pack.preview', 'content.show', 'motion.list', 'project.show', 'revision.list', 'cut.show'];

type SetIn = OpInput<'content.pack.set'>;
type Choice = { value: string; label: string };

const TEMPLATE_WORDS: Record<string, string> = {
  'title.card': 'Title card',
  'slide.title': 'Title slide',
  'lower-third.clean': 'Clean name title',
  'lower-third.cinematic': 'Cinematic name title',
  'endcard.cta': 'End card with a call to action',
  'end.screen': 'End screen with room for the next videos',
  'slide.quote': 'Quote slide',
  'quote.kinetic': 'Word-by-word quote',
};

const SHAPES: Choice[] = [
  { value: '', label: "The latest cut's shape" },
  { value: 'landscape', label: 'Landscape' },
  { value: 'vertical', label: 'Vertical' },
  { value: 'square', label: 'Square' },
];
const CORNERS: Choice[] = [
  { value: '', label: 'Bottom right (MetaRush’s own)' },
  { value: 'bottom-left', label: 'Bottom left' },
  { value: 'top-right', label: 'Top right' },
  { value: 'top-left', label: 'Top left' },
];
const SIZES: Choice[] = [
  { value: '', label: 'Medium (MetaRush’s own)' },
  { value: 'small', label: 'Small' },
  { value: 'large', label: 'Large' },
];
const SHAPES_PIP: Choice[] = [
  { value: '', label: 'Rounded (MetaRush’s own)' },
  { value: 'square', label: 'Square corners' },
  { value: 'circle', label: 'A circle' },
];
const FRAMES: Choice[] = [
  { value: '', label: 'No line' },
  { value: 'accent', label: "A line in the brand's accent" },
  { value: 'primary', label: "A line in the brand's main colour" },
  { value: 'text', label: "A line in the brand's text colour" },
];
const GAPS: Choice[] = [
  { value: '', label: 'Thin (MetaRush’s own)' },
  { value: 'none', label: 'No gap' },
  { value: 'wide', label: 'Wide, with a margin' },
];
const BACKDROPS: Choice[] = [
  { value: '', label: 'Black' },
  { value: 'brand', label: "The brand's background" },
];

const field = 'h-9 w-full min-w-0 rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';

function Pick({
  label,
  value,
  choices,
  onChange,
  disabled,
  name,
}: {
  label: string;
  value: string;
  choices: Choice[];
  onChange: (v: string) => void;
  disabled?: boolean;
  name: string;
}) {
  const id = useId();
  if (value && !choices.some((c) => c.value === value)) choices = [...choices, { value, label: value }];
  return (
    <label htmlFor={id} className="block min-w-0">
      <span className="mb-1 block text-[12px] font-medium text-muted">{label}</span>
      <select id={id} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} className={field} data-pack-field={name}>
        {choices.map((c) => (
          <option key={c.value} value={c.value}>
            {c.label}
          </option>
        ))}
      </select>
    </label>
  );
}

/** The layout pack part: the roles in the house style, a proposed pack, the latest cuts' offers. */
export function LayoutPackPart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const q = useOp('content.pack.show', { projectId });
  const [shape, setShape] = useState('');
  const [draftView, setDraftView] = useState(false);
  const preview = useOp('content.pack.preview', {
    projectId,
    shape: (shape || undefined) as OpInput<'content.pack.preview'>['shape'],
    draft: draftView || undefined,
  });
  const motion = useOp('motion.list', { state: 'approved' }, { enabled: !readOnly, staleTime: 60_000 });
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const [busy, setBusy] = useState<string | null>(null);
  const [offers, setOffers] = useState<PackCutOffer[]>([]);
  const [name, setName] = useState('');
  useEffect(() => setName(q.data?.own ? (q.data.pack.name ?? '') : ''), [q.data?.own, q.data?.pack.name]);
  useEffect(() => {
    if (!q.data?.draft) setDraftView(false);
  }, [q.data?.draft]);
  const refresh = () => {
    for (const k of PACK_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
  const run = async (key: string, fn: () => Promise<ContentPackOut>, ok: (out: ContentPackOut) => string, failed = "The layout pack didn't change") => {
    setBusy(key);
    try {
      const out = await fn();
      refresh();
      setOffers(out.latestCuts ?? []);
      toast({ tone: 'success', title: ok(out), body: out.summary });
      return true;
    } catch (e) {
      const err = toApiError(e);
      toast({
        tone: 'error',
        title: failed,
        body: [err.message, err.hint].filter(Boolean).join(' '),
      });
      return false;
    } finally {
      setBusy(null);
    }
  };
  const save = (key: string, input: SetIn) =>
    run(
      key,
      () => call('content.pack.set', { ...input, projectId }),
      () => 'Layout pack saved',
    );
  const accept = async () => {
    const d = q.data?.draft;
    if (!d) return;
    const ok = await dialog.confirm({
      title: 'Make the proposed pack the layout pack?',
      body: (
        <p>
          New cuts of every project using these settings draw their graphics with it
          {q.data?.usedBy ? ` (also used by ${q.data.usedBy} other ${q.data.usedBy === 1 ? 'project' : 'projects'})` : ''}. Saved versions don't change; the latest cuts are offered
          it.
        </p>
      ),
      confirmLabel: 'Use this pack',
    });
    if (!ok) return;
    await run(
      'accept',
      // Exactly the proposed pack shown here: one proposed since is refused.
      () => call('content.pack.accept', { projectId, draftAt: d.at }),
      () => 'The proposed pack is the layout pack now',
      "The proposed pack isn't the layout pack yet",
    );
  };
  const decline = () =>
    run(
      'decline',
      () => call('content.pack.decline', { projectId }),
      () => 'Proposed pack set aside',
    );
  // A newer approved version of a shared library the pack's graphics use (D171): the person moves the pack to it.
  const moveToLibrary = async (name: string, version: string) => {
    const ok = await dialog.confirm({
      title: `Use the newer version of ${name} in the pack?`,
      body: <p>Every graphic of the pack that uses this shared library draws with the approved version. New cuts get it; the latest cuts are offered it.</p>,
      confirmLabel: 'Use it',
    });
    if (ok) await save(`library:${name}`, { library: version });
  };
  const reset = async () => {
    const ok = await dialog.confirm({
      title: "Go back to MetaRush's own layout pack?",
      body: <p>Every project using these settings draws new graphics with MetaRush's own templates, in the house style. Saved versions don't change.</p>,
      confirmLabel: "Use MetaRush's own",
      tone: 'danger',
    });
    if (ok) await save('reset', { reset: true });
  };
  const applyToCut = async (o: PackCutOffer) => {
    setBusy(`apply:${o.projectId}`);
    try {
      const out = await call('content.pack.apply', {
        projectId: o.projectId,
        instruction: 'Use the layout pack',
      });
      refresh();
      const problems = out.problems.filter((x) => x.severity === 'error');
      if (problems.length)
        toast({
          tone: 'error',
          title: 'Not changed',
          body: problems.map((x) => x.message).join(' '),
        });
      else {
        setOffers((list) => list.filter((x) => x.projectId !== o.projectId));
        toast({
          tone: 'success',
          title: out.noChange ? 'That cut follows the layout pack already' : `${o.projectName}'s latest cut follows the layout pack`,
          body: out.summary,
        });
      }
    } catch (e) {
      const err = toApiError(e);
      toast({
        tone: 'error',
        title: 'Not changed',
        body: [err.message, err.hint].filter(Boolean).join(' '),
      });
    } finally {
      setBusy(null);
    }
  };

  if (q.isLoading) return <Skeleton className="h-40" />;
  if (q.error || !q.data) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  const dis = !!busy;
  const previews = new Map((preview.data?.previews ?? []).map((p) => [p.role, p]));
  const approved = (motion.data?.components ?? []).filter((c) => c.kind !== 'library');
  const pip = d.pack.layouts?.pip ?? {};
  const side = d.pack.layouts?.sideBySide ?? {};
  return (
    <div data-layout-pack data-pack-own={d.own || undefined}>
      <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
        The title cards, lower thirds, end screens, quote and chapter cards and call layouts every project using these settings draws with, in the brand's colours, font and logos.
        A graphic in a cut names its part (in a cut sheet: <code className="text-[12px]">- intro: title card at 0:00 for 3 s</code>) and MetaRush draws it with the pack. Saved
        versions never change.
      </p>
      {d.draft ? (
        <DraftBox
          d={d}
          busy={busy}
          readOnly={readOnly}
          viewing={draftView}
          onView={() => setDraftView((v) => !v)}
          onAccept={() => void accept()}
          onDecline={() => void decline()}
        />
      ) : null}
      {(d.libraryUpdates ?? []).length && !readOnly ? (
        <div className="mb-4 rounded-[14px] bg-accent-soft/60 p-3.5" data-pack-library-updates>
          {(d.libraryUpdates ?? []).map((u) => (
            <div key={u.name} className="flex flex-wrap items-start gap-2 text-[12.5px]">
              <span className="min-w-0 flex-1 text-fg">
                A newer approved version of the shared library <Theirs>{u.name}</Theirs> is there for the {u.roles.join(', ')}.
              </span>
              <Button
                size="sm"
                variant="secondary"
                loading={busy === `library:${u.name}`}
                disabled={!!busy}
                onClick={() => void moveToLibrary(u.name, u.version)}
                data-pack-library={u.name}
              >
                Use it in the pack
              </Button>
            </div>
          ))}
        </div>
      ) : null}
      {offers.length && !readOnly ? (
        <div className="mb-4 rounded-[14px] bg-accent-soft/60 p-3.5" data-pack-offers>
          <p className="text-[13px] font-medium text-fg">Use the layout pack on the latest cut</p>
          <p className="mt-0.5 text-[12px] text-muted">Saved versions never change. Each one below becomes a new version whose graphics follow the pack.</p>
          <ul className="mt-2 space-y-2">
            {offers.map((o) => (
              <li key={o.projectId} className="flex flex-wrap items-start gap-2 text-[12.5px]">
                <span className="min-w-0 flex-1 text-fg">
                  <Theirs>{o.projectName}</Theirs> <span className="text-subtle">· version {o.version}</span>
                  {o.changes.length ? <span className="mt-0.5 block text-[12px] text-muted">{o.changes.slice(0, 3).join(' ')}</span> : null}
                </span>
                <Button size="sm" variant="secondary" loading={busy === `apply:${o.projectId}`} onClick={() => void applyToCut(o)} data-pack-offer={o.projectId}>
                  Use the layout pack
                </Button>
              </li>
            ))}
          </ul>
          <button type="button" className="mt-2 text-[12px] text-muted hover:text-fg" onClick={() => setOffers([])}>
            Not now
          </button>
        </div>
      ) : null}
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <div className="min-w-0 flex-1">
          {d.own ? (
            <p className="text-[13px] text-fg" data-pack-name>
              {d.pack.name ? <Theirs>{d.pack.name}</Theirs> : 'Its own pack'} <span className="text-subtle">· version {d.pack.version}</span>
            </p>
          ) : (
            <p className="text-[13px] text-fg" data-pack-name>
              MetaRush's own, in the house style <Badge tone="neutral">Starter pack</Badge>
            </p>
          )}
          {draftView ? <p className="mt-0.5 text-[12px] text-accent-text">Showing the proposed pack.</p> : null}
        </div>
        <div className="w-48">
          <Pick name="shape" label="Preview" value={shape} choices={SHAPES} onChange={setShape} />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3" data-pack-roles>
        {d.roles.map((r) => (
          <RoleCard
            key={r.role}
            role={r}
            preview={previews.get(r.role)}
            loading={preview.isLoading}
            readOnly={readOnly || draftView}
            own={d.own}
            busy={dis}
            approved={approved}
            onSet={(input) => save(`role:${r.role}`, { role: r.role, ...input })}
            onRemove={() => save(`remove:${r.role}`, { remove: [r.role] })}
          />
        ))}
      </div>
      {readOnly ? null : (
        <div className="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-2">
          <fieldset className="min-w-0 rounded-[14px] p-3.5 hairline" data-pack-pip>
            <legend className="px-1 text-[12.5px] font-semibold text-fg">Picture in picture</legend>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Pick
                name="pipCorner"
                label="Corner"
                value={pip.corner ?? ''}
                choices={CORNERS}
                disabled={dis}
                onChange={(v) => void save('pip', { pipCorner: v as SetIn['pipCorner'] })}
              />
              <Pick name="pipSize" label="Size" value={pip.size ?? ''} choices={SIZES} disabled={dis} onChange={(v) => void save('pip', { pipSize: v as SetIn['pipSize'] })} />
              <Pick
                name="pipShape"
                label="Shape"
                value={pip.shape ?? ''}
                choices={SHAPES_PIP}
                disabled={dis}
                onChange={(v) => void save('pip', { pipShape: v as SetIn['pipShape'] })}
              />
              <Pick
                name="pipFrame"
                label="Frame"
                value={pip.frame === 'none' ? '' : (pip.frame ?? '')}
                choices={FRAMES}
                disabled={dis}
                onChange={(v) => void save('pip', { pipFrame: v as SetIn['pipFrame'] })}
              />
            </div>
          </fieldset>
          <fieldset className="min-w-0 rounded-[14px] p-3.5 hairline" data-pack-side>
            <legend className="px-1 text-[12.5px] font-semibold text-fg">Side by side</legend>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Pick name="gap" label="Gap" value={side.gap ?? ''} choices={GAPS} disabled={dis} onChange={(v) => void save('side', { gap: v as SetIn['gap'] })} />
              <Pick
                name="frame"
                label="Frame"
                value={side.frame === 'none' ? '' : (side.frame ?? '')}
                choices={FRAMES}
                disabled={dis}
                onChange={(v) => void save('side', { frame: v as SetIn['frame'] })}
              />
              <Pick
                name="backdrop"
                label="Between the pictures"
                value={side.backdrop === 'black' ? '' : (side.backdrop ?? '')}
                choices={BACKDROPS}
                disabled={dis}
                onChange={(v) => void save('side', { backdrop: v as SetIn['backdrop'] })}
              />
              <Pick
                name="rounded"
                label="Corners"
                value={side.rounded ? 'rounded' : ''}
                choices={[
                  { value: '', label: 'Square' },
                  { value: 'rounded', label: 'Rounded' },
                ]}
                disabled={dis}
                onChange={(v) => void save('side', { rounded: v === 'rounded' })}
              />
            </div>
          </fieldset>
          <form
            className="flex flex-wrap items-end gap-2 lg:col-span-2"
            onSubmit={(e) => {
              e.preventDefault();
              void save('name', { name });
            }}
          >
            <label className="block min-w-[14rem] flex-1">
              <span className="mb-1 block text-[12px] font-medium text-muted">The pack's name</span>
              <input className={field} value={name} maxLength={80} placeholder="Like the client's name" onChange={(e) => setName(e.target.value)} data-pack-field="name" />
            </label>
            <Button type="submit" size="sm" variant="secondary" loading={busy === 'name'} disabled={dis}>
              Save the name
            </Button>
            {d.own ? (
              <Button size="sm" variant="ghost" loading={busy === 'reset'} disabled={dis} onClick={() => void reset()} data-pack-reset>
                Back to MetaRush's own
              </Button>
            ) : null}
          </form>
        </div>
      )}
    </div>
  );
}

/** A proposed pack: what it holds, what it waits for, and a person's decision. */
function DraftBox({
  d,
  busy,
  readOnly,
  viewing,
  onView,
  onAccept,
  onDecline,
}: {
  d: ContentPackOut;
  busy: string | null;
  readOnly: boolean;
  viewing: boolean;
  onView: () => void;
  onAccept: () => void;
  onDecline: () => void;
}) {
  const dr = d.draft!;
  return (
    <div className="mb-4 rounded-[14px] bg-accent-soft/60 p-3.5" data-pack-draft data-pack-draft-ready={dr.ready || undefined}>
      <p className="text-[13px] font-medium text-fg">
        A proposed layout pack{dr.by ? ` from ${dr.by}` : ''}
        {dr.pack.name ? (
          <>
            : <Theirs>{dr.pack.name}</Theirs>
          </>
        ) : null}
        <span className="ml-1 text-[12px] font-normal text-subtle">· {relative(dr.at)}</span>
      </p>
      {dr.note ? (
        <p className="mt-0.5 text-[12px] text-muted">
          <Theirs>{dr.note}</Theirs>
        </p>
      ) : null}
      <ul className="mt-2 space-y-0.5 text-[12.5px] text-fg">
        {dr.lines.map((l) => (
          <li key={l}>{l}</li>
        ))}
      </ul>
      {dr.waiting.length ? (
        <ul className="mt-2 space-y-1 text-[12.5px]" data-pack-waiting>
          {dr.waiting.map((w) => (
            <li key={`${w.role}:${w.library ? 'library' : 'graphic'}:${w.name}:${w.hash}`} className="text-muted" data-pack-wait={w.library ? 'library' : 'graphic'}>
              {w.library ? (
                <>
                  The {w.words}'s graphic uses the shared library <Theirs>{w.name}</Theirs>:{' '}
                </>
              ) : (
                <>
                  The {w.words} is the motion graphic <Theirs>{w.name}</Theirs>:{' '}
                </>
              )}
              <Link className="text-accent-text underline-offset-2 hover:underline" to={w.review}>
                {w.state === 'unknown' ? 'check it first' : w.state === 'rejected' ? 'it was turned down' : 'review and approve it'}
              </Link>
            </li>
          ))}
        </ul>
      ) : null}
      {!dr.ready && dr.why && !dr.waiting.length ? <p className="mt-2 text-[12px] text-muted">{dr.why}</p> : null}
      <div className="mt-3 flex flex-wrap gap-2">
        {readOnly ? null : (
          <>
            <Button size="sm" variant="primary" disabled={!dr.ready || !!busy} loading={busy === 'accept'} onClick={onAccept} data-pack-accept>
              Use this pack
            </Button>
            <Button size="sm" variant="ghost" disabled={!!busy} loading={busy === 'decline'} onClick={onDecline} data-pack-decline>
              Set it aside
            </Button>
          </>
        )}
        <Button size="sm" variant="ghost" onClick={onView} data-pack-view-draft>
          {viewing ? 'Show the current pack' : 'Preview it'}
        </Button>
      </div>
    </div>
  );
}

/** One role: its preview in the house style, what draws it, and the choice. */
function RoleCard({
  role,
  preview,
  loading,
  readOnly,
  own,
  busy,
  approved,
  onSet,
  onRemove,
}: {
  role: PackRoleOut;
  preview?: PackPreview;
  loading: boolean;
  readOnly: boolean;
  own: boolean;
  busy: boolean;
  approved: { name: string; version: string; summary?: string }[];
  onSet: (input: SetIn) => Promise<boolean>;
  onRemove: () => Promise<boolean>;
}) {
  const piece = role.piece;
  const current = piece?.motion ? `motion:${piece.motion.name}@${piece.motion.hash.slice(0, 8)}` : (piece?.template ?? '');
  const choices: Choice[] = [
    ...(role.templates ?? []).map((t) => ({
      value: t,
      label: TEMPLATE_WORDS[t] ?? t,
    })),
    // A motion graphic's version is name@short-version, what content.pack.set takes.
    ...approved.map((m) => ({ value: `motion:${m.version}`, label: `Motion graphic ${m.version.replace('@', ' (')})` })),
  ];
  if (current && !choices.some((c) => c.value === current))
    choices.push({ value: current, label: piece?.motion ? `Motion graphic ${piece.motion.name} (${piece.motion.hash.slice(0, 8)})` : (TEMPLATE_WORDS[current] ?? current) });
  return (
    <div className="flex min-w-0 flex-col rounded-[14px] p-3 hairline" data-pack-role={role.role} data-pack-waiting={role.waiting || undefined}>
      <PreviewBox preview={preview} loading={loading} />
      <div className="mt-2.5 flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-medium text-fg first-letter:uppercase">{role.words}</p>
          <p className="text-[12px] text-muted first-letter:uppercase">{role.what}</p>
        </div>
        {role.waiting ? <Badge tone="accent">Waits for approval</Badge> : role.starter && own ? <Badge tone="neutral">MetaRush's own</Badge> : null}
      </div>
      {role.graphic && !readOnly ? (
        <div className="mt-2 space-y-2">
          <Pick
            name={`role:${role.role}`}
            label="Drawn with"
            value={current}
            choices={choices}
            disabled={busy}
            onChange={(v) => void (v.startsWith('motion:') ? onSet({ motion: v.slice('motion:'.length) }) : onSet({ template: v }))}
          />
          <div className="flex flex-wrap items-center gap-3 text-[12.5px]">
            <label className="inline-flex items-center gap-1.5 text-muted">
              <input type="checkbox" checked={!!piece?.logo} disabled={busy} onChange={(e) => void onSet({ logo: e.target.checked })} data-pack-logo={role.role} />
              Show the logo
            </label>
            {own && !role.starter ? (
              <button type="button" className="text-muted hover:text-fg" disabled={busy} onClick={() => void onRemove()} data-pack-remove={role.role}>
                Use MetaRush's own
              </button>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

/** A graphic playing in the player, or a call layout's pictures as boxes. */
function PreviewBox({ preview, loading }: { preview?: PackPreview; loading: boolean }) {
  const w = preview?.width || 1920;
  const h = preview?.height || 1080;
  // Wide frames fill the card's width; tall and square ones keep a height and are as wide as their shape.
  const size = w > h ? { width: '100%' } : { height: 240, maxWidth: '100%', marginInline: 'auto' };
  return (
    <div className="relative overflow-hidden rounded-[10px] bg-[#1b1d22]" style={{ aspectRatio: `${w} / ${h}`, ...size }} data-pack-preview>
      {!preview ? (
        <div className="grid h-full w-full place-items-center">{loading ? <Spinner size={14} label="Preparing the preview" /> : null}</div>
      ) : preview.plan ? (
        <Suspense fallback={null}>
          <LoopPreview plan={preview.plan} inFrame={0} outFrame={Math.max(1, preview.plan.durationInFrames - 1)} />
        </Suspense>
      ) : (
        <LayoutBoxes preview={preview} />
      )}
    </div>
  );
}

/** Where a call layout puts the pictures (Go's geometry), with its corners, frame and backdrop. */
function LayoutBoxes({ preview }: { preview: PackPreview }) {
  const w = preview.width || 1920;
  const h = preview.height || 1080;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="h-full w-full" role="img" aria-label={`Where ${preview.words} places the pictures`}>
      <rect x={0} y={0} width={w} height={h} fill={preview.backdrop || '#000'} />
      {(preview.tiles ?? []).map((t, i) => (
        <g key={i}>
          <rect
            x={t.rect.x}
            y={t.rect.y}
            width={t.rect.w}
            height={t.rect.h}
            rx={t.radius ?? 0}
            fill={i === 0 ? '#3a4150' : '#56607a'}
            stroke={t.border?.color}
            strokeWidth={t.border ? t.border.width * 2 : 0}
          />
          <circle cx={t.rect.x + t.rect.w / 2} cy={t.rect.y + t.rect.h * 0.42} r={Math.min(t.rect.w, t.rect.h) * 0.14} fill="#8a93a8" />
          <rect
            x={t.rect.x + t.rect.w * 0.3}
            y={t.rect.y + t.rect.h * 0.62}
            width={t.rect.w * 0.4}
            height={t.rect.h * 0.38}
            rx={Math.min(t.rect.w, t.rect.h) * 0.12}
            fill="#8a93a8"
          />
        </g>
      ))}
    </svg>
  );
}
