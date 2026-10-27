import { useId, type ReactNode } from 'react';
import { ChatCircleDots, FileAudio, Microphone, PencilLine, Robot } from '@phosphor-icons/react';
import type { Clip } from '../../../api/types';
import { cx } from '../../../ui/primitives';

/*
 * "Add voiceover": how the person wants to make it, for the whole video or one
 * clip. The same chooser sits in the Voiceover tab (when there is no narration
 * yet) and in a dialog from the Current cut panel.
 */

export type VoiceoverMode = 'write' | 'ask' | 'import' | 'record' | 'ai';

export interface Scope {
  /** One clip, or the whole video when undefined. */
  clipId?: string;
}

export function clipName(clips: Clip[], id: string | undefined): string {
  const i = clips.findIndex((c) => c.id === id);
  if (i < 0) return 'the whole video';
  return clips[i]!.label ? `“${clips[i]!.label}”` : `shot ${i + 1}`;
}

interface Option {
  mode: VoiceoverMode;
  title: string;
  body: string;
  icon: ReactNode;
}

/** Two ways to start with the words, three with the voice. */
const GROUPS: { title: string; options: Option[] }[] = [
  {
    title: 'Start with the words',
    options: [
      { mode: 'write', title: 'Write my own script', body: 'Type or paste what to say. Each line can start on its own shot.', icon: <PencilLine size={20} /> },
      { mode: 'ask', title: 'Ask Claude to suggest one', body: 'Claude watches the cut and drafts lines for you to change or keep.', icon: <ChatCircleDots size={20} /> },
    ],
  },
  {
    title: 'Start with the voice',
    options: [
      { mode: 'record', title: 'Record it now', body: 'Read into the microphone while the video plays, your words on screen.', icon: <Microphone size={20} /> },
      { mode: 'import', title: 'Use a recording I have', body: 'Add an audio file. MetaRush lines it up with the video.', icon: <FileAudio size={20} /> },
      { mode: 'ai', title: 'AI voice', body: 'A natural computer voice reads your lines. See the price first.', icon: <Robot size={20} /> },
    ],
  },
];

/** Audio files the import option accepts (anything with sound works; these are the common ones). */
export const AUDIO_ACCEPT = 'audio/*,.wav,.mp3,.m4a,.aac,.aif,.aiff,.flac,.ogg,.opus,.webm,video/*';

export function AddVoiceover({ clips, scope, onScope, onChoose, hasScript, compact, hideScope }: {
  clips: Clip[];
  scope: Scope;
  onScope: (s: Scope) => void;
  /** import comes with the file the person picked. */
  onChoose: (mode: VoiceoverMode, scope: Scope, file?: File) => void;
  hasScript: boolean;
  compact?: boolean;
  /** The scope is chosen elsewhere (the Voiceover tab's own "For" picker). */
  hideScope?: boolean;
}) {
  const scopeId = useId();
  const clipSel = useId();
  return (
    <div>
      <fieldset className={cx('mb-4', hideScope && 'hidden')}>
        <legend id={scopeId} className="mb-2 text-[12.5px] font-medium text-muted">
          The voiceover covers
        </legend>
        <div className="flex flex-wrap items-center gap-2" role="radiogroup" aria-labelledby={scopeId}>
          <Choice checked={!scope.clipId} onClick={() => onScope({})}>
            The whole video
          </Choice>
          <Choice checked={!!scope.clipId} onClick={() => onScope({ clipId: scope.clipId ?? clips[0]?.id })} disabled={clips.length === 0}>
            One clip
          </Choice>
          {scope.clipId ? (
            <>
              <label htmlFor={clipSel} className="sr-only">
                Which clip
              </label>
              <select
                id={clipSel}
                value={scope.clipId}
                onChange={(e) => onScope({ clipId: e.target.value })}
                className="h-8 max-w-[240px] rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
              >
                {clips.map((c, i) => (
                  <option key={c.id} value={c.id}>
                    {i + 1}. {c.label || `Shot ${i + 1}`}
                  </option>
                ))}
              </select>
            </>
          ) : null}
        </div>
      </fieldset>
      <div className="space-y-4">
        {GROUPS.map((g) => (
          <div key={g.title}>
            <h4 className="mb-2 text-[12px] font-medium text-subtle">{g.title}</h4>
            <ul
              className={cx('grid gap-2.5', compact ? 'grid-cols-1' : g.options.length === 3 ? 'grid-cols-1 sm:grid-cols-3' : 'grid-cols-1 sm:grid-cols-2')}
              aria-label={g.title}
            >
              {g.options.map((o) => (
                <li key={o.mode}>
                  <OptionCard o={o} hint={o.mode === 'ai' && !hasScript ? 'You write the lines first.' : undefined} onChoose={(file) => onChoose(o.mode, scope, file)} />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </div>
  );
}

function OptionCard({ o, hint, onChoose }: { o: Option; hint?: string; onChoose: (file?: File) => void }) {
  const content = (
    <>
      <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[10px] bg-accent-soft text-accent-text transition-transform duration-200 group-hover:scale-105" aria-hidden>
        {o.icon}
      </span>
      <span className="min-w-0">
        <span className="block text-[14px] font-semibold tracking-[-0.01em] text-fg">{o.title}</span>
        <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">{o.body}</span>
        {hint ? <span className="mt-1 block text-[11.5px] text-subtle">{hint}</span> : null}
      </span>
    </>
  );
  const cls =
    'group flex h-full w-full items-start gap-3 rounded-[14px] bg-panel p-3.5 text-left hairline transition-[background-color,box-shadow,transform] duration-200 hover:bg-hover active:scale-[0.99] focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-accent';
  if (o.mode === 'import') {
    return (
      <label className={cx(cls, 'cursor-pointer')}>
        <input
          type="file"
          accept={AUDIO_ACCEPT}
          className="sr-only"
          aria-label="Use a recording I have: choose the audio file"
          onChange={(e) => {
            const f = e.target.files?.[0];
            e.target.value = '';
            if (f) onChoose(f);
          }}
        />
        {content}
      </label>
    );
  }
  return (
    <button type="button" className={cls} onClick={() => onChoose()}>
      {content}
    </button>
  );
}

function Choice({ checked, onClick, children, disabled }: { checked: boolean; onClick: () => void; children: ReactNode; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      disabled={disabled}
      onClick={onClick}
      className={cx(
        'inline-flex h-8 items-center rounded-full px-3 text-[13px] font-medium transition-colors duration-200',
        checked ? 'bg-accent text-accent-ink' : 'bg-panel text-muted hairline hover:bg-hover hover:text-fg',
      )}
    >
      {children}
    </button>
  );
}
