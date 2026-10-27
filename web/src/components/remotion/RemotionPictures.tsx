import { useEffect, useRef, useState, type ReactNode } from 'react';
import { ArrowCounterClockwise, Image as ImageIcon, ImageSquare, UploadSimple, WarningCircle } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import { resourceMedia } from '../../api/resources';
import type { RemotionPictureRow, RemotionPicturesCheckOut, RemotionPicturesOut, RemotionSourceRow, ResourceRow } from '../../api/types';
import { uploads } from '../../api/uploads';
import { plural } from '../../lib/format';
import { LibraryPicker } from '../../screens/Brand';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, ErrorPanel, Skeleton, Spinner, Theirs, Thumb } from '../../ui/primitives';
import { failedToast, StillPairs, useTask, Working } from './RemotionMap';

/*
 * Pictures as settings (D209): the pictures a Remotion project draws from its
 * own files. Once a person approved the one-time change to its code, each one
 * is replaced here — a picture uploaded now or one of the project's resources —
 * drawn in the render jail with no network and checked (does the new picture
 * show, does its shape fit), then the latest cuts' clips are made again with
 * it: no new version of the code to approve. The same ops as `metarush
 * remotion pictures list|setup|set`. Nothing of the project runs here: its
 * own pictures are shown as files (an SVG cleaned first), the checks' stills
 * were drawn in the jail.
 */

const INVALIDATE = ['remotion.pictures', 'remotion.list', 'remotion.show', 'project.show', 'cut.show', 'revision.list', 'remotion.map'];

/** A picture chosen here, not used yet: a resource, or back to the project's own. */
type Draft = { id: string; title: string; thumb: string } | { original: true };

