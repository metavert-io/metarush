import { useEffect, useState } from 'react';
import { CopySimple, VideoCamera } from '@phosphor-icons/react';
import { media } from '../api/client';
import { uploads, type DuplicateChoice, type DuplicateQuestion } from '../api/uploads';
import { bytes, plural } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Button, cx, Thumb } from '../ui/primitives';

const baseName = (p: string) => p.slice(Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\')) + 1);

function keepLabel(q: DuplicateQuestion): string | null {
  if (q.existing?.sameName) return q.existing.inTarget ? null : `Add to ${q.targetLabel}`;
  return 'Keep both';
}

function Where({ q }: { q: DuplicateQuestion }) {
  if (q.existing) {
    return (
      <>
        identical to <span className="font-medium text-fg">“{q.existing.title}”</span> <span className="text-subtle">(in {q.existing.where})</span>
      </>
    );
  }
  return (
    <>
      identical to <span className="font-medium text-fg">{baseName(q.sameAs ?? '')}</span> <span className="text-subtle">(in this drop)</span>
    </>
  );
}

function Single({ q, close }: { q: DuplicateQuestion; close: (v?: Map<string, DuplicateChoice>) => void }) {
  const keep = keepLabel(q);
  return (
    <div>
      <div className="flex items-start gap-3.5">
        {q.existing ? (
          <Thumb src={media.asset(q.existing.assetId, 'thumbnail')} alt="" className="h-14 w-[88px] shrink-0 rounded-[10px]" fallback={<VideoCamera size={18} />} />
        ) : (
          <span className="grid h-14 w-14 shrink-0 place-items-center rounded-[12px] bg-accent-soft text-accent-text">
            <CopySimple size={22} />
          </span>
        )}
        <p className="min-w-0 text-[13.5px] leading-relaxed text-muted">
          <span className="font-medium text-fg [overflow-wrap:anywhere]">{baseName(q.name)}</span> is <Where q={q} />.
        </p>
      </div>
      <p className="mt-4 text-[12.5px] leading-relaxed text-subtle">
        Nothing has been uploaded yet.{' '}
        {keep === 'Keep both'
          ? 'Keep both remembers this filename on the same video. The video is stored only once.'
          : keep
            ? `Adding it puts the video you already have into ${q.targetLabel}, without a second copy.`
            : 'It is already where you dropped it.'}
      </p>
      <div className="mt-6 flex flex-wrap justify-end gap-2">
        {keep ? (
          <Button variant="secondary" onClick={() => close(new Map([[q.key, 'keep']]))}>
            {keep}
          </Button>
        ) : null}
        <Button variant="primary" data-autofocus onClick={() => close(new Map([[q.key, 'skip']]))}>
          Skip
        </Button>
      </div>
    </div>
  );
}

function Many({ qs, close }: { qs: DuplicateQuestion[]; close: (v?: Map<string, DuplicateChoice>) => void }) {
  const [choice, setChoice] = useState<Map<string, DuplicateChoice>>(() => new Map(qs.map((q) => [q.key, 'skip'])));
  const set = (k: string, v: DuplicateChoice) => setChoice((m) => new Map(m).set(k, v));
  const kept = [...choice.values()].filter((v) => v === 'keep').length;
  return (
    <div>
      <p className="text-[13px] leading-relaxed text-muted">Nothing has been uploaded yet. Skipped files are left out. Kept files add their filename to the video you already have, which is still stored only once.</p>
      <ul className="mt-4 max-h-[46dvh] space-y-1.5 overflow-y-auto">
        {qs.map((q) => {
          const keep = keepLabel(q);
          const v = choice.get(q.key);
          return (
            <li key={q.key} className="flex flex-col gap-2 rounded-[12px] px-3 py-2.5 hairline sm:flex-row sm:items-center">
              <div className="flex min-w-0 flex-1 items-center gap-3">
                <Thumb src={q.existing ? media.asset(q.existing.assetId, 'thumbnail') : undefined} alt="" className="h-9 w-14 shrink-0 rounded-[7px]" fallback={<CopySimple size={14} />} />
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-medium text-fg" title={q.name}>
                    {baseName(q.name)} <span className="tc text-[11px] font-normal text-subtle">{bytes(q.size)}</span>
                  </div>
                  <div className="truncate text-[12px] text-muted">
                    <Where q={q} />
                  </div>
                </div>
              </div>
              <div role="radiogroup" aria-label={`What to do with ${baseName(q.name)}`} className="flex shrink-0 rounded-[9px] bg-panel p-0.5 hairline">
                {(['skip', 'keep'] as const).map((opt) => {
                  if (opt === 'keep' && !keep) return null;
                  const on = v === opt;
                  return (
                    <button
                      key={opt}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => set(q.key, opt)}
                      className={cx('h-7 rounded-[7px] px-2.5 text-[12px] font-medium transition-colors', on ? 'bg-accent-soft text-accent-text' : 'text-muted hover:text-fg')}
                    >
                      {opt === 'skip' ? 'Skip' : keep}
                    </button>
                  );
                })}
              </div>
            </li>
          );
        })}
      </ul>
      <div className="mt-6 flex flex-wrap items-center justify-end gap-2">
        <span className="mr-auto text-[12px] text-subtle">{kept ? `${plural(kept, 'file')} kept, ${qs.length - kept} skipped` : 'All skipped'}</span>
        <Button variant="ghost" onClick={() => setChoice(new Map(qs.map((q) => [q.key, keepLabel(q) ? 'keep' : 'skip'])))}>
          Keep all
        </Button>
        <Button variant="primary" data-autofocus onClick={() => close(choice)}>
          Continue
        </Button>
      </div>
    </div>
  );
}

/** Registers the styled duplicate dialog with the upload manager (Skip is the default). */
export function DuplicateResolver() {
  const dialog = useDialog();
  useEffect(() => {
    uploads.setDuplicateResolver(async (qs) => {
      const title = qs.length === 1 ? 'Already in MetaRush' : `${plural(qs.length, 'file')} already in MetaRush`;
      const out = await dialog.open<Map<string, DuplicateChoice>>(
        (close) => (qs.length === 1 ? <Single q={qs[0]!} close={close} /> : <Many qs={qs} close={close} />),
        { title, size: qs.length === 1 ? 'sm' : 'md' },
      );
      return out ?? new Map(qs.map((q) => [q.key, 'skip' as DuplicateChoice])); // Esc or backdrop = skip
    });
    return () => uploads.setDuplicateResolver(null);
  }, [dialog]);
  return null;
}
