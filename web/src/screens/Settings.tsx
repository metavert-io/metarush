import { useEffect, useState } from 'react';
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router';
import {
  BatteryCharging,
  BatteryFull,
  BatteryLow,
  BatteryWarning,
  CaretDown,
  Database,
  Desktop,
  FolderOpen,
  HardDrives,
  Keyboard,
  Laptop,
  ShieldCheck,
} from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import { useOp, useSession, useStatus } from '../api/queries';
import { relative } from '../lib/format';
import { resetSelection } from '../lib/workbench';
import { WatchSettings } from '../components/WatchFolder';
import { BrowserExtensionSettings, LinkImportSettings } from '../components/LinkImports';
import { Badge, CopyBlock, cx, Details, ErrorPanel, Freshness, Kbd, Skeleton } from '../ui/primitives';
import { ClaudeCodeSettings } from '../layout/assistant/SettingsCard';
import { UsageSettings } from '../layout/assistant/UsageSettings';
import { WhoseClaudeSettings } from '../layout/assistant/WhoAnswers';
import { LookingSettings } from './settings/LookingSettings';
import { StorageMove } from './settings/StorageMove';
import { TrashSettings } from './settings/TrashSettings';
import { WorkspaceExport } from './settings/WorkspaceExport';
import { ShipGateSettings } from './settings/ShipGateSettings';
import { ReviewLinkSettings } from './settings/ReviewLinkSettings';
import { LogPage, LogSettingsCard } from './Log';
import { RemoteAccessSettings } from './settings/RemoteAccess';
import { UsersSettings } from './settings/Users';
import { AboutMetaRush } from './settings/About';
import { IntegrationsPage } from './settings/Integrations';
import { ComponentsSection } from '../components/Components';
import { isSettingsSection, movedPart, settingsRedirect, settingsSection, SETTINGS_SECTIONS, StorageDot, useStorageBadge, type SettingsSectionId } from './settings/sections';
import { OwnerOnly } from '../components/OwnerOnly';
import { LooksPage } from './Looks';
import { PreferencesSection } from './settings/Preferences';
import { NotFound } from './NotFound';
import { useHost } from '../lib/host';
import { useToast } from '../ui/Toast';

/*
 * Settings (D184): one page per section at /settings/<section> — General,
 * People, Preferences (each person's own, D208), Claude Code, Footage, Browser
 * extension, Storage, Components (D324), Integrations, Log and Command line (the brand and
 * spelling lists are in each project's Settings tab, D193) — with a side menu (a dropdown on a
 * phone). /settings opens General; older addresses with an #anchor open the
 * section the anchor lives in now. Everyone sees this computer's settings;
 * only owners change them (OwnerOnly, D191).
 */

const THRESHOLDS = [
  { v: 0, label: 'Never wait' },
  { v: 10, label: '10%' },
  { v: 25, label: '25%' },
  { v: 40, label: '40%' },
  { v: 60, label: '60%' },
];

