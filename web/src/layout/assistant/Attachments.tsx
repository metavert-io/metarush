import { useMemo, useState } from 'react';
import { FilePdf, X } from '@phosphor-icons/react';
import type { KeepFile } from '../../api/types';
import { bytes } from '../../lib/format';
import { Lightbox, type LightboxItem } from '../../ui/Lightbox';
import { cx, Spinner, Theirs } from '../../ui/primitives';

/*
 * Pictures and PDFs in the Claude Code panel (D166): the message box takes
 * them (the paperclip, a drop, a paste), each is stored by its content with
 * assistant.attach before the message goes, and the person's message shows
 * them afterwards. MetaRush then asks once whether to keep each in the project.
 */

export type AttachedFile = KeepFile;

/** What the message box takes, by type and by name (some systems send no type). */
const TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif', 'image/svg+xml', 'application/pdf'];
const EXTENSIONS = ['.png', '.jpg', '.jpeg', '.webp', '.gif', '.svg', '.pdf'];
export const ATTACH_ACCEPT = [...TYPES, ...EXTENSIONS].join(',');
/** At most this many files go with one message (the server takes 10). */
export const MAX_ATTACHMENTS = 10;
/** 20 MB each, what the server takes (assistant.attach; the request carries the file written out as text). */
export const MAX_ATTACH_BYTES = 20 * 1024 * 1024;

/** Why a file can't be attached, in plain words ("" when it can). */
export function attachProblem(f: File): string {
  const name = f.name || 'That file';
  const ext = name.includes('.') ? name.slice(name.lastIndexOf('.')).toLowerCase() : '';
  const known = TYPES.includes(f.type) || (!f.type && EXTENSIONS.includes(ext)) || (f.type === 'application/octet-stream' && EXTENSIONS.includes(ext));
  if (!known) return `“${name}” isn't a picture or a PDF. A message takes pictures (PNG, JPEG, WebP, GIF, SVG) and PDFs.`;
  if (f.size === 0) return `“${name}” is empty.`;
  if (f.size > MAX_ATTACH_BYTES) return `“${name}” is too large to attach here (${bytes(f.size)}; up to 20 MB).`;
  return '';
}

/** A file's bytes as base64 (what assistant.attach takes). */
export function readBase64(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onerror = () => reject(r.error ?? new Error('read failed'));
    r.onload = () => {
      const s = typeof r.result === 'string' ? r.result : '';
      const i = s.indexOf(',');
      resolve(i >= 0 ? s.slice(i + 1) : s);
    };
    r.readAsDataURL(f);
  });
}

/** Whether a drag carries files from the computer (not MetaRush's own drags). */
export function dragHasFiles(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  const types = Array.from(dt.types ?? []);
  return types.includes('Files') && !types.includes('application/x-metarush-assets');
}

/** Where the dashboard shows a file attached to a conversation's message (same origin). */
export function attachmentUrl(conversationId: string, sha256: string): string {
  return `/api/assistant/conversations/${encodeURIComponent(conversationId)}/attachments/${encodeURIComponent(sha256)}`;
}

export const isPicture = (mime: string) => mime.startsWith('image/');

/** The message's own words when the person sent only files. */
export function attachedPrompt(files: AttachedFile[]): string {
  if (files.length === 1) return `Here is “${files[0]!.name}”.`;
  return `Here are ${files.length} files: ${files.map((f) => `“${f.name}”`).join(', ')}.`;
}

/* ---------- In the message box: the files about to go ---------- */

export interface PendingAttachment {
  key: string;
  name: string;
  size: number;
  mime: string;
  /** A picture's preview from this computer (before it is sent). */
  preview?: string;
  state: 'uploading' | 'ready';
  file?: AttachedFile;
}

