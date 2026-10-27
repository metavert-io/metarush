import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { ArrowsLeftRight, CaretDown, CaretRight, GitBranch, HandGrabbing } from '@phosphor-icons/react';
import { useInstallationNames, useOp } from '../../api/queries';
import type { ProjectShowOut, RevisionSummary } from '../../api/types';
import { absolute, capFirst, plural, stamp, viaLabel } from '../../lib/format';
import { useListNav } from '../../lib/hooks';
import { RevisionRow } from '../../components/Review';
import { UsesList } from '../../components/SourceActions';
import { CutNameBar } from '../../components/sources/CutNames';
import { Button, EmptyState, ErrorPanel, Freshness, SectionTitle, Skeleton } from '../../ui/primitives';

/**
 * What an entry in the project's record did, in words ("approved a version").
 * The action names themselves stay in the activity log for anyone who asks.
 */
const ACTION_WORDS: Record<string, string> = {
  'revision.create': 'saved a new version',
  'revision.approved': 'approved a version',
  'revision.rejected': 'said a version needs changes',
  'revision.askReview': 'asked for a version to be reviewed',
  'render.submit': 'started a render',
  'render.follow': 'got a review copy of the latest version, made by itself',
  'render.follow.set': 'turned review copies of the latest versions on or off',
  'render.cancel': 'canceled a render',
  'render.complete': 'finished a render',
  'qa.run': 'started a quality check',
  'qa.complete': 'finished a quality check',
  'deliver.export': 'exported the finished video',
  'export.timeline': 'exported the cut for another editing app',
  'job.cancel': 'canceled a job',
  'job.retry': 'tried a job again',
  'job.expedite': 'started a job right away',
  'project.create': 'created the project',
  'project.update': 'changed the project',
  'project.link': "started using another project's things",
  'project.use': 'used more of another project',
  'project.unlink': 'stopped using another project',
  'asset.ingest': 'added a clip',
  'asset.duplicate': 'sorted out a copy of a clip',
  'asset.alias': 'kept both copies of a clip',
  'asset.link': 'added a clip to the project',
  'asset.update': 'renamed a clip',
  'asset.name': 'named a clip',
  'asset.fixes.set': "changed a clip's fixes",
  'asset.fixes.switch': 'switched a clip to its fixed copy or back',
  'cut.rename': 'named the cut',
  'revision.note': "changed a version's note",
  'asset.rights': 'recorded where some music comes from',
  'asset.move': 'moved a clip to another project',
  'asset.archive': 'archived a clip',
  'asset.unarchive': 'took a clip out of the Archive',
  'asset.trash': 'moved a clip to the Trash',
  'asset.restore': 'restored a clip from the Trash',
  'asset.delete': 'deleted a clip for good',
  'import.url': 'imported a video from a link',
  'watch.import': 'imported files from the watched folder',
  'footage.describe': 'described the scenes of a clip',
  'footage.faces.name': 'named a face',
  'footage.faces.reject': 'corrected a face',
  'speakers.assign': 'fixed who said a line',
  'speakers.name': 'named a voice',
  'speakers.reject': 'corrected a voice',
  'speakers.detect': 'listened for the voices again',
  'speakers.hint': 'said how many people speak',
  'speakers.unmerge': 'separated two voices',
  'people.create': 'added a person',
  'people.rename': 'renamed a person',
  'people.merge': 'combined two people',
  'people.split': 'separated two voices',
  'people.distinct': 'said two people are different',
  'people.update': 'updated notes about a person',
  'people.rebuild': "rebuilt a person's voice",
  'voiceover.tts': 'made the AI voice',
  'voiceover.scratch': 'made a practice narration',
  'voiceover.take': 'added a narration recording',
  'voiceover.place': 'placed a live take',
  'voiceover.align': 'placed a recording',
  'voiceover.cleanup': 'cleaned up the narration',
  'voiceover.clone': 'copied a voice for the AI voice',
  'sound.isolate': 'made voices clearer',
  'sound.setup': 'set up clearer voices',
  'motion.check': 'checked a motion graphic',
  'resource.add': 'added a logo, picture or font',
  'resource.upload': 'added a logo, picture or font',
  'resource.version': 'added a new version of a logo or picture',
  'resource.update': 'changed a logo or picture',
  'resource.refresh': 'looked at a logo or picture again',
  'resource.remove': 'removed a logo or picture',
  'resource.restore': 'brought back a logo or picture',
  'resource.promote': 'moved a logo or picture to the workspace',
  'resource.fetch': 'brought in a repository or a web page',
  'brand.set': 'changed the brand kit',
  'collection.add': 'added clips to a collection',
  'collection.remove': 'took clips off a collection',
  'todos.answer': 'answered a question',
  'todos.dismiss': 'skipped a question',
  'assistant.action': 'decided on a change Claude suggested',
  'assistant.ask': 'started a conversation with Claude Code',
  'assistant.turn': 'had an answer from Claude Code',
};

