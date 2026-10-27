import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { NavLink, Outlet, useLocation, useSearchParams } from 'react-router';
import { Books, FilmSlate, MagnifyingGlass, Question, Queue, TerminalWindow } from '@phosphor-icons/react';
import { TodoBadge } from '../components/TodoBadge';
import { MotionBadge } from '../components/MotionBadge';
import { useSession } from '../api/queries';
import { useAssistantStatus } from '../api/assistant';
import { useHotkeys } from '../lib/hooks';
import { openAskRun, setAskOpen } from '../lib/workbench';
import { Logo } from '../ui/Logo';
import { cx, IconButton, Kbd, Spinner } from '../ui/primitives';
import { GlobalDrop } from './GlobalDrop';
import { DragLabel } from './DragLabel';
import { UploadTray } from './UploadTray';
import { AskClaude } from './AskClaude';
import { ActivityButton, ActivityDrawer, ActivityStrip } from './ActivityBar';
import { NotificationBell } from './NotificationBell';
import { AccountMenu } from './AccountMenu';
import { DuplicateResolver } from './DuplicateDialog';
import { SearchPalette } from './SearchPalette';
import { JobDetailsHost } from '../components/JobDetails';

export function Shell() {
  const [searchOpen, setSearchOpen] = useState(false);
  const loc = useLocation();
  const [params, setParams] = useSearchParams();
  useHotkeys({
    '/': (e) => {
      e.preventDefault();
      setSearchOpen(true);
    },
  });
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [loc.pathname]);
  // Deep link from a notification: ?ask=<assistant run>
  useEffect(() => {
    const run = params.get('ask');
    if (!run) return;
    openAskRun(run);
    const next = new URLSearchParams(params);
    next.delete('ask');
    setParams(next, { replace: true });
  }, [params, setParams]);

  return (
    <div className="min-h-[100dvh]">
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[90] focus:rounded-[8px] focus:bg-accent focus:px-3 focus:py-2 focus:text-accent-ink">
        Skip to content
      </a>
      <TopBar onSearch={() => setSearchOpen(true)} />
      <main id="main" className="mx-auto w-full max-w-[1480px] px-4 pb-28 pt-6 sm:px-6 lg:px-8 lg:pt-8">
        <Outlet />
      </main>
      <GlobalDrop />
      <DragLabel />
      <UploadTray />
      <AskClaude />
      <DuplicateResolver />
      <JobDetailsHost />
      {searchOpen ? <SearchPalette onClose={() => setSearchOpen(false)} /> : null}
    </div>
  );
}

/**
 * The top bar's real height as --mr-header-h on <html>: it grows while the
 * activity strip shows, and everything that sticks below it (the studio's
 * section tabs and what sticks under those) follows it instead of assuming
 * the bar's usual 56 px.
 */
function useHeaderHeight() {
  const ref = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const root = document.documentElement;
    const set = () => root.style.setProperty('--mr-header-h', `${Math.round(el.getBoundingClientRect().height)}px`);
    set();
    const ro = new ResizeObserver(set);
    ro.observe(el);
    return () => {
      ro.disconnect();
      root.style.removeProperty('--mr-header-h');
    };
  }, []);
  return ref;
}

