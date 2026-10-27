import { useState } from 'react';
import { DownloadSimple, Subtitles, WarningCircle } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import type { CaptionIssue } from '../../api/types';
import { timecode } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Button, EmptyState, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { SnippetButton } from '../SnippetPlayer';
import { FixWordsEditor } from './FixWords';
import { plainError, useFixes } from './useFixes';

/**
 * The caption check before a version goes out (D148): words the computer
 * wasn't sure it heard, names not on the spelling lists, and words fixed
 * since the version (its captions still say the old ones). Listen, fix, or
 * put a name on the spelling list.
 */
export function CaptionCheck({ projectId, revisionId }: { projectId: string; revisionId: string }) {
  const chk = useOp('captions.check', { projectId, revision: revisionId });
  const fixes = useFixes();
  const [fixing, setFixing] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (chk.isLoading) return <Skeleton className="h-32" />;
  if (chk.error) return <ErrorPanel error={chk.error} onRetry={() => void chk.refetch()} compact />;
  const d = chk.data!;
  const key = (i: CaptionIssue) => `${i.kind}:${i.assetId}:${i.from}`;
  return (
    <div className="space-y-3" data-caption-check>
      <p className="text-[12.5px] text-muted">{d.summary}</p>
      {d.note ? <p className="text-[11.5px] text-subtle">{d.note}</p> : null}
      {d.issues.length === 0 ? (
        <EmptyState compact icon={<Subtitles size={20} />} title="Nothing to look at" body="Every word was heard clearly and every name is on the spelling list." />
      ) : (
        <ul className="max-h-[min(56dvh,480px)] divide-y divide-line overflow-y-auto rounded-[12px] hairline">
          {d.issues.map((is) => (
            <li key={key(is)} className="px-3 py-2" data-caption-issue={is.kind}>
              <div className="flex items-start gap-2.5">
                {is.assetId ? <SnippetButton assetId={is.assetId} from={Math.max(0, is.from - 1)} to={is.to + 1} label={`what is said at ${timecode(is.from, { ms: false })}`} size="sm" /> : null}
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] text-fg">
                    {is.kind === 'fixed' ? <WarningCircle size={13} className="mr-1 inline text-accent-text" /> : null}
                    {is.says}
                  </p>
                  {is.kind !== 'fixed' ? (
                    <div className="mt-1 flex flex-wrap gap-1.5">
                      <Button size="sm" variant="ghost" onClick={() => setFixing(fixing === key(is) ? null : key(is))}>
                        Fix it
                      </Button>
                      {is.kind === 'name' ? (
                        <Button size="sm" variant="ghost" onClick={() => void fixes.addSpelling({ projectId, word: is.word.replace(/^[^\p{L}\p{N}]+|[^\p{L}\p{N}]+$/gu, '') })}>
                          It's right: add <Theirs>“{is.word.replace(/^[^\p{L}\p{N}]+|[^\p{L}\p{N}]+$/gu, '')}”</Theirs> to the spelling list
                        </Button>
                      ) : null}
                    </div>
                  ) : null}
                </div>
              </div>
              {fixing === key(is) && is.assetId ? (
                <div className="mt-2">
                  <FixWordsEditor
                    words={is.word}
                    busy={busy}
                    onCancel={() => setFixing(null)}
                    onSave={async (text) => {
                      setBusy(true);
                      const out = await fixes.fix({ assetId: is.assetId!, from: is.from, to: is.to, text, expect: is.word, projectId });
                      setBusy(false);
                      if (out) setFixing(null);
                    }}
                  />
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** "Check the captions" and "Captions file" for a version. */
export function CaptionTools({ projectId, revisionId }: { projectId: string; revisionId: string }) {
  const dialog = useDialog();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const download = async (format: 'srt' | 'vtt') => {
    setBusy(format);
    try {
      const out = await call('captions.file', { projectId, revision: revisionId, format });
      const url = URL.createObjectURL(new Blob([out.content], { type: format === 'vtt' ? 'text/vtt' : 'application/x-subrip' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = out.fileName;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 2000);
      toast({ tone: 'success', title: out.summary, body: out.note || 'Text fixes reach this file without a new render.' });
    } catch (err) {
      toast({ tone: 'error', title: 'No captions file', body: plainError(err) });
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className="flex flex-wrap items-center justify-center gap-1.5" data-caption-tools>
      <Button size="sm" variant="ghost" icon={<Subtitles size={13} />} onClick={() => void dialog.open(() => <CaptionCheck projectId={projectId} revisionId={revisionId} />, { title: 'Check the captions', size: 'lg' })} data-caption-check-open>
        Check the captions
      </Button>
      <Button size="sm" variant="ghost" icon={<DownloadSimple size={13} />} loading={busy === 'srt'} onClick={() => void download('srt')} data-captions-file>
        Captions file (.srt)
      </Button>
      <Button size="sm" variant="ghost" loading={busy === 'vtt'} onClick={() => void download('vtt')}>
        .vtt
      </Button>
    </div>
  );
}
