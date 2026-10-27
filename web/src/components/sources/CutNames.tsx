import { useState } from 'react';
import { NotePencil, PencilSimple, Sparkle } from '@phosphor-icons/react';
import type { ProjectShowOut, RevisionSummary } from '../../api/types';
import { call, toApiError } from '../../api/client';
import { useDialog } from '../../ui/Dialog';
import { Button, cx, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { useRefreshSources } from './FixesPanel';

/*
 * Cuts and versions have descriptive names too (D150): a cut is called
 * “Main open day video”, not by the words Claude used for its first version;
 * a version carries a short note saying what it changed ("flicker fixes and
 * headline cards"). Renaming saves no new version and keeps approvals.
 */

/** The project's cuts by name, each with Rename and MetaRush's suggestion. */
export function CutNameBar({ data, className }: { data: ProjectShowOut; className?: string }) {
  const cuts = (data.cuts ?? []).filter((c) => c.headRevisionId);
  const toast = useToast();
  const dialog = useDialog();
  const refresh = useRefreshSources();
  const [busy, setBusy] = useState<string | null>(null);
  if (cuts.length === 0) return null;
  const rename = async (branch: string, current: string, suggested?: boolean) => {
    let title: string | null = null;
    if (!suggested) {
      title = await dialog.prompt({ title: 'Name this cut', label: 'Name', defaultValue: current, maxLength: 120, required: true, helper: 'A few words saying what the video is, like “Main open day video”. Saved versions stay as they are.' });
      if (!title || title.trim() === current) return;
    }
    setBusy(branch);
    try {
      const out = await call('cut.rename', { projectId: data.project.id, ...(branch !== 'main' ? { branch } : {}), ...(suggested ? { suggested: true } : { title: title!.trim() }) });
      refresh();
      toast({ tone: 'success', title: `The cut is called “${out.name}” now` });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't rename the cut", body: err.message });
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className={cx('space-y-1.5', className)} data-cut-names>
      {cuts.map((c) => {
        // Nobody named it yet: the cut goes by MetaRush's suggestion, which a click keeps.
        const suggested = c.nameBy === 'suggested';
        const offer = !suggested && c.suggestion && c.suggestion !== c.name && c.nameBy !== 'person' ? c.suggestion : '';
        return (
          <div key={c.branch} className="flex flex-wrap items-center gap-2 rounded-[12px] px-3 py-2 hairline" data-cut-name={c.branch}>
            <span className="text-[12.5px] text-muted">{cuts.length > 1 ? 'Cut' : 'This cut'}</span>
            <Theirs className="min-w-0 truncate text-[13.5px] font-semibold text-fg">{c.name}</Theirs>
            {c.headNumber ? <span className="text-[12px] text-subtle">version {c.headNumber}</span> : null}
            <Button size="sm" variant="ghost" icon={<PencilSimple size={12} />} loading={busy === c.branch && !offer && !suggested} onClick={() => void rename(c.branch, c.name)} data-rename-cut>
              Rename
            </Button>
            {suggested ? (
              <span className="flex min-w-0 flex-wrap items-center gap-1.5 text-[12px] sm:ml-auto">
                <Sparkle size={12} weight="fill" className="text-accent-text" aria-hidden />
                <span className="text-muted">Suggested by MetaRush</span>
                <Button size="sm" variant="secondary" loading={busy === c.branch} onClick={() => void rename(c.branch, c.name, true)} data-use-cut-name>
                  Keep this name
                </Button>
              </span>
            ) : offer ? (
              <span className="flex min-w-0 flex-wrap items-center gap-1.5 text-[12px] sm:ml-auto">
                <Sparkle size={12} weight="fill" className="text-accent-text" aria-hidden />
                <span className="text-muted">Suggested:</span>
                <Theirs className="font-medium text-fg">{offer}</Theirs>
                <Button size="sm" variant="secondary" loading={busy === c.branch} onClick={() => void rename(c.branch, c.name, true)} data-use-cut-name>
                  Use it
                </Button>
              </span>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

/** A version's note on its row ("flicker fixes and headline cards"), and a way to change it. */
export function VersionNote({ rev, className }: { rev: Pick<RevisionSummary, 'id' | 'number' | 'note'>; className?: string }) {
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useRefreshSources();
  const edit = async () => {
    const note = await dialog.prompt({
      title: `Note on version ${rev.number}`,
      label: 'What this version changed',
      defaultValue: rev.note ?? '',
      maxLength: 80,
      placeholder: 'flicker fixes and headline cards',
      helper: 'Only the note changes; the version itself stays as it is. Leave it empty to remove the note.',
    });
    if (note === null || note.trim() === (rev.note ?? '')) return;
    try {
      await call('revision.note', { id: rev.id, note: note.trim() });
      refresh();
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't change the note", body: err.message });
    }
  };
  return (
    <button
      type="button"
      onClick={() => void edit()}
      aria-label={rev.note ? `Change the note on version ${rev.number}` : `Add a note to version ${rev.number}`}
      title={rev.note ? 'Change the note' : 'Add a note'}
      className={cx('relative z-10 inline-flex shrink-0 items-center gap-1 self-center rounded-[8px] px-2 py-1 text-[12px] text-subtle hover:bg-hover hover:text-fg', className)}
      data-version-note
    >
      <NotePencil size={13} />
      <span className="sr-only sm:not-sr-only">{rev.note ? 'Note' : 'Add a note'}</span>
    </button>
  );
}
