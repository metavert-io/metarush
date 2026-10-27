import { useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { DownloadSimple, Package, UploadSimple } from '@phosphor-icons/react';
import { request, toApiError } from '../../api/client';
import type { AbilityImportOut, AbilityVersion } from '../../api/types';
import { absolute, plural, stamp } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Button, Details, SectionTitle, Theirs } from '../../ui/primitives';

/*
 * Ability files between workspaces (D292), like Recipes' files (D219).
 * "Export to a file…" on an ability's page downloads a version as a file
 * another workspace can bring in (GET /api/abilities/export). "Bring in an
 * ability file…" on Library → Abilities sends one up (POST
 * /api/abilities/import) — first to see what would come in, then, after a
 * styled question, as a draft: never approved or set up here; its code waits
 * for a check on this computer and an owner's approval. Its approval where it
 * came from is shown as information only.
 */

const QUERIES = ['ability.list', 'ability.show'];

export function abilityLink(name: string, version?: string): string {
  const p = new URLSearchParams({ ability: name });
  if (version) p.set('version', version);
  return `/library/abilities?${p.toString()}`;
}

/** Downloads a version of an ability as an ability file. */
export function ExportAbilityButton({ v }: { v: AbilityVersion }) {
  const exportIt = () => {
    const a = document.createElement('a');
    a.href = `/api/abilities/export?${new URLSearchParams({ ref: `${v.name}@${v.hash}` }).toString()}`;
    a.download = `${v.name}.ability.zip`;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    a.remove();
  };
  return (
    <Button size="sm" variant="ghost" icon={<DownloadSimple size={13} />} onClick={exportIt} data-ability-export
      title="A file another workspace can bring in: it comes in there as a draft its owners check and approve">
      Export to a file…
    </Button>
  );
}

function sendAbilityFile(file: File, o: { dryRun?: boolean; asVersion?: boolean }): Promise<AbilityImportOut> {
  const q = new URLSearchParams();
  if (o.dryRun) q.set('dryRun', '1');
  if (o.asVersion) q.set('asVersion', '1');
  return request<AbilityImportOut>('POST', `/api/abilities/import?${q.toString()}`, { body: file, headers: { 'Content-Type': 'application/zip' } });
}

/** "Bring in an ability file…": a file another workspace exported. */
export function ImportAbilityButton() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const input = useRef<HTMLInputElement | null>(null);
  const [busy, setBusy] = useState(false);
  const bring = async (file: File) => {
    setBusy(true);
    try {
      let asVersion = false;
      let dry: AbilityImportOut;
      try {
        dry = await sendAbilityFile(file, { dryRun: true });
      } catch (e) {
        const err = toApiError(e);
        if (err.code !== 'E_CONFLICT') throw e;
        // Another ability of the same name: only when the person says it is the same one.
        const same = await dialog.confirm({ title: 'An ability of that name is here already', body: <p>{err.message} {err.hint}</p>, confirmLabel: 'Bring it in as a new version', tone: 'danger' });
        if (!same) return;
        asVersion = true;
        dry = await sendAbilityFile(file, { dryRun: true, asVersion });
      }
      if (!dry.new) {
        toast({ tone: 'info', title: dry.message });
        navigate(abilityLink(dry.name, dry.version));
        return;
      }
      const ok = await dialog.confirm({ title: `Bring in “${dry.title}”?`, body: <ImportPreview out={dry} />, confirmLabel: 'Bring it in' });
      if (!ok) return;
      const out = await sendAbilityFile(file, { asVersion });
      for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message, body: 'Its page says how to check it on this computer; then an owner approves it.' });
      navigate(abilityLink(out.name, out.version));
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'The ability file didn’t come in', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <input
        ref={input}
        type="file"
        accept=".zip,application/zip"
        className="hidden"
        data-ability-import-file
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = '';
          if (f) void bring(f);
        }}
      />
      <Button size="sm" icon={<UploadSimple size={12} />} disabled={busy} onClick={() => input.current?.click()} className="ml-auto" data-ability-import
        title="Bring in an ability another workspace exported: it comes in as a draft, never approved or set up here">
        {busy ? 'Reading the file…' : 'Bring in an ability file…'}
      </Button>
    </>
  );
}

/** What a file would bring in, for the question before it comes. */
function ImportPreview({ out }: { out: AbilityImportOut }) {
  const from = out.from;
  const last = from?.decisions.length ? from.decisions[from.decisions.length - 1] : undefined;
  return (
    <div className="space-y-2 text-[13px]" data-ability-import-preview>
      <p>
        It comes in as {out.of ? 'a new version of the ability here (they share their history)' : 'a new ability'}: {plural(out.files.length, 'file')}, never approved or set up
        here.
      </p>
      <p className="text-muted">What leaves this computer when it runs: {out.leaves}</p>
      {out.lineage ? <p className="text-[12.5px] text-muted">{out.lineage}</p> : null}
      {from ? (
        <p className="text-[12.5px] text-subtle">
          {from.workspace ? (
            <>
              From <Theirs>{from.workspace}</Theirs>
            </>
          ) : (
            'From another workspace'
          )}
          , version {from.number} there
          {last ? (
            <>
              , {last.state.replace('_', ' ')} by <Theirs>{last.by}</Theirs>
            </>
          ) : null}
          . That is information only: here it is checked on this computer, then an owner approves it.
        </p>
      ) : null}
    </div>
  );
}

/** An imported version's page: where it came from, and what it waits for. */
export function ImportedSection({ v }: { v: AbilityVersion }) {
  const from = v.imported;
  return (
    <section aria-labelledby="ability-imported" className="rounded-[14px] bg-panel p-4 hairline" data-ability-imported>
      <SectionTitle id="ability-imported">Brought in from another workspace</SectionTitle>
      <p className="mt-2 flex items-start gap-1.5 text-[13px]">
        <Package size={14} className="mt-[3px] shrink-0 text-subtle" aria-hidden />
        <span>
          Its test hasn’t run on a computer here, and nobody here approved it. Check it on this computer first (it runs once in the sandbox with the network off); then an owner
          reads what it may do and approves it.
        </span>
      </p>
      <Details label="How to check it">
        <p className="text-[12.5px] text-muted">In a terminal on this computer:</p>
        <code className="mt-1 block text-[12px]" data-ability-check-command>
          metarush ability check --version {v.name}@{v.hash.slice(0, 12)}
        </code>
      </Details>
      {from ? (
        <div className="mt-3 text-[12.5px] text-muted">
          <p>
            {from.workspace ? (
              <>
                From <Theirs>{from.workspace}</Theirs>
              </>
            ) : (
              'From another workspace'
            )}{' '}
            (version {from.number} there), brought in <span title={absolute(from.at)}>{stamp(from.at)}</span>.
          </p>
          {from.decisions.length ? (
            <ul className="mt-1 space-y-0.5">
              {from.decisions.map((d, i) => (
                <li key={i}>
                  There: {d.state.replace('_', ' ')} by <Theirs>{d.by}</Theirs>, <span title={absolute(d.at)}>{stamp(d.at)}</span>
                  {d.note ? (
                    <>
                      {' '}
                      — <Theirs>{d.note}</Theirs>
                    </>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}
          <p className="mt-1 text-subtle">Information only: nothing there approves it here.</p>
        </div>
      ) : null}
    </section>
  );
}
