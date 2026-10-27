import { forwardRef, lazy, Suspense, type ComponentProps } from 'react';
import { Spinner } from '../ui/primitives';
import type { CutPreview as CutPreviewType, CutPreviewHandle } from './CutPreview';

// Remotion and the MetaRush templates are the heaviest part of the bundle; load
// them only on screens that preview a cut.
const Impl = lazy(() => import('./CutPreview').then((m) => ({ default: m.CutPreview })));

type Props = ComponentProps<typeof CutPreviewType>;

export const CutPreview = forwardRef<CutPreviewHandle, Omit<Props, 'ref'>>(function LazyCutPreview(props, ref) {
  return (
    <Suspense
      fallback={
        <div className="mx-auto grid aspect-[9/16] w-full max-w-[360px] place-items-center rounded-[14px] bg-inset">
          <Spinner size={16} className="text-[#a69d93]" label="Loading the preview player" />
        </div>
      }
    >
      <Impl ref={ref} {...props} />
    </Suspense>
  );
});

export type { CutPreviewHandle };
