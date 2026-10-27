import { useCallback, useEffect, useMemo } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { Archive, CaretRight, PencilSimple, Sparkle, TextAa, Trash } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../api/queries';
import type { ProjectShowOut } from '../api/types';
import { plural, relative } from '../lib/format';
import { updateSelection, resetSelection, useProvideDropTarget } from '../lib/workbench';
import { ProjectForm } from '../components/Library';
import { RenderButtons } from '../components/Review';
import { TodoBadge } from '../components/TodoBadge';
import { panelParam } from '../components/SourceInspector';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, EmptyState, ErrorPanel, Freshness, Skeleton, Tabs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { CutTab } from './studio/CutTab';
import { SourcesTab } from './studio/SourcesTab';
import { RendersTab } from './studio/RendersTab';
import { HistoryTab } from './studio/HistoryTab';
import { CutSheetTab } from './studio/CutSheetTab';
import { VoiceoverTab } from './studio/VoiceoverTab';
import { SoundTab } from './studio/SoundTab';
import { ResourcesTab } from './studio/ResourcesTab';
import { ConversationTab } from './studio/ConversationTab';
import { originSummary } from '../components/ProjectLinks';
import { PendingFixesBanner } from '../components/transcripts/PendingFixes';
import { ProjectMemory } from '../components/project/ProjectMemory';
import { ContentSettings } from '../components/project/ContentSettings';
import { ReceiptTab } from '../components/project/Receipt';
import { ARCHIVED_PROJECTS, ArchivedProject, useProjectActions } from '../components/project/ProjectDelete';
import { MenuButton } from '../layout/assistant/Menu';
import { MadeWith } from '../components/recipes/MadeWith';
import { useSaveAsMenuItem } from '../components/recipes/SaveDialog';

export type StudioTab = 'cut' | 'conversation' | 'voiceover' | 'sound' | 'script' | 'sources' | 'resources' | 'renders' | 'history' | 'receipt' | 'settings';

const TABS: StudioTab[] = ['cut', 'conversation', 'voiceover', 'sound', 'script', 'sources', 'resources', 'renders', 'history', 'receipt', 'settings'];
/** Older names of tabs, still accepted in links people saved ("Cut sheet" is now "Script"; "What it keeps" is now "Settings", D193). */
const TAB_ALIASES: Record<string, StudioTab> = {
  sheet: 'script',
  cutsheet: 'script',
  'cut-sheet': 'script',
  memory: 'settings',
  keeps: 'settings',
  rules: 'settings',
  signoffs: 'settings',
  brand: 'settings',
  spelling: 'settings',
};

export function tabParam(v: string | null): StudioTab | null {
  if (!v) return null;
  const t = TAB_ALIASES[v] ?? v;
  return (TABS as string[]).includes(t) ? (t as StudioTab) : null;
}

