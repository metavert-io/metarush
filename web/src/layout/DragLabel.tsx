import { useEffect, useState } from 'react';
import { DownloadSimple } from '@phosphor-icons/react';
import { useArmedDrop } from '../components/Library';
import { isAssetDrag, useAssetDrag } from '../lib/dragAssets';

/**
 * While clips are dragged, a label beside the pointer says where a drop would
 * land ("Add to fernbrook"); it shows only once a project is armed (B).
 */
export function DragLabel() {
  const drag = useAssetDrag();
  const armed = useArmedDrop();
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  useEffect(() => {
    if (!drag) {
      setAt(null);
      return;
    }
    const onOver = (e: DragEvent) => {
      if (isAssetDrag(e.dataTransfer)) setAt({ x: e.clientX, y: e.clientY });
    };
    window.addEventListener('dragover', onOver, true);
    return () => window.removeEventListener('dragover', onOver, true);
  }, [drag]);
  if (!drag || !armed || !at) return null;
  const n = drag.ids.length;
  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="drag-label"
      className="pointer-events-none fixed z-[90] inline-flex items-center gap-1.5 whitespace-nowrap rounded-full bg-accent px-3 py-1.5 text-[12.5px] font-medium text-accent-ink shadow-[var(--shadow-float)]"
      style={{ left: at.x + 18, top: at.y + 18 }}
    >
      <DownloadSimple size={13} weight="bold" />
      {n > 1 ? `Add ${n} clips to ${armed.name}` : `Add to ${armed.name}`}
    </div>
  );
}
