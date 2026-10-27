import { useEffect, useId, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle, Eye, EyeSlash, FilmSlate, Key, LockKey, Trash, WarningCircle, Waveform } from '@phosphor-icons/react';
import { call, toApiError, type ApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { ProviderName, ProviderStatus, ProviderUsage, RemoteOp } from '../../api/types';
import { absolute, relative, titleCase } from '../../lib/format';
import { resetSelection } from '../../lib/workbench';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, ErrorPanel, Freshness, Progress, Skeleton, Spinner, type Tone } from '../../ui/primitives';
import { OwnerOnly, OwnersNote } from '../../components/OwnerOnly';
import { Details } from './Details';
import { ElevenLabsSettings } from './ElevenLabsSettings';
import { GenerativeVideoSettings } from './GenerativeVideoSettings';

/*
 * Settings → Integrations: outside services that need an API key (ElevenLabs,
 * Google Gemini for generated video); Looks and consent follow on the same page.
 * Keys and the team's choices and limits are the owners' (D191); a project's
 * own choices and budget stay open to everyone.
 *
 * A key travels once from this form to the local daemon (authenticated session,
 * CSRF header) and is saved on this computer only: the OS keychain, or the
 * installation's private .env file. Nothing here can read a saved key back —
 * the daemon only ever answers with "configured" and the last four characters.
 * Keys go through call() directly rather than a mutation hook, so they are not
 * kept in the query cache after the request.
 */

/** Queries whose answers change when a key is added, replaced or removed. */
export const PROVIDER_QUERIES: RemoteOp[] = ['provider.list', 'provider.status', 'voiceover.voices', 'voiceover.models', 'generate.settings', 'generate.estimate'];

/** Settings → Integrations has one section per kind of service. */
const SECTIONS: { category: ProviderStatus['category']; title: string; blurb: string }[] = [
  { category: 'voice', title: 'AI voice and sound', blurb: 'Narration read by an AI voice, with word timing for captions, and voices made clearer in the cloud.' },
  {
    category: 'video',
    title: 'Generative video',
    blurb: 'New shots made from a few words, from a frame of your footage or between two frames. Every shot is priced before it is made, lands in Sources as a new clip and is marked as AI-generated everywhere.',
  },
  {
    category: 'presenter',
    title: 'Presenters',
    blurb: 'A presenter reads your lines on camera: a person’s own, made with their consent recorded on camera, speaking your own sound. Every clip is priced first and marked as an AI presenter everywhere.',
  },
];

/** Settings → Integrations (D184: a section of Settings, which gives it its heading). */
export function IntegrationsPage() {
  const list = useOp('provider.list', {});
  useEffect(() => resetSelection({}), []);
  const providers = list.data?.providers ?? [];

  return (
    <div data-integrations>
      <div className="mb-8 flex flex-wrap items-start gap-3">
        <p className="min-w-0 max-w-[70ch] flex-1 text-[13px] leading-relaxed text-muted">
          Each computer keeps its own key, so add one on every computer that should use the service.
        </p>
        <Freshness className="ml-auto" at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} />
      </div>

      <OwnersNote className="mb-6" />
      {list.error ? <ErrorPanel className="mb-6" error={list.error} onRetry={() => void list.refetch()} /> : null}
      {list.isLoading ? <Skeleton className="h-56 rounded-[16px]" /> : null}

      <div className="space-y-10">
        {SECTIONS.map((sec) => {
          const ps = providers.filter((p) => (p.category ?? 'voice') === sec.category);
          if (ps.length === 0) return null;
          return (
            <section key={sec.category} aria-labelledby={`int-${sec.category}`} className="space-y-4" data-integrations-section={sec.category}>
              <div>
                <h2 id={`int-${sec.category}`} className="text-[18px] font-semibold tracking-[-0.02em]">
                  {sec.title}
                </h2>
                <p className="mt-1 max-w-[76ch] text-[13px] leading-relaxed text-muted">{sec.blurb}</p>
              </div>
              {ps.map((p) => (
                <div key={p.name} className="space-y-6">
                  <OwnerOnly>
                    <ProviderCard status={p} />
                  </OwnerOnly>
                  {p.name === 'elevenlabs' && p.configured ? <ElevenLabsSettings /> : null}
                  {p.category === 'video' && p.configured ? <GenerativeVideoSettings /> : null}
                </div>
              ))}
            </section>
          );
        })}
      </div>

      <p className="mt-8 flex max-w-[80ch] items-start gap-2 text-[12px] leading-relaxed text-subtle">
        <LockKey size={13} className="mt-[2px] shrink-0" />
        Keys stay on this computer and are only ever sent to the service they belong to. They are not saved in your workspace, never shown again after you save them, and never
        written to logs or the activity history.
      </p>
    </div>
  );
}

