import { useEffect, useRef, useState } from 'react';
import { DownloadSimple } from '@phosphor-icons/react';
import { filesFromDataTransfer, hasFiles, isSystemFile } from '../api/uploads';
import { plural } from '../lib/format';
import { getDropTarget, useDropTarget, useFilePickerRequests } from '../lib/workbench';
import { useToast } from '../ui/Toast';
import { findLinks, useAddLinks } from '../components/LinkImports';
import { addFiles } from '../components/MakeConversation';
import { splitTimelines, useTimelineImport } from '../components/TimelineImport';

/** A link being dragged in (not files, not MetaRush's own clip drags). */
function hasLink(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  const types = Array.from(dt.types ?? []);
  return types.includes('text/uri-list') && !types.includes('Files') && !types.includes('application/x-metarush-assets');
}

/**
 * Drop footage anywhere: a full-window target that routes files (and folders,
 * where the browser exposes them) to the open project, or the inbox.
 */
export function GlobalDrop() {
  const [over, setOver] = useState(false);
  const depth = useRef(0);
  const target = useDropTarget();
  const toast = useToast();
  const input = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const picks = useFilePickerRequests();
  const addLinks = useAddLinks();
  const openTimeline = useTimelineImport();
  const openTimelineRef = useRef(openTimeline);
  openTimelineRef.current = openTimeline;

  useEffect(() => {
    if (picks.nonce === 0) return;
    (picks.folder ? folderInput : input).current?.click();
  }, [picks]);

  useEffect(() => {
    // webkitdirectory is not in React's typed props; set it imperatively.
    folderInput.current?.setAttribute('webkitdirectory', '');
  }, []);

  const onPicked = (list: File[]) => {
    const picked = list
      .map((file) => ({ file, path: (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name }))
      .filter((f) => !isSystemFile(f.path));
    const t = getDropTarget();
    // A timeline from another editing app becomes a cut, not footage (D304).
    const { timelines, rest: files } = splitTimelines(picked);
    for (const x of timelines) void openTimeline(x.file, x.path, t);
    if (files.length === 0) return;
    void addFiles(files, t);
    toast({ title: `Uploading ${plural(files.length, 'file')}`, body: `Into ${t.label}.` });
  };

  useEffect(() => {
    // A link dragged in (from another window or the address bar) or pasted with
    // ⌘V anywhere outside a text field imports the video.
    const takes = (dt: DataTransfer | null) => hasFiles(dt) || hasLink(dt);
    const onPaste = (e: ClipboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.closest('[contenteditable="true"], .cm-editor'))) return;
      const links = findLinks(e.clipboardData?.getData('text/plain') ?? '');
      if (!links.length) return;
      e.preventDefault();
      const t = getDropTarget();
      void addLinks(links, { projectId: t.projectId, label: t.label });
    };
    const onEnter = (e: DragEvent) => {
      if (!takes(e.dataTransfer)) return;
      e.preventDefault();
      depth.current += 1;
      setOver(true);
    };
    const onOver = (e: DragEvent) => {
      if (!takes(e.dataTransfer)) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
    };
    const onLeave = (e: DragEvent) => {
      if (!takes(e.dataTransfer)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setOver(false);
    };
    const onDrop = async (e: DragEvent) => {
      if (!takes(e.dataTransfer)) return;
      e.preventDefault();
      depth.current = 0;
      setOver(false);
      const dt = e.dataTransfer;
      if (!dt) return;
      if (!hasFiles(dt)) {
        const links = findLinks(dt.getData('text/uri-list') || dt.getData('text/plain'));
        if (!links.length) {
          toast({ tone: 'error', title: 'Nothing to import', body: 'That drop had no link to a video.' });
          return;
        }
        const t = getDropTarget();
        void addLinks(links, { projectId: t.projectId, label: t.label });
        return;
      }
      const dropped = await filesFromDataTransfer(dt);
      const t = getDropTarget();
      const { timelines, rest: files } = splitTimelines(dropped);
      for (const x of timelines) void openTimelineRef.current(x.file, x.path ?? x.file.name, t);
      if (files.length === 0) {
        if (!timelines.length) toast({ tone: 'error', title: 'Nothing to upload', body: 'That drop had no files (empty folders and hidden files are skipped).' });
        return;
      }
      void addFiles(files, t);
      toast({ title: `Adding ${plural(files.length, 'file')} to ${t.label}`, body: 'Footage goes to Sources; pictures, logos, PDFs and fonts to Resources. You can keep working while it runs.' });
    };
    window.addEventListener('dragenter', onEnter);
    window.addEventListener('dragover', onOver);
    window.addEventListener('dragleave', onLeave);
    window.addEventListener('drop', onDrop);
    window.addEventListener('paste', onPaste);
    return () => {
      window.removeEventListener('dragenter', onEnter);
      window.removeEventListener('dragover', onOver);
      window.removeEventListener('dragleave', onLeave);
      window.removeEventListener('drop', onDrop);
      window.removeEventListener('paste', onPaste);
    };
  }, [toast, addLinks]);

  return (
    <>
      <input
        ref={input}
        type="file"
        multiple
        className="hidden"
        aria-hidden
        tabIndex={-1}
        onChange={(e) => {
          const list = Array.from(e.target.files ?? []);
          e.target.value = '';
          onPicked(list);
        }}
      />
      <input
        ref={folderInput}
        type="file"
        multiple
        className="hidden"
        aria-hidden
        tabIndex={-1}
        onChange={(e) => {
          const list = Array.from(e.target.files ?? []);
          e.target.value = '';
          onPicked(list);
        }}
      />
      {over ? (
        <div className="fade-in pointer-events-none fixed inset-0 z-[60] grid place-items-center bg-[var(--overlay)] p-6 backdrop-blur-[3px]">
          <div className="flex w-full max-w-[560px] flex-col items-center gap-3 rounded-[24px] border-2 border-dashed border-accent-line bg-raised/95 px-8 py-14 text-center shadow-[var(--shadow-float)]">
            <div className="grid h-14 w-14 place-items-center rounded-[16px] bg-accent text-accent-ink">
              <DownloadSimple size={26} weight="bold" />
            </div>
            <div className="text-[20px] font-semibold tracking-[-0.02em] text-fg" data-testid="drop-title">
              Drop to add to {target.projectId ? target.label : 'the Inbox'}
            </div>
            <div className="text-[13.5px] text-muted">
              Footage goes to {target.projectId ? 'its Sources' : 'the Inbox'}; pictures, logos, PDFs and fonts to {target.projectId ? 'its Resources' : 'the workspace library'}. Each file is stored once.
            </div>
          </div>
        </div>
      ) : null}
    </>
  );
}
