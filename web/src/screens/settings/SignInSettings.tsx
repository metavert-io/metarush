import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { ArrowsClockwise, Buildings, CheckCircle, Warning } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { SSOSetIn, SSOSetup, SSOStatusOut } from '../../api/types';
import { OwnerOnly, useOwnership } from '../../components/OwnerOnly';
import { PasswordField, TextField } from '../../components/PasswordFields';
import { absolute, relative } from '../../lib/format';
import { useHost } from '../../lib/host';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, CopyButton, Details, ErrorPanel, Segmented, Skeleton, Theirs, cx } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * Settings → People → Sign-in with your organization's account (D425–D434,
 * docs/accounts.md): which provider and app people sign in with, who may join
 * by themselves, whether passwords and Tailscale accounts still work, this
 * computer's app secret (sent once, never shown again), the addresses to
 * register with the provider, the last test sign-in, and the switch, which
 * only follows a test sign-in that passed here. Everyone signed in reads it;
 * owners change it, at this computer only. Every step is an op the CLI has
 * too (`metarush auth sso status|set|secret|test`, `metarush auth method sso`).
 */

type Provider = 'google' | 'microsoft' | 'oidc';

const PROVIDERS: { value: Provider; label: string }[] = [
  { value: 'google', label: 'Google' },
  { value: 'microsoft', label: 'Microsoft' },
  { value: 'oidc', label: 'Another provider' },
];

/** Where each provider keeps the addresses to register. */
const WHERE_TO_REGISTER: Record<Provider, string> = {
  google: 'In Google Cloud: APIs & Services → Credentials → your OAuth client (a web application) → Authorized redirect URIs.',
  microsoft: 'In Microsoft Entra: App registrations → your app → Authentication → Web → Redirect URIs.',
  oidc: "In your provider's console: the app's allowed redirect (callback) addresses.",
};

function errorWords(e: unknown): string {
  const err = toApiError(e);
  return [err.message, err.hint].filter(Boolean).join(' ');
}

function joinWords(s: SSOSetup): string {
  if (!s.domain) return 'Only people you invite.';
  switch (s.provider) {
    case 'google':
      return `People of the Google Workspace domain ${s.domain} join by themselves; anyone else needs an invite.`;
    case 'microsoft':
      return 'People of your Microsoft directory join by themselves; anyone else needs an invite.';
    default:
      return `People whose checked email is at ${s.domain} join by themselves; anyone else needs an invite.`;
  }
}

function Row({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="grid grid-cols-1 gap-0.5 py-1.5 sm:grid-cols-[200px_1fr] sm:gap-3">
      <dt className="text-[12.5px] text-subtle">{label}</dt>
      <dd className="min-w-0 text-[13px] text-fg">{children}</dd>
    </div>
  );
}

export function SignInSettings() {
  const st = useOp('auth.sso.status', {}, { staleTime: 10_000 });
  return (
    <section id="sign-in" className="panel scroll-mt-28 p-5" aria-labelledby="sign-in-h" data-sso-section>
      <h2 id="sign-in-h" className="mb-1 flex flex-wrap items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Buildings size={17} />
        </span>
        Sign-in with your organization&rsquo;s account
        {st.data ? (
          <Badge className="ml-auto" tone={st.data.inUse ? 'ok' : st.data.next || st.data.live ? 'accent' : 'neutral'}>
            {st.data.inUse ? 'In use' : st.data.next || st.data.live ? 'Set up, not in use' : 'Not set up'}
          </Badge>
        ) : null}
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        People sign in with the account they already have at Google, Microsoft or your organization&rsquo;s own provider. Only people you invite, or people of a domain you
        choose, get in; nobody else does just because they have an account there.
      </p>
      {st.isLoading ? <Skeleton className="h-32" /> : st.error ? <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} /> : st.data ? <SignInBody st={st.data} /> : null}
    </section>
  );
}

