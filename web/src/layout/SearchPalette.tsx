import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router';
import { ChatCenteredText, ChatText, Eye, FilmSlate, FolderSimple, GitBranch, ImageSquare, MagnifyingGlass, VideoCamera } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { SnippetPart } from '../api/types';
import { duration, plural, relative, timecode } from '../lib/format';
import { ModalFrame } from '../ui/Dialog';
import { cx, Kbd, Spinner, Thumb } from '../ui/primitives';

type Hit = {
  key: string;
  group: 'moments' | 'visuals' | 'videos' | 'resources' | 'projects' | 'revisions' | 'comments';
  title: ReactNode;
  sub: ReactNode;
  thumb?: string;
  icon: ReactNode;
  to: string;
  label: string;
};

const GROUPS: { id: Hit['group']; label: string }[] = [
  { id: 'moments', label: 'Footage moments' },
  { id: 'visuals', label: 'On screen' },
  { id: 'videos', label: 'Videos' },
  { id: 'resources', label: 'Resources' },
  { id: 'projects', label: 'Projects' },
  { id: 'revisions', label: 'Versions' },
  { id: 'comments', label: 'Comments' },
];

/** Plain words for the part of a version that matched. */
const matchedWord: Record<string, string> = { title: 'title', instruction: 'request', rationale: 'reasoning' };

