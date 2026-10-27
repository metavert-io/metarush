import { useEffect, useId, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowSquareOut, HandPalm, Layout, ListChecks, Microphone, PaintBrush, Palette, Plus, Seal, SealCheck, ShieldCheck, Sliders, Sparkle, TextAa, X } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp, useOpMutation } from '../../api/queries';
import type { ContentShowOut, ProjectRule, RemoteOp } from '../../api/types';
import { absolute, plural, relative } from '../../lib/format';
import { BrandEditor } from '../../screens/Brand';
import { HowItSounds, VoiceChooser } from '../../screens/studio/voiceover/AIVoice';
import { SpellingList } from '../transcripts/SpellingList';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { HouseSignoffsPart, HouseStylePart } from './HouseParts';
import { AIServicesPart } from './AIServicesPart';
import { LeavePart } from './LeavePart';
import { LayoutPackPart } from './LayoutPackPart';
import { GatePart } from './GatePart';
import { measureWords } from './ProjectMemory';

/*
 * Content Settings (D192, D193, D198–D200): what every video made with them
 * uses — the brand (with its named logos), the spelling list, the narration
 * voice, house rules, the house style and house sign-offs. A project
 * uses the default (the project whose "Use for new projects" is ticked),
 * another project's (followed live) or its own. A project that uses another's
 * shows them read-only ("From fernbrook — edit them there, or give this project its
 * own"); changing settings other projects use says so ("Also used by 3
 * projects"). Every control is one op, the same as `metarush content
 * show|follow|own|default` and the parts' own commands.
 */

const CONTENT_QUERIES: RemoteOp[] = [
  'content.show',
  'content.leave',
  'content.rules.list',
  'content.style.show',
  'content.signoffs.list',
  'content.pack.show',
  'content.pack.preview',
  'delivery.gate',
  'delivery.ready',
  'cut.signoffs.list',
  'brand.show',
  'transcript.spelling.list',
  'provider.status',
  'project.show',
  'project.list',
];

