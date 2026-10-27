import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Archive, ArrowCounterClockwise, ArrowLeft, ArrowsLeftRight, CaretDown, CaretRight, CheckCircle, CookingPot, DownloadSimple, Play, SealCheck, Sparkle, UploadSimple, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { absolute, stamp } from '../../lib/format';
import { MenuButton } from '../../layout/assistant/Menu';
import { RecoveringVideo } from '../../player/Trouble';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, EmptyState, ErrorPanel, Freshness, Skeleton, Spinner, Theirs, Thumb } from '../../ui/primitives';
import { RECIPE_QUERIES, recipesLink, runLinks, runTone, stateShort, type RecipeDiffOut, type RecipeRunBrief, type RecipeShowOut, type RecipeVersionRow } from './api';
import { Differences } from './Differences';
import { exportRecipeFile, ImportedNote, useImportRecipe } from './Files';
import { KIND_ICON } from './Gallery';
import { PlannedBanner, PlansByItself } from './PlansByItself';
import { useRunDialog } from './RunDialog';
import { Suggestions } from './Suggestions';

/*
 * One recipe (?recipe=<name>, and ?version=<n> for one of its versions): what
 * it makes and where runs go, its example, what changes each run (with
 * limits and where it goes), its sections, rules, sign-offs and hints in
 * plain words, its versions — which one is current, Claude's drafts marked,
 * made current only by a person, and what each changed from the one before
 * in plain words (D212) — and its runs, two of which play side by side; what
 * people kept changing in runs, offered as a draft (D217); when it plans a run
 * by itself, and the plan it made, waiting for a person to run it (D218: the
 * bell's link, ?planned=1, opens Run with… on it once).
 */

