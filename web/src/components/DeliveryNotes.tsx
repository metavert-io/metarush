import { useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { NotePencil, Sparkle, Trash } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { ops } from '@metarush/schema';
import { askInProject } from '../layout/assistant/store';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, CopyButton, ErrorPanel, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * A version's delivery notes (D237): its title, other titles, description,
 * chapters and show notes. Claude Code writes them on the person's own Claude
 * plan (Ask Claude to write them opens the panel with the request; its card
 * keeps them), or a person writes them here. Delivery writes them next to the
 * video; the version itself never changes.
 */

type Notes = ops.DeliveryNotes;

const fieldClass =
  'w-full rounded-[10px] bg-panel px-3 py-2 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';

/** "1:05", "1:02:03" → seconds (the chapter lines people type). */
export function parseClock(v: string): number | undefined {
  const parts = v.trim().split(':');
  if (parts.length < 2 || parts.length > 3) return undefined;
  let total = 0;
  for (let i = 0; i < parts.length; i++) {
    const x = Number(parts[i]);
    if (!Number.isFinite(x) || x < 0 || (i > 0 && x >= 60) || parts[i] === '') return undefined;
    total = total * 60 + x;
  }
  return total;
}

/** "0:00 Intro" lines → chapters, or the first line that isn't one. */
export function parseChapters(text: string): { chapters: ops.Chapter[]; bad?: string } {
  const chapters: ops.Chapter[] = [];
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    const m = /^(?:[-*]\s+)?\(?(\d{1,2}(?::\d{1,2}){1,2}(?:\.\d+)?)\)?\s*(?:[-–—:|]\s*)?(.+)$/.exec(line);
    const at = m?.[1] !== undefined ? parseClock(m[1]) : undefined;
    if (!m || at === undefined) return { chapters, bad: line };
    chapters.push({ at, title: (m[2] ?? '').trim() });
  }
  return { chapters };
}

