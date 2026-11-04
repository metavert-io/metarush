import { useState } from 'react';
import { Link } from 'react-router';
import { Certificate, CheckCircle, Warning } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import type { ops } from '@metarush/schema';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { relative } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, Details, ErrorPanel, Segmented, Skeleton, Spinner } from '../../ui/primitives';
import { OwnerOnly } from '../../components/OwnerOnly';

/*
 * Settings → Delivery → Content Credentials (D418, D424): whether the final
 * videos MetaRush delivers carry Content Credentials — a signed record of how
 * each was made — off, on or required (owners; asking less is a person's),
 * and this computer's signing certificate: a test one made here, or the
 * organisation's own (PEM files, read once in this browser and sent to this
 * computer, whose keychain keeps the key). The CLI does the same with
 * `metarush credentials`.
 */

type Status = ops.CredentialsStatusOut;

const MODES = [
  { value: 'off', label: 'Off' },
  { value: 'on', label: 'On' },
  { value: 'required', label: 'Required' },
] as const;

const rank: Record<string, number> = { off: 0, on: 1, required: 2 };

const QUERIES = ['credentials.status', 'delivery.ready', 'content.show'] as const;

export function ContentCredentialsSettings() {
  const st = useOp('credentials.status', {});
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const refresh = () => {
    for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
  const run = async (key: string, fn: () => Promise<string>) => {
    setBusy(key);
    try {
      toast({ tone: 'success', title: await fn() });
      refresh();
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Nothing changed', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };

  if (st.isLoading) return <Skeleton className="h-40" />;
  if (st.error || !st.data) return <ErrorPanel error={st.error} compact onRetry={() => void st.refetch()} />;
  const s: Status = st.data;
  const ws = s.workspace;
  const mode = ws?.mode ?? 'off';
  const here = s.here;
  const cert = here.certificate;

  const choose = async (v: string) => {
    if (v === mode) return;
    if (rank[v]! < rank[mode]!) {
      const ok = await dialog.confirm({
        title: v === 'off' ? 'Deliver videos without Content Credentials?' : 'Let videos go out when they can’t be signed?',
        body:
          v === 'off'
            ? 'Delivered videos will no longer say how they were made. A client’s own settings can still ask for them.'
            : 'A version this computer can’t sign then goes out without them (its record says why), instead of waiting.',
        confirmLabel: 'Change it',
        tone: 'danger',
      });
      if (!ok) return;
    }
    void run('mode', async () => (await call('credentials.set', { mode: v as 'off' | 'on' | 'required' })).summary);
  };

  const makeTest = async () => {
    const org = await dialog.prompt({
      title: cert ? 'Replace it with a test certificate?' : 'Make a test certificate?',
      body: (
        <div className="space-y-2 text-[13px]">
          <p>A test certificate is made on this computer and lasts a year. Videos signed with it carry Content Credentials, but verifiers show the signer as unknown: it is for trying them, never for a client.</p>
          {cert ? <p className="text-muted">This computer’s certificate for “{cert.signer}” is replaced, and its key is removed from this computer.</p> : null}
        </div>
      ),
      label: 'Your organisation’s name, to put in it (leave it empty for none)',
      placeholder: 'Example Studio',
      maxLength: 64,
      confirmLabel: 'Make it',
    });
    if (org === null) return;
    void run('test', async () => (await call('credentials.setup', { test: true, organisation: org.trim(), replace: !!cert })).summary);
  };

  const bring = async () => {
    const files = await dialog.open<{ certificate: string; privateKey: string }>((close) => <BringForm replacing={cert?.signer} close={close} />, {
      title: cert ? 'Replace it with your certificate' : 'Use your own certificate',
      size: 'md',
    });
    if (!files) return;
    void run('bring', async () => (await call('credentials.setup', { ...files, replace: !!cert })).summary);
  };

  const remove = async () => {
    const ok = await dialog.confirm({
      title: 'Remove this computer’s signing certificate?',
      body: `Its key is removed from this computer’s keychain. Videos delivered here then go out without Content Credentials${mode === 'required' ? ', or wait where they are required' : ''}.`,
      confirmLabel: 'Remove it',
      tone: 'danger',
    });
    if (!ok) return;
    void run('remove', async () => {
      await call('credentials.remove', {});
      return 'This computer’s signing certificate is removed.';
    });
  };

  const timestamp = async () => {
    const url = await dialog.prompt({
      title: 'A timestamp service',
      body: (
        <p className="text-[13px]">
          A timestamp service vouches for when each video was signed, so its Content Credentials still check out after the certificate runs out. It receives a fingerprint of
          what was signed, never the video or anything of the client&apos;s. Its address is https, on the internet. Leave it empty for none.
        </p>
      ),
      label: 'Its address',
      placeholder: 'https://timestamp.example.com',
      defaultValue: ws?.timestampUrl ?? '',
      maxLength: 200,
      confirmLabel: 'Save',
    });
    if (url === null) return;
    void run('tsa', async () => (await call('credentials.set', { mode: mode as 'off' | 'on' | 'required', timestampUrl: url.trim() })).summary);
  };

  return (
    <section id="content-credentials" className="panel scroll-mt-28 p-5" aria-labelledby="content-credentials-h" data-content-credentials-settings>
      <h2 id="content-credentials-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Certificate size={17} />
        </span>
        Content Credentials
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        A signed record inside each final video MetaRush delivers: that it was made with MetaRush, which footage, sound and pictures it uses (by their fingerprints, never their
        names) and which parts AI made or changed. Apps and sites that read Content Credentials show who signed it. They never carry the names of people, projects or clients.
      </p>
      <OwnerOnly note className="space-y-3">
        <Segmented label="Content Credentials" className="max-w-full flex-wrap" value={mode} onChange={(v) => void choose(v)} items={[...MODES]} />
        <p className="text-[12.5px] text-fg" data-credentials-summary>
          {s.means}
          {ws ? (
            <span className="text-subtle">
              {' '}
              Chosen by {ws.updatedBy.userName || 'a person'} {relative(ws.updatedAt)}.
            </span>
          ) : null}
        </p>
      </OwnerOnly>

      <div className="mt-5 rounded-[12px] px-4 py-3 hairline" data-credentials-here data-ready={here.ready ? 'yes' : 'no'}>
        <div className="flex flex-wrap items-center gap-2">
          {here.ready ? <CheckCircle size={16} className="text-ok" aria-hidden /> : <Warning size={16} className="text-info" aria-hidden />}
          <h3 className="text-[13.5px] font-semibold">This computer ({here.machine})</h3>
          {cert?.test ? <Badge tone="info">Test certificate</Badge> : null}
        </div>
        <p className="mt-1 text-[13px]" data-credentials-says>
          {here.says}
        </p>
        {here.problems.length > 1 ? (
          <ul className="mt-1 list-disc pl-5 text-[12.5px] text-muted">
            {here.problems.slice(1).map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        ) : null}
        {!here.tool ? (
          <p className="mt-1 text-[12.5px] text-muted">
            c2patool, the program that puts Content Credentials into videos, comes from{' '}
            <Link to="/settings/components" className="text-accent-text hover:underline">
              Settings → Components
            </Link>
            .
          </p>
        ) : null}
        {cert ? (
          <Details compact className="mt-2">
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[12px]">
              <dt className="text-subtle">Name</dt>
              <dd>{cert.name}</dd>
              <dt className="text-subtle">Issued by</dt>
              <dd>{cert.issuer || '—'}</dd>
              <dt className="text-subtle">Valid</dt>
              <dd>
                {cert.validFrom.slice(0, 10)} to {cert.validUntil.slice(0, 10)}
              </dd>
              <dt className="text-subtle">Signature</dt>
              <dd>{cert.alg.toUpperCase()}</dd>
              <dt className="text-subtle">Fingerprint</dt>
              <dd className="break-all font-mono">{cert.fingerprint}</dd>
              <dt className="text-subtle">Key kept in</dt>
              <dd>{here.kept === 'file' ? 'this computer’s secrets file' : 'this computer’s keychain'}</dd>
              {here.setBy ? (
                <>
                  <dt className="text-subtle">Set up by</dt>
                  <dd>
                    {here.setBy}
                    {here.setAt ? `, ${relative(here.setAt)}` : ''}
                  </dd>
                </>
              ) : null}
              {here.tool ? (
                <>
                  <dt className="text-subtle">c2patool</dt>
                  <dd>{here.tool}</dd>
                </>
              ) : null}
            </dl>
          </Details>
        ) : null}
        <OwnerOnly className="mt-3 flex flex-wrap gap-2">
          <Button size="sm" variant="secondary" onClick={() => void makeTest()} disabled={busy !== null} data-credentials-make-test>
            {busy === 'test' ? <Spinner size={13} /> : null} {cert?.test ? 'Make a new test certificate…' : 'Make a test certificate…'}
          </Button>
          <Button size="sm" variant="secondary" onClick={() => void bring()} disabled={busy !== null} data-credentials-bring>
            {busy === 'bring' ? <Spinner size={13} /> : null} Use my own certificate…
          </Button>
          {cert ? (
            <Button size="sm" variant="ghost" onClick={() => void remove()} disabled={busy !== null} data-credentials-remove>
              Remove…
            </Button>
          ) : null}
        </OwnerOnly>
        {cert ? (
          <div className="mt-2">
            <Button
              size="sm"
              variant="ghost"
              disabled={busy !== null || !here.tool}
              onClick={() => void run('try', async () => (await call('credentials.test', {})).summary)}
              data-credentials-try
            >
              {busy === 'try' ? <Spinner size={13} /> : null} Try it on a sample
            </Button>
          </div>
        ) : null}
      </div>

      <OwnerOnly className="mt-4">
        <p className="text-[12.5px] text-muted">
          {ws?.timestampUrl ? 'A timestamp service vouches for when each video is signed.' : 'No timestamp service: a signature checks out until its certificate runs out.'}{' '}
          <button type="button" className="text-accent-text hover:underline" onClick={() => void timestamp()} data-credentials-timestamp>
            {ws?.timestampUrl ? 'Change it' : 'Add one'}
          </button>
        </p>
      </OwnerOnly>
    </section>
  );
}

/** The two PEM files of a certificate a person brings, read in this browser. */
function BringForm({ replacing, close }: { replacing?: string; close: (v?: { certificate: string; privateKey: string }) => void }) {
  const [certificate, setCertificate] = useState('');
  const [privateKey, setPrivateKey] = useState('');
  const [names, setNames] = useState<{ cert?: string; key?: string }>({});
  const [error, setError] = useState<string | null>(null);
  const read = async (f: File | undefined, which: 'cert' | 'key') => {
    setError(null);
    if (!f) return;
    if (f.size > 64 * 1024) {
      setError('That file is larger than a certificate or a key can be.');
      return;
    }
    const text = await f.text();
    if (which === 'cert') setCertificate(text);
    else setPrivateKey(text);
    setNames((n) => ({ ...n, [which]: f.name }));
  };
  return (
    <div className="space-y-4 text-[13.5px]">
      <p className="text-muted">
        Your certificate chain (the signing certificate first, then the ones that issued it) and its private key, as PEM files from your certificate authority. The key goes to this
        computer’s keychain and is never shown again; verifiers show the organisation the certificate names.
        {replacing ? ` It replaces the certificate for “${replacing}”.` : ''}
      </p>
      <label className="block">
        <span className="mb-1 block font-medium">Certificate chain</span>
        <input type="file" accept=".pem,.crt,.cer,text/plain" onChange={(e) => void read(e.target.files?.[0], 'cert')} data-credentials-cert-file />
        {names.cert ? <span className="ml-2 text-[12px] text-subtle">{names.cert}</span> : null}
      </label>
      <label className="block">
        <span className="mb-1 block font-medium">Private key (without a password)</span>
        <input type="file" accept=".pem,.key,text/plain" onChange={(e) => void read(e.target.files?.[0], 'key')} data-credentials-key-file />
        {names.key ? <span className="ml-2 text-[12px] text-subtle">{names.key}</span> : null}
      </label>
      {error ? <p className="text-[13px] text-danger">{error}</p> : null}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="primary" disabled={!certificate || !privateKey} onClick={() => close({ certificate, privateKey })} data-credentials-bring-save>
          Use it
        </Button>
      </div>
    </div>
  );
}
