import { useEffect, useId, useState, type FormEvent, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Plus, Signature, X } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp, useOpMutation } from '../../api/queries';
import type { ContentStyleOut, OpInput, RemoteOp, Signoff, StyleCard, StyleCutOffer } from '../../api/types';
import { absolute, relative } from '../../lib/format';
import { Button, cx, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { CAPTION_LOOKS } from '../studio/CaptionLook';

/*
 * The house style (D199) and house sign-offs (D200): Content Settings parts.
 * The house style is what new cuts of every project using the settings start
 * with; after a change the latest cuts that don't follow it are offered "Use
 * the house style on the latest cut" (a checked change each). House sign-offs
 * are OKs every project using the settings needs before a video goes out; each
 * project ticks its own. A project that uses another's settings shows both
 * read-only. Every control is one op, the same as `metarush content style
 * show|set|apply` and `metarush content signoffs list|add|remove`.
 */

const STYLE_QUERIES: RemoteOp[] = ['content.style.show', 'content.show', 'project.show', 'revision.list', 'cut.show'];

type SetIn = OpInput<'content.style.set'>;

type Choice = { value: string; label: string };

const OWN = "MetaRush's own";

const CAPTIONS: Choice[] = [
  { value: '', label: 'Each cut decides' },
  { value: 'on', label: 'On' },
  { value: 'off', label: 'Off' },
];
// The vetted caption looks (D245), from schema/caption-looks.json.
const LOOKS: Choice[] = [{ value: '', label: `${OWN} (clean)` }, ...CAPTION_LOOKS.map((l) => ({ value: l.id, label: l.name }))];
const SIZES: Choice[] = [
  { value: '', label: `${OWN} (medium)` },
  { value: 'small', label: 'Small' },
  { value: 'medium', label: 'Medium' },
  { value: 'large', label: 'Large' },
];
const POSITIONS: Choice[] = [
  { value: '', label: `${OWN} (low on the picture)` },
  { value: 'bottom', label: 'At the bottom' },
  { value: 'lower', label: 'Low on the picture' },
  { value: 'middle', label: 'In the middle' },
  { value: 'top', label: 'At the top' },
];
const BAR_LOOKS: Choice[] = [
  { value: '', label: `${OWN} (a band across the picture)` },
  { value: 'band', label: 'A band across the picture' },
  { value: 'tab', label: 'A tab as wide as the names' },
];
const BAR_HEIGHTS: Choice[] = [
  { value: '', label: `${OWN} (full height)` },
  { value: 'full', label: 'Full height' },
  { value: 'half', label: 'About half as tall' },
];
const BAR_MOTION: Choice[] = [
  { value: '', label: `${OWN} (names fade in)` },
  { value: 'fade', label: 'Names fade in' },
  { value: 'slide', label: 'Names slide in' },
  { value: 'never', label: 'Never animates' },
];
const TITLES: Choice[] = [
  { value: '', label: 'Each cut decides' },
  { value: 'lower-third.clean', label: 'Clean' },
  { value: 'lower-third.cinematic', label: 'Cinematic' },
];
const FRAMING: Choice[] = [
  { value: '', label: `${OWN} (keeps faces in frame)` },
  { value: 'faces', label: 'Keeps faces in frame' },
  { value: 'follow', label: 'Follows movement' },
  { value: 'center', label: 'Fixed (the middle)' },
];
const BLENDS: Choice[] = [
  { value: '', label: `${OWN} (0.1 s)` },
  { value: '0', label: 'None' },
  { value: '0.05', label: '0.05 s' },
  { value: '0.1', label: '0.1 s' },
  { value: '0.15', label: '0.15 s' },
  { value: '0.2', label: '0.2 s' },
  { value: '0.3', label: '0.3 s' },
];
const LEVELS: Choice[] = [
  { value: '', label: `${OWN} (evened out)` },
  { value: 'on', label: 'Evened out' },
  { value: 'off', label: 'Left as recorded' },
];
const INTROS: Choice[] = [
  { value: '', label: 'None' },
  { value: 'title.card', label: 'Title card' },
  { value: 'slide.title', label: 'Title slide' },
];
const ENDS: Choice[] = [
  { value: '', label: 'None' },
  { value: 'endcard.cta', label: 'End card' },
  { value: 'slide.title', label: 'Title slide' },
];
const SECONDS = ['2', '3', '4', '5', '6', '8'];

const field = 'h-9 w-full min-w-0 rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';

function Pick({ label, value, choices, onChange, disabled, name }: { label: string; value: string; choices: Choice[]; onChange: (v: string) => void; disabled?: boolean; name: string }) {
  const id = useId();
  // A value set elsewhere (the CLI takes any number of seconds) still shows as it is.
  if (value && !choices.some((c) => c.value === value)) choices = [...choices, { value, label: /^\d/.test(value) ? `${value} s` : value }];
  return (
    <label htmlFor={id} className="block min-w-0">
      <span className="mb-1 block text-[12px] font-medium text-muted">{label}</span>
      <select id={id} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} className={field} data-style-field={name}>
        {choices.map((c) => (
          <option key={c.value} value={c.value}>
            {c.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className="min-w-0 rounded-[14px] p-3.5 hairline">
      <legend className="px-1 text-[12.5px] font-semibold text-fg">{title}</legend>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">{children}</div>
    </fieldset>
  );
}

/** The house style part: what new cuts start with, and the offer for the latest cuts. */
export function HouseStylePart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const q = useOp('content.style.show', { projectId });
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  // After a change: the latest cuts that don't follow the style (saved versions never change).
  const [offers, setOffers] = useState<StyleCutOffer[]>([]);
  useEffect(() => {
    if (offers.length) document.querySelector('[data-style-offers]')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }, [offers.length]);
  const refresh = () => {
    for (const k of STYLE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
  const save = async (key: string, input: OpInput<'content.style.set'>) => {
    setBusy(key);
    try {
      const out: ContentStyleOut = await call('content.style.set', { ...input, projectId });
      refresh();
      setOffers(out.latestCuts ?? []);
      toast({ tone: 'success', title: out.empty ? 'House style cleared' : 'House style saved' });
      return true;
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "The house style didn't change", body: [err.message, err.hint].filter(Boolean).join(' ') });
      return false;
    } finally {
      setBusy(null);
    }
  };
  const applyToCut = async (o: StyleCutOffer) => {
    setBusy(`apply:${o.projectId}`);
    try {
      const out = await call('content.style.apply', { projectId: o.projectId, instruction: 'Use the house style' });
      refresh();
      const problems = out.problems.filter((x) => x.severity === 'error');
      if (problems.length) toast({ tone: 'error', title: 'Not changed', body: problems.map((x) => x.message).join(' ') });
      else {
        setOffers((list) => list.filter((x) => x.projectId !== o.projectId));
        toast({ tone: 'success', title: out.noChange ? 'That cut follows the house style already' : `${o.projectName}'s latest cut follows the house style`, body: out.summary });
      }
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Not changed', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };

  if (q.isLoading) return <Skeleton className="h-40" />;
  if (q.error || !q.data) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  const st = d.style ?? {};
  const cap = st.captions ?? {};
  const bar = st.nameBar ?? {};
  const sound = st.sound ?? {};
  const on = cap.on === true ? 'on' : cap.on === false ? 'off' : '';
  const level = sound.levelSpeech === true ? 'on' : sound.levelSpeech === false ? 'off' : '';
  const blend = sound.cutBlend === undefined || sound.cutBlend === null ? '' : String(sound.cutBlend);
  const dis = !!busy;
  return (
    <div data-house-style data-style-empty={d.empty || undefined}>
      <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
        What new cuts of every project using these settings start with. Claude follows it too, and the quality check measures what it can (captions on, a name bar that never
        animates or is half as tall). Saved versions never change.
      </p>
      {d.lines.length ? (
        <ul className="mb-4 space-y-1 rounded-[12px] bg-inset px-3 py-2.5 text-[12.5px] text-fg" aria-label="The house style in words" data-style-lines>
          {d.lines.map((l) => (
            <li key={l} className="break-words">
              {l}
            </li>
          ))}
        </ul>
      ) : (
        <p className="mb-4 text-[12.5px] text-subtle">Nothing set: new cuts get MetaRush's own choices.</p>
      )}
      {offers.length && !readOnly ? (
        <div className="mb-4 rounded-[14px] bg-accent-soft/60 p-3.5" data-style-offers>
          <p className="text-[13px] font-medium text-fg">Use the house style on the latest cut</p>
          <p className="mt-0.5 text-[12px] text-muted">Saved versions never change. Each one below becomes a new version that follows the house style.</p>
          <ul className="mt-2 space-y-2">
            {offers.map((o) => (
              <li key={o.projectId} className="flex flex-wrap items-start gap-2 text-[12.5px]">
                <span className="min-w-0 flex-1 text-fg">
                  {o.projectName} <span className="text-subtle">· version {o.version}</span>
                  {o.changes.length ? <span className="mt-0.5 block text-[12px] text-muted">{o.changes.slice(0, 3).join(' ')}</span> : null}
                </span>
                <Button size="sm" variant="secondary" loading={busy === `apply:${o.projectId}`} onClick={() => void applyToCut(o)} data-style-offer={o.projectId}>
                  Use the house style
                </Button>
              </li>
            ))}
          </ul>
          <button type="button" className="mt-2 text-[12px] text-muted hover:text-fg" onClick={() => setOffers([])}>
            Not now
          </button>
        </div>
      ) : null}
      {readOnly ? null : (
        <div className="grid grid-cols-1 items-start gap-3 lg:grid-cols-2">
          <Group title="Captions">
            <Pick name="captions" label="New cuts" value={on} choices={CAPTIONS} disabled={dis} onChange={(v) => void save('captions', { captions: v as SetIn['captions'] })} />
            <Pick name="captionLook" label="Look" value={cap.look ?? ''} choices={LOOKS} disabled={dis} onChange={(v) => void save('look', { captionLook: v as SetIn['captionLook'] })} />
            <Pick name="captionSize" label="Size" value={cap.size ?? ''} choices={SIZES} disabled={dis} onChange={(v) => void save('size', { captionSize: v as SetIn['captionSize'] })} />
            <Pick
              name="captionPosition"
              label="Where"
              value={cap.position ?? ''}
              choices={POSITIONS}
              disabled={dis}
              onChange={(v) => void save('position', { captionPosition: v as SetIn['captionPosition'] })}
            />
          </Group>
          <Group title="Names on screen">
            <Pick name="nameBarLook" label="Name bar" value={bar.look ?? ''} choices={BAR_LOOKS} disabled={dis} onChange={(v) => void save('barLook', { nameBarLook: v as SetIn['nameBarLook'] })} />
            <Pick
              name="nameBarHeight"
              label="Name bar height"
              value={bar.height ?? ''}
              choices={BAR_HEIGHTS}
              disabled={dis}
              onChange={(v) => void save('barHeight', { nameBarHeight: v as SetIn['nameBarHeight'] })}
            />
            <Pick
              name="nameBarAnimate"
              label="Name bar animation"
              value={bar.animate ?? ''}
              choices={BAR_MOTION}
              disabled={dis}
              onChange={(v) => void save('barMotion', { nameBarAnimate: v as SetIn['nameBarAnimate'] })}
            />
            <Pick name="lowerThird" label="Name titles" value={st.lowerThird ?? ''} choices={TITLES} disabled={dis} onChange={(v) => void save('titles', { lowerThird: v as SetIn['lowerThird'] })} />
          </Group>
          <Group title="Picture and sound">
            <Pick name="framing" label="Framing" value={st.framing ?? ''} choices={FRAMING} disabled={dis} onChange={(v) => void save('framing', { framing: v as SetIn['framing'] })} />
            <Pick
              name="cutBlend"
              label="Blend at straight cuts"
              value={blend}
              choices={BLENDS}
              disabled={dis}
              onChange={(v) => void save('blend', v === '' ? { clear: ['cutBlend'] } : { cutBlend: Number(v) })}
            />
            <Pick name="levelSpeech" label="Voices" value={level} choices={LEVELS} disabled={dis} onChange={(v) => void save('level', { levelSpeech: v as SetIn['levelSpeech'] })} />
          </Group>
          <CardForm kind="intro" card={st.intro} busy={dis} onSave={(c) => save('intro', { intro: c })} />
          <CardForm kind="end" card={st.endCard} busy={dis} onSave={(c) => save('end', { endCard: c })} />
        </div>
      )}
    </div>
  );
}

/** The house's intro or end card: which card, and its words. */
function CardForm({ kind, card, busy, onSave }: { kind: 'intro' | 'end'; card?: StyleCard; busy: boolean; onSave: (c: StyleCard) => Promise<boolean> }) {
  const intro = kind === 'intro';
  const [template, setTemplate] = useState<string>(card?.template ?? '');
  const [title, setTitle] = useState(card?.title ?? '');
  const [subtitle, setSubtitle] = useState(card?.subtitle ?? '');
  const [cta, setCta] = useState(card?.cta ?? '');
  const [handle, setHandle] = useState(card?.handle ?? '');
  const [seconds, setSeconds] = useState(card?.seconds ? String(card.seconds) : intro ? '3' : '4');
  const key = JSON.stringify(card ?? null);
  useEffect(() => {
    setTemplate(card?.template ?? '');
    setTitle(card?.title ?? '');
    setSubtitle(card?.subtitle ?? '');
    setCta(card?.cta ?? '');
    setHandle(card?.handle ?? '');
    setSeconds(card?.seconds ? String(card.seconds) : intro ? '3' : '4');
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  const tid = useId();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void onSave({ template: template as StyleCard['template'], seconds: Number(seconds), title: title.trim(), subtitle: subtitle.trim(), cta: cta.trim(), handle: handle.trim() });
  };
  const choose = (v: string) => {
    setTemplate(v);
    if (!v) void onSave({ template: '' });
  };
  const cardWords = template === 'endcard.cta';
  return (
    <form onSubmit={submit} className="min-w-0 rounded-[14px] p-3.5 hairline" data-style-card={kind}>
      <p className="mb-2 text-[12.5px] font-semibold text-fg">{intro ? 'Intro card' : 'End card'}</p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Pick name={kind === 'intro' ? 'intro' : 'endCard'} label="Card" value={template} choices={intro ? INTROS : ENDS} disabled={busy} onChange={choose} />
        {template ? (
          <Pick name={`${kind}Seconds`} label="Stays for" value={seconds} choices={SECONDS.map((s) => ({ value: s, label: `${s} s` }))} disabled={busy} onChange={setSeconds} />
        ) : null}
      </div>
      {template ? (
        <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label htmlFor={`${tid}-t`} className="block min-w-0 sm:col-span-2">
            <span className="mb-1 block text-[12px] font-medium text-muted">{intro ? 'Title' : 'Headline'}</span>
            <input
              id={`${tid}-t`}
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              maxLength={80}
              placeholder={intro ? "Each cut's title" : 'Volunteer with us'}
              className={field}
              data-style-card-title
            />
          </label>
          <label htmlFor={`${tid}-s`} className="block min-w-0 sm:col-span-2">
            <span className="mb-1 block text-[12px] font-medium text-muted">A line under it</span>
            <input id={`${tid}-s`} value={subtitle} onChange={(e) => setSubtitle(e.target.value)} maxLength={120} className={field} />
          </label>
          {cardWords ? (
            <>
              <label htmlFor={`${tid}-c`} className="block min-w-0">
                <span className="mb-1 block text-[12px] font-medium text-muted">Call to action</span>
                <input id={`${tid}-c`} value={cta} onChange={(e) => setCta(e.target.value)} maxLength={40} placeholder="Sign up" className={field} />
              </label>
              <label htmlFor={`${tid}-h`} className="block min-w-0">
                <span className="mb-1 block text-[12px] font-medium text-muted">Handle</span>
                <input id={`${tid}-h`} value={handle} onChange={(e) => setHandle(e.target.value)} maxLength={40} placeholder="@yourname" className={field} />
              </label>
            </>
          ) : null}
          <div className="sm:col-span-2">
            <Button type="submit" size="sm" variant="secondary" disabled={busy || (!intro && !title.trim())} data-style-card-save={kind}>
              Save the {intro ? 'intro' : 'end'} card
            </Button>
            {!intro && !title.trim() ? <span className="ml-2 text-[12px] text-subtle">An end card needs a headline.</span> : null}
          </div>
        </div>
      ) : (
        <p className="mt-2 text-[12px] text-subtle">{intro ? 'New cuts open without a house card.' : 'New cuts end without a house card.'}</p>
      )}
    </form>
  );
}

/* ---------- House sign-offs ---------- */

const SIGNOFF_QUERIES: RemoteOp[] = ['content.signoffs.list', 'content.show', 'cut.signoffs.list', 'project.show'];

/** The house sign-offs part: OKs every project using the settings needs. */
export function HouseSignoffsPart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const q = useOp('content.signoffs.list', { projectId });
  const toast = useToast();
  const add = useOpMutation('content.signoffs.add', { invalidate: SIGNOFF_QUERIES });
  const remove = useOpMutation('content.signoffs.remove', { invalidate: SIGNOFF_QUERIES });
  const [text, setText] = useState('');
  const list: Signoff[] = q.data?.signoffs ?? [];
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    add.mutate(
      { projectId, text: t },
      {
        onSuccess: (out) => {
          setText('');
          toast({ tone: 'success', title: out.summary });
        },
        onError: (err) => toast({ tone: 'error', title: 'Not added', body: err.message }),
      },
    );
  };
  const takeOut = (s: Signoff) =>
    remove.mutate(
      { projectId, signoff: s.id },
      {
        onSuccess: () => toast({ title: 'House sign-off taken out', body: `“${s.text}”`, action: { label: 'Undo', onClick: () => add.mutate({ projectId, text: s.text }) } }),
        onError: (e) => toast({ tone: 'error', title: 'The house sign-off is still there', body: e.message }),
      },
    );
  return (
    <div data-house-signoffs>
      <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
        OKs every project using these settings needs before a video goes out (“Legal OKs it before export”). Each project ticks its own under This project; MetaRush reminds before
        approving or downloading the final video, and never stops you.
      </p>
      {q.isLoading ? (
        <Skeleton className="h-12" />
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : list.length ? (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="House sign-offs">
          {list.map((s) => (
            <li key={s.id} className="flex items-start gap-2.5 px-3 py-2.5" data-house-signoff={s.text}>
              <span className="mt-[3px] shrink-0 text-subtle" aria-hidden>
                <Signature size={14} />
              </span>
              <div className="min-w-0 flex-1">
                <Theirs className="block break-words text-[13.5px] leading-snug text-fg">{s.text}</Theirs>
                <p className="mt-0.5 text-[12px] text-subtle" title={absolute(s.addedAt)}>
                  added {relative(s.addedAt)}
                </p>
              </div>
              {readOnly ? null : (
                <button
                  type="button"
                  onClick={() => takeOut(s)}
                  aria-label={`Take the house sign-off “${s.text}” out`}
                  className="grid h-7 w-7 shrink-0 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg"
                >
                  <X size={13} />
                </button>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] text-subtle">No house sign-offs yet.</p>
      )}
      {readOnly ? null : (
        <form onSubmit={submit} className="mt-3 flex flex-wrap items-center gap-2" data-house-signoff-add>
          <input
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="Legal OKs it before export"
            aria-label="A new house sign-off: who OKs what"
            maxLength={200}
            className={cx(field, 'flex-1 basis-[180px]')}
          />
          <Button type="submit" variant="secondary" icon={<Plus size={13} />} disabled={!text.trim()} loading={add.isPending}>
            Add
          </Button>
        </form>
      )}
    </div>
  );
}