/** Seconds as a description lists chapters: 0:00, 1:05, 1:02:03. */
export function chapterClock(sec: number): string {
  const s = Math.floor(Math.max(0, sec) + 1e-6);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

export function DeliveryNotes({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const notes = useOp('delivery.notes', { projectId, revision: revisionId });
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: ['delivery.notes'] });
  const n = notes.data?.notes;

  const edit = async () => {
    const out = await dialog.open<string>((close) => <NotesForm projectId={projectId} revisionId={revisionId} notes={n} onClose={close} />, {
      title: `Delivery notes of version ${number}`,
      size: 'lg',
    });
    if (out) {
      refresh();
      toast({ tone: 'success', title: out });
    }
  };
  const remove = async () => {
    const ok = await dialog.confirm({
      title: `Take the delivery notes off version ${number}?`,
      body: 'Its title, description, chapters and show notes are removed. The version itself stays as it is.',
      confirmLabel: 'Take them off',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('delivery.notes.set', { projectId, revision: revisionId, notes: {}, clear: true });
      refresh();
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Not removed', body: toApiError(e).message });
    }
  };
  const ask = () =>
    askInProject(
      projectId,
      `Write the delivery notes for version ${number}: a title and two others to choose from, a description, chapters and show notes.`,
    );

  return (
    <section aria-label="Delivery notes" className="mt-8" data-delivery-notes>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-[14px] font-semibold">Delivery notes</h2>
        {n ? (
          <Badge tone={n.writtenBy === 'person' ? 'neutral' : 'accent'}>
            {n.writtenBy === 'claude' ? 'Written by Claude' : n.writtenBy === 'agent' ? 'Written by an agent' : 'Written by a person'}
          </Badge>
        ) : null}
        <div className="ml-auto flex flex-wrap gap-1.5">
          <Button size="sm" variant="ghost" icon={<Sparkle size={13} />} onClick={ask} data-notes-ask>
            {n ? 'Ask Claude for new ones' : 'Ask Claude to write them'}
          </Button>
          <Button size="sm" variant="secondary" icon={<NotePencil size={13} />} onClick={() => void edit()} data-notes-edit>
            {n ? 'Edit…' : 'Write them…'}
          </Button>
          {n ? (
            <>
              <CopyButton text={notes.data?.markdown ?? ''} label="Copy all" />
              <Button size="sm" variant="ghost" icon={<Trash size={13} />} onClick={() => void remove()} aria-label="Take the delivery notes off">
                Remove
              </Button>
            </>
          ) : null}
        </div>
      </div>
      {notes.isLoading ? (
        <Skeleton className="h-16" />
      ) : notes.error ? (
        <ErrorPanel error={notes.error} compact onRetry={() => void notes.refetch()} />
      ) : !n ? (
        <p className="text-[13px] text-muted">
          No title, description or chapters yet. They go out with the video when it is delivered, ready to paste where you post it.
        </p>
      ) : (
        <div className="space-y-4 rounded-[12px] px-4 py-3 text-[13px] hairline" data-notes-shown>
          {n.title ? (
            <div>
              <div className="text-[11.5px] font-medium uppercase tracking-wide text-subtle">Title</div>
              <div className="text-[15px] font-semibold">
                <Theirs>{n.title}</Theirs>
              </div>
              {(n.titles ?? []).length > 0 ? (
                <ul className="mt-1 space-y-0.5 text-muted">
                  {(n.titles ?? []).map((t) => (
                    <li key={t}>
                      or <Theirs>{t}</Theirs>
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}
          {n.description ? (
            <div>
              <div className="text-[11.5px] font-medium uppercase tracking-wide text-subtle">Description</div>
              <p className="whitespace-pre-wrap">
                <Theirs>{n.description}</Theirs>
              </p>
            </div>
          ) : null}
          {(n.chapters ?? []).length > 0 ? (
            <div>
              <div className="text-[11.5px] font-medium uppercase tracking-wide text-subtle">Chapters</div>
              <ol className="space-y-0.5">
                {(n.chapters ?? []).map((c) => (
                  <li key={c.at} className="flex gap-3">
                    <span className="w-14 shrink-0 tabular-nums text-muted">{chapterClock(c.at)}</span>
                    <Theirs>{c.title}</Theirs>
                  </li>
                ))}
              </ol>
            </div>
          ) : null}
          {n.showNotes ? (
            <div>
              <div className="text-[11.5px] font-medium uppercase tracking-wide text-subtle">Show notes</div>
              <p className="whitespace-pre-wrap">
                <Theirs>{n.showNotes}</Theirs>
              </p>
            </div>
          ) : null}
          {(notes.data?.warnings ?? []).map((w) => (
            <p key={w} className="text-[12.5px] text-info">
              {w}
            </p>
          ))}
        </div>
      )}
    </section>
  );
}

function NotesForm({ projectId, revisionId, notes, onClose }: { projectId: string; revisionId: string; notes?: Notes; onClose: (v?: string) => void }) {
  const uid = useId();
  const [title, setTitle] = useState(notes?.title ?? '');
  const [titles, setTitles] = useState((notes?.titles ?? []).join('\n'));
  const [description, setDescription] = useState(notes?.description ?? '');
  const [chapters, setChapters] = useState((notes?.chapters ?? []).map((c) => `${chapterClock(c.at)} ${c.title}`).join('\n'));
  const [showNotes, setShowNotes] = useState(notes?.showNotes ?? '');
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const save = async () => {
    const parsed = parseChapters(chapters);
    if (parsed.bad !== undefined) {
      setError(`MetaRush can't read the chapter “${parsed.bad}”: write a time and a name, like 1:05 The garden.`);
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      const out = await call('delivery.notes.set', {
        projectId,
        revision: revisionId,
        writtenBy: 'person',
        notes: {
          title,
          titles: titles
            .split('\n')
            .map((t) => t.trim())
            .filter(Boolean),
          description,
          chapters: parsed.chapters,
          showNotes,
        },
      });
      onClose(out.summary);
    } catch (e) {
      setError(toApiError(e).message);
    } finally {
      setBusy(false);
    }
  };
  const label = 'mb-1.5 block text-[12.5px] font-medium text-muted';
  return (
    <div className="space-y-4" data-notes-form>
      <div>
        <label htmlFor={`${uid}-t`} className={label}>
          Title
        </label>
        <input id={`${uid}-t`} value={title} maxLength={100} onChange={(e) => setTitle(e.target.value)} className={`${fieldClass} h-9`} />
      </div>
      <div>
        <label htmlFor={`${uid}-o`} className={label}>
          Other titles to choose from (one a line)
        </label>
        <textarea id={`${uid}-o`} rows={2} value={titles} onChange={(e) => setTitles(e.target.value)} className={fieldClass} />
      </div>
      <div>
        <label htmlFor={`${uid}-d`} className={label}>
          Description
        </label>
        <textarea id={`${uid}-d`} rows={4} maxLength={5000} value={description} onChange={(e) => setDescription(e.target.value)} className={fieldClass} />
      </div>
      <div>
        <label htmlFor={`${uid}-c`} className={label}>
          Chapters (a time into the video and a name, one a line; the first at 0:00)
        </label>
        <textarea id={`${uid}-c`} rows={4} value={chapters} placeholder={'0:00 Welcome\n1:05 The garden'} onChange={(e) => setChapters(e.target.value)} className={`${fieldClass} tabular-nums`} />
      </div>
      <div>
        <label htmlFor={`${uid}-s`} className={label}>
          Show notes
        </label>
        <textarea id={`${uid}-s`} rows={5} value={showNotes} onChange={(e) => setShowNotes(e.target.value)} className={fieldClass} />
      </div>
      {error ? (
        <p className="text-[12.5px] text-danger" role="alert">
          {error}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()}>
          Cancel
        </Button>
        <Button variant="primary" loading={busy} onClick={() => void save()} data-notes-save>
          Save notes
        </Button>
      </div>
    </div>
  );
}
