import { useEffect } from 'react';
import { ArrowClockwise, LockKey, PlugsConnected, WarningOctagon } from '@phosphor-icons/react';
import { Button, CopyBlock, Details, ErrorPanel, Spinner } from '../ui/primitives';
import { Logo } from '../ui/Logo';
import { isElsewhere } from '../lib/host';
import type { AuthMethod } from '../api/types';

export function GateFrame({ icon, title, children, wide }: { icon: React.ReactNode; title: React.ReactNode; children: React.ReactNode; wide?: boolean }) {
  return (
    <div className="grid min-h-[100dvh] place-items-center px-4 py-10">
      <div className={wide ? 'fade-up w-full max-w-[560px]' : 'fade-up w-full max-w-[520px]'}>
        <div className="mb-8 flex items-center gap-2.5">
          <Logo className="h-8 w-8" />
          <span className="text-[16px] font-semibold tracking-[-0.02em]">MetaRush</span>
        </div>
        <div className="panel p-6 sm:p-8">
          <div className="mb-5 grid h-11 w-11 place-items-center rounded-[13px] bg-accent-soft text-accent-text">{icon}</div>
          <h1 className="text-[22px] font-semibold leading-tight tracking-[-0.025em] text-fg sm:text-[26px]">{title}</h1>
          {children}
        </div>
      </div>
    </div>
  );
}

export interface SignInWay {
  /** How this MetaRush signs people in (from the "not signed in" answer or /api/auth/state). */
  method?: AuthMethod;
  /** This browser is on another computer. */
  remote?: boolean;
  /** The computer MetaRush runs on, as people call it. */
  machine?: string;
}

/**
 * Why this browser isn't signed in, and what to do, by sign-in method (D183).
 * Email and password has its own page (/signin); this covers "just me on this
 * computer" (the one-time link from this computer, or "sign-in isn't set up"
 * on another) and Tailscale accounts.
 */
export function SignInScreen({ onRetry, way, retrying }: { onRetry: () => void; way?: SignInWay; retrying?: boolean }) {
  const remote = way?.remote ?? isElsewhere();
  const machine = way?.machine || 'the computer that runs MetaRush';
  const method = way?.method;
  if (method === 'password') {
    return (
      <GateFrame icon={<LockKey size={20} />} title="Sign in to MetaRush">
        <p className="mt-3 text-[14px] leading-relaxed text-muted">People sign in to this MetaRush with their email and password.</p>
        <div className="mt-6 flex flex-wrap items-center gap-3">
          <a href="/signin" className="inline-flex h-9 items-center rounded-[10px] bg-accent px-3.5 text-[13.5px] font-medium text-accent-ink hover:bg-accent-hover">
            Sign in
          </a>
        </div>
      </GateFrame>
    );
  }
  if (remote && method === 'tailscale') {
    return (
      <GateFrame icon={<LockKey size={20} />} title="Sign in with your Tailscale account">
        <p className="mt-3 text-[14px] leading-relaxed text-muted" data-signin-explain="tailscale">
          MetaRush on {machine} lets people in by their Tailscale account. Sign in to Tailscale on this computer with the account the owner let in, then check again.
        </p>
        <p className="mt-3 text-[14px] leading-relaxed text-muted">Not let in yet? Ask the owner of this MetaRush to invite you.</p>
        <div className="mt-6 flex flex-wrap items-center gap-3">
          <Button variant="primary" loading={retrying} icon={<ArrowClockwise size={15} />} onClick={onRetry}>
            Check again
          </Button>
        </div>
      </GateFrame>
    );
  }
  if (remote) {
    // Just me on this computer, opened from another computer: nobody else gets in yet.
    return (
      <GateFrame icon={<LockKey size={20} />} title={`Sign-in isn't set up on ${machine} yet`}>
        <p className="mt-3 text-[14px] leading-relaxed text-muted" data-signin-explain="single">
          For now MetaRush on {machine} lets only its owner in, on that computer. Ask the owner to set it up in Settings → People and to invite you.
        </p>
        <div className="mt-6 flex flex-wrap items-center gap-3">
          <Button variant="primary" loading={retrying} icon={<ArrowClockwise size={15} />} onClick={onRetry}>
            Check again
          </Button>
        </div>
      </GateFrame>
    );
  }
  return (
    <GateFrame icon={<LockKey size={20} />} title="Open MetaRush from this computer">
      <p className="mt-3 text-[14px] leading-relaxed text-muted" data-signin-explain="link">
        This browser isn't signed in yet, or its sign-in ran out. To keep your work safe, the dashboard opens with a one-time link that only this computer can make.
      </p>
      <p className="mt-3 text-[14px] leading-relaxed text-muted">
        Ask Claude Code to open the MetaRush dashboard, or open it from a terminal on this computer. The link works once, within 10 minutes.
      </p>
      <Details label="The command for a terminal" className="mt-5">
        <CopyBlock text="metarush dashboard" caption="Type it in a terminal on this computer. It opens a fresh sign-in link in your browser." />
      </Details>
      <div className="mt-6 flex flex-wrap items-center gap-3">
        <Button variant="primary" loading={retrying} icon={<ArrowClockwise size={15} />} onClick={onRetry}>
          Check again
        </Button>
        <span className="text-[12.5px] text-subtle">Your passwords and keys stay with MetaRush on this computer; the browser never sees them.</span>
      </div>
    </GateFrame>
  );
}

