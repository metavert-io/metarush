import { useEffect, useId, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowsClockwise, LinkSimple, Trash, UserCirclePlus, Warning } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { ops } from '@metarush/schema';
import { plural } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, CopyButton, ErrorPanel, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { ownerTitle, useOwnership } from './OwnerOnly';

/*
 * Share with a guest (D490–D502): beside Share for review on a version's
 * page, an owner shares this version — or every version of the project with a
 * review copy — with someone outside the workspace, as a viewer or a
 * commenter, until a day they choose, then makes a one-time sign-in link to
 * send them. Guests open MetaRush's guest page, never the dashboard. The same
 * operations as `metarush guests …`: guests.share (a dry run first says what
 * the guest sees and whether What may leave lets it go), guests.link,
 * guests.revoke. Nothing is reachable from the internet yet: the link opens on
 * this computer.
 */

const fieldClass =
  'w-full rounded-[10px] bg-panel px-3 py-2 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';

const DAYS = [1, 3, 7, 14, 30, 60, 90];

function emailLike(s: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.trim());
}

/** The button beside Share for review…: owners share; anyone else sees who may. */
export function ShareWithGuestButton({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const dialog = useDialog();
  const qc = useQueryClient();
  const o = useOwnership();
  const open = async () => {
    const done = await dialog.open<boolean>((close) => <GuestShareForm projectId={projectId} revisionId={revisionId} number={number} onClose={close} />, {
      title: 'Share with a guest',
      size: 'lg',
    });
    if (done) void qc.invalidateQueries({ queryKey: ['guests.list'] });
  };
  return (
    <Button size="sm" variant="secondary" icon={<UserCirclePlus size={13} />} onClick={() => void open()} disabled={!o.owner} title={ownerTitle(o, 'share with guests')} data-guest-share-open>
      Share with a guest…
    </Button>
  );
}

/** Who outside the workspace may see this version, with what owners can do for each. */
export function GuestsOfVersion({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const list = useOp('guests.list', { projectId });
  const o = useOwnership();
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: ['guests.list'] });
  const rows = (list.data?.guests ?? []).flatMap((g) =>
    g.grants.filter((gr) => gr.live && (gr.grant.revisionIds.length === 0 || gr.grant.revisionIds.includes(revisionId))).map((gr) => ({ g, gr })),
  );
  if (list.isLoading) return <Skeleton className="mt-3 h-10" />;
  if (list.error) return <ErrorPanel error={list.error} compact onRetry={() => void list.refetch()} className="mt-3" />;
  if (rows.length === 0) return null;
  const link = async (g: ops.GuestView) => {
    try {
      const out = await call('guests.link', { guest: g.guest.email });
      await dialog.open<void>((close) => <LinkMade out={out} onClose={() => close()} />, { title: `A sign-in link for ${g.guest.name || g.guest.email}`, size: 'md' });
    } catch (e) {
      toast({ tone: 'error', title: 'No link was made', body: toApiError(e).message });
    }
  };
  const takeBack = async (g: ops.GuestView, gr: ops.GrantView) => {
    const ok = await dialog.confirm({
      title: `Take this back from ${g.guest.name || g.guest.email}?`,
      body: 'It stops at once: if nothing else is shared with them, they are signed out. Comments they left stay on the versions.',
      confirmLabel: 'Take it back',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('guests.revoke', { grant: gr.grant.id });
      refresh();
      toast({ tone: 'success', title: 'Taken back', body: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Not taken back', body: toApiError(e).message });
    }
  };
  return (
    <div className="mt-4" data-guests-of-version>
      <h3 className="mb-2 text-[13px] font-semibold text-muted">Guests who can see version {number}</h3>
      <ul className="divide-y divide-line rounded-[12px] hairline">
        {rows.map(({ g, gr }) => (
          <li key={gr.grant.id} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3 text-[13px]" data-guest-row={g.guest.email}>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-fg">
                  <Theirs>{g.guest.name || g.guest.email}</Theirs>
                </span>
                {g.guest.name ? <span className="text-[12px] text-subtle">{g.guest.email}</span> : null}
                <Badge tone={gr.grant.role === 'commenter' ? 'accent' : 'neutral'}>{gr.grant.role === 'commenter' ? 'Watches and comments' : 'Watches'}</Badge>
                {g.signedIn > 0 ? <Badge tone="ok">Signed in</Badge> : null}
              </div>
              <div className="mt-0.5 text-[12.5px] text-muted">{gr.summary}</div>
            </div>
            {o.owner ? (
              <div className="flex flex-wrap gap-1.5">
                <Button size="sm" variant="secondary" icon={<LinkSimple size={13} />} onClick={() => void link(g)} data-guest-link>
                  Sign-in link…
                </Button>
                <Button size="sm" variant="ghost" icon={<Trash size={13} />} onClick={() => void takeBack(g, gr)} data-guest-revoke>
                  Take back
                </Button>
              </div>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** A link made: shown once, to copy and send. */
export function LinkMade({ out, onClose }: { out: ops.GuestsLinkOut; onClose: () => void }) {
  return (
    <div className="space-y-4" data-guest-link-made>
      <p className="text-[13.5px] text-fg">{out.summary}</p>
      <div className="flex items-start gap-2 rounded-[12px] bg-inset p-3">
        <code className="tc min-w-0 flex-1 break-all text-[12px] leading-relaxed text-[#efe8de]" data-guest-link-url>
          {out.url}
        </code>
      </div>
      {out.warnings.map((w) => (
        <p key={w} className="flex items-start gap-1.5 text-[12.5px] text-info">
          <Warning size={14} className="mt-0.5 shrink-0" /> {w}
        </p>
      ))}
      <div className="flex justify-end gap-2">
        <CopyButton text={out.url} label="Copy link" />
        <Button variant="primary" size="sm" onClick={onClose} data-guest-link-done>
          Done
        </Button>
      </div>
    </div>
  );
}

/** The form: who, what, may they comment, until when; what leaves; then the link. */
function GuestShareForm({ projectId, revisionId, number, onClose }: { projectId: string; revisionId: string; number: number; onClose: (v?: boolean) => void }) {
  const uid = useId();
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [whole, setWhole] = useState(false);
  const [role, setRole] = useState<'viewer' | 'commenter'>('commenter');
  const [days, setDays] = useState(14);
  const [message, setMessage] = useState('');
  const [plan, setPlan] = useState<ops.GuestsShareOut>();
  const [planning, setPlanning] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [shared, setShared] = useState<ops.GuestsShareOut>();
  const [link, setLink] = useState<ops.GuestsLinkOut>();
  const ask = useRef(0);
  const input: ops.GuestsShareIn = { email: email.trim(), name: name.trim(), projectId, role, days, ...(whole ? {} : { versions: [revisionId] }), ...(message.trim() ? { message: message.trim() } : {}) };
  const key = JSON.stringify(input);
  // What the guest would see, and whether it may be shared (nothing is made).
  useEffect(() => {
    if (!emailLike(email)) {
      setPlan(undefined);
      return;
    }
    const n = ++ask.current;
    setPlanning(true);
    const t = setTimeout(() => {
      call('guests.share', { ...input, dryRun: true })
        .then((p) => n === ask.current && setPlan(p))
        .catch((e) => n === ask.current && setError(toApiError(e).message))
        .finally(() => n === ask.current && setPlanning(false));
    }, 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const share = async () => {
    setBusy(true);
    setError(undefined);
    try {
      setShared(await call('guests.share', input));
    } catch (e) {
      setError(toApiError(e).message);
    } finally {
      setBusy(false);
    }
  };
  const makeLink = async () => {
    setBusy(true);
    setError(undefined);
    try {
      setLink(await call('guests.link', { guest: input.email }));
    } catch (e) {
      setError(toApiError(e).message);
    } finally {
      setBusy(false);
    }
  };

  if (link) return <LinkMade out={link} onClose={() => onClose(true)} />;
  if (shared) {
    return (
      <div className="space-y-4" data-guest-shared>
        <p className="text-[13.5px] text-fg">{shared.summary}</p>
        {shared.warnings.map((w) => (
          <p key={w} className="flex items-start gap-1.5 text-[12.5px] text-info">
            <Warning size={14} className="mt-0.5 shrink-0" /> {w}
          </p>
        ))}
        {error ? (
          <p role="alert" className="rounded-[12px] bg-danger-soft px-3 py-2.5 text-[13px] text-danger">
            {error}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => onClose(true)}>
            Later
          </Button>
          <Button variant="primary" size="sm" loading={busy} icon={<LinkSimple size={13} />} onClick={() => void makeLink()} data-guest-make-link>
            Make a sign-in link
          </Button>
        </div>
      </div>
    );
  }

  const label = 'mb-1.5 block text-[12.5px] font-medium text-muted';
  const radio = 'inline-flex items-center gap-1.5 text-[13px] text-fg';
  return (
    <div className="space-y-4" data-guest-form>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label htmlFor={`${uid}-email`} className={label}>
            Their email
          </label>
          <input id={`${uid}-email`} type="email" value={email} maxLength={254} onChange={(e) => { setEmail(e.target.value); setError(undefined); }} placeholder="dana@example.com" className={`${fieldClass} h-9`} data-guest-email />
        </div>
        <div>
          <label htmlFor={`${uid}-name`} className={label}>
            What to call them (optional)
          </label>
          <input id={`${uid}-name`} value={name} maxLength={80} onChange={(e) => setName(e.target.value)} placeholder="Dana at the client" className={`${fieldClass} h-9`} data-guest-name />
        </div>
      </div>
      <fieldset className="space-y-1.5">
        <legend className={label}>What they see</legend>
        <label className={radio}>
          <input type="radio" name={`${uid}-what`} checked={!whole} onChange={() => setWhole(false)} className="accent-[var(--accent)]" data-guest-what="version" />
          Version {number}
        </label>
        <label className={`${radio} ml-4`}>
          <input type="radio" name={`${uid}-what`} checked={whole} onChange={() => setWhole(true)} className="accent-[var(--accent)]" data-guest-what="project" />
          Every version of the project with a review copy, now and later
        </label>
      </fieldset>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <fieldset className="space-y-1.5">
          <legend className={label}>What they may do</legend>
          <label className={radio}>
            <input type="radio" name={`${uid}-role`} checked={role === 'viewer'} onChange={() => setRole('viewer')} className="accent-[var(--accent)]" data-guest-role="viewer" />
            Watch
          </label>
          <label className={`${radio} ml-4`}>
            <input type="radio" name={`${uid}-role`} checked={role === 'commenter'} onChange={() => setRole('commenter')} className="accent-[var(--accent)]" data-guest-role="commenter" />
            Watch and comment
          </label>
        </fieldset>
        <div>
          <label htmlFor={`${uid}-days`} className={label}>
            For how long
          </label>
          <select id={`${uid}-days`} value={days} onChange={(e) => setDays(Number(e.target.value))} className={`${fieldClass} h-9`} data-guest-days>
            {DAYS.map((d) => (
              <option key={d} value={d}>
                {plural(d, 'day')}
              </option>
            ))}
          </select>
        </div>
      </div>
      <div>
        <label htmlFor={`${uid}-msg`} className={label}>
          A note for them (optional)
        </label>
        <textarea id={`${uid}-msg`} rows={2} maxLength={500} value={message} onChange={(e) => setMessage(e.target.value)} placeholder="Is the ending right?" className={fieldClass} data-guest-message />
      </div>

      <div className="rounded-[12px] px-4 py-3 hairline" data-guest-leaves>
        <div className="flex items-center gap-2 text-[12.5px] font-medium text-muted">
          What the guest sees {planning ? <ArrowsClockwise size={12} className="animate-spin" aria-hidden /> : null}
        </div>
        {!emailLike(email) ? (
          <p className="mt-1 text-[12.5px] text-muted">Give their email to see what they would see.</p>
        ) : plan ? (
          <>
            {plan.may ? (
              <ul className="mt-1 list-disc space-y-0.5 pl-4 text-[13px] text-fg">
                {plan.exposes.map((x) => (
                  <li key={x}>{x}</li>
                ))}
              </ul>
            ) : null}
            <p className="mt-2 text-[12.5px] text-muted">
              They open it on MetaRush’s guest page, never the dashboard, and see nothing else of this workspace. Nothing is reachable from the internet yet: the guest
              address is this computer’s own.
            </p>
            {plan.warnings.map((w) => (
              <p key={w} className="mt-1 flex items-start gap-1.5 text-[12.5px] text-info">
                <Warning size={14} className="mt-0.5 shrink-0" /> {w}
              </p>
            ))}
          </>
        ) : (
          <Skeleton className="mt-2 h-10" />
        )}
      </div>

      {plan && !plan.may ? (
        <div role="alert" className="rounded-[12px] bg-danger-soft px-3 py-2.5 text-[13px] text-danger" data-guest-refused>
          {plan.why}
          {plan.hint ? <div className="mt-1 text-[12.5px] opacity-90">{plan.hint}</div> : null}
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
        <Button variant="primary" size="sm" loading={busy} disabled={!plan?.may || planning} onClick={() => void share()} data-guest-share>
          Share
        </Button>
      </div>
    </div>
  );
}
