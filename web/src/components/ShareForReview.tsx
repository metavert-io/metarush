import { useEffect, useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowClockwise, LinkSimple, ShareNetwork, Trash, Warning } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { ops } from '@metarush/schema';
import { absolute, plural, stamp } from '../lib/format';
import { copyText } from '../lib/hooks';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, CopyButton, cx, Details, ErrorPanel, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * Share for review (D313–D317): a person sends someone outside the workspace
 * an expiring link to this version's review copy, its words if they allow,
 * and a place to leave comments pinned to times. The page is kept in the
 * workspace's own cloud bucket until the link ends; comments come back as
 * this version's comments, marked "(outside)". The same ops as `metarush
 * share …`: share.plan says what a link would show and whether it may be
 * made, share.create makes it (people only), share.link signs it again,
 * share.revoke takes it back.
 */

type Speakers = 'labels' | 'names' | 'none';

const fieldClass =
  'w-full rounded-[10px] bg-panel px-3 py-2 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';

export function ShareForReview({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const links = useOp('share.list', { projectId, revision: revisionId, all: true });
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['share.list'] });
    void qc.invalidateQueries({ queryKey: ['comment.list'] });
  };
  const list = links.data?.links ?? [];
  const live = list.filter((l) => l.live);
  const ended = list.filter((l) => !l.live);

  const open = async () => {
    const made = await dialog.open<ops.ShareOut>((close) => <ShareForm projectId={projectId} revisionId={revisionId} number={number} onClose={close} />, {
      title: `Share version ${number} for review`,
      size: 'lg',
    });
    if (made) refresh();
  };
  const copyAgain = async (l: ops.ShareView) => {
    try {
      const out = await call('share.link', { id: l.link.id });
      if (out.url && (await copyText(out.url))) toast({ tone: 'success', title: 'Link copied', body: `It works ${out.ends}.` });
    } catch (e) {
      toast({ tone: 'error', title: 'The link couldn’t be copied', body: toApiError(e).message });
    }
  };
  const takeBack = async (l: ops.ShareView) => {
    const ok = await dialog.confirm({
      title: 'Take this review link back?',
      body: 'Its page and its copy of the video are deleted at once, so the link no longer opens. Comments already in stay on this version.',
      confirmLabel: 'Take it back',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('share.revoke', { id: l.link.id });
      refresh();
      // Say what was really done: a link whose page couldn't be deleted yet is a warning, not a success.
      const warnings = out.warnings ?? [];
      toast({ tone: warnings.length ? 'info' : 'success', title: 'Link taken back', body: [out.summary, ...warnings].join(' ') });
    } catch (e) {
      toast({ tone: 'error', title: 'Not taken back', body: toApiError(e).message });
    }
  };
  const look = async (l: ops.ShareView) => {
    try {
      const out = await call('share.sync', { id: l.link.id });
      refresh();
      toast({ tone: out.comments > 0 ? 'success' : 'info', title: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Comments couldn’t be read', body: toApiError(e).message });
    }
  };

  return (
    <section aria-labelledby="share-h" className="mt-8" data-share>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 id="share-h" className="text-[14px] font-semibold">
          Shared for review
        </h2>
        {live.length ? <Badge tone="accent">{plural(live.length, 'link')} working</Badge> : null}
        <Button size="sm" variant="secondary" className="ml-auto" icon={<ShareNetwork size={13} />} onClick={() => void open()} data-share-open>
          Share for review…
        </Button>
      </div>
      {links.isLoading ? (
        <Skeleton className="h-14" />
      ) : links.error ? (
        <ErrorPanel error={links.error} compact onRetry={() => void links.refetch()} />
      ) : list.length === 0 ? (
        <p className="text-[13px] text-muted">
          Send this version to a client or a colleague without an account: they watch it, read what is said if you allow, and leave comments pinned to times,
          which come back here.
        </p>
      ) : (
        <ul className="divide-y divide-line rounded-[12px] hairline" data-share-links>
          {[...live, ...ended].map((l) => (
            <li key={l.link.id} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3 text-[13px]" data-share-link={l.link.id} data-share-state={l.link.state}>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-fg">{l.link.for ? <Theirs>{l.link.for}</Theirs> : 'Someone outside the workspace'}</span>
                  <Badge tone={l.live ? 'ok' : 'neutral'}>{l.stateWords}</Badge>
                  {l.link.draft ? <Badge tone="info">Marked as a draft</Badge> : null}
                  <span className="text-[12px] text-subtle" title={absolute(l.link.createdAt)}>
                    sent by {l.link.createdBy.userName ?? 'someone'}, {stamp(l.link.createdAt)}
                  </span>
                </div>
                <div className="mt-0.5 text-[12.5px] text-muted">
                  {l.live ? `Works ${l.ends}` : capFirst(l.ends)} · {l.link.comments ? `${plural(l.link.comments, 'comment')} came back` : 'no comments yet'}
                  {l.link.waiting ? ` · ${plural(l.link.waiting, 'more')} waiting for the next hour` : ''}
                </div>
                <Details label="What it shows" compact className="mt-1">
                  <ul className="list-disc space-y-0.5 pl-4 text-[12.5px] text-muted">
                    {l.link.exposes.map((x) => (
                      <li key={x}>{x}</li>
                    ))}
                  </ul>
                  {l.link.anyway ? (
                    <p className="mt-1 text-[12.5px] text-muted">
                      Sent while the ship gate held it: <Theirs>{l.link.anyway.note}</Theirs>
                    </p>
                  ) : null}
                </Details>
              </div>
              {l.live ? (
                <div className="flex flex-wrap gap-1.5">
                  <Button size="sm" variant="secondary" icon={<LinkSimple size={13} />} onClick={() => void copyAgain(l)} data-share-copy>
                    Copy link
                  </Button>
                  <Button size="sm" variant="ghost" icon={<ArrowClockwise size={13} />} onClick={() => void look(l)} data-share-look>
                    Look for comments
                  </Button>
                  <Button size="sm" variant="ghost" icon={<Trash size={13} />} onClick={() => void takeBack(l)} data-share-revoke>
                    Take back
                  </Button>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function capFirst(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

/** The form: who it's for, what it shows, for how long; what leaves, and the link once made. */
function ShareForm({ projectId, revisionId, number, onClose }: { projectId: string; revisionId: string; number: number; onClose: (v?: ops.ShareOut) => void }) {
  const uid = useId();
  const [forWhom, setFor] = useState('');
  const [message, setMessage] = useState('');
  const [title, setTitle] = useState('');
  const [days, setDays] = useState(0);
  const [words, setWords] = useState(true);
  const [speakers, setSpeakers] = useState<Speakers>('labels');
  const [download, setDownload] = useState(false);
  const [asDraft, setAsDraft] = useState<boolean>();
  const [anyway, setAnyway] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [made, setMade] = useState<ops.ShareOut>();
  const input = { projectId, revision: revisionId, words, speakers, download, ...(days ? { days } : {}), ...(asDraft ? { asDraft: true } : {}), ...(anyway.trim() ? { anyway } : {}) };
  const plan = useOp('share.plan', input, { placeholderData: (prev) => prev });
  const p = plan.data;
  // A version nobody approved goes out marked as a draft unless the person says otherwise.
  useEffect(() => {
    if (p && asDraft === undefined) setAsDraft(!p.approved);
  }, [p, asDraft]);
  useEffect(() => {
    if (p && !title) setTitle(p.title);
  }, [p, title]);

  const make = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const out = await call('share.create', { ...input, title: title.trim(), for: forWhom.trim(), message: message.trim() });
      setMade(out);
    } catch (e) {
      setError(toApiError(e).message);
    } finally {
      setBusy(false);
    }
  };

  if (made) {
    return (
      <div className="space-y-4" data-share-made>
        <p className="text-[13.5px] text-fg">{made.summary}</p>
        <div className="flex items-start gap-2 rounded-[12px] bg-inset p-3">
          <code className="tc min-w-0 flex-1 break-all text-[12px] leading-relaxed text-[#efe8de]" data-share-url>
            {made.url}
          </code>
        </div>
        <p className="text-[12.5px] text-muted">
          Send it the way you usually do (email, chat). It works {made.ends}; anyone who has it can watch, so send it only to who should review. You can take it
          back at any time below the version.
        </p>
        {made.warnings.map((w) => (
          <p key={w} className="flex items-start gap-1.5 text-[12.5px] text-info">
            <Warning size={14} className="mt-0.5 shrink-0" /> {w}
          </p>
        ))}
        <div className="flex justify-end gap-2">
          <CopyButton text={made.url ?? ''} label="Copy link" />
          <Button variant="primary" size="sm" onClick={() => onClose(made)} data-share-done>
            Done
          </Button>
        </div>
      </div>
    );
  }

  const label = 'mb-1.5 block text-[12.5px] font-medium text-muted';
  const check = 'flex items-start gap-2 text-[13px] text-fg';
  return (
    <div className="space-y-4" data-share-form>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label htmlFor={`${uid}-for`} className={label}>
            Who it’s for (their name fills in on the page)
          </label>
          <input id={`${uid}-for`} value={forWhom} maxLength={80} onChange={(e) => setFor(e.target.value)} placeholder="Dana at the client" className={`${fieldClass} h-9`} data-share-for />
        </div>
        <div>
          <label htmlFor={`${uid}-days`} className={label}>
            How long it works
          </label>
          <select id={`${uid}-days`} value={days || p?.maxDays || 7} onChange={(e) => setDays(Number(e.target.value))} className={`${fieldClass} h-9`} data-share-days>
            {Array.from({ length: p?.maxDays ?? 7 }, (_, i) => i + 1).map((d) => (
              <option key={d} value={d}>
                {plural(d, 'day')}
              </option>
            ))}
          </select>
        </div>
      </div>
      <div>
        <label htmlFor={`${uid}-title`} className={label}>
          The title on the page
        </label>
        <input id={`${uid}-title`} value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} className={`${fieldClass} h-9`} data-share-title />
      </div>
      <div>
        <label htmlFor={`${uid}-msg`} className={label}>
          A note for them (optional)
        </label>
        <textarea id={`${uid}-msg`} rows={2} maxLength={1000} value={message} onChange={(e) => setMessage(e.target.value)} placeholder="Is the ending right?" className={fieldClass} data-share-message />
      </div>
      <fieldset className="space-y-2">
        <legend className={label}>What the page shows besides the video</legend>
        <label className={check}>
          <input type="checkbox" checked={words} onChange={(e) => setWords(e.target.checked)} className="mt-[3px] accent-[var(--accent)]" data-share-words />
          <span>What is said, line by line (the script)</span>
        </label>
        {words ? (
          <div role="radiogroup" aria-label="Who speaks" className="ml-6 flex flex-wrap gap-x-4 gap-y-1 text-[12.5px]">
            {(
              [
                ['labels', 'Speaker 1, 2…'],
                ['names', 'Their names'],
                ['none', 'No one named'],
              ] as [Speakers, string][]
            ).map(([v, w]) => (
              <label key={v} className="inline-flex items-center gap-1.5">
                <input type="radio" name={`${uid}-sp`} checked={speakers === v} onChange={() => setSpeakers(v)} className="accent-[var(--accent)]" data-share-speakers={v} />
                {w}
              </label>
            ))}
          </div>
        ) : null}
        <label className={check}>
          <input type="checkbox" checked={download} onChange={(e) => setDownload(e.target.checked)} className="mt-[3px] accent-[var(--accent)]" data-share-download />
          <span>A button to download the video</span>
        </label>
        <label className={check}>
          <input type="checkbox" checked={!!asDraft} onChange={(e) => setAsDraft(e.target.checked)} className="mt-[3px] accent-[var(--accent)]" data-share-draft />
          <span>Mark it as a draft for review (not approved yet)</span>
        </label>
      </fieldset>

      {p?.held ? (
        <div className="rounded-[12px] bg-info-soft px-3 py-2.5 text-[12.5px] text-info" data-share-held>
          The ship gate holds version {number}: {p.holds.join('; ')}. Share it marked as a draft, or send it anyway with a note.
          {!asDraft ? (
            <input value={anyway} maxLength={500} onChange={(e) => setAnyway(e.target.value)} placeholder="Why it goes out anyway" className={cx(fieldClass, 'mt-2 h-9')} aria-label="Why it goes out anyway" />
          ) : null}
        </div>
      ) : null}

      <div className="rounded-[12px] px-4 py-3 hairline" data-share-leaves>
        <div className="text-[12.5px] font-medium text-muted">What leaves this computer</div>
        {plan.isLoading && !p ? (
          <Skeleton className="mt-2 h-10" />
        ) : p ? (
          <>
            <ul className="mt-1 list-disc space-y-0.5 pl-4 text-[13px] text-fg">
              {p.exposes.map((x) => (
                <li key={x}>{x}</li>
              ))}
            </ul>
            <p className="mt-2 text-[12.5px] text-muted">
              Kept in {p.store} {p.ends}, then deleted. Anyone with the link can watch and comment; the page reaches nothing else of this workspace.
            </p>
            {p.warnings.map((w) => (
              <p key={w} className="mt-1 flex items-start gap-1.5 text-[12.5px] text-info">
                <Warning size={14} className="mt-0.5 shrink-0" /> {w}
              </p>
            ))}
          </>
        ) : null}
      </div>

      {p && !p.may ? (
        <div role="alert" className="rounded-[12px] bg-danger-soft px-3 py-2.5 text-[13px] text-danger" data-share-refused>
          {p.why}
          {p.hint ? <div className="mt-1 text-[12.5px] opacity-90">{p.hint}</div> : null}
        </div>
      ) : null}
      {error ? (
        <div role="alert" className="rounded-[12px] bg-danger-soft px-3 py-2.5 text-[13px] text-danger">
          {error}
        </div>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={() => onClose()}>
          Cancel
        </Button>
        <Button variant="primary" size="sm" loading={busy} disabled={!p?.may} onClick={() => void make()} data-share-make>
          Make the link
        </Button>
      </div>
    </div>
  );
}