export function Snippet({ parts, className }: { parts: SnippetPart[]; className?: string }) {
  return (
    <span className={className}>
      {parts.map((p, i) =>
        p.hit ? (
          <mark key={i} className="rounded-[3px] bg-accent-soft px-[1px] font-medium text-fg">
            {p.text}
          </mark>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </span>
  );
}

/** Where a source opens: its project's Sources tab, or the inbox viewer, at a time with words highlighted (or on a tab). */
export function sourceLink(assetId: string, projectId: string | undefined, at?: number, terms?: string[], panel?: string): string {
  const q = new URLSearchParams();
  if (projectId) q.set('tab', 'sources');
  q.set('asset', assetId);
  if (at !== undefined) q.set('t', at.toFixed(3));
  if (terms?.length) q.set('hl', terms.slice(0, 6).join('|'));
  if (panel) q.set('panel', panel);
  return projectId ? `/p/${projectId}?${q.toString()}` : `/?${q.toString()}`;
}

/** Plain words for what an on-screen result matched. */
const seenAs: Record<string, string> = { label: 'Seen', text: 'Text on screen', description: 'Scene' };

/**
 * The search palette (/): server-side search across what was said in every
 * transcript, video titles and filenames, projects, revisions and comments.
 */
export function SearchPalette({ onClose }: { onClose: () => void }) {
  const [q, setQ] = useState('');
  const [debounced, setDebounced] = useState('');
  const [active, setActive] = useState(0);
  const navigate = useNavigate();
  const listRef = useRef<HTMLUListElement>(null);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 200);
    return () => clearTimeout(t);
  }, [q]);
  const search = useOp('search', debounced ? { query: debounced, limit: 8 } : null, { staleTime: 10_000, placeholderData: (prev) => prev });
  const projects = useOp('project.list', {}, { enabled: !debounced });

  const hits: Hit[] = useMemo(() => {
    if (!debounced) {
      return (projects.data?.projects ?? []).slice(0, 6).map((p) => ({
        key: `p-${p.id}`,
        group: 'projects' as const,
        title: p.name,
        sub: `${p.assetCount} sources, updated ${relative(p.updatedAt)}`,
        thumb: p.thumbAssetId ? media.asset(p.thumbAssetId, 'thumbnail') : undefined,
        icon: <FilmSlate size={14} />,
        to: `/p/${p.id}`,
        label: p.name,
      }));
    }
    const d = search.data;
    if (!d) return [];
    const out: Hit[] = [];
    for (const m of d.moments)
      out.push({
        key: `m-${m.segmentId}`,
        group: 'moments',
        title: <Snippet parts={m.snippet} />,
        sub: (
          <>
            <span className="tc text-accent-text">{timecode(m.at, { ms: false })}</span> {m.assetTitle}
            {m.speakerName ? `, ${m.speakerName}` : ''}
            {m.projectName ? `, ${m.projectName}` : `, ${m.filed ?? 'the Inbox'}`}
          </>
        ),
        thumb: media.asset(m.assetId, 'thumbnail'),
        icon: <ChatText size={14} />,
        to: sourceLink(m.assetId, m.projectId, m.at, m.terms),
        label: `${m.assetTitle} at ${timecode(m.at, { ms: false })}: ${m.text}`,
      });
    for (const v of d.visuals ?? [])
      out.push({
        key: `s-${v.assetId}-${v.matchedIn}-${v.from}`,
        group: 'visuals',
        title: <Snippet parts={v.snippet} />,
        sub: (
          <>
            <span className="tc text-accent-text">{timecode(v.from, { ms: false })}</span> {seenAs[v.matchedIn] ?? 'Seen'} in {v.assetTitle}
            {v.projectName ? `, ${v.projectName}` : `, ${v.filed ?? 'the Inbox'}`}
          </>
        ),
        thumb: media.frame(v.assetId, v.at, 160),
        icon: <Eye size={14} />,
        to: sourceLink(v.assetId, v.projectId, v.at, undefined, 'onscreen'),
        label: `${seenAs[v.matchedIn] ?? 'Seen'}: ${v.text}, ${v.assetTitle} at ${timecode(v.at, { ms: false })}`,
      });
    for (const v of d.videos)
      out.push({
        key: `v-${v.assetId}`,
        group: 'videos',
        // A person's name: the clip's title, then who speaks in it.
        title: v.matchedIn === 'speaker' ? v.title : <Snippet parts={v.snippet} />,
        sub:
          v.matchedIn === 'speaker' ? (
            <>
              <Snippet parts={v.snippet} />, {duration(v.duration)}, {v.projectName ?? v.filed ?? 'the Inbox'}
            </>
          ) : (
            `${v.matchedIn === 'title' ? v.name : v.title}, ${duration(v.duration)}, ${v.projectName ?? v.filed ?? 'the Inbox'}`
          ),
        thumb: v.hasThumb ? media.asset(v.assetId, 'thumbnail') : undefined,
        icon: <VideoCamera size={14} />,
        to: sourceLink(v.assetId, v.projectId),
        label: v.title,
      });
    for (const r of d.resources ?? [])
      out.push({
        key: `res-${r.resourceId}`,
        group: 'resources',
        title: r.matchedIn === 'title' ? <Snippet parts={r.snippet} /> : r.title,
        sub: (
          <>
            {r.matchedIn !== 'title' ? (
              <>
                <Snippet parts={r.snippet} />,{' '}
              </>
            ) : null}
            {r.from ? `from ${r.from}` : r.projectName ?? 'the workspace library'}
          </>
        ),
        thumb: r.thumbUrl,
        icon: <ImageSquare size={14} />,
        to: r.projectId ? `/p/${r.projectId}?tab=resources&resource=${r.resourceId}` : '/settings/brand',
        label: r.title,
      });
    for (const p of d.projects)
      out.push({
        key: `p-${p.projectId}`,
        group: 'projects',
        title: p.matchedIn === 'name' ? <Snippet parts={p.snippet} /> : p.name,
        sub: p.matchedIn === 'name' ? 'Project' : <Snippet parts={p.snippet} />,
        thumb: p.thumbAssetId ? media.asset(p.thumbAssetId, 'thumbnail') : undefined,
        icon: <FolderSimple size={14} />,
        to: `/p/${p.projectId}`,
        label: p.name,
      });
    for (const r of d.revisions)
      out.push({
        key: `r-${r.revisionId}`,
        group: 'revisions',
        title: <Snippet parts={r.snippet} />,
        sub: `Version ${r.number} of ${r.projectName ?? 'a project'}, matched its ${matchedWord[r.matchedIn] ?? r.matchedIn}`,
        icon: <GitBranch size={14} />,
        to: `/p/${r.projectId}/r/${r.revisionId}`,
        label: `Version ${r.number} ${r.projectName ?? ''}`,
      });
    for (const c of d.comments)
      out.push({
        key: `c-${c.commentId}`,
        group: 'comments',
        title: <Snippet parts={c.snippet} />,
        sub: `${c.author ?? 'Comment'}, ${c.projectName ?? 'project'}${c.at != null ? `, at ${timecode(c.at, { ms: false })}` : ''}`,
        icon: <ChatCenteredText size={14} />,
        to: c.revisionId ? `/p/${c.projectId}/r/${c.revisionId}` : c.assetId ? sourceLink(c.assetId, c.projectId, c.at ?? undefined) : `/p/${c.projectId}`,
        label: 'Comment',
      });
    return out;
  }, [debounced, search.data, projects.data]);

  useEffect(() => setActive(0), [debounced]);
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const go = (h: Hit | undefined) => {
    if (!h) return;
    onClose();
    navigate(h.to);
  };
  const loading = debounced ? search.isFetching : projects.isLoading;

  return (
    <ModalFrame top size="lg" ariaLabel="Search" onCancel={onClose}>
      <div className="-m-1">
        <div className="flex items-center gap-2.5 rounded-[12px] bg-panel px-3 hairline">
          <MagnifyingGlass size={17} className="text-subtle" />
          <input
            data-autofocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') {
                e.preventDefault();
                setActive((i) => Math.min(hits.length - 1, i + 1));
              } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                setActive((i) => Math.max(0, i - 1));
              } else if (e.key === 'Enter') {
                e.preventDefault();
                go(hits[active]);
              }
            }}
            placeholder="What was said or seen, people, video titles, projects…"
            aria-label="Search what was said, what's on screen, people, videos, projects, versions and comments"
            role="combobox"
            aria-expanded
            aria-controls="mr-search-results"
            aria-activedescendant={hits[active] ? `mr-sr-${active}` : undefined}
            className="h-12 min-w-0 flex-1 bg-transparent text-[15px] text-fg outline-none placeholder:text-subtle"
          />
          {loading ? <Spinner size={14} className="text-subtle" label="Searching" /> : null}
        </div>
        <ul id="mr-search-results" ref={listRef} role="listbox" className="mt-3 max-h-[58dvh] overflow-y-auto">
          {hits.length === 0 ? (
            <li className="px-3 py-10 text-center">
              {loading ? (
                <span className="text-[13px] text-muted">Searching…</span>
              ) : debounced ? (
                <>
                  <div className="text-[13.5px] font-medium text-fg">Nothing matches “{debounced}”</div>
                  <div className="mx-auto mt-1 max-w-[44ch] text-[12.5px] leading-relaxed text-muted">
                    Search looks through what was said in your footage (whole words and the start of words), what is on screen (things, animals, places and text, found on a Mac), the names of people you named, video titles and filenames, projects, versions and comments.
                  </div>
                </>
              ) : (
                <>
                  <div className="text-[13.5px] font-medium text-fg">Search everything</div>
                  <div className="mx-auto mt-1 max-w-[44ch] text-[12.5px] leading-relaxed text-muted">Type a word someone said to jump to that moment, something you can see (a dog, a beach, a sign's words), someone's name to find the videos they speak in, or a title, filename, project or note.</div>
                </>
              )}
            </li>
          ) : null}
          {hits.map((h, i) => {
            const first = i === 0 || hits[i - 1]?.group !== h.group;
            const group = GROUPS.find((g) => g.id === h.group);
            return (
              <li key={h.key} role="presentation">
                {first ? <div className="px-3 pb-1.5 pt-3 text-[11.5px] font-medium text-subtle">{debounced ? group?.label : 'Recent projects'}</div> : null}
                <div
                  id={`mr-sr-${i}`}
                  role="option"
                  aria-selected={i === active}
                  aria-label={h.label}
                  data-index={i}
                  onMouseMove={() => setActive(i)}
                  onClick={() => go(h)}
                  className={cx('flex cursor-pointer items-center gap-3 rounded-[10px] px-3 py-2', i === active ? 'bg-hover' : '')}
                >
                  {h.group === 'revisions' || h.group === 'comments' ? (
                    <span className="grid h-9 w-14 shrink-0 place-items-center rounded-[7px] bg-inset text-[#a69d93]">{h.icon}</span>
                  ) : (
                    <Thumb src={h.thumb} alt="" className="h-9 w-14 shrink-0 rounded-[7px]" fallback={h.icon} />
                  )}
                  <div className="min-w-0 flex-1">
                    <div className={cx('text-[13.5px] text-fg', h.group === 'moments' || h.group === 'visuals' ? 'clamp-2 leading-snug' : 'truncate font-medium')}>{h.title}</div>
                    <div className="truncate text-[12px] text-subtle">{h.sub}</div>
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
        <div className="mt-3 flex items-center gap-3 border-t border-line px-1 pt-3 text-[11.5px] text-subtle">
          <span className="inline-flex items-center gap-1">
            <Kbd>↑</Kbd>
            <Kbd>↓</Kbd> move
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>↵</Kbd> open
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>esc</Kbd> close
          </span>
          {search.data && debounced ? <span className="ml-auto">{plural(search.data.total, 'result')}</span> : null}
        </div>
      </div>
    </ModalFrame>
  );
}
