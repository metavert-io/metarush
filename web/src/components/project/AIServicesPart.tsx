import { useEffect, useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AIRulesSetIn, RemoteOp } from '../../api/types';
import { Button, ErrorPanel, Skeleton } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * Which AI services a client's work may go to (D225): a Content Settings part
 * that follows like the brand. Nothing at all, never some providers, or a
 * model chosen for a kind of work; generated video asks it too. The same op as
 * `metarush ai rules set`.
 */

const QUERIES: RemoteOp[] = ['ai.rules', 'ai.models', 'content.show'];

export function AIServicesPart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const uid = useId();
  const rules = useOp('ai.rules', { projectId });
  const models = useOp('ai.models', { projectId });
  const qc = useQueryClient();
  const toast = useToast();
  const [none, setNone] = useState(false);
  const [forbid, setForbid] = useState<string[]>([]);
  // Kinds of work never made for this client: presenters (D277).
  const [noPresenters, setNoPresenters] = useState(false);
  const [pin, setPin] = useState<Record<string, string>>({});
  const [note, setNote] = useState('');
  const [saving, setSaving] = useState(false);
  const r = rules.data?.rules;
  useEffect(() => {
    if (!r) return;
    setNone(!!r.none);
    setForbid(r.forbid ?? []);
    setNoPresenters((r.forbidKinds ?? []).includes('presenter'));
    setPin(r.pin ?? {});
    setNote(r.note ?? '');
  }, [r]);
  if (rules.isLoading || models.isLoading) return <Skeleton className="h-16" />;
  if (rules.error) return <ErrorPanel compact error={rules.error} />;
  const providers = models.data?.providers ?? [];
  const kinds = (models.data?.kinds ?? []).filter((k) => k.kind !== 'video' && k.kind !== 'presenter-make' && !(noPresenters && k.kind === 'presenter'));
  const save = async () => {
    setSaving(true);
    try {
      const was = r?.pin ?? {};
      const input: AIRulesSetIn = {
        projectId,
        none,
        forbid,
        allow: providers.map((p) => p.name).filter((n) => !forbid.includes(n)),
        ...(noPresenters ? { forbidKinds: ['presenter'] } : { allowKinds: ['presenter'] }),
        pin: Object.fromEntries(Object.entries(pin).filter(([, v]) => v)),
        unpin: Object.keys(was).filter((k) => !pin[k]),
        note,
      };
      const out = await call('ai.rules.set', input);
      for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: 'Saved', body: out.summary });
    } catch (e) {
      const err = toApiError(e);
      toast({
        tone: 'error',
        title: 'That change was not saved',
        body: [err.message, err.hint].filter(Boolean).join(' '),
      });
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="space-y-3" data-ai-services-part>
      <p className="text-[12.5px] leading-relaxed text-muted">
        Which AI services the work of every project using these settings may go to: for a client whose contract keeps their footage away from them, or one model for all their
        music. Generated video follows them too. Budgets stay with each project.
      </p>
      <fieldset disabled={readOnly} className="space-y-3">
        <label className="flex items-start gap-2 text-[13px] text-fg">
          <input type="checkbox" checked={none} onChange={(e) => setNone(e.target.checked)} className="mt-[3px]" data-ai-none />
          <span>
            Nothing goes to any AI service
            <span className="block text-[12px] text-muted">Pictures, speech, music, dubs, cloud transcripts and generated video are all refused.</span>
          </span>
        </label>
        {!none ? (
          <div className="flex flex-wrap gap-x-5 gap-y-1">
            {providers.map((p) => (
              <label key={p.name} className="flex items-center gap-2 text-[13px] text-fg">
                <input
                  type="checkbox"
                  checked={!forbid.includes(p.name)}
                  onChange={(e) => setForbid((f) => (e.target.checked ? f.filter((x) => x !== p.name) : [...f, p.name]))}
                  data-ai-allow={p.name}
                />
                Use {p.title}
              </label>
            ))}
          </div>
        ) : null}
        {!none ? (
          <label className="flex items-start gap-2 text-[13px] text-fg">
            <input type="checkbox" checked={noPresenters} onChange={(e) => setNoPresenters(e.target.checked)} className="mt-[3px]" data-ai-no-presenters />
            <span>
              No presenters
              <span className="block text-[12px] text-muted">Nobody, a person’s own or a stock presenter, reads lines on camera for this client.</span>
            </span>
          </label>
        ) : null}
        {!none ? (
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {kinds.map((k) => (
              <label key={k.kind} className="block text-[12.5px] text-muted">
                {k.title}
                <select
                  value={pin[k.kind] ?? ''}
                  onChange={(e) => setPin((p) => ({ ...p, [k.kind]: e.target.value }))}
                  className="mt-1 h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent"
                  data-ai-pin={k.kind}
                >
                  <option value="">The usual model</option>
                  {k.models
                    .filter((m) => m.built && !m.retired && !forbid.includes(m.provider))
                    .map((m) => (
                      <option key={`${m.provider}/${m.id}`} value={`${m.provider}/${m.id}`}>
                        {m.title} ({m.providerTitle})
                      </option>
                    ))}
                </select>
              </label>
            ))}
          </div>
        ) : null}
        <div>
          <label htmlFor={`${uid}-note`} className="mb-1 block text-[12.5px] text-muted">
            Why (shown when something is refused)
          </label>
          <input
            id={`${uid}-note`}
            value={note}
            maxLength={500}
            onChange={(e) => setNote(e.target.value)}
            placeholder="The client's contract"
            className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent"
          />
        </div>
        {!readOnly ? (
          <Button size="sm" variant="secondary" loading={saving} onClick={() => void save()} data-ai-rules-save>
            Save
          </Button>
        ) : null}
      </fieldset>
    </div>
  );
}
