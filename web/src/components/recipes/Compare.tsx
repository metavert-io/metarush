import { Link } from 'react-router';
import { ArrowsLeftRight, FilmStrip } from '@phosphor-icons/react';
import { cx, Theirs } from '../../ui/primitives';
import { inApp, recipesLink, type RecipeCompareOut } from './api';

/*
 * What changed between two runs of a recipe (recipe.compare): which inputs
 * changed, the notes, which recipe versions made them, what each cost, the
 * changes between the versions they made (the cut sheets' difference) and
 * links to both review copies. The two runs' page (SideBySide.tsx, D212) plays
 * the review copies next to each other above it.
 */

export function CompareDetails({ c, labels }: { c: RecipeCompareOut; labels?: Record<string, string> }) {
  const sameProject = c.cut && c.cut.left.projectId === c.cut.right.projectId;
  const versions = c.cut ? (sameProject ? `/p/${c.cut.left.projectId}/compare?left=${c.cut.left.id}&right=${c.cut.right.id}` : inApp(c.cut.sheetUrl)) : undefined;
  const renders = c.renders.map((u) => inApp(u));
  return (
    <div className="space-y-4" data-compare-runs>
      {c.summary.length ? (
        <ul className="list-disc space-y-0.5 pl-5 text-[13px] leading-relaxed text-fg">
          {c.summary.map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ul>
      ) : null}
      {c.problems?.length ? (
        <ul className="space-y-0.5 text-[12.5px] text-danger">
          {c.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ) : null}
      <div className="overflow-x-auto rounded-[12px] hairline">
        <table className="w-full min-w-[320px] text-left text-[12.5px]">
          <thead>
            <tr className="border-b border-line text-[11.5px] text-subtle">
              <th className="px-3 py-2 font-medium">What changes</th>
              <th className="px-3 py-2 font-medium">
                <Link to={recipesLink({ run: c.left.id })} className="hover:underline">
                  Run {c.left.number}
                </Link>
              </th>
              <th className="px-3 py-2 font-medium">
                <Link to={recipesLink({ run: c.right.id })} className="hover:underline">
                  Run {c.right.number}
                </Link>
              </th>
            </tr>
          </thead>
          <tbody>
            {c.inputs.map((row) => (
              <tr key={row.name} className={cx('border-b border-line last:border-0', row.changed && 'bg-accent-soft/50')} data-compare-input={row.name} data-changed={row.changed ? 'yes' : 'no'}>
                <td className="px-3 py-2 align-top font-medium text-fg">{labels?.[row.name] ?? row.name}</td>
                <td className="break-words px-3 py-2 align-top text-muted">{row.left}</td>
                <td className={cx('break-words px-3 py-2 align-top', row.changed ? 'text-fg' : 'text-muted')}>{row.right}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <dl className="grid grid-cols-1 gap-x-4 gap-y-1 text-[12.5px] sm:grid-cols-[auto_1fr]">
        <dt className="text-subtle">Recipe versions</dt>
        <dd className="text-fg">{c.versions}</dd>
        <dt className="text-subtle">What they cost</dt>
        <dd className="text-fg">{c.cost}</dd>
        {c.notes?.length ? (
          <>
            <dt className="text-subtle">What they were for</dt>
            <dd className="text-fg">
              {c.notes.map((n, i) => (
                <span key={i} className="block">
                  “<Theirs>{n}</Theirs>”
                </span>
              ))}
            </dd>
          </>
        ) : null}
      </dl>
      {c.cut ? (
        <section aria-label="What changed in the cut">
          <h3 className="mb-1 text-[13px] font-semibold text-fg">What changed in the cut</h3>
          {c.cut.changes.length ? (
            <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
              {c.cut.changes.map((x) => (
                <li key={x}>{x}</li>
              ))}
            </ul>
          ) : (
            <p className="text-[12.5px] text-muted">Nothing: the two versions are the same cut.</p>
          )}
        </section>
      ) : null}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-line pt-3 text-[12.5px]">
        {versions ? (
          <Link to={versions} className="inline-flex items-center gap-1 font-medium text-accent-text hover:underline">
            <ArrowsLeftRight size={13} /> Compare the versions of the cut
          </Link>
        ) : null}
        {renders.map((u, i) =>
          u ? (
            <Link key={i} to={u} className="inline-flex items-center gap-1 font-medium text-accent-text hover:underline">
              <FilmStrip size={13} /> Run {i === 0 ? c.left.number : c.right.number}’s review copy
            </Link>
          ) : null,
        )}
      </div>
    </div>
  );
}
