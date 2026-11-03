import type { OutputShape } from '../../api/types';
import type { Shape } from '../../lib/shape';

/*
 * The camera in the project's shape (D392). Kept free of other modules so the
 * recorder's unit tests (web/scripts/recorder-test.mjs) load it as it is.
 */

/**
 * The size a camera is asked for in each shape (D395): 720p widescreen, and a
 * tall or square picture by its long side on both sides (1280), so the
 * camera's own mode nearest it wins — its real tall mode when it has one
 * (a phone, a turned camera), else its 720p widescreen one, never a smaller
 * mode chosen for being nearer a narrow width.
 */
const CAMERA_SIZE: Record<Shape, [number, number]> = { landscape: [1280, 720], vertical: [1280, 1280], square: [1280, 1280] };
const CAMERA_ASPECT: Record<Shape, number> = { landscape: 1.7778, vertical: 0.5625, square: 1 };

/** A track setting the browser reports: whether it cropped or scaled the camera's picture itself. */
export const croppedByBrowser = (s?: { resizeMode?: string } | null) => s?.resizeMode === 'crop-and-scale';

/**
 * What the camera is asked for: the project's shape (widescreen when nothing
 * says) at 720p and 30 frames a second, where the camera can — only among the
 * camera's own modes (resizeMode exactly none: the browser never crops or
 * stretches it; a browser that doesn't know the setting ignores it, and
 * openCamera opens it again at its own size if it cropped anyway). A camera
 * that can't make the shape records its own, which the cut then frames.
 */
export function cameraConstraints(deviceId?: string, shape?: Shape): MediaTrackConstraints {
  const s = shape ?? 'landscape';
  const [width, height] = CAMERA_SIZE[s];
  const c: MediaTrackConstraints & { resizeMode?: ConstrainDOMString } = {
    ...(deviceId ? { deviceId: { exact: deviceId } } : {}),
    width: { ideal: width },
    height: { ideal: height },
    aspectRatio: { ideal: CAMERA_ASPECT[s] },
    frameRate: { ideal: 30 },
    resizeMode: { exact: 'none' },
  };
  return c;
}

/** The camera at its own size, nothing more asked (when a browser cropped or refused the shape). */
export function ownSizeConstraints(deviceId?: string): MediaTrackConstraints {
  return { ...(deviceId ? { deviceId: { exact: deviceId } } : {}), width: { ideal: 1280 }, frameRate: { ideal: 30 } };
}

/**
 * The shape being recorded, in words, from what the camera really gives (its
 * picture's size, described by lib/shape.ts sizeShape): "Recording widescreen
 * (1280×720)", and, when the project is another shape, that the cut frames it.
 */
export function recordingShapeWords(got: Pick<OutputShape, 'shape' | 'word' | 'width' | 'height'> | undefined, project?: Pick<OutputShape, 'shape' | 'word'>): string {
  if (!got?.shape) return '';
  const words = `Recording ${got.word} (${got.width}×${got.height})`;
  if (!project?.shape || project.shape === got.shape) return `${words}.`;
  return `${words}: the camera can't record ${project.word}, so the cut frames it ${project.word}.`;
}
