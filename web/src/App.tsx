import { useCallback, useEffect, useState } from 'react';
import { createBrowserRouter, Navigate, RouterProvider, useLocation, useNavigate } from 'react-router';
import { QueryClientProvider, useQueryClient } from '@tanstack/react-query';
import { IconContext } from '@phosphor-icons/react';
import { onReachable, onSignedOut, onUnreachable, toApiError, type ApiError } from './api/client';
import { signedOutWay, signInPath, useAuthState } from './api/auth';
import { useLiveEvents } from './api/events';
import { createQueryClient, useSession } from './api/queries';
import { uploads } from './api/uploads';
import { DialogProvider } from './ui/Dialog';
import { ToastProvider } from './ui/Toast';
import { Shell } from './layout/Shell';
import { DaemonDownScreen, SessionErrorScreen, SignInScreen, Splash } from './layout/Gates';
import { Home } from './screens/Home';
import { ArchivedProjectsPage } from './screens/ArchivedProjects';
import { ProjectStudio } from './screens/ProjectStudio';
import { RevisionPage, ComparePage } from './screens/Revisions';
import { RendersPage } from './screens/Renders';
import { SettingsPage } from './screens/Settings';
import { DefaultContentPage } from './screens/settings/DefaultContent';
import { TodosPage } from './screens/Todos';
import { ImprovementsPage } from './screens/Improvements';
import { MotionReviewPage } from './screens/Motion';
import { LibraryAbilitiesPage, LibraryMotionPage, LibraryPage, LibraryPeoplePage } from './screens/Library';
import { RecipesPage } from './screens/Recipes';
import { ReviewDraftPage } from './screens/ReviewDraft';
import { RecordPage } from './screens/Record';
import { NotFound, RouteError } from './screens/NotFound';
import { SignInPage, WelcomePage } from './screens/SignIn';

const queryClient = createQueryClient();
uploads.attach(queryClient);

const router = createBrowserRouter([
  // Signing in (D183): these pages ask only what works without a session, so a
  // browser that isn't signed in never loops between them and the app.
  { path: '/signin', element: <SignInPage />, errorElement: <RouteError /> },
  { path: '/welcome/:token', element: <WelcomePage />, errorElement: <RouteError /> },
  {
    path: '/',
    // The gate decides between the app and a sign-in screen. Dialogs opened
    // inside the app render within the router, so their content can link and
    // navigate (job details, QA reports); the outer provider only serves the
    // sign-in and offline screens.
    element: <Gate />,
    errorElement: <RouteError />,
    children: [
      { index: true, element: <Home /> },
      // Deleted projects (D196): restore one, or clear them out for good.
      { path: 'projects/archived', element: <ArchivedProjectsPage /> },
      { path: 'p/:projectId', element: <ProjectStudio /> },
      { path: 'p/:projectId/r/:revisionId', element: <RevisionPage /> },
      { path: 'p/:projectId/compare', element: <ComparePage /> },
      // An AI app's draft, or any version, as one page (D249, D250): /review/drf_… or /review/rev_….
      { path: 'review/:id', element: <ReviewDraftPage /> },
      { path: 'renders', element: <RendersPage /> },
      { path: 'renders/:renderId', element: <RendersPage /> },
      { path: 'settings', element: <SettingsPage /> },
      // The brand and spelling are Content Settings (D193): these open the default's.
      { path: 'settings/brand', element: <DefaultContentPage /> },
      { path: 'settings/content', element: <DefaultContentPage /> },
      { path: 'settings/:section', element: <SettingsPage /> },
      { path: 'todos', element: <TodosPage /> },
      { path: 'improvements', element: <ImprovementsPage /> },
      { path: 'motion/:hash', element: <MotionReviewPage /> },
      { path: 'library', element: <LibraryPage /> },
      { path: 'library/people', element: <LibraryPeoplePage /> },
      { path: 'library/motion', element: <LibraryMotionPage /> },
      // Abilities (D230–D235): ?ability=<name>[&version=<hash>][&run=<id>].
      { path: 'library/abilities', element: <LibraryAbilitiesPage /> },
      // Recipes (D208): the gallery, ?recipe=<name>[&version=<n>] and ?run=<id>.
      { path: 'library/recipes', element: <RecipesPage /> },
      // Record your screen (D258): ?project=<id> records into a project, else the Inbox.
      { path: 'record', element: <RecordPage /> },
      { path: 'recipes', element: <MovedTo path="/library/recipes" /> },
      // Older addresses (D184): the places moved; links and bookmarks keep working.
      { path: 'people', element: <MovedTo path="/library/people" /> },
      { path: 'motion', element: <MovedTo path="/library/motion" /> },
      { path: 'brand', element: <MovedTo path="/settings/brand" /> },
      { path: 'log', element: <MovedTo path="/settings/log" /> },
      { path: 'looks', element: <MovedTo path="/settings/integrations" hash="#looks" /> },
      { path: 'preferences', element: <MovedTo path="/settings/preferences" /> },
      { path: '*', element: <NotFound /> },
    ],
  },
]);

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <IconContext.Provider value={{ weight: 'regular', mirrored: false }}>
        <ToastProvider>
          <DialogProvider>
            <RouterProvider router={router} />
          </DialogProvider>
        </ToastProvider>
      </IconContext.Provider>
    </QueryClientProvider>
  );
}