/** A change made by hand in the studio (the timeline, or a button that saves one edit). */
const byHand = (r: RevisionSummary) => !!r.origin && (r.origin.startsWith('timeline:') || /^cut-[a-z-]+:studio$/.test(r.origin));

interface Run {
  key: string;
  revs: RevisionSummary[]; // newest first
}

/** Consecutive hand edits by one person (within half an hour of each other) form one run. */
function runsOf(revs: RevisionSummary[]): Run[] {
  const out: Run[] = [];
  for (const r of revs) {
    const last = out[out.length - 1];
    const prev = last?.revs[last.revs.length - 1];
    if (last && prev && byHand(r) && byHand(prev) && r.createdBy.userId === prev.createdBy.userId && Math.abs(new Date(prev.createdAt).getTime() - new Date(r.createdAt).getTime()) < 30 * 60_000) {
      last.revs.push(r);
    } else out.push({ key: r.id, revs: [r] });
  }
  return out;
}

/** "Used in": other projects' cuts that play this cut inside them (D84), each opening that version. */
function PlayedElsewhere({ projectId }: { projectId: string }) {
  const where = useOp('cut.where', { projectId }, { staleTime: 30_000 });
  const uses = where.data?.uses ?? [];
  if (!uses.length) return null;
  return (
    <section aria-labelledby="played-h">
      <SectionTitle id="played-h" meta={where.data?.summary}>
        Played inside other cuts
      </SectionTitle>
      <UsesList uses={uses} />
    </section>
  );
}

