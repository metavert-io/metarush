import { useEffect, useId, useState, type FormEvent, type ReactNode } from 'react';
import { ArrowsClockwise, Crown, Key, LinkSimple, Prohibit, SignOut, Trash, User, UserMinus, UserPlus, UsersThree } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { METHOD_LABEL, reasonOf } from '../../api/auth';
import { call, toApiError } from '../../api/client';
import { useOp, useSession } from '../../api/queries';
import type { Account, AuthMethod, AuthMethodIn, AuthStatusOut, UserLinkOut, UserOut } from '../../api/types';
import { NewPasswordFields, PasswordField, TextField, inputClass, newPasswordReady } from '../../components/PasswordFields';
import { MenuButton, type MenuItem } from '../../layout/assistant/Menu';
import { absolute, plural, relative } from '../../lib/format';
import { AvatarControl } from '../../components/AvatarPicker';
import { Avatar } from '../../ui/Avatar';
import { useHost } from '../../lib/host';
import { useDialog, type DialogApi } from '../../ui/Dialog';
import { Badge, Button, CopyButton, ErrorPanel, Segmented, Skeleton, Theirs, cx, type Tone } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * Settings → People → Users (D183, docs/accounts.md): how people sign in (a
 * guided step, owners only and only on the computer that runs MetaRush), your
 * own account and picture (D184), and the workspace's people with what owners can do for each:
 * invite, make a reset link, turn off, remove, make owner or member. Links are
 * shown once, to copy and send however the owner likes; nothing is emailed.
 * Every step is an op the CLI has too (`metarush auth`, `users`, `account`).
 */

const METHOD_BLURB: Record<AuthMethod, string> = {
  single: 'Only you, on this computer, with its one-time link. Nobody else can sign in.',
  password: 'Everyone you invite signs in with their email and a password, on this computer or another (another needs the https:// address).',
  tailscale: 'People sign in with their Tailscale account on the address for other computers. Good when everyone is on your Tailscale network already.',
};

/** A plain error line: the server's message, then its hint. */
function errorWords(e: unknown): string {
  const err = toApiError(e);
  return [err.message, err.hint].filter(Boolean).join(' ');
}

function FormError({ text }: { text: string | null }) {
  if (!text) return null;
  return (
    <p role="alert" className="rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] leading-relaxed text-danger" data-form-error>
      {text}
    </p>
  );
}

function whoOf(p: Account): string {
  return p.name || p.email || 'this person';
}

/** A person's state, as people say it. */
function stateWords(p: Account): string {
  switch (p.state) {
    case 'active':
      return 'can sign in';
    case 'invited':
      return p.inviteUntil ? `invited, until ${new Date(p.inviteUntil).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}` : 'invited';
    case 'disabled':
      return 'turned off';
    default:
      return 'earlier history, no account yet';
  }
}

const STATE_TONE: Record<Account['state'], Tone> = { active: 'ok', invited: 'accent', disabled: 'danger', earlier: 'neutral' };
const STATE_ORDER: Record<Account['state'], number> = { active: 0, invited: 1, disabled: 2, earlier: 3 };

export function UsersSettings() {
  // Others change things too (an invite accepted, a password reset): keep the list current.
  const st = useOp('auth.status', {}, { staleTime: 15_000, refetchInterval: 30_000 });
  const list = useOp('users.list', {}, { staleTime: 10_000, refetchInterval: 20_000 });
  const qc = useQueryClient();
  const refresh = () => {
    for (const k of ['auth.status', 'users.list', 'auth.state', 'session']) void qc.invalidateQueries({ queryKey: [k] });
  };
  const status = st.data;
  return (
    <section id="users" className="panel scroll-mt-28 p-5" aria-labelledby="users-h" data-users-section>
      <h2 id="users-h" className="mb-1 flex flex-wrap items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <UsersThree size={17} />
        </span>
        Users
        {status ? (
          <Badge className="ml-auto" tone={status.method === 'single' ? 'neutral' : 'ok'}>
            {METHOD_LABEL[status.method]}
          </Badge>
        ) : null}
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        Who can open this MetaRush and how they sign in. Owners invite people and look after their accounts; everyone does the rest, approving included.
      </p>
      {st.isLoading ? (
        <Skeleton className="h-40" />
      ) : st.error ? (
        <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} />
      ) : status ? (
        <div className="space-y-5">
          <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-2">
            <MethodCard status={status} onChanged={refresh} />
            <YourAccount status={status} onChanged={refresh} />
          </div>
          <People status={status} people={list.data?.people} loading={list.isLoading} error={list.error} onRetry={() => void list.refetch()} onChanged={refresh} />
        </div>
      ) : null}
    </section>
  );
}

