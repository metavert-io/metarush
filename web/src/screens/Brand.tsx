import { useEffect, useRef, useState } from 'react';
import { MagicWand, Swatches, TextAa, UploadSimple, X } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import { RESOURCE_QUERIES } from '../api/resources';
import type { BrandCutOffer, BrandLogoOut, BrandShowOut, OpInput, ResourceRow } from '../api/types';
import { uploads } from '../api/uploads';
import { ResourceGrid, ResourcePicture } from '../components/Resources';
import { SiteBrandSuggestion } from '../components/ResourceSources';
import { useQueryClient } from '@tanstack/react-query';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, SectionTitle, Skeleton } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * The brand (D82, D165), a part of a project's Content Settings (D193): five
 * named logos (D198: main, for dark backgrounds, for light backgrounds, a
 * square icon, the wordmark — each uploaded right here), colours (taken from
 * the main logo, editable) and a font. New cuts of every project using these
 * settings start with it, and the built-in title card, lower thirds, end card,
 * name bar and slides show the logo made for their background. For a project
 * with a website snapshot, "From its website" offers the site's logo, colours
 * and font in one step. Saved versions never change: after a change the
 * latest cuts that could use the new brand are offered as a checked change
 * each. A project that uses another project's settings shows the brand
 * read-only. Every control here is one op — `content.logo.set|remove`,
 * `brand.set`, `brand.apply` — the same as `metarush content logo` and
 * `metarush brand set|apply`.
 */

type LogoWhich = 'main' | 'dark' | 'light' | 'icon' | 'wordmark';

/** How each named logo is shown: on the background it is made for, in its shape. */
const BOTH = 'linear-gradient(135deg, #f7f5f0 0 50%, #0b0b0e 50% 100%)';
const LOGO_LOOK: Record<LogoWhich, { bg: string; aspect: string; hint: string }> = {
  main: { bg: BOTH, aspect: 'aspect-[16/7]', hint: 'Shown wherever no other logo fits' },
  dark: { bg: '#0b0b0e', aspect: 'aspect-[16/7]', hint: 'On dark cards and bars (usually light or white)' },
  light: { bg: '#f7f5f0', aspect: 'aspect-[16/7]', hint: 'On light cards and bars (usually dark)' },
  icon: { bg: BOTH, aspect: 'aspect-square max-w-[150px]', hint: 'Square, for small places' },
  wordmark: { bg: BOTH, aspect: 'aspect-[16/5]', hint: 'The name written out' },
};

const FONTS = ['Inter', 'Fraunces', 'JetBrains Mono'] as const;
const COLOR_FIELDS: { key: 'primary' | 'accent' | 'text' | 'background'; label: string; hint: string }[] = [
  { key: 'background', label: 'Background', hint: 'Cards and end screens' },
  { key: 'text', label: 'Text', hint: 'Titles and names' },
  { key: 'accent', label: 'Accent', hint: 'Rules, buttons, highlights' },
  { key: 'primary', label: 'Primary', hint: 'Soft tints' },
];

function useFontFamily(row?: ResourceRow): string | null {
  const [fam, setFam] = useState<string | null>(null);
  useEffect(() => {
    if (!row || row.kind !== 'font' || typeof FontFace === 'undefined') {
      setFam(null);
      return;
    }
    const name = `MR Brand ${row.current.slice(0, 12)}`;
    let live = true;
    new FontFace(name, `url("${row.fileUrl}")`)
      .load()
      .then((f) => {
        document.fonts.add(f);
        if (live) setFam(name);
      })
      .catch(() => live && setFam(null));
    return () => {
      live = false;
    };
  }, [row?.id, row?.current]); // eslint-disable-line react-hooks/exhaustive-deps
  return fam;
}

/** A colour white stands out on more than black does (WCAG contrast; Go: editspec.DarkBackground). */
export function isDark(hex: string): boolean {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return true;
  const v = parseInt(m[1]!, 16);
  const lin = (c: number) => (c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));
  const l = 0.2126 * lin(((v >> 16) & 255) / 255) + 0.7152 * lin(((v >> 8) & 255) / 255) + 0.0722 * lin((v & 255) / 255);
  return 1.05 / (l + 0.05) >= (l + 0.05) / 0.05;
}