export function HistoryTab({ data, highlight }: { data: ProjectShowOut; highlight?: string }) {
  const navigate = useNavigate();
  const machines = useInstallationNames();
  const audit = useOp('audit.list', { projectId: data.project.id, limit: 60 });
  const [picked, setPicked] = useState<string[]>([]);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const revs = data.revisions;
  const runs = useMemo(() => runsOf(revs), [revs]);
  // The rows shown: every revision, except the earlier ones of a closed run of hand edits.
  const shown = useMemo(() => runs.flatMap((run) => (run.revs.length > 1 && !open.has(run.key) ? [run.revs[0]!] : run.revs)), [runs, open]);
  const [active, setActive] = useListNav(shown.length, (i) => shown[i] && navigate(`/p/${data.project.id}/r/${shown[i].id}`));
  // Deep link (?rev=<id>): select that version (opening its run).
  useEffect(() => {
    if (!highlight) return;
    const run = runs.find((x) => x.revs.some((r) => r.id === highlight));
    if (run && run.revs.length > 1 && run.revs[0]!.id !== highlight && !open.has(run.key)) setOpen((s) => new Set(s).add(run.key));
    const i = shown.findIndex((r) => r.id === highlight);
    if (i >= 0) setActive(i);
  }, [highlight, runs, shown, open, setActive]);

  const compare = () => {
    if (picked.length !== 2) return;
    const [a, b] = picked.map((id) => revs.find((r) => r.id === id)!).sort((x, y) => x.number - y.number);
    navigate(`/p/${data.project.id}/compare?left=${a!.id}&right=${b!.id}`);
  };

  const row = (r: RevisionSummary) => (
    <RevisionRow
      key={r.id}
      rev={r}
      to={`/p/${data.project.id}/r/${r.id}`}
      scriptTo={`/p/${data.project.id}?tab=script${r.isHead ? '' : `&rev=${r.id}`}`}
      active={shown.indexOf(r) === active}
      compareChecked={picked.includes(r.id)}
      onCompareToggle={(on) => setPicked((p) => (on ? [...p.filter((x) => x !== r.id), r.id].slice(-2) : p.filter((x) => x !== r.id)))}
      installationName={(id) => machines.get(id)}
    />
  );

  return (
    <div className="grid grid-cols-1 gap-8 xl:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
      <section aria-labelledby="lineage-h">
        <SectionTitle
          id="lineage-h"
          meta={plural(revs.length, 'version')}
          action={
            <Button size="sm" variant={picked.length === 2 ? 'primary' : 'secondary'} disabled={picked.length !== 2} icon={<ArrowsLeftRight size={13} />} onClick={compare}>
              Compare {picked.length === 2 ? 'selected' : `(${picked.length}/2)`}
            </Button>
          }
        >
          Versions
        </SectionTitle>
        <CutNameBar data={data} className="mb-3" />
        {revs.length === 0 ? (
          <div className="panel">
            <EmptyState icon={<GitBranch size={22} />} title="No versions yet" body="Every change, from Claude Code or by hand on the timeline, is saved here as a new version, with who made it and on which machine. Saved versions never change." />
          </div>
        ) : (
          <div className="space-y-1">
            {runs.map((run) => {
              if (run.revs.length === 1) return row(run.revs[0]!);
              const isOpen = open.has(run.key);
              const newest = run.revs[0]!;
              const oldest = run.revs[run.revs.length - 1]!;
              const who = newest.createdBy.userName ?? 'Someone';
              return (
                <div key={run.key} className="rounded-[14px] hairline">
                  <button
                    type="button"
                    aria-expanded={isOpen}
                    onClick={() =>
                      setOpen((s) => {
                        const next = new Set(s);
                        if (next.has(run.key)) next.delete(run.key);
                        else next.add(run.key);
                        return next;
                      })
                    }
                    className="flex w-full items-center gap-2 rounded-[14px] px-3 py-2 text-left text-[12.5px] hover:bg-hover"
                  >
                    {isOpen ? <CaretDown size={13} className="text-subtle" /> : <CaretRight size={13} className="text-subtle" />}
                    <HandGrabbing size={14} className="text-accent-text" />
                    <span className="font-medium text-fg">{plural(run.revs.length, 'change')} by hand</span>
                    <span className="truncate text-subtle" title={`${absolute(oldest.createdAt)} – ${absolute(newest.createdAt)}`}>
                      <span className="text-muted" data-stamp>
                        {stamp(newest.createdAt)}
                      </span>
                      , {who}, r{oldest.number}–r{newest.number}
                    </span>
                  </button>
                  <div className="space-y-1 px-1 pb-1">{(isOpen ? run.revs : [newest]).map(row)}</div>
                  {!isOpen ? (
                    <div className="px-4 pb-2 text-[11.5px] text-subtle">
                      and {plural(run.revs.length - 1, 'earlier change')} ({run.revs.slice(1, 4).map((r) => r.instruction || 'a change').join('; ')}
                      {run.revs.length > 4 ? '…' : ''})
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}
      </section>
      <PlayedElsewhere projectId={data.project.id} />
      <section aria-labelledby="audit-h">
        <SectionTitle id="audit-h" action={<Freshness at={audit.dataUpdatedAt || undefined} fetching={audit.isFetching} onRefresh={() => void audit.refetch()} />}>
          Who did what
        </SectionTitle>
        {audit.isLoading ? (
          <div className="space-y-2">
            {[0, 1, 2, 3, 4].map((i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : audit.error ? (
          <ErrorPanel error={audit.error} onRetry={() => void audit.refetch()} />
        ) : (audit.data?.events.length ?? 0) === 0 ? (
          <div className="rounded-[14px] px-4 py-5 text-[13px] text-muted hairline">No recorded actions yet.</div>
        ) : (
          <ol className="relative space-y-0.5 border-l border-line pl-4">
            {audit.data!.events.map((e) => (
              <li key={e.id} className="relative py-1.5">
                <span className="absolute -left-[21px] top-3 h-2 w-2 rounded-full bg-line-strong" aria-hidden />
                <div className="text-[13px] text-fg">
                  <span className="font-medium">{e.actor.userName ?? 'Someone'}</span> <span className="text-muted">{ACTION_WORDS[e.action] ?? 'made a change'}</span>
                </div>
                <div className="truncate text-[11.5px] text-subtle" title={absolute(e.at)}>
                  {capFirst(viaLabel(e.actor.via))}
                  {machines.get(e.actor.installationId) ? ` on ${machines.get(e.actor.installationId)}` : ''}, {stamp(e.at)}
                </div>
              </li>
            ))}
          </ol>
        )}
      </section>
    </div>
  );
}