function Box({ children, className, ...rest }: { children: ReactNode; className?: string; id?: string } & Record<`data-${string}`, string | boolean | undefined>) {
  return (
    <div className={cx('rounded-[14px] p-4 hairline', className)} {...rest}>
      {children}
    </div>
  );
}

function BoxTitle({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <h3 className="flex items-center gap-2 text-[13.5px] font-semibold text-fg">
      <span className="text-subtle">{icon}</span>
      {children}
    </h3>
  );
}

/* ---------- How people sign in ---------- */

function MethodCard({ status, onChanged }: { status: AuthStatusOut; onChanged: () => void }) {
  const host = useHost();
  const dialog = useDialog();
  const canChange = status.owner && status.here;
  const open = () => void dialog.open<boolean>((close) => <MethodWizard status={status} close={close} onChanged={onChanged} />, { title: 'Change how people sign in' });
  return (
    <Box data-method-card data-method={status.method}>
      <BoxTitle icon={<Key size={15} />}>How people sign in</BoxTitle>
      <p className="mt-2 text-[13.5px] font-medium text-fg">{METHOD_LABEL[status.method]}</p>
      <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted" data-method-summary>
        {status.summary}
      </p>
      {status.method !== 'single' && !status.publicUrl ? (
        <p className="mt-2 text-[12px] leading-relaxed text-subtle">
          People on other computers also need remote access to reach MetaRush (see Remote access). On this computer, sign-in works already.
        </p>
      ) : null}
      {status.method === 'password' && status.publicUrl && !status.publicHttps ? (
        <p className="mt-2 rounded-[10px] bg-accent-soft px-3 py-2 text-[12px] leading-relaxed text-accent-text">
          The address for other computers isn't private (https), so nobody can type a password there yet.
        </p>
      ) : null}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" disabled={!canChange} onClick={open} data-method-change>
          Change how people sign in
        </Button>
      </div>
      {!status.owner ? (
        <p className="mt-2 text-[12px] text-subtle">Only an owner can change it.</p>
      ) : !status.here ? (
        <p className="mt-2 text-[12px] text-subtle">
          It can be changed only on <Theirs>{host.machine}</Theirs>.
        </p>
      ) : null}
    </Box>
  );
}