/** Pictures (or fonts) a project can use, to pick one (the brand's logos and font, a Remotion project's pictures). */
export function LibraryPicker({ kind, projectId, close, empty }: { kind: 'picture' | 'font'; projectId?: string; close: (r?: ResourceRow) => void; empty?: string }) {
  const list = useOp('resources.list', { ...(kind === 'font' ? { kind: 'font' as const } : {}), ...(projectId ? { projectId } : {}) });
  const rows = (list.data?.resources ?? []).filter((r) => (kind === 'font' ? r.kind === 'font' : r.kind !== 'font' && r.kind !== 'repo'));
  if (list.isLoading) return <Skeleton className="h-40" />;
  if (list.error) return <ErrorPanel compact error={list.error} />;
  return (
    <div>
      <p className="mb-3 text-[13px] text-muted">
        {projectId
          ? "What this project can use: its own resources, the workspace library's and its linked projects'. They stay where they are."
          : "From the workspace library. A project's own logo can be used too: open it in the project's Resources and choose “Use as the brand logo”."}
      </p>
      <div className="max-h-[56dvh] overflow-y-auto">
        <ResourceGrid rows={rows} onOpen={(r) => close(r)} empty={empty ?? (kind === 'font' ? 'No fonts here yet: upload one.' : 'No pictures here yet: upload your logo.')} />
      </div>
    </div>
  );
}

