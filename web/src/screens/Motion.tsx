import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { ArrowLeft, CaretDown, CaretRight, CheckCircle, FilmStrip, Prohibit, Shapes, ShieldCheck, Sparkle, WarningCircle, XCircle } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp, useOpMutation } from '../api/queries';
import type { MotionComponent, MotionFrame, MotionImport, MotionRow, MotionShape, MotionShowOut, MotionSourceFile } from '../api/types';
import { relative, plural, bytes } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Lightbox, type LightboxItem } from '../ui/Lightbox';
import { useToast } from '../ui/Toast';
import { Badge, Button, cx, EmptyState, ErrorPanel, Freshness, SectionTitle, Skeleton, Theirs, type Tone } from '../ui/primitives';
import { askInProject } from '../layout/assistant/store';

/*
 * Project motion graphics (D68): components a project carries as code. People
 * approve each exact version after looking at its test frames, preview and
 * source; nothing renders a version before that. This page shows pictures,
 * video and text only — the component's code never runs in the dashboard.
 */

const stateTone: Record<string, Tone> = { waiting: 'accent', approved: 'ok', rejected: 'danger', revoked: 'danger' };

function StateBadge({ row }: { row: Pick<MotionRow, 'state' | 'stateLabel'> }) {
  const icon =
    row.state === 'approved' ? <CheckCircle size={12} weight="fill" /> : row.state === 'waiting' ? <ShieldCheck size={12} /> : <XCircle size={12} weight="fill" />;
  return (
    <Badge tone={stateTone[row.state] ?? 'neutral'} icon={icon}>
      {row.stateLabel}
    </Badge>
  );
}

const regionWords: Record<string, string> = { lower: 'the lower band', center: 'the middle', full: 'the whole frame' };

function seconds(n: number) {
  return `${Number.isInteger(n) ? n : n.toFixed(1)} s`;
}

/* ---------- the list ---------- */

type Filter = 'all' | 'waiting' | 'approved';

