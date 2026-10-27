import { useMemo, useState } from 'react';
import { useParams } from 'react-router';
import { useOp } from '../../api/queries';
import { timecode } from '../../lib/format';
import { EmptyState, Skeleton } from '../../ui/primitives';
import { FixWordsEditor } from './FixWords';
import { useFixes } from './useFixes';

/**
 * Fixing what a clip of the cut says, from the Script view (D145): its words
 * between its in and out points, retyped in one go; only the words that
 * differ change and each keeps its timing.
 */
export function ClipWordsFix({ assetId, from, to, onDone }: { assetId: string; from: number; to: number; onDone: () => void }) {
  const { projectId } = useParams();
  const tx = useOp('footage.transcript', { assetId, words: true, from, to });
  const fixes = useFixes();
  const [busy, setBusy] = useState(false);
  const words = useMemo(() => {
    const out: string[] = [];
    for (const s of tx.data?.segments ?? []) {
      for (const w of s.words ?? []) {
        const mid = (w.f + w.t) / 2;
        if (mid >= from && mid < to) out.push(w.w.trim());
      }
    }
    return out.join(' ');
  }, [tx.data, from, to]);
  if (tx.isLoading) return <Skeleton className="h-24" />;
  if (!words) return <EmptyState compact title="No words to fix" body="Nothing with word timing is said in this clip." />;
  return (
    <div className="space-y-2" data-clip-words-fix>
      <p className="text-[12px] text-subtle">
        What is said from {timecode(from, { ms: false })} to {timecode(to, { ms: false })} in the footage. Saved versions keep their words until you make a new version.
      </p>
      <FixWordsEditor
        words={words}
        busy={busy}
        onCancel={onDone}
        onSave={async (text) => {
          setBusy(true);
          const out = await fixes.fix({ assetId, from, to, text, expect: words, projectId });
          setBusy(false);
          if (out) onDone();
        }}
      />
    </div>
  );
}
