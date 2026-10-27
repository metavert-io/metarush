import { useEffect, useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { LeaveOut, LeavePlaceOut, LeaveRules, RemoteOp } from '../../api/types';
import { absolute, stamp } from '../../lib/format';
import { Button, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * What may leave (D288–D290): a Content Settings part that says, for every
 * project using these settings (a client), which of their things may leave
 * this computer — footage, voices, faces, words, pictures — and to whom: AI
 * services, abilities, Claude and AI apps, cloud storage, delivery and
 * people outside the workspace (review links, D317). Ticked means it may go. Nothing set keeps everything MetaRush offers. Anyone keeps
 * more here; letting more leave takes a person who is an owner (the server says so). The same
 * op as `metarush content leave set`.
 */

const QUERIES: RemoteOp[] = ['content.leave', 'content.show', 'project.leaves', 'project.receipt'];

const KINDS: { kind: string; label: string }[] = [
  { kind: 'footage', label: 'Footage' },
  { kind: 'voices', label: 'Voices' },
  { kind: 'faces', label: 'Faces' },
  { kind: 'words', label: 'Words' },
  { kind: 'pictures', label: 'Pictures' },
];

type Places = Record<string, { keep: string[]; only: string[] }>;

function placesOf(out: LeaveOut): Places {
  const p: Places = {};
  for (const x of out.places) p[x.place] = { keep: [...x.keep], only: [...x.only] };
  return p;
}

export function LeavePart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const uid = useId();
  const q = useOp('content.leave', { projectId });
  const qc = useQueryClient();
  const toast = useToast();
  const [nothing, setNothing] = useState(false);
  const [places, setPlaces] = useState<Places>({});
  const [note, setNote] = useState('');
  const [saving, setSaving] = useState(false);
  const data = q.data;
  useEffect(() => {
    if (!data) return;
    setNothing(!!data.rules.nothing);
    setPlaces(placesOf(data));
    setNote(data.rules.note ?? '');
  }, [data]);
  if (q.isLoading) return <Skeleton className="h-24" />;
  if (q.error || !data) return <ErrorPanel compact error={q.error} />;
  const may = (place: string, kind: string) => !nothing && !(places[place]?.keep ?? []).includes(kind);
  const toggle = (place: string, kind: string, go: boolean) =>
    setPlaces((p) => {
      const cur = p[place] ?? { keep: [], only: [] };
      const keep = go ? cur.keep.filter((k) => k !== kind) : [...cur.keep.filter((k) => k !== kind), kind];
      return { ...p, [place]: { ...cur, keep } };
    });
  const toggleOnly = (place: string, who: string, on: boolean) =>
    setPlaces((p) => {
      const cur = p[place] ?? { keep: [], only: [] };
      const only = on ? [...cur.only.filter((x) => x !== who), who] : cur.only.filter((x) => x !== who);
      return { ...p, [place]: { ...cur, only } };
    });
  const save = async () => {
    setSaving(true);
    try {
      const rules: LeaveRules = { nothing, note, places: {} };
      for (const [place, v] of Object.entries(places)) {
        if (v.keep.length || v.only.length) rules.places![place] = { keep: v.keep, only: v.only };
      }
      const out = await call('content.leave.set', { projectId, rules, note });
      for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: 'Saved', body: out.summary });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'That change was not saved', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="space-y-3" data-leave-part>
      <p className="text-[12.5px] leading-relaxed text-muted">
        What of every project using these settings may leave this computer, and to whom: for a client whose footage can’t go to the cloud, or whose transcripts Claude
        mustn’t read. Every place that sends something asks first, refuses plainly, and the project’s receipt lists what left. Anyone can keep more here; letting more
        leave takes an owner.
      </p>
      {data.problems.length ? (
        <ul className="space-y-1 rounded-[10px] bg-warn/10 p-2.5 text-[12.5px] text-fg" data-leave-problems>
          {data.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ) : null}
      <fieldset disabled={readOnly} className="min-w-0 space-y-3">
        <label className="flex items-start gap-2 text-[13px] text-fg">
          <input type="checkbox" checked={nothing} onChange={(e) => setNothing(e.target.checked)} className="mt-[3px]" data-leave-nothing />
          <span>
            Nothing leaves this computer
            <span className="block text-[12px] text-muted">No AI services, abilities, Claude, cloud storage or delivery for any of it.</span>
          </span>
        </label>
        {!nothing ? <p className="text-[12.5px] text-muted">Ticked may go there; untick what stays on this computer.</p> : null}
        {!nothing ? (
          <ul className="divide-y divide-line rounded-[12px] hairline" data-leave-grid>
            {data.places.map((p) => (
              <PlaceRow key={p.place} p={p} may={may} toggle={toggle} />
            ))}
          </ul>
        ) : null}
        {!nothing
          ? data.places
              .filter((p) => p.receivers.length > 0)
              .map((p) => (
                <div key={p.place} className="text-[12.5px]" data-leave-only={p.place}>
                  <div className="mb-1 text-muted">{p.title}: only these may receive them (none ticked: any)</div>
                  <div className="flex flex-wrap gap-x-4 gap-y-1">
                    {p.receivers.map((r) => (
                      <label key={r.name} className="flex items-center gap-1.5 text-fg">
                        <input type="checkbox" checked={(places[p.place]?.only ?? []).includes(r.name)} onChange={(e) => toggleOnly(p.place, r.name, e.target.checked)} data-leave-receiver={`${p.place}:${r.name}`} />
                        {r.title}
                      </label>
                    ))}
                  </div>
                </div>
              ))
          : null}
        <div>
          <label htmlFor={`${uid}-note`} className="mb-1 block text-[12.5px] text-muted">
            Why (shown when something is kept here)
          </label>
          <input
            id={`${uid}-note`}
            value={note}
            maxLength={500}
            onChange={(e) => setNote(e.target.value)}
            placeholder="The client's contract"
            className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent"
            data-leave-note
          />
        </div>
        <div className="flex flex-wrap items-center gap-3">
          {!readOnly ? (
            <Button size="sm" variant="secondary" loading={saving} onClick={() => void save()} data-leave-save>
              Save
            </Button>
          ) : null}
          {data.setBy ? (
            <span className="text-[12px] text-subtle" title={data.setAt ? absolute(data.setAt) : undefined}>
              Set by <Theirs>{data.setBy}</Theirs>
              {data.setAt ? `, ${stamp(data.setAt)}` : ''}
            </span>
          ) : null}
        </div>
      </fieldset>
    </div>
  );
}

function PlaceRow({ p, may, toggle }: { p: LeavePlaceOut; may: (place: string, kind: string) => boolean; toggle: (place: string, kind: string, go: boolean) => void }) {
  return (
    <li className="grid grid-cols-1 gap-2 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start sm:gap-4" data-leave-place={p.place}>
      <div className="min-w-0">
        <div className="text-[13px] font-medium text-fg">{p.title}</div>
        <div className="text-[11.5px] leading-snug text-subtle">{p.says}</div>
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-1.5 text-[12.5px]" role="group" aria-label={`What may go to ${p.title}`}>
        {KINDS.map((k) =>
          p.kinds.includes(k.kind) ? (
            <label key={k.kind} className="flex items-center gap-1.5 text-fg">
              <input type="checkbox" checked={may(p.place, k.kind)} onChange={(e) => toggle(p.place, k.kind, e.target.checked)} data-leave-cell={`${p.place}:${k.kind}`} />
              {k.label}
            </label>
          ) : null,
        )}
      </div>
    </li>
  );
}