function SignInBody({ st }: { st: SSOStatusOut }) {
  const own = useOwnership();
  const host = useHost();
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [testing, setTesting] = useState<number | null>(null);
  const [busy, setBusy] = useState('');
  const [testLink, setTestLink] = useState<string | null>(null);
  const refresh = () => {
    for (const k of ['auth.sso.status', 'auth.status', 'users.list', 'auth.state', 'session']) void qc.invalidateQueries({ queryKey: [k] });
  };
  const setup = st.next ?? st.live;
  const canChange = own.owner && st.here;

  // While a test sign-in is going on in another tab, look for its result.
  const since = useRef(0);
  useEffect(() => {
    if (testing === null) return;
    since.current = testing;
    const t = setInterval(() => void qc.invalidateQueries({ queryKey: ['auth.sso.status'] }), 2000);
    const stop = setTimeout(() => setTesting(null), 12 * 60_000);
    return () => {
      clearInterval(t);
      clearTimeout(stop);
    };
  }, [testing, qc]);
  useEffect(() => {
    if (testing !== null && st.test && new Date(st.test.at).getTime() >= since.current - 1000) {
      setTesting(null);
      setTestLink(null);
      toast(st.test.ok ? { tone: 'success', title: 'The test sign-in worked', body: st.canSwitch ? 'You can switch to it now.' : undefined } : { tone: 'error', title: "The test sign-in didn't work", body: st.test.message });
    }
  }, [st.test, testing, toast, st.canSwitch]);

  const openSetup = () =>
    void dialog.open<boolean>((close) => <SetupForm st={st} close={close} onSaved={refresh} />, {
      title: setup ? 'Change the sign-in with your organization’s account' : 'Set up signing in with your organization’s account',
    });

  const test = async () => {
    // A tab of its own, opened in the click (so the browser allows it); the
    // address follows once MetaRush has made the test sign-in.
    const tab = window.open('about:blank', '_blank');
    setBusy('test');
    try {
      const out = await call('auth.sso.test', {});
      setTesting(Date.now());
      if (tab) {
        tab.opener = null;
        tab.location.replace(out.url);
      } else {
        setTestLink(out.url);
      }
    } catch (err) {
      tab?.close();
      toast({ tone: 'error', title: "Couldn't start the test sign-in", body: errorWords(err) });
    } finally {
      setBusy('');
    }
  };

  const switchTo = async () => {
    if (!setup) return;
    const ok = await dialog.confirm({
      title: `Sign in with ${setup.title} from now on?`,
      body: [
        `Everyone signs in with their ${setup.title} account. ${joinWords(setup)}`,
        setup.alsoPassword ? 'Passwords keep working too.' : 'Passwords stop working: people who used one sign in with their account instead.',
        setup.alsoTailscale ? 'Tailscale accounts keep signing in too.' : '',
        `You keep getting in with the account you tested with. If ${setup.title} ever stops working, a one-time link made on this computer still opens MetaRush.`,
      ]
        .filter(Boolean)
        .join(' '),
      confirmLabel: `Switch to ${setup.title}`,
    });
    if (!ok) return;
    setBusy('switch');
    try {
      const out = await call('auth.method.set', { method: 'sso' });
      toast({ tone: 'success', title: `People sign in with ${setup.title}`, body: out.summary });
      refresh();
    } catch (err) {
      toast({ tone: 'error', title: "Couldn't switch", body: errorWords(err) });
    } finally {
      setBusy('');
    }
  };

  const t = st.test;
  const provider = (setup?.provider ?? 'google') as Provider;
  return (
    <div className="space-y-4">
      <p className="text-[13px] leading-relaxed text-fg" data-sso-summary>
        {st.summary}
      </p>
      {setup ? (
        <dl className="divide-y divide-line rounded-[12px] px-3 hairline" data-sso-setup data-sso-provider={setup.provider}>
          <Row label="Provider">
            <span className="font-medium">{setup.title}</span>
            {st.next && st.live && st.inUse ? <span className="text-subtle"> (waiting for its test; {st.live.title} is in use)</span> : null}
          </Row>
          <Row label="Who may join by themselves">{joinWords(setup)}</Row>
          <Row label="Passwords">{setup.alsoPassword ? 'Still work beside it' : 'Off while it is in use'}</Row>
          <Row label="Tailscale accounts">{setup.alsoTailscale ? 'Still sign people in beside it' : 'Off while it is in use'}</Row>
          <Row label="This computer’s app secret">
            {st.secret.set ? <span className="tc">{st.secret.masked}</span> : <span className="text-accent-text">Not on this computer yet</span>}
          </Row>
          <Row label="Last test sign-in here">
            {t ? (
              <span className="inline-flex flex-wrap items-center gap-1.5" data-sso-test={t.ok ? 'ok' : 'failed'}>
                {t.ok ? <CheckCircle size={14} className="text-ok" /> : <Warning size={14} className="text-danger" />}
                {t.ok ? 'Worked' : 'Didn’t work'}, <span title={absolute(t.at)}>{relative(t.at)}</span>
                {t.email ? (
                  <>
                    {' '}
                    as <Theirs>{t.email}</Theirs>
                  </>
                ) : null}
                {t.forSetup === 'other' ? <span className="text-subtle"> (of an earlier setup)</span> : null}
                {!t.ok && t.message ? <span className="block w-full text-[12.5px] text-muted">{t.message}</span> : null}
              </span>
            ) : (
              <span className="text-subtle">None yet</span>
            )}
          </Row>
        </dl>
      ) : null}

      <div className="rounded-[12px] p-3 hairline" data-sso-callbacks>
        <p className="text-[12.5px] font-medium text-fg">Addresses to register with {setup?.title ?? 'the provider'}</p>
        <p className="mt-0.5 text-[12px] leading-relaxed text-muted">{WHERE_TO_REGISTER[provider]}</p>
        {[st.callbacks.local, st.callbacks.public].filter(Boolean).map((u) => (
          <div key={u} className="mt-2 flex items-center gap-2 rounded-[10px] bg-panel p-1.5 pl-3 hairline">
            <input readOnly value={u} aria-label="An address to register" onFocus={(e) => e.currentTarget.select()} spellCheck={false} className="tc min-w-0 flex-1 bg-transparent text-[12.5px] text-fg outline-none" data-sso-callback />
            <CopyButton text={u!} label="Copy" />
          </div>
        ))}
        <p className="mt-2 text-[12px] leading-relaxed text-subtle">
          {st.callbacks.public
            ? 'The first is for this computer’s own browser, the second for other computers (remote access).'
            : 'For this computer’s own browser. With remote access on, the address for other computers shows here too.'}
        </p>
        {provider === 'microsoft' ? (
          <Details label="For Microsoft: finding invited people by their email" className="mt-2">
            <p className="text-[12.5px] leading-relaxed text-muted">
              Microsoft vouches for a person&rsquo;s email only when its domain belongs to their directory. In the app&rsquo;s Token configuration, add the optional
              claims <span className="tc">email</span> and <span className="tc">xms_edov</span> to the ID token: then invited people are found by their email
              when they first sign in. Without them, people join through the invite link you send, or by being in the directory you let join.
            </p>
          </Details>
        ) : null}
      </div>

      {/* One owners line per page (Remote access below has it): here, the short form, as "How people sign in" says it. */}
      <OwnerOnly>
        {!own.owner ? <p className="mb-2 text-[12px] text-subtle">Only an owner can change it.</p> : null}
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant={setup ? 'secondary' : 'primary'} disabled={!canChange} onClick={openSetup} data-sso-setup-button>
            {setup ? 'Change…' : 'Set it up…'}
          </Button>
          {setup ? (
            <Button size="sm" variant="secondary" icon={<ArrowsClockwise size={13} />} disabled={!canChange} loading={busy === 'test' || testing !== null} onClick={() => void test()} data-sso-test-button>
              {testing !== null ? 'Waiting for the test sign-in…' : 'Test sign-in'}
            </Button>
          ) : null}
          {setup && (st.canSwitch || (!st.inUse && st.next === undefined && st.live)) ? (
            <Button size="sm" variant="primary" disabled={!canChange || !st.canSwitch} loading={busy === 'switch'} onClick={() => void switchTo()} data-sso-switch>
              Switch to {setup.title}
            </Button>
          ) : null}
        </div>
        {testLink ? (
          <p className="mt-2 text-[12.5px] text-muted">
            Your browser didn’t open a new tab.{' '}
            <a href={testLink} target="_blank" rel="noopener noreferrer" className="font-medium text-accent-text hover:underline" data-sso-test-link>
              Open the test sign-in
            </a>
          </p>
        ) : null}
        {own.owner && !st.here ? (
          <p className="mt-2 text-[12px] text-subtle">
            It can be set up and tested only on <Theirs>{host.machine}</Theirs>.
          </p>
        ) : null}
      </OwnerOnly>
      <Details label="If the provider stops working">
        <p className="text-[12.5px] leading-relaxed text-muted">
          On {host.machine}, <span className="tc">metarush dashboard</span> always opens MetaRush as the owner with a one-time link, whatever the sign-in method and
          whether the provider answers. <span className="tc">metarush auth reset</span> there goes back to just you on that computer; everyone&rsquo;s work and
          accounts stay.
        </p>
      </Details>
    </div>
  );
}

