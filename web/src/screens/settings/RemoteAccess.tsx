import { useId, useState } from 'react';
import { ArrowSquareOut, CheckCircle, Globe, SignOut, UserPlus, WarningCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { relative } from '../../lib/format';
import { useHost } from '../../lib/host';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, CopyBlock, CopyButton, Details, ErrorPanel, Skeleton } from '../../ui/primitives';

/*
 * Settings → People → Remote access (D128, D129): the address people use from their
 * other computers over Tailscale, whether it works yet, the HTTPS-certificate
 * hint, and who signs in with their Tailscale account (adding people only at
 * the computer that runs MetaRush; removing works from anywhere). Turning it
 * on or off and starting tailscale serve happen in a terminal on that
 * computer: the browser never runs them.
 */

type Person = { login: string; name?: string; thisComputer?: boolean; lastSignedInAt?: string; browsers: number };

export function RemoteAccessSettings() {
  const st = useOp('remote.status', {}, { refetchInterval: 30_000 });
  const host = useHost();
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const [adding, setAdding] = useState('');
  const [busy, setBusy] = useState('');
  const inputId = useId();
  const d = st.data;
  const refresh = () => void qc.invalidateQueries({ queryKey: ['remote.status'] });

  const add = async () => {
    const login = adding.trim();
    if (!login) return;
    setBusy('add');
    try {
      const out = await call('remote.allow', { add: [login] });
      setAdding('');
      toast({ tone: 'success', title: `${login} can sign in`, body: out.on ? `With their Tailscale account, at ${out.url}.` : 'Once remote access is on.' });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Could not add them', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy('');
      refresh();
    }
  };
  const remove = async (p: Person) => {
    const who = p.name ? `${p.name} (${p.login})` : p.login;
    const ok = await dialog.confirm({
      title: `Stop letting ${who} in?`,
      body: p.thisComputer
        ? 'This is the Tailscale account of this computer. You can still open MetaRush here, and from your other computers with a sign-in link.'
        : 'Their browsers are signed out at once. Everything they made stays, with their name on it. You can add them again later.',
      confirmLabel: 'Remove',
      tone: 'danger',
    });
    if (!ok) return;
    setBusy(p.login);
    try {
      const out = await call('remote.allow', { remove: [p.login] });
      toast({ tone: 'success', title: `${who} can no longer sign in`, body: out.signedOut ? `Signed out ${out.signedOut === 1 ? '1 browser' : `${out.signedOut} browsers`}.` : undefined });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not remove them', body: toApiError(e).message });
    } finally {
      setBusy('');
      refresh();
    }
  };
  const signOutAll = async () => {
    const ok = await dialog.confirm({
      title: 'Sign out every browser on another computer?',
      body: 'For a lost or shared computer. They need to sign in again (people on the list do that by opening the address). This computer stays signed in.',
      confirmLabel: 'Sign them out',
      tone: 'danger',
    });
    if (!ok) return;
    setBusy('signout');
    try {
      const out = await call('remote.signout', { all: true });
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not sign them out', body: toApiError(e).message });
    } finally {
      setBusy('');
      refresh();
    }
  };

  const httpsOff = d && d.tailscale.installed && d.tailscale.running && d.tailscale.dnsName && !d.tailscale.httpsReady;
  return (
    <section id="remote-access" className="panel scroll-mt-28 p-5" aria-labelledby="remote-h" data-remote-access>
      <h2 id="remote-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Globe size={17} />
        </span>
        Remote access
        {d ? <Badge className="ml-auto" tone={d.ready ? 'ok' : d.on ? 'accent' : 'neutral'}>{d.ready ? 'On' : d.on ? 'Not working yet' : 'Off'}</Badge> : null}
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        Open this dashboard from your other computers, and let people you choose open it from theirs, over your Tailscale network. MetaRush keeps answering only on {host.remote ? host.machine : 'this computer'}; Tailscale brings the visits to it securely.
      </p>
      {st.isLoading ? (
        <Skeleton className="h-24" />
      ) : st.error ? (
        <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} />
      ) : d ? (
        <div className="space-y-4 text-[13px]">
          <p className={d.ready ? 'flex items-start gap-2 text-fg' : 'text-fg'}>
            {d.ready ? <CheckCircle size={16} weight="fill" className="mt-[2px] shrink-0 text-ok" /> : null}
            <span>{d.summary}</span>
          </p>
          {d.on && d.url ? (
            <div className="flex flex-wrap items-center gap-2 rounded-[10px] bg-panel px-3 py-2 hairline">
              <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg" title={d.url} data-remote-url>
                {d.url}
              </span>
              <CopyButton text={d.url} label="Copy the address" />
              {!host.remote ? (
                <a href={d.url} target="_blank" rel="noreferrer" className="inline-flex h-7 items-center gap-1.5 rounded-[8px] px-2.5 text-[12.5px] text-muted hover:bg-hover hover:text-fg">
                  <ArrowSquareOut size={13} /> Open
                </a>
              ) : null}
            </div>
          ) : null}
          {httpsOff ? (
            <div className="flex items-start gap-2 rounded-[10px] bg-accent-soft px-3 py-2.5 text-[12.5px] leading-relaxed text-accent-text" role="note" data-https-hint>
              <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0" />
              <span>
                HTTPS certificates are off for your Tailscale network, so it can't give MetaRush a secure address yet (the microphone needs one). Turn them on in the Tailscale admin console: DNS, then HTTPS Certificates, then Enable.{' '}
                <a href="https://login.tailscale.com/admin/dns" target="_blank" rel="noreferrer" className="font-medium underline">
                  Open the admin console
                </a>
              </span>
            </div>
          ) : null}
          {d.steps.length > 0 ? (
            <div>
              <div className="mb-1 text-[12px] font-medium text-muted">Still to do</div>
              <ul className="list-disc space-y-1 pl-5 text-[12.5px] leading-relaxed text-muted">
                {d.steps.map((s) => (
                  <li key={s}>{s}</li>
                ))}
              </ul>
            </div>
          ) : null}

          <div>
            <div className="mb-1.5 flex flex-wrap items-center gap-2">
              <span className="text-[12px] font-medium text-muted">Who signs in with their Tailscale account</span>
            </div>
            {d.people.length === 0 ? (
              <p className="text-[12.5px] text-subtle">No one yet: people on other computers need a sign-in link from {host.remote ? host.machine : 'this computer'}.</p>
            ) : (
              <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="People who sign in with Tailscale">
                {d.people.map((p) => (
                  <li key={p.login} className="flex items-center gap-3 px-3 py-2.5" data-remote-person={p.login}>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[13px] text-fg">{p.name ? `${p.name} (${p.login})` : p.login}</div>
                      <div className="text-[11.5px] text-subtle">
                        {[p.thisComputer ? "this computer's Tailscale account" : '', p.lastSignedInAt ? `last signed in ${relative(p.lastSignedInAt)}` : 'not signed in yet', p.browsers > 0 ? `${p.browsers === 1 ? '1 browser' : `${p.browsers} browsers`} signed in now` : '']
                          .filter(Boolean)
                          .join(', ')}
                      </div>
                    </div>
                    <Button size="sm" variant="ghost" loading={busy === p.login} onClick={() => void remove(p)}>
                      Remove
                    </Button>
                  </li>
                ))}
              </ul>
            )}
            {host.remote ? (
              <p className="mt-2 text-[12px] text-subtle">Only someone at {host.machine} can let more people in.</p>
            ) : (
              <form
                className="mt-2.5 flex flex-wrap items-center gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  void add();
                }}
              >
                <label htmlFor={inputId} className="sr-only">
                  Their Tailscale account
                </label>
                <input
                  id={inputId}
                  value={adding}
                  onChange={(e) => setAdding(e.target.value)}
                  placeholder="Their Tailscale account (name@example.com)"
                  autoComplete="off"
                  spellCheck={false}
                  className="h-8 min-w-0 flex-1 rounded-[9px] bg-panel px-3 text-[12.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
                />
                <Button size="sm" type="submit" variant="secondary" icon={<UserPlus size={13} />} loading={busy === 'add'} disabled={!adding.trim()}>
                  Add a person
                </Button>
              </form>
            )}
          </div>
          {d.on ? (
            <div className="flex flex-wrap items-center gap-2 border-t border-line pt-3">
              <span className="min-w-0 flex-1 text-[12px] text-subtle">Lost a computer, or used a shared one? Sign out every browser on another computer.</span>
              <Button size="sm" variant="ghost" icon={<SignOut size={13} />} loading={busy === 'signout'} onClick={() => void signOutAll()}>
                Sign out other computers
              </Button>
            </div>
          ) : null}
          <p className="text-[12px] text-subtle">
            {d.on ? 'Turning remote access off, ' : 'Turning remote access on, '}
            and starting or stopping Tailscale's secure address, happen in a terminal on {host.remote ? host.machine : 'this computer'}: the browser never runs them.
          </p>
          <Details label="The commands for a terminal">
            <div className="grid grid-cols-1 gap-3">
              {!d.on ? <CopyBlock text="metarush remote enable --allow name@example.com" caption="Turns remote access on: this computer's Tailscale name becomes the address; the people you list (and you) sign in with their Tailscale account. Add `--apply` to start tailscale serve too." /> : null}
              <CopyBlock text={d.serveCommand} caption="Starts Tailscale's secure address for MetaRush (HTTPS on your tailnet, handed to MetaRush on this computer)." />
              {d.on ? <CopyBlock text={d.stopCommand} caption="Stops it again (other things tailscale serve offers stay)." /> : null}
              {d.on ? <CopyBlock text="metarush remote disable" caption="Turns remote access off and signs out every browser on another computer." /> : null}
              <CopyBlock text="metarush dashboard --print-link --remote" caption="A one-time sign-in link for a browser on another computer (signs in as you)." />
              <div className="text-[12px] text-subtle">
                Tailscale on this computer: {d.tailscale.installed ? (d.tailscale.running ? `connected as ${d.tailscale.hostName ?? ''}` : 'not connected') : 'not installed'}
                {d.tailscale.dnsName ? `, ${d.tailscale.dnsName}` : ''}
                {d.tailscale.version ? `, version ${d.tailscale.version}` : ''}
              </div>
            </div>
          </Details>
        </div>
      ) : null}
    </section>
  );
}
