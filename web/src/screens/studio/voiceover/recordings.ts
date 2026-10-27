import { call } from '../../../api/client';
import { uploads, type UploadItem } from '../../../api/uploads';

/*
 * Getting a recording into the project: it is uploaded through the same
 * resumable uploads as footage (the upload tray shows it), and once MetaRush
 * has listened to it (a local transcript) its words can be matched to the
 * script.
 */

export class RecordingProblem extends Error {}

/** Uploads a file into the project and resolves with its asset when it is stored. */
export function uploadRecording(file: File, projectId: string, label: string, onProgress?: (it: UploadItem) => void): Promise<string> {
  return new Promise((resolve, reject) => {
    const unsub = uploads.subscribe(() => {
      const it = uploads.getSnapshot().find((x) => x.file === file);
      if (it) onProgress?.(it);
    });
    const off = uploads.onFinished((it) => {
      if (it.file !== file) return;
      off();
      unsub();
      const id = it.result?.assetId ?? it.duplicate?.assetId;
      if ((it.state === 'done' || it.state === 'aliased' || it.state === 'skipped') && id) resolve(id);
      else reject(new RecordingProblem(it.error || 'The recording could not be added to the project.'));
    });
    uploads.add([{ file }], { projectId, label }).catch((e: unknown) => {
      off();
      unsub();
      reject(e);
    });
  });
}

/**
 * Waits until MetaRush has listened to a recording (its words are written
 * down). onWaiting is told when the work has not started yet (another job is
 * first, or the computer is saving battery).
 */
export async function waitForWords(assetId: string, signal: AbortSignal, onWaiting?: (waiting: boolean) => void): Promise<void> {
  const started = Date.now();
  for (;;) {
    if (signal.aborted) throw new DOMException('stopped', 'AbortError');
    const out = await call('asset.show', { id: assetId }, signal);
    const st = out.asset.transcript?.state;
    if (st === 'available') return;
    const job = out.jobs.find((j) => j.type === 'transcribe');
    if (st === 'failed' || job?.state === 'failed') {
      throw new RecordingProblem(
        `MetaRush could not listen to the recording on this computer${out.asset.transcript?.error ? `: ${out.asset.transcript.error}` : job?.error ? `: ${job.error}` : '.'}`,
      );
    }
    if (!job && Date.now() - started > 15_000 && !st) {
      // Already stored before (the same file again): ask for its words now.
      await call('footage.analyze', { assetId }).catch(() => undefined);
    }
    onWaiting?.(!!job && job.state === 'queued' && Date.now() - started > 8_000);
    await new Promise((r) => setTimeout(r, 1500));
  }
}

/** A recording's words as script paragraphs (one per sentence group), for a script written from what was said. */
export async function wordsAsScript(assetId: string): Promise<string> {
  const tx = await call('footage.transcript', { assetId });
  const paras = tx.segments.map((s) => s.text.trim()).filter(Boolean);
  if (!paras.length) throw new RecordingProblem('No speech was found in the recording.');
  return paras.join('\n\n');
}
