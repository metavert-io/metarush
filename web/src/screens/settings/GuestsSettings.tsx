import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { LinkSimple, Trash, UserCircleDashed, Warning } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { ops } from '@metarush/schema';
import { OwnerOnly, useOwnership } from '../../components/OwnerOnly';
import { LinkMade } from '../../components/ShareWithGuest';
import { absolute, plural, stamp } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, CopyButton, ErrorPanel, Segmented, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * Settings → People → Guests (D502): people outside the workspace an owner
 * shares versions with. On or off for the workspace (off signs every guest
 * out at once), where this computer answers them, how they sign in (with the
 * address to add to the organization's sign-in app), who has what with Take
 * back and a new sign-in link, and what guests did. Everyone sees it; owners
 * change it (guests.settings.set, guests.link, guests.revoke) and alone read
 * what guests did (guests.activity). The same as `metarush guests …`.
 */
export function GuestsSettings() {
  const st = useOp('guests.status', {}, { staleTime: 10_000, refetchInterval: 30_000 });
  const list = useOp('guests.list', {}, { staleTime: 10_000, refetchInterval: 30_000 });
  const o = useOwnership();
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [port, setPort] = useState('');
  const s = st.data;
  const refresh = () => {
    for (const k of ['guests.status', 'guests.list', 'guests.activity']) void qc.invalidateQueries({ queryKey: [k] });
  };
  const turn = async (on: boolean) => {
    if (!on) {
      const ok = await dialog.confirm({
        title: 'Turn guests off?',
        body: 'Every guest is signed out at once and sign-in links you sent stop working. What you shared stays, and works again when you turn guests back on.',
        confirmLabel: 'Turn guests off',
        tone: 'danger',
      });
      if (!ok) return;
    }
    setBusy(true);
    try {
      const out = await call('guests.settings.set', { on });
      refresh();
      toast({ tone: 'success', title: on ? 'Guests are on' : 'Guests are off', body: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Couldn’t change it', body: [toApiError(e).message, toApiError(e).hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  const savePort = async () => {
    const n = Number(port);
    setBusy(true);
    try {
      await call('guests.settings.set', { port: n });
      setPort('');
      refresh();
      toast({ tone: 'success', title: `Guests are answered at port ${n}` });
    } catch (e) {
      toast({ tone: 'error', title: 'Couldn’t move it', body: toApiError(e).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <section id="guests" className="panel scroll-mt-28 p-5" aria-labelledby="guests-h" data-guests-settings>
      <h2 id="guests-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <UserCircleDashed size={17} />
        </span>
        Guests
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        People outside the workspace you share versions with, from a version’s page (Share with a guest…). They watch on MetaRush’s guest page — never the dashboard —
        and commenters leave comments that come back to the version. Nothing is reachable from the internet yet: the guest address is this computer’s own, until a
        later release gives guests a public address.
      </p>
      {st.error ? <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} /> : null}
      {st.isLoading || !s ? (
        <Skeleton className="h-16" />
      ) : (
        <div className="space-y-4">
          <OwnerOnly className="space-y-3">
            <div className="flex flex-wrap items-center gap-3">
              <span className="w-40 text-[12.5px] text-muted">Let guests in</span>
              <Segmented
                label="Let guests in"
                value={s.on && s.listening ? 'on' : 'off'}
                onChange={(v) => void turn(v === 'on')}
                items={[
                  { value: 'on', label: 'On' },
                  { value: 'off', label: 'Off' },
                ]}
              />
              {busy ? <span className="text-[12px] text-subtle">Changing…</span> : null}
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <span className="w-40 text-[12.5px] text-muted">The guest address</span>
              <code className="tc rounded-[8px] bg-inset px-2 py-1 text-[12px] text-[#efe8de]" data-guests-address>
                {s.address}
              </code>
              <Badge tone={s.running ? 'ok' : 'neutral'}>{s.running ? 'Answering' : 'Not answering'}</Badge>
              <label className="ml-auto flex items-center gap-2 text-[12.5px] text-muted">
                Port
                <input
                  value={port}
                  onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, '').slice(0, 5))}
                  placeholder={String(s.port)}
                  inputMode="numeric"
                  className="h-8 w-20 rounded-[8px] bg-panel px-2 text-[13px] hairline"
                  aria-label="The guest listener's port"
                  data-guests-port
                />
                <Button size="sm" variant="secondary" disabled={!port} onClick={() => void savePort()}>
                  Move
                </Button>
              </label>
            </div>
          </OwnerOnly>
          <div className="space-y-1 text-[12.5px] text-fg" data-guests-lines>
            {s.lines.map((l) => (
              <p key={l}>{l}</p>
            ))}
          </div>
          {s.ssoCallback ? (
            <div className="flex flex-wrap items-center gap-2 rounded-[12px] px-3 py-2 text-[12.5px] hairline">
              <span className="text-muted">Add to the {s.sso} app’s addresses for guests:</span>
              <code className="tc min-w-0 break-all rounded-[8px] bg-inset px-2 py-1 text-[12px] text-[#efe8de]">{s.ssoCallback}</code>
              <CopyButton text={s.ssoCallback} />
            </div>
          ) : null}
        </div>
      )}
      <WhoHasWhat list={list.data} loading={list.isLoading} error={list.error} owner={o.owner} onChange={refresh} />
      {o.owner ? <Activity /> : <p className="mt-5 text-[12.5px] text-muted">Only owners see what guests did.</p>}
    </section>
  );
}

function WhoHasWhat({ list, loading, error, owner, onChange }: { list?: ops.GuestsListOut; loading: boolean; error: unknown; owner: boolean; onChange: () => void }) {
  const dialog = useDialog();
  const toast = useToast();
  const link = async (g: ops.GuestView) => {
    try {
      const out = await call('guests.link', { guest: g.guest.email });
      await dialog.open<void>((close) => <LinkMade out={out} onClose={() => close()} />, { title: `A sign-in link for ${g.guest.name || g.guest.email}`, size: 'md' });
    } catch (e) {
      toast({ tone: 'error', title: 'No link was made', body: toApiError(e).message });
    }
  };
  const takeBack = async (g: ops.GuestView, gr?: ops.GrantView) => {
    const who = g.guest.name || g.guest.email;
    const ok = await dialog.confirm({
      title: gr ? `Take this back from ${who}?` : `Take back everything shared with ${who}?`,
      body: gr
        ? 'It stops at once: if nothing else is shared with them, they are signed out. Comments they left stay on the versions.'
        : 'Everything shared with them stops at once and they are signed out. Comments they left stay on the versions.',
      confirmLabel: 'Take it back',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('guests.revoke', gr ? { grant: gr.grant.id } : { guest: g.guest.email });
      onChange();
      toast({ tone: 'success', title: 'Taken back', body: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Not taken back', body: toApiError(e).message });
    }
  };
  return (
    <div className="mt-6" data-guests-list>
      <h3 className="mb-2 text-[13.5px] font-semibold">Who has what</h3>
      {loading ? (
        <Skeleton className="h-12" />
      ) : error ? (
        <ErrorPanel compact error={error} />
      ) : !list || list.guests.length === 0 ? (
        <p className="text-[12.5px] text-muted">Nothing is shared with guests now. Share a version from its page.</p>
      ) : (
        <ul className="divide-y divide-line rounded-[12px] hairline">
          {list.guests.map((g) => (
            <li key={g.guest.id} className="px-4 py-3 text-[13px]" data-guest={g.guest.email}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">
                  <Theirs>{g.guest.name || g.guest.email}</Theirs>
                </span>
                {g.guest.name ? <span className="text-[12px] text-subtle">{g.guest.email}</span> : null}
                {g.signedIn > 0 ? <Badge tone="ok">Signed in</Badge> : null}
                {g.guest.lastSeenAt ? (
                  <span className="text-[12px] text-subtle" title={absolute(g.guest.lastSeenAt)}>
                    last seen {stamp(g.guest.lastSeenAt)}
                  </span>
                ) : null}
                {owner ? (
                  <span className="ml-auto flex flex-wrap gap-1.5">
                    <Button size="sm" variant="secondary" icon={<LinkSimple size={13} />} onClick={() => void link(g)} data-guest-link>
                      Sign-in link…
                    </Button>
                    <Button size="sm" variant="ghost" icon={<Trash size={13} />} onClick={() => void takeBack(g)} data-guest-revoke-all>
                      Take back everything
                    </Button>
                  </span>
                ) : null}
              </div>
              <ul className="mt-1 space-y-1">
                {g.grants.map((gr) => (
                  <li key={gr.grant.id} className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted" data-guest-grant={gr.grant.id}>
                    <span className="min-w-0 flex-1">{gr.summary}</span>
                    {owner && gr.live ? (
                      <Button size="sm" variant="ghost" onClick={() => void takeBack(g, gr)} data-guest-revoke>
                        Take back
                      </Button>
                    ) : null}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Activity() {
  const act = useOp('guests.activity', { limit: 50 }, { staleTime: 10_000, refetchInterval: 30_000 });
  const a = act.data;
  return (
    <div className="mt-6" data-guests-activity>
      <h3 className="mb-2 text-[13.5px] font-semibold">What guests did</h3>
      {act.isLoading ? (
        <Skeleton className="h-12" />
      ) : act.error ? (
        <ErrorPanel compact error={act.error} />
      ) : !a || a.rows.length === 0 ? (
        <p className="text-[12.5px] text-muted">No guest has opened anything yet.</p>
      ) : (
        <>
          {a.unusual.length ? (
            <p className="mb-2 flex items-start gap-1.5 rounded-[10px] bg-info-soft px-3 py-2 text-[12.5px] text-info" data-guests-unusual>
              <Warning size={14} className="mt-0.5 shrink-0" /> Unusually many requests in the last hour: {a.unusual.join(', ')}.
            </p>
          ) : null}
          <ul className="max-h-[360px] divide-y divide-line overflow-y-auto rounded-[12px] hairline">
            {a.rows.map((r) => (
              <li key={r.activity.id} className="flex flex-wrap gap-x-3 px-4 py-2 text-[12.5px]" data-guest-activity={r.activity.what}>
                <span className="w-28 shrink-0 text-subtle" title={absolute(r.activity.at)}>
                  {stamp(r.activity.at)}
                </span>
                <span className={r.activity.refused ? 'text-danger' : 'text-fg'}>
                  {r.who} {r.says}
                </span>
              </li>
            ))}
          </ul>
          <p className="mt-1 text-[12px] text-subtle">{plural(a.rows.length, 'request')} shown, newest first.</p>
        </>
      )}
    </div>
  );
}