function MethodWizard({ status, close, onChanged }: { status: AuthStatusOut; close: (v?: boolean) => void; onChanged: () => void }) {
  const toast = useToast();
  const you = status.you;
  const hasPassword = !!you?.hasPassword;
  const [step, setStep] = useState<'choose' | 'details'>('choose');
  const [choice, setChoice] = useState<AuthMethod>(status.method === 'single' ? 'password' : status.method);
  const [email, setEmail] = useState(you?.email ?? '');
  const [password, setPassword] = useState('');
  const [again, setAgain] = useState('');
  const [current, setCurrent] = useState('');
  const [login, setLogin] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pwError, setPwError] = useState<string | null>(null);
  const [currentError, setCurrentError] = useState<string | null>(null);

  const emailOk = /^[^\s@]+@[^\s@]+$/.test(email.trim());
  const ready =
    choice === 'single'
      ? true
      : choice === 'tailscale'
        ? /^[^\s@]+@[^\s@]+$/.test(login.trim())
        : emailOk && (hasPassword ? !!current && (password === '' ? again === '' : newPasswordReady(password, again)) : newPasswordReady(password, again));

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!ready) {
      setError(
        choice === 'tailscale'
          ? 'Give your Tailscale login: it looks like an email address.'
          : !emailOk
            ? 'Give your email: it is how you sign in.'
            : hasPassword && !current
              ? 'Type your current password.'
              : 'Choose a password of 12 characters or more and type it twice.',
      );
      return;
    }
    const input: AuthMethodIn =
      choice === 'password'
        ? { method: 'password', email: email.trim(), password: password || undefined, currentPassword: hasPassword ? current : undefined }
        : choice === 'tailscale'
          ? { method: 'tailscale', tailscaleLogin: login.trim() }
          : { method: 'single' };
    setBusy(true);
    setError(null);
    setPwError(null);
    setCurrentError(null);
    try {
      const out = await call('auth.method.set', input);
      toast({ tone: 'success', title: METHOD_LABEL[out.method], body: out.summary });
      onChanged();
      close(true);
    } catch (err) {
      const why = reasonOf(err);
      if (why === 'weak_password') setPwError(toApiError(err).message);
      else if (why === 'bad_current') setCurrentError(toApiError(err).message);
      else setError(errorWords(err));
    } finally {
      setBusy(false);
    }
  };

  if (step === 'choose') {
    return (
      <div data-method-wizard data-step="choose">
        <p className="text-[13px] leading-relaxed text-muted">Choose who can open this MetaRush. You give your own sign-in at the next step, so you keep getting in.</p>
        <div role="radiogroup" aria-label="How people sign in" className="mt-4 space-y-2">
          {(['single', 'password', 'tailscale'] as const).map((m) => {
            const on = choice === m;
            return (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => setChoice(m)}
                data-method-option={m}
                className={cx(
                  'block w-full rounded-[12px] px-3.5 py-3 text-left transition-colors hairline',
                  on ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover',
                )}
              >
                <span className="flex items-center gap-2 text-[13.5px] font-medium text-fg">
                  <span className={cx('grid h-4 w-4 shrink-0 place-items-center rounded-full border', on ? 'border-accent bg-accent' : 'border-line-strong')} aria-hidden>
                    {on ? <span className="h-1.5 w-1.5 rounded-full bg-accent-ink" /> : null}
                  </span>
                  {METHOD_LABEL[m]}
                  {status.method === m ? <Badge tone="neutral">now</Badge> : null}
                </span>
                <span className="mt-1 block pl-6 text-[12.5px] leading-relaxed text-muted">{METHOD_BLURB[m]}</span>
              </button>
            );
          })}
        </div>
        <div className="mt-6 flex flex-wrap justify-end gap-2">
          <Button variant="ghost" onClick={() => close()}>
            Cancel
          </Button>
          <Button variant="primary" disabled={choice === 'single' && status.method === 'single'} onClick={() => setStep('details')} data-method-next>
            Next
          </Button>
        </div>
      </div>
    );
  }

  return (
    <form data-method-wizard data-step="details" data-choice={choice} onSubmit={(e) => void submit(e)} noValidate className="space-y-4">
      <p className="text-[13px] font-medium text-fg">{METHOD_LABEL[choice]}</p>
      {choice === 'password' ? (
        <>
          <TextField label="Your email" type="email" name="email" autoComplete="username" inputMode="email" autoCapitalize="off" spellCheck={false} value={email} onChange={setEmail} help="You sign in with it, on this computer and others." data-method-email />
          {hasPassword ? (
            <PasswordField label="Your current password" autoComplete="current-password" value={current} onChange={(v) => { setCurrent(v); setCurrentError(null); }} error={currentError || undefined} data-method-current />
          ) : null}
          <NewPasswordFields
            label={hasPassword ? 'New password' : 'Your password'}
            optional={hasPassword}
            password={password}
            again={again}
            onPassword={(v) => {
              setPassword(v);
              setPwError(null);
            }}
            onAgain={setAgain}
            serverError={pwError}
          />
        </>
      ) : choice === 'tailscale' ? (
        <TextField
          label="Your Tailscale login"
          type="email"
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
          placeholder="name@example.com"
          value={login}
          onChange={setLogin}
          help="The account you use in Tailscale. On the address for other computers, it signs you in by itself."
          data-method-login
        />
      ) : (
        <p className="text-[13px] leading-relaxed text-muted" data-method-single>
          Only you, on this computer, can open MetaRush again, with its one-time link. Everyone else is signed out; their history stays, and you can invite them again later.
        </p>
      )}
      <FormError text={error} />
      <div className="flex flex-wrap justify-end gap-2 pt-2">
        <Button variant="ghost" onClick={() => setStep('choose')}>
          Back
        </Button>
        <Button type="submit" variant={choice === 'single' ? 'danger' : 'primary'} loading={busy} data-method-save>
          {choice === 'single' ? 'Go back to just me' : 'Save'}
        </Button>
      </div>
    </form>
  );
}

