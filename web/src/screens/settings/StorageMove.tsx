import { useId, useRef, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowsLeftRight, CheckCircle, Eye, EyeSlash, FolderOpen, WarningCircle } from '@phosphor-icons/react';
import { request, toApiError, type ApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { useHost } from '../../lib/host';
import type {
  StorageChooseOut,
  StorageMovePlanIn,
  StorageMovePlanOut,
  StorageMoveStartIn,
  StorageMoveStartOut,
  StoragePlanOut,
  StorageStatusOut,
  StorageTarget,
} from '../../api/types';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, ErrorPanel, Spinner, Theirs } from '../../ui/primitives';

/*
 * Settings → Storage → "Move the media to another storage" (D112). The form
 * describes the new storage; "Check" runs storage.plan on this computer (a dry
 * run: nothing is copied or created) through /api/storage/plan; "Move" asks in
 * a styled dialog, then /api/storage/move starts `metarush storage migrate` in
 * a process of its own — the same move as the terminal's, shown in the activity
 * bar. On another computer, after the move, storage.status says it still uses
 * the old storage and offers the same form, filled in, for its own keys.
 * A typed secret key goes once to this computer's keychain and is never shown
 * again; it lives only in this component's state until it is sent.
 */

type Provider = StorageTarget['provider'];
type Preset = NonNullable<StorageTarget['preset']>;

const SERVICES: { v: Exclude<Preset, ''>; label: string; endpoint?: string; needsAddress?: boolean; addressHint?: string }[] = [
  { v: 'tigris', label: 'Tigris', endpoint: 'https://fly.storage.tigris.dev' },
  { v: 'r2', label: 'Cloudflare R2', needsAddress: true, addressHint: 'https://<account>.r2.cloudflarestorage.com' },
  { v: 'b2', label: 'Backblaze B2', needsAddress: true, addressHint: 'https://s3.<region>.backblazeb2.com' },
  { v: 'aws', label: 'Amazon S3' },
  { v: 'gcs', label: 'Google Cloud Storage (S3 keys)', endpoint: 'https://storage.googleapis.com' },
  { v: 'custom', label: 'Another S3-compatible service', needsAddress: true, addressHint: 'https://…' },
];

const KINDS: { v: Provider; label: string; hint: string }[] = [
  { v: 'filesystem', label: 'A folder', hint: 'On this computer or a shared drive' },
  { v: 's3', label: 'Online storage', hint: 'Tigris, Cloudflare R2, Backblaze B2, Amazon S3…' },
  { v: 'gcs', label: 'Google Cloud Storage', hint: 'With a key file or this computer’s Google sign-in' },
];

const empty: StorageTarget = { provider: 'filesystem', root: '', shared: false, preset: 'tigris', endpoint: '', region: '', bucket: '', prefix: '', accessKeyId: '' };

/** The storage status, polled quickly while a move runs. */
export function useStorageStatus() {
  return useOp('storage.status', {}, { refetchInterval: (q) => (q.state.data?.moving ? 2_000 : 60_000), staleTime: 1_000, retry: 4 });
}