/** This machine: battery state and when big jobs wait for power (worker.configure). */
function MachineCard() {
  const s = useOp('worker.settings', {}, { refetchInterval: 30_000 });
  const host = useHost();
  const qc = useQueryClient();
  const toast = useToast();
  const [saving, setSaving] = useState<number | null>(null);
  const d = s.data;
  const save = async (v: number) => {
    setSaving(v);
    try {
      await call('worker.configure', { heavyBatteryThreshold: v });
      for (const k of ['worker.settings', 'activity.summary', 'worker.list']) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: v === 0 ? 'Big jobs now run on battery too' : `Big jobs wait below ${v}% on battery` });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not change it', body: toApiError(e).message });
    } finally {
      setSaving(null);
    }
  };
  const b = d?.battery;
  const icon = !b?.present ? <Desktop size={17} /> : b.charging ? <BatteryCharging size={17} /> : b.percent < (d?.heavyBatteryThreshold ?? 25) && !b.onAc ? <BatteryLow size={17} /> : <BatteryFull size={17} />;
  return (
    <section id="machine" className="panel scroll-mt-28 p-5">
      <h2 className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Laptop size={17} />
        </span>
        {host.remote ? host.machine : 'This machine'}
        <Freshness className="ml-auto" at={s.dataUpdatedAt || undefined} fetching={s.isFetching} />
      </h2>
      {s.isLoading || !d ? (
        <div className="mt-3 space-y-2">
          <Skeleton className="h-6" />
          <Skeleton className="h-9" />
        </div>
      ) : (
        <>
          <div className="mt-2 flex items-center gap-2 text-[13px] text-muted">
            <span className="text-subtle">{icon}</span>
            {b?.present ? (
              <span>
                Battery {b.percent}%{b.charging ? ', charging' : b.onAc ? ', plugged in' : ', on battery'}
              </span>
            ) : (
              <span>Runs on mains power (no battery)</span>
            )}
            {d.fakeBattery ? <Badge tone="info">simulated for testing</Badge> : null}
          </div>
          {d.deferringHeavy ? (
            <div className="mt-3 flex items-start gap-2 rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] text-accent-text">
              <BatteryWarning size={15} weight="fill" className="mt-[1px] shrink-0" /> Big jobs are waiting right now ({d.deferReason}). Plug in, or choose “Process now” in the activity bar.
            </div>
          ) : null}
          <p className="mt-4 text-[13px] leading-relaxed text-muted">
            Big jobs (transcripts and final renders) can drain a laptop. They wait only while it is unplugged and the battery is below:
          </p>
          <div role="radiogroup" aria-label="Battery level below which big jobs wait" className="mt-2.5 flex flex-wrap gap-1.5">
            {THRESHOLDS.map((t) => {
              const on = d.heavyBatteryThreshold === t.v;
              return (
                <button
                  key={t.v}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  disabled={saving !== null}
                  onClick={() => !on && void save(t.v)}
                  className={cx('inline-flex h-8 items-center gap-1.5 rounded-full px-3 text-[12.5px] transition-colors hairline', on ? 'bg-accent-soft font-medium text-accent-text ring-1 ring-accent-line' : 'text-muted hover:bg-hover hover:text-fg')}
                >
                  {t.label}
                  {t.v === 25 ? <span className="text-[11px] text-subtle">default</span> : null}
                </button>
              );
            })}
            {!THRESHOLDS.some((t) => t.v === d.heavyBatteryThreshold) ? <Badge tone="accent">{d.heavyBatteryThreshold}% (set in the settings file)</Badge> : null}
          </div>
          {!d.heavyEnabled ? <p className="mt-3 text-[12px] text-subtle">This machine is set not to run big jobs at all.</p> : null}
          <p className="mt-3 text-[12px] text-subtle">Saved for this machine only. Running a job from Claude Code or the command line with “wait” still runs it right away.</p>
        </>
      )}
    </section>
  );
}

export function SettingsPage() {
  const { section } = useParams();
  const loc = useLocation();
  useEffect(() => resetSelection({}), []);
  // Deep links (#watched-folder, #machine) scroll to their part of the section.
  useEffect(() => {
    if (!loc.hash) return;
    const t = setTimeout(() => document.getElementById(decodeURIComponent(loc.hash.slice(1)))?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 200);
    return () => clearTimeout(t);
  }, [loc.hash, section]);

  // Older links (/settings#users, #claude-code, #trash…) open the section they live in now.
  if (!section && loc.hash) return <Navigate to={settingsRedirect(loc.hash)} replace />;
  // Parts that became sections of their own (/settings/footage#browser-extension).
  const moved = section && loc.hash ? movedPart(section, loc.hash) : undefined;
  if (moved) return <Navigate to={moved} replace />;
  if (section && !isSettingsSection(section)) return <NotFound />;
  const current = settingsSection(section && isSettingsSection(section) ? section : 'general');

  return (
    <div className="mx-auto w-full max-w-[1240px] lg:grid lg:grid-cols-[252px_minmax(0,1fr)] lg:gap-10" data-settings={current.id}>
      <aside className="max-lg:mb-6">
        <div className="lg:sticky lg:top-[calc(var(--mr-header-h)+28px)]">
          <SettingsMenu current={current.id} />
        </div>
      </aside>
      <div className="min-w-0">
        <header className="mb-6">
          <h1 className="text-[26px] font-semibold tracking-[-0.03em] sm:text-[28px]">{current.label}</h1>
          <p className="mt-1 max-w-[72ch] text-[14px] leading-relaxed text-muted">{current.intro}</p>
        </header>
        <SectionBody id={current.id} />
      </div>
    </div>
  );
}