/**
 * Session and daemon reachability decide between the console and a full-page
 * gate. Not signed in: with email and password, the sign-in page (coming back
 * here afterwards); otherwise the screen that says how to get in (D183).
 */
function Gate() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const session = useSession();
  const [signedOut, setSignedOut] = useState<ApiError | null>(null);
  const [unreachable, setUnreachable] = useState(false);

  useEffect(() => onSignedOut((e) => setSignedOut(e)), []);
  useEffect(() => onUnreachable(() => setUnreachable(true)), []);
  useEffect(
    () =>
      onReachable(() => {
        setUnreachable(false);
      }),
    [],
  );

  const sessionErr = session.error ? toApiError(session.error) : null;
  const outErr = signedOut ?? (sessionErr?.isSignedOut ? sessionErr : null);
  const isDown = !!sessionErr?.isUnreachable && !session.data;
  const way = signedOutWay(outErr);
  // Signed out: the sign-in state (it needs no session) names the computer for the page, and gives
  // the method when the answer didn't (an older MetaRush).
  const state = useAuthState(!!outErr);
  const method = way.method ?? (outErr ? state.data?.method : undefined);
  const toSignIn = !!outErr && method === 'password';

  useLiveEvents(qc, !!session.data && !outErr);

  useEffect(() => {
    if (toSignIn) navigate(signInPath(), { replace: true });
  }, [toSignIn, navigate]);

  const retry = useCallback(() => {
    setSignedOut(null);
    void state.refetch();
    void session.refetch().then((r) => {
      if (r.data) void qc.invalidateQueries();
    });
  }, [session, state, qc]);

  if (session.isPending) return <Splash />;
  if (outErr) {
    if (toSignIn || state.isPending) return <Splash />;
    return (
      <SignInScreen
        way={{ method, remote: way.remote ?? (state.data ? !state.data.here : undefined), machine: state.data?.machine }}
        retrying={session.isFetching}
        onRetry={retry}
      />
    );
  }
  if (isDown) return <DaemonDownScreen onRetry={retry} retrying={session.isFetching} />;
  if (sessionErr && !session.data) return <SessionErrorScreen error={sessionErr} onRetry={retry} />;
  return (
    <>
      {unreachable ? <OfflineBanner /> : null}
      <DialogProvider>
        <Shell />
      </DialogProvider>
    </>
  );
}

/** An address that moved: the same query (and #part) at its new place, replacing the old one in the history. */
function MovedTo({ path, hash }: { path: string; hash?: string }) {
  const loc = useLocation();
  return <Navigate to={`${path}${loc.search}${loc.hash || hash || ''}`} replace />;
}

function OfflineBanner() {
  return (
    <div role="alert" className="fixed inset-x-0 top-0 z-[55] flex justify-center p-2">
      <div className="fade-up rounded-full bg-danger-soft px-4 py-1.5 text-[12.5px] font-medium text-danger backdrop-blur-xl hairline">
        MetaRush stopped answering. Showing what was here last; reconnecting…
      </div>
    </div>
  );
}