/** The brand of a project's Content Settings (the Settings tab's Brand part). */
export function BrandEditor({ projectId }: { projectId: string }) {
  const q = useOp('brand.show', { projectId });
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const fileInput = useRef<HTMLInputElement>(null);
  const [uploadFor, setUploadFor] = useState<LogoWhich | 'fontResource'>('main');
  const [busy, setBusy] = useState<string | null>(null);
  const [siteId, setSiteId] = useState('');
  // After a change: the latest cuts that could use the new brand (saved versions never change).
  const [offers, setOffers] = useState<BrandCutOffer[]>([]);
  const data: BrandShowOut | undefined = q.data;
  const fontFamily = useFontFamily(data?.fontFile);
  const readOnly = !!data?.readOnly;

  useEffect(() => setSiteId(''), [projectId]);
  // The offer shows where it is seen: bring it into view when a change makes one.
  useEffect(() => {
    if (offers.length) document.querySelector(`[data-brand-offers]`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }, [offers.length]);

  const refresh = () => {
    for (const k of [...RESOURCE_QUERIES, 'brand.suggest', 'content.show']) void qc.invalidateQueries({ queryKey: [k] });
  };
  const set = async (key: string, input: OpInput<'brand.set'>, done?: string) => {
    setBusy(key);
    try {
      const out = await call('brand.set', { ...input, projectId });
      refresh();
      setOffers(out.latestCuts ?? []);
      toast({ tone: 'success', title: done ?? out.message });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "The brand kit didn't change", body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };

  // A named logo (D198): one op, the same as `metarush content logo set|remove`.
  const setLogo = async (which: LogoWhich, resource: string) => {
    setBusy(`logo:${which}`);
    try {
      const out = await call(resource ? 'content.logo.set' : 'content.logo.remove', resource ? { projectId, which, resource } : { projectId, which });
      refresh();
      setOffers(out.latestCuts ?? []);
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "The logo didn't change", body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };

  // An upload from here goes to the project's resources (versioned; SVGs
  // cleaned, a PDF's pages kept as pictures), then into the brand.
  useEffect(
    () =>
      uploads.onFinished((it) => {
        if (it.targetLabel !== 'the brand kit') return;
        const r = it.result?.resource;
        if (!r) return;
        const field = uploadFor;
        if (field === 'fontResource') void set(field, { fontResource: r.id }, `“${r.title}” is the brand font`);
        else void setLogo(field, r.id);
      }),
    [uploadFor, projectId], // eslint-disable-line react-hooks/exhaustive-deps
  );

  const upload = (field: LogoWhich | 'fontResource') => {
    setUploadFor(field);
    fileInput.current?.click();
  };
  const choose = async (field: LogoWhich | 'fontResource') => {
    const r = await dialog.open<ResourceRow>((close) => <LibraryPicker kind={field === 'fontResource' ? 'font' : 'picture'} projectId={projectId || undefined} close={close} />, {
      title: field === 'fontResource' ? 'Choose the brand font' : 'Choose the logo',
      size: 'lg',
    });
    if (!r) return;
    if (field === 'fontResource') void set(field, { fontResource: r.id }, `“${r.title}” is the brand font`);
    else void setLogo(field, r.id);
  };
  const putOnCut = async (id: string, name?: string) => {
    setBusy(`apply:${id}`);
    try {
      const out = await call('brand.apply', { projectId: id, instruction: 'Use the brand kit' });
      for (const k of ['project.show', 'revision.list', 'revision.show', 'cut.show']) void qc.invalidateQueries({ queryKey: [k] });
      const problems = out.problems.filter((x) => x.severity === 'error');
      if (problems.length) toast({ tone: 'error', title: 'Not applied', body: problems.map((x) => x.message).join(' ') });
      else {
        setOffers((o) => o.filter((x) => x.projectId !== id));
        toast({ tone: 'success', title: out.noChange ? `That cut already uses the brand` : name ? `${name}'s latest cut uses the new brand` : 'The cut uses the brand', body: out.summary });
      }
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Not applied', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };
  const applyTo = async () => {
    const ok = await dialog.confirm({
      title: "Put the brand on this project's latest cut?",
      body: "The cut's colours and font become the brand's, and its logo is ready to use by name (logo). A new version is saved; Undo is in its history.",
      confirmLabel: 'Save a new version',
    });
    if (ok) void putOnCut(projectId, data?.projectName);
  };

  if (q.isLoading) return <Skeleton className="h-96" />;
  if (q.error || !data) return <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />;
  const b = data.brand;
  const kit = data.kit;
  const sites = data.sites ?? [];
  const site = sites.find((s) => s.id === siteId) ?? sites[0];
  // The preview is a card on the brand's background: the logo made for it, else the main one.
  const darkBg = isDark(b.background ?? '#0B0B0E');
  const previewLogo = (darkBg ? data.logoDark : data.logoLight) ?? data.logo;

  const logoCard = (l: BrandLogoOut) => {
    const which = l.which as LogoWhich;
    const look = LOGO_LOOK[which];
    const row = l.resource;
    return (
      <div key={which} className="min-w-0 rounded-[16px] p-4 hairline" data-brand-logo={which} data-has-logo={row ? 'yes' : undefined}>
        <div className="mb-1 flex min-w-0 items-center gap-2">
          <h3 className="text-[13.5px] font-semibold text-fg">{l.label}</h3>
          {row ? <span className="tc min-w-0 truncate text-[11.5px] text-subtle">{row.name}</span> : null}
        </div>
        <p className="mb-3 text-[11.5px] text-subtle">{look.hint}</p>
        <div className={cx('relative grid place-items-center overflow-hidden rounded-[12px] hairline', look.aspect)} style={{ background: look.bg }}>
          {row ? (
            <ResourcePicture r={row} className="!absolute inset-4 !bg-none" />
          ) : (
            <span className="rounded-full bg-black/55 px-2.5 py-0.5 text-center text-[12px] text-white">No logo yet</span>
          )}
        </div>
        {readOnly ? null : (
          <div className="mt-3 flex flex-wrap gap-1.5">
            <Button size="sm" variant="secondary" icon={<UploadSimple size={13} />} onClick={() => upload(which)} data-logo-upload={which}>
              Upload…
            </Button>
            <Button size="sm" variant="ghost" onClick={() => void choose(which)}>
              From the resources…
            </Button>
            {row ? (
              <Button size="sm" variant="ghost" icon={<X size={12} />} loading={busy === `logo:${which}`} onClick={() => void setLogo(which, '')} aria-label={`Clear the ${l.label.toLowerCase()} logo`}>
                Clear
              </Button>
            ) : null}
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="space-y-8" data-brand-page data-read-only={readOnly || undefined}>
      {offers.length && !readOnly ? (
        <div className="rounded-[14px] bg-accent-soft/60 p-3.5" data-brand-offers>
          <p className="text-[13px] font-medium text-fg">Use the new brand on the latest cut</p>
          <p className="mt-0.5 text-[12px] text-muted">Saved versions never change. Each one below becomes a new version with the brand's colours, font and logos.</p>
          <ul className="mt-2 space-y-1.5">
            {offers.map((o) => (
              <li key={o.projectId} className="flex flex-wrap items-center gap-2 text-[12.5px]">
                <span className="min-w-0 flex-1 text-fg">
                  {o.projectName} <span className="text-subtle">· version {o.version}</span>
                </span>
                <Button size="sm" variant="secondary" loading={busy === `apply:${o.projectId}`} onClick={() => void putOnCut(o.projectId, o.projectName)} data-brand-offer={o.projectId}>
                  Use the new brand
                </Button>
              </li>
            ))}
          </ul>
          <button type="button" className="mt-2 text-[12px] text-muted hover:text-fg" onClick={() => setOffers([])}>
            Not now
          </button>
        </div>
      ) : null}
      <input
        ref={fileInput}
        type="file"
        accept={uploadFor === 'fontResource' ? '.ttf,.otf,.woff2,font/ttf,font/otf,font/woff2' : 'image/png,image/jpeg,image/webp,image/svg+xml,.svg,.heic,application/pdf,.pdf'}
        className="hidden"
        aria-hidden
        tabIndex={-1}
        data-testid="brand-upload"
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = '';
          if (!f) return;
          void uploads.add([{ file: f, path: f.name }], { label: 'the brand kit', projectId });
          toast({ title: `Uploading ${f.name}`, body: "It goes to this project's resources, then into the brand." });
        }}
      />

      {site && !readOnly ? (
        <section aria-labelledby="brand-suggested">
          <SectionTitle
            id="brand-suggested"
            action={
              sites.length > 1 ? (
                <select
                  value={site.id}
                  onChange={(e) => setSiteId(e.target.value)}
                  aria-label="Which website snapshot"
                  className="h-8 rounded-[9px] bg-panel px-2.5 text-[12.5px] text-fg hairline"
                >
                  {sites.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.site} · {s.title}
                    </option>
                  ))}
                </select>
              ) : undefined
            }
          >
            From its website
          </SectionTitle>
          <SiteBrandSuggestion from={site.id} projectId={projectId} />
        </section>
      ) : null}

      <section aria-labelledby="brand-logos">
        <SectionTitle id="brand-logos">Logos</SectionTitle>
        <p className="-mt-1 mb-3 max-w-[70ch] text-[12.5px] leading-relaxed text-muted">
          Cards, name titles, the name bar and slides show the logo made for their background — the one for dark backgrounds on a dark card, the one for light backgrounds on a light
          one — and the main logo when there is none. A PDF shows its first page.
        </p>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3" data-brand-logos>
          {(data.logos ?? []).map((l) => logoCard(l))}
        </div>
      </section>

      <section aria-labelledby="brand-colours">
        <SectionTitle
          id="brand-colours"
          action={
            data.logo && !readOnly ? (
              <Button size="sm" variant="ghost" icon={<MagicWand size={13} />} loading={busy === 'fromLogo'} onClick={() => void set('fromLogo', { fromLogo: true }, 'Colours taken from the logo')}>
                Take colours from the logo
              </Button>
            ) : undefined
          }
        >
          Colours
        </SectionTitle>
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          {COLOR_FIELDS.map((f) => {
            const value = (kit[f.key] as string | undefined) ?? (b[f.key] as string | undefined) ?? '#000000';
            return (
              <label key={f.key} className="block rounded-[14px] p-3 hairline">
                <span className="block text-[12.5px] font-medium text-fg">{f.label}</span>
                <span className="block text-[11.5px] text-subtle">{f.hint}</span>
                <span className="mt-2.5 flex items-center gap-2">
                  <input
                    type="color"
                    value={value}
                    disabled={readOnly}
                    aria-label={`${f.label} colour`}
                    onChange={(e) => void set(f.key, { [f.key]: e.target.value.toUpperCase() })}
                    className="h-9 w-12 cursor-pointer rounded-[8px] bg-transparent"
                  />
                  <span className="tc text-[12.5px] text-muted">{value.toUpperCase()}</span>
                </span>
              </label>
            );
          })}
        </div>
        {kit.suggested?.length ? (
          <div className="mt-3 flex flex-wrap items-center gap-2 text-[12px] text-subtle">
            <Swatches size={13} /> In the logo:
            {kit.suggested.map((c) => (
              <span key={c} className="inline-flex items-center gap-1">
                <span className="h-4 w-4 rounded-full ring-1 ring-line-strong" style={{ background: c }} />
                <span className="tc">{c}</span>
              </span>
            ))}
            {kit.colorsEdited ? <Badge>colours kept as chosen</Badge> : null}
          </div>
        ) : null}
      </section>

      <section aria-labelledby="brand-font">
        <SectionTitle id="brand-font">Font</SectionTitle>
        <div className="flex flex-wrap items-center gap-2">
          {FONTS.map((f) => (
            <button
              key={f}
              type="button"
              aria-pressed={!data.fontFile && (kit.font ?? b.font) === f}
              disabled={readOnly}
              onClick={() => void set('font', { font: f, fontResource: '' })}
              className={cx('h-10 rounded-[10px] px-4 text-[14px] transition-colors hairline', !data.fontFile && (kit.font ?? b.font) === f ? 'bg-accent-soft text-accent-text ring-1 ring-accent-line' : 'text-fg hover:bg-hover')}
              style={{ fontFamily: f === 'Inter' ? 'system-ui' : f === 'Fraunces' ? 'Georgia, serif' : 'ui-monospace, monospace' }}
            >
              {f}
            </button>
          ))}
          <span className="mx-1 text-subtle" aria-hidden>
            or
          </span>
          {data.fontFile ? (
            <Badge tone="accent" icon={<TextAa size={12} />}>
              {data.fontFile.title}
            </Badge>
          ) : null}
          {readOnly ? null : (
            <>
              <Button size="sm" variant="secondary" icon={<UploadSimple size={13} />} onClick={() => upload('fontResource')}>
                Upload a font…
              </Button>
              <Button size="sm" variant="ghost" onClick={() => void choose('fontResource')}>
                From the resources…
              </Button>
            </>
          )}
        </div>
      </section>

      <section aria-labelledby="brand-preview">
        <SectionTitle
          id="brand-preview"
          action={
            <Button size="sm" variant="primary" loading={busy === `apply:${projectId}`} onClick={() => void applyTo()}>
              Put it on this project's latest cut…
            </Button>
          }
        >
          Preview
        </SectionTitle>
        <div className={cx('grid aspect-video w-full max-w-[640px] place-items-center overflow-hidden rounded-[18px] hairline')} style={{ background: b.background ?? '#0B0B0E' }} data-testid="brand-preview">
          <div className="flex flex-col items-center gap-3 px-6 text-center">
            {previewLogo ? <ResourcePicture r={previewLogo} className="h-16 w-48 !bg-none" /> : null}
            <div className="text-[34px] font-semibold leading-tight tracking-[-0.02em]" style={{ color: b.text, fontFamily: fontFamily ? `"${fontFamily}", system-ui` : undefined }}>
              Your title here
            </div>
            <div className="h-[3px] w-12 rounded-full" style={{ background: b.accent }} />
            <div className="text-[15px] opacity-80" style={{ color: b.text, fontFamily: fontFamily ? `"${fontFamily}", system-ui` : undefined }}>
              A subtitle in your brand
            </div>
          </div>
        </div>
        <p className="mt-2 text-[12px] text-subtle">{data.message}</p>
      </section>
    </div>
  );
}