/* ---------- Provider card ---------- */

function health(s: ProviderStatus): { tone: Tone; label: string; icon?: ReactNode } {
  if (!s.configured) return { tone: 'neutral', label: 'Not set up' };
  if (s.lastTest && !s.lastTest.ok) return { tone: 'danger', label: 'Key not working', icon: <WarningCircle size={12} weight="fill" /> };
  if (s.lastTest?.ok) return { tone: 'ok', label: 'Connected', icon: <CheckCircle size={12} weight="fill" /> };
  return { tone: 'accent', label: 'Key saved, not checked yet' };
}

/** Where the key lives, in plain words. */
function whereKept(s: Pick<ProviderStatus, 'source' | 'secretMode'>): string {
  switch (s.source) {
    case 'keychain':
      return "Saved in this computer's keychain";
    case 'file':
      return "Saved in a private file in MetaRush's folder on this computer";
    case 'environment':
      return "Read from this computer's environment settings";
    default:
      return s.secretMode === 'env' ? "A key you add is saved in a private file in MetaRush's folder on this computer" : "A key you add is saved in this computer's keychain";
  }
}

function usageLine(u: ProviderUsage): string {
  const unit = u.unit || 'credits';
  const parts = [`${u.used.toLocaleString()} of ${u.limit.toLocaleString()} ${unit} used`];
  if (u.plan) parts.unshift(`${titleCase(u.plan)} plan`);
  if (u.resetsAt) parts.push(`resets ${new Date(u.resetsAt).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}`);
  return parts.join(' · ');
}

