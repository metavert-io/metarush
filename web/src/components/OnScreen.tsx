import { useState } from 'react';
import { Eye, Sparkle, TextT, UserCircle } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../api/queries';
import type { AssetSummary, OpOutput } from '../api/types';
import { relative } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { Badge, Button, cx, EmptyState, ErrorPanel, Skeleton, Spinner } from '../ui/primitives';
import { FaceTile } from './FaceTile';
import { PersonPicker, type PersonChoice } from './PersonPicker';
import { notThemFace } from './CompareDialogs';
import { ownerTitle, useOwnership } from './OwnerOnly';

type Visuals = OpOutput<'footage.visuals'>;
type Face = Visuals['faces'][number];

const INVALIDATE = ['footage.visuals', 'footage.faces', 'todos.list', 'todos.counts', 'people.show', 'people.list', 'notification.list'] as const;

/** M:SS (tenths only when they matter), the way the readout writes times. */
export function clock(t: number): string {
  const r = Math.round(t * 10) / 10;
  const m = Math.floor(r / 60);
  const s = r - m * 60;
  const whole = Math.abs(s - Math.round(s)) < 0.05;
  return `${m}:${whole ? String(Math.round(s)).padStart(2, '0') : s.toFixed(1).padStart(4, '0')}`;
}

/**
 * The clip's "On screen" tab: what the on-device index saw (Apple Vision on a
 * Mac), line by line, the faces and whose they are (only where a person said
 * so; "Is this …?" questions can be answered here), on-screen text, things
 * seen, and scene descriptions Claude Code wrote.
 */
