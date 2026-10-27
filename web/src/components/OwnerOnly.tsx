import type { ReactNode } from 'react';
import { LockSimple } from '@phosphor-icons/react';
import { useSession } from '../api/queries';
import { cx, Theirs } from '../ui/primitives';

/*
 * Owner-only settings (D191): everyone sees this computer's settings, only an
 * owner changes them. The server refuses anyone else (ops.Flags.OwnerOnly);
 * the dashboard says so up front: the controls are disabled and the section
 * shows one line naming the owners. With "just me" sign-in the person is the
 * owner.
 */

export interface Ownership {
  /** The signed-in person may change this computer's settings. */
  owner: boolean;
  /** The owners' names ("Theo Hartley"). */
  owners: string[];
}

export function useOwnership(): Ownership {
  const s = useSession().data;
  // The session is loaded before any page shows (App's Gate).
  return { owner: s ? s.owner !== false : false, owners: s?.owners ?? [] };
}

/** "Theo Hartley", "Theo Hartley and Sam", "Ann, Bo and Cy". */
function namesList(names: string[]): ReactNode {
  return names.map((n, i) => (
    <span key={`${n}-${i}`}>
      {i > 0 ? (i === names.length - 1 ? ' and ' : ', ') : null}
      <Theirs>{n}</Theirs>
    </span>
  ));
}

/** The one line a section shows to someone who isn't an owner (nothing for an owner). */
export function OwnersNote({ className }: { className?: string }) {
  const o = useOwnership();
  if (o.owner) return null;
  return (
    <p className={cx('flex items-start gap-2 rounded-[10px] bg-hover/60 px-3 py-2 text-[12.5px] leading-relaxed text-muted hairline', className)} data-owners-line>
      <LockSimple size={14} className="mt-[3px] shrink-0 text-subtle" aria-hidden />
      <span>
        Only an owner can change this.
        {o.owners.length ? (
          <>
            {' '}
            {o.owners.length === 1 ? 'Owner' : 'Owners'}: {namesList(o.owners)}.
          </>
        ) : null}
      </span>
    </p>
  );
}

/**
 * Settings only an owner changes: for anyone else every control inside is
 * disabled (a disabled fieldset disables its buttons, inputs and selects), with
 * the owners line on top when `note` is set.
 */
export function OwnerOnly({ children, note, className }: { children: ReactNode; note?: boolean; className?: string }) {
  const o = useOwnership();
  return (
    <fieldset disabled={!o.owner} data-owner-only={o.owner ? undefined : ''} className={cx('m-0 min-w-0 border-0 p-0', className)}>
      {note ? <OwnersNote className="mb-4" /> : null}
      {children}
    </fieldset>
  );
}

/** A title for a disabled owner-only button elsewhere ("Only an owner can …"). */
export function ownerTitle(o: Ownership, what: string): string | undefined {
  if (o.owner) return undefined;
  return `Only an owner can ${what}${o.owners.length ? ` (${o.owners.join(', ')})` : ''}.`;
}
