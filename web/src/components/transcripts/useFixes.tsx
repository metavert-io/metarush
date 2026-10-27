import { useCallback } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import type { OpInput, OpOutput } from '../../api/types';
import { useToast } from '../../ui/Toast';

/** An error in plain words: what happened and what to do. */
export function plainError(err: unknown): string {
  const e = toApiError(err);
  return [e.message, e.hint].filter(Boolean).join(' ');
}

/**
 * The change was saved and only learning the voices again from it failed
 * (the server says so in the error's details): the words moved, so the page
 * refreshes and the toast says what is left to do.
 */
function savedAnyway(err: unknown): string | null {
  const e = toApiError(err);
  const d = e.details as { saved?: boolean } | undefined;
  return d?.saved ? e.hint || e.message : null;
}

/** Queries a transcript fix changes (the words show everywhere at once). */
export const FIX_QUERIES = ['footage.transcript', 'transcript.history', 'transcript.pending', 'transcript.find', 'captions.check', 'captions.file', 'cut.show', 'search', 'transcript.spelling.suggestions', 'transcript.spelling.list', 'todos.list', 'todos.counts'] as const;

type Suggestion = NonNullable<OpOutput<'transcript.fix'>['suggestion']>;

/**
 * Fixing transcripts (D145): retype words, give a stretch to someone, undo, and
 * put a word on the spelling list, each with a toast that can take it back.
 */
export function useFixes() {
  const qc = useQueryClient();
  const toast = useToast();
  const refresh = useCallback(() => {
    for (const k of FIX_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);

  const undo = useCallback(
    async (input: OpInput<'transcript.undo'>) => {
      try {
        const out = await call('transcript.undo', input);
        refresh();
        toast({ tone: out.undone ? 'success' : 'info', title: out.summary });
        return out;
      } catch (err) {
        const left = savedAnyway(err);
        if (left) {
          refresh();
          void qc.invalidateQueries({ queryKey: ['footage.speakers'] });
          void qc.invalidateQueries({ queryKey: ['people.list'] });
          toast({ tone: 'info', title: 'Undone, but the voices weren’t learned again', body: left });
          return null;
        }
        toast({ tone: 'error', title: 'Not undone', body: plainError(err) });
        return null;
      }
    },
    [qc, refresh, toast],
  );

  const addSpelling = useCallback(
    async (input: OpInput<'transcript.spelling.add'>) => {
      try {
        const out = await call('transcript.spelling.add', input);
        refresh();
        toast({ tone: 'success', title: out.summary });
        return out;
      } catch (err) {
        toast({ tone: 'error', title: 'Not added to the spelling list', body: plainError(err) });
        return null;
      }
    },
    [refresh, toast],
  );

  /** The spelling list question that comes with a fix people keep making. */
  const suggestionActions = useCallback(
    (s: Suggestion | undefined, projectId?: string) =>
      s && projectId
        ? [{ label: `Add “${s.word}” to the spelling list`, onClick: () => void addSpelling({ projectId, word: s.word, heardAs: s.heard }) }]
        : undefined,
    [addSpelling],
  );

  const fix = useCallback(
    async (input: OpInput<'transcript.fix'>) => {
      try {
        const out = await call('transcript.fix', input);
        refresh();
        if (!out.fixes.length) {
          toast({ tone: 'info', title: out.summary });
          return out;
        }
        const s = out.suggestion;
        toast({
          tone: 'success',
          title: out.summary,
          body: s ? `You fixed “${s.heard[0] ?? s.word}” ${s.times > 1 ? `${s.times} times` : `in ${s.places} places`}. Add “${s.word}” to the spelling list so new transcripts get it right?` : 'Saved versions keep their words until you make a new version.',
          action: out.batchId ? { label: 'Undo', onClick: () => void undo({ batchId: out.batchId }) } : undefined,
          actions: suggestionActions(s, input.projectId),
          durationMs: s ? 12_000 : undefined,
        });
        return out;
      } catch (err) {
        toast({ tone: 'error', title: 'Not fixed', body: plainError(err) });
        return null;
      }
    },
    [refresh, toast, undo, suggestionActions],
  );

  const speaker = useCallback(
    async (input: OpInput<'transcript.speaker'>) => {
      try {
        const out = await call('transcript.speaker', input);
        refresh();
        void qc.invalidateQueries({ queryKey: ['footage.speakers'] });
        void qc.invalidateQueries({ queryKey: ['people.list'] });
        toast({
          tone: 'success',
          title: out.summary,
          body: out.note || undefined,
          action: out.fixId ? { label: 'Undo', onClick: () => void undo({ fixId: out.fixId }) } : undefined,
        });
        return out;
      } catch (err) {
        const left = savedAnyway(err);
        if (left) {
          refresh();
          void qc.invalidateQueries({ queryKey: ['footage.speakers'] });
          void qc.invalidateQueries({ queryKey: ['people.list'] });
          toast({ tone: 'info', title: 'Saved, but the voices weren’t learned again', body: left });
          return null;
        }
        toast({ tone: 'error', title: 'Could not change who says it', body: plainError(err) });
        return null;
      }
    },
    [qc, refresh, toast, undo],
  );

  const replace = useCallback(
    async (input: OpInput<'transcript.replace'>) => {
      try {
        const out = await call('transcript.replace', input);
        refresh();
        toast({
          tone: out.changed ? 'success' : 'info',
          title: out.summary,
          body: out.note || (out.changed ? 'Saved versions keep their words until you make a new version.' : undefined),
          action: out.changed ? { label: 'Undo', onClick: () => void undo({ batchId: out.batchId }) } : undefined,
          actions: suggestionActions(out.suggestion, input.projectId),
        });
        return out;
      } catch (err) {
        toast({ tone: 'error', title: 'Nothing changed', body: plainError(err) });
        return null;
      }
    },
    [refresh, toast, undo, suggestionActions],
  );

  return { fix, speaker, replace, undo, addSpelling, refresh };
}
