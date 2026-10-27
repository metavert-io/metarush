import { ShareNetwork } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import { OwnerOnly } from '../../components/OwnerOnly';
import { plural, relative } from '../../lib/format';
import { ErrorPanel, Segmented, Skeleton } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * Settings → Storage → Review links (D314): the owners' word on links people
 * send to someone outside the workspace — on or off, everyone or only owners,
 * and the longest a link lasts (a cloud bucket signs a link for a week at
 * most). Everyone sees it; only owners change it (share.settings.set).
 */
export function ReviewLinkSettings() {
  const settings = useOp('share.settings', {});
  const set = useOpMutation('share.settings.set', { invalidate: ['share.settings', 'share.plan'] });
  const toast = useToast();
  const s = settings.data;
  const change = (input: { on?: boolean; ownersOnly?: boolean; maxDays?: number }) =>
    set.mutate(input, {
      onSuccess: (o) => toast({ tone: 'success', title: o.summary }),
      onError: (e) => toast({ tone: 'error', title: 'Couldn’t change it', body: [e.message, e.hint].filter(Boolean).join(' ') }),
    });
  return (
    <section id="review-links" className="panel scroll-mt-28 p-5" aria-labelledby="review-links-h" data-review-link-settings>
      <h2 id="review-links-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <ShareNetwork size={17} />
        </span>
        Review links
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        People send a version to someone outside the workspace from its page (Share for review…). The page is kept in this workspace’s cloud bucket until the
        link ends; their comments come back to the version.
      </p>
      {settings.error ? <ErrorPanel compact error={settings.error} onRetry={() => void settings.refetch()} /> : null}
      {settings.isLoading || !s ? (
        <Skeleton className="h-16" />
      ) : (
        // The Storage page around it already says who may change it (one owners line a page).
        <OwnerOnly className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <span className="w-40 text-[12.5px] text-muted">Who may send them</span>
            <Segmented
              label="Who may send review links"
              className="max-w-full flex-wrap"
              value={!s.on ? 'off' : s.ownersOnly ? 'owners' : 'everyone'}
              onChange={(v) => change(v === 'off' ? { on: false } : { on: true, ownersOnly: v === 'owners' })}
              items={[
                { value: 'everyone', label: 'Anyone in the workspace' },
                { value: 'owners', label: 'Only owners' },
                { value: 'off', label: 'Nobody (off)' },
              ]}
            />
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <span className="w-40 text-[12.5px] text-muted">The longest a link lasts</span>
            <Segmented
              label="The longest a review link lasts"
              className="max-w-full flex-wrap"
              value={String(s.maxDays)}
              onChange={(v) => change({ maxDays: Number(v) })}
              items={[...new Set([1, 3, 7, s.maxDays])].sort((a, b) => a - b).map((d) => ({ value: String(d), label: plural(d, 'day') }))}
            />
          </div>
        </OwnerOnly>
      )}
      {s ? (
        <p className="mt-3 text-[12.5px] text-fg" data-review-link-summary>
          {s.summary}
          {s.updatedBy?.userName && s.updatedAt ? <span className="text-subtle"> Changed by {s.updatedBy.userName} {relative(s.updatedAt)}.</span> : null}
        </p>
      ) : null}
    </section>
  );
}
