import { useEffect, useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { Key, LinkBreak, LockKey, SignOut, UserCirclePlus } from '@phosphor-icons/react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { acceptLink, getLink, reasonOf, safeNext, signIn, signInError, signOut, useAuthState } from '../api/auth';
import { toApiError, type ApiError } from '../api/client';
import type { LinkInfo } from '../api/types';
import { absolute } from '../lib/format';
import { DaemonDownScreen, GateFrame, SignInScreen, Splash } from '../layout/Gates';
import { NewPasswordFields, PasswordField, TextField, newPasswordReady } from '../components/PasswordFields';
import { Button, CopyBlock, Details, ErrorPanel, Theirs } from '../ui/primitives';

/*
 * The sign-in page (/signin) and invite and reset links (/welcome/:token), D183.
 * Both render outside the app's shell and ask only the endpoints that work
 * without a session, so a browser that isn't signed in never loops here.
 */

/** A plain line under a form when something went wrong. */
function FormError({ text, attr }: { text: string | null; attr?: string }) {
  if (!text) return null;
  return (
    <p role="alert" className="rounded-[10px] bg-danger-soft px-3 py-2 text-[13px] leading-relaxed text-danger" {...(attr ? { [attr]: '' } : {})}>
      {text}
    </p>
  );
}

function LoadProblem({ error, onRetry }: { error: ApiError; onRetry: () => void }) {
  if (error.isUnreachable) return <DaemonDownScreen onRetry={onRetry} retrying={false} />;
  return (
    <GateFrame icon={<LockKey size={20} />} title="MetaRush couldn't open this page">
      <ErrorPanel className="mt-5" error={error} onRetry={onRetry} />
    </GateFrame>
  );
}

/** /signin: email and password, or why this MetaRush signs people in another way. */
export function SignInPage() {
  const [params] = useSearchParams();
  const next = safeNext(params.get('next'));
  const state = useAuthState();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (state.isPending) return <Splash />;
  if (state.error) return <LoadProblem error={toApiError(state.error)} onRetry={() => void state.refetch()} />;
  const s = state.data;

  if (s.signedIn) {
    return (
      <GateFrame icon={<LockKey size={20} />} title="You're signed in">
        <p className="mt-3 text-[14px] leading-relaxed text-muted">
          This browser is signed in to MetaRush on {s.machine}
          {s.name ? (
            <>
              {' '}as <Theirs className="font-medium text-fg">{s.name}</Theirs>
            </>
          ) : null}
          .
        </p>
        <div className="mt-6 flex flex-wrap items-center gap-3">
          <Button variant="primary" onClick={() => navigate(next, { replace: true })}>
            Open MetaRush
          </Button>
          {s.method !== 'single' ? (
            <Button
              variant="ghost"
              icon={<SignOut size={15} />}
              onClick={async () => {
                await signOut().catch(() => undefined);
                qc.clear();
                void state.refetch();
              }}
            >
              Sign out
            </Button>
          ) : null}
        </div>
      </GateFrame>
    );
  }

  if (s.method !== 'password') {
    return <SignInScreen way={{ method: s.method, remote: !s.here, machine: s.machine }} retrying={state.isFetching} onRetry={() => void state.refetch()} />;
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!email.trim() || !password) {
      setError('Type your email and your password.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await signIn({ email: email.trim(), password });
      setPassword('');
      qc.clear(); // nothing from an earlier sign-in carries over
      navigate(next, { replace: true });
    } catch (err) {
      const why = reasonOf(err);
      if (why === 'no_passwords' || why === 'not_https') void state.refetch();
      setError(signInError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <GateFrame icon={<LockKey size={20} />} title="Sign in to MetaRush">
      <p className="mt-2 text-[14px] text-muted">On {s.machine}, with the email and password you chose.</p>
      {!s.secure ? (
        <p className="mt-5 rounded-[10px] bg-accent-soft px-3 py-2.5 text-[13px] leading-relaxed text-accent-text" role="note" data-signin-insecure>
          Passwords are typed only over a private connection, and this one isn't. Open MetaRush at its https:// address to sign in (the owner can tell you what it is).
        </p>
      ) : (
        <form className="mt-6 space-y-4" onSubmit={(e) => void submit(e)} data-signin-form noValidate>
          <TextField label="Email" type="email" name="email" autoComplete="username" inputMode="email" autoCapitalize="off" spellCheck={false} autoFocus value={email} onChange={setEmail} />
          <PasswordField label="Password" name="password" autoComplete="current-password" value={password} onChange={setPassword} />
          <FormError text={error} attr="data-signin-error" />
          <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy}>
            Sign in
          </Button>
        </form>
      )}
      <p className="mt-5 text-[12.5px] leading-relaxed text-subtle">Forgot your password, or don't have an account yet? Ask the owner of this MetaRush for a link.</p>
      {s.here ? (
        <Details label="For the owner, on this computer" className="mt-4">
          <CopyBlock text="metarush dashboard" caption="Opens MetaRush with a one-time link, whatever the sign-in method. It works only on this computer." />
        </Details>
      ) : null}
    </GateFrame>
  );
}

/** A link that was used, ran out or was taken back. */
function LinkGone() {
  return (
    <GateFrame icon={<LinkBreak size={20} />} title="This link doesn't work any more">
      <p className="mt-3 text-[14px] leading-relaxed text-muted" data-link-gone>
        It was used already, it ran out, or the owner took it back. Ask the owner of this MetaRush for a new one.
      </p>
      <p className="mt-4 text-[13px] text-muted">
        Already have an account?{' '}
        <Link to="/signin" className="font-medium text-accent-text hover:underline">
          Sign in
        </Link>
      </p>
    </GateFrame>
  );
}

/** /welcome/:token: join with an invite (a name and a password) or choose a new password. */
export function WelcomePage() {
  const { token = '' } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const link = useQuery<LinkInfo, ApiError>({
    queryKey: ['auth.link', token],
    queryFn: ({ signal }) => getLink(token, signal),
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
  const state = useAuthState();
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [again, setAgain] = useState('');
  const [busy, setBusy] = useState(false);
  const [gone, setGone] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [passwordError, setPasswordError] = useState<string | null>(null);
  const info = link.data;
  useEffect(() => {
    if (info?.name) setName(info.name);
  }, [info?.name]);

  if (link.isPending) return <Splash />;
  if (gone || (link.error && (reasonOf(link.error) === 'link_gone' || link.error.code === 'E_NOT_FOUND'))) return <LinkGone />;
  if (link.error || !info) return <LoadProblem error={toApiError(link.error)} onRetry={() => void link.refetch()} />;

  const invite = info.kind === 'invite';
  const insecure = state.data ? !state.data.secure : false;
  const ready = newPasswordReady(password, again) && (!invite || name.trim().length > 0) && !insecure;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ready) {
      setError(invite && !name.trim() ? 'Give your name: it is how others see you in MetaRush.' : 'Choose a password of 12 characters or more and type it twice.');
      return;
    }
    setBusy(true);
    setError(null);
    setPasswordError(null);
    try {
      await acceptLink(token, invite ? { name: name.trim(), password } : { password });
      setPassword('');
      setAgain('');
      qc.clear();
      navigate('/', { replace: true });
    } catch (err) {
      const why = reasonOf(err);
      if (why === 'link_gone') setGone(true);
      else if (why === 'weak_password') setPasswordError(toApiError(err).message);
      else setError(signInError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <GateFrame wide icon={invite ? <UserCirclePlus size={20} /> : <Key size={20} />} title={invite ? `Join MetaRush on ${info.machine}` : 'Choose a new password'}>
      <p className="mt-2 text-[14px] leading-relaxed text-muted">
        {invite
          ? 'You were invited to make videos together. Choose the name others see and a password; you sign in with your email and that password from now on.'
          : 'Choose a new password for your account. Once it is saved you are signed in here, and signed out everywhere else.'}
      </p>
      <form className="mt-6 space-y-4" onSubmit={(e) => void submit(e)} data-welcome-form data-welcome-kind={info.kind} noValidate>
        <TextField label="Your email" type="email" name="email" autoComplete="username" readOnly value={info.email} help="You sign in with this email." />
        {invite ? <TextField label="Your name" name="name" autoComplete="name" maxLength={80} value={name} onChange={setName} help="How others see you in MetaRush." data-welcome-name /> : null}
        <NewPasswordFields
          label={invite ? 'Password' : 'New password'}
          password={password}
          again={again}
          onPassword={(v) => {
            setPassword(v);
            setPasswordError(null);
          }}
          onAgain={setAgain}
          serverError={passwordError}
          disabled={insecure}
        />
        {insecure ? (
          <p className="rounded-[10px] bg-accent-soft px-3 py-2.5 text-[13px] leading-relaxed text-accent-text" role="note">
            Passwords are typed only over a private connection, and this one isn't. Open this link at MetaRush's https:// address (the owner can send it again).
          </p>
        ) : null}
        <FormError text={error} attr="data-welcome-error" />
        <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} disabled={insecure}>
          {invite ? 'Join MetaRush' : 'Save the new password'}
        </Button>
      </form>
      <p className="mt-4 text-[12.5px] text-subtle">This link works once, until {absolute(info.expiresAt)}.</p>
    </GateFrame>
  );
}