export function OnScreenPanel({ asset, onSeek }: { asset: AssetSummary; onSeek: (t: number) => void }) {
  const visuals = useOp('footage.visuals', { assetId: asset.id });
  const toast = useToast();
  const analyze = useOpMutation('footage.analyze', { invalidate: ['footage.visuals', 'activity.summary'] });
  const v = visuals.data;
  if (visuals.isLoading)
    return (
      <div className="space-y-2">
        <Skeleton className="h-6 w-3/4" />
        <Skeleton className="h-9" />
        <Skeleton className="h-9" />
        <Skeleton className="h-9" />
      </div>
    );
  if (visuals.error) return <ErrorPanel error={visuals.error} onRetry={() => void visuals.refetch()} compact />;
  if (!v) return null;
  const look = () =>
    analyze.mutate(
      { assetId: asset.id, transcribe: false, force: v.state === 'failed' },
      { onSuccess: () => toast({ tone: 'info', title: "Looking at what's on screen", body: 'It runs on a Mac; big jobs wait while the battery is low.' }) },
    );
  return (
    <div data-onscreen>
      {v.state === 'available' ? (
        <>
          <p className="text-[13px] leading-relaxed text-fg" data-onscreen-summary>
            {capFirst(v.summary)}.
          </p>
          <ol className="mt-3 space-y-0.5" aria-label="What's on screen, stretch by stretch">
            {v.lines.map((l) => (
              <li key={l.from}>
                <button type="button" onClick={() => onSeek(l.from)} className="flex w-full items-start gap-3 rounded-[9px] px-2.5 py-2 text-left hover:bg-hover" data-onscreen-line>
                  <span className="tc w-[92px] shrink-0 pt-[1px] text-[12px] text-accent-text">
                    {clock(l.from)}–{clock(l.to)}
                  </span>
                  <span className="min-w-0 text-[12.5px] leading-snug text-muted">{l.line.slice(l.line.indexOf(' ') + 1)}</span>
                </button>
              </li>
            ))}
          </ol>
          <Faces asset={asset} faces={v.faces} onSeek={onSeek} />
          {v.text.length > 0 ? (
            <section className="mt-4" aria-label="On-screen text">
              <h3 className="mb-1.5 flex items-center gap-1.5 text-[12px] font-medium text-muted">
                <TextT size={13} /> Text on screen
              </h3>
              <ul className="space-y-0.5">
                {v.text.map((t, i) => (
                  <li key={i}>
                    <button type="button" onClick={() => onSeek(t.from)} className="flex w-full items-center gap-3 rounded-[9px] px-2.5 py-1.5 text-left hover:bg-hover">
                      <span className="tc w-[92px] shrink-0 text-[12px] text-accent-text">
                        {clock(t.from)}–{clock(t.to)}
                      </span>
                      <span className="truncate text-[12.5px] text-fg">{t.changing ? `${t.text} (keeps changing)` : `“${t.text}”`}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          {v.labels.length > 0 ? (
            <section className="mt-4" aria-label="Seen in the clip">
              <h3 className="mb-1.5 flex items-center gap-1.5 text-[12px] font-medium text-muted">
                <Eye size={13} /> Seen
              </h3>
              <div className="flex flex-wrap gap-1.5">
                {v.labels.slice(0, 18).map((l) => (
                  <button key={l.label} type="button" onClick={() => onSeek(l.first)} title={`About ${Math.round(l.seconds)} s, first at ${clock(l.first)}`}>
                    <Badge tone={l.kind === 'animal' ? 'accent' : l.kind === 'scene' ? 'info' : 'neutral'}>{l.label}</Badge>
                  </button>
                ))}
              </div>
            </section>
          ) : null}
        </>
      ) : v.state === 'queued' || v.state === 'running' ? (
        <div className="flex items-center gap-2 rounded-[10px] bg-panel px-3 py-3 text-[13px] text-muted hairline" role="status">
          <Spinner size={14} /> {v.note}
        </div>
      ) : v.state === 'none' ? (
        <EmptyState compact title="No picture" body="This clip has no video, so there is nothing on screen to look at." />
      ) : v.state === 'unavailable' ? (
        <EmptyState compact icon={<Eye size={20} />} title="Needs a Mac" body="Seeing what's on screen uses Apple Vision, which runs on a Mac. When a Mac in this workspace runs MetaRush, it looks at your clips." />
      ) : (
        <EmptyState
          compact
          icon={<Eye size={20} />}
          title={v.state === 'failed' ? 'Could not look at this clip' : 'Not looked at yet'}
          body={v.state === 'failed' ? v.note : 'MetaRush looks at about one frame a second on this Mac (nothing is uploaded): people and faces, the place, things and animals, and text on screen.'}
          action={
            <Button variant="secondary" size="sm" loading={analyze.isPending} onClick={look}>
              {v.state === 'failed' ? 'Try again' : 'Look now'}
            </Button>
          }
        />
      )}
      <Described asset={asset} described={v.described} onSeek={onSeek} />
      {v.state === 'available' ? <p className="mt-4 text-[11.5px] leading-relaxed text-subtle">MetaRush's guesses, from about one frame a second. Names show only where you confirmed a face.</p> : null}
    </div>
  );
}

function capFirst(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

function Faces({ asset, faces, onSeek }: { asset: AssetSummary; faces: Face[]; onSeek: (t: number) => void }) {
  if (faces.length === 0) return null;
  return (
    <section className="mt-4" aria-label="Faces">
      <h3 className="mb-2 flex items-center gap-1.5 text-[12px] font-medium text-muted">
        <UserCircle size={13} /> Faces
      </h3>
      <ul className="space-y-2">
        {faces.map((f) => (
          <FaceRow key={f.id} asset={asset} face={f} onSeek={onSeek} />
        ))}
      </ul>
    </section>
  );
}

function FaceRow({ asset, face: f, onSeek }: { asset: AssetSummary; face: Face; onSeek: (t: number) => void }) {
  const dialog = useDialog();
  const toast = useToast();
  const answer = useOpMutation('todos.answer', { invalidate: [...INVALIDATE] });
  const reject = useOpMutation('footage.faces.reject', { invalidate: [...INVALIDATE] });
  const name = useOpMutation('footage.faces.name', { invalidate: [...INVALIDATE] });
  const busy = answer.isPending || reject.isPending || name.isPending;
  const done = (o: { done: string }) => toast({ tone: 'success', title: o.done });
  const failed = (title: string) => (e: Error) => toast({ tone: 'error', title, body: e.message });
  const who = async () => {
    const group = !!f.question; // one person's faces in this clip, asked about once (D155)
    const choice = await dialog.open<PersonChoice>((close) => <WhoDialog group={group} onPick={(c) => close(c)} />, { title: 'Whose face is this?', size: 'sm' });
    if (!choice) return;
    if (group) {
      // The answer names all of their faces in this clip.
      if (choice.kind === 'person') answer.mutate({ id: f.question!, answer: choice.person.name, personId: choice.person.id }, { onSuccess: done, onError: failed('Could not name the faces') });
      else if (choice.kind === 'new') answer.mutate({ id: f.question!, answer: choice.name, newPerson: true }, { onSuccess: done, onError: failed('Could not name the faces') });
      return;
    }
    if (choice.kind === 'person') name.mutate({ faceId: f.id, personId: choice.person.id, name: choice.person.name }, { onSuccess: done, onError: failed('Could not name the face') });
    else if (choice.kind === 'new') name.mutate({ faceId: f.id, name: choice.name, newPerson: true }, { onSuccess: done, onError: failed('Could not name the face') });
  };
  const rec = !f.name ? f.recognized : undefined;
  const why = (m: NonNullable<Face['maybe']>) =>
    m.by === 'looks'
      ? `looks like ${m.name} in other clips (${Math.round((m.score ?? 0) * 100)}% alike)`
      : m.by === 'both'
        ? `alone on screen while ${m.name} speaks, and looks like them`
        : m.by === 'shown'
          ? `the video shows the name ${m.name} with this face`
          : m.by === 'said'
            ? `the name ${m.name} is said in the video`
            : m.by === 'aside'
              ? `left out when you named ${m.name}'s other faces`
              : `alone on screen while ${m.name} speaks`;
  return (
    <li
      className={cx('flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-[12px] px-2 py-1.5', rec ? 'border border-dashed border-line' : 'hairline')}
      data-face={f.name || rec?.name || f.maybe?.name || 'unnamed'}
      data-face-recognized={rec ? 'looks' : undefined}
    >
      <button type="button" onClick={() => onSeek(f.at)} aria-label={`Go to ${clock(f.at)}`} className="shrink-0">
        <FaceTile assetId={asset.id} at={f.at} box={f.box} size={52} label={f.name ? `${f.name}'s face` : rec ? `Looks like ${rec.name}` : 'A face'} />
      </button>
      <div className="min-w-0 grow basis-36">
        <div className="truncate text-[13px] font-medium text-fg">{f.name ? f.name : rec ? rec.name : f.maybe ? `Is this ${f.maybe.name}?` : 'Someone'}</div>
        <div className="tc text-[11.5px] text-subtle">
          {clock(f.from)}–{clock(f.to)}
          {rec ? <span className="font-sans"> · recognized by their face, {Math.round(rec.score * 100)}% alike. Not confirmed yet.</span> : null}
          {f.maybe && !f.name && !rec ? <span className="font-sans"> · {why(f.maybe)}</span> : null}
        </div>
      </div>
      <div className="ml-auto flex shrink-0 flex-wrap justify-end gap-1">
        {f.name ? (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => reject.mutate({ faceId: f.id }, { onSuccess: done, onError: failed('Could not change it') })}>
            Not {f.name}?
          </Button>
        ) : rec ? (
          <>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => name.mutate({ faceId: f.id, personId: rec.personId, name: rec.name }, { onSuccess: done, onError: failed('Could not save that') })}>
              That's {rec.name}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void notThemFace(dialog, { faceId: f.id, assetId: asset.id, at: f.at, box: f.box, personId: rec.personId, name: rec.name, score: rec.score })}>
              Not {rec.name}?
            </Button>
          </>
        ) : f.maybe && f.question ? (
          <>
            <Button size="sm" variant="primary" disabled={busy} onClick={() => answer.mutate({ id: f.question!, suggestion: 1 }, { onSuccess: done, onError: failed('Could not save that answer') })}>
              Yes
            </Button>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => answer.mutate({ id: f.question!, answer: 'no' }, { onSuccess: done, onError: failed('Could not save that answer') })}>
              No
            </Button>
          </>
        ) : (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void who()}>
            Who is this?
          </Button>
        )}
      </div>
    </li>
  );
}

function WhoDialog({ onPick, group }: { onPick: (c: PersonChoice) => void; group?: boolean }) {
  const [text, setText] = useState('');
  return (
    <div>
      <p className="mb-3 text-[13px] leading-relaxed text-muted">
        {group ? 'The name goes on this face and the others like it in this clip (one answer for all of them). ' : 'The name shows when this face is on screen in this clip. '}
        MetaRush also learns what they look like from the faces you name, and recognizes them in other clips when it is very sure (you can always say it isn't them).
      </p>
      <PersonPicker label="Whose face is this?" value={text} onValueChange={setText} onPick={onPick} autoFocus placeholder="Type a name" />
    </div>
  );
}

function Described({ asset, described, onSeek }: { asset: AssetSummary; described: Visuals['described']; onSeek: (t: number) => void }) {
  const projectId = asset.projectIds[0];
  const settings = useOp('footage.describe.settings', projectId ? { projectId } : {});
  const dialog = useDialog();
  const toast = useToast();
  const describe = useOpMutation('footage.describe', { invalidate: ['activity.summary', 'footage.visuals'] });
  const configure = useOpMutation('footage.describe.configure', { invalidate: ['footage.describe.settings'] });
  const own = useOwnership();
  const s = settings.data;
  const scenes = described?.scenes ?? [];
  const start = () =>
    describe.mutate(
      { assetId: asset.id, force: scenes.length > 0 },
      {
        onSuccess: (o) => toast({ tone: o.jobs.length ? 'info' : 'error', title: o.jobs.length ? 'Claude Code is describing the scenes' : 'Nothing to describe', body: o.jobs.length ? 'It uses your Claude Code usage.' : o.summary }),
        onError: (e) => toast({ tone: 'error', title: 'Could not start', body: e.message }),
      },
    );
  const turnOn = async () => {
    const ok = await dialog.confirm({
      title: projectId ? 'Describe the scenes in this project?' : 'Describe the scenes of your clips?',
      body: 'Your own Claude Code, on this computer, looks at each new clip and writes a sentence or two per scene, so search finds clips by what happens in them. It uses your Claude Code usage. You can turn it off any time.',
      confirmLabel: 'Turn on',
    });
    if (!ok) return;
    configure.mutate({ projectId, on: true }, { onSuccess: () => toast({ tone: 'success', title: 'Scene descriptions are on' }), onError: (e) => toast({ tone: 'error', title: 'Could not turn them on', body: e.message }) });
  };
  return (
    <section className="mt-5 rounded-[12px] bg-panel px-3 py-3 hairline" aria-label="Scene descriptions" data-described>
      <h3 className="mb-1 flex items-center gap-1.5 text-[12px] font-medium text-muted">
        <Sparkle size={13} /> Scenes, in words
      </h3>
      {scenes.length > 0 ? (
        <>
          <ul className="space-y-0.5">
            {scenes.map((sc, i) => (
              <li key={i}>
                <button type="button" onClick={() => onSeek(sc.from)} className="flex w-full items-start gap-3 rounded-[9px] px-1.5 py-1.5 text-left hover:bg-hover">
                  <span className="tc w-[92px] shrink-0 pt-[1px] text-[12px] text-accent-text">
                    {clock(sc.from)}–{clock(sc.to)}
                  </span>
                  <span className="min-w-0 text-[12.5px] leading-snug text-fg">{sc.text}</span>
                </button>
              </li>
            ))}
          </ul>
          <p className="mt-1 text-[11.5px] text-subtle">Written by Claude Code after looking at the clip{described?.createdAt ? `, ${relative(described.createdAt)}` : ''}.</p>
        </>
      ) : (
        <p className="text-[12.5px] leading-relaxed text-muted">Claude Code can write a sentence or two per scene so search finds this clip by what happens in it. It uses your Claude Code usage.</p>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-2">
        {s && s.on ? (
          <Button size="sm" variant="secondary" loading={describe.isPending} disabled={!s.claudeReady} onClick={start} title={s.claudeReady ? undefined : s.claudeText}>
            {scenes.length ? 'Describe again' : 'Describe the scenes'}
          </Button>
        ) : s ? (
          <Button
            size="sm"
            variant="ghost"
            loading={configure.isPending}
            onClick={() => void turnOn()}
            // A project's own choice is anyone's; the whole workspace's is the owners' (D191).
            disabled={!projectId && !own.owner}
            title={projectId ? undefined : ownerTitle(own, 'turn them on for the whole workspace')}
          >
            {projectId ? 'Turn on for this project' : 'Turn on'}
          </Button>
        ) : null}
        {s && !s.claudeReady ? <span className={cx('text-[11.5px] text-subtle')}>{s.claudeText}</span> : null}
      </div>
    </section>
  );
}