function ProviderCard({ status: s }: { status: ProviderStatus }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [checking, setChecking] = useState(false);
  const [removing, setRemoving] = useState(false);
  const h = health(s);
  const name = s.name as ProviderName;
  const usage = s.lastTest?.usage;
  const refresh = () => PROVIDER_QUERIES.forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));

  const addKey = async () => {
    const saved = await dialog.open<boolean>((close) => <KeyDialog status={s} onClose={close} />, {
      title: s.configured ? `Replace your ${s.title} key` : `Add your ${s.title} key`,
    });
    if (!saved) return;
    refresh();
    toast({ title: `${s.title} is connected`, body: 'The key is saved on this computer.', tone: 'success' });
  };

  const check = async () => {
    setChecking(true);
    try {
      const r = await call('provider.test', { name });
      refresh();
      toast(
        r.result.ok
          ? { title: `Your ${s.title} key works`, body: r.result.message, tone: 'success' }
          : { title: `${s.title} did not accept the key`, body: r.result.message, tone: 'error' },
      );
    } catch (e) {
      toast({ title: `Could not check the ${s.title} key`, body: toApiError(e).message, tone: 'error' });
    } finally {
      setChecking(false);
    }
  };

  const remove = async () => {
    const ok = await dialog.confirm({
      title: `Remove the ${s.title} key from this computer?`,
      body: (
        <>
          MetaRush on this computer stops using {s.title} until a key is added again. {s.category === 'video' ? 'Clips you already made' : 'Narration you already made'} stays in your projects, and other
          computers keep their own keys. The key still works in your {s.title} account; delete it there if nobody needs it any more.
        </>
      ),
      confirmLabel: 'Remove key',
      tone: 'danger',
    });
    if (!ok) return;
    setRemoving(true);
    try {
      await call('provider.remove', { name });
      refresh();
      toast({ title: `${s.title} key removed from this computer`, tone: 'success' });
    } catch (e) {
      toast({ title: 'Could not remove the key', body: toApiError(e).message, tone: 'error' });
    } finally {
      setRemoving(false);
    }
  };

  return (
    <section className="panel p-5" aria-labelledby={`prov-${s.name}`} data-provider={s.name}>
      <div className="flex flex-wrap items-start gap-x-4 gap-y-3">
        <div className="grid h-10 w-10 shrink-0 place-items-center rounded-[12px] bg-hover text-muted" aria-hidden>
          {s.category === 'video' ? <FilmSlate size={19} /> : <Waveform size={19} />}
        </div>
        <div className="min-w-[220px] flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 id={`prov-${s.name}`} className="text-[16px] font-semibold tracking-[-0.01em]">
              {s.title}
            </h3>
            <Badge tone={h.tone} icon={h.icon}>
              {h.label}
            </Badge>
          </div>
          <p className="mt-1 max-w-[68ch] text-[13.5px] leading-relaxed text-muted">{s.summary}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {s.configured ? (
            <>
              <Button size="sm" onClick={() => void check()} loading={checking}>
                Check connection
              </Button>
              {s.source !== 'environment' ? (
                <>
                  <Button size="sm" icon={<Key size={13} />} onClick={() => void addKey()}>
                    Replace key
                  </Button>
                  <Button size="sm" variant="ghost" icon={<Trash size={13} />} onClick={() => void remove()} loading={removing}>
                    Remove
                  </Button>
                </>
              ) : null}
            </>
          ) : (
            <Button variant="primary" icon={<Key size={14} />} onClick={() => void addKey()}>
              Add key
            </Button>
          )}
        </div>
      </div>

      <dl className="mt-4 grid grid-cols-1 gap-x-6 sm:grid-cols-2">
        <Row label="Key on this computer">
          {s.configured ? (
            <span className="flex flex-wrap items-center gap-x-2">
              <span className="tc" aria-label={`ending in ${s.masked?.replace(/•/g, '') ?? ''}`}>
                {s.masked}
              </span>
              <span className="text-subtle">{whereKept(s)}</span>
            </span>
          ) : (
            <span className="text-muted">None yet. {whereKept(s)}.</span>
          )}
        </Row>
        <Row label="Last check">
          {s.lastTest ? (
            <span className={cx(s.lastTest.ok ? 'text-fg' : 'text-danger')} title={absolute(s.lastTest.at)}>
              {s.lastTest.ok ? 'Worked' : 'Failed'} {relative(s.lastTest.at)}
              {!s.lastTest.ok ? <span className="block text-[12.5px] text-muted">{s.lastTest.message}</span> : null}
            </span>
          ) : (
            <span className="text-subtle">{s.configured ? 'Not checked yet' : '-'}</span>
          )}
        </Row>
        {usage && usage.limit > 0 ? (
          <Row label="Plan usage" wide>
            <div className="max-w-[460px]">
              <div className="text-[13px]">{usageLine(usage)}</div>
              <Progress className="mt-2" value={usage.used / usage.limit} tone={usage.used / usage.limit > 0.9 ? 'danger' : 'accent'} label={`${s.title} plan usage`} />
            </div>
          </Row>
        ) : null}
      </dl>

      {!s.configured ? <p className="mt-3 max-w-[72ch] text-[12.5px] leading-relaxed text-subtle">{s.keyHelp}</p> : null}
      {s.source === 'environment' ? (
        <p className="mt-3 max-w-[72ch] text-[12.5px] leading-relaxed text-subtle">
          This key comes from how MetaRush was started on this computer, so it can only be changed or removed there.
        </p>
      ) : null}
      {s.note && s.configured ? <p className="mt-3 text-[12.5px] text-danger">{s.note}</p> : null}

      <Details>
        <dl className="grid grid-cols-1 gap-x-3 sm:grid-cols-[150px_minmax(0,1fr)] sm:gap-y-1">
          <dt>Computer</dt>
          <dd className="min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">{s.machine}</dd>
          <dt>Stored in</dt>
          <dd className="min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">
            {s.source ?? 'nothing yet'} (this installation keeps keys in the {s.secretMode === 'env' ? 'private .env file' : 'keychain'})
          </dd>
          <dt>Environment variable</dt>
          <dd className="tc min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">{s.envVar}</dd>
          {s.setAt ? (
            <>
              <dt>Saved</dt>
              <dd className="min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">{absolute(s.setAt)}</dd>
            </>
          ) : null}
          {s.lastTest ? (
            <>
              <dt>Last check said</dt>
              <dd className="min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">{s.lastTest.message}</dd>
            </>
          ) : null}
          <dt>From a terminal</dt>
          <dd className="tc min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">metarush providers set {s.name}</dd>
        </dl>
      </Details>
    </section>
  );
}