export function DaemonDownScreen({ onRetry, retrying }: { onRetry: () => void; retrying: boolean }) {
  useEffect(() => {
    const t = setInterval(onRetry, 5000);
    return () => clearInterval(t);
  }, [onRetry]);
  return (
    <GateFrame icon={<PlugsConnected size={20} />} title="MetaRush isn't answering">
      <p className="mt-3 text-[14px] leading-relaxed text-muted">
        {isElsewhere()
          ? 'This page needs MetaRush running on the computer that runs it, and it isn’t answering. Check that MetaRush and Tailscale are running there (and Tailscale here); this page reconnects on its own.'
          : "This page needs MetaRush running on this computer, and it isn't answering. Start it again (ask Claude Code to open the MetaRush dashboard, or use a terminal), and this page reconnects on its own."}
      </p>
      <Details label="The command for a terminal" className="mt-5">
        <CopyBlock text="metarush dashboard" caption="Starts MetaRush and opens a fresh sign-in link. To start it without opening a browser: `metarush serve`." />
      </Details>
      <div className="mt-6 flex items-center gap-3">
        <Button variant="primary" loading={retrying} icon={<ArrowClockwise size={15} />} onClick={onRetry}>
          Retry now
        </Button>
        <span className="text-[12.5px] text-subtle">Checking every 5 seconds</span>
      </div>
    </GateFrame>
  );
}

export function SessionErrorScreen({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  return (
    <GateFrame icon={<WarningOctagon size={20} />} title="MetaRush couldn't open the dashboard">
      <p className="mt-3 text-[14px] leading-relaxed text-muted">
        Something went wrong while signing you in. Here is what MetaRush said. Its setup check finds most problems like this: ask Claude Code to run the MetaRush setup check, or use a terminal.
      </p>
      <ErrorPanel className="mt-5" error={error} onRetry={onRetry} />
      <Details label="The command for a terminal" className="mt-5">
        <CopyBlock text="metarush doctor" caption="Checks the setup on this computer and says what to fix." />
      </Details>
    </GateFrame>
  );
}

export function Splash() {
  return (
    <div className="grid min-h-[100dvh] place-items-center">
      <div className="flex items-center gap-3 text-muted">
        <Logo className="h-7 w-7" />
        <Spinner size={14} label="Loading MetaRush" />
      </div>
    </div>
  );
}
