import { forwardRef, lazy, Suspense, type ComponentProps } from 'react';
import { Spinner } from '../ui/primitives';
import type { CutPreview as CutPreviewType, CutPreviewHandle } from './CutPreview';
import { useServedShape } from '../lib/useShape';
import { shapeAspect } from '../ui/ShapeBox';

// Remotion and the MetaRush templates are the heaviest part of the bundle; load
// them only on screens that preview a cut.
const Impl = lazy(() => import('./CutPreview').then((m) => ({ default: m.CutPreview })));

type Props = ComponentProps<typeof CutPreviewType>;

export const CutPreview = forwardRef<CutPreviewHandle, Omit<Props, 'ref'>>(function LazyCutPreview(props, ref) {
  // The player's box at the cut's shape as the server says it (D376), the size the player takes.
  const served = useServedShape({ revisionId: props.revisionId });
  const aspect = shapeAspect(props.shape ?? served);
  const maxHeight = props.maxHeight ?? 'min(70dvh, 720px)';
  return (
    <Suspense
      fallback={
        <div className="mx-auto grid w-full place-items-center rounded-[14px] bg-inset" style={{ aspectRatio: String(aspect), maxHeight, maxWidth: `calc(${maxHeight} * ${aspect})` }}>
          <Spinner size={16} className="text-[#a69d93]" label="Loading the preview player" />
        </div>
      }
    >
      <Impl ref={ref} {...props} />
    </Suspense>
  );
});

export type { CutPreviewHandle };