function Row({ label, children, wide }: { label: string; children: ReactNode; wide?: boolean }) {
  return (
    <div className={cx('border-t border-line py-2.5 text-[13px]', wide && 'sm:col-span-2')}>
      <dt className="mb-0.5 text-[12px] text-subtle">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

/* ---------- Add / replace key ---------- */

type Phase = 'idle' | 'checking' | 'works' | 'rejected' | 'unreachable' | 'saving';

function KeyDialog({ status: s, onClose }: { status: ProviderStatus; onClose: (saved?: boolean) => void }) {
  const id = useId();
  const name = s.name as ProviderName;
  const [key, setKey] = useState('');
  const [show, setShow] = useState(false);
  const [phase, setPhase] = useState<Phase>('idle');
  const [message, setMessage] = useState('');
  const [usage, setUsage] = useState<ProviderUsage | undefined>();
  const [saveError, setSaveError] = useState<ApiError | null>(null);
  const k = key.trim();
  const shape = !k
    ? null
    : /\s/.test(k)
      ? 'API keys have no spaces. Check that you copied only the key.'
      : k.length < 16
        ? 'That looks too short to be an API key. Check that you copied all of it.'
        : null;
  const busy = phase === 'checking' || phase === 'saving';

  const check = async () => {
    if (!k || shape || busy) return;
    setPhase('checking');
    setSaveError(null);
    try {
      const r = await call('provider.test', { name, key: k });
      setMessage(r.result.message);
      setUsage(r.result.usage);
      setPhase(r.result.ok ? 'works' : 'rejected');
    } catch (e) {
      const err = toApiError(e);
      setMessage(err.message);
      setPhase(err.code === 'E_INVALID_INPUT' ? 'rejected' : 'unreachable');
    }
  };

  const save = async (skipTest: boolean) => {
    setPhase('saving');
    setSaveError(null);
    try {
      await call('provider.set', { name, key: k, ...(skipTest ? { skipTest: true } : {}) });
      setKey('');
      onClose(true);
    } catch (e) {
      setSaveError(toApiError(e));
      setPhase('idle');
    }
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (phase === 'works') void save(false);
        else void check();
      }}
      className="space-y-4"
      autoComplete="off"
    >
      <p className="text-[13.5px] leading-relaxed text-muted">
        Paste your {s.title} API key below. {s.keyHelp}
      </p>
      <div>
        <label htmlFor={id} className="text-[12.5px] font-medium text-muted">
          API key
        </label>
        <div className="relative mt-1.5">
          <input
            id={id}
            data-autofocus
            type={show ? 'text' : 'password'}
            name="metarush-integration-key"
            value={key}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
            spellCheck={false}
            data-1p-ignore
            data-lpignore="true"
            maxLength={512}
            placeholder="Paste your key"
            aria-invalid={!!shape || phase === 'rejected'}
            aria-describedby={`${id}-status`}
            onChange={(e) => {
              setKey(e.target.value);
              setPhase('idle');
              setSaveError(null);
            }}
            onPaste={(e) => {
              // Copying from a web page often brings a stray space or line break along.
              const text = e.clipboardData.getData('text');
              if (text && text.trim() !== text) {
                e.preventDefault();
                setKey(text.trim());
                setPhase('idle');
              }
            }}
            className="tc h-10 w-full rounded-[10px] bg-panel pl-3 pr-11 text-[13.5px] text-fg hairline placeholder:font-sans placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
          <button
            type="button"
            onClick={() => setShow((v) => !v)}
            aria-label={show ? 'Hide the key' : 'Show the key you typed'}
            aria-pressed={show}
            className="absolute right-1 top-1 grid h-8 w-8 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-accent"
          >
            {show ? <EyeSlash size={15} /> : <Eye size={15} />}
          </button>
        </div>
        <div id={`${id}-status`} className="mt-2 min-h-[20px] text-[12.5px] leading-relaxed" aria-live="polite">
          {shape ? <span className="text-danger">{shape}</span> : null}
          {!shape && phase === 'checking' ? (
            <span className="inline-flex items-center gap-2 text-muted">
              <Spinner size={12} /> Checking the key with {s.title}…
            </span>
          ) : null}
          {!shape && phase === 'works' ? (
            <span className="flex items-start gap-1.5 text-ok">
              <CheckCircle size={15} weight="fill" className="mt-[1px] shrink-0" />
              <span>The key works. {usage && usage.limit > 0 ? <span className="text-muted">{usageLine(usage)}.</span> : <span className="text-muted">{message}</span>}</span>
            </span>
          ) : null}
          {!shape && phase === 'rejected' ? (
            <span className="flex items-start gap-1.5 text-danger">
              <WarningCircle size={15} weight="fill" className="mt-[1px] shrink-0" />
              <span>{message}</span>
            </span>
          ) : null}
          {!shape && phase === 'unreachable' ? (
            <span className="flex items-start gap-1.5 text-muted">
              <WarningCircle size={15} weight="fill" className="mt-[1px] shrink-0 text-danger" />
              <span>
                Could not reach {s.title} to check the key ({message}). You can save it now and check it later.
              </span>
            </span>
          ) : null}
          {!shape && phase === 'idle' && k ? <span className="text-subtle">Check the key before saving it.</span> : null}
        </div>
      </div>

      <div className="rounded-[12px] bg-accent-soft px-3.5 py-3 text-[12.5px] leading-relaxed text-accent-text">
        <strong className="font-semibold">You will not see this key again.</strong> After you save it, MetaRush never shows it, here or in the terminal; keep your own copy in a
        password manager if you need one. {whereKept({ source: undefined, secretMode: s.secretMode })} only, so other computers need their own key.
      </div>

      {saveError ? <ErrorPanel compact error={saveError} /> : null}

      <div className="flex flex-wrap items-center justify-end gap-2 pt-1">
        <Button variant="ghost" onClick={() => onClose()} disabled={phase === 'saving'}>
          Cancel
        </Button>
        {phase === 'unreachable' ? (
          <Button variant="quiet" onClick={() => void save(true)}>
            Save without checking
          </Button>
        ) : null}
        {phase !== 'works' && phase !== 'saving' ? (
          <Button type="submit" variant={phase === 'unreachable' ? 'secondary' : 'primary'} disabled={!k || !!shape} loading={phase === 'checking'}>
            Check key
          </Button>
        ) : (
          <Button type="submit" variant="primary" loading={phase === 'saving'}>
            Save key
          </Button>
        )}
      </div>
    </form>
  );
}
