import type { ReactNode } from 'react';
import { useOp } from '../../api/queries';
import { cx } from '../../ui/primitives';
import { ClockCounterClockwise, FilmStrip, GearSix, HardDrives, Package, PlugsConnected, PuzzlePiece, SlidersHorizontal, TerminalWindow, UsersThree, Robot } from '@phosphor-icons/react';

/*
 * Settings in sections (D184): each section is a page of its own at
 * /settings/<id>, listed in the side menu (a dropdown on a phone) and in the
 * account menu at the right of the top bar. Older addresses (/settings#users,
 * /brand, /log, /looks) land in the right section through settingsRedirect.
 * The brand and spelling lists are Content Settings (D193), in each project's
 * Settings tab: /settings/brand and /settings/content open the default's.
 * Preferences (D208) are each person's own: estimates of what outside AI
 * services cost, Ask first or Auto, light or dark (which moved there from
 * General: /settings/general#appearance redirects).
 */

export type SettingsSectionId = 'general' | 'people' | 'preferences' | 'claude' | 'footage' | 'extension' | 'storage' | 'components' | 'integrations' | 'log' | 'cli';

export interface SettingsSection {
  id: SettingsSectionId;
  label: string;
  /** One line under the name in the side menu. */
  line: string;
  /** What the section's page says under its heading. */
  intro: string;
  icon: (size: number) => ReactNode;
}

export const SETTINGS_SECTIONS: SettingsSection[] = [
  {
    id: 'general',
    label: 'General',
    line: "Version, what's new, this workspace",
    intro: 'Which MetaRush you have and what is new in it, this workspace, and the keyboard.',
    icon: (s) => <GearSix size={s} />,
  },
  {
    id: 'people',
    label: 'People',
    line: 'Your account and who signs in',
    intro: 'Who can open this MetaRush and how they sign in, your own account and picture, and opening it from other computers.',
    icon: (s) => <UsersThree size={s} />,
  },
  {
    id: 'preferences',
    label: 'Preferences',
    line: 'Estimates, Auto, light or dark',
    intro: 'Your own choices: whether the costs of outside AI services are estimated before your edits, how Claude’s changes for you run, and light or dark.',
    icon: (s) => <SlidersHorizontal size={s} />,
  },
  {
    id: 'claude',
    label: 'Claude Code',
    line: 'Model, effort, usage and memory',
    intro: 'How Claude Code works in the dashboard: its model and effort, the steps it may take, and how much it may use.',
    icon: (s) => <Robot size={s} />,
  },
  {
    id: 'footage',
    label: 'Footage',
    line: 'Watched folder, links, looking',
    intro: 'How footage comes in and what MetaRush learns from it: the watched folder, links and looking at your footage. Spelling lists are in each project’s Settings tab.',
    icon: (s) => <FilmStrip size={s} />,
  },
  {
    id: 'extension',
    label: 'Browser extension',
    line: 'Send videos from your browser',
    intro: '“Send to MetaRush” in Chrome, Brave or Edge: install it, pair your browser with this computer, or remove one. What your browser sends is yours.',
    icon: (s) => <PuzzlePiece size={s} />,
  },
  {
    id: 'storage',
    label: 'Storage',
    line: 'Where footage is kept, review links, Trash',
    intro: 'Where your footage and renders are kept, moving them, this computer and its battery, review links for people outside the workspace, and the Trash.',
    icon: (s) => <HardDrives size={s} />,
  },
  {
    id: 'components',
    label: 'Components',
    line: 'Optional parts and what they add',
    intro: 'What MetaRush can use on this computer: the optional parts that unlock features, such as FFmpeg, the Remotion renderer and transcription, with what each adds and Add it.',
    icon: (s) => <Package size={s} />,
  },
  {
    id: 'integrations',
    label: 'Integrations',
    line: 'AI voices, generated video, looks',
    intro: 'Outside services MetaRush can use for your videos, and the looks of people and characters in generated video.',
    icon: (s) => <PlugsConnected size={s} />,
  },
  {
    id: 'log',
    label: 'Log',
    line: 'What MetaRush did and keeps',
    intro: 'Everything MetaRush did, newest first: what you and Claude asked for, the jobs that ran, and anything that went wrong.',
    icon: (s) => <ClockCounterClockwise size={s} />,
  },
  {
    id: 'cli',
    label: 'Command line',
    line: 'Commands for a terminal',
    intro: 'What you change in a terminal on this computer: the database and the storage passwords, and the setup check.',
    icon: (s) => <TerminalWindow size={s} />,
  },
];

export function isSettingsSection(v: string | undefined): v is SettingsSectionId {
  return !!v && SETTINGS_SECTIONS.some((s) => s.id === v);
}

export function settingsSection(id: SettingsSectionId): SettingsSection {
  return SETTINGS_SECTIONS.find((s) => s.id === id) ?? SETTINGS_SECTIONS[0]!;
}

/** Where an older Settings anchor (/settings#users) lives now. */
const OLD_ANCHORS: Record<string, string> = {
  users: '/settings/people#users',
  'your-account': '/settings/people#your-account',
  'remote-access': '/settings/people#remote-access',
  'claude-code': '/settings/claude#claude-code',
  'claude-usage': '/settings/claude#claude-usage',
  'watched-folder': '/settings/footage#watched-folder',
  'link-imports': '/settings/footage#link-imports',
  spelling: '/settings/content?part=spelling',
  looking: '/settings/footage#looking',
  extension: '/settings/extension',
  'browser-extension': '/settings/extension',
  storage: '/settings/storage#storage',
  machine: '/settings/storage#machine',
  trash: '/settings/storage#trash',
  'review-links': '/settings/storage#review-links',
  log: '/settings/log',
  about: '/settings/general',
  preferences: '/settings/preferences',
  estimates: '/settings/preferences#estimates',
  appearance: '/settings/preferences#appearance',
  components: '/settings/components',
};

/** The new address for /settings with an old #anchor (an unknown one opens General). */
export function settingsRedirect(hash: string): string {
  const key = hash.replace(/^#/, '');
  return OLD_ANCHORS[key] ?? '/settings/general';
}

/** Parts that moved to a section of their own (/settings/footage#browser-extension → /settings/extension, D191). */
const MOVED_PARTS: Record<string, Record<string, string>> = {
  footage: { extension: '/settings/extension', 'browser-extension': '/settings/extension', spelling: '/settings/content?part=spelling' },
  // Light or dark is a preference (D208).
  general: { appearance: '/settings/preferences#appearance' },
};

/** Where a section's old #anchor lives now, when it moved out (undefined: it stays). */
export function movedPart(section: string, hash: string): string | undefined {
  return MOVED_PARTS[section]?.[hash.replace(/^#/, '')];
}

/** Another computer moved the workspace's media: this one still uses the old storage (D112). */
export function useStorageBadge(): boolean {
  const st = useOp('storage.status', {}, { staleTime: 60_000, refetchInterval: 300_000 });
  return !!st.data?.switchMeToo;
}

export function StorageDot({ className }: { className?: string }) {
  return (
    <span
      className={cx('inline-block h-2 w-2 shrink-0 rounded-full bg-accent', className)}
      role="img"
      aria-label="This computer still uses the old storage"
      title="This computer still uses the old storage"
      data-storage-badge=""
    />
  );
}
