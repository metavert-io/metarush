import { useSyncExternalStore } from 'react';
import { ASSET_DRAG_TYPE } from '../api/uploads';

/*
 * Internal drags of library items onto projects. The payload travels as
 * ASSET_DRAG_TYPE (never "Files"), so the window-wide file drop zone ignores it.
 * A tiny store tells every drop target to show itself while a drag is on.
 */

let current: { ids: string[] } | null = null;
const subs = new Set<() => void>();
const set = (v: { ids: string[] } | null) => {
  current = v;
  subs.forEach((fn) => fn());
};

export function useAssetDrag(): { ids: string[] } | null {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    () => current,
  );
}

/** Starts an internal drag of `ids` with a drag image that says how many. */
export function startAssetDrag(e: React.DragEvent, ids: string[], label: string, thumb?: string) {
  e.dataTransfer.setData(ASSET_DRAG_TYPE, JSON.stringify(ids));
  e.dataTransfer.setData('text/plain', label);
  e.dataTransfer.effectAllowed = 'link';
  const ghost = document.createElement('div');
  ghost.style.cssText =
    'position:fixed;top:-200px;left:-200px;display:flex;align-items:center;gap:8px;padding:6px 12px 6px 6px;border-radius:999px;' +
    'background:var(--bg-raised);color:var(--fg);font:500 13px/1.2 var(--font-sans,system-ui);box-shadow:0 10px 30px -10px rgb(0 0 0/.5),inset 0 0 0 1px var(--line-strong);z-index:9999;';
  const pic = document.createElement('div');
  pic.style.cssText = `width:44px;height:26px;border-radius:7px;background:var(--bg-inset) center/cover no-repeat;${thumb ? `background-image:url("${thumb}");` : ''}`;
  const badge = document.createElement('span');
  badge.textContent = label;
  ghost.append(pic, badge);
  document.body.appendChild(ghost);
  try {
    e.dataTransfer.setDragImage(ghost, 22, 18);
  } catch {
    // some browsers refuse custom drag images; the default one is fine
  }
  setTimeout(() => ghost.remove(), 0);
  set({ ids });
}

export function endAssetDrag() {
  set(null);
}

/** The asset IDs of an internal drag (null for anything else, e.g. OS files). */
export function draggedAssetIds(dt: DataTransfer | null): string[] | null {
  if (!dt || !Array.from(dt.types ?? []).includes(ASSET_DRAG_TYPE)) return null;
  try {
    const v = JSON.parse(dt.getData(ASSET_DRAG_TYPE) || '[]') as unknown;
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export function isAssetDrag(dt: DataTransfer | null): boolean {
  return !!dt && Array.from(dt.types ?? []).includes(ASSET_DRAG_TYPE);
}