/** A source's pictures: settings to replace, or the offer to make them settings. */
export function RemotionPicturesPanel({ row, projectId, onReview }: { row: RemotionSourceRow; projectId: string; onReview: (version: string) => void }) {
  const toast = useToast();
  // A version read by an older MetaRush has its pictures read once: look again until that is done.
  const q = useOp('remotion.pictures', { source: row.name, projectId }, { refetchInterval: (s) => (s.state.data?.reading && s.state.data.jobId ? 2500 : false) });
  const setup = useOpMutation('remotion.pictures.setup');
  const task = useTask(INVALIDATE);
  if (q.isLoading) return <Skeleton className="mt-3 h-32 rounded-[14px]" />;
  if (q.error || !q.data) return <ErrorPanel className="mt-3" error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  if (d.reading) {
    return (
      <p role="status" className="mt-3 flex items-center gap-2 rounded-[12px] bg-panel px-3.5 py-3 text-[13px] text-muted hairline" data-remotion-pictures="reading">
        {d.jobId ? <Spinner size={13} /> : null}
        {d.message}
      </p>
    );
  }
  const makeSettings = (
    <Button
      size="sm"
      variant={d.hasSettings ? 'secondary' : 'primary'}
      icon={<ImageSquare size={13} />}
      loading={setup.isPending}
      disabled={task.busy}
      onClick={() => setup.mutate({ source: row.name, projectId }, { onSuccess: task.start, onError: failedToast(toast) })}
    >
      {d.hasSettings ? `Make ${d.more === 1 ? 'it' : 'them'} settings too` : 'Make its pictures settings'}
    </Button>
  );
  if (d.hasSettings) return <PicturesForm data={d} row={row} projectId={projectId} more={d.more > 0 && !d.waiting ? makeSettings : null} task={task} onReview={onReview} />;
  return (
    <div className="mt-3 rounded-[12px] bg-panel px-3.5 py-3 hairline" data-remotion-pictures="offer">
      <p className="max-w-[72ch] text-[13px] leading-relaxed text-muted">{d.message}</p>
      {d.next && !d.next.startsWith('metarush ') ? <p className="mt-1 max-w-[72ch] text-[12.5px] leading-relaxed text-subtle">{d.next}</p> : null}
      {d.waiting ? (
        <Button className="mt-2" size="sm" variant="primary" onClick={() => onReview(d.waiting!.hash)}>
          Review version {d.waiting.number}
        </Button>
      ) : d.more > 0 ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          {makeSettings}
          {task.busy ? <Working line={task.line} pct={task.pct} /> : null}
        </div>
      ) : null}
      {d.pictures.length ? (
        <ul className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4" aria-label="Its pictures">
          {d.pictures.map((p) => (
            <li key={p.name} className="min-w-0" data-picture-candidate={p.name}>
              <Thumb src={p.originalUrl || undefined} alt={`${p.name}, as the project has it`} className="aspect-[4/3] w-full rounded-[10px] hairline" fit="contain" fallback={<ImageIcon size={18} className="text-subtle" />} />
              <div className="mt-1 truncate text-[12px] text-subtle" title={p.file}>
                {p.name}
                {p.why ? ' · stays in the code' : ''}
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      <Details compact className="mt-2">
        {d.pictures.map((p) => (
          <div key={p.name}>
            {p.name}: {p.file || 'named in code'}
            {p.uses.length ? ` — ${p.uses.map((u) => `${u.file}:${u.line}`).join(', ')}` : ''}
            {p.why ? ` (${p.why})` : ''}
          </div>
        ))}
        {d.left.map((l, i) => (
          <div key={`l${i}`}>
            Stays in the code: {l.file}:{l.line} {l.code} — {l.why}
          </div>
        ))}
      </Details>
    </div>
  );
}

function PicturesForm({ data: d, row, projectId, more, task, onReview }: {
  data: RemotionPicturesOut;
  row: RemotionSourceRow;
  projectId: string;
  more: ReactNode;
  task: ReturnType<typeof useTask>;
  onReview: (version: string) => void;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const set = useOpMutation('remotion.pictures.set');
  const change = useTask(INVALIDATE);
  const [draft, setDraft] = useState<Record<string, Draft>>({});
  // An upload goes to the project's resources, then becomes the picture it was for.
  const uploadFor = useRef('');
  const label = `the pictures of “${row.title}”`;
  useEffect(
    () =>
      uploads.onFinished((it) => {
        if (it.targetLabel !== label || !uploadFor.current) return;
        const r = it.result?.resource;
        if (!r) return;
        const name = uploadFor.current;
        setDraft((x) => ({ ...x, [name]: { id: r.id, title: r.title, thumb: resourceMedia.thumb(r.id) } }));
      }),
    [label],
  );
  const approved = !!row.approved && row.approved === d.version;
  const pictures = d.pictures.filter((p) => p.setting);
  const edits = Object.entries(draft);
  const busy = change.busy || task.busy;
  const replace = async (p: RemotionPictureRow) => {
    const picked = await dialog.open<ResourceRow | 'upload'>((close) => <ReplacePicker projectId={projectId} name={p.name} close={close} onFile={(f) => {
      uploadFor.current = p.name;
      void uploads.add([{ file: f, path: f.name }], { label, projectId });
      toast({ title: `Uploading ${f.name}`, body: `It goes to this project's resources, then it is ready to replace ${p.name}.` });
    }} />, { title: `Replace ${p.name}`, size: 'lg' });
    if (picked && picked !== 'upload') setDraft((x) => ({ ...x, [p.name]: { id: picked.id, title: picked.title, thumb: picked.thumbUrl || resourceMedia.thumb(picked.id) } }));
  };
  const send = (checkOnly: boolean) =>
    set.mutate(
      { source: row.name, projectId, set: edits.map(([name, x]) => ('original' in x ? { name } : { name, resource: x.id })), checkOnly },
      {
        onSuccess: (o) => {
          change.start(o);
          if (!checkOnly) setDraft({});
        },
        onError: failedToast(toast),
      },
    );
  return (
    <div className="mt-3" data-remotion-pictures="settings">
      <p className="mb-2 max-w-[72ch] text-[12.5px] leading-relaxed text-muted">
        {d.message} A new picture is drawn with no network and checked (does it show, does its shape fit), then the latest cuts&apos; clips are made again with it.
      </p>
      {!approved ? <p className="mb-2 text-[12.5px] text-muted">Version {d.versionNumber} isn&apos;t approved yet, so its pictures can&apos;t change for cuts until a person approves it.</p> : null}
      <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {pictures.map((p) => (
          <li key={p.name}>
            <PictureCard
              p={p}
              draft={draft[p.name]}
              disabled={!approved || busy}
              onReplace={() => void replace(p)}
              onOriginal={() => setDraft((x) => ({ ...x, [p.name]: { original: true } }))}
              onForget={() =>
                setDraft((x) => {
                  const next = { ...x };
                  delete next[p.name];
                  return next;
                })
              }
            />
          </li>
        ))}
      </ul>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" disabled={!approved || !edits.length || busy} loading={set.isPending && !!set.variables?.checkOnly} onClick={() => send(true)}>
          Check the new pictures
        </Button>
        <Button size="sm" variant="primary" disabled={!approved || !edits.length || busy} loading={set.isPending && !set.variables?.checkOnly} onClick={() => send(false)}>
          Use the new pictures
        </Button>
        {edits.length ? (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDraft({})}>
            Undo my choices
          </Button>
        ) : null}
        {change.busy ? <Working line={change.line} pct={change.pct} /> : null}
      </div>
      {more || d.waiting ? (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-[12.5px] text-muted">
          {d.waiting ? (
            <>
              <span>Version {d.waiting.number} makes more of its pictures settings.</span>
              <Button size="sm" variant="secondary" onClick={() => onReview(d.waiting!.hash)}>
                Review version {d.waiting.number}
              </Button>
            </>
          ) : (
            <>
              <span>{plural(d.more, 'more picture')} can become a setting with the one-time change.</span>
              {more}
              {task.busy ? <Working line={task.line} pct={task.pct} /> : null}
            </>
          )}
        </div>
      ) : null}
      {d.pending ? <PicturesCheck title="The last check (nothing changed yet)" c={d.pending} /> : null}
      {d.last ? <PicturesCheck title={`The last change (${d.number})`} c={d.last} /> : null}
      <Details compact className="mt-3">
        {d.pictures.map((p) => (
          <div key={p.name}>
            {p.name}: {p.file || 'named in code'}
            {p.uses.length ? ` — ${p.uses.map((u) => `${u.file}:${u.line}`).join(', ')}` : ''}
            {p.current ? ` — now ${p.current.resource ?? ''} (${(p.current.sha256 ?? '').slice(0, 8)})` : ''}
          </div>
        ))}
        {d.left.map((l, i) => (
          <div key={`l${i}`}>
            Stays in the code: {l.file}:{l.line} {l.code} — {l.why}
          </div>
        ))}
      </Details>
    </div>
  );
}

function PictureCard({ p, draft, disabled, onReplace, onOriginal, onForget }: {
  p: RemotionPictureRow;
  draft?: Draft;
  disabled: boolean;
  onReplace: () => void;
  onOriginal: () => void;
  onForget: () => void;
}) {
  const where = p.seen[0] ?? (p.used.length ? `drawn by ${p.used.slice(0, 2).join(', ')}` : 'not seen on screen');
  const chosen = draft && !('original' in draft) ? draft : null;
  const backToOriginal = !!draft && 'original' in draft;
  const nowSrc = chosen ? chosen.thumb : p.changed && !backToOriginal ? p.currentUrl || undefined : undefined;
  const nowTitle = chosen ? chosen.title : p.current?.title || p.current?.resource || '';
  return (
    <div className="flex h-full flex-col rounded-[12px] bg-raised p-2.5 hairline" data-picture-setting={p.name} data-picture-changed={p.changed || undefined}>
      <div className="mb-1.5 flex min-w-0 flex-wrap items-baseline gap-x-2">
        <span className="text-[13px] font-medium text-fg">{p.name}</span>
        <Theirs className="min-w-0 truncate text-[11.5px] text-subtle">{where}</Theirs>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <figure className="min-w-0">
          <Thumb
            src={p.originalUrl || undefined}
            alt={`${p.name}, as the project has it`}
            className={cx('aspect-[4/3] w-full rounded-[9px] hairline', (p.changed && !backToOriginal) || chosen ? 'opacity-60' : '')}
            fit="contain"
            fallback={<ImageIcon size={18} className="text-subtle" />}
          />
          <figcaption className="mt-0.5 truncate text-[11px] text-subtle">
            The project&apos;s own{p.width && p.height ? ` · ${p.width}×${p.height}` : ''}
          </figcaption>
        </figure>
        <figure className="min-w-0" data-picture-now>
          {nowSrc ? (
            <Thumb src={nowSrc} alt={`${p.name}, replaced by ${nowTitle}`} className="aspect-[4/3] w-full rounded-[9px] hairline" fit="contain" fallback={<ImageIcon size={18} className="text-subtle" />} />
          ) : (
            <div className="grid aspect-[4/3] w-full place-items-center rounded-[9px] px-2 text-center text-[11.5px] text-subtle hairline">
              {backToOriginal ? 'Back to its own' : 'Not replaced'}
            </div>
          )}
          <figcaption className="mt-0.5 flex min-w-0 items-center gap-1 text-[11px] text-subtle">
            {chosen ? <Badge tone="accent">new</Badge> : null}
            {nowSrc ? <Theirs className="min-w-0 truncate">{nowTitle}</Theirs> : null}
          </figcaption>
        </figure>
      </div>
      <div className="mt-2 flex flex-wrap gap-1.5">
        <Button size="sm" variant="secondary" icon={<UploadSimple size={13} />} disabled={disabled} onClick={onReplace} data-picture-replace={p.name}>
          Replace…
        </Button>
        {(p.changed && !backToOriginal) || chosen ? (
          <Button size="sm" variant="ghost" icon={<ArrowCounterClockwise size={13} />} disabled={disabled} onClick={chosen && !p.changed ? onForget : onOriginal}>
            {chosen && !p.changed ? 'Keep its own' : 'Back to its own'}
          </Button>
        ) : null}
        {draft && p.changed && backToOriginal ? (
          <Button size="sm" variant="ghost" disabled={disabled} onClick={onForget}>
            Keep the new one
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/** Where a new picture comes from: this computer (uploaded to the project's resources) or the resources. */
function ReplacePicker({ projectId, name, close, onFile }: { projectId: string; name: string; close: (v?: ResourceRow | 'upload') => void; onFile: (f: File) => void }) {
  const file = useRef<HTMLInputElement>(null);
  return (
    <div data-picture-picker={name}>
      <input
        ref={file}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml,.svg,.heic"
        className="hidden"
        aria-hidden
        tabIndex={-1}
        data-picture-upload={name}
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = '';
          if (!f) return;
          onFile(f);
          close('upload');
        }}
      />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" icon={<UploadSimple size={13} />} onClick={() => file.current?.click()}>
          Upload a picture…
        </Button>
        <span className="text-[12.5px] text-muted">or choose one of the project&apos;s pictures:</span>
      </div>
      <LibraryPicker kind="picture" projectId={projectId} close={(r) => close(r)} empty="No pictures here yet: upload one." />
    </div>
  );
}

function PicturesCheck({ title, c }: { title: string; c: RemotionPicturesCheckOut }) {
  const problems = c.check?.problems ?? [];
  return (
    <div className="mt-4" data-pictures-check>
      <h5 className="text-[13px] font-semibold text-fg">{title}</h5>
      {c.summary ? <p className="mt-0.5 max-w-[72ch] text-[12.5px] leading-relaxed text-muted">{c.summary}</p> : null}
      {problems.some((p) => p.severity === 'error') ? (
        <p className="mt-1 flex items-start gap-1.5 text-[12px] text-danger">
          <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" />
          <span>Nothing was changed.</span>
        </p>
      ) : null}
      {c.stills.length ? <StillPairs stills={c.stills} afterLabel="With the new pictures" /> : null}
    </div>
  );
}
