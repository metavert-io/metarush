import { useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle, Package, UploadSimple, WarningCircle } from '@phosphor-icons/react';
import { call, request, toApiError } from '../../api/client';
import { plural } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Button, cx, Theirs } from '../../ui/primitives';
import { inApp, RECIPE_QUERIES, recipesLink, type RecipeImportCodeView, type RecipeImportOut, type RecipeImportView } from './api';

/*
 * Recipe files between workspaces (D219). "Export to a file…" on a recipe's
 * page downloads the recipe as a file another workspace can bring in (GET
 * /api/recipes/export: its document, sheet, pictures, footage and approved
 * code by hash). "Import a recipe…" on the Recipes tab sends a file up (POST
 * /api/recipes/import) — first to see what would come in, then, after a styled
 * question, to bring it in as a draft whose code waits for a person here. An
 * imported version's page says where it came from and what its code waits for.
 */

/** Downloads a version of a recipe as a recipe file. */
export function exportRecipeFile(name: string, version?: number) {
  const q = new URLSearchParams({ recipe: name });
  if (version) q.set('version', String(version));
  const a = document.createElement('a');
  a.href = `/api/recipes/export?${q.toString()}`;
  a.download = `${name}.recipe.zip`;
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function sendRecipeFile(file: File, dryRun: boolean): Promise<RecipeImportOut> {
  const q = new URLSearchParams({ fileName: file.name });
  if (dryRun) q.set('dryRun', '1');
  return request<RecipeImportOut>('POST', `/api/recipes/import?${q.toString()}`, { body: file, headers: { 'Content-Type': 'application/zip' } });
}

/** Choosing a recipe file and bringing it in, after saying what would come. */
export function useImportRecipe() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const input = useRef<HTMLInputElement | null>(null);
  const [busy, setBusy] = useState(false);
  const bring = async (file: File) => {
    setBusy(true);
    try {
      const dry = await sendRecipeFile(file, true);
      if (dry.same) {
        toast({ tone: 'info', title: dry.message });
        if (dry.name) navigate(recipesLink({ recipe: dry.name }));
        return;
      }
      const ok = await dialog.confirm({ title: `Bring in “${dry.title}”?`, body: <ImportPreview out={dry} />, confirmLabel: 'Bring it in' });
      if (!ok) return;
      const out = await sendRecipeFile(file, false);
      for (const k of RECIPE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message });
      navigate(recipesLink({ recipe: out.name }));
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'The recipe file didn’t come in', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  const picker = (
    <input
      ref={input}
      type="file"
      accept=".zip,application/zip"
      className="hidden"
      data-recipe-import-file
      onChange={(e) => {
        const f = e.target.files?.[0];
        e.target.value = '';
        if (f) void bring(f);
      }}
    />
  );
  return { choose: () => input.current?.click(), picker, busy };
}

/** "Import a recipe…": a file another workspace exported. */
export function ImportButton({ className }: { className?: string }) {
  const imp = useImportRecipe();
  return (
    <>
      {imp.picker}
      <Button size="sm" icon={<UploadSimple size={12} />} disabled={imp.busy} onClick={imp.choose} className={className} data-recipe-import
        title="Bring in a recipe another workspace exported: it comes in as a draft, and its code waits for a person's approval here">
        {imp.busy ? 'Reading the file…' : 'Import a recipe…'}
      </Button>
    </>
  );
}

const KIND_WORDS: Record<RecipeImportCodeView['kind'], string> = {
  motion: 'The motion graphic',
  library: 'The shared library',
  remotion: 'The Remotion project',
};

/** What a file would bring in, for the question before it comes. */
function ImportPreview({ out }: { out: RecipeImportOut }) {
  const where = out.new ? `a new recipe, “${out.title}”` : `a new draft of “${out.title}” (they share their history)`;
  const project = out.newProject ? `a new project, “${out.project}”` : `the project “${out.project}”`;
  return (
    <div className="space-y-2" data-recipe-import-preview>
      <p>
        It holds <Theirs>{out.from}</Theirs>. It comes in as {where}, a draft, with {plural(out.pictures, 'picture')}, {plural(out.footage, 'clip')} and{' '}
        {plural(out.code.length, 'piece')} of code, kept in {project}.
      </p>
      {out.code.length ? <CodeList code={out.code} /> : null}
      {out.notes.length ? (
        <ul className="list-disc space-y-0.5 pl-5 text-[12.5px]">
          {out.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      ) : null}
      <p className="text-[12.5px] text-subtle">Its words are another workspace’s: a person here makes the version current before it runs.</p>
    </div>
  );
}

function CodeList({ code, checks }: { code: RecipeImportCodeView[]; checks?: boolean }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [checking, setChecking] = useState<string | null>(null);
  // A graphic that came with the file is checked here first (test frames in the jailed browser); a person approves it after.
  const check = async (c: RecipeImportCodeView) => {
    if (!c.check) return;
    setChecking(c.hash);
    try {
      const out = await call('motion.check', { version: c.check.version, ...(c.check.libraries ? { libraries: c.check.libraries } : {}), ...(c.check.projectId ? { projectId: c.check.projectId } : {}) });
      for (const k of [...RECIPE_QUERIES, 'motion.list', 'motion.show'] as const) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: out.ok ? 'success' : 'error', title: out.summary });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'The check didn’t run', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setChecking(null);
    }
  };
  return (
    <ul className="space-y-1" aria-label="The code it came with">
      {code.map((c) => {
        const to = c.check ? undefined : inApp(c.url);
        return (
          <li key={c.kind + c.hash} className="flex min-w-0 items-start gap-1.5 text-[12.5px]" data-recipe-import-code={c.name}>
            {c.ready ? <CheckCircle size={14} className="mt-[2px] shrink-0 text-ok" /> : <WarningCircle size={14} className="mt-[2px] shrink-0 text-accent-text" />}
            <span className="min-w-0">
              {KIND_WORDS[c.kind]} <Theirs>{c.name}</Theirs>: {c.state}
              {!c.ready && c.next ? <span className="text-muted"> — {c.next}</span> : null}
              {to ? (
                <>
                  {' '}
                  <Link to={to} className="font-medium text-accent-text underline-offset-2 hover:underline">
                    Look at it
                  </Link>
                </>
              ) : null}
              {checks && c.check ? (
                <>
                  {' '}
                  <Button size="sm" variant="ghost" disabled={checking !== null} onClick={() => void check(c)} data-recipe-import-check={c.name}>
                    {checking === c.hash ? 'Checking…' : 'Check it here'}
                  </Button>
                </>
              ) : null}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** An imported version's page: where it came from, and what its code waits for here. */
export function ImportedNote({ imported, className }: { imported: RecipeImportView; className?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className={cx('rounded-[12px] bg-inset px-4 py-3 text-[13px] text-muted', className)} data-recipe-imported>
      <div className="flex items-start gap-2">
        <Package size={16} className="mt-[2px] shrink-0 text-accent-text" aria-hidden />
        <div className="min-w-0 flex-1 space-y-2">
          <p className="text-fg">{imported.summary}</p>
          {imported.code.length ? <CodeList code={imported.code} checks /> : null}
          {imported.notes.length ? (
            <div>
              <button type="button" className="text-[12.5px] font-medium text-accent-text underline-offset-2 hover:underline" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
                {open ? 'Hide' : 'Show'} what changed on the way in ({imported.notes.length})
              </button>
              {open ? (
                <ul className="mt-1 list-disc space-y-0.5 pl-5 text-[12.5px]">
                  {imported.notes.map((n) => (
                    <li key={n}>{n}</li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}
          {imported.project ? (
            <p className="text-[12px] text-subtle">
              What came with it is in{' '}
              {imported.projectId ? (
                <Link to={`/p/${imported.projectId}`} className="underline-offset-2 hover:underline">
                  <Theirs>{imported.project}</Theirs>
                </Link>
              ) : (
                <Theirs>{imported.project}</Theirs>
              )}
              {imported.file ? (
                <>
                  , from the file <Theirs>{imported.file}</Theirs>
                </>
              ) : null}
              .
            </p>
          ) : null}
        </div>
      </div>
    </div>
  );
}