export function AttachmentChips({ items, onRemove }: { items: PendingAttachment[]; onRemove: (key: string) => void }) {
  if (!items.length) return null;
  return (
    <ul className="flex flex-wrap gap-1.5 px-2.5 pt-2.5" aria-label="Files to send with the message" data-attachments>
      {items.map((it) => (
        <li
          key={it.key}
          className="relative flex min-w-0 max-w-[230px] items-center gap-2 rounded-[10px] bg-raised py-1 pl-1 pr-1.5 hairline"
          data-attachment={it.state}
          aria-busy={it.state === 'uploading' || undefined}
        >
          <span className="relative grid h-9 w-9 shrink-0 place-items-center overflow-hidden rounded-[7px] bg-inset text-[#d9d1c6]">
            {it.preview ? <img src={it.preview} alt="" className="h-full w-full object-cover" draggable={false} /> : <FilePdf size={17} aria-hidden />}
            {it.state === 'uploading' ? (
              <span className="absolute inset-0 grid place-items-center bg-[rgb(0_0_0/0.45)] text-white">
                <Spinner size={12} />
              </span>
            ) : null}
          </span>
          <span className="min-w-0 flex-1">
            <Theirs className="block truncate text-[12px] font-medium text-fg" title={it.name}>
              {it.name}
            </Theirs>
            <span className="block text-[11px] text-subtle">{it.state === 'uploading' ? 'Attaching…' : bytes(it.size)}</span>
          </span>
          <button
            type="button"
            onClick={() => onRemove(it.key)}
            className="grid h-6 w-6 shrink-0 place-items-center rounded-[6px] text-subtle hover:bg-hover hover:text-fg"
            aria-label={`Remove “${it.name}”`}
            title="Remove"
          >
            <X size={12} />
          </button>
        </li>
      ))}
    </ul>
  );
}

/* ---------- In the thread: the files a message took ---------- */

export function MessageFiles({ conversationId, files, className }: { conversationId: string; files: AttachedFile[]; className?: string }) {
  const [open, setOpen] = useState<number | null>(null);
  const pictures = useMemo(() => files.filter((f) => isPicture(f.mime)), [files]);
  const others = useMemo(() => files.filter((f) => !isPicture(f.mime)), [files]);
  const items: LightboxItem[] = useMemo(() => pictures.map((f) => ({ src: attachmentUrl(conversationId, f.sha256), label: f.name })), [pictures, conversationId]);
  if (!files.length) return null;
  return (
    <>
      <ul className={cx('flex flex-wrap justify-end gap-1.5', className)} aria-label={files.length === 1 ? 'Attached file' : `${files.length} attached files`} data-message-files>
        {pictures.map((f, i) => (
          <li key={f.sha256}>
            <MessagePicture src={items[i]!.src} name={f.name} onOpen={() => setOpen(i)} />
          </li>
        ))}
        {others.map((f) => (
          <li key={f.sha256}>
            <a
              href={attachmentUrl(conversationId, f.sha256)}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-9 max-w-[220px] items-center gap-1.5 rounded-[9px] bg-panel px-2.5 text-[12px] text-fg hairline hover:bg-hover"
              title="Open the PDF"
              data-message-file="pdf"
            >
              <FilePdf size={15} className="shrink-0 text-subtle" aria-hidden />
              <Theirs className="min-w-0 truncate">{f.name}</Theirs>
              <span className="shrink-0 text-[11px] text-subtle">{bytes(f.size)}</span>
            </a>
          </li>
        ))}
      </ul>
      {open != null ? <Lightbox items={items} index={open} title="What you attached" onClose={() => setOpen(null)} /> : null}
    </>
  );
}

function MessagePicture({ src, name, onOpen }: { src: string; name: string; onOpen: () => void }) {
  const [state, setState] = useState<'loading' | 'ok' | 'gone'>('loading');
  return (
    <button
      type="button"
      onClick={onOpen}
      disabled={state === 'gone'}
      className="group relative grid h-16 w-16 place-items-center overflow-hidden rounded-[9px] bg-inset hairline focus-visible:outline-2 focus-visible:outline-accent"
      aria-label={`Open “${name}”, large`}
      title={state === 'gone' ? 'This picture is on the machine that holds the conversation' : 'Open large'}
      data-thumb
      data-message-file="picture"
    >
      {state !== 'gone' ? (
        <img
          src={src}
          alt=""
          loading="lazy"
          onLoad={() => setState('ok')}
          onError={() => setState('gone')}
          className={cx('h-full w-full object-cover transition-[opacity,transform] duration-300 group-hover:scale-[1.04]', state === 'ok' ? 'opacity-100' : 'opacity-0')}
        />
      ) : null}
      {state === 'loading' ? (
        <span className="absolute inset-0 grid place-items-center text-subtle">
          <Spinner size={12} />
        </span>
      ) : null}
    </button>
  );
}
