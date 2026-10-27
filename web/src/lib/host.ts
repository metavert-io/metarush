import { useSession } from '../api/queries';

/**
 * Where this browser is (remote access, D128). On the computer that runs
 * MetaRush, actions on files and windows happen in front of the person ("on
 * this computer"); from another computer, through the Tailscale address, they
 * happen on that computer, which the dashboard names ("on Sam's MacBook").
 */
export interface Host {
  /** The browser is on another computer. */
  remote: boolean;
  /** The computer MetaRush runs on, as people call it. */
  machine: string;
  /** "this computer", or the machine's name. */
  here: string;
  /** "on this computer", or "on Sam's MacBook". */
  onHere: string;
  /** The page is secure (HTTPS, or this computer's own address): the microphone works. */
  secure: boolean;
  /** The address for other computers, when remote access is on. */
  publicUrl?: string;
}

export function useHost(): Host {
  const s = useSession();
  const a = s.data?.access;
  const remote = a ? a.remote : isElsewhere();
  const machine = a?.machine || 'the computer that runs MetaRush';
  return { remote, machine, here: remote ? machine : 'this computer', onHere: remote ? `on ${machine}` : 'on this computer', secure: a?.secure ?? true, publicUrl: a?.publicUrl };
}

/** Before the session is known: is this page served from another computer's address? */
export function isElsewhere(): boolean {
  const h = typeof location === 'undefined' ? '' : location.hostname;
  return !!h && h !== '127.0.0.1' && h !== 'localhost' && h !== '[::1]' && h !== '::1';
}
