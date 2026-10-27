import { useEffect, useRef, useState } from 'react';
import { CloudArrowUp, Desktop, FolderOpen, Plus, Stack } from '@phosphor-icons/react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { toApiError } from '../api/client';
import { chooseFolder, RESOURCE_QUERIES } from '../api/resources';
import { isSystemFile, useUploads } from '../api/uploads';
import { plural } from '../lib/format';
import { useHost } from '../lib/host';
import { Button, cx, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { GenerateVideoButton } from './generate/GenerateVideoButton';
import { PasteLinkField } from './LinkImports';
import { addFiles, ConversationFromUploads } from './MakeConversation';
import { splitTimelines, useTimelineImport } from './TimelineImport';

/*
 * The one Add box (A): the same component on Home (into the Inbox), a
 * project's Sources tab and its Resources tab (into that project). Files go
 * where they belong — video and audio to Sources, pictures, logos, SVGs, PDFs
 * and fonts to Resources — and links through the same classifier as a paste
 * anywhere (videos are imported; GitHub repositories and web pages become
 * resources). In a project, "Choose a folder" links the folder on this computer
 * (kept in step: new and changed files come in by themselves); without the
 * native picker it uploads the folder's files once. Dropping anywhere on the
 * page does the same as the box (layout/GlobalDrop).
 */

export interface AddTarget {
  projectId?: string;
  /** "Test project", or "the Inbox". */
  label: string;
}

export function AddBox({
  target,
  variant = 'hero',
  focus = 'any',
  onFromProject,
  className,
}: {
  target: AddTarget;
  /** hero: Home's big box; empty: a project with nothing yet; compact: a bar above a list. */
  variant?: 'hero' | 'empty' | 'compact';
  /** What the words lead with (the box takes everything either way). */
  focus?: 'any' | 'footage' | 'resources';
  /** "From another project": use another project's footage, resources and cuts. */
  onFromProject?: () => void;
  className?: string;
}) {
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const host = useHost();
  const input = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const [linking, setLinking] = useState(false);
  // A project's Sources also take a Remotion project (D131): its folder or GitHub address.
  const remotion = focus === 'footage' && !!target.projectId;
  const uploading = useUploads().filter((u) => !['done', 'failed', 'canceled', 'skipped', 'aliased'].includes(u.state) && (u.projectId ?? '') === (target.projectId ?? '')).length;

  useEffect(() => {
    folderInput.current?.setAttribute('webkitdirectory', '');
  }, []);

  const openTimeline = useTimelineImport();
  const send = (list: File[], folder: boolean) => {
    const picked = list
      .map((file) => ({ file, path: (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name }))
      .filter((f) => !isSystemFile(f.path));
    // A timeline from another editing app becomes a cut, not footage (D304).
    const { timelines, rest: files } = splitTimelines(picked);
    for (const t of timelines) void openTimeline(t.file, t.path, target);
    if (files.length === 0) return;
    // Two or more files may be a call's recordings: the box offers a conversation once they are in.
    void addFiles(files, target);
    toast({
      title: `Adding ${plural(files.length, 'file')} to ${target.label}`,
      body: folder && target.projectId ? 'Once. To keep a folder in step, link it with Choose a folder.' : 'Footage goes to Sources; pictures, logos, PDFs and fonts to Resources. You can keep working while it runs.',
    });
  };

  const pickFolder = async () => {
    // From another computer a folder can't be linked (it would have to be on
    // the computer that runs MetaRush): its files are sent from this browser.
    if (!target.projectId || host.remote) {
      folderInput.current?.click();
      return;
    }
    setLinking(true);
    try {
      const out = await chooseFolder({ projectId: target.projectId });
      if (out.canceled || !out.linked) return;
      for (const k of RESOURCE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: `Linked ${out.linked.folder.name}`, body: out.linked.message, durationMs: 8000 });
    } catch (e) {
      const err = toApiError(e);
      if (err.code === 'E_NOT_CONFIGURED') folderInput.current?.click(); // no native picker here: send its files once
      else toast({ tone: 'error', title: "Couldn't link that folder", body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setLinking(false);
    }
  };

  const title =
    variant === 'hero'
      ? 'Drop footage here'
      : focus === 'resources'
        ? `Add logos, pictures and fonts to ${target.label}`
        : `Add to ${target.label}`;
  const body =
    focus === 'resources'
      ? 'Drop files anywhere on this page, or choose them: pictures, logos and SVGs, PDFs (each page becomes a picture) and fonts. Video goes to Sources.'
      : variant === 'hero'
        ? `Anywhere on any screen, files or links. Each file is stored once, then MetaRush makes a preview, a transcript and a scene analysis on ${host.remote ? host.machine : 'your computer'}. Pictures, logos and fonts go to Resources.`
        : 'Drop files anywhere on this page, or choose them. Footage gets a preview, a transcript and a scene analysis; pictures, logos, PDFs and fonts go to Resources.';

  const pickers = (
    <>
      <input
        ref={input}
        type="file"
        multiple
        className="hidden"
        aria-hidden
        tabIndex={-1}
        data-testid="addbox-files"
        onChange={(e) => {
          const list = Array.from(e.target.files ?? []);
          e.target.value = '';
          send(list, false);
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
          send(list, true);
        }}
      />
    </>
  );

  const buttons = (size: 'md' | 'lg', stacked: boolean) => (
    <div className={cx('flex flex-wrap gap-2', stacked && 'sm:flex-col sm:items-stretch')}>
      <Button size={size} variant="primary" icon={<Plus size={size === 'lg' ? 16 : 14} weight="bold" />} onClick={() => input.current?.click()}>
        Choose files
      </Button>
      {focus !== 'resources' ? <GenerateVideoButton target={target} size={size} /> : null}
      {focus !== 'resources' ? (
        <Button
          size={size}
          variant="secondary"
          icon={<Desktop size={size === 'lg' ? 16 : 14} />}
          onClick={() => navigate(target.projectId ? `/record?project=${target.projectId}` : '/record')}
          title="Record a window, a tab or your whole screen, with your camera and voice, while the teleprompter shows your script"
          data-record-screen
        >
          Record your screen
        </Button>
      ) : null}
      <Button
        size={size}
        variant="secondary"
        icon={<FolderOpen size={size === 'lg' ? 16 : 14} />}
        loading={linking}
        onClick={() => void pickFolder()}
        title={host.remote ? `Send a folder's files from this computer (linking a folder that stays in step works on ${host.machine} only)` : target.projectId ? 'Link a folder on this computer: its files come in and stay in step' : undefined}
      >
        Choose a folder
      </Button>
      {onFromProject ? (
        <Button size={size} variant="ghost" icon={<Stack size={size === 'lg' ? 16 : 14} />} onClick={onFromProject}>
          From another project
        </Button>
      ) : null}
    </div>
  );

  if (variant === 'compact')
    return (
      <div className={className}>
      <section aria-label={title} data-addbox className="flex flex-col gap-3 rounded-[16px] border border-dashed border-line-strong bg-raised px-4 py-3 sm:flex-row sm:items-center">
        {pickers}
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <div className="grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-accent-soft text-accent-text">
            <CloudArrowUp size={18} weight="bold" />
          </div>
          <div className="min-w-0">
            <div className="truncate text-[13.5px] font-medium text-fg">{title}</div>
            <div className="truncate text-[12px] text-subtle">Drop files or folders anywhere here, or paste a link with ⌘V.</div>
          </div>
          {uploading > 0 ? (
            <span className="ml-auto inline-flex shrink-0 items-center gap-1.5 rounded-full bg-accent-soft px-2.5 py-1 text-[12px] text-accent-text">
              <Spinner size={10} /> {plural(uploading, 'upload')}
            </span>
          ) : null}
        </div>
        <PasteLinkField compact remotion={remotion} className="w-full sm:w-[260px]" target={{ projectId: target.projectId, label: target.label }} />
        {buttons('md', false)}
      </section>
      <ConversationFromUploads projectId={target.projectId} />
      </div>
    );

  return (
    <section
      aria-label={title}
      data-addbox
      className={cx(
        'relative flex flex-col justify-center overflow-hidden rounded-[22px] border border-dashed border-line-strong bg-raised p-6 transition-colors sm:p-8',
        'bg-[radial-gradient(120%_140%_at_100%_0%,var(--accent-soft),transparent_55%)]',
        className,
      )}
    >
      {pickers}
      <div className="flex flex-col gap-6 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <div className="mb-4 grid h-12 w-12 place-items-center rounded-[14px] bg-accent text-accent-ink shadow-[inset_0_1px_0_rgb(255_255_255/0.3)]">
            <CloudArrowUp size={24} weight="bold" />
          </div>
          {variant === 'hero' ? (
            <h1 className="text-[26px] font-semibold leading-[1.1] tracking-[-0.03em] text-fg sm:text-[30px]">{title}</h1>
          ) : (
            <h2 className="text-[22px] font-semibold leading-[1.15] tracking-[-0.025em] text-fg sm:text-[24px]">{title}</h2>
          )}
          <p className="mt-2 max-w-[56ch] text-[14px] leading-relaxed text-muted">{body}</p>
        </div>
        {buttons('lg', true)}
      </div>
      <PasteLinkField
        className="mt-6 max-w-[640px]"
        remotion={remotion}
        target={{ projectId: target.projectId, label: target.label }}
        hint={
          remotion
            ? `Videos are downloaded ${host.onHere} and added to ${target.label}; a GitHub repository or a web page becomes one of its resources, and a Remotion project (its folder or GitHub address) makes videos you can use as clips. Where each came from stays with it.`
            : target.projectId
              ? `Videos are downloaded ${host.onHere} and added to ${target.label}; a GitHub repository or a web page becomes one of its resources. Where each came from stays with it.`
              : undefined
        }
      />
      {uploading > 0 ? (
        <div className="mt-5 inline-flex items-center gap-2 self-start rounded-full bg-accent-soft px-3 py-1 text-[12.5px] text-accent-text">
          <Spinner size={11} /> {plural(uploading, 'upload')} in progress
        </div>
      ) : null}
      <ConversationFromUploads projectId={target.projectId} />
    </section>
  );
}
