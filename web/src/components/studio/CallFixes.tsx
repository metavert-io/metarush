import { useCallback, useEffect, useRef, useState } from 'react';
import { IdentificationBadge, Scissors } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import type { Clip, CutFlashesOut, CutNamesOut, Spec } from '../../api/types';
import { capFirst, plural } from '../../lib/format';
import { Button, Segmented, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import type { TimelineEditor as Editor } from './useTimelineEditor';

/*
 * Call recordings in the studio (D160–D162, docs/call-recordings.md): when the
 * latest cut opens or ends a clip on a split-second of the other person
 * ("Trim the flashes"), or shows the names a call app printed in the picture
 * ("Cover the printed names"), MetaRush offers the fix right under the player.
 * Each is the same op as `metarush cut flashes|names`: checked first (a dry run,
 * once per version of the cut, never while the timeline has unsaved changes)
 * and saved with that check's confirm token as a new version you can undo.
 */

const DEBOUNCE_MS = 900;

function usable(out: { noChange: boolean; check?: { valid: boolean; confirmToken?: string } } | null): boolean {
  return !!out && !out.noChange && out.check?.valid !== false && !!out.check?.confirmToken;
}

export function CallFixes({
  projectId,
  headId,
  dirty,
  editor,
  spec,
  nameOf,
  firstNamesRule,
}: {
  projectId: string;
  /** The latest version of the cut: the checks run again when it changes. */
  headId?: string;
  dirty: boolean;
  editor: Editor;
  spec?: Spec;
  nameOf: (c: Clip) => string;
  /** The project's rules say "first names only". */
  firstNamesRule: boolean;
}) {
  const toast = useToast();
  const [flashes, setFlashes] = useState<CutFlashesOut | null>(null);
  const [names, setNames] = useState<CutNamesOut | null>(null);
  // First names or full names: what the project's rules say until the person picks (the op's own default).
  const [picked, setPicked] = useState<boolean>();
  const first = picked ?? firstNamesRule;
  const [busy, setBusy] = useState<'flashes' | 'names' | null>(null);
  const [rechecking, setRechecking] = useState(false);
  const checked = useRef('');
  const pickedRef = useRef(picked);
  pickedRef.current = picked;

  const check = useCallback(
    (key: string, isGone: () => boolean) =>
      Promise.allSettled([call('cut.flashes', { projectId, dryRun: true }), call('cut.names', { projectId, dryRun: true, firstNames: pickedRef.current })]).then(([f, n]) => {
        if (isGone()) return;
        checked.current = key;
        setFlashes(f.status === 'fulfilled' ? f.value : null);
        // No printed names found (or no call footage): nothing to offer.
        setNames(n.status === 'fulfilled' ? n.value : null);
      }),
    [projectId],
  );

  // Once per version of the cut, a moment after it settles.
  useEffect(() => {
    if (!headId || dirty || checked.current === headId) return;
    let gone = false;
    const t = setTimeout(() => void check(headId, () => gone), DEBOUNCE_MS);
    return () => {
      gone = true;
      clearTimeout(t);
    };
  }, [headId, dirty, check]);

  const chooseFirst = (v: boolean) => {
    if (v === first) return;
    setPicked(v);
    setRechecking(true);
    call('cut.names', { projectId, dryRun: true, firstNames: v })
      .then(setNames)
      .catch(() => setNames(null))
      .finally(() => setRechecking(false));
  };

  const save = async (kind: 'flashes' | 'names') => {
    const token = (kind === 'flashes' ? flashes : names)?.check?.confirmToken;
    if (!token) return;
    const summary = (kind === 'flashes' ? flashes : names)?.summary;
    setBusy(kind);
    let failed: string | undefined;
    const ok = await editor
      .runSaved(kind === 'flashes' ? 'Trimmed the flashes' : 'Covered the printed names', async () => {
        const out = kind === 'flashes' ? await call('cut.flashes', { projectId, confirmToken: token }) : await call('cut.names', { projectId, confirmToken: token, firstNames: picked });
        return out.applied?.revision.id;
      })
      .catch((e: unknown) => {
        const ae = toApiError(e);
        failed = [ae.message, ae.hint].filter(Boolean).join(' ');
        return false;
      });
    setBusy(null);
    if (ok) {
      if (kind === 'flashes') setFlashes(null);
      else setNames(null);
      toast({
        tone: 'success',
        title: kind === 'flashes' ? 'Trimmed the flashes' : 'Covered the printed names',
        body: summary,
        action: { label: 'Undo', onClick: () => void editor.undo() },
      });
      return;
    }
    toast({ tone: 'error', title: kind === 'flashes' ? 'The flashes were not trimmed' : 'The names were not covered', body: failed ?? 'Save or undo your changes on the timeline first, then try again.' });
    // The cut may have moved on: look again.
    checked.current = '';
    if (headId) void check(headId, () => false);
  };

  const showFlashes = usable(flashes) && (flashes?.flashes.length ?? 0) > 0;
  const showNames = usable(names) && (names?.nameBar?.clips ?? 0) > 0;
  if (!showFlashes && !showNames) return null;

  const clipOf = (id: string) => spec?.clips.find((c) => c.id === id);
  const fl = flashes?.flashes ?? [];
  const one = fl.length === 1 ? fl[0]! : undefined;
  const oneClip = one ? clipOf(one.clipId) : undefined;
  const unknown = names?.nameBar?.unknown ?? 0;

  return (
    <div className="mt-3 space-y-2 text-left" data-call-fixes>
      {showFlashes ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[12px] bg-panel px-3 py-2.5 hairline" data-call-fix="flashes">
          <Scissors size={16} className="shrink-0 text-accent-text" aria-hidden />
          <div className="min-w-0 flex-1 basis-[220px]">
            <p className="text-[13px] font-medium leading-snug text-fg">
              {one && oneClip ? (
                <>
                  {capFirst(nameOf(oneClip))} {one.edge === 'start' ? 'opens' : 'ends'} with a split-second of {one.flash ? <Theirs>{one.flash}</Theirs> : 'another picture'}
                </>
              ) : (
                `${plural(fl.length, 'split-second flash', 'split-second flashes')} of another picture at the clips' edges`
              )}
            </p>
            <p className="mt-0.5 text-[12px] leading-relaxed text-muted">
              {flashes?.summary}
              {fl.some((f) => f.words) ? ' No words are lost: the sound starts a little earlier or carries on.' : ''}
            </p>
          </div>
          <Button size="sm" variant="primary" icon={<Scissors size={13} />} loading={busy === 'flashes'} disabled={!!busy || dirty} onClick={() => void save('flashes')} data-trim-flashes>
            Trim the flashes
          </Button>
        </div>
      ) : null}
      {showNames ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[12px] bg-panel px-3 py-2.5 hairline" data-call-fix="names">
          <IdentificationBadge size={16} className="shrink-0 text-accent-text" aria-hidden />
          <div className="min-w-0 flex-1 basis-[220px]">
            <p className="text-[13px] font-medium leading-snug text-fg">The call app printed names in the picture</p>
            <p className="mt-0.5 text-[12px] leading-relaxed text-muted">
              {names?.summary}
              {unknown ? ` In ${plural(unknown, 'shot')} nobody is known yet, so the bar shows no name there.` : ''}
            </p>
          </div>
          <Segmented<'first' | 'full'>
            label="Names in the bar"
            value={first ? 'first' : 'full'}
            onChange={(v) => chooseFirst(v === 'first')}
            items={[
              { value: 'first', label: 'First names' },
              { value: 'full', label: 'Full names' },
            ]}
          />
          <Button
            size="sm"
            variant="primary"
            icon={<IdentificationBadge size={13} />}
            loading={busy === 'names'}
            disabled={!!busy || dirty || rechecking}
            onClick={() => void save('names')}
            data-cover-names
          >
            Cover the printed names
          </Button>
        </div>
      ) : null}
      {dirty ? <p className="text-[11.5px] text-subtle">This waits until your changes on the timeline are saved.</p> : null}
    </div>
  );
}
