import { useId, useMemo, useState, type ReactNode } from 'react';
import { ArrowCounterClockwise, CheckCircle, FilmSlate, Image as ImageIcon, MagnifyingGlass, Microphone, TextAa, ListBullets } from '@phosphor-icons/react';
import { media } from '../../api/client';
import { useOp } from '../../api/queries';
import { resourceMedia } from '../../api/resources';
import { duration as fmtDuration, plural } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Button, cx, EmptyState, ErrorPanel, Skeleton, Theirs, Thumb } from '../../ui/primitives';
import type { RecipeInputRow, RecipeInputValue, RecipeRunInput } from './api';

/*
 * What changes each run (D202): the recipe's inputs as a form — words, a
 * choice, the AI voice, clips or pictures — each with its limits, where it
 * goes and what the plan says the run will use. Only what the person changed
 * is sent; everything else keeps the recipe's default (or, making a run
 * again, what that run used).
 */

/** A changed input: words or a choice or a voice (text), or clips and pictures (items, with how to say them). */
export interface InputDraft {
  text?: string;
  items?: string[];
  /** The chosen clips or pictures in a few words ("2 clips: “Harbour”, “Kitchen”"). */
  words?: string;
}

export function draftsToInputs(drafts: Record<string, InputDraft>): RecipeInputValue[] {
  return Object.entries(drafts).map(([name, d]) => ({ name, ...(d.text !== undefined ? { text: d.text } : {}), ...(d.items ? { items: d.items } : {}) }));
}

const KIND: Record<RecipeInputRow['kind'], { label: string; word: string; icon: ReactNode }> = {
  words: { label: 'Words', word: 'words', icon: <TextAa size={13} /> },
  choice: { label: 'A choice', word: 'choice', icon: <ListBullets size={13} /> },
  voice: { label: 'A voice', word: 'voice', icon: <Microphone size={13} /> },
  footage: { label: 'Footage', word: 'footage', icon: <FilmSlate size={13} /> },
  pictures: { label: 'Pictures', word: 'picture', icon: <ImageIcon size={13} /> },
};

/** What kind of input it is and its limits, without saying what its name already says ("The footage": "at least 1 clip"). */
function kindWords(row: RecipeInputRow): string {
  const k = KIND[row.kind];
  const parts = row.label.toLowerCase().includes(k.word) ? [] : [k.label];
  if (row.limits) parts.push(row.limits);
  const s = parts.join(', ');
  return s ? s[0]!.toUpperCase() + s.slice(1) : '';
}

const field =
  'w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60';

export function InputsForm({ inputs, drafts, onChange, planned, base, projectId, disabled }: {
  inputs: RecipeInputRow[];
  drafts: Record<string, InputDraft>;
  onChange: (name: string, d: InputDraft | undefined) => void;
  /** What the plan says each input will be. */
  planned?: RecipeRunInput[];
  /** Making a run again: what that run used (the form starts from it). */
  base?: RecipeRunInput[];
  /** The recipe's project: the pictures it can use. */
  projectId?: string;
  disabled?: boolean;
}) {
  if (!inputs.length) {
    return <p className="text-[13px] text-muted">This recipe has nothing that changes from run to run: every run makes the same video again.</p>;
  }
  const plannedBy = new Map((planned ?? []).map((p) => [p.name, p]));
  const baseBy = new Map((base ?? []).map((p) => [p.name, p]));
  return (
    <div className="space-y-4" data-run-inputs>
      {inputs.map((row) => (
        <InputField
          key={row.name}
          row={row}
          draft={drafts[row.name]}
          onChange={(d) => onChange(row.name, d)}
          planned={plannedBy.get(row.name)}
          base={baseBy.get(row.name)}
          projectId={projectId}
          disabled={disabled}
        />
      ))}
    </div>
  );
}

