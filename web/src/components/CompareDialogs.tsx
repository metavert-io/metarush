import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { WarningCircle, CheckCircle, Sparkle } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { LearnedFace, LearnedVoice, OpInput, PersonLearnedOut, RemoteOp } from '../api/types';
import type { DialogApi } from '../ui/Dialog';
import { Badge, Button, cx, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { FaceTile } from './FaceTile';
import { PersonPicker, type PersonChoice } from './PersonPicker';
import { SampleTile } from './SampleTile';

/*
 * Hearing and seeing what a name was learned from (D158). Before "Is this
 * really Maya?" is answered, the person hears this voice next to the voices
 * Maya's voice was learned from (or, for "Not Maya?" on a face, sees the
 * faces hers was learned from), each with how alike it is, the odd one marked,
 * and "Not Maya" on each: the wrong one is taken back in place, her voice or
 * face is learned again at once, and the warning is worked out again.
 */

const QUERIES: RemoteOp[] = ['footage.speakers', 'footage.transcript', 'footage.faces', 'footage.visuals', 'todos.counts', 'todos.list', 'todos.groups', 'people.list', 'people.show', 'notification.list'];

/** What a naming that needs confirming carries (the E_CONFIRM_REQUIRED details). */
export interface ConfirmDetails {
  reason?: string;
  name?: string;
  other?: string;
  differ?: boolean;
  personId?: string;
  assetId?: string;
  speaker?: string;
  otherSpeaker?: string;
}

/** false: nothing changes; 'same': yes, the same person (name it with samePerson); 'again': the conflict was fixed, name it again. */
export type Confirmed = false | 'same' | 'again';

const pct = (s?: number) => Math.round((s ?? 0) * 100);

/** The person picker with its own text (who it is instead). */
function InlinePicker(props: { label: string; onPick: (c: PersonChoice) => void; exclude?: string[]; allowUnsure?: boolean; autoFocus?: boolean; placeholder?: string }) {
  const [text, setText] = useState('');
  return <PersonPicker {...props} value={text} onValueChange={setText} />;
}

function unlearnInput(choice: PersonChoice): Partial<OpInput<'people.unlearn'>> {
  switch (choice.kind) {
    case 'person':
      return { otherId: choice.person.id, name: choice.person.name };
    case 'new':
      return { name: choice.name, newPerson: true };
  }
  return { unsure: true };
}

/**
 * "Is this really Maya?" (voices_differ) or "Both voices in this clip will
 * be Maya" (same_name_in_clip), with the voices to hear and what to do.
 */
export function confirmSamePerson(dialog: DialogApi, d: ConfirmDetails): Promise<Confirmed> {
  const same = d.reason === 'same_name_in_clip';
  const title = same ? `Both voices in this clip will be ${d.name}` : `Is this really ${d.name}?`;
  return dialog
    .open<Confirmed>((close) => (same ? <SameNameBody d={d} close={close} /> : <VoicesDifferBody d={d} close={close} />), { title, size: same ? 'md' : 'lg' })
    .then((v) => v ?? false);
}

function useRefresh() {
  const qc = useQueryClient();
  return () => {
    for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
}

function VoicesDifferBody({ d, close }: { d: ConfirmDetails; close: (v?: Confirmed) => void }) {
  const name = d.name ?? 'them';
  const canCompare = !!(d.personId && d.assetId && d.speaker);
  const learned = useOp('people.learned', canCompare ? { id: d.personId!, assetId: d.assetId!, speaker: d.speaker! } : null);
  const [fixed, setFixed] = useState<PersonLearnedOut | undefined>();
  const out = fixed ?? learned.data;
  const still = out ? out.still : !!d.differ;
  const [fixing, setFixing] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const refresh = useRefresh();
  const unlearn = async (v: LearnedVoice, choice: PersonChoice) => {
    setBusy(true);
    try {
      const res = await call('people.unlearn', { id: d.personId!, assetId: v.assetId, speaker: v.label, againstAssetId: d.assetId, againstSpeaker: d.speaker, ...unlearnInput(choice) });
      setFixed(res);
      setFixing(null);
      refresh();
      toast({ tone: 'success', title: res.done });
    } catch (err) {
      const e = toApiError(err);
      toast({ tone: 'error', title: 'Could not take it back', body: [e.message, e.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  const voice = out?.voice;
  return (
    <div data-compare-voices>
      {still ? (
        <p className="flex items-start gap-2 rounded-[10px] bg-accent-soft px-3 py-2 text-[13.5px] leading-relaxed text-accent-text" data-pitch-warning>
          <WarningCircle size={16} className="mt-[2px] shrink-0" /> These two voices sound quite different — one is much lower. Listen to them first.
        </p>
      ) : fixed ? (
        <p className="flex items-start gap-2 rounded-[10px] bg-ok-soft px-3 py-2 text-[13.5px] leading-relaxed text-ok" data-warning-gone>
          <CheckCircle size={16} weight="fill" className="mt-[2px] shrink-0" />
          {fixed.voices.length > 0 ? `This voice sounds like ${name}'s other voices now.` : `Nothing else is learned as ${name}'s voice now: this one would be the first.`}
        </p>
      ) : null}
      <p className="mt-3 text-[13px] leading-relaxed text-muted">
        {name}'s voice is learned from the clips you named. If one of them is someone else, say who it is: {name}'s voice is learned again right away.
      </p>
      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-[auto_1fr]">
        <section aria-label="This voice" className="sm:w-[176px]">
          <h3 className="mb-1.5 text-[12px] font-semibold text-fg">This voice</h3>
          {voice?.sample && d.assetId ? (
            <SampleTile assetId={d.assetId} from={voice.sample.from} to={voice.sample.to} hasVideo={voice.hasVideo} label="This voice" size="md" />
          ) : learned.isLoading ? (
            <Skeleton className="h-[90px] w-[160px] rounded-[12px]" />
          ) : null}
        </section>
        <section aria-label={`What ${name}'s voice is learned from`} data-learned-voices>
          <h3 className="mb-1.5 text-[12px] font-semibold text-fg">{name}'s voice, as you named it</h3>
          {learned.isLoading && !out ? (
            <Skeleton className="h-24 rounded-[12px]" />
          ) : !out || out.voices.length === 0 ? (
            <p className="text-[12.5px] text-subtle">Not learned from any other clip now.</p>
          ) : (
            <ul className="space-y-2">
              {out.voices.map((v) => {
                const key = `${v.assetId}/${v.label}`;
                return (
                  <li key={key} className={cx('rounded-[12px] p-2', v.odd ? 'bg-danger-soft' : 'bg-panel hairline')} data-learned-voice data-odd={v.odd || undefined}>
                    <div className="flex items-start gap-3">
                      {v.sample ? <SampleTile assetId={v.assetId} from={v.sample.from} to={v.sample.to} hasVideo={v.hasVideo} label={`${name} in ${v.assetTitle}`} size="sm" /> : null}
                      <div className="min-w-0 flex-1">
                        <Theirs className="block truncate text-[12.5px] font-medium text-fg">{v.assetTitle}</Theirs>
                        <span className="block text-[11.5px] text-muted">
                          {v.speaker}
                          {v.likeThis != null ? ` · ${pct(v.likeThis)}% like this voice` : ''}
                        </span>
                        {v.odd ? (
                          <span className="mt-1 flex items-start gap-1 text-[11.5px] font-medium leading-snug text-danger" data-odd-why>
                            <WarningCircle size={12} weight="fill" className="mt-[2px] shrink-0" />
                            <span>{v.why}</span>
                          </span>
                        ) : null}
                        <Button size="sm" variant={v.odd ? 'secondary' : 'ghost'} className="mt-1.5" disabled={busy} onClick={() => setFixing(fixing === key ? null : key)}>
                          Not {name}
                        </Button>
                      </div>
                    </div>
                    {fixing === key ? (
                      <div className="mt-2">
                        <InlinePicker label={`Who is it, if not ${name}?`} onPick={(c) => void unlearn(v, c)} exclude={d.personId ? [d.personId] : []} allowUnsure autoFocus placeholder="Who is it really?" />
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      </div>
      <div className="mt-6 flex flex-wrap justify-end gap-2">
        {/* The safe answer is the default while the voices differ: nothing is learned unless the person says so. Once
            the mistake is taken back and they don't, finishing the answer is. */}
        <Button variant={fixed && !still ? 'secondary' : 'primary'} data-autofocus={fixed && !still ? undefined : true} data-safe-choice onClick={() => close(false)}>
          No, let me pick another name
        </Button>
        <Button variant={fixed && !still ? 'primary' : 'secondary'} data-autofocus={fixed && !still ? true : undefined} disabled={busy} onClick={() => close(still ? 'same' : 'again')}>
          Yes, it's {name}
        </Button>
      </div>
    </div>
  );
}

function SameNameBody({ d, close }: { d: ConfirmDetails; close: (v?: Confirmed) => void }) {
  const name = d.name ?? 'them';
  const sp = useOp('footage.speakers', d.assetId ? { assetId: d.assetId } : null);
  const voices = sp.data?.speakers ?? [];
  const mine = voices.find((v) => v.label === d.speaker);
  const other = voices.find((v) => v.label === d.otherSpeaker);
  const [renaming, setRenaming] = useState(false);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const refresh = useRefresh();
  const hasVideo = true;
  const renameOther = async (choice: PersonChoice) => {
    if (!d.assetId || !d.otherSpeaker || choice.kind === 'unsure') return;
    setBusy(true);
    try {
      await call('footage.speakers.rename', choice.kind === 'person'
        ? { assetId: d.assetId, speaker: d.otherSpeaker, personId: choice.person.id, name: choice.person.name }
        : { assetId: d.assetId, speaker: d.otherSpeaker, name: choice.name, newPerson: true });
      refresh();
      close('again');
    } catch (err) {
      const e = toApiError(err);
      toast({ tone: 'error', title: 'Could not rename the other voice', body: [e.message, e.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div data-same-name>
      {d.differ ? (
        <p className="rounded-[10px] bg-accent-soft px-3 py-2 text-[13.5px] leading-relaxed text-accent-text" data-pitch-warning>
          These two voices sound quite different — one is much lower. Same person?
        </p>
      ) : null}
      <p className="mt-3 text-[13.5px] leading-relaxed text-muted">
        Another voice in this clip is already {name}. Listen to both: if they are one person, their words become one voice (you can undo it).
      </p>
      <div className="mt-3 flex flex-wrap gap-4" data-both-voices>
        {[{ v: mine, label: 'This voice' }, { v: other, label: `${d.other ?? 'The other voice'}, already ${name}` }].map(({ v, label }) => (
          <figure key={label} className="w-[176px]">
            {v?.samples?.[0] && d.assetId ? (
              <SampleTile assetId={d.assetId} from={v.samples[0].from} to={v.samples[0].to} hasVideo={hasVideo} label={label} size="md" />
            ) : (
              <Skeleton className="h-[90px] w-[160px] rounded-[12px]" />
            )}
            <figcaption className="mt-1 text-[12px] text-muted">{label}</figcaption>
          </figure>
        ))}
      </div>
      {renaming ? (
        <div className="mt-4">
          <InlinePicker label="Who is the other voice?" onPick={(c) => void renameOther(c)} exclude={d.personId ? [d.personId] : []} autoFocus placeholder="Who is the other voice?" />
        </div>
      ) : null}
      <div className="mt-6 flex flex-wrap justify-end gap-2">
        <Button variant="primary" data-autofocus data-safe-choice onClick={() => close(false)}>
          Keep things as they are
        </Button>
        <Button variant="secondary" disabled={busy} onClick={() => close('same')}>
          Same person — combine
        </Button>
        <Button variant="secondary" disabled={busy} loading={busy && renaming} onClick={() => setRenaming(true)}>
          Different — rename the other one
        </Button>
      </div>
    </div>
  );
}

/** A face, and the person it is said or thought to be. */
export interface FaceInQuestion {
  faceId: string;
  assetId: string;
  at: number;
  box: number[];
  personId: string;
  name: string;
  /** How alike it looks (a face named by its looks). */
  score?: number;
}

/**
 * "Not Maya?" on a face: the faces Maya's face was learned from, the one
 * this face looks most like marked, each with "Not Maya"; then this face is
 * said not to be her. Resolves true when something changed.
 */
export function notThemFace(dialog: DialogApi, f: FaceInQuestion): Promise<boolean> {
  return dialog.open<boolean>((close) => <FaceLearnedBody f={f} close={close} />, { title: `Not ${f.name}?`, size: 'md' }).then((v) => v === true);
}

function FaceLearnedBody({ f, close }: { f: FaceInQuestion; close: (v?: boolean) => void }) {
  const learned = useOp('people.learned', { id: f.personId, faceId: f.faceId });
  const [fixed, setFixed] = useState<PersonLearnedOut | undefined>();
  const out = fixed ?? learned.data;
  const [fixing, setFixing] = useState<string | null>(null);
  const [who, setWho] = useState(false);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const refresh = useRefresh();
  const named = out?.face ? out.face.recognized : true;
  const unlearn = async (lf: LearnedFace, choice: PersonChoice) => {
    setBusy(true);
    try {
      const res = await call('people.unlearn', { id: f.personId, faceId: lf.faceId, againstFaceId: f.faceId, ...unlearnInput(choice) });
      setFixed(res);
      setFixing(null);
      refresh();
      toast({ tone: 'success', title: res.done });
    } catch (err) {
      const e = toApiError(err);
      toast({ tone: 'error', title: 'Could not take it back', body: [e.message, e.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  const notThem = async (choice?: PersonChoice) => {
    setBusy(true);
    try {
      await call('footage.faces.reject', { faceId: f.faceId, person: f.personId });
      if (choice && choice.kind !== 'unsure') {
        await call('footage.faces.name', choice.kind === 'person' ? { faceId: f.faceId, personId: choice.person.id, name: choice.person.name } : { faceId: f.faceId, name: choice.name, newPerson: true });
      }
      refresh();
      toast({ tone: 'success', title: `Noted: that face is not ${f.name}.` });
      close(true);
    } catch (err) {
      const e = toApiError(err);
      toast({ tone: 'error', title: 'Could not change it', body: [e.message, e.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div data-compare-faces>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-[auto_1fr]">
        <section aria-label="This face" className="sm:w-[132px]">
          <h3 className="mb-1.5 text-[12px] font-semibold text-fg">This face</h3>
          <FaceTile assetId={f.assetId} at={f.at} box={f.box} size={116} label={`The face said to be ${f.name}`} />
          {named && f.score ? (
            <span className="mt-1 flex items-center gap-1 text-[11.5px] text-muted">
              <Sparkle size={11} /> {pct(f.score)}% like {f.name}
            </span>
          ) : null}
          {fixed && !named ? <p className="mt-1 text-[11.5px] font-medium text-ok" data-unnamed-now>Not named {f.name} any more.</p> : null}
        </section>
        <section aria-label={`What ${f.name}'s face is learned from`} data-learned-faces>
          <h3 className="mb-1.5 text-[12px] font-semibold text-fg">{f.name}'s face, as you named it</h3>
          {learned.isLoading && !out ? (
            <Skeleton className="h-28 rounded-[12px]" />
          ) : !out || out.faces.length === 0 ? (
            <p className="text-[12.5px] text-subtle">Not learned from any face now.</p>
          ) : (
            <ul className="flex flex-wrap gap-2">
              {out.faces.map((lf) => (
                <li key={lf.faceId} className={cx('w-[132px] rounded-[12px] p-2', lf.odd ? 'bg-danger-soft' : lf.closest ? 'bg-accent-soft' : 'bg-panel hairline')} data-learned-face data-closest={lf.closest || undefined} data-odd={lf.odd || undefined}>
                  <FaceTile assetId={lf.assetId} at={lf.at} box={lf.box} size={116} label={`${f.name}'s face in ${lf.assetTitle}`} />
                  <Theirs className="mt-1 block truncate text-[11.5px] text-fg">{lf.assetTitle}</Theirs>
                  <span className="block text-[11px] text-muted">
                    {lf.faces > 1 ? `${lf.faces} faces, one answer` : 'one face'}
                    {lf.likeThis != null ? ` · ${pct(lf.likeThis)}% like this face` : ''}
                  </span>
                  {lf.closest ? <Badge tone="accent" className="mt-1">Most like this face</Badge> : null}
                  {lf.odd ? <span className="mt-1 block text-[11px] font-medium text-danger">{lf.why}</span> : null}
                  <Button size="sm" variant="ghost" className="mt-1 -ml-1.5" disabled={busy} onClick={() => setFixing(fixing === lf.faceId ? null : lf.faceId)}>
                    Not {f.name}
                  </Button>
                  {fixing === lf.faceId ? (
                    <InlinePicker label={`Who is it, if not ${f.name}?`} onPick={(c) => void unlearn(lf, c)} exclude={[f.personId]} allowUnsure autoFocus placeholder="Who is it?" />
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
      {who ? (
        <div className="mt-4">
          <InlinePicker label="Who is this face?" onPick={(c) => void notThem(c)} exclude={[f.personId]} allowUnsure autoFocus placeholder="Who is it?" />
        </div>
      ) : null}
      <div className="mt-6 flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={() => close(!!fixed)}>
          {fixed ? 'Done' : 'Cancel'}
        </Button>
        {named ? (
          <>
            <Button variant="secondary" disabled={busy} onClick={() => setWho(true)}>
              Say who it is
            </Button>
            <Button variant="primary" data-autofocus disabled={busy} onClick={() => void notThem()}>
              This face isn't {f.name}
            </Button>
          </>
        ) : null}
      </div>
    </div>
  );
}
