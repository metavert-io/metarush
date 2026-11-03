import type { OutputShape } from '../../api/types';
import type { Shape } from '../../lib/shape';

/*
 * The camera in the project's shape (D392). Kept free of other modules so the
 * recorder's unit tests (web/scripts/recorder-test.mjs) load it as it is.
 */

/** The size a camera is asked for in each shape: 720p's. */
const CAMERA_SIZE: Record<Shape, [number, number]> = { landscape: [1280, 720], vertical: [720, 1280], square: [960, 960] };

/**
 * What the camera is asked for: the project's shape (widescreen when nothing
 * says) at 720p and 30 frames a second, where the camera can. Only ideals: a
 * camera that can't is recorded in its own shape, and the browser isn't asked
 * to crop or stretch it (resizeMode none), so what is recorded is the
 * camera's real picture, which the cut then frames.
 */
export function cameraConstraints(deviceId?: string, shape?: Shape): MediaTrackConstraints {
  const [width, height] = CAMERA_SIZE[shape ?? 'landscape'];
  const c: MediaTrackConstraints & { resizeMode?: ConstrainDOMString } = {
    ...(deviceId ? { deviceId: { exact: deviceId } } : {}),
    width: { ideal: width },
    height: { ideal: height },
    aspectRatio: { ideal: Math.round((width / height) * 10000) / 10000 },
    frameRate: { ideal: 30 },
    resizeMode: { ideal: 'none' },
  };
  return c;
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