/* ---------- Your account ---------- */

function YourAccount({ status, onChanged }: { status: AuthStatusOut; onChanged: () => void }) {
  const you = status.you;
  const session = useSession();
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const [name, setName] = useState(you?.name ?? '');
  const [email, setEmail] = useState(you?.email ?? '');
  const [current, setCurrent] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    setName(you?.name ?? '');
    setEmail(you?.email ?? '');
  }, [you?.name, you?.email]);
  if (!you) {
    return (
      <Box id="your-account" className="scroll-mt-28" data-your-account>
        <BoxTitle icon={<User size={15} />}>Your account</BoxTitle>
        {status.method === 'single' && session.data ? <AvatarControl name={session.data.user.name} avatar={session.data.avatar} /> : null}
        <p className="mt-2 text-[12.5px] leading-relaxed text-muted">
          {status.method === 'single'
            ? 'You open MetaRush on this computer with its one-time link. Your account, with your email and a password if you like, is made when you choose how people sign in.'
            : "MetaRush doesn't have an account for this browser's sign-in."}
        </p>
      </Box>
    );
  }
  const emailChanged = email.trim().toLowerCase() !== (you.email ?? '').toLowerCase();
  const nameChanged = name.trim() !== you.name;
  const needCurrent = emailChanged && you.hasPassword;
  const dirty = (nameChanged && name.trim() !== '') || (emailChanged && email.trim() !== '');

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!dirty) return;
    if (needCurrent && !current) {
      setError('Changing your email asks for your current password.');
      return;
    }
    setBusy('save');
    setError(null);
    try {
      const out = await call('account.update', {
        name: nameChanged ? name.trim() : undefined,
        email: emailChanged ? email.trim() : undefined,
        currentPassword: needCurrent ? current : undefined,
      });
      setCurrent('');
      toast({ tone: 'success', title: out.summary });
      onChanged();
    } catch (err) {
      setError(errorWords(err));
    } finally {
      setBusy('');
    }
  };
  const changePassword = () => void dialog.open<UserOut>((close) => <ChangePassword hasPassword={you.hasPassword} close={close} />, { title: 'Change your password' });
  const signOutEverywhere = async () => {
    const ok = await dialog.confirm({
      title: 'Sign out on every computer and browser?',
      body:
        status.method === 'password'
          ? 'This browser too: you sign in again with your email and password. Use it if you lost a computer or signed in on a shared one.'
          : status.method === 'tailscale'
            ? 'This browser too. On the address for other computers your Tailscale account signs you in again; on this computer, open MetaRush with its one-time link.'
            : 'This browser too: open MetaRush again from this computer with its one-time link.',
      confirmLabel: 'Sign out everywhere',
      tone: 'danger',
    });
    if (!ok) return;
    setBusy('signout');
    try {
      const out = await call('account.signoutEverywhere', {});
      toast({ tone: 'success', title: out.summary });
      // This browser's sign-in ended too: the next answer says so and the sign-in page opens.
      void qc.invalidateQueries({ queryKey: ['session'] });
    } catch (err) {
      toast({ tone: 'error', title: "Couldn't sign you out everywhere", body: errorWords(err) });
    } finally {
      setBusy('');
    }
  };

  return (
    <Box id="your-account" className="scroll-mt-28" data-your-account>
      <BoxTitle icon={<User size={15} />}>Your account</BoxTitle>
      <p className="mt-1 text-[12px] text-subtle">{you.role === 'owner' ? 'You are an owner.' : 'You are a member.'}</p>
      <AvatarControl name={you.name || you.email || 'You'} avatar={you.avatar} />
      <form className="mt-3 space-y-3" onSubmit={(e) => void save(e)} noValidate>
        <TextField label="Name" autoComplete="name" maxLength={80} value={name} onChange={setName} help="How others see you in MetaRush." data-account-name-field />
        <TextField label="Email" type="email" autoComplete="email" autoCapitalize="off" spellCheck={false} value={email} onChange={setEmail} help={status.method === 'password' ? 'You sign in with it.' : undefined} data-account-email-field />
        {needCurrent ? <PasswordField label="Your current password" autoComplete="current-password" value={current} onChange={setCurrent} help="Changing your email asks for it." /> : null}
        <FormError text={error} />
        <Button type="submit" size="sm" variant="secondary" disabled={!dirty} loading={busy === 'save'} data-account-save>
          Save
        </Button>
      </form>
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-3">
        {status.method === 'password' || you.hasPassword ? (
          <Button size="sm" variant="secondary" icon={<Key size={13} />} onClick={changePassword} data-change-password>
            Change password
          </Button>
        ) : null}
        <Button size="sm" variant="ghost" icon={<SignOut size={13} />} loading={busy === 'signout'} onClick={() => void signOutEverywhere()} data-signout-everywhere>
          Sign out everywhere
        </Button>
      </div>
    </Box>
  );
}