export function ProjectStudio() {
  const { projectId = '' } = useParams();
  const [params, setParams] = useSearchParams();
  const show = useOp('project.show', { id: projectId });
  const resources = useOp('resources.list', projectId ? { projectId } : null, { staleTime: 30_000 });
  // The Conversation tab shows once the project has a conversation (or a link asks for it).
  const convs = useOp('conversation.list', projectId ? { projectId } : null, { staleTime: 30_000 });
  const data = show.data;
  const head = data?.headRevisionId;
  // An older link to a part (?tab=spelling, ?tab=brand) opens that part of the Settings tab.
  const rawTab = params.get('tab');
  const part = params.get('part') ?? (rawTab === 'spelling' || rawTab === 'brand' ? rawTab : undefined);
  // The mixer used to be the Voiceover tab's Sound panel: links to it open the Sound tab.
  const asked = tabParam(params.get('tab'));
  const tab: StudioTab = (asked === 'voiceover' && params.get('mode') === 'sound' ? 'sound' : asked) ?? (data && !head ? 'sources' : 'cut');

  useProvideDropTarget(projectId, data?.project.name);
  useEffect(() => {
    resetSelection({ projectId });
  }, [projectId]);
  useEffect(() => {
    if (!data) return;
    const headRev = data.revisions.find((r) => r.id === head);
    updateSelection({ projectId, projectName: data.project.name, headRevisionId: head, headRevisionNumber: headRev?.number });
  }, [data, head, projectId]);

  const setTab = useCallback(
    (t: StudioTab, extra?: Record<string, string>) => {
      const next = new URLSearchParams();
      next.set('tab', t);
      for (const [k, v] of Object.entries(extra ?? {})) next.set(k, v);
      setParams(next, { replace: !extra });
    },
    [setParams],
  );

  const openSource = useCallback((assetId: string, t: number) => setTab('sources', { asset: assetId, t: t.toFixed(3) }), [setTab]);

  if (show.isLoading) return <StudioSkeleton />;
  if (show.error || !data)
    return (
      <div className="mx-auto max-w-[640px] py-10">
        <ErrorPanel error={show.error} onRetry={() => void show.refetch()} title={show.error?.code === 'E_NOT_FOUND' ? 'Project not found' : undefined} />
        <Link to="/" className="mt-4 inline-block text-[13px] text-accent-text hover:underline">
          Back to the studio
        </Link>
      </div>
    );
  // A deleted project (D196): links and notifications land on a page that says so.
  if (data.project.archived) return <ArchivedProject project={data.project} />;

  return (
    <div>
      <StudioHeader data={data} updatedAt={show.dataUpdatedAt} fetching={show.isFetching} onRefresh={() => void show.refetch()} />
      <div data-studio-tabs className="sticky top-[var(--mr-header-h)] z-30 -mx-4 mb-6 border-b border-line bg-[color-mix(in_oklab,var(--bg)_88%,transparent)] px-4 py-2 backdrop-blur-xl sm:-mx-6 sm:px-6 lg:-mx-8 lg:px-8">
        <Tabs<StudioTab>
          label="Project sections"
          value={tab}
          onChange={(t) => setTab(t)}
          items={[
            { value: 'cut', label: 'Edit' }, // the URL value stays cut so saved links work (D188)
            ...((convs.data?.conversations.length ?? 0) > 0 || tab === 'conversation'
              ? [{ value: 'conversation' as const, label: 'Conversation', count: (convs.data?.conversations.length ?? 0) > 1 ? convs.data?.conversations.length : undefined }]
              : []),
            { value: 'voiceover', label: 'Voiceover' },
            { value: 'sound', label: 'Sound' },
            { value: 'script', label: 'Script' },
            { value: 'sources', label: 'Sources', count: data.assets.length },
            { value: 'resources', label: 'Resources', count: resources.data?.resources.length },
            { value: 'renders', label: 'Renders', count: data.renders.length },
            { value: 'history', label: 'Versions', count: data.revisions.length },
            { value: 'receipt', label: 'Receipt' }, // what went to outside AI services and Claude, and the cost at cost (D240)
            { value: 'settings', label: 'Settings' }, // "What it keeps" before D193 (?tab=memory still opens it)
          ]}
        />
      </div>
      {tab === 'cut' ? (
        head ? (
          <CutTab data={data} revisionId={head} onOpenSource={openSource} />
        ) : (
          <NoCutYet data={data} onSources={() => setTab('sources')} />
        )
      ) : tab === 'conversation' ? (
        <ConversationTab data={data} revisionId={head} />
      ) : tab === 'voiceover' ? (
        head ? (
          <VoiceoverTab
            data={data}
            revisionId={head}
            mode={params.get('mode') ?? undefined}
            clipId={params.get('clip') ?? undefined}
            fit={params.get('fit') ?? undefined}
            onSettled={() => setTab('voiceover')}
          />
        ) : (
          <NoCutYet data={data} onSources={() => setTab('sources')} />
        )
      ) : tab === 'sound' ? (
        head ? (
          <SoundTab data={data} revisionId={head} onSettled={() => params.get('mode') && setTab('sound')} />
        ) : (
          <NoCutYet data={data} onSources={() => setTab('sources')} />
        )
      ) : tab === 'script' ? (
        <CutSheetTab data={data} />
      ) : tab === 'sources' ? (
        <SourcesTab
          data={data}
          assetId={params.get('asset') ?? undefined}
          t={params.get('t') ? Number(params.get('t')) : undefined}
          highlight={params.get('hl') ? params.get('hl')!.split('|') : undefined}
          panel={panelParam(params.get('panel'))}
          onSelect={(id) => setTab('sources', id ? { asset: id } : {})}
        />
      ) : tab === 'resources' ? (
        <ResourcesTab data={data} resourceId={params.get('resource') ?? undefined} onSelect={(id) => setTab('resources', id ? { resource: id } : {})} />
      ) : tab === 'renders' ? (
        <RendersTab data={data} highlight={params.get('render') ?? undefined} qaReport={params.get('qa') ?? undefined} />
      ) : tab === 'receipt' ? (
        <ReceiptTab projectId={data.project.id} revisionId={head} />
      ) : tab === 'settings' ? (
        <div className="space-y-10" data-project-settings>
          <ContentSettings projectId={data.project.id} part={part} />
          <ProjectMemory data={data} />
        </div>
      ) : (
        <HistoryTab data={data} highlight={params.get('rev') ?? undefined} />
      )}
    </div>
  );
}