export function RecipePage({ name, version, planned }: { name: string; version?: number; planned?: boolean }) {
  const show = useOp('recipe.show', { recipe: name, ...(version ? { version: String(version) } : {}) });
  const run = useRunDialog();
  const navigate = useNavigate();
  const actions = useRecipeActions();
  const imp = useImportRecipe();
  const [picked, setPicked] = useState<string[]>([]);
  const opened = useRef(false);
  useEffect(() => {
    const d = show.data;
    if (!planned || opened.current || !d) return;
    opened.current = true;
    navigate(recipesLink({ recipe: name }), { replace: true });
    if (!d.recipe.archived && d.versions.some((x) => x.state === 'current')) void run({ recipe: d.recipe.name, title: d.recipe.title });
  }, [planned, show.data]); // eslint-disable-line react-hooks/exhaustive-deps
  if (show.isLoading) {
    return (
      <div className="space-y-4" aria-busy="true">
        <Skeleton className="h-10 w-72" />
        <Skeleton className="h-64 rounded-[16px]" />
      </div>
    );
  }
  if (show.error || !show.data) {
    return (
      <div className="space-y-4">
        <BackLink />
        <ErrorPanel error={show.error} onRetry={() => void show.refetch()} title={show.error?.code === 'E_NOT_FOUND' ? 'There is no such recipe' : undefined} />
      </div>
    );
  }
  const d: RecipeShowOut = show.data;
  const r = d.recipe;
  const v = d.version;
  const current = d.versions.find((x) => x.state === 'current');
  const canRun = !r.archived && !!current;
  const stills = (v.check?.stills ?? []).filter((s) => s.url);
  // Two runs side by side (D212): the earlier on the left.
  const sideBySide = () => {
    const byNumber = (id: string) => d.runs.find((x) => x.id === id)?.number ?? 0;
    const [x, y] = [...picked].sort((p, q) => byNumber(p) - byNumber(q));
    navigate(recipesLink({ compare: [x!, y!] }));
  };
  return (
    <div className="space-y-8" data-recipe-page={r.name} data-version={v.number}>
      <div>
        <div className="mb-3 flex items-center gap-2">
          <BackLink />
          <Freshness className="ml-auto" at={show.dataUpdatedAt || undefined} fetching={show.isFetching} onRefresh={() => void show.refetch()} />
        </div>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start">
          <div className="min-w-0 flex-1">
            <h2 className="flex min-w-0 flex-wrap items-center gap-2 text-[24px] font-semibold leading-tight tracking-[-0.025em] text-fg sm:text-[28px]">
              <CookingPot size={22} className="shrink-0 text-accent-text" aria-hidden />
              <Theirs className="min-w-0 break-words">{r.title}</Theirs>
            </h2>
            <p className="mt-1.5 max-w-[80ch] text-[13.5px] leading-relaxed text-muted">
              {[r.makes ? `Makes ${r.makes}` : '', d.lands].filter(Boolean).join(', ')}.
            </p>
            <div className="mt-2 flex flex-wrap items-center gap-1.5">
              {current ? <Badge tone="ok">Version {current.number} is current</Badge> : <Badge tone="info">No version is current yet</Badge>}
              {r.draft ? <Badge tone="info">Version {r.draft} is a draft</Badge> : null}
              {r.archived ? <Badge>Archived</Badge> : null}
              <span className="text-[12px] text-subtle">{r.runs === 1 ? '1 run' : `${r.runs} runs`}</span>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {imp.picker}
            <Button variant="primary" icon={<Play size={14} weight="fill" />} disabled={!canRun} onClick={() => void run({ recipe: r.name, title: r.title })} data-run-with={r.name}
              title={canRun ? undefined : r.archived ? 'Restore it to run it' : 'A person makes a version current first'}>
              Run with…
            </Button>
            <MenuButton
              label={`More for ${r.title}`}
              items={[
                // A file another workspace brings in (D219): its code waits for a person there.
                { label: 'Export to a file…', icon: <DownloadSimple size={14} />, onSelect: () => exportRecipeFile(r.name, v.number), hint: 'For another workspace' },
                { label: 'Import from a file…', icon: <UploadSimple size={14} />, onSelect: imp.choose, disabled: imp.busy, hint: 'A newer version from another workspace comes in as a draft' },
                r.archived
                  ? { label: 'Restore', icon: <ArrowCounterClockwise size={14} />, onSelect: () => void actions.archive(r.name, r.title, true), hint: 'Back on the tab, ready to run' }
                  : { label: 'Archive…', icon: <Archive size={14} />, onSelect: () => void actions.archive(r.name, r.title, false), hint: 'Its versions and runs stay' },
              ]}
            />
          </div>
        </div>
      </div>

      <PlannedBanner name={r.name} canRun={canRun} onRun={() => void run({ recipe: r.name, title: r.title })} />

      {d.imported ? <ImportedNote imported={d.imported} /> : null}

      {v.state !== 'current' ? (
        <div className="rounded-[12px] bg-info-soft px-4 py-3 text-[13px] text-info" data-version-banner>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <span className="min-w-0 flex-1">
              You are looking at version {v.number}, {v.state === 'draft' ? `a draft ${v.by.startsWith('Claude') ? 'Claude made' : 'by ' + v.by}` : 'an earlier one'}
              {v.note ? `: “${v.note}”` : ''}.{current ? ` Runs make version ${current.number}.` : ''}
            </span>
            <MakeCurrentButton name={r.name} title={r.title} v={v} current={current} />
            {current ? (
              <Link to={recipesLink({ recipe: r.name })} className="font-medium underline-offset-2 hover:underline">
                Show the current one
              </Link>
            ) : null}
          </div>
          {current ? <AgainstCurrent name={r.name} v={v} current={current} /> : null}
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,380px)_minmax(0,1fr)]">
        <section aria-label="What it makes" className="min-w-0">
          {r.example ? (
            <figure>
              <RecoveringVideo src={r.example.videoUrl} poster={r.example.thumbUrl} controls playsInline preload="metadata" className="w-full rounded-[14px] bg-black" wrapClassName="w-full" />
              <figcaption className="mt-1.5 text-[12px] text-subtle">The video it was saved from: what a run makes.</figcaption>
            </figure>
          ) : (
            <div className="grid aspect-video place-items-center rounded-[14px] bg-inset text-[12.5px] text-subtle">
              <span className="flex flex-col items-center gap-1.5">
                <CookingPot size={24} /> No example video yet
              </span>
            </div>
          )}
          {stills.length ? (
            <div className="mt-4">
              <h3 className="mb-1.5 text-[12.5px] font-medium text-muted">Each section, with its own inputs</h3>
              <ul className="grid grid-cols-3 gap-1.5" aria-label="Stills of each section">
                {stills.slice(0, 9).map((s, i) => (
                  <li key={i}>
                    <Thumb src={s.url} alt={`The section “${s.section}”`} className="aspect-video w-full rounded-[8px]" />
                    <span className="mt-0.5 block truncate text-[11px] text-subtle">{s.section}</span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
          <CheckLine v={v} />
        </section>

        <div className="min-w-0 space-y-6">
          <section aria-labelledby="rcp-inputs">
            <h3 id="rcp-inputs" className="mb-2 text-[15px] font-semibold text-fg">
              What changes each run
            </h3>
            {d.inputs.length === 0 ? (
              <p className="text-[13px] text-muted">Nothing: every run makes the same video again.</p>
            ) : (
              <ul className="space-y-2">
                {d.inputs.map((i) => (
                  <li key={i.name} className="rounded-[12px] bg-hover/40 px-3.5 py-2.5" data-recipe-input={i.name}>
                    <div className="flex flex-wrap items-baseline gap-x-2">
                      <span className="inline-flex items-center gap-1.5 text-[13px] font-medium text-fg">
                        <span className="text-subtle">{KIND_ICON[i.kind]}</span>
                        {i.label}
                      </span>
                      {i.limits ? <span className="text-[11.5px] text-subtle">{i.limits}</span> : null}
                    </div>
                    <div className="mt-0.5 text-[12.5px] text-muted">
                      Now: {i.default}
                      {i.from ? `; a run takes ${i.from}` : ''}
                    </div>
                    {i.goes.length ? <div className="mt-0.5 text-[12px] text-subtle">Goes to {i.goes.join('; ')}.</div> : null}
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-labelledby="rcp-sections">
            <h3 id="rcp-sections" className="mb-2 text-[15px] font-semibold text-fg">
              Its sections
            </h3>
            <ol className="space-y-1.5">
              {d.sections.map((s, i) => (
                <li key={s.name} className="grid grid-cols-[22px_minmax(0,1fr)] gap-x-2 text-[13px]" data-recipe-section={s.name}>
                  <span className="tc pt-[1px] text-[12px] text-subtle">{i + 1}</span>
                  <span className="min-w-0">
                    <span className="font-medium text-fg">{s.name}</span>{' '}
                    <Badge tone={s.kind === 'chosen' ? 'accent' : 'neutral'} className="ml-1 align-[1px]">
                      {s.kind === 'chosen' ? 'Claude chooses' : 'The same every run'}
                    </Badge>
                    <span className="ml-2 text-[12px] text-subtle">{s.length}</span>
                    <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">{s.what}</span>
                    {s.purpose && s.purpose !== 'the strongest moments' ? <span className="block text-[12px] text-subtle">For {s.purpose}</span> : null}
                  </span>
                </li>
              ))}
            </ol>
          </section>

          {d.rules.length || d.signoffs.length || d.hints.length ? (
            <section aria-labelledby="rcp-rules" className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <h3 id="rcp-rules" className="sr-only">
                Rules, sign-offs and hints
              </h3>
              {d.rules.length || d.signoffs.length ? (
                <div>
                  <h4 className="mb-1 text-[13px] font-semibold text-fg">Rules every run keeps</h4>
                  <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
                    {d.rules.map((x) => (
                      <li key={x}>{x}</li>
                    ))}
                    {d.signoffs.map((x) => (
                      <li key={x}>A person signs off: {x}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
              {d.hints.length ? (
                <div>
                  <h4 className="mb-1 text-[13px] font-semibold text-fg">Hints for Claude</h4>
                  <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
                    {d.hints.map((x) => (
                      <li key={x}>{x}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </section>
          ) : null}
        </div>
      </div>

      <section aria-labelledby="rcp-runs" data-recipe-runs>
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <h3 id="rcp-runs" className="text-[15px] font-semibold text-fg">
            Runs
          </h3>
          {d.runs.length > 1 ? (
            <>
              <span className="text-[12px] text-subtle">Tick two to watch them side by side.</span>
              <Button
                size="sm"
                variant={picked.length === 2 ? 'primary' : 'ghost'}
                className="ml-auto"
                disabled={picked.length !== 2}
                icon={<ArrowsLeftRight size={13} />}
                onClick={sideBySide}
                data-compare-runs-button
              >
                Side by side
              </Button>
            </>
          ) : null}
        </div>
        {d.runs.length === 0 ? (
          <div className="panel">
            <EmptyState compact icon={<Play size={20} />} title="No runs yet" body={canRun ? 'Run it with new inputs: you see what it makes and what it costs first.' : 'Once a person makes a version current, it can run.'} />
          </div>
        ) : (
          <ul className="divide-y divide-line overflow-hidden rounded-[14px] hairline">
            {d.runs.map((x) => (
              <RunRow key={x.id} x={x} picked={picked.includes(x.id)} onPick={(on) => setPicked((p) => (on ? [...p.filter((y) => y !== x.id), x.id].slice(-2) : p.filter((y) => y !== x.id)))} />
            ))}
          </ul>
        )}
      </section>

      {r.runs > 0 ? <Suggestions name={r.name} /> : null}

      <PlansByItself name={r.name} />

      <section aria-labelledby="rcp-versions" data-recipe-versions>
        <h3 id="rcp-versions" className="mb-2 text-[15px] font-semibold text-fg">
          Versions
        </h3>
        <ul className="space-y-1.5">
          {d.versions.map((x) => (
            <VersionRow key={x.number} name={r.name} title={r.title} x={x} here={x.number === v.number} current={current} first={x.number === Math.min(...d.versions.map((y) => y.number))} />
          ))}
        </ul>
      </section>
    </div>
  );
}

/**
 * One version: whose, when, its note and check, Make current… (people only)
 * and what it changed from the version it was made from, opened on demand (D212).
 */
function VersionRow({ name, title, x, here, current, first }: { name: string; title: string; x: RecipeVersionRow; here: boolean; current?: RecipeVersionRow; first: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <li className={cx('rounded-[12px] px-3 py-2', here ? 'bg-panel hairline' : 'hover:bg-hover/60')} data-recipe-version={x.number} data-version-state={x.state}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Link to={recipesLink({ recipe: name, version: x.state === 'current' ? undefined : x.number })} className="text-[13px] font-medium text-fg hover:underline">
          Version {x.number}
        </Link>
        <Badge tone={x.state === 'current' ? 'ok' : x.state === 'draft' ? 'info' : 'neutral'}>{x.state === 'current' ? 'Current' : x.state === 'draft' ? 'Draft' : 'Earlier'}</Badge>
        {x.by.startsWith('Claude') ? (
          <span className="inline-flex items-center gap-1 text-[12px] text-accent-text" data-by-claude>
            <Sparkle size={11} weight="fill" aria-hidden />
            {x.by}
          </span>
        ) : (
          <span className="text-[12px] text-muted">{x.by}</span>
        )}
        <span className="text-[12px] text-subtle" title={absolute(x.at)}>
          {stamp(x.at)}
        </span>
        {x.note ? (
          <span className="min-w-0 truncate text-[12px] text-muted">
            “<Theirs>{x.note}</Theirs>”
          </span>
        ) : null}
        {!x.ok ? (
          <span className="inline-flex items-center gap-1 text-[12px] text-danger">
            <WarningCircle size={12} weight="fill" aria-hidden /> Its check found problems
          </span>
        ) : null}
        <span className="ml-auto flex flex-wrap items-center gap-1">
          {first ? (
            <span className="text-[12px] text-subtle">The first version</span>
          ) : (
            <button
              type="button"
              onClick={() => setOpen((o) => !o)}
              aria-expanded={open}
              className="inline-flex h-7 items-center gap-1 rounded-[8px] px-2 text-[12px] font-medium text-muted hover:bg-hover hover:text-fg"
              data-version-changes-toggle={x.number}
            >
              {open ? <CaretDown size={11} /> : <CaretRight size={11} />} What changed
            </button>
          )}
          {x.state !== 'current' ? <MakeCurrentButton name={name} title={title} v={x} current={current} small /> : null}
        </span>
      </div>
      {open ? <VersionChanges name={name} to={x.number} /> : null}
    </li>
  );
}

/** What a version changed from the one it was made from (recipe.diff). */
function VersionChanges({ name, to }: { name: string; to: number }) {
  const q = useOp('recipe.diff', { recipe: name, to: String(to) }, { staleTime: 60_000 });
  if (q.isLoading) {
    return (
      <p className="mt-2 flex items-center gap-1.5 text-[12px] text-subtle">
        <Spinner size={11} /> Comparing…
      </p>
    );
  }
  if (q.error || !q.data) return <ErrorPanel compact error={q.error} className="mt-2" onRetry={() => void q.refetch()} />;
  return <DiffBlock d={q.data} className="mt-2" />;
}

/** A version against the one runs make now, on the banner of a version that isn't current. */
function AgainstCurrent({ name, v, current }: { name: string; v: RecipeVersionRow; current: RecipeVersionRow }) {
  const q = useOp('recipe.diff', { recipe: name, to: String(v.number), from: String(current.number) }, { staleTime: 60_000 });
  if (!q.data || q.data.same) return null;
  return (
    <div className="mt-3 rounded-[10px] bg-raised px-3 py-2.5 text-fg hairline" data-against-current>
      <DiffBlock d={q.data} />
    </div>
  );
}

function DiffBlock({ d, className }: { d: RecipeDiffOut; className?: string }) {
  return (
    <div className={cx('space-y-2', className)} data-version-changes={d.to.number}>
      <p className="text-[12.5px] text-muted">{d.summary}</p>
      <Differences parts={d.parts} removedRules={d.removedRules} compact />
    </div>
  );
}

function BackLink() {
  return (
    <Link to={recipesLink()} className="-ml-1 inline-flex items-center gap-1 rounded-[8px] px-1.5 py-1 text-[12.5px] text-muted hover:bg-hover hover:text-fg" data-back-to-recipes>
      <ArrowLeft size={13} /> All recipes
    </Link>
  );
}

function CheckLine({ v }: { v: RecipeVersionRow }) {
  const c = v.check;
  if (!c) return null;
  return c.ok ? (
    <p className="mt-3 flex items-center gap-1.5 text-[12.5px] text-ok">
      <CheckCircle size={14} weight="fill" aria-hidden /> Its check found no problems.
    </p>
  ) : (
    <div className="mt-3 rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px]">
      <div className="mb-0.5 flex items-center gap-1.5 font-medium text-danger">
        <WarningCircle size={14} weight="fill" aria-hidden /> Its check found something to fix
      </div>
      <ul className="list-disc pl-5 text-fg">
        {(c.problems ?? []).map((p, i) => (
          <li key={i}>{p.message}</li>
        ))}
      </ul>
    </div>
  );
}

function RunRow({ x, picked, onPick }: { x: RecipeRunBrief; picked: boolean; onPick: (on: boolean) => void }) {
  const tone = runTone(x.state, x.waitingFor);
  const where = runLinks({ projectId: x.projectId });
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 bg-raised px-3 py-2.5" data-run-row={x.number}>
      <input type="checkbox" checked={picked} onChange={(e) => onPick(e.target.checked)} aria-label={`Compare run ${x.number}`} className="h-4 w-4 accent-[var(--accent)]" />
      <Link to={recipesLink({ run: x.id })} className="text-[13px] font-medium text-fg hover:underline">
        Run {x.number}
      </Link>
      <Badge tone={tone} title={x.stateLabel}>
        {stateShort(x)}
      </Badge>
      {x.approved ? (
        <Badge tone="ok" icon={<SealCheck size={11} weight="fill" />}>
          Approved
        </Badge>
      ) : null}
      <span className="text-[12px] text-muted">
        {x.by}, <span title={absolute(x.at)}>{stamp(x.at)}</span>
      </span>
      {x.project && where.project ? (
        <Link to={where.project} className="min-w-0 truncate text-[12px] text-accent-text hover:underline">
          <Theirs>{x.project}</Theirs>
          {x.version ? `, version ${x.version}` : ''}
        </Link>
      ) : null}
      <span className="tc ml-auto text-[12px] text-subtle">{x.spentWords === 'nothing' ? 'spent nothing' : x.spentWords}</span>
    </li>
  );
}

/** Make a version current (people only): it asks first, naming rules the new one no longer keeps. */
function MakeCurrentButton({ name, title, v, current, small }: { name: string; title: string; v: RecipeVersionRow; current?: RecipeVersionRow; small?: boolean }) {
  const actions = useRecipeActions();
  return (
    <Button
      size="sm"
      variant={small ? 'ghost' : 'secondary'}
      icon={<SealCheck size={13} />}
      disabled={!v.ok}
      title={v.ok ? undefined : 'Its check found problems: fix them first'}
      onClick={() => void actions.makeCurrent(name, title, v, current)}
      data-make-current={v.number}
    >
      Make current…
    </Button>
  );
}

function useRecipeActions() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const refresh = () => {
    for (const k of RECIPE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
  const fail = (title: string, e: unknown) => {
    const err = toApiError(e);
    toast({ tone: 'error', title, body: [err.message, err.hint].filter(Boolean).join(' ') });
  };
  return {
    makeCurrent: async (name: string, title: string, v: RecipeVersionRow, current?: RecipeVersionRow) => {
      // What it changes from the version runs make now, and the rules it no
      // longer keeps: said before, not after (D212).
      let diff: RecipeDiffOut | null = null;
      try {
        if (current) diff = await call('recipe.diff', { recipe: name, to: String(v.number), from: String(current.number) });
      } catch {
        diff = null;
      }
      const ok = await dialog.confirm({
        title: `Make version ${v.number} of “${title}” current?`,
        body: (
          <div className="space-y-2" data-make-current-dialog>
            <p>Runs make it from now on. Runs already made keep the version they made.</p>
            {diff && !diff.same ? (
              <>
                <p className="text-[12.5px] text-muted">Compared with version {current?.number}, the current one:</p>
                <Differences parts={diff.parts} removedRules={diff.removedRules} compact />
              </>
            ) : null}
          </div>
        ),
        confirmLabel: 'Make it current',
      });
      if (!ok) return;
      try {
        const out = await call('recipe.publish', { recipe: name, version: String(v.number) });
        refresh();
        toast({ tone: 'success', title: out.message });
        navigate(recipesLink({ recipe: name }));
      } catch (e) {
        fail("It didn't become current", e);
      }
    },
    archive: async (name: string, title: string, restore: boolean) => {
      if (!restore) {
        const ok = await dialog.confirm({
          title: `Archive “${title}”?`,
          body: 'It leaves the Recipes tab and can’t run. Its versions and runs stay, and you can restore it.',
          confirmLabel: 'Archive',
        });
        if (!ok) return;
      }
      try {
        const out = await call('recipe.archive', { recipe: name, ...(restore ? { restore: true } : {}) });
        refresh();
        toast({ tone: 'success', title: out.message });
        if (!restore) navigate(recipesLink());
      } catch (e) {
        fail(restore ? "It wasn't restored" : "It wasn't archived", e);
      }
    },
  };
}