export function StorageMove() {
  const st = useStorageStatus();
  const s = st.data;
  const [open, setOpen] = useState<'new' | 'switch' | null>(null);
  const sectionRef = useRef<HTMLElement>(null);
  // From another computer (remote access) the page shows the move but doesn't
  // start one: it runs on the computer with MetaRush, with its keys (D112).
  const host = useHost();
  const here = host.remote ? <Theirs>{host.machine}</Theirs> : 'this computer';
  const status = s ? <MoveState s={s} here={here} onSwitch={host.remote ? undefined : () => setOpen('switch')} /> : null;
  return (
    <section id="storage" ref={sectionRef} aria-labelledby="storage-move-h" className="panel scroll-mt-28 p-5">
      <h2 id="storage-move-h" className="mb-1 flex flex-wrap items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <ArrowsLeftRight size={17} />
        </span>
        <span className="whitespace-nowrap">Move the media</span>
        {s?.switchMeToo && !host.remote ? (
          <Badge tone="accent" className="max-sm:hidden">
            switch this computer too
          </Badge>
        ) : null}
      </h2>
      {st.error && !s ? <ErrorPanel compact className="mt-3" error={st.error} onRetry={() => void st.refetch()} /> : null}
      {!s ? (
        st.isLoading ? (
          <div className="mt-3 flex items-center gap-2 text-[13px] text-muted">
            <Spinner size={13} /> Looking at {here}’s storage…
          </div>
        ) : null
      ) : (
        <>
          <p className="max-w-[78ch] text-[13px] leading-relaxed text-muted">
            Copy every clip and render to another storage, then use it: a folder, a shared drive or online storage. Each file is checked as it arrives, and nothing is deleted: the old storage stays as it is. Right now {here} keeps the media in{' '}
            <Theirs>{s.this.name}</Theirs>.
          </p>
          {status}
          {host.remote ? (
            <p className="mt-3 max-w-[78ch] text-[13px] leading-relaxed text-muted" data-storage-elsewhere="">
              Moving the media starts on <Theirs>{host.machine}</Theirs> itself, where MetaRush runs and keeps its keys.
            </p>
          ) : open ? (
            <MoveForm
              key={open}
              s={s}
              initial={open === 'switch' && s.target ? { ...empty, ...s.target, preset: s.target.preset || (s.target.provider === 's3' ? 'custom' : 'tigris') } : empty}
              switching={open === 'switch'}
              onClose={() => setOpen(null)}
              onStarted={() => {
                setOpen(null);
                void st.refetch();
                sectionRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
              }}
            />
          ) : !s.moving && !s.switchMeToo ? (
            <div className="mt-4">
              <Button variant="secondary" icon={<ArrowsLeftRight size={15} />} onClick={() => setOpen('new')}>
                Move the media to another storage
              </Button>
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}

/** What is going on: a move running here, the last one's outcome, or another computer's move to follow. */
function MoveState({ s, here, onSwitch }: { s: StorageStatusOut; here: ReactNode; onSwitch?: () => void }) {
  const mv = s.move;
  if (s.moving) {
    return (
      <div className="mt-4 flex items-start gap-3 rounded-[12px] bg-accent-soft px-3.5 py-3" role="status" data-move-state="moving">
        <Spinner size={15} className="mt-[2px] shrink-0 text-accent-text" />
        <div className="min-w-0 text-[13px] leading-relaxed">
          <div className="font-medium text-fg">{s.summary}</div>
          {s.progress ? <div className="text-muted">{s.progress}</div> : null}
          <div className="mt-1 text-[12px] text-muted">
            The activity bar shows how far it is. When every file is in, MetaRush starts again by itself to use the new storage, and this page reconnects.
          </div>
        </div>
      </div>
    );
  }
  return (
    <>
      {mv && (mv.state === 'done' || mv.state === 'failed' || mv.state === 'stopped') ? (
        <div
          className={cx('mt-4 flex items-start gap-3 rounded-[12px] px-3.5 py-3 text-[13px] leading-relaxed', mv.state === 'done' ? 'bg-ok-soft' : 'bg-danger-soft')}
          data-move-state={mv.state}
        >
          {mv.state === 'done' ? <CheckCircle size={16} weight="fill" className="mt-[2px] shrink-0 text-ok" /> : <WarningCircle size={16} weight="fill" className="mt-[2px] shrink-0 text-danger" />}
          <div className="min-w-0">
            <div className="font-medium text-fg">{mv.state === 'done' ? 'The last move finished' : mv.state === 'failed' ? 'The last move didn’t finish' : 'The last move stopped'}</div>
            <div className="text-muted">{mv.summary}</div>
            {mv.next?.length ? (
              <ul className="mt-1 list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
                {mv.next.map((n) => (
                  <li key={n}>{n}</li>
                ))}
              </ul>
            ) : null}
          </div>
        </div>
      ) : null}
      {s.switchMeToo && s.workspace ? (
        <div className="mt-4 rounded-[12px] bg-accent-soft px-3.5 py-3" data-switch-me-too="">
          <div className="text-[13.5px] font-medium text-fg">{onSwitch ? 'This computer' : here} still uses the old storage</div>
          <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">
            The workspace moved its media to <Theirs>{s.workspace.name}</Theirs>, but {here} still uses <Theirs>{s.this.name}</Theirs>.{' '}
            {onSwitch ? (
              <>Switch it too: MetaRush copies anything only this computer has, checks everything and then uses the new storage. You need this computer’s own keys for it.</>
            ) : (
              <>Switch it on {here} itself: MetaRush copies anything only it has, checks everything and then uses the new storage.</>
            )}
          </p>
          {onSwitch ? (
            <div className="mt-2.5">
              <Button variant="primary" size="sm" icon={<ArrowsLeftRight size={14} />} onClick={onSwitch}>
                Switch this computer too
              </Button>
            </div>
          ) : null}
        </div>
      ) : null}
    </>
  );
}

function Field({ label, hint, children, id }: { label: string; hint?: ReactNode; children: ReactNode; id: string }) {
  return (
    <div>
      <label htmlFor={id} className="text-[12.5px] font-medium text-muted">
        {label}
      </label>
      <div className="mt-1.5">{children}</div>
      {hint ? <div className="mt-1 text-[11.5px] leading-relaxed text-subtle">{hint}</div> : null}
    </div>
  );
}

const inputCls =
  'h-10 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60';

function MoveForm({
  s,
  initial,
  switching,
  onClose,
  onStarted,
}: {
  s: StorageStatusOut;
  initial: StorageTarget;
  switching: boolean;
  onClose: () => void;
  onStarted: () => void;
}) {
  const id = useId();
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [t, setT] = useState<StorageTarget>(initial);
  const [secret, setSecret] = useState('');
  const [showSecret, setShowSecret] = useState(false);
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [choosing, setChoosing] = useState(false);
  const [readBack, setReadBack] = useState(false);
  const [plan, setPlan] = useState<{ id: string; plan: StoragePlanOut } | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const envKeys = s.keysIn === 'env';
  const service = SERVICES.find((x) => x.v === t.preset) ?? SERVICES[SERVICES.length - 1]!;
  const set = (patch: Partial<StorageTarget>) => {
    setT((cur) => ({ ...cur, ...patch }));
    setPlan(null);
    setError(null);
  };

  const ready =
    t.provider === 'filesystem'
      ? !!t.root?.trim()
      : t.provider === 'gcs'
        ? !!t.bucket?.trim()
        : !!t.bucket?.trim() && !!t.accessKeyId?.trim() && (envKeys ? !!t.secretVariable?.trim() : !!secret.trim()) && (!service.needsAddress || !!t.endpoint?.trim());

  const target = (): StorageTarget => {
    const out: StorageTarget = { provider: t.provider };
    if (t.provider === 'filesystem') Object.assign(out, { root: t.root?.trim(), shared: !!t.shared });
    if (t.provider === 'gcs') Object.assign(out, { bucket: t.bucket?.trim(), prefix: t.prefix?.trim() || undefined, credentialsFile: t.credentialsFile?.trim() || undefined });
    if (t.provider === 's3') {
      Object.assign(out, {
        preset: t.preset,
        endpoint: service.needsAddress ? t.endpoint?.trim() : service.endpoint,
        region: t.preset === 'aws' ? t.region?.trim() || undefined : undefined,
        bucket: t.bucket?.trim(),
        prefix: t.prefix?.trim() || undefined,
        accessKeyId: t.accessKeyId?.trim(),
        forcePathStyle: t.forcePathStyle || undefined,
      });
      if (envKeys) out.secretVariable = t.secretVariable?.trim();
      else out.secretAccessKey = secret.trim();
    }
    return out;
  };

  const check = async () => {
    if (!ready || checking) return;
    setChecking(true);
    setError(null);
    setPlan(null);
    try {
      const out = await request<StorageMovePlanOut>('POST', '/api/storage/plan', { json: { target: target() } satisfies StorageMovePlanIn });
      setPlan({ id: out.moveId, plan: out.plan });
    } catch (e) {
      setError(toApiError(e));
    } finally {
      setChecking(false);
    }
  };

  const choose = async () => {
    setChoosing(true);
    try {
      const out = await request<StorageChooseOut>('POST', '/api/storage/choose', { json: {} });
      if (!out.canceled && out.path) set({ root: out.path });
    } catch (e) {
      toast({ tone: 'error', title: 'The folder picker didn’t open', body: toApiError(e).message });
    } finally {
      setChoosing(false);
    }
  };

  const blocked = plan ? (plan.plan.differ?.length ?? 0) > 0 || (plan.plan.overrides?.length ?? 0) > 0 : false;
  const move = async () => {
    if (!plan || blocked) return;
    const p = plan.plan;
    const ok = await dialog.confirm({
      title: switching ? `Switch this computer to ${p.to.name}?` : `Move the media to ${p.to.name}?`,
      body: (
        <div className="space-y-2">
          <p>{p.summary}</p>
          <p>
            Every file is checked as it arrives. This computer switches only once all of it is there; MetaRush then starts again by itself to use it. Nothing is deleted: <Theirs>{p.from.name}</Theirs> stays as it is.
          </p>
          {p.machines?.length ? <p>Your other computers keep using the old storage until each one switches too, with its own keys.</p> : null}
        </div>
      ),
      confirmLabel: switching ? 'Switch this computer' : 'Move the media',
    });
    if (!ok) return;
    setStarting(true);
    try {
      const out = await request<StorageMoveStartOut>('POST', '/api/storage/move', { json: { moveId: plan.id, readBack } satisfies StorageMoveStartIn });
      setSecret('');
      toast({ tone: 'success', title: out.summary });
      void qc.invalidateQueries({ queryKey: ['activity.summary'] });
      onStarted();
    } catch (e) {
      setError(toApiError(e));
    } finally {
      setStarting(false);
    }
  };

  return (
    <form
      className="mt-4 space-y-4 rounded-[14px] p-4 hairline"
      autoComplete="off"
      onSubmit={(e) => {
        e.preventDefault();
        void check();
      }}
      aria-label={switching ? 'Switch this computer to the new storage' : 'The new storage'}
    >
      {switching ? (
        <p className="text-[12.5px] leading-relaxed text-muted">
          Filled in from the workspace’s new storage. {t.provider === 's3' ? 'Add this computer’s own access key.' : 'Check that this computer reaches it at the same place.'}
        </p>
      ) : (
        <div role="radiogroup" aria-label="Where the media goes" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          {KINDS.map((k) => {
            const on = t.provider === k.v;
            return (
              <button
                key={k.v}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => !on && set({ provider: k.v })}
                className={cx('rounded-[12px] px-3 py-2.5 text-left transition-colors hairline', on ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
              >
                <div className={cx('text-[13px] font-medium', on ? 'text-accent-text' : 'text-fg')}>{k.label}</div>
                <div className="text-[11.5px] text-subtle">{k.hint}</div>
              </button>
            );
          })}
        </div>
      )}

      {t.provider === 'filesystem' ? (
        <>
          <Field id={`${id}-root`} label="Folder" hint="Its full place on this computer or the shared drive (Choose… finds it for you). MetaRush makes the folder if it isn’t there yet.">
            <div className="flex gap-2">
              <input id={`${id}-root`} className={inputCls} value={t.root ?? ''} spellCheck={false} placeholder="/Volumes/Studio/MetaRush media" onChange={(e) => set({ root: e.target.value })} />
              <Button variant="secondary" icon={<FolderOpen size={15} />} loading={choosing} onClick={() => void choose()}>
                Choose…
              </Button>
            </div>
          </Field>
          <label className="flex items-start gap-2 text-[13px] text-muted">
            <input type="checkbox" className="mt-[3px]" checked={!!t.shared} onChange={(e) => set({ shared: e.target.checked })} />
            <span>Every computer of the workspace reaches this folder (a shared drive), so they can all play and render the footage.</span>
          </label>
        </>
      ) : null}

      {t.provider === 's3' ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <Field id={`${id}-svc`} label="Service">
            <select id={`${id}-svc`} className={inputCls} value={t.preset || 'custom'} onChange={(e) => set({ preset: e.target.value as Preset, endpoint: '' })}>
              {SERVICES.map((x) => (
                <option key={x.v} value={x.v}>
                  {x.label}
                </option>
              ))}
            </select>
          </Field>
          {service.needsAddress ? (
            <Field id={`${id}-ep`} label="Address" hint="The service shows it with the bucket.">
              <input id={`${id}-ep`} className={inputCls} value={t.endpoint ?? ''} spellCheck={false} placeholder={service.addressHint} onChange={(e) => set({ endpoint: e.target.value })} />
            </Field>
          ) : t.preset === 'aws' ? (
            <Field id={`${id}-region`} label="Region">
              <input id={`${id}-region`} className={inputCls} value={t.region ?? ''} spellCheck={false} placeholder="us-east-1" onChange={(e) => set({ region: e.target.value })} />
            </Field>
          ) : (
            <div className="hidden md:block" />
          )}
          <Field id={`${id}-bucket`} label="Bucket">
            <input id={`${id}-bucket`} className={inputCls} value={t.bucket ?? ''} spellCheck={false} onChange={(e) => set({ bucket: e.target.value })} />
          </Field>
          <Field id={`${id}-prefix`} label="Folder inside the bucket (optional)">
            <input id={`${id}-prefix`} className={inputCls} value={t.prefix ?? ''} spellCheck={false} onChange={(e) => set({ prefix: e.target.value })} />
          </Field>
          <Field id={`${id}-key`} label="Access key ID">
            <input id={`${id}-key`} className={inputCls} value={t.accessKeyId ?? ''} spellCheck={false} autoComplete="off" data-1p-ignore data-lpignore="true" onChange={(e) => set({ accessKeyId: e.target.value })} />
          </Field>
          {envKeys ? (
            <Field
              id={`${id}-var`}
              label="Variable that holds the secret key"
              hint="This computer keeps MetaRush’s keys in its environment file: add the secret key there as a variable, start MetaRush again, then type the variable’s name here."
            >
              <input id={`${id}-var`} className={inputCls} value={t.secretVariable ?? ''} spellCheck={false} placeholder="NEW_MEDIA_SECRET" onChange={(e) => set({ secretVariable: e.target.value })} />
            </Field>
          ) : (
            <Field id={`${id}-secret`} label="Secret key" hint="Kept in this computer’s keychain. You won’t see it again, here or anywhere in MetaRush.">
              <div className="relative">
                <input
                  id={`${id}-secret`}
                  type={showSecret ? 'text' : 'password'}
                  name="metarush-storage-secret"
                  className={cx(inputCls, 'pr-11')}
                  value={secret}
                  autoComplete="off"
                  autoCorrect="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  data-1p-ignore
                  data-lpignore="true"
                  maxLength={400}
                  onChange={(e) => {
                    setSecret(e.target.value);
                    setPlan(null);
                    setError(null);
                  }}
                />
                <button
                  type="button"
                  onClick={() => setShowSecret((v) => !v)}
                  aria-label={showSecret ? 'Hide the secret key' : 'Show the secret key you typed'}
                  aria-pressed={showSecret}
                  className="absolute right-1 top-1 grid h-8 w-8 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-accent"
                >
                  {showSecret ? <EyeSlash size={15} /> : <Eye size={15} />}
                </button>
              </div>
            </Field>
          )}
        </div>
      ) : null}

      {t.provider === 'gcs' ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <Field id={`${id}-gbucket`} label="Bucket">
            <input id={`${id}-gbucket`} className={inputCls} value={t.bucket ?? ''} spellCheck={false} onChange={(e) => set({ bucket: e.target.value })} />
          </Field>
          <Field id={`${id}-gprefix`} label="Folder inside the bucket (optional)">
            <input id={`${id}-gprefix`} className={inputCls} value={t.prefix ?? ''} spellCheck={false} onChange={(e) => set({ prefix: e.target.value })} />
          </Field>
          <div className="md:col-span-2">
            <Field id={`${id}-cred`} label="Key file (optional)" hint="A service account’s key file (.json) on this computer. Leave it empty to use this computer’s Google sign-in.">
              <input id={`${id}-cred`} className={inputCls} value={t.credentialsFile ?? ''} spellCheck={false} placeholder="~/keys/metarush-media.json" onChange={(e) => set({ credentialsFile: e.target.value })} />
            </Field>
          </div>
        </div>
      ) : null}

      {error ? <ErrorPanel compact error={error} /> : null}

      {plan ? (
        <div className="rounded-[12px] bg-hover px-3.5 py-3 text-[13px] leading-relaxed" data-storage-plan="" aria-live="polite">
          <div className="font-medium text-fg">{plan.plan.summary}</div>
          {plan.plan.notes?.length ? (
            <ul className="mt-1 list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
              {plan.plan.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          ) : null}
          {!plan.plan.nothing && !blocked ? (
            <label className="mt-2.5 flex items-start gap-2 text-[12.5px] text-muted">
              <input type="checkbox" className="mt-[3px]" checked={readBack} onChange={(e) => setReadBack(e.target.checked)} />
              <span>Also download every file from the new storage again and check it (slower; the storage may charge for the downloads).</span>
            </label>
          ) : null}
          <Details compact>
            <div className="break-all">
              From <span className="tc text-muted">{plan.plan.from.locator}</span>
            </div>
            <div className="break-all">
              To <span className="tc text-muted">{plan.plan.to.locator}</span>
            </div>
          </Details>
        </div>
      ) : null}

      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button variant="ghost" onClick={onClose} disabled={starting}>
          Cancel
        </Button>
        {plan && !plan.plan.nothing ? (
          <>
            <Button variant="secondary" onClick={() => void check()} loading={checking} disabled={!ready || starting}>
              Check again
            </Button>
            <Button variant="primary" icon={<ArrowsLeftRight size={15} />} onClick={() => void move()} loading={starting} disabled={blocked}>
              {switching ? 'Switch this computer' : 'Move the media'}
            </Button>
          </>
        ) : (
          <Button type="submit" variant="primary" loading={checking} disabled={!ready}>
            {checking ? 'Checking both storages…' : 'Check what it would copy'}
          </Button>
        )}
      </div>
    </form>
  );
}
