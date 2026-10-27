import { useEffect, useId, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { List, Moon, SignOut, Sun, UserCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { signOut, useAuthState } from '../api/auth';
import { useOp, useSession } from '../api/queries';
import { useTheme } from '../lib/theme';
import { SETTINGS_SECTIONS, StorageDot, useStorageBadge } from '../screens/settings/sections';
import { Avatar } from '../ui/Avatar';
import { Spinner, cx } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { useDialog } from '../ui/Dialog';
import { clearAll as clearRecorder, unsentBytes } from '../screens/record/store';

/**
 * The one menu at the right of the top bar (D184): Settings by section, light
 * or dark, and — when people sign in (email and password, or Tailscale) — who
 * is signed in, "Your account" and "Sign out". The button is the person's
 * picture (initials without one) then; with "just me on this computer" it is a
 * menu button, and there is nothing to sign out of (the one-time link opens
 * MetaRush again). The storage dot (another computer moved the media) rides on
 * the button.
 */
export function AccountMenu() {
  const session = useSession();
  const state = useAuthState();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const dialog = useDialog();
  const [theme, setTheme] = useTheme();
  const storage = useStorageBadge();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const id = useId();
  const method = state.data?.method;
  // Until the sign-in method is known, a quiet placeholder of the same size (no jump from ☰ to a picture).
  const signedIn = method === 'password' || method === 'tailscale';
  // Your account as the workspace has it (the name and email you set there; the session's may be older).
  const you = useOp('auth.status', {}, { staleTime: 60_000, enabled: signedIn }).data?.you;

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!menu.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  if (!session.data) return null;
  const name = you?.name || session.data.user.name || '';
  const email = you?.email || session.data.user.email || '';
  const avatar = you ? you.avatar : session.data.avatar;

  const close = () => {
    setOpen(false);
    btn.current?.focus();
  };
  const doSignOut = async () => {
    // Screen recordings kept in this browser are the person's: they go with the sign-out (D258).
    const unsent = await unsentBytes();
    if (unsent > 0) {
      const ok = await dialog.confirm({
        title: 'Sign out and lose a recording that wasn’t sent?',
        body: `This browser still holds ${unsent >= 1 << 20 ? `${Math.round(unsent / (1 << 20))} MB` : 'part'} of a screen recording that hasn’t reached MetaRush. Signing out deletes it from this browser. To keep it, open the recorder and send the rest first.`,
        confirmLabel: 'Sign out anyway',
        tone: 'danger',
      });
      if (!ok) return;
    }
    setBusy(true);
    try {
      await clearRecorder();
      await signOut();
      qc.clear();
      navigate('/signin', { replace: true });
    } catch (e) {
      setBusy(false);
      toast({ tone: 'error', title: "Couldn't sign out", body: e instanceof Error ? e.message : undefined });
    }
  };
  const onKey = (e: React.KeyboardEvent) => {
    const items = Array.from(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = items.length;
      items[(i + (e.key === 'ArrowDown' ? 1 : n - 1) + n) % n]?.focus();
    } else if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault();
      (e.key === 'Home' ? items[0] : items[items.length - 1])?.focus();
    } else if (e.key === 'Tab') {
      setOpen(false);
    }
  };

  const item = 'flex w-full items-center gap-2.5 px-3 py-[7px] text-left text-[13px] text-fg outline-none hover:bg-hover focus:bg-hover';
  const label = signedIn ? `Your account and settings (signed in as ${name})` : 'Settings and appearance';

  return (
    <div className="relative shrink-0">
      <button
        ref={btn}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        aria-label={label}
        title={signedIn ? `Signed in as ${name}` : 'Settings and appearance'}
        onClick={() => setOpen((v) => !v)}
        className={cx(
          'relative inline-flex h-9 items-center justify-center rounded-[10px] text-muted transition-colors hover:bg-hover hover:text-fg',
          signedIn ? 'px-1' : 'w-9 max-sm:w-8',
          open && 'bg-hover text-fg',
        )}
        data-account-menu
        data-account-button={signedIn ? '' : undefined}
        data-menu-kind={!method ? 'pending' : signedIn ? 'account' : 'menu'}
      >
        {!method ? (
          <span className="h-7 w-7 rounded-full bg-hover" aria-hidden />
        ) : signedIn ? (
          <>
            <Avatar src={avatar} name={name} size={28} marker="avatar" />
            <span className="sr-only" data-account-name data-user-content="">
              {name}
            </span>
          </>
        ) : (
          <List size={19} />
        )}
        {storage ? <StorageDot className="absolute right-0.5 top-0.5 ring-2 ring-[var(--bg)]" /> : null}
      </button>
      {open ? (
        <div
          ref={menu}
          id={id}
          role="menu"
          aria-label={signedIn ? 'Your account and settings' : 'Settings and appearance'}
          onKeyDown={onKey}
          className="fade-up absolute right-0 top-full z-50 mt-1.5 max-h-[calc(100dvh-var(--mr-header-h)-16px)] w-[min(280px,calc(100vw-16px))] overflow-y-auto overscroll-contain rounded-[12px] bg-raised py-1 shadow-[var(--shadow-float)] hairline max-sm:-right-2"
          data-account-menu-list
        >
          {signedIn ? (
            <>
              <div className="flex items-center gap-2.5 px-3 pb-2 pt-1.5">
                <Avatar src={avatar} name={name} size={36} />
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-medium text-fg" data-user-content="" data-menu-name>
                    {name}
                  </div>
                  {email ? (
                    <div className="truncate text-[12px] text-subtle" data-user-content="" data-menu-email>
                      {email}
                    </div>
                  ) : null}
                </div>
              </div>
              <Link to="/settings/people#your-account" role="menuitem" tabIndex={-1} onClick={() => setOpen(false)} className={item} data-menu-your-account>
                <UserCircle size={15} className="text-subtle" aria-hidden />
                Your account
              </Link>
              <div role="separator" className="my-1 h-px bg-line" />
            </>
          ) : null}
          <div className="px-3 pb-1 pt-1.5 text-[11.5px] font-medium text-subtle" aria-hidden>
            Settings
          </div>
          <div role="group" aria-label="Settings">
            {SETTINGS_SECTIONS.map((s) => (
              <Link
                key={s.id}
                to={`/settings/${s.id}`}
                role="menuitem"
                tabIndex={-1}
                onClick={() => setOpen(false)}
                title={s.line}
                className={item}
                {...{ [`data-menu-settings-${s.id}`]: '' }}
              >
                <span className="text-subtle" aria-hidden>
                  {s.icon(15)}
                </span>
                <span className="min-w-0 flex-1 truncate">{s.label}</span>
                {s.id === 'storage' && storage ? <StorageDot /> : null}
              </Link>
            ))}
          </div>
          <div role="separator" className="my-1 h-px bg-line" />
          <button type="button" role="menuitem" tabIndex={-1} onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} className={item} data-menu-theme={theme === 'dark' ? 'light' : 'dark'}>
            {theme === 'dark' ? <Sun size={15} className="text-subtle" aria-hidden /> : <Moon size={15} className="text-subtle" aria-hidden />}
            {theme === 'dark' ? 'Light mode' : 'Dark mode'}
          </button>
          {signedIn ? (
            <button type="button" role="menuitem" tabIndex={-1} disabled={busy} onClick={() => void doSignOut()} className={item} data-signout>
              {busy ? <Spinner size={13} /> : <SignOut size={15} className="text-subtle" aria-hidden />}
              Sign out
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