function TopBar({ onSearch }: { onSearch: () => void }) {
  const session = useSession();
  const assistant = useAssistantStatus();
  const working = !!assistant.data?.activeRunId;
  const waiting = assistant.data?.waiting ?? 0;
  const header = useHeaderHeight();
  return (
    <header ref={header} className="sticky top-0 z-40 border-b border-line bg-[color-mix(in_oklab,var(--bg)_82%,transparent)] backdrop-blur-xl">
      <div className="mx-auto flex h-14 w-full max-w-[1480px] items-center gap-0.5 px-4 sm:gap-2 sm:px-6 lg:px-8">
        <NavLink to="/" className="group mr-0.5 flex shrink-0 items-center gap-2.5 rounded-[10px] py-1 pr-1 sm:mr-3 sm:pr-2" aria-label="MetaRush home">
          <Logo className="h-7 w-7" />
          <span className="hidden flex-col leading-none md:flex">
            <span className="text-[14.5px] font-semibold tracking-[-0.02em] text-fg">MetaRush</span>
            <span className="mt-0.5 max-w-[150px] truncate text-[11px] text-subtle">{session.data?.workspace.name || 'Workspace'}</span>
          </span>
        </NavLink>
        {/* Four places (D184). Labels from lg; below that the places are icons (with
            tooltips) so search, Claude Code, activity, the bell and the menu keep their
            room, down to a phone. People and Motion are Library tabs; everything else is
            in the menu at the right. */}
        <nav aria-label="Primary" className="flex shrink-0 items-center gap-0.5">
          <NavItem to="/" end icon={<FilmSlate size={17} />} label="Studio" />
          <NavItem to="/library" icon={<Books size={17} />} label="Library" badge={<MotionBadge />} alsoActive="/motion/" />
          <NavItem to="/renders" icon={<Queue size={17} />} label="Renders" />
          <NavItem to="/todos" icon={<Question size={17} />} label="To-dos" badge={<TodoBadge link={false} />} />
        </nav>
        <div className="min-w-0 flex-1" />
        <button
          type="button"
          onClick={onSearch}
          className="hidden h-9 w-[min(320px,24vw)] min-w-[128px] items-center gap-2 rounded-[10px] bg-panel px-3 text-left text-[13px] text-subtle hairline transition-colors hover:text-muted lg:flex"
        >
          <MagnifyingGlass size={15} />
          <span className="flex-1 truncate">Search footage and what was said</span>
          <Kbd>/</Kbd>
        </button>
        <IconButton label="Search (/)" className="lg:hidden max-sm:w-8" onClick={onSearch}>
          <MagnifyingGlass size={18} />
        </IconButton>
        <button
          type="button"
          onClick={() => setAskOpen(true)}
          className="relative inline-flex h-9 shrink-0 items-center justify-center gap-2 rounded-[10px] px-2 text-[13px] font-medium text-accent-text transition-colors hover:bg-accent-soft max-sm:w-8 max-sm:px-0 sm:px-3"
          aria-label={waiting ? `Ask Claude Code (${waiting} waiting for you)` : working ? 'Ask Claude Code (working on a request)' : 'Ask Claude Code'}
          title={waiting ? `${waiting} ${waiting === 1 ? 'card or question waits' : 'cards and questions wait'} for you` : working ? 'Claude Code is working on a request' : undefined}
        >
          {working ? <Spinner size={15} /> : <TerminalWindow size={17} />}
          <span className="hidden whitespace-nowrap min-[1400px]:inline">Ask Claude Code</span>
          {waiting ? (
            <span className="tc grid h-[18px] min-w-[18px] place-items-center rounded-full bg-accent px-1 text-[10.5px] font-semibold text-accent-ink max-[1399px]:absolute max-[1399px]:-right-0.5 max-[1399px]:-top-0.5" data-assistant-waiting aria-hidden>
              {waiting > 9 ? '9+' : waiting}
            </span>
          ) : null}
        </button>
        <ActivityButton />
        <NotificationBell />
        <AccountMenu />
      </div>
      <ActivityStrip />
      <ActivityDrawer />
    </header>
  );
}

function NavItem({
  to,
  icon,
  label,
  end,
  badge,
  className,
  alsoActive,
}: {
  to: string;
  icon: ReactNode;
  label: string;
  end?: boolean;
  badge?: ReactNode;
  className?: string;
  /** Pages under another address that belong to this place (a motion graphic's review is in the Library). */
  alsoActive?: string;
}) {
  const loc = useLocation();
  // Project pages live under the Studio.
  const extra = (to === '/' && loc.pathname.startsWith('/p/')) || (!!alsoActive && loc.pathname.startsWith(alsoActive));
  return (
    <NavLink
      to={to}
      end={end}
      aria-label={label}
      title={label}
      className={({ isActive }) =>
        cx(
          'relative inline-flex h-9 shrink-0 items-center gap-1.5 rounded-[10px] px-1.5 text-[13px] font-medium transition-colors duration-200 sm:gap-2 sm:px-2.5',
          isActive || extra ? 'bg-hover text-fg' : 'text-muted hover:bg-hover hover:text-fg',
          className,
        )
      }
    >
      {icon}
      <span className="hidden whitespace-nowrap lg:inline">{label}</span>
      {badge ? <span className="pointer-events-none flex empty:hidden max-lg:absolute max-lg:-right-1 max-lg:-top-1 max-lg:[&_svg]:hidden">{badge}</span> : null}
    </NavLink>
  );
}