/** The sections: a side menu with a line about each on wide screens, a dropdown on a phone. */
function SettingsMenu({ current }: { current: SettingsSectionId }) {
  const navigate = useNavigate();
  const here = settingsSection(current);
  const storage = useStorageBadge();
  return (
    <>
      <p className="mb-2 px-1 text-[12.5px] font-semibold text-subtle lg:mb-3 lg:px-3">Settings</p>
      <nav aria-label="Settings sections" className="max-lg:hidden" data-settings-menu>
        <ul className="space-y-0.5">
          {SETTINGS_SECTIONS.map((s) => {
            const on = s.id === current;
            return (
              <li key={s.id}>
                <Link
                  to={`/settings/${s.id}`}
                  aria-current={on ? 'page' : undefined}
                  className={cx(
                    'flex items-start gap-3 rounded-[11px] px-3 py-2 transition-colors duration-200 focus-visible:outline-2 focus-visible:outline-accent',
                    on ? 'bg-hover text-fg' : 'text-muted hover:bg-hover/70 hover:text-fg',
                  )}
                  data-settings-link={s.id}
                >
                  <span className={cx('mt-[2px] shrink-0', on ? 'text-accent-text' : 'text-subtle')} aria-hidden>
                    {s.icon(17)}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5 text-[13.5px] font-medium">
                      {s.label}
                      {s.id === 'storage' && storage ? <StorageDot /> : null}
                    </span>
                    <span className="block truncate text-[12px] text-subtle" title={s.line}>
                      {s.line}
                    </span>
                  </span>
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>
      <label className="block lg:hidden">
        <span className="sr-only">Settings section</span>
        <span className="relative block">
          <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-accent-text" aria-hidden>
            {here.icon(17)}
          </span>
          <select
            value={current}
            onChange={(e) => navigate(`/settings/${e.target.value}`)}
            className="h-11 w-full appearance-none rounded-[12px] bg-panel pl-10 pr-10 text-[14px] font-medium text-fg hairline focus:outline-2 focus:outline-offset-0 focus:outline-accent"
            data-settings-select
          >
            {SETTINGS_SECTIONS.map((s) => (
              <option key={s.id} value={s.id}>
                {s.label}
              </option>
            ))}
          </select>
          <CaretDown size={14} className="pointer-events-none absolute right-3.5 top-1/2 -translate-y-1/2 text-subtle" aria-hidden />
        </span>
      </label>
    </>
  );
}

function SectionBody({ id }: { id: SettingsSectionId }) {
  switch (id) {
    case 'general':
      return <GeneralSection />;
    case 'people':
      return (
        <div className="space-y-6">
          <UsersSettings />
          <OwnerOnly note>
            <RemoteAccessSettings />
          </OwnerOnly>
        </div>
      );
    case 'preferences':
      // Each person's own; the owners' word on estimates for everyone inside.
      return <PreferencesSection />;
    case 'claude':
      return (
        <div className="space-y-6">
          <WhoseClaudeSettings />
          <OwnerOnly note className="space-y-6">
            <ClaudeCodeSettings />
            <UsageSettings />
          </OwnerOnly>
        </div>
      );
    case 'footage':
      // All of it is this computer's (the spelling lists move to a project's Content Settings).
      return (
        <OwnerOnly note>
          <FootageSection />
        </OwnerOnly>
      );
    case 'extension':
      // Everyone pairs and removes their own browsers; owners anyone's (D197).
      return <BrowserExtensionSettings />;
    case 'storage':
      return (
        <OwnerOnly note>
          <StorageSection />
        </OwnerOnly>
      );
    case 'components':
      // Everyone sees what is here; owners add and remove (D327).
      return <ComponentsSection />;
    case 'integrations':
      return (
        <div className="space-y-12">
          <IntegrationsPage />
          <LooksPage />
        </div>
      );
    case 'log':
      return (
        <div className="space-y-10">
          <LogPage />
          <OwnerOnly note>
            <LogSettingsCard />
          </OwnerOnly>
        </div>
      );
    case 'cli':
      return <CommandLineSection />;
  }
}

/** About MetaRush, the workspace, the keyboard (light or dark is a preference, D208). */
function GeneralSection() {
  const ws = useOp('workspace.show', {});
  const status = useStatus();
  const session = useSession();
  const host = useHost();
  const d = ws.data;
  return (
    <div className="space-y-6">
      {ws.error ? <ErrorPanel error={ws.error} onRetry={() => void ws.refetch()} /> : null}
      <AboutMetaRush />
      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        <Card icon={<Desktop size={17} />} title={d?.workspace.name ?? 'Workspace'} loading={ws.isLoading} id="workspace" action={<Freshness at={ws.dataUpdatedAt || undefined} fetching={ws.isFetching} onRefresh={() => void ws.refetch()} />}>
          <Rows
            rows={[
              ['Signed in as', session.data ? `${session.data.user.name}${session.data.user.email ? ` (${session.data.user.email})` : ''}${session.data.access.remote ? (session.data.access.signedInWith === 'tailscale' ? ', with Tailscale, from another computer' : ', from another computer') : ''}` : '…'],
              [host.remote ? 'Runs on' : 'This machine', d ? `${d.installation.name} (${d.installation.hostname})` : ''],
              ['Running since', status.data ? relative(status.data.startedAt) : '…'],
            ]}
          />
          {d ? (
            <Details>
              <div>
                Workspace ID <span className="tc text-muted">{d.workspace.id}</span>
              </div>
              <div>
                Dashboard address <span className="tc text-muted">{d.dashboard}</span>
              </div>
            </Details>
          ) : null}
        </Card>
        <div className="space-y-6">
          <Card icon={<Keyboard size={17} />} title="Keyboard" id="keyboard">
            <ul className="space-y-2 text-[13px]">
              {[
                ['/', 'Search footage and what was said'],
                ['j / k', 'Move through lists'],
                ['Enter', 'Open the highlighted item'],
                ['Space', 'Play or pause the active player'],
                ['← →', 'Jump 1 second on a selected waveform (Shift: 5 seconds)'],
                ['Esc', 'Close dialogs and panels'],
              ].map(([k = '', v]) => (
                <li key={k} className="flex items-center gap-3">
                  <span className="flex w-[64px] shrink-0 gap-1">
                    {k.split(' ').map((x) => (x === '/' && k !== '/' ? <span key={x} className="text-subtle">/</span> : <Kbd key={x}>{x}</Kbd>))}
                  </span>
                  <span className="text-muted">{v}</span>
                </li>
              ))}
            </ul>
          </Card>
        </div>
      </div>
      <ShipGateSettings />
      <div className="flex items-center gap-2 text-[12px] text-subtle">
        <Database size={13} className="shrink-0" /> Your projects live in your database; this browser keeps only your theme and panel choices. Light or dark and
        your other preferences are in{' '}
        <Link to="/settings/preferences" className="font-medium text-accent-text hover:underline">
          Settings → Preferences
        </Link>
        .
      </div>
    </div>
  );
}

/** The watched folder, links, looking at footage. */
function FootageSection() {
  const host = useHost();
  return (
    <div className="space-y-6">
      <section id="watched-folder" className="panel scroll-mt-28 p-5" aria-labelledby="watch-h">
        <h2 id="watch-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
          <span className="text-subtle">
            <FolderOpen size={17} />
          </span>
          Watched folder
        </h2>
        <p className="mb-4 text-[12.5px] leading-relaxed text-muted">Anything put in this folder {host.remote ? `on ${host.machine} ` : ''}is added to MetaRush once it has finished copying.</p>
        <WatchSettings />
      </section>
      <LinkImportSettings />
      <LookingSettings />
    </div>
  );
}

/** Where footage is kept, moving it, this computer, the Trash. */
function StorageSection() {
  const ws = useOp('workspace.show', {});
  const status = useStatus();
  const host = useHost();
  const d = ws.data;
  return (
    <div className="space-y-6">
      {ws.error ? <ErrorPanel error={ws.error} onRetry={() => void ws.refetch()} /> : null}
      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        <Card icon={<HardDrives size={17} />} title="Storage" loading={ws.isLoading} id="storage-kept">
          <Rows
            rows={[
              [
                'Footage and renders',
                <span className="flex flex-wrap items-center gap-2">
                  <span>{storageName(d?.workspace.mediaStore.provider, host.here)}</span>
                  {d ? <Badge tone={d.mediaShared ? 'ok' : 'accent'}>{d.mediaShared ? 'shared' : host.remote ? `only on ${host.machine}` : 'this machine only'}</Badge> : null}
                </span>,
              ],
              [
                'Database',
                <span className="flex flex-wrap items-center gap-2">
                  <span>Your projects, versions and notes</span>
                  {status.data ? <Badge tone={status.data.dbOk ? 'ok' : 'danger'}>{status.data.dbOk ? 'connected' : 'not answering'}</Badge> : null}
                </span>,
              ],
              ['Transcripts', status.data?.transcriber ? `Made ${host.onHere}` : host.remote ? `Not set up on ${host.machine}` : 'Not set up here'],
            ]}
          />
          {d ? (
            <Details>
              <div className="break-all">
                Media store <span className="tc text-muted">{d.mediaStore}</span>
              </div>
              <div className="break-all">
                This machine's copies <span className="tc text-muted">{d.cacheDir}</span>
              </div>
              <div className="break-all">
                Database <span className="tc text-muted">{d.database}</span>
              </div>
              {status.data?.transcriber ? (
                <div>
                  Transcriber <span className="tc text-muted">{status.data.transcriber}</span>
                </div>
              ) : null}
            </Details>
          ) : null}
          {d && !d.mediaShared ? (
            <p className="mt-3 rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text">
              Your footage is kept {host.onHere} only, so your other computers that run MetaRush can see the projects but can't play or render the footage. To share it, move it to online storage or a shared drive (see “Move the media” below).
            </p>
          ) : null}
        </Card>
        <MachineCard />
      </div>
      <StorageMove />
      <WorkspaceExport />
      <ReviewLinkSettings />
      <TrashSettings />
    </div>
  );
}

/** What is changed in a terminal: the database and storage passwords, the setup check. */
function CommandLineSection() {
  return (
    <section id="cli" className="panel scroll-mt-28 p-5" aria-labelledby="cli-h">
      <h2 id="cli-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <ShieldCheck size={17} />
        </span>
        Change the database or passwords
      </h2>
      <p className="max-w-[72ch] text-[13px] leading-relaxed text-muted">
        You change the database’s password and the storage’s keys in a terminal on this computer; MetaRush never shows them in the browser. The dashboard picks up the change by itself. To move the media to another storage, use Settings → Storage → Move the media. To check that everything is set up well, ask Claude Code to run the MetaRush setup check.
      </p>
      <Details label="The commands for a terminal">
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <CopyBlock text="metarush init --reconfigure" caption="Change the database or the storage's passwords (kept in your keychain)." />
          <CopyBlock text="metarush doctor" caption="The setup check: tools, database, storage and machines, with fixes for anything broken." />
        </div>
      </Details>
    </section>
  );
}

/** Where the footage is kept, by kind of storage (the exact store is in Details). */
function storageName(provider: string | undefined, here: string): string {
  if (!provider) return '';
  return ({ filesystem: `A folder on ${here}`, gcs: 'Google Cloud Storage', s3: 'Online storage' } as Record<string, string>)[provider] ?? provider;
}

function Card({ icon, title, loading, children, id, action }: { icon: React.ReactNode; title: string; loading?: boolean; children: React.ReactNode; id?: string; action?: React.ReactNode }) {
  return (
    <section className="panel scroll-mt-28 p-5" id={id}>
      <h2 className="mb-4 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">{icon}</span>
        <span className="min-w-0 truncate">{title}</span>
        {action ? <span className="ml-auto">{action}</span> : null}
      </h2>
      {loading ? (
        <div className="space-y-2">
          <Skeleton className="h-6" />
          <Skeleton className="h-6" />
          <Skeleton className="h-6" />
        </div>
      ) : (
        children
      )}
    </section>
  );
}

function Rows({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="divide-y divide-line">
      {rows.map(([k, v]) => (
        <div key={k} className="grid grid-cols-[120px_1fr] gap-3 py-2.5 text-[13px]">
          <dt className="text-subtle">{k}</dt>
          <dd className="min-w-0 break-words text-fg">{v || <span className="text-subtle">-</span>}</dd>
        </div>
      ))}
    </dl>
  );
}