function Check({ checked, onChange, children, ...data }: { checked: boolean; onChange: (v: boolean) => void; children: ReactNode } & Record<`data-${string}`, string | boolean | undefined>) {
  const id = useId();
  return (
    <label htmlFor={id} className="flex cursor-pointer items-start gap-2.5 text-[13px] leading-relaxed text-fg">
      <input id={id} type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]" {...data} />
      <span>{children}</span>
    </label>
  );
}

function SetupForm({ st, close, onSaved }: { st: SSOStatusOut; close: (v?: boolean) => void; onSaved: () => void }) {
  const toast = useToast();
  const cur = st.next ?? st.live;
  const [provider, setProvider] = useState<Provider>((cur?.provider as Provider) ?? 'google');
  const [clientId, setClientId] = useState(cur?.clientId ?? '');
  const [secret, setSecret] = useState('');
  const [issuer, setIssuer] = useState(cur?.provider === 'oidc' ? cur.issuer : '');
  const [label, setLabel] = useState(cur?.label ?? '');
  const [tenant, setTenant] = useState(cur?.tenant ?? '');
  const [domain, setDomain] = useState(cur?.provider !== 'microsoft' ? (cur?.domain ?? '') : '');
  const [joinDirectory, setJoinDirectory] = useState(cur?.provider === 'microsoft' && !!cur.domain);
  const [alsoPassword, setAlsoPassword] = useState(cur?.alsoPassword ?? false);
  const [alsoTailscale, setAlsoTailscale] = useState(cur?.alsoTailscale ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const directoryID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(tenant.trim());
  const needSecret = provider !== 'oidc' && !st.secret.set;
  const ready = clientId.trim() !== '' && (provider !== 'oidc' || issuer.trim() !== '') && (provider !== 'microsoft' || tenant.trim() !== '') && (!needSecret || secret.trim() !== '');

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ready) {
      setError(needSecret && !secret.trim() ? 'Give the app’s client secret: this computer doesn’t have it yet.' : 'Fill in what the provider gave you.');
      return;
    }
    const input: SSOSetIn = {
      provider,
      clientId: clientId.trim(),
      clientSecret: secret.trim() || undefined,
      issuer: provider === 'oidc' ? issuer.trim() : undefined,
      label: provider === 'oidc' ? label.trim() || undefined : undefined,
      tenant: provider === 'microsoft' ? tenant.trim() : undefined,
      domain: provider === 'microsoft' ? (joinDirectory && directoryID ? tenant.trim() : undefined) : domain.trim() || undefined,
      alsoPassword,
      alsoTailscale,
    };
    setBusy(true);
    setError(null);
    try {
      const out = await call('auth.sso.set', input);
      setSecret('');
      toast({ tone: 'success', title: out.next ? 'Saved: test it next' : 'Saved', body: out.summary });
      onSaved();
      close(true);
    } catch (err) {
      setError(errorWords(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="space-y-4" onSubmit={(e) => void submit(e)} noValidate data-sso-form>
      <div>
        <div className="mb-1.5 text-[12.5px] font-medium text-muted">Provider</div>
        <Segmented label="Provider" value={provider} onChange={setProvider} items={PROVIDERS} />
      </div>
      {provider === 'oidc' ? (
        <>
          <TextField label="Its name on the sign-in button" maxLength={40} value={label} onChange={setLabel} placeholder="Okta" help="People see “Sign in with …”." data-sso-label />
          <TextField
            label="The provider’s address"
            type="url"
            autoCapitalize="off"
            spellCheck={false}
            value={issuer}
            onChange={setIssuer}
            placeholder="https://login.example.com"
            help="Its issuer, as its settings page or discovery document says. MetaRush reads its settings from there."
            data-sso-issuer
          />
        </>
      ) : null}
      {provider === 'microsoft' ? (
        <TextField
          label="Directory (tenant) ID"
          autoCapitalize="off"
          spellCheck={false}
          value={tenant}
          onChange={setTenant}
          placeholder="00000000-0000-0000-0000-000000000000"
          help="From the app’s Overview page in Microsoft Entra. Use organizations only for an app any work or school account can use."
          data-sso-tenant
        />
      ) : null}
      <TextField
        label={provider === 'microsoft' ? 'Application (client) ID' : 'Client ID'}
        autoCapitalize="off"
        spellCheck={false}
        value={clientId}
        onChange={setClientId}
        help={provider === 'google' ? 'From Google Cloud: APIs & Services → Credentials → your OAuth client (a web application).' : 'As the provider shows it for the app.'}
        data-sso-client-id
      />
      <PasswordField
        label="Client secret"
        autoComplete="new-password"
        value={secret}
        onChange={setSecret}
        help={st.secret.set ? `This computer has one (${st.secret.masked}). Leave this empty to keep it.` : 'Kept on this computer only, never shown again. Each computer that runs MetaRush needs its own copy.'}
        data-sso-secret
      />
      {provider === 'microsoft' ? (
        <Check checked={joinDirectory} onChange={setJoinDirectory} data-sso-join-directory>
          People of this directory join by themselves {directoryID ? '' : <span className="text-subtle">(give the directory’s ID above)</span>}
        </Check>
      ) : (
        <TextField
          label={provider === 'google' ? 'Google Workspace domain whose people join by themselves (optional)' : 'Email domain whose people join by themselves (optional)'}
          autoCapitalize="off"
          spellCheck={false}
          value={domain}
          onChange={setDomain}
          placeholder="example.com"
          help="Leave it empty and only people you invite get in."
          data-sso-domain
        />
      )}
      <div className="space-y-2 rounded-[12px] p-3 hairline">
        <Check checked={alsoPassword} onChange={setAlsoPassword} data-sso-also-password>
          Keep email and password working too
          <span className="block text-[12px] text-subtle">People with a password can still use it. Off is stronger: a stolen password then opens nothing.</span>
        </Check>
        <Check checked={alsoTailscale} onChange={setAlsoTailscale} data-sso-also-tailscale>
          Keep Tailscale accounts signing in too
          <span className="block text-[12px] text-subtle">The Tailscale accounts you let in keep getting in by themselves on the address for other computers.</span>
        </Check>
      </div>
      <p className={cx('text-[12px] leading-relaxed', 'text-subtle')}>
        A new provider or app waits for a test sign-in on this computer before anyone uses it. Changes to who may join and to the other sign-ins apply at once.
      </p>
      {error ? (
        <p role="alert" className="rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] leading-relaxed text-danger" data-form-error>
          {error}
        </p>
      ) : null}
      <div className="flex flex-wrap justify-end gap-2 pt-2">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={busy} data-sso-save>
          Save
        </Button>
      </div>
    </form>
  );
}
