import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { useNavigate } from 'react-router';
import { UsersThree } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import type { AssetSummary, CallDetectOut, ProjectShowOut } from '../api/types';
import { uploads, useUploads } from '../api/uploads';
import { markJobSeen } from '../lib/workbench';
import { useDialog } from '../ui/Dialog';
import { Button, cx } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * Making a conversation (D99): from recordings already in a project (a
 * dialog: which recordings, whose they are), or from what was just added — a
 * Zoom, Riverside or SquadCast folder, or several recordings — when the Add
 * box recognizes a call ("Make a conversation from these"). Both end in
 * conversation.create; the Conversation tab opens on the result.
 */

type Use = 'both' | 'sound' | 'picture';

interface Row {
  assetId: string;
  title: string;
  on: boolean;
  name: string;
  use: Use;
}

function guessUse(a: AssetSummary): Use {
  if (a.kind === 'audio' || !a.width) return 'sound';
  if (!a.hasAudio) return 'picture';
  return 'both';
}

function personName(title: string): string {
  const skip = new Set(['cam', 'camera', 'webcam', 'audio', 'video', 'mic', 'track', 'raw', 'recording', 'take']);
  const words = title
    .replace(/[_\-.]+/g, ' ')
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .split(/\s+/)
    .filter((w) => w && !skip.has(w.toLowerCase()) && !/^\d+$/.test(w));
  return (words.length ? words : [title]).map((w) => w[0]!.toUpperCase() + w.slice(1)).join(' ');
}

export function MakeConversationButton({ data, variant = 'primary' }: { data: ProjectShowOut; variant?: 'primary' | 'ghost' }) {
  const dialog = useDialog();
  const recordings = data.assets.filter((a) => (a.kind === 'video' || a.kind === 'audio') && a.duration > 0);
  if (recordings.length < 2) return null;
  const open = () =>
    void dialog.open<true>((close) => <MakeForm data={data} recordings={recordings} onDone={() => close(true)} onCancel={() => close()} />, { title: 'Make a conversation', size: 'md' });
  return (
    <Button variant={variant} size={variant === 'ghost' ? 'sm' : 'md'} icon={<UsersThree size={variant === 'ghost' ? 13 : 15} />} onClick={open}>
      {variant === 'ghost' ? 'Make another' : 'Make a conversation'}
    </Button>
  );
}

