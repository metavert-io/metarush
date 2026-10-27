import { Link } from 'react-router';
import { TrashSimple } from '@phosphor-icons/react';
import { libraryLink } from '../../api/library';
import { useOp, useOpMutation } from '../../api/queries';
import { bytes, plural, relative } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { ErrorPanel, Segmented, Skeleton } from '../../ui/primitives';

/*
 * Settings → Storage → Trash (D98): nothing in the Trash is deleted on its own unless a
 * person turns on "Empty the Trash after N days". Turning it on asks first
 * (styled), and the choice is the workspace's: every machine follows it.
 */

const CHOICES = ['0', '7', '30', '90'] as const;
type Choice = (typeof CHOICES)[number];

export function TrashSettings() {
  const settings = useOp('trash.settings', {});
  const trash = useOp('trash.list', {}, { staleTime: 30_000 });
  const configure = useOpMutation('trash.configure', { invalidate: ['trash.settings', 'library.list', 'trash.list'] });
  const dialog = useDialog();
  const toast = useToast();
  const days = settings.data?.emptyAfterDays ?? 0;
  const current = (CHOICES as readonly string[]).includes(String(days)) ? (String(days) as Choice) : undefined;
  const set = async (v: Choice) => {
    const n = Number(v);
    if (n === days) return;
    if (n > 0) {
      const ok = await dialog.confirm({
        title: `Empty the Trash after ${plural(n, 'day')}?`,
        body: `Clips are deleted for good ${plural(n, 'day')} after they are moved to the Trash, with their previews, transcripts and speaker data, for every machine. Clips a cut uses stay. You can turn this off again at any time.`,
        confirmLabel: 'Turn on',
        tone: 'danger',
      });
      if (!ok) return;
    }
    configure.mutate(
      { emptyAfterDays: n },
      {
        onSuccess: (o) => toast({ tone: 'success', title: o.summary }),
        onError: (e) => toast({ tone: 'error', title: "Couldn't change it", body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  return (
    <section id="trash" className="panel scroll-mt-28 p-5" aria-labelledby="trash-h">
      <h2 id="trash-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <TrashSimple size={17} />
        </span>
        Trash
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        Clips you move to the Trash can be restored until you delete them for good. Nothing is deleted on its own unless you choose a number of days here.
      </p>
      {settings.error ? <ErrorPanel compact error={settings.error} onRetry={() => void settings.refetch()} /> : null}
      {settings.isLoading ? (
        <Skeleton className="h-16" />
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <span className="text-[12.5px] text-muted">Empty the Trash</span>
            <Segmented
              label="Empty the Trash"
              className="max-w-full flex-wrap"
              value={current ?? '0'}
              onChange={(v) => void set(v)}
              items={[
                { value: '0', label: 'Never on its own' },
                { value: '7', label: 'After 7 days' },
                { value: '30', label: 'After 30 days' },
                { value: '90', label: 'After 90 days' },
              ]}
            />
          </div>
          <p className="text-[12.5px] text-fg" data-trash-setting>
            {settings.data?.summary}
            {settings.data?.updatedBy && days > 0 ? <span className="text-subtle"> Turned on by {settings.data.updatedBy} {relative(settings.data.updatedAt)}.</span> : null}
          </p>
          <p className="text-[12px] text-muted">
            {trash.data ? (trash.data.count ? `${plural(trash.data.count, 'clip')} in the Trash now (${bytes(trash.data.size)}). ` : 'The Trash is empty. ') : null}
            <Link to={libraryLink({ view: 'trash' })} className="text-accent-text hover:underline">
              Open the Trash
            </Link>
          </p>
        </div>
      )}
    </section>
  );
}
