import { useCallback } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError, call, toApiError } from '../api/client';
import type { AutoMerge, OpInput, RemoteOp, SpeakerRenameOut } from '../api/types';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { confirmSamePerson, type ConfirmDetails } from './CompareDialogs';
import type { PersonChoice } from './PersonPicker';

/*
 * Naming voices and fixing who said what, the same way everywhere (the
 * Speakers tab, the transcript, To-dos, People): two voices of one clip only
 * become one person after a person says so (with a plain warning when the
 * voices sound clearly different), every merge can be undone from its toast,
 * and errors say what to do.
 */

const SPEAKER_QUERIES: RemoteOp[] = ['footage.speakers', 'footage.transcript', 'todos.counts', 'todos.list', 'people.list', 'people.show', 'notification.list', 'activity.summary'];

export { confirmSamePerson } from './CompareDialogs';
export type { ConfirmDetails, Confirmed } from './CompareDialogs';

function nameInput(assetId: string, speaker: string, choice: PersonChoice): OpInput<'footage.speakers.rename'> | null {
  switch (choice.kind) {
    case 'person':
      return { assetId, speaker, personId: choice.person.id, name: choice.person.name };
    case 'new':
      return { assetId, speaker, name: choice.name, newPerson: true };
  }
  return null;
}

export function useSpeakerActions() {
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useCallback(() => {
    for (const k of SPEAKER_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);

  const fail = useCallback((title: string, err: unknown) => {
    const e = toApiError(err);
    toast({ tone: 'error', title, body: [e.message, e.hint].filter(Boolean).join(' ') });
  }, [toast]);

  const unmerge = useCallback(
    async (assetId: string, speaker: string) => {
      try {
        await call('footage.speakers.unmerge', { assetId, speaker });
        refresh();
        toast({ tone: 'success', title: 'Separated again', body: 'Both voices are back as they were.' });
      } catch (err) {
        fail('Could not separate the voices', err);
      }
    },
    [refresh, toast, fail],
  );

  /** Says what changed; every combination of two voices comes with Undo. */
  const announce = useCallback(
    (assetId: string | undefined, summary: string, separate?: string, autoMerged?: AutoMerge[]) => {
      if (separate && assetId) {
        toast({ tone: 'success', title: summary, action: { label: 'Undo', onClick: () => void unmerge(assetId, separate) }, durationMs: 9000 });
      } else toast({ tone: 'success', title: summary });
      for (const m of autoMerged ?? []) {
        toast({
          tone: 'info',
          title: `Also combined two voices in “${m.assetTitle}”`,
          body: `Both sound like ${m.name}.`,
          action: { label: 'Undo', onClick: () => void unmerge(m.assetId, m.dropped) },
          durationMs: 9000,
        });
      }
    },
    [toast, unmerge],
  );
  const announceMerges = useCallback((assetId: string, out: SpeakerRenameOut) => announce(assetId, out.summary, out.separate, out.autoMerged), [announce]);

  /**
   * Names a voice. Resolves true when it was named; false when the person
   * chose another name instead (the caller keeps the editor open).
   */
  const nameVoice = useCallback(
    async (assetId: string, speaker: string, choice: PersonChoice): Promise<boolean> => {
      const input = nameInput(assetId, speaker, choice);
      if (!input) return false;
      try {
        // A naming that needs confirming opens the comparison dialog (D158):
        // hear what it is compared with, fix an earlier mistake in place, then
        // name it (again) — at most a couple of rounds.
        let out: SpeakerRenameOut | undefined;
        let samePerson = false;
        for (let round = 0; !out; round++) {
          try {
            out = await call('footage.speakers.rename', samePerson ? { ...input, samePerson: true } : input);
          } catch (err) {
            const e = toApiError(err);
            if (e.code !== 'E_CONFIRM_REQUIRED' || round >= 2) throw err;
            const ok = await confirmSamePerson(dialog, (e.details ?? {}) as ConfirmDetails);
            if (!ok) return false;
            samePerson = ok === 'same';
          }
        }
        refresh();
        announceMerges(assetId, out);
        return true;
      } catch (err) {
        fail('Could not save the name', err);
        return false;
      }
    },
    [dialog, refresh, announceMerges, fail],
  );

  /** "Not Maya?": the label goes, the voice is asked about again. */
  const notThem = useCallback(
    async (assetId: string, speaker: string, person: { id?: string; name: string }) => {
      try {
        await call('footage.speakers.reject', { assetId, speaker, personId: person.id, name: person.id ? undefined : person.name });
        refresh();
        toast({ tone: 'info', title: `Okay, that's not ${person.name}`, body: 'We will ask who it is instead.' });
        return true;
      } catch (err) {
        fail('Could not update the voice', err);
        return false;
      }
    },
    [refresh, toast, fail],
  );

  /** Gives the words said in [from, to) to a person (fixing who said a line). */
  const assign = useCallback(
    async (assetId: string, from: number, to: number, choice: PersonChoice) => {
      const input: OpInput<'footage.speakers.assign'> =
        choice.kind === 'person'
          ? { assetId, from, to, personId: choice.person.id }
          : choice.kind === 'new'
            ? { assetId, from, to, person: choice.name, newPerson: true }
            : { assetId, from, to, unsure: true };
      try {
        const out = await call('footage.speakers.assign', input);
        refresh();
        toast({ tone: 'success', title: out.summary, body: out.note || (out.dropped.length ? `We'll ask again who speaks in ${out.dropped.map((d) => `“${d.assetTitle}”`).join(', ')}.` : undefined) });
        return true;
      } catch (err) {
        fail('Could not change who said it', err);
        return false;
      }
    },
    [refresh, toast, fail],
  );

  return { nameVoice, notThem, assign, unmerge, announce, refresh };
}

export function isConfirm(err: unknown): err is ApiError {
  return toApiError(err).code === 'E_CONFIRM_REQUIRED';
}