export function ContentSettings({ projectId, part }: { projectId: string; part?: string }) {
  const q = useOp('content.show', { projectId });
  const data = q.data;
  useEffect(() => {
    if (!part || !data) return;
    const t = setTimeout(() => document.getElementById(`content-${part}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 150);
    return () => clearTimeout(t);
  }, [part, !!data]); // eslint-disable-line react-hooks/exhaustive-deps
  if (q.isLoading) return <Skeleton className="h-48" />;
  if (q.error || !data) return <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />;
  const ro = data.readOnly;
  return (
    <section className="space-y-5" aria-labelledby="content-settings-h" data-content-settings data-uses={data.uses} data-read-only={ro || undefined}>
      <header className="max-w-[78ch]">
        <h2 id="content-settings-h" className="text-[17px] font-semibold tracking-[-0.015em] text-fg">
          Content Settings
        </h2>
        <p className="mt-1 text-[13px] leading-relaxed text-muted">
          The brand, spelling list, narration voice, house rules, house style, sign-offs and layout pack every video made with them uses. Several projects can share one set: changes show up in
          all of them.
        </p>
      </header>
      <UsesPanel data={data} />
      <div className="grid grid-cols-1 items-start gap-5">
        <Part id="brand" icon={<Palette size={16} />} title="Brand" summary={summaryOf(data, 'brand')} readOnly={ro} from={data.from?.name}>
          <BrandEditor projectId={projectId} />
        </Part>
        <div className="grid grid-cols-1 items-start gap-5 lg:grid-cols-2">
          <Part id="spelling" icon={<TextAa size={16} />} title="Spelling" summary={summaryOf(data, 'spelling')} readOnly={ro} from={data.from?.name}>
            <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
              How names and terms are always written. New transcripts of every project using these settings are corrected by it; a clip in several projects uses all their lists
              {data.isDefault || data.default?.id === data.from?.id ? ', and footage in no project uses these' : ''}.
            </p>
            <SpellingList projectId={projectId} compact />
          </Part>
          <Part id="voice" icon={<Microphone size={16} />} title="Narration voice" summary={summaryOf(data, 'voice')} readOnly={ro} from={data.from?.name}>
            <VoicePart projectId={projectId} readOnly={ro} />
          </Part>
        </div>
        <Part id="rules" icon={<ListChecks size={16} />} title="House rules" summary={summaryOf(data, 'rules')} readOnly={ro} from={data.from?.name}>
          <HouseRules projectId={projectId} readOnly={ro} />
        </Part>
        <Part id="style" icon={<PaintBrush size={16} />} title="House style" summary={summaryOf(data, 'style')} readOnly={ro} from={data.from?.name}>
          <HouseStylePart projectId={projectId} readOnly={ro} />
        </Part>
        <Part id="signoffs" icon={<SealCheck size={16} />} title="House sign-offs" summary={summaryOf(data, 'signoffs')} readOnly={ro} from={data.from?.name}>
          <HouseSignoffsPart projectId={projectId} readOnly={ro} />
        </Part>
        <Part id="pack" icon={<Layout size={16} />} title="Layout pack" summary={summaryOf(data, 'pack')} readOnly={ro} from={data.from?.name}>
          <LayoutPackPart key={projectId} projectId={projectId} readOnly={ro} />
        </Part>
        <Part id="gate" icon={<HandPalm size={16} />} title="Before it goes out" summary={summaryOf(data, 'gate')} readOnly={ro} from={data.from?.name}>
          <GatePart projectId={projectId} readOnly={ro} />
        </Part>
        <Part id="aiServices" icon={<Sparkle size={16} />} title="AI services" summary={summaryOf(data, 'aiServices')} readOnly={ro} from={data.from?.name}>
          <AIServicesPart projectId={projectId} readOnly={ro} />
        </Part>
        <Part id="leave" icon={<ShieldCheck size={16} />} title="What may leave" summary={summaryOf(data, 'leave')} readOnly={ro} from={data.from?.name}>
          <LeavePart projectId={projectId} readOnly={ro} />
        </Part>
      </div>
    </section>
  );
}

function summaryOf(d: ContentShowOut, name: string): string {
  return d.parts.find((p) => p.name === name)?.summary ?? '';
}

/** Whose settings the project uses, "Use for new projects", and who else uses them. */
function UsesPanel({ data }: { data: ContentShowOut }) {
  const project = data.project!;
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const [busy, setBusy] = useState<string | null>(null);
  const followId = useId();
  // "Use for new projects" shows the tick at once, then the saved answer (unticked again if it failed).
  const [ticked, setTicked] = useState(false);
  useEffect(() => setTicked(false), [data.isDefault]);
  const run = async (key: string, fn: () => Promise<ContentShowOut>) => {
    setBusy(key);
    try {
      const out = await fn();
      for (const k of CONTENT_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.changed || out.summary });
      return true;
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Nothing changed', body: [err.message, err.hint].filter(Boolean).join(' ') });
      return false;
    } finally {
      setBusy(null);
    }
  };
  const useDefault = () => run('default', () => call('content.follow', { projectId: project.id, useDefault: true }));
  const follow = (id: string) => id && run('follow', () => call('content.follow', { projectId: project.id, follow: id }));
  const own = async () => {
    if (data.keptAside) return run('own', () => call('content.own', { projectId: project.id }));
    const how = await dialog.open<'empty' | 'copy'>(
      (close) => (
        <div className="space-y-4">
          <p className="text-[13px] leading-relaxed text-muted">
            Its brand, spelling list, narration voice, house rules, house style and sign-offs become its own: changes to them stay in this project
            {data.from ? `, and changes to ${data.from.name}'s no longer show up here` : ''}.
          </p>
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="ghost" onClick={() => close()}>
              Cancel
            </Button>
            <Button variant="secondary" onClick={() => close('empty')} data-own="empty">
              Start empty
            </Button>
            {data.from ? (
              <Button variant="primary" onClick={() => close('copy')} data-own="copy">
                Start from a copy of {data.from.name}'s
              </Button>
            ) : null}
          </div>
        </div>
      ),
      { title: `Give ${project.name} its own settings?`, size: 'md' },
    );
    if (how) void run('own', () => call('content.own', { projectId: project.id, ...(how === 'copy' ? { copy: true } : {}) }));
  };
  const setDefault = async () => {
    setTicked(true);
    if (!(await run('setDefault', () => call('content.setDefault', { projectId: project.id })))) setTicked(false);
  };
  const defName = data.default?.name;
  const usesDefault = data.uses === 'default';
  const usesOther = data.uses === 'project';
  const usesOwn = data.uses === 'own';
  const option = (active: boolean) =>
    cx('flex min-w-0 items-center gap-2 rounded-[11px] px-3 py-2 text-left text-[13px] transition-colors hairline', active ? 'bg-accent-soft text-accent-text ring-1 ring-accent-line' : 'text-fg hover:bg-hover');
  return (
    <div className="panel space-y-4 p-4 sm:p-5" data-content-uses>
      <div>
        <div className="mb-2 flex items-center gap-2 text-[13px] font-medium text-fg">
          <Sliders size={15} className="text-subtle" aria-hidden /> Uses
        </div>
        <div role="radiogroup" aria-label="Whose settings this project uses" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          <button
            type="button"
            role="radio"
            aria-checked={usesDefault}
            className={option(usesDefault)}
            disabled={!!busy || data.isDefault}
            onClick={() => !usesDefault && void useDefault()}
            data-uses-option="default"
            title={data.isDefault ? 'Its settings are the default: tick “Use for new projects” on another project first' : undefined}
          >
            <span className="min-w-0">
              <span className="block font-medium">The default</span>
              <span className="block truncate text-[12px] text-muted">{defName ? (data.isDefault ? 'its own are the default' : `${defName}'s`) : 'none yet'}</span>
            </span>
          </button>
          <div className={option(usesOther)} data-uses-option="project">
            <span className="min-w-0 flex-1">
              <label htmlFor={followId} className="block font-medium">
                Another project's
              </label>
              <select
                id={followId}
                value={usesOther ? data.follows?.id ?? '' : ''}
                disabled={!!busy || data.choices.length === 0}
                onChange={(e) => void follow(e.target.value)}
                className="mt-0.5 h-7 w-full min-w-0 rounded-[8px] bg-panel px-2 text-[12.5px] text-fg hairline"
                data-follow-select
              >
                <option value="">{data.choices.length ? 'Choose a project…' : 'No other project yet'}</option>
                {data.choices.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </span>
          </div>
          <button type="button" role="radio" aria-checked={usesOwn} className={option(usesOwn)} disabled={!!busy} onClick={() => !usesOwn && void own()} data-uses-option="own">
            <span className="min-w-0">
              <span className="block font-medium">Its own</span>
              <span className="block truncate text-[12px] text-muted">{usesOwn ? 'changed here' : data.keptAside ? 'the ones it had before' : 'empty, or a copy'}</span>
            </span>
          </button>
        </div>
      </div>
      {data.readOnly && data.from ? (
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[11px] bg-inset px-3 py-2 text-[12.5px] text-muted" data-content-from>
          <span>
            From <Theirs className="font-medium text-fg">{data.from.name}</Theirs>
            {data.through?.length ? <> (through {data.through.map((t) => t.name).join(', ')})</> : null} — edit them there, or give this project its own.
          </span>
          <Link to={`/p/${data.from.id}?tab=settings`} className="inline-flex items-center gap-1 font-medium text-accent-text hover:underline">
            Open {data.from.name}'s settings <ArrowSquareOut size={12} />
          </Link>
        </p>
      ) : null}
      {data.readOnly && !data.from ? <p className="text-[12.5px] text-muted">No project's settings are the default yet: give this project its own, or tick “Use for new projects” on a project.</p> : null}
      <label className="flex items-start gap-2 text-[13px] text-fg" data-use-for-new>
        <input
          type="checkbox"
          checked={data.isDefault || ticked}
          disabled={data.isDefault || ticked || !!busy}
          onChange={() => void setDefault()}
          className="mt-[3px] accent-[var(--accent)]"
          aria-describedby={`${followId}-def`}
        />
        <span>
          Use for new projects
          <span id={`${followId}-def`} className="block text-[12px] text-muted">
            {data.isDefault
              ? 'New projects (and footage in no project) use these settings. To move it, tick it on another project.'
              : `New projects use ${defName ? `${defName}'s` : 'no project’s'} settings now. Ticking this moves that here.`}
          </span>
        </span>
      </label>
      {data.usedBy.length ? (
        <p className="text-[12.5px] text-muted" data-also-used-by={data.usedBy.length}>
          <Badge tone="accent">Also used by {plural(data.usedBy.length, 'project')}</Badge>{' '}
          <span className="text-subtle">{data.usedBy.map((u) => u.name).join(', ')}</span>
        </p>
      ) : null}
    </div>
  );
}

function Part({ id, icon, title, summary, readOnly, from, children }: { id: string; icon: ReactNode; title: string; summary: string; readOnly: boolean; from?: string; children: ReactNode }) {
  const hid = useId();
  return (
    <section id={`content-${id}`} className="panel min-w-0 scroll-mt-32 p-4 sm:p-5" aria-labelledby={hid} data-content-part={id}>
      <div className="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-subtle" aria-hidden>
          {icon}
        </span>
        <h3 id={hid} className="text-[14.5px] font-semibold tracking-[-0.01em] text-fg">
          {title}
        </h3>
        {summary ? <span className="text-[12px] text-subtle">{summary}</span> : null}
        {readOnly && from ? <Badge>from {from}</Badge> : null}
      </div>
      {children}
    </section>
  );
}

/* ---------- Narration voice ---------- */

const EL = 'elevenlabs' as const;

function VoicePart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const st = useOp('provider.status', { name: EL, projectId });
  const qc = useQueryClient();
  const toast = useToast();
  const [choosing, setChoosing] = useState(false);
  const eff = st.data?.effective;
  const keyed = st.data?.status.configured;
  const save = async (input: Record<string, unknown>, done: string) => {
    try {
      const out = await call('provider.settings', { name: EL, scope: 'project', projectId, ...input });
      for (const k of CONTENT_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: done, body: out.note || undefined });
      return out;
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'That change was not saved', body: [err.message, err.hint].filter(Boolean).join(' ') });
      return undefined;
    }
  };
  if (st.isLoading) return <Skeleton className="h-16" />;
  if (st.error) return <ErrorPanel compact error={st.error} />;
  const favs = st.data?.settings?.favorites ?? [];
  return (
    <div data-voice-part>
      <p className="text-[13px] text-fg">
        {eff?.voiceName ? (
          <>
            Voice: <span className="font-medium">{eff.voiceName}</span>
          </>
        ) : eff?.voiceId ? (
          'A chosen voice'
        ) : (
          'No voice chosen yet'
        )}
        {eff?.modelName ? <span className="text-muted"> · {eff.modelName}</span> : null}
      </p>
      <p className="mt-1 text-[12px] leading-relaxed text-muted">
        The AI voice that reads narration lines in every project using these settings; a line can still have a voice of its own. Spending limits stay with each project (its Voiceover
        tab).
      </p>
      {favs.length ? <p className="mt-1 text-[12px] text-subtle">Favourite voices: {favs.map((f) => f.name || f.voiceId).join(', ')}</p> : null}
      {readOnly ? null : !keyed ? (
        <p className="mt-2 text-[12px] text-subtle">
          Choosing a voice needs an ElevenLabs key on this computer (
          <Link to="/settings/integrations" className="text-accent-text hover:underline">
            Settings → Integrations
          </Link>
          ).
        </p>
      ) : (
        <>
          <Button size="sm" variant="secondary" className="mt-2" onClick={() => setChoosing((v) => !v)} aria-expanded={choosing}>
            {choosing ? 'Done choosing' : eff?.voiceId ? 'Change voice' : 'Choose a voice'}
          </Button>
          {choosing ? <VoiceChooser current={eff?.voiceId} onPick={(v) => void save({ voice: v.id }, `The voice is ${v.name} now`).then(() => setChoosing(false))} /> : null}
          {eff ? (
            <HowItSounds
              speed={eff.settings.speed}
              stability={eff.settings.stability}
              model={eff.from.model === 'settings' ? eff.modelId : undefined}
              inherited={eff.from.model !== 'settings' ? eff.modelName : undefined}
              onSave={(input, done) => save(input, done)}
              saveLabel="Save"
            />
          ) : null}
        </>
      )}
    </div>
  );
}

