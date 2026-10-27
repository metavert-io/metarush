import type { TurnContext } from '../../api/types';
import { timecode } from '../../lib/format';
import type { Selection } from '../../lib/workbench';

/** A piece of the current selection the person can include or leave out. */
export interface ContextItem {
  key: 'project' | 'revision' | 'clip' | 'footage' | 'playhead';
  label: string;
  value: string;
  /** Always sent: a conversation's project is its own for good (D185). */
  fixed?: boolean;
}

/**
 * What a message would carry. A new conversation takes the page's selection;
 * a conversation keeps its project (D185), so its messages carry that project
 * wherever they are typed, and the page's selection only on its own page
 * (footage picked outside any project too). The server scopes it the same way.
 */
export function contextItems(sel: Selection, conv?: { projectId?: string; projectName?: string }): ContextItem[] {
  if (conv && (conv.projectId ?? '') !== (sel.projectId ?? '')) {
    const out: ContextItem[] = [];
    if (conv.projectId) out.push({ key: 'project', label: 'This conversation is about', value: conv.projectName ?? 'its project', fixed: true });
    if (sel.assetId && !sel.projectId) {
      const at = sel.time !== undefined && sel.timeBase === 'source' ? ` at ${timecode(sel.time)}` : '';
      out.push({ key: 'footage', label: 'Selected footage', value: `${sel.assetName ?? 'Footage'}${at}` });
    }
    return out;
  }
  const own = contextItemsOf(sel);
  return conv ? own.map((c) => (c.key === 'project' ? { ...c, fixed: true } : c)) : own;
}

/** What the page's selection would send with a message. */
function contextItemsOf(sel: Selection): ContextItem[] {
  const out: ContextItem[] = [];
  if (sel.projectId) {
    const head = sel.headRevisionNumber ? ` · version ${sel.headRevisionNumber}` : '';
    out.push({ key: 'project', label: 'Project', value: `${sel.projectName ?? 'Project'}${head}` });
    if (sel.revisionId && sel.revisionId !== sel.headRevisionId) out.push({ key: 'revision', label: 'Version in view', value: `version ${sel.revisionNumber ?? '?'}` });
    if (sel.clipId) out.push({ key: 'clip', label: 'Selected clip', value: sel.clipId });
  }
  if (sel.assetId) {
    const at = sel.time !== undefined && sel.timeBase === 'source' ? ` at ${timecode(sel.time)}` : '';
    out.push({ key: 'footage', label: 'Selected footage', value: `${sel.assetName ?? 'Footage'}${at}` });
  } else if (sel.projectId && sel.time !== undefined && sel.time > 0.05 && sel.timeBase === 'timeline') {
    out.push({ key: 'playhead', label: 'Playhead', value: timecode(sel.time) });
  }
  return out;
}

/** The quiet context line under the input ("Test project · version 3 · clip broll"). */
export function contextLine(items: ContextItem[], excluded: Set<string>): string {
  const projectOff = excluded.has('project') && !items.some((c) => c.key === 'project' && c.fixed);
  const on = items.filter((c) => (c.fixed || !excluded.has(c.key)) && !(c.key !== 'footage' && c.key !== 'project' && projectOff));
  return on.map((c) => (c.key === 'clip' ? `clip ${c.value}` : c.key === 'playhead' ? `at ${c.value}` : c.value)).join(' · ');
}

export interface MessageFields {
  prompt: string;
  projectId?: string;
  revisionId?: string;
  assetId?: string;
  clipId?: string;
  time?: number;
  timeBase?: 'source' | 'timeline';
}

/** The request fields: the prompt plus the IDs of the included items (names are resolved on the server). */
export function messageFields(sel: Selection, prompt: string, excluded: Set<string>): MessageFields {
  const input: MessageFields = { prompt: prompt.trim() };
  const has = (k: ContextItem['key']) => !excluded.has(k);
  if (sel.projectId && has('project')) {
    input.projectId = sel.projectId;
    if (sel.revisionId && sel.revisionId !== sel.headRevisionId && has('revision')) input.revisionId = sel.revisionId;
    if (sel.clipId && has('clip')) input.clipId = sel.clipId;
    if (!sel.assetId && sel.time !== undefined && sel.time > 0.05 && sel.timeBase === 'timeline' && has('playhead')) {
      input.time = sel.time;
      input.timeBase = 'timeline';
    }
  }
  if (sel.assetId && has('footage')) {
    input.assetId = sel.assetId;
    if (sel.time !== undefined && sel.timeBase === 'source') {
      input.time = sel.time;
      input.timeBase = 'source';
    }
  }
  return input;
}

/** What a sent message was about, from the server-resolved context. */
export function turnContextLine(c: TurnContext | undefined): string {
  if (!c) return '';
  const out: string[] = [];
  if (c.projectName) out.push(c.projectName + (c.headRevisionNumber ? ` · version ${c.headRevisionNumber}` : ''));
  if (c.revisionId && c.revisionId !== c.headRevisionId) out.push(`viewing version ${c.revisionNumber ?? '?'}`);
  if (c.clipId) out.push(`clip ${c.clipId}`);
  if (c.assetName) out.push(c.assetName + (c.time !== undefined && c.timeBase === 'source' ? ` at ${timecode(c.time)}` : ''));
  else if (c.time !== undefined && c.timeBase === 'timeline') out.push(`at ${timecode(c.time)}`);
  return out.join(' · ');
}