function StudioHeader({ data, updatedAt, fetching, onRefresh }: { data: ProjectShowOut; updatedAt: number; fetching: boolean; onRefresh: () => void }) {
  const p = data.project;
  const dialog = useDialog();
  const toast = useToast();
  const update = useOpMutation('project.update', { invalidate: ['project.show', 'project.list'] });
  const head = data.revisions.find((r) => r.id === data.headRevisionId);
  const facts = [p.brief.tone, p.brief.audience, p.brief.targetSeconds ? `${p.brief.targetSeconds}s target` : undefined, ...(p.brief.platforms ?? [])].filter(Boolean) as string[];

  const edit = () =>
    void dialog.open<true>(
      (close) => (
        <ProjectForm
          initial={{ name: p.name, brief: p.brief }}
          submitLabel="Save brief"
          busy={update.isPending}
          error={update.error ? update.error.message : null}
          onCancel={() => close()}
          onSubmit={(v) =>
            update.mutate(
              { id: p.id, name: v.name, brief: v.brief },
              {
                onSuccess: () => {
                  toast({ tone: 'success', title: 'Brief saved' });
                  close(true);
                },
              },
            )
          }
        />
      ),
      { title: 'Edit project', size: 'md' },
    );

  // Deleting a project archives it (D196): it waits in Archived projects.
  const actions = useProjectActions();
  const navigate = useNavigate();
  // A video that worked, written down to make again (D201): the proposal opens from here.
  const saveAs = useSaveAsMenuItem({ id: p.id, name: p.name }, !!data.headRevisionId);
  const remove = async () => {
    if (await actions.deleteProject(p)) navigate('/');
  };

  return (
    <header className="mb-5">
      <nav aria-label="Breadcrumb" className="mb-3 flex items-center gap-1.5 text-[12.5px] text-subtle">
        <Link to="/" className="hover:text-fg">
          Studio
        </Link>
        <CaretRight size={11} />
        <span className="truncate text-muted">{p.name}</span>
        <Freshness className="ml-auto" at={updatedAt || undefined} fetching={fetching} onRefresh={onRefresh} />
      </nav>
      <div className="flex flex-col gap-4 lg:flex-row lg:items-end">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="min-w-0 break-words text-[26px] font-semibold leading-tight tracking-[-0.03em] text-fg sm:text-[32px]">{p.name}</h1>
            <TodoBadge projectId={p.id} />
          </div>
          {p.brief.goal ? <p className="mt-1.5 max-w-[70ch] text-[14.5px] leading-relaxed text-muted">{p.brief.goal}</p> : null}
          <MadeWith projectId={p.id} cut={head?.branch} className="mt-1.5" />
          <div className="mt-3 flex flex-wrap items-center gap-1.5">
            {facts.map((f) => (
              <Badge key={f}>{f}</Badge>
            ))}
            <span className="text-[12px] text-subtle">
              {originSummary(data.assets, 'source')}, {plural(data.revisions.length, 'version')}
              {head ? `, latest r${head.number} ${relative(head.createdAt)}` : ''}
              {data.openJobs ? `, ${plural(data.openJobs, 'job')} running` : ''}
            </span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" size="md" icon={<PencilSimple size={15} />} onClick={edit}>
            Brief
          </Button>
          <Link
            to={`/p/${p.id}?tab=settings&part=spelling`}
            className="inline-flex h-9 items-center gap-1.5 rounded-[10px] px-3 text-[13px] font-medium text-fg hover:bg-hover"
            data-spelling-open
          >
            <TextAa size={15} /> Spelling
          </Link>
          <RenderButtons revisionId={data.headRevisionId} brief={p.brief} output={head?.output} />
          <MenuButton
            label={`More for ${p.name}`}
            items={[
              saveAs,
              'separator',
              { label: 'Archived projects', icon: <Archive size={14} />, onSelect: () => navigate(ARCHIVED_PROJECTS) },
              'separator',
              { label: 'Delete project…', icon: <Trash size={14} />, danger: true, hint: 'It moves to Archived projects', onSelect: () => void remove() },
            ]}
          />
        </div>
      </div>
      {data.headRevisionId ? <PendingFixesBanner projectId={p.id} className="mt-4" /> : null}
    </header>
  );
}

function NoCutYet({ data, onSources }: { data: ProjectShowOut; onSources: () => void }) {
  const ready = useMemo(() => data.assets.filter((a) => a.variants.includes('proxy') || a.kind === 'audio').length, [data.assets]);
  return (
    <div className="panel">
      <EmptyState
        icon={<Sparkle size={22} />}
        title="No cut yet"
        body={
          data.assets.length === 0
            ? 'Drop footage onto this page to add it, then ask Claude Code (top right) for a first cut. It reads what was said and what MetaRush noticed in the footage, and suggests a first version for you to review here.'
            : `${plural(ready, 'source')} ready. Ask Claude Code (top right) for a first cut: it suggests a first version based on what was said and what MetaRush noticed, and it shows up here as soon as it is saved.`
        }
        action={
          <Button variant="secondary" onClick={onSources}>
            Browse sources
          </Button>
        }
      />
    </div>
  );
}

function StudioSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading project">
      <Skeleton className="h-4 w-40" />
      <Skeleton className="mt-4 h-9 w-[min(420px,80%)]" />
      <Skeleton className="mt-3 h-4 w-[min(560px,90%)]" />
      <Skeleton className="mt-6 h-9 w-[360px] max-w-full" />
      <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-[420px_1fr]">
        <Skeleton className="aspect-[9/16] w-full max-w-[420px] rounded-[14px]" />
        <div className="space-y-3">
          <Skeleton className="h-28 rounded-[14px]" />
          <Skeleton className="h-40 rounded-[14px]" />
        </div>
      </div>
    </div>
  );
}