function MakeForm({ data, recordings, onDone, onCancel }: { data: ProjectShowOut; recordings: AssetSummary[]; onDone: () => void; onCancel: () => void }) {
  const [rows, setRows] = useState<Row[]>(() => recordings.map((a) => ({ assetId: a.id, title: a.title || a.name, on: false, name: personName(a.title || a.name), use: guessUse(a) })));
  const [name, setName] = useState('');
  const [together, setTogether] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const create = useCreate(data.project.id);
  const chosen = rows.filter((r) => r.on);
  const patch = (i: number, p: Partial<Row>) => setRows((rs) => rs.map((r, k) => (k === i ? { ...r, ...p } : r)));
  const submit = async () => {
    setBusy(true);
    setProblem(undefined);
    const err = await create(
      chosen.map((r) => ({ assetId: r.assetId, name: r.use === 'picture' ? undefined : r.name, use: r.use })),
      { name: name.trim() || undefined, align: together ? 'together' : 'audio' },
    );
    setBusy(false);
    if (err) setProblem(err);
    else onDone();
  };
  return (
    <div className="space-y-4">
      <p className="text-[13px] leading-relaxed text-muted">Pick the recordings of one call: one per person, or each person's sound and the meeting's video. MetaRush lines them up by their sound.</p>
      <ul className="max-h-[46dvh] space-y-2 overflow-y-auto pr-1" aria-label="Recordings">
        {rows.map((r, i) => (
          <li key={r.assetId} className={cx('rounded-[12px] p-2.5 hairline', r.on ? 'bg-accent-soft/60' : 'bg-panel')}>
            <label className="flex min-w-0 items-center gap-2.5">
              <input type="checkbox" checked={r.on} onChange={(e) => patch(i, { on: e.target.checked })} className="h-4 w-4 accent-[var(--accent)]" />
              <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{r.title}</span>
            </label>
            {r.on ? (
              <div className="mt-2 flex flex-wrap items-center gap-2 pl-6">
                {r.use !== 'picture' ? (
                  <input
                    aria-label={`Who is in ${r.title}`}
                    value={r.name}
                    onChange={(e) => patch(i, { name: e.target.value })}
                    className="h-8 min-w-0 flex-1 rounded-[9px] bg-panel px-2 text-[13px] hairline"
                    placeholder="Their name"
                  />
                ) : null}
                <select aria-label={`What ${r.title} gives`} value={r.use} onChange={(e) => patch(i, { use: e.target.value as Use })} className="h-8 rounded-[9px] bg-panel px-2 text-[12.5px] hairline">
                  <option value="both">Picture and sound</option>
                  <option value="sound">Sound only</option>
                  <option value="picture">The meeting's video</option>
                </select>
              </div>
            ) : null}
          </li>
        ))}
      </ul>
      <div className="grid gap-2 sm:grid-cols-2">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name (default: the people's names)" aria-label="The conversation's name" className="h-9 rounded-[10px] bg-panel px-2.5 text-[13px] hairline" />
        <label className="flex items-center gap-2 text-[12.5px] text-muted">
          <input type="checkbox" checked={together} onChange={(e) => setTogether(e.target.checked)} className="h-4 w-4 accent-[var(--accent)]" />
          They start together (the app recorded them so)
        </label>
      </div>
      {problem ? (
        <p className="text-[12.5px] text-danger" role="alert">
          {problem}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="primary" disabled={chosen.length < 2} loading={busy} onClick={() => void submit()}>
          {chosen.length < 2 ? 'Pick two or more' : `Line up ${chosen.length} recordings`}
        </Button>
      </div>
    </div>
  );
}

/** conversation.create, then the Conversation tab; returns the problem in plain words (or undefined). */
function useCreate(projectId: string) {
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  return async (
    members: { assetId: string; name?: string; use?: Use }[],
    o: { name?: string; align?: 'audio' | 'together'; source?: { provider: 'zoom' | 'riverside' | 'squadcast' | 'folder'; folder?: string; aligned?: boolean } },
  ): Promise<string | undefined> => {
    try {
      const out = await call('conversation.create', { projectId, members, ...(o.name ? { name: o.name } : {}), align: o.align ?? 'audio', ...(o.source ? { source: o.source } : {}) });
      markJobSeen(out.jobId);
      for (const k of ['conversation.list', 'conversation.show', 'project.show']) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.summary });
      navigate(`/p/${projectId}?tab=conversation`);
      return undefined;
    } catch (e) {
      const err = toApiError(e);
      return [err.message, err.hint].filter(Boolean).join(' ');
    }
  };
}

/*
 * The last files added together, per project (the Add box and drops anywhere
 * share them; the Add box that shows the offer may not be the one that took
 * the files: an empty project's box gives way to the list's).
 */
const batches = new Map<string, string[]>();
const batchSubs = new Set<() => void>();
const NONE: string[] = [];

function setBatch(projectId: string | undefined, keys: string[]) {
  batches.set(projectId ?? '', keys);
  batchSubs.forEach((fn) => fn());
}

export function useBatch(projectId: string | undefined): string[] {
  return useSyncExternalStore(
    (fn) => {
      batchSubs.add(fn);
      return () => batchSubs.delete(fn);
    },
    () => batches.get(projectId ?? '') ?? NONE,
  );
}

/** Starts uploads and remembers them together when there are two or more (a call's recordings). */
export function addFiles(files: { file: File; path?: string }[], target: { projectId?: string; label: string }) {
  const before = new Set(uploads.getSnapshot().map((u) => u.key));
  const done = uploads.add(files, target);
  if (files.length >= 2) setBatch(target.projectId, uploads.getSnapshot().filter((u) => !before.has(u.key)).map((u) => u.key));
  return done;
}

/*
 * The Add box's card: when a batch of added files looks like one call, offer
 * to make a conversation from it once the files are in.
 */
export function ConversationFromUploads({ projectId }: { projectId?: string }) {
  const batch = useBatch(projectId);
  const items = useUploads();
  const [detected, setDetected] = useState<CallDetectOut>();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const [dismissed, setDismissed] = useState(false);
  const create = useCreate(projectId ?? '');
  const mine = useMemo(() => items.filter((u) => batch.includes(u.key)), [items, batch]);
  const finished = mine.length > 0 && mine.every((u) => ['done', 'skipped', 'aliased', 'failed', 'canceled'].includes(u.state));
  const asked = useRef('');

  useEffect(() => {
    if (!projectId || !finished) return;
    const files = mine
      .map((u) => ({ name: u.name, assetId: u.result?.assetId ?? u.duplicate?.assetId }))
      .filter((f): f is { name: string; assetId: string } => !!f.assetId);
    const key = files.map((f) => f.assetId).join(',');
    if (files.length < 2 || asked.current === key) return;
    asked.current = key;
    call('conversation.detect', { files })
      .then((d) => setDetected(d.found ? d : undefined))
      .catch(() => setDetected(undefined));
  }, [finished, mine, projectId]);

  if (!projectId || !detected || dismissed || !batch.length) return null;
  const make = async () => {
    setBusy(true);
    setProblem(undefined);
    const members = detected.members
      .filter((m) => !m.skip && m.assetId)
      .map((m) => ({ assetId: m.assetId!, name: m.person || undefined, use: (m.use || 'both') as Use }));
    const err = await create(members, {
      name: detected.name,
      align: detected.aligned ? 'together' : 'audio',
      source: detected.provider ? { provider: detected.provider, folder: detected.folder, aligned: detected.aligned } : undefined,
    });
    setBusy(false);
    if (err) setProblem(err);
    else {
      setDismissed(true);
      setBatch(projectId, []);
    }
  };
  return (
    <div className="mt-4 flex flex-col gap-3 rounded-[14px] bg-accent-soft p-3.5 sm:flex-row sm:items-center" role="status" data-conversation-offer>
      <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-accent text-accent-ink">
        <UsersThree size={17} weight="bold" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-medium text-fg">These look like one call</div>
        <div className="text-[12.5px] leading-relaxed text-muted">{detected.text}</div>
        {problem ? (
          <div className="mt-1 text-[12.5px] text-danger" role="alert">
            {problem}
          </div>
        ) : null}
      </div>
      <div className="flex shrink-0 gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            setDismissed(true);
            setBatch(projectId, []);
          }}
        >
          Not now
        </Button>
        <Button variant="primary" size="sm" loading={busy} onClick={() => void make()}>
          Make a conversation from these
        </Button>
      </div>
    </div>
  );
}
