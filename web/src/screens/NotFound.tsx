import { Link, useRouteError } from 'react-router';
import { Compass, WarningOctagon } from '@phosphor-icons/react';
import { Details, EmptyState } from '../ui/primitives';

export function NotFound() {
  return (
    <div className="panel mx-auto mt-10 max-w-[560px]">
      <EmptyState
        icon={<Compass size={22} />}
        title="Nothing here"
        body="This address doesn't match a page in the dashboard. Check the link, or go back to the studio."
        action={
          <Link to="/" className="text-[13px] font-medium text-accent-text hover:underline">
            Back to the studio
          </Link>
        }
      />
    </div>
  );
}

export function RouteError() {
  const err = useRouteError();
  const msg = err instanceof Error ? err.message : typeof err === 'string' ? err : 'Something unexpected went wrong';
  return (
    <div className="grid min-h-[100dvh] place-items-center px-4">
      <div className="panel w-full max-w-[560px]">
        <EmptyState
          icon={<WarningOctagon size={22} />}
          title="This page stopped working"
          body={
            <>
              Something went wrong while showing this page. Reloading the dashboard usually fixes it; nothing you saved is lost.
              <Details compact className="mt-2">
                <span className="tc">{msg}</span>
              </Details>
            </>
          }
          action={
            <a href="/" className="text-[13px] font-medium text-accent-text hover:underline">
              Reload the dashboard
            </a>
          }
        />
      </div>
    </div>
  );
}