/* ---------- House rules ---------- */

const RULE_QUERIES: RemoteOp[] = ['content.rules.list', 'content.show', 'project.show'];

function HouseRules({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const q = useOp('content.rules.list', { projectId });
  const toast = useToast();
  const add = useOpMutation('content.rules.add', { invalidate: RULE_QUERIES });
  const remove = useOpMutation('content.rules.remove', { invalidate: RULE_QUERIES });
  const [text, setText] = useState('');
  const [check, setCheck] = useState(false);
  const [promise, setPromise] = useState(false);
  const rules = q.data?.rules ?? [];
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    add.mutate(
      { projectId, text: t, ...(check ? { check: true } : {}), ...(promise ? { promise: true } : {}) },
      {
        onSuccess: (out) => {
          setText('');
          setCheck(false);
          setPromise(false);
          toast({ tone: 'success', title: out.summary });
        },
        onError: (err) => toast({ tone: 'error', title: 'Not added', body: err.message }),
      },
    );
  };
  const takeOut = (r: ProjectRule) =>
    remove.mutate(
      { projectId, rule: r.id },
      {
        onSuccess: () =>
          toast({
            title: 'House rule taken out',
            body: `“${r.text}”`,
            action: {
              label: 'Undo',
              onClick: () => add.mutate({ projectId, text: r.text, ...(r.check ? { check: true } : {}), ...(r.promise ? { promise: true } : {}), ...(r.measure ? { measure: r.measure } : {}) }),
            },
          }),
        onError: (e) => toast({ tone: 'error', title: 'The house rule is still there', body: e.message }),
      },
    );
  return (
    <div data-house-rules>
      <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
        Rules for every video using these settings (“The name bar never animates”). Claude follows them and the quality check measures the ones it can; a project's own rules are below,
        under This project.
      </p>
      {q.isLoading ? (
        <Skeleton className="h-12" />
      ) : rules.length ? (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="House rules">
          {rules.map((r) => (
            <li key={r.id} className="flex items-start gap-2.5 px-3 py-2.5" data-house-rule={r.text}>
              <span className="mt-[3px] shrink-0 text-subtle" aria-hidden>
                {r.promise ? <Seal size={14} /> : r.check ? <ShieldCheck size={14} /> : <ListChecks size={14} />}
              </span>
              <div className="min-w-0 flex-1">
                <Theirs className="block break-words text-[13.5px] leading-snug text-fg">{r.text}</Theirs>
                <p className="mt-0.5 text-[12px] leading-relaxed text-subtle">
                  {r.promise ? <Badge tone="accent" className="mr-1.5">Promise to the client</Badge> : null}
                  {measureWords(r.measure) ? `The quality check measures it: ${measureWords(r.measure)}` : r.check ? 'Checked in every quality check' : 'Claude follows it'}
                  <span className="whitespace-nowrap" title={absolute(r.addedAt)}>
                    {' '}
                    · added {relative(r.addedAt)}
                  </span>
                </p>
              </div>
              {readOnly ? null : (
                <button
                  type="button"
                  onClick={() => takeOut(r)}
                  aria-label={`Take the house rule “${r.text}” out`}
                  className="grid h-7 w-7 shrink-0 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg"
                >
                  <X size={13} />
                </button>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] text-subtle">No house rules yet.</p>
      )}
      {readOnly ? null : (
        <form onSubmit={submit} className="mt-3 flex flex-wrap items-center gap-2" data-house-rule-add>
          <input
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="The name bar never animates"
            aria-label="A new house rule, in your words"
            maxLength={300}
            className="h-9 min-w-0 flex-1 basis-[180px] rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
          <Button type="submit" variant="secondary" icon={<Plus size={13} />} disabled={!text.trim()} loading={add.isPending}>
            Add
          </Button>
          <label className="flex basis-full items-center gap-1.5 text-[12px] text-muted">
            <input type="checkbox" checked={check} onChange={(e) => setCheck(e.target.checked)} className="accent-[var(--accent)]" />
            It's a problem to check for in every quality check
          </label>
          <label className="flex basis-full items-center gap-1.5 text-[12px] text-muted">
            <input type="checkbox" checked={promise} onChange={(e) => setPromise(e.target.checked)} className="accent-[var(--accent)]" data-house-rule-promise />
            It's a promise to the client: a version that breaks it waits before it goes out
          </label>
        </form>
      )}
    </div>
  );
}
