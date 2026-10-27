import { useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { DownloadSimple, Sparkle, Translate, Trash } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import { askInProject } from '../../layout/assistant/store';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, ErrorPanel, Skeleton } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { plainError } from './useFixes';

/*
 * Captions in other languages (D238). Claude Code translates a version's
 * caption lines on the person's own Claude plan (Ask Claude to translate…
 * opens the panel with the request; its card keeps them), with the spelling
 * list as its glossary. Each language's file is marked as machine-translated,
 * follows later text fixes, and goes out with the video when it is delivered.
 */

/** Languages people ask for most, by their codes. */
const COMMON: [string, string][] = [
  ['es', 'Spanish'],
  ['fr', 'French'],
  ['de', 'German'],
  ['pt-BR', 'Brazilian Portuguese'],
  ['it', 'Italian'],
  ['nl', 'Dutch'],
  ['ja', 'Japanese'],
  ['ko', 'Korean'],
  ['zh-Hans', 'Chinese (Simplified)'],
  ['hi', 'Hindi'],
  ['ar', 'Arabic'],
  ['pl', 'Polish'],
  ['tr', 'Turkish'],
  ['sv', 'Swedish'],
  ['uk', 'Ukrainian'],
];

export function CaptionLanguages({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const langs = useOp('captions.languages', { projectId, revision: revisionId });
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const d = langs.data;

  const ask = async () => {
    const picked = await dialog.open<{ code: string; name: string }>((close) => <PickLanguage have={(d?.languages ?? []).map((l) => l.language)} onClose={close} />, {
      title: 'Translate the captions',
      size: 'sm',
    });
    if (!picked) return;
    askInProject(
      projectId,
      `Translate the captions of version ${number} into ${picked.name} (${picked.code}), keeping the names on the spelling list as written.`,
    );
  };
  const download = async (language: string, format: 'srt' | 'vtt') => {
    setBusy(language + format);
    try {
      const out = await call('captions.file', { projectId, revision: revisionId, format, language });
      const url = URL.createObjectURL(new Blob([out.content], { type: format === 'vtt' ? 'text/vtt' : 'application/x-subrip' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = out.fileName;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 2000);
      toast({ tone: 'success', title: out.summary, body: out.note || undefined });
    } catch (err) {
      toast({ tone: 'error', title: 'No captions file', body: plainError(err) });
    } finally {
      setBusy(null);
    }
  };
  const remove = async (language: string, name: string) => {
    const ok = await dialog.confirm({
      title: `Take the ${name} captions off?`,
      body: `Every line translated into ${name} goes, for every version of the cut. Videos delivered before keep their files.`,
      confirmLabel: 'Take them off',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('captions.translation.remove', { projectId, language });
      void qc.invalidateQueries({ queryKey: ['captions.languages'] });
      toast({ tone: 'success', title: out.summary });
    } catch (err) {
      toast({ tone: 'error', title: 'Not taken off', body: plainError(err) });
    }
  };

  return (
    <section aria-label="Captions in other languages" className="mt-8" data-caption-languages>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-[14px] font-semibold">Captions in other languages</h2>
        <Button size="sm" variant="ghost" className="ml-auto" icon={<Translate size={13} />} onClick={() => void ask()} data-translate-ask>
          Ask Claude to translate…
        </Button>
      </div>
      {langs.isLoading ? (
        <Skeleton className="h-10" />
      ) : langs.error ? (
        <ErrorPanel error={langs.error} compact onRetry={() => void langs.refetch()} />
      ) : !d || d.languages.length === 0 ? (
        <p className="text-[13px] text-muted">
          The captions are in {d?.fromName ?? 'one language'} only. Claude translates them in the Claude Code panel, keeping the names on the spelling list as written; the
          files say they were machine-translated.
        </p>
      ) : (
        <ul className="divide-y divide-line rounded-[12px] hairline">
          {d.languages.map((l) => {
            const todo = l.missing + l.outdated;
            return (
              <li key={l.language} className="flex flex-wrap items-center gap-2 px-3 py-2 text-[13px]" data-caption-language={l.language}>
                <span className="font-medium">{l.name}</span>
                <span className="text-muted">
                  {l.translated} of {l.lines} lines
                </span>
                {l.machine ? <Badge tone="accent">Machine-translated</Badge> : null}
                {l.outdated > 0 ? (
                  <Badge tone="info">
                    {l.outdated === 1 ? '1 line changed since it was translated' : `${l.outdated} lines changed since they were translated`}
                  </Badge>
                ) : null}
                <div className="ml-auto flex flex-wrap gap-1">
                  {todo > 0 ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Sparkle size={12} />}
                      onClick={() => askInProject(projectId, `Translate the lines of version ${number}'s captions that still need translating into ${l.name} (${l.language}).`)}
                    >
                      Ask Claude for the rest
                    </Button>
                  ) : null}
                  <Button size="sm" variant="ghost" icon={<DownloadSimple size={12} />} loading={busy === l.language + 'srt'} onClick={() => void download(l.language, 'srt')}>
                    .srt
                  </Button>
                  <Button size="sm" variant="ghost" loading={busy === l.language + 'vtt'} onClick={() => void download(l.language, 'vtt')}>
                    .vtt
                  </Button>
                  <Button size="sm" variant="ghost" icon={<Trash size={12} />} onClick={() => void remove(l.language, l.name)} aria-label={`Take the ${l.name} captions off`} />
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function PickLanguage({ have, onClose }: { have: string[]; onClose: (v?: { code: string; name: string }) => void }) {
  const uid = useId();
  const choices = COMMON.filter(([c]) => !have.includes(c));
  const [code, setCode] = useState(choices[0]?.[0] ?? '');
  const [other, setOther] = useState('');
  const name = COMMON.find(([c]) => c === code)?.[1];
  const go = () => {
    if (code === 'other') {
      const v = other.trim();
      if (v) onClose({ code: v, name: v });
      return;
    }
    if (code && name) onClose({ code, name });
  };
  return (
    <div className="space-y-3">
      <label htmlFor={`${uid}-l`} className="block text-[12.5px] font-medium text-muted">
        Into which language?
      </label>
      <select id={`${uid}-l`} value={code} onChange={(e) => setCode(e.target.value)} className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline">
        {choices.map(([c, n]) => (
          <option key={c} value={c}>
            {n}
          </option>
        ))}
        <option value="other">Another language…</option>
      </select>
      {code === 'other' ? (
        <input
          aria-label="The language"
          value={other}
          onChange={(e) => setOther(e.target.value)}
          placeholder="Catalan, or its code: ca"
          className="h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline"
        />
      ) : null}
      <p className="text-[12px] text-subtle">Claude Code translates them in the panel (Settings → Claude Code says whose Claude account answers); you keep them from its card.</p>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()}>
          Cancel
        </Button>
        <Button variant="primary" onClick={go} data-translate-go>
          Ask Claude
        </Button>
      </div>
    </div>
  );
}
