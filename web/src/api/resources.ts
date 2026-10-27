import type { ops } from '@metarush/schema';
import { request } from './client';
import type { FolderChooseIn, FolderChooseOut, LinkedFolderOut, RemoteOp } from './types';

/*
 * Resources (D79–D84) outside the op registry: the native folder picker that
 * links a folder (a LocalOnly op on this machine), changing a linked folder,
 * and the same-origin media addresses of resource files.
 */

/** Opens the folder picker on this computer, then links the folder (to the project, or the workspace library). */
export function chooseFolder(input: FolderChooseIn): Promise<FolderChooseOut> {
  return request<FolderChooseOut>('POST', '/api/resources/folders/choose', { json: input });
}

/** Changes a linked folder on this machine: upload policy, pause, resume, look again. */
export function setFolder(input: ops.LinkedFolderSetIn): Promise<LinkedFolderOut> {
  return request<LinkedFolderOut>('POST', '/api/resources/folders/set', { json: input });
}

export const resourceMedia = {
  /** A version's thumbnail (an SVG is its own). */
  thumb: (id: string, sha?: string) => `/media/resources/${encodeURIComponent(id)}/thumb${sha ? `?v=${sha.slice(0, 12)}` : ''}`,
  /** The file itself (download=1 saves it). */
  file: (id: string, opts: { sha?: string; page?: number; download?: boolean } = {}) => {
    const q = new URLSearchParams();
    if (opts.sha) q.set('v', opts.sha.slice(0, 12));
    if (opts.page) q.set('page', String(opts.page));
    if (opts.download) q.set('download', '1');
    const s = q.toString();
    return `/media/resources/${encodeURIComponent(id)}/file${s ? `?${s}` : ''}`;
  },
};

/** Queries to refresh after anything about resources, links or the brand kit changes. */
export const RESOURCE_QUERIES: RemoteOp[] = [
  'resources.list',
  'resources.show',
  'resources.where',
  'resources.folders',
  'brand.show',
  'brand.suggest',
  'project.show',
  'project.list',
  'project.links',
  'asset.list',
  'asset.where',
  'search',
];

/** Kinds of resource in plain words. */
export function resourceKindLabel(kind: string): string {
  switch (kind) {
    case 'svg':
      return 'SVG';
    case 'pdf':
      return 'PDF';
    case 'font':
      return 'Font';
    case 'repo':
      return 'Repository';
    case 'website':
      return 'Web page';
    default:
      return 'Picture';
  }
}
