import type { RemoteOp } from './types';

/*
 * The Library (D96–D98): the Inbox is a triage pile, and each clip leaves it
 * by being added to a project, put on a collection, archived or moved to the
 * Trash. These are the queries that change when something is filed away,
 * brought back or deleted for good.
 */
export const LIBRARY_QUERIES: RemoteOp[] = [
  'library.list',
  'collection.list',
  'collection.show',
  'trash.list',
  'trash.settings',
  'asset.list',
  'asset.show',
  'asset.where',
  'project.show',
  'project.list',
  'project.links',
  'search',
  'activity.summary',
  'todos.list',
  'todos.counts',
];

export type LibraryView = 'inbox' | 'collections' | 'archived' | 'projects' | 'trash' | 'all';

export const LIBRARY_VIEWS: { value: LibraryView; label: string }[] = [
  { value: 'inbox', label: 'Inbox' },
  { value: 'collections', label: 'Collections' },
  { value: 'archived', label: 'Archived' },
  { value: 'projects', label: 'In projects' },
  { value: 'trash', label: 'Trash' },
];

/** The Library page, at a view, a collection or with one clip open. */
export function libraryLink(o: { view?: LibraryView; collection?: string; asset?: string; q?: string } = {}): string {
  const p = new URLSearchParams();
  if (o.view && o.view !== 'inbox') p.set('view', o.view);
  if (o.collection) p.set('collection', o.collection);
  if (o.asset) p.set('asset', o.asset);
  if (o.q) p.set('q', o.q);
  const s = p.toString();
  return `/library${s ? `?${s}` : ''}`;
}