function InputField({ row, draft, onChange, planned, base, projectId, disabled }: {
  row: RecipeInputRow;
  draft?: InputDraft;
  onChange: (d: InputDraft | undefined) => void;
  planned?: RecipeRunInput;
  base?: RecipeRunInput;
  projectId?: string;
  disabled?: boolean;
}) {
  const id = useId();
  const kind = KIND[row.kind];
  // Where the form starts: the run being made again, else the recipe's default.
  const startText = base ? base.text || (row.kind === 'voice' ? base.items?.[0]?.title : '') || '' : row.value.text ?? '';
  const changed = !!draft;
  const goes = row.goes.filter(Boolean);
  return (
    <div className="rounded-[12px] bg-hover/40 px-3.5 py-3" data-run-input={row.name} data-changed={changed ? 'yes' : 'no'}>
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <label htmlFor={id} className="text-[13px] font-medium text-fg">
          {row.label}
        </label>
        <span className="inline-flex items-center gap-1 text-[11.5px] text-subtle">
          {kind.icon}
          {kindWords(row)}
        </span>
        {changed ? (
          <button
            type="button"
            className="ml-auto inline-flex items-center gap-1 text-[12px] font-medium text-accent-text hover:underline"
            onClick={() => onChange(undefined)}
            disabled={disabled}
            data-input-reset
          >
            <ArrowCounterClockwise size={12} /> {base ? 'As before' : 'As in the recipe'}
          </button>
        ) : null}
      </div>
      {goes.length ? <p className="mt-0.5 text-[11.5px] leading-relaxed text-subtle">Goes to {goes.join('; ')}.</p> : null}
      <div className="mt-2">
        {row.kind === 'words' ? (
          <WordsField id={id} row={row} start={startText} draft={draft} onChange={onChange} disabled={disabled} />
        ) : row.kind === 'choice' ? (
          <select
            id={id}
            value={draft?.text ?? startText}
            disabled={disabled}
            onChange={(e) => onChange(e.target.value === startText ? undefined : { text: e.target.value })}
            className={cx(field, 'h-9')}
          >
            {(row.choices ?? []).map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        ) : row.kind === 'voice' ? (
          <VoiceField id={id} start={startText} draft={draft} onChange={onChange} disabled={disabled} />
        ) : (
          <ItemsField id={id} row={row} draft={draft} onChange={onChange} base={base} projectId={projectId} disabled={disabled} />
        )}
      </div>
      {planned?.words ? (
        <p className="mt-1.5 text-[11.5px] text-muted" data-input-planned>
          This run: {planned.words}
          {planned.default ? ' (as in the recipe)' : ''}
          {planned.changed ? ' (changed)' : ''}
        </p>
      ) : null}
    </div>
  );
}

function WordsField({ id, row, start, draft, onChange, disabled }: {
  id: string;
  row: RecipeInputRow;
  start: string;
  draft?: InputDraft;
  onChange: (d: InputDraft | undefined) => void;
  disabled?: boolean;
}) {
  const value = draft?.text ?? start;
  const long = (row.maxLength ?? 0) > 160 || start.length > 90;
  const set = (v: string) => onChange(v === start ? undefined : { text: v });
  const left = row.maxLength ? row.maxLength - value.length : undefined;
  return (
    <div>
      {long ? (
        <textarea id={id} rows={3} value={value} maxLength={row.maxLength || undefined} disabled={disabled} onChange={(e) => set(e.target.value)} className={cx(field, 'resize-y py-2 leading-relaxed')} />
      ) : (
        <input id={id} value={value} maxLength={row.maxLength || undefined} disabled={disabled} onChange={(e) => set(e.target.value)} className={cx(field, 'h-9')} />
      )}
      {left !== undefined && left <= 10 ? (
        <p className={cx('mt-1 text-right text-[11px]', left < 0 ? 'text-danger' : 'text-subtle')}>{left >= 0 ? `${plural(left, 'character')} left` : `${plural(-left, 'character')} too many`}</p>
      ) : null}
    </div>
  );
}

/** The AI voice: one of the account's voices by name (typed when they can't be listed). */
function VoiceField({ id, start, draft, onChange, disabled }: { id: string; start: string; draft?: InputDraft; onChange: (d: InputDraft | undefined) => void; disabled?: boolean }) {
  const voices = useOp('voiceover.voices', { provider: 'elevenlabs' }, { staleTime: 5 * 60_000, retry: false });
  const value = draft?.text ?? start;
  const names = useMemo(() => {
    const list = (voices.data?.voices ?? []).map((v) => v.name).filter(Boolean);
    if (start && !list.includes(start)) list.unshift(start);
    return [...new Set(list)];
  }, [voices.data, start]);
  const set = (v: string) => onChange(v === start ? undefined : { text: v });
  if (voices.data && names.length > 1) {
    return (
      <select id={id} value={value} disabled={disabled} onChange={(e) => set(e.target.value)} className={cx(field, 'h-9')} data-voice-select>
        {names.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    );
  }
  return <input id={id} value={value} disabled={disabled} onChange={(e) => set(e.target.value)} placeholder="The voice's name" className={cx(field, 'h-9')} />;
}

/** Clips or pictures: what the run uses now, and a picker for others. */
function ItemsField({ id, row, draft, onChange, base, projectId, disabled }: {
  id: string;
  row: RecipeInputRow;
  draft?: InputDraft;
  onChange: (d: InputDraft | undefined) => void;
  base?: RecipeRunInput;
  projectId?: string;
  disabled?: boolean;
}) {
  const dialog = useDialog();
  const pictures = row.kind === 'pictures';
  const what = pictures ? 'picture' : 'clip';
  const now = draft?.words ?? (base ? base.words : row.default);
  const choose = async () => {
    const picked = await dialog.open<{ ids: string[]; words: string }>(
      (close) =>
        pictures ? (
          <PicturePicker projectId={projectId} min={row.minCount} max={row.maxCount} onDone={close} />
        ) : (
          <ClipPicker min={row.minCount} max={row.maxCount} onDone={close} />
        ),
      { title: pictures ? `Choose pictures for “${row.label}”` : `Choose clips for “${row.label}”`, size: 'lg' },
    );
    if (picked) onChange({ items: picked.ids, words: picked.words });
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span id={id} className="min-w-0 flex-1 text-[13px] text-fg">
        {now || `No ${what}s yet`}
        {row.from && !draft ? <span className="block text-[11.5px] text-subtle">A run takes {row.from}.</span> : null}
      </span>
      <Button size="sm" variant="secondary" icon={pictures ? <ImageIcon size={13} /> : <FilmSlate size={13} />} onClick={() => void choose()} disabled={disabled} data-input-choose={row.name}>
        {pictures ? 'Choose pictures…' : 'Choose clips…'}
      </Button>
    </div>
  );
}

function countWords(n: number, what: string, titles: string[]): string {
  const shown = titles.slice(0, 3).map((t) => `“${t}”`);
  return `${plural(n, what)}: ${shown.join(', ')}${n > 3 ? ` and ${n - 3} more` : ''}`;
}

function limitsOk(n: number, min?: number, max?: number) {
  return n > 0 && (!min || n >= min) && (!max || n <= max);
}

function PickerFooter({ n, min, max, what, onCancel, onUse }: { n: number; min?: number; max?: number; what: string; onCancel: () => void; onUse: () => void }) {
  const ok = limitsOk(n, min, max);
  const need = min && max && min !== max ? `${min}–${max} ${what}s` : max ? plural(max, what) : min ? `at least ${plural(min, what)}` : '';
  return (
    <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-4">
      <span className="text-[12.5px] text-muted">
        {plural(n, what)} chosen{need ? `; this takes ${need}` : ''}.
      </span>
      <span className="flex-1" />
      <Button variant="ghost" onClick={onCancel}>
        Cancel
      </Button>
      <Button variant="primary" icon={<CheckCircle size={14} />} disabled={!ok} onClick={onUse} data-picker-use>
        Use {plural(n, what)}
      </Button>
    </div>
  );
}

function ClipPicker({ min, max, onDone }: { min?: number; max?: number; onDone: (v?: { ids: string[]; words: string }) => void }) {
  const [q, setQ] = useState('');
  const lib = useOp('library.list', { view: 'all', ...(q.trim() ? { query: q.trim() } : {}), limit: 120 }, { placeholderData: (p) => p });
  const [picked, setPicked] = useState<string[]>([]);
  const clips = (lib.data?.items ?? []).filter((a) => !a.trashedAt && !a.trash && (a.kind === 'video' || a.kind === 'audio'));
  const titles = new Map(clips.map((c) => [c.id, c.title]));
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : max === 1 ? [id] : [...p, id]));
  return (
    <div data-clip-picker>
      <label className="flex h-9 items-center gap-2 rounded-[10px] bg-panel px-3 hairline focus-within:outline-2 focus-within:outline-accent">
        <MagnifyingGlass size={14} className="text-subtle" aria-hidden />
        <span className="sr-only">Search clips</span>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search all footage" className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-subtle" />
      </label>
      <div className="mt-3 max-h-[52dvh] overflow-y-auto">
        {lib.isLoading ? (
          <Skeleton className="h-40" />
        ) : lib.error ? (
          <ErrorPanel compact error={lib.error} />
        ) : clips.length === 0 ? (
          <EmptyState compact icon={<FilmSlate size={20} />} title="No clips here" body="Add footage to MetaRush first: drop it anywhere." />
        ) : (
          <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3" aria-label="Clips">
            {clips.map((c) => {
              const on = picked.includes(c.id);
              return (
                <li key={c.id}>
                  <button
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggle(c.id)}
                    className={cx('block w-full rounded-[12px] p-1.5 text-left transition-colors', on ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
                    data-pick-clip={c.title}
                  >
                    <Thumb src={c.kind === 'video' ? media.asset(c.id, 'thumbnail') : undefined} alt="" className="aspect-video w-full rounded-[9px]" fallback={<FilmSlate size={18} />} />
                    <span className="mt-1 block truncate px-0.5 text-[12.5px] font-medium text-fg">
                      <Theirs>{c.title}</Theirs>
                    </span>
                    <span className="block px-0.5 text-[11px] text-subtle">{fmtDuration(c.duration)}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <PickerFooter
        n={picked.length}
        min={min}
        max={max}
        what="clip"
        onCancel={() => onDone()}
        onUse={() => onDone({ ids: picked, words: countWords(picked.length, 'clip', picked.map((id) => titles.get(id) ?? 'a clip')) })}
      />
    </div>
  );
}

function PicturePicker({ projectId, min, max, onDone }: { projectId?: string; min?: number; max?: number; onDone: (v?: { ids: string[]; words: string }) => void }) {
  const list = useOp('resources.list', projectId ? { projectId } : {});
  const [picked, setPicked] = useState<string[]>([]);
  const rows = (list.data?.resources ?? []).filter((r) => r.kind === 'image' || r.kind === 'svg');
  const titles = new Map(rows.map((r) => [r.id, r.title]));
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : max === 1 ? [id] : [...p, id]));
  return (
    <div data-picture-picker>
      <p className="text-[13px] text-muted">Pictures the recipe&rsquo;s project can use: its own, its linked projects&rsquo; and the workspace library&rsquo;s. They are used where they are, nothing is copied.</p>
      <div className="mt-3 max-h-[52dvh] overflow-y-auto">
        {list.isLoading ? (
          <Skeleton className="h-40" />
        ) : list.error ? (
          <ErrorPanel compact error={list.error} />
        ) : rows.length === 0 ? (
          <EmptyState compact icon={<ImageIcon size={20} />} title="No pictures here yet" body="Add pictures to the project's Resources, or link a folder of them." />
        ) : (
          <ul className="grid grid-cols-3 gap-2 sm:grid-cols-4" aria-label="Pictures">
            {rows.map((r) => {
              const on = picked.includes(r.id);
              return (
                <li key={r.id}>
                  <button
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggle(r.id)}
                    className={cx('block w-full rounded-[12px] p-1.5 text-left transition-colors', on ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
                    data-pick-picture={r.title}
                  >
                    <Thumb src={resourceMedia.thumb(r.id, r.current)} alt="" fit="contain" className="aspect-square w-full rounded-[9px]" fallback={<ImageIcon size={18} />} />
                    <span className="mt-1 block truncate px-0.5 text-[12px] text-fg">
                      <Theirs>{r.title}</Theirs>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <PickerFooter
        n={picked.length}
        min={min}
        max={max}
        what="picture"
        onCancel={() => onDone()}
        onUse={() => onDone({ ids: picked, words: countWords(picked.length, 'picture', picked.map((id) => titles.get(id) ?? 'a picture')) })}
      />
    </div>
  );
}
