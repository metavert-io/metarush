import { useId, useState } from 'react';
import { Translate } from '@phosphor-icons/react';
import type { ops } from '@metarush/schema';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { Button, cx } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { useRefreshSources } from './FixesPanel';

/*
 * The language a clip is spoken in (D243): heard by MetaRush, set by a
 * person, or the workspace's. Changing it writes the clip's words down again
 * with the recognizer for that language (footage.language.set, the same as
 * `metarush footage language <clip> --set <language>`).
 */
export function SpokenLanguage({ language, className }: { language: ops.FootageLanguageOut; className?: string }) {
  const [open, setOpen] = useState(false);
  const how =
    language.language?.by === 'person' ? 'a person set it' : language.language?.by === 'heard' ? 'MetaRush heard it' : "this workspace's language";
  return (
    <div className={cx('space-y-2 rounded-[12px] px-3.5 py-3 text-[12.5px] hairline', className)} data-spoken-language={language.language?.code ?? ''}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Translate size={14} className="shrink-0 text-accent-text" aria-hidden />
        <span className="font-medium text-fg">Spoken in {language.name || 'a language not known yet'}</span>
        {language.name ? <span className="text-subtle">({how})</span> : null}
        {!open ? (
          <Button size="sm" variant="ghost" onClick={() => setOpen(true)} data-change-language>
            Change…
          </Button>
        ) : null}
      </div>
      {!language.sure || language.wordsIn ? <p className="text-muted" data-language-note>{language.note}</p> : null}
      {!language.sure && language.heardName ? (
        <SetLanguage assetId={language.assetId} code={language.language?.heard ?? ''} label={`It's ${language.heardName}: write it down again`} />
      ) : null}
      {open ? <LanguagePicker language={language} onDone={() => setOpen(false)} /> : null}
    </div>
  );
}

function LanguagePicker({ language, onDone }: { language: ops.FootageLanguageOut; onDone: () => void }) {
  const id = useId();
  const q = useOp('footage.language', { assetId: language.assetId, choices: true });
  const [code, setCode] = useState(language.language?.code ?? 'en');
  const choices = q.data?.choices ?? [];
  return (
    <div className="flex flex-wrap items-end gap-2">
      <label htmlFor={id} className="block min-w-0">
        <span className="mb-1 block text-[12px] font-medium text-muted">The language it is spoken in</span>
        <select id={id} value={code} onChange={(e) => setCode(e.target.value)} disabled={!choices.length} className="h-8 rounded-[9px] bg-panel px-2 text-[12.5px] hairline" data-language-choice>
          {choices.map((c) => (
            <option key={c.code} value={c.code}>
              {c.name}
            </option>
          ))}
        </select>
      </label>
      <SetLanguage assetId={language.assetId} code={code} label="Write it down again" onDone={onDone} />
      <Button size="sm" variant="ghost" onClick={onDone}>
        Cancel
      </Button>
    </div>
  );
}

function SetLanguage({ assetId, code, label, onDone }: { assetId: string; code: string; label: string; onDone?: () => void }) {
  const toast = useToast();
  const refresh = useRefreshSources();
  const [busy, setBusy] = useState(false);
  return (
    <Button
      size="sm"
      variant="secondary"
      loading={busy}
      disabled={!code}
      data-set-language={code}
      onClick={async () => {
        setBusy(true);
        try {
          const out = await call('footage.language.set', { assetId, language: code });
          toast({ tone: 'success', title: `Spoken in ${out.name}`, body: out.note });
          refresh();
          onDone?.();
        } catch (e) {
          toast({ tone: 'error', title: "Couldn't change the language", body: toApiError(e).message });
        } finally {
          setBusy(false);
        }
      }}
    >
      {label}
    </Button>
  );
}