/** Library → Motion (D184): every motion graphic, with its test frames. */
export function MotionListPage() {
  const [params, setParams] = useSearchParams();
  const filter = (params.get('show') as Filter | null) ?? 'all';
  const versions = params.get('versions') === '1';
  const list = useOp('motion.list', versions ? { all: true } : {});
  const rows = (list.data?.components ?? []).filter((r) => filter === 'all' || r.state === filter);
  const set = (k: string, v?: string) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v);
    else n.delete(k);
    setParams(n, { replace: true });
  };
  return (
    <div data-motion-page>
      <h2 className="sr-only">Motion graphics</h2>
      <div className="mb-5 flex flex-wrap items-center gap-2" role="group" aria-label="Show">
        {(
          [
            ['all', 'All'],
            ['waiting', 'Waiting for you'],
            ['approved', 'Approved'],
          ] as [Filter, string][]
        ).map(([k, label]) => (
          <button
            key={k}
            type="button"
            aria-pressed={filter === k}
            onClick={() => set('show', k === 'all' ? undefined : k)}
            className={cx('inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full px-3 text-[12.5px] transition-colors hairline', filter === k ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
          >
            {label}
            {k === 'waiting' && list.data?.waiting ? <span className="tc text-[11px] text-subtle">{list.data.waiting}</span> : null}
          </button>
        ))}
        <label className="ml-auto flex items-center gap-2 text-[12.5px] text-muted">
          <input type="checkbox" checked={versions} onChange={(e) => set('versions', e.target.checked ? '1' : undefined)} className="accent-[var(--accent)]" />
          Every version
        </label>
        <Freshness at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} className="max-sm:hidden" />
      </div>
      {list.isLoading ? (
        <div className="space-y-3">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-28 rounded-[16px]" />
          ))}
        </div>
      ) : list.error ? (
        <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<Sparkle size={22} />}
            title={filter === 'waiting' ? 'Nothing waiting for you' : 'No motion graphics yet'}
            body="When Claude writes a motion graphic for a project and checks it, it shows up here with its test frames, for you to approve or turn down."
          />
        </div>
      ) : (
        <ul className="space-y-3">
          {rows.map((r) => (
            <li key={r.hash}>
              <Link to={`/motion/${r.hash}`} className="fade-in grid grid-cols-1 gap-4 rounded-[16px] bg-raised p-4 hairline transition-shadow hover:shadow-[var(--shadow-soft)] sm:grid-cols-[220px_1fr]">
                <div className="overflow-hidden rounded-[10px] bg-inset">
                  {r.sheet ? (
                    <img src={media.motionSheet(r.hash)} alt={`Test frames of ${r.name}`} className="block h-auto w-full" loading="lazy" />
                  ) : r.kind === 'library' ? (
                    <div className="grid h-24 place-items-center text-[12px] text-subtle">
                      <span className="flex flex-col items-center gap-1">
                        <Shapes size={18} aria-hidden />
                        Pictures come from the graphics that use it
                      </span>
                    </div>
                  ) : (
                    <div className="h-24" />
                  )}
                </div>
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-[15px] font-semibold text-fg">{r.name}</span>
                    <StateBadge row={r} />
                    {r.kind === 'library' ? <Badge tone="accent">Shared library</Badge> : null}
                    {r.warnings ? <Badge tone="info">{plural(r.warnings, 'note')}</Badge> : null}
                  </div>
                  {r.summary ? <p className="mt-1 text-[13.5px] leading-relaxed text-muted">{r.summary}</p> : null}
                  <LibraryLine row={r} />
                  <p className="mt-2 text-[12.5px] text-subtle">
                    Checked {relative(r.checkedAt)}
                    {r.checkedBy ? ` by ${r.checkedBy}` : ''}
                    {r.projectName ? ` · ${r.projectName}` : ''}
                    {r.approvedBy && r.state === 'approved' ? ` · approved by ${r.approvedBy}` : ''}
                  </p>
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/* ---------- one version: the review page ---------- */

export function MotionReviewPage() {
  const { hash = '' } = useParams();
  const show = useOp('motion.show', { ref: hash });
  if (show.isLoading) {
    return (
      <div className="mx-auto max-w-[1180px] space-y-4">
        <Skeleton className="h-10 w-64 rounded-[10px]" />
        <Skeleton className="h-72 rounded-[16px]" />
      </div>
    );
  }
  if (show.error || !show.data) {
    return (
      <div className="mx-auto max-w-[1180px]">
        <BackLink />
        <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />
      </div>
    );
  }
  return <Review data={show.data} />;
}

function BackLink() {
  return (
    <Link to="/library/motion" className="mb-4 inline-flex items-center gap-1.5 text-[12.5px] font-medium text-muted hover:text-fg">
      <ArrowLeft size={13} /> Motion graphics
    </Link>
  );
}

function Review({ data }: { data: MotionShowOut }) {
  const r = data.row;
  const c = data.component;
  const [details, setDetails] = useState(false);
  const [code, setCode] = useState(false);
  return (
    <div className="mx-auto max-w-[1180px]">
      <BackLink />
      <div className="mb-5 flex flex-col gap-3 sm:flex-row sm:items-start">
        <div className="min-w-0 sm:flex-1">
          <div className="flex flex-wrap items-center gap-2.5">
            <h1 className="min-w-0 text-[26px] font-semibold tracking-[-0.03em] [overflow-wrap:anywhere]">{r.name}</h1>
            <StateBadge row={r} />
            {r.kind === 'library' ? <Badge tone="accent">Shared library</Badge> : null}
          </div>
          {r.summary ? <p className="mt-1.5 max-w-[70ch] text-[14px] leading-relaxed text-muted">{r.summary}</p> : null}
          <LibraryLine row={r} />
          <p className="mt-1.5 text-[12.5px] text-subtle">
            Checked {relative(r.checkedAt)}
            {r.checkedBy ? ` by ${r.checkedBy}` : ''}
            {c.check.machine ? ` on ${c.check.machine}` : ''}
            {r.projectName ? ` · made for ${r.projectName}` : ''}
          </p>
        </div>
        <Decide row={r} />
      </div>

      {data.previous ? (
        <p className="mb-5 flex items-start gap-2 rounded-[12px] bg-info-soft px-3.5 py-2.5 text-[13px] leading-relaxed text-fg">
          <FilmStrip size={15} className="mt-[2px] shrink-0 text-info" />
          <span>
            A different version of {r.name} was approved {relative(data.previous.approvedAt ?? data.previous.checkedAt)}
            {data.previous.state === 'revoked' ? ', and that approval was later withdrawn' : ''}. Its code (See code, below) marks what changed since then.
          </span>
        </p>
      ) : null}

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)]">
        <section aria-labelledby="frames" className="min-w-0">
          <SectionTitle id="frames" meta="drawn with no network access">
            Test frames
          </SectionTitle>
          {byShape(c) ? (
            <ShapeFrames hash={r.hash} name={r.name} component={c} />
          ) : (
            <div className="overflow-hidden rounded-[14px] bg-inset hairline">
              {c.sheet ? <img src={media.motionSheet(r.hash)} alt={`Test frames of ${r.name}: ${(c.frames ?? []).map((f) => f.label).join(', ')}`} className="block h-auto w-full" /> : null}
            </div>
          )}
          <p className="mt-2 text-[12px] text-subtle">The dashed amber line is the safe area: platform buttons and captions can cover what's outside it.</p>
          {c.video && !byShape(c) ? (
            <div className="mt-5">
              <SectionTitle>Preview</SectionTitle>
              <video src={media.motionPreview(r.hash)} controls playsInline loop className="mx-auto block max-h-[520px] w-auto max-w-full rounded-[14px] bg-inset hairline" aria-label={`Preview of ${r.name}`} />
            </div>
          ) : null}
        </section>

        <section aria-label="What it is" className="min-w-0 space-y-5">
          <div className="panel p-4">
            <h2 className="text-[15px] font-semibold tracking-[-0.01em]">What it does</h2>
            <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-[13px]">
              <dt className="text-subtle">Shows in</dt>
              <dd className="text-fg">{regionWords[r.region] ?? r.region}</dd>
              <dt className="text-subtle">Length</dt>
              <dd className="text-fg">
                {seconds(r.minDuration)} to {seconds(r.maxDuration)}
              </dd>
            </dl>
            {data.fields.length ? (
              <>
                <h3 className="mt-4 text-[13px] font-semibold text-fg">What a cut fills in</h3>
                <ul className="mt-2 space-y-1.5">
                  {data.fields.map((f) => (
                    <li key={f.name} className="text-[13px] leading-relaxed">
                      <span className="tc text-fg">{f.name}</span>
                      <span className="text-subtle">
                        {' '}
                        · {f.type}
                        {f.required ? '' : ', optional'}
                      </span>
                      {f.description ? <span className="text-muted"> — {f.description}</span> : null}
                      {data.preview && sampleOf(data.preview, f.name) !== undefined ? (
                        <span className="block text-[12px] text-subtle">In the test frames: {String(sampleOf(data.preview, f.name))}</span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              </>
            ) : null}
          </div>

          <div className="panel p-4">
            <h2 className="text-[15px] font-semibold tracking-[-0.01em]">What it uses</h2>
            <p className="mt-1 text-[12.5px] text-muted">Only packages MetaRush ships and allows; it can't reach files, programs or the network.</p>
            <Uses imports={c.imports} />
          </div>

          {c.warnings?.length ? (
            <div className="panel p-4">
              <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Notes from the check</h2>
              <ul className="mt-2 space-y-2">
                {c.warnings.map((w, i) => (
                  <li key={i} className="flex gap-2 text-[13px] leading-relaxed text-fg">
                    <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-accent-text" />
                    <span>
                      {w.file && w.line ? (
                        <span className="text-subtle">
                          {w.file} line {w.line}:{' '}
                        </span>
                      ) : null}
                      {w.message}
                      {w.hint ? <span className="block text-[12.5px] text-muted">{w.hint}</span> : null}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {data.usedBy.length ? (
            <div className="panel p-4">
              <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Used in</h2>
              <ul className="mt-2 space-y-1">
                {data.usedBy.map((u) => (
                  <li key={u.revisionId} className="text-[13px]">
                    <Link to={`/p/${u.projectId}/r/${u.revisionId}`} className="text-accent-text hover:underline">
                      {u.projectName}, version {u.number}
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {c.decisions?.length ? (
            <div className="panel p-4">
              <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Decisions</h2>
              <ul className="mt-2 space-y-1.5">
                {[...c.decisions].reverse().map((d, i) => (
                  <li key={i} className="text-[13px] leading-relaxed text-fg">
                    {d.state === 'approved' ? 'Approved' : d.state === 'rejected' ? 'Turned down' : 'Approval withdrawn'} {relative(d.at)} by {d.by.userName || 'someone'}
                    {d.note ? <span className="text-muted">: “{d.note}”</span> : null}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </section>
      </div>

      <section aria-labelledby="source" className="mt-7">
        <SectionTitle id="source" meta={plural(data.source.filter((f) => f.change !== 'removed').length, 'file')}>
          Source
        </SectionTitle>
        {/* The picture comes first; the code is there for whoever wants it (D186). */}
        {code ? (
          <>
            <button type="button" onClick={() => setCode(false)} aria-expanded className="mb-2 flex items-center gap-1 text-[12.5px] font-medium text-muted hover:text-fg" data-see-code>
              <CaretDown size={11} /> Hide the code
            </button>
            <Source files={data.source} compared={!!data.previous} />
          </>
        ) : (
          <button
            type="button"
            onClick={() => setCode(true)}
            aria-expanded={false}
            className="flex items-center gap-1.5 rounded-[10px] bg-panel px-3 py-2 text-[12.5px] font-medium text-muted hairline hover:bg-hover hover:text-fg"
            data-see-code
          >
            <CaretRight size={11} /> See code
            {data.previous && data.source.some((f) => f.change === 'changed' || f.change === 'added') ? <span className="font-normal text-subtle">· some of it changed</span> : null}
          </button>
        )}
      </section>

      {data.versions.length > 1 ? (
        <section aria-labelledby="versions" className="mt-7">
          <SectionTitle id="versions">Versions</SectionTitle>
          <ul className="divide-y divide-line overflow-hidden rounded-[14px] bg-raised hairline">
            {data.versions.map((v) => (
              <li key={v.hash}>
                <Link to={`/motion/${v.hash}`} className={cx('flex flex-wrap items-center gap-2 px-4 py-2.5 text-[13px] hover:bg-hover', v.hash === r.hash && 'bg-hover')}>
                  <StateBadge row={v} />
                  <span className="text-muted">checked {relative(v.checkedAt)}</span>
                  {v.hash === r.hash ? <span className="text-subtle">· this one</span> : null}
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <div className="mt-7 mb-10">
        <button type="button" onClick={() => setDetails((v) => !v)} aria-expanded={details} className="flex items-center gap-1 text-[12.5px] font-medium text-muted hover:text-fg">
          {details ? <CaretDown size={11} /> : <CaretRight size={11} />}
          Details
        </button>
        {details ? (
          <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-[12px] bg-panel p-3.5 text-[12.5px] hairline">
            <dt className="text-subtle">Version</dt>
            <dd className="tc break-all text-fg">{r.version}</dd>
            <dt className="text-subtle">Hash</dt>
            <dd className="tc break-all text-fg">{r.hash}</dd>
            <dt className="text-subtle">Renderer</dt>
            <dd className="text-fg">{c.check.renderer || '—'}</dd>
            <dt className="text-subtle">Bundle</dt>
            <dd className="text-fg">{bytes(c.check.bundleBytes)}</dd>
            <dt className="text-subtle">Test frames</dt>
            <dd className="text-fg">
              {c.check.width}×{c.check.height}, slowest {c.check.slowestMs ?? 0} ms
            </dd>
            {c.check.blocked?.length ? (
              <>
                <dt className="text-subtle">Blocked</dt>
                <dd className="text-fg">{c.check.blocked.join(', ')}</dd>
              </>
            ) : null}
            <dt className="text-subtle">In a cut sheet</dt>
            <dd className="tc break-all text-fg">- name: motion {r.version} at 0:01 for {seconds(Math.min(r.maxDuration, Math.max(r.minDuration, 3)))}</dd>
          </dl>
        ) : null}
      </div>
    </div>
  );
}

/* ---------- test frames in the shapes of the cuts it is for (D164) ---------- */

/** Versions checked in the cuts' shapes have frames that say their shape; older ones show their contact sheet. */
function byShape(c: MotionComponent): boolean {
  return (c.shapes?.length ?? 0) > 0 && (c.frames ?? []).some((f) => !!f.shape);
}

export function shapeTitle(label: string): string {
  switch (label) {
    case 'widescreen':
      return 'Widescreen';
    case 'vertical':
      return 'Vertical';
    case 'square':
      return 'Square';
  }
  return label ? label.charAt(0).toUpperCase() + label.slice(1) : 'Test frames';
}

function frameLabel(f: MotionFrame): string {
  const what = f.label ? f.label.charAt(0).toUpperCase() + f.label.slice(1) : 'Frame';
  return `${what}, ${seconds(f.at)}`;
}

interface ShapeGroup {
  key: string;
  title: string;
  shape?: MotionShape;
  frames: { f: MotionFrame; i: number }[];
  preview?: string;
}

function ShapeFrames({ hash, name, component: c }: { hash: string; name: string; component: MotionComponent }) {
  const [box, setBox] = useState<{ items: LightboxItem[]; index: number; title: string } | null>(null);
  const groups = useMemo(() => {
    const frames = (c.frames ?? []).map((f, i) => ({ f, i }));
    const shapes = c.shapes ?? [];
    const out: ShapeGroup[] = shapes.map((s, si) => ({
      key: s.label,
      title: shapeTitle(s.label),
      shape: s,
      frames: frames.filter((x) => x.f.shape === s.label),
      // The first shape's preview is the graphic's preview; the others have their own.
      preview: si === 0 ? (c.video ? media.motionPreview(hash) : undefined) : s.video ? media.motionShapePreview(hash, s.label) : undefined,
    }));
    const known = new Set(shapes.map((s) => s.label));
    const rest = frames.filter((x) => !x.f.shape || !known.has(x.f.shape));
    if (rest.length) out.push({ key: 'other', title: 'Other test frames', frames: rest });
    return out.filter((g) => g.frames.length || g.preview);
  }, [c, hash]);

  return (
    <div className="space-y-6" data-motion-shapes>
      {groups.map((g) => {
        const w = g.shape?.width || c.check.width || 16;
        const h = g.shape?.height || c.check.height || 9;
        const ratio = `${w} / ${h}`;
        const tall = h > w;
        const items: LightboxItem[] = [
          ...g.frames.map((x) => ({ src: media.motionFrame(hash, x.i), label: `${g.title}: ${frameLabel(x.f)}` })),
          ...(g.preview ? [{ src: g.preview, kind: 'video' as const, label: `${g.title}: preview` }] : []),
        ];
        const title = `${name}, ${g.title.toLowerCase()}`;
        return (
          <div key={g.key} data-motion-shape={g.key}>
            <h3 className="mb-2 flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5 text-[13.5px] font-semibold text-fg">
              <span>{g.title}</span>
              {g.shape?.for ? (
                <span className="font-normal text-muted">
                  · for <Theirs>“{g.shape.for}”</Theirs>
                </span>
              ) : null}
              {g.shape ? (
                <span className="tc text-[11.5px] font-normal text-subtle">
                  {g.shape.width}×{g.shape.height}
                </span>
              ) : null}
            </h3>
            <ul className="grid gap-2" style={{ gridTemplateColumns: `repeat(auto-fill, minmax(${tall ? 92 : w === h ? 124 : 164}px, 1fr))` }}>
              {g.frames.map((x, k) => (
                <li key={x.i} className="min-w-0">
                  <button
                    type="button"
                    onClick={() => setBox({ items, index: k, title })}
                    className="group block w-full overflow-hidden rounded-[10px] bg-inset hairline focus-visible:outline-2 focus-visible:outline-accent"
                    style={{ aspectRatio: ratio }}
                    aria-label={`Open ${g.title.toLowerCase()} ${frameLabel(x.f).toLowerCase()}, large`}
                    data-thumb
                  >
                    <img src={media.motionFrame(hash, x.i)} alt="" loading="lazy" className="h-full w-full object-contain transition-transform duration-300 group-hover:scale-[1.02]" />
                  </button>
                  <span className="mt-1 block truncate text-[11.5px] text-subtle">{frameLabel(x.f)}</span>
                </li>
              ))}
            </ul>
            {g.preview ? (
              <video
                src={g.preview}
                controls
                playsInline
                loop
                preload="metadata"
                className="mt-3 block w-auto max-w-full rounded-[12px] bg-inset hairline"
                style={{ aspectRatio: ratio, maxHeight: tall ? 420 : 300 }}
                aria-label={`Preview of ${name}, ${g.title.toLowerCase()}`}
              />
            ) : null}
          </div>
        );
      })}
      {box ? <Lightbox items={box.items} index={box.index} title={box.title} onClose={() => setBox(null)} /> : null}
    </div>
  );
}

function sampleOf(preview: unknown, key: string): unknown {
  return preview && typeof preview === 'object' && !Array.isArray(preview) ? (preview as Record<string, unknown>)[key] : undefined;
}

const kindWords: Record<string, string> = {
  remotion: 'Remotion (the video renderer)',
  react: 'React',
  metarush: 'MetaRush’s theme and layout helpers',
  relative: 'its own files',
  package: '',
};

function Uses({ imports }: { imports: MotionImport[] }) {
  const groups = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const i of imports) {
      const label = kindWords[i.kind] || i.specifier;
      const set = m.get(label) ?? new Set<string>();
      if (i.kind === 'relative') set.add(i.specifier.replace(/^\.\//, ''));
      m.set(label, set);
    }
    return [...m.entries()];
  }, [imports]);
  return (
    <ul className="mt-2 flex flex-wrap gap-1.5">
      {groups.map(([label, files]) => (
        <li key={label}>
          <Badge tone="neutral" title={files.size ? [...files].join(', ') : undefined}>
            {label}
            {files.size ? `: ${[...files].join(', ')}` : ''}
          </Badge>
        </li>
      ))}
    </ul>
  );
}

function Source({ files, compared }: { files: MotionSourceFile[]; compared: boolean }) {
  const shown = files.filter((f) => f.change !== 'removed');
  const [active, setActive] = useState(() => shown.find((f) => f.path === 'index.tsx')?.path ?? shown[0]?.path ?? '');
  const [onlyChanges, setOnlyChanges] = useState(compared);
  const f = shown.find((x) => x.path === active) ?? shown[0];
  const removed = files.filter((x) => x.change === 'removed');
  if (!f) return null;
  const lines = f.text.split('\n');
  return (
    <div className="overflow-hidden rounded-[14px] bg-raised hairline">
      <div className="flex flex-wrap items-center gap-1 border-b border-line px-2 py-1.5" role="tablist" aria-label="Files">
        {shown.map((x) => (
          <button
            key={x.path}
            role="tab"
            type="button"
            aria-selected={x.path === f.path}
            onClick={() => setActive(x.path)}
            className={cx('tc inline-flex h-7 items-center gap-1.5 rounded-[8px] px-2 text-[12px]', x.path === f.path ? 'bg-hover text-fg' : 'text-muted hover:text-fg')}
          >
            {x.path}
            {x.change === 'changed' || x.change === 'added' ? <span className="h-1.5 w-1.5 rounded-full bg-accent" aria-label={x.change === 'added' ? 'new file' : 'changed'} /> : null}
          </button>
        ))}
        {compared && f.change === 'changed' ? (
          <label className="ml-auto flex items-center gap-1.5 px-1 text-[12px] text-muted">
            <input type="checkbox" checked={onlyChanges} onChange={(e) => setOnlyChanges(e.target.checked)} className="accent-[var(--accent)]" />
            Only what changed
          </label>
        ) : null}
      </div>
      {removed.length ? <p className="border-b border-line px-3 py-1.5 text-[12px] text-muted">Removed since the approved version: {removed.map((x) => x.path).join(', ')}</p> : null}
      <div className="max-h-[560px] overflow-auto bg-inset">
        {onlyChanges && f.change === 'changed' && f.changes?.length ? (
          <pre className="tc min-w-max px-0 py-2 text-[12px] leading-[1.6]">
            {f.changes.map((l, i) => (
              <div key={i} data-change={l.op === 'added' || l.op === 'removed' ? l.op : undefined} className={cx('flex px-3', l.op === 'added' ? 'bg-[rgb(146_199_156/0.14)]' : l.op === 'removed' ? 'bg-[rgb(239_141_128/0.14)]' : '')}>
                <span className="w-10 shrink-0 select-none pr-3 text-right text-[#8f877e]">{l.op === 'removed' ? l.left : l.right}</span>
                <span className="w-4 shrink-0 select-none text-[#8f877e]">{l.op === 'added' ? '+' : l.op === 'removed' ? '−' : ' '}</span>
                <span className="whitespace-pre text-[#e9dcc8]">{l.text}</span>
              </div>
            ))}
          </pre>
        ) : (
          <pre className="tc min-w-max py-2 text-[12px] leading-[1.6]">
            {lines.map((l, i) => (
              <div key={i} className="flex px-3">
                <span className="w-10 shrink-0 select-none pr-3 text-right text-[#8f877e]">{i + 1}</span>
                <span className="whitespace-pre text-[#e9dcc8]">{l}</span>
              </div>
            ))}
          </pre>
        )}
      </div>
    </div>
  );
}

/* ---------- the decision (people only) ---------- */

/** A shared library's graphics, or the libraries a graphic draws with (D171). */
function LibraryLine({ row: r }: { row: MotionRow }) {
  if (r.kind === 'library')
    return (
      <p className="mt-1 text-[12.5px] text-muted">
        Graphics import it for their colours, fonts and parts; one approval of a version covers all of them
        {r.usedBy?.length ? (
          <>
            {' '}
            (used by <Theirs>{r.usedBy.join(', ')}</Theirs>)
          </>
        ) : null}
        .
      </p>
    );
  if (r.libraries?.length)
    return (
      <p className="mt-1 text-[12.5px] text-muted">
        Draws with the shared {r.libraries.length === 1 ? 'library' : 'libraries'} <Theirs>{r.libraries.join(', ')}</Theirs>, approved on {r.libraries.length === 1 ? 'its' : 'their'} own.
      </p>
    );
  return null;
}

function Decide({ row }: { row: MotionRow }) {
  const dialog = useDialog();
  const toast = useToast();
  const navigate = useNavigate();
  const invalidate = ['motion.list', 'motion.show'] as const;
  const approve = useOpMutation('motion.approve', { invalidate: [...invalidate] });
  const revoke = useOpMutation('motion.revoke', { invalidate: [...invalidate] });
  const busy = approve.isPending || revoke.isPending;
  const ref = `${row.name}@${row.hash}`;
  const done = (o: { summary: string }) => toast({ tone: 'success', title: o.summary });
  const failed = (e: Error) => toast({ tone: 'error', title: 'That did not work', body: e.message });

  const onApprove = async () => {
    const ok = await dialog.confirm({
      title: `Approve ${row.name}?`,
      body:
        row.kind === 'library' ? (
          <p>
            Every graphic that uses this shared library can then draw with this exact version, and the latest cuts that use those graphics move to it in a new version of each cut.
            If anyone changes its files, the new version needs your approval again.
          </p>
        ) : (
          <p>
            Cuts can then use this exact version, and it renders. You've looked at its test frames, preview and source; if anyone changes its files, the new version needs your
            approval again.
          </p>
        ),
      confirmLabel: 'Approve',
    });
    if (ok) approve.mutate({ ref }, { onSuccess: done, onError: failed });
  };
  const onReject = async () => {
    const note = await dialog.prompt({
      title: `Turn down ${row.name}?`,
      body: <p>It can't be used in a cut. Claude can make a new version from your note.</p>,
      label: 'What should change (optional)',
      multiline: true,
      maxLength: 2000,
      confirmLabel: 'Turn down',
      tone: 'danger',
    });
    if (note === null) return;
    const text = note.trim();
    // The note goes to the conversation that made the graphic (D186); when none
    // did, "Send to Claude" starts one in its project with the note filled in.
    approve.mutate(
      { ref, reject: true, note: text || undefined },
      {
        onSuccess: (o) => {
          const told = o.toldClaude;
          toast({
            tone: 'success',
            title: o.summary,
            body: text ? (told ? `Your note went to Claude in “${told.title || 'the conversation that made it'}”.` : 'No conversation with Claude made it.') : undefined,
            action:
              text && !told
                ? {
                    label: 'Send to Claude',
                    onClick: () => {
                      if (row.projectId) navigate(`/p/${row.projectId}`);
                      askInProject(row.projectId, `I turned down the graphic “${row.name}”: ${text}${/[.!?…]$/.test(text) ? '' : '.'} Make a new version with that change.`);
                    },
                  }
                : undefined,
          });
        },
        onError: failed,
      },
    );
  };
  const onRevoke = async () => {
    const note = await dialog.prompt({
      title: `Withdraw approval of ${row.name}?`,
      body: <p>New renders of cuts that use it are refused until someone approves it again. Videos already made are kept.</p>,
      label: 'Why (optional)',
      multiline: true,
      maxLength: 2000,
      confirmLabel: 'Withdraw approval',
      tone: 'danger',
    });
    if (note !== null) revoke.mutate({ ref, note: note.trim() || undefined }, { onSuccess: done, onError: failed });
  };
  void navigate;
  return (
    <div className="flex flex-wrap items-center gap-2">
      {row.state === 'approved' ? (
        <Button variant="secondary" icon={<Prohibit size={14} />} loading={revoke.isPending} disabled={busy} onClick={() => void onRevoke()}>
          Withdraw approval
        </Button>
      ) : (
        <>
          {row.state === 'waiting' ? (
            <Button variant="ghost" icon={<XCircle size={14} />} loading={approve.isPending && approve.variables?.reject} disabled={busy} onClick={() => void onReject()}>
              Turn down
            </Button>
          ) : null}
          <Button variant="primary" icon={<CheckCircle size={14} />} loading={approve.isPending && !approve.variables?.reject} disabled={busy} onClick={() => void onApprove()}>
            Approve
          </Button>
        </>
      )}
    </div>
  );
}