function ChangePassword({ hasPassword, close }: { hasPassword: boolean; close: (v?: UserOut) => void }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [current, setCurrent] = useState('');
  const [password, setPassword] = useState('');
  const [again, setAgain] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pwError, setPwError] = useState<string | null>(null);
  const [currentError, setCurrentError] = useState<string | null>(null);
  const ready = newPasswordReady(password, again) && (!hasPassword || !!current);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ready) {
      setError(hasPassword && !current ? 'Type your current password.' : 'Choose a password of 12 characters or more and type it twice.');
      return;
    }
    setBusy(true);
    setError(null);
    setPwError(null);
    setCurrentError(null);
    try {
      const out = await call('account.password', { current: hasPassword ? current : undefined, new: password });
      toast({ tone: 'success', title: 'Your password is changed', body: out.summary });
      close(out);
      // If the change signed this browser out too, the sign-in page opens now rather than at a later click.
      void qc.invalidateQueries({ queryKey: ['session'] });
    } catch (err) {
      const why = reasonOf(err);
      if (why === 'weak_password') setPwError(toApiError(err).message);
      else if (why === 'bad_current') setCurrentError(toApiError(err).message);
      else setError(errorWords(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="space-y-4" onSubmit={(e) => void submit(e)} noValidate data-change-password-dialog>
      {hasPassword ? (
        <PasswordField label="Your current password" autoComplete="current-password" value={current} onChange={(v) => { setCurrent(v); setCurrentError(null); }} error={currentError || undefined} autoFocus />
      ) : null}
      <NewPasswordFields
        label="New password"
        password={password}
        again={again}
        onPassword={(v) => {
          setPassword(v);
          setPwError(null);
        }}
        onAgain={setAgain}
        serverError={pwError}
      />
      <FormError text={error} />
      <div className="flex flex-wrap justify-end gap-2 pt-2">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={busy}>
          Change password
        </Button>
      </div>
    </form>
  );
}

/* ---------- People ---------- */

function People({ status, people, loading, error, onRetry, onChanged }: {
  status: AuthStatusOut;
  people?: Account[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  onChanged: () => void;
}) {
  const dialog = useDialog();
  const toast = useToast();
  const [busy, setBusy] = useState('');
  const sorted = [...(people ?? [])].sort((a, b) => Number(b.you) - Number(a.you) || STATE_ORDER[a.state] - STATE_ORDER[b.state] || whoOf(a).localeCompare(whoOf(b)));
  const earlier = sorted.filter((p) => p.state === 'earlier');
  const activeOwners = sorted.filter((p) => p.role === 'owner' && p.state === 'active');
  const canInvite = status.owner && status.method !== 'single';
  const shown = sorted.filter((p) => p.state !== 'earlier').length;

  const invite = async (preset?: Account) => {
    const out = await dialog.open<UserLinkOut>((close) => <InviteForm earlier={earlier} preset={preset} close={close} />, { title: preset ? `Invite ${whoOf(preset)}` : 'Invite a person' });
    if (!out) return;
    onChanged();
    void showLink(dialog, out);
  };

  const act = async (p: Account, key: string, run: () => Promise<UserOut | UserLinkOut>, failed: string) => {
    setBusy(p.id + key);
    try {
      const out = await run();
      onChanged();
      if ('link' in out) void showLink(dialog, out);
      else toast({ tone: 'success', title: out.summary });
    } catch (err) {
      toast({ tone: 'error', title: failed, body: errorWords(err) });
    } finally {
      setBusy('');
    }
  };

  const actions = (p: Account): (MenuItem | 'separator')[] => {
    if (!status.owner) return [];
    const who = whoOf(p);
    const items: (MenuItem | 'separator')[] = [];
    if (p.state === 'earlier') {
      items.push({
        label: 'Invite them',
        icon: <UserPlus size={14} />,
        disabled: !canInvite,
        hint: canInvite ? 'What they did becomes theirs again' : 'Choose how people sign in first',
        onSelect: () => void invite(p),
      });
      return items;
    }
    if (p.state === 'invited') {
      items.push({ label: 'Make a new invite link', icon: <LinkSimple size={14} />, onSelect: () => void act(p, 'link', () => call('users.resetLink', { user: p.id }), "Couldn't make the link") });
      items.push({
        label: 'Take back the invite',
        icon: <Prohibit size={14} />,
        onSelect: async () => {
          const ok = await dialog.confirm({ title: `Take back the invite for ${who}?`, body: 'Their link stops working. You can invite them again later.', confirmLabel: 'Take it back', tone: 'danger' });
          if (ok) void act(p, 'revoke', () => call('users.revoke', { user: p.id }), "Couldn't take it back");
        },
      });
    }
    if (p.state === 'active' && !p.you && status.method === 'password') {
      items.push({ label: 'Make a reset link', icon: <ArrowsClockwise size={14} />, hint: 'They choose a new password with it', onSelect: () => void act(p, 'link', () => call('users.resetLink', { user: p.id }), "Couldn't make the link") });
    }
    if (p.state === 'disabled') {
      items.push({ label: 'Turn back on', icon: <ArrowsClockwise size={14} />, onSelect: () => void act(p, 'enable', () => call('users.enable', { user: p.id }), "Couldn't turn it back on") });
    }
    // Never offer what would leave the workspace without an active owner (the server refuses it too).
    const otherOwners = activeOwners.filter((o) => o.id !== p.id).length;
    if (p.role === 'owner') {
      if (!p.you || otherOwners > 0) {
        items.push({
          label: 'Make member',
          icon: <UserMinus size={14} />,
          onSelect: async () => {
            const ok = await dialog.confirm({
              title: p.you ? 'Make yourself a member?' : `Make ${who} a member?`,
              body: p.you ? "You won't be able to invite people or change accounts any more; another owner can make you an owner again." : 'They keep doing everything else, approving included, but no longer manage people or the sign-in method.',
              confirmLabel: 'Make member',
            });
            if (ok) void act(p, 'role', () => call('users.role', { user: p.id, role: 'member' }), "Couldn't change it");
          },
        });
      }
    } else {
      items.push({
        label: 'Make owner',
        icon: <Crown size={14} />,
        onSelect: async () => {
          const ok = await dialog.confirm({
            title: `Make ${who} an owner?`,
            body: 'Owners invite people, change their accounts and the sign-in method, and look after remote access, storage and keys.',
            confirmLabel: 'Make owner',
          });
          if (ok) void act(p, 'role', () => call('users.role', { user: p.id, role: 'owner' }), "Couldn't change it");
        },
      });
    }
    if (!p.you && (p.state === 'active' || p.state === 'disabled')) {
      items.push('separator');
      if (p.state === 'active') {
        items.push({
          label: 'Turn off',
          icon: <Prohibit size={14} />,
          danger: true,
          onSelect: async () => {
            const ok = await dialog.confirm({
              title: `Turn off ${who}'s account?`,
              body: "They are signed out everywhere and can't sign in until you turn it back on. Everything they did stays.",
              confirmLabel: 'Turn off',
              tone: 'danger',
            });
            if (ok) void act(p, 'disable', () => call('users.disable', { user: p.id }), "Couldn't turn it off");
          },
        });
      }
      items.push({
        label: 'Remove account',
        icon: <Trash size={14} />,
        danger: true,
        onSelect: async () => {
          const ok = await dialog.confirm({
            title: `Remove ${who}'s account?`,
            body: "They can't sign in any more. Their name stays on what they did, and if you invite them again later it becomes theirs again.",
            confirmLabel: 'Remove account',
            tone: 'danger',
          });
          if (ok) void act(p, 'remove', () => call('users.remove', { user: p.id }), "Couldn't remove the account");
        },
      });
    }
    return items;
  };

  return (
    <div data-people>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h3 className="flex items-center gap-2 text-[13.5px] font-semibold text-fg">
          <span className="text-subtle">
            <UsersThree size={15} />
          </span>
          People
        </h3>
        {people ? <span className="text-[12px] text-subtle">{plural(shown, 'account')}</span> : null}
        {status.owner ? (
          <div className="ml-auto flex flex-wrap items-center gap-2">
            {!canInvite ? <span className="text-[12px] text-subtle">Choose how people sign in first.</span> : null}
            <Button size="sm" variant="primary" icon={<UserPlus size={13} />} disabled={!canInvite} onClick={() => void invite()} data-invite-button>
              Invite a person
            </Button>
          </div>
        ) : null}
      </div>
      {loading ? (
        <Skeleton className="h-24" />
      ) : error ? (
        <ErrorPanel compact error={error} onRetry={onRetry} />
      ) : (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="People">
          {sorted.length === 0 ? (
            <li className="px-3 py-3 text-[12.5px] text-subtle">{status.method === 'single' ? 'Just you so far. Choose how people sign in, then invite them.' : 'Nobody yet.'}</li>
          ) : null}
          {sorted.map((p) => {
            const items = actions(p);
            const meta = [p.work > 0 ? `${plural(p.work, 'thing')} done` : '', p.lastSignIn ? `last signed in ${relative(p.lastSignIn)}` : ''].filter(Boolean).join(', ');
            return (
              <li key={p.id} className="flex items-start gap-3 px-3 py-2.5" data-person={p.email || p.id} data-person-state={p.state} data-person-role={p.role}>
                <Avatar src={p.avatar} name={whoOf(p)} size={32} muted={p.state !== 'active'} className="mt-0.5" marker="person-avatar" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
                    <Theirs className="truncate text-[13px] font-medium text-fg">{whoOf(p)}</Theirs>
                    {p.you ? <span className="text-[12px] text-subtle">(you)</span> : null}
                    <Badge tone={p.role === 'owner' ? 'accent' : 'neutral'}>{p.role === 'owner' ? 'owner' : 'member'}</Badge>
                    <Badge tone={STATE_TONE[p.state]}>{stateWords(p)}</Badge>
                  </div>
                  {p.email && p.name ? (
                    <div className="truncate text-[12px] text-muted">
                      <Theirs>{p.email}</Theirs>
                    </div>
                  ) : null}
                  {meta ? <div className="truncate text-[11.5px] text-subtle">{meta}</div> : null}
                </div>
                {busy.startsWith(p.id) ? <span className="mt-1.5 text-[12px] text-subtle">Working…</span> : null}
                {items.length > 0 ? <MenuButton items={items} label={`Change ${whoOf(p)}'s account`} /> : null}
              </li>
            );
          })}
        </ul>
      )}
      {!status.owner ? <p className="mt-2 text-[12px] text-subtle">Owners invite people and change their accounts.</p> : null}
    </div>
  );
}

function InviteForm({ earlier, preset, close }: { earlier: Account[]; preset?: Account; close: (v?: UserLinkOut) => void }) {
  const [email, setEmail] = useState(preset?.email ?? '');
  const [name, setName] = useState(preset?.name ?? '');
  const [role, setRole] = useState<'member' | 'owner'>('member');
  const [linkTo, setLinkTo] = useState(preset?.id ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const historyId = useId();
  const emailOk = /^[^\s@]+@[^\s@]+$/.test(email.trim());
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!emailOk) {
      setError('Give their email: they sign in with it.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const out = await call('users.invite', { email: email.trim(), name: name.trim() || undefined, role, linkTo: linkTo || undefined });
      close(out);
    } catch (err) {
      setError(errorWords(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="space-y-4" onSubmit={(e) => void submit(e)} noValidate data-invite-dialog>
      <p className="text-[13px] leading-relaxed text-muted">MetaRush makes a link for them to choose a password. Nothing is emailed: you send the link yourself.</p>
      <TextField label="Their email" type="email" autoComplete="off" autoCapitalize="off" spellCheck={false} autoFocus value={email} onChange={setEmail} placeholder="name@example.com" data-invite-email />
      <TextField label="Their name" autoComplete="off" maxLength={80} value={name} onChange={setName} help="Optional: they can change it when they join." data-invite-name />
      <div>
        <div className="mb-1.5 text-[12.5px] font-medium text-muted">Role</div>
        <Segmented
          label="Role"
          value={role}
          onChange={setRole}
          items={[
            { value: 'member', label: 'Member' },
            { value: 'owner', label: 'Owner' },
          ]}
        />
        <p className="mt-1.5 text-[12px] text-subtle">
          {role === 'owner' ? 'Owners also invite people, change accounts and the sign-in method, and look after remote access, storage and keys.' : 'Members do everything else, approving included.'}
        </p>
      </div>
      {earlier.length > 0 ? (
        <div>
          <label htmlFor={historyId} className="mb-1.5 block text-[12.5px] font-medium text-muted">
            Continue the history of
          </label>
          <select
            id={historyId}
            value={linkTo}
            onChange={(e) => {
              const v = e.target.value;
              setLinkTo(v);
              const p = earlier.find((x) => x.id === v);
              if (p && !name.trim()) setName(p.name);
              if (p?.email && !email.trim()) setEmail(p.email);
            }}
            className={inputClass}
            data-invite-earlier
          >
            <option value="">Nobody: a new person</option>
            {earlier.map((p) => (
              <option key={p.id} value={p.id}>
                {whoOf(p)}
                {p.email && p.name ? ` (${p.email})` : ''}
                {p.work > 0 ? `, ${plural(p.work, 'thing')} done` : ''}
              </option>
            ))}
          </select>
          <p className="mt-1.5 text-[12px] text-subtle">Someone who used MetaRush before accounts existed: what they did becomes theirs again.</p>
        </div>
      ) : null}
      <FormError text={error} />
      <div className="flex flex-wrap justify-end gap-2 pt-2">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={busy} data-invite-submit>
          Make the invite link
        </Button>
      </div>
    </form>
  );
}

/** The link to send, shown once (an invite or a password reset). */
function showLink(dialog: DialogApi, out: UserLinkOut) {
  const who = whoOf(out.person);
  return dialog.open<boolean>((close) => <LinkPanel out={out} close={close} />, { title: out.kind === 'invite' ? `An invite link for ${who}` : `A reset link for ${who}` });
}

function LinkPanel({ out, close }: { out: UserLinkOut; close: (v?: boolean) => void }) {
  return (
    <div data-link-dialog data-link-kind={out.kind}>
      <p className="text-[13.5px] leading-relaxed text-muted" data-link-summary>
        {out.summary}
      </p>
      <div className="mt-4 flex items-center gap-2 rounded-[10px] bg-panel p-1.5 pl-3 hairline">
        <input
          readOnly
          value={out.link}
          aria-label="The link to send"
          onFocus={(e) => e.currentTarget.select()}
          spellCheck={false}
          className="tc min-w-0 flex-1 bg-transparent text-[12.5px] text-fg outline-none"
          data-link
        />
        <CopyButton text={out.link} label="Copy" />
      </div>
      <p className="mt-2 text-[12px] text-subtle">It works once, until {absolute(out.expiresAt)}.</p>
      <p className="mt-3 rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text">Shown only now — copy it and send it to them however you like.</p>
      <div className="mt-5 flex justify-end">
        <Button variant="primary" onClick={() => close(true)}>
          Done
        </Button>
      </div>
    </div>
  );
}
