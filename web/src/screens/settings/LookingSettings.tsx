import { Eye } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, ErrorPanel, Segmented, Skeleton } from '../../ui/primitives';

/*
 * Settings → Footage → Looking at your footage: the on-screen index (Apple Vision on a
 * Mac: free, nothing uploaded) and written scene descriptions by the person's
 * own Claude Code (off unless turned on; it uses their Claude Code usage).
 */
export function LookingSettings() {
  const status = useOp('footage.visuals.status', {});
  const describe = useOp('footage.describe.settings', {});
  const configure = useOpMutation('footage.describe.configure', { invalidate: ['footage.describe.settings'] });
  const dialog = useDialog();
  const toast = useToast();
  const s = status.data;
  const d = describe.data;
  const set = async (on: boolean) => {
    if (on) {
      const ok = await dialog.confirm({
        title: 'Describe the scenes of new clips?',
        body: 'Your own Claude Code, on this computer, looks at each new clip and writes a sentence or two per scene, so search finds clips by what happens in them. It uses your Claude Code usage. Projects can still choose for themselves.',
        confirmLabel: 'Turn on',
      });
      if (!ok) return;
    }
    configure.mutate({ on }, { onSuccess: (o) => toast({ tone: 'success', title: o.summary }), onError: (e) => toast({ tone: 'error', title: 'Could not change it', body: e.message }) });
  };
  return (
    <section id="looking" className="panel scroll-mt-28 p-5" aria-labelledby="looking-h">
      <h2 id="looking-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Eye size={17} />
        </span>
        Looking at your footage
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        On a Mac, MetaRush looks at about one frame a second of each clip with Apple Vision: people and faces, the place, things and animals, and text on screen. It stays on your computer and costs nothing. Search finds clips by what is on screen, and Claude Code reads it before saying anything about the picture.
      </p>
      {status.error ? <ErrorPanel compact error={status.error} onRetry={() => void status.refetch()} /> : null}
      {s ? (
        <dl className="divide-y divide-line overflow-hidden rounded-[12px] text-[12.5px] hairline" data-looking-status>
          <div className="grid grid-cols-[130px_1fr] gap-3 px-3.5 py-2.5">
            <dt className="text-subtle">This computer</dt>
            <dd className="min-w-0 text-fg">
              <span className="mr-2">{s.here.text}</span>
              {s.here.ready ? <Badge tone="ok">ready</Badge> : s.here.supported ? <Badge tone="accent">set up on first use</Badge> : <Badge>not a Mac</Badge>}
              {s.here.fix ? <span className="mt-1 block text-[11.5px] text-subtle">{s.here.fix}</span> : null}
            </dd>
          </div>
          <div className="grid grid-cols-[130px_1fr] gap-3 px-3.5 py-2.5">
            <dt className="text-subtle">Your clips</dt>
            <dd className="text-fg">{s.summary}</dd>
          </div>
        </dl>
      ) : status.isLoading ? (
        <Skeleton className="h-20" />
      ) : null}
      <h3 className="mb-1 mt-5 text-[13.5px] font-semibold">Scenes, in words</h3>
      <p className="mb-3 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        Claude Code can also write a sentence or two per scene of each new clip, so search finds clips by what happens in them. It uses your own Claude Code usage. Projects can choose for themselves on a clip's On screen tab.
      </p>
      {d ? (
        <div className="flex flex-wrap items-center gap-3">
          <Segmented<'on' | 'off'>
            label="Scene descriptions for this workspace"
            value={d.workspace ? 'on' : 'off'}
            onChange={(v) => void set(v === 'on')}
            items={[
              { value: 'off', label: 'Off' },
              { value: 'on', label: 'On' },
            ]}
          />
          <span className="text-[12px] text-subtle">{d.claudeText}</span>
        </div>
      ) : describe.isLoading ? (
        <Skeleton className="h-9 w-48" />
      ) : null}
    </section>
  );
}
