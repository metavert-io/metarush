import { useState } from 'react';
import { ArrowSquareOut, CheckCircle, CircleNotch, XCircle, YoutubeLogo } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call } from '../api/client';
import { useOp } from '../api/queries';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Progress, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * Posting a version to YouTube (D305): what would be sent and what holds it
 * (publish.plan), then a person posts it (publish.send) as their own channel.
 * Claude, Auto and AI apps never post; the server refuses them too.
 */

const PRIVACY: { id: 'private' | 'unlisted' | 'public'; label: string; says: string }[] = [
  { id: 'private', label: 'Private', says: 'Only you, until you change it on YouTube' },
  { id: 'unlisted', label: 'Unlisted', says: 'Anyone with the link' },
  { id: 'public', label: 'Public', says: 'Everyone' },
];

export function PostToYouTube({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const [open, setOpen] = useState(false);
  const [privacy, setPrivacy] = useState<'private' | 'unlisted' | 'public'>('private');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<unknown>();
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  // While a post goes out (from this tab, another, or a terminal), look
  // again every two seconds: how much has gone, and when it is done.
  const plan = useOp('publish.plan', open ? { projectId, revision: revisionId, platform: 'youtube', privacy } : null, {
    refetchInterval: (q) => (q.state.data?.sending ? 2000 : false),
  });
  const p = plan.data;
  const going = p?.sending;

  const post = async () => {
    if (!p) return;
    // Already on the channel: a second video only when the person says so.
    const again = !!p.posted;
    const ok = await dialog.confirm(
      again
        ? {
            title: `Post version ${number} to YouTube again?`,
            body: (
              <>
                It is already on <Theirs>“{p.posted?.channel}”</Theirs>. Posting it again makes a second video there, {privacy}.
              </>
            ),
            confirmLabel: 'Post it again',
          }
        : {
            title: `Post version ${number} to YouTube?`,
            body: (
              <>
                As <Theirs>“{p.as?.channel}”</Theirs>, {privacy}. It goes out with the title, description and captions shown.
              </>
            ),
            confirmLabel: 'Post it',
          },
    );
    if (!ok) return;
    setSending(true);
    setError(undefined);
    void qc.invalidateQueries({ queryKey: ['publish.plan'] }); // shows it going out
    try {
      const out = await call('publish.send', { projectId, revision: revisionId, platform: 'youtube', privacy, fingerprint: p.fingerprint, again });
      toast({ tone: 'success', title: 'Posted to YouTube', body: out.publication.notes?.[0] ?? `Version ${number} is on “${out.publication.channel}”.`, durationMs: 10000 });
      void qc.invalidateQueries({ queryKey: ['publish.plan'] });
      void qc.invalidateQueries({ queryKey: ['project.receipt'] });
    } catch (e) {
      setError(e);
      void qc.invalidateQueries({ queryKey: ['publish.plan'] });
    } finally {
      setSending(false);
    }
  };

  return (
    <section className="mt-6 rounded-[16px] bg-panel p-4 hairline" aria-label="Post to YouTube" data-testid="post-to-youtube">
      <div className="flex items-center gap-3">
        <YoutubeLogo size={20} weight="fill" className="shrink-0 text-muted" />
        <div className="min-w-0 flex-1">
          <h3 className="text-[14px] font-medium">Post to YouTube</h3>
          <p className="text-[12.5px] text-muted">As your own channel, from this computer. You do it yourself, every time.</p>
        </div>
        <Button size="sm" variant={open ? 'ghost' : 'secondary'} onClick={() => setOpen((o) => !o)}>
          {open ? 'Close' : 'Look at what would be posted'}
        </Button>
      </div>
      {open ? (
        <div className="mt-4 flex flex-col gap-3 text-[13px]">
          {plan.isLoading ? <Skeleton className="h-24" /> : null}
          {plan.error ? <ErrorPanel error={plan.error} compact onRetry={() => void plan.refetch()} /> : null}
          {p ? (
            <>
              <p className={cx(p.ready || going ? 'text-fg' : 'text-muted')}>{p.summary}</p>
              {going ? <Sending x={going} /> : null}
              {p.holds.some((h) => !h.sending) ? (
                <ul className="flex flex-col gap-1.5">
                  {p.holds.filter((h) => !h.sending).map((h, i) => (
                    <li key={i} className="flex items-start gap-2">
                      <XCircle size={15} className="mt-0.5 shrink-0 text-danger" />
                      <span>
                        {h.says}
                        {h.command ? (
                          <details className="mt-0.5 text-[12px] text-muted">
                            <summary className="cursor-pointer">Details</summary>
                            <code>{h.command}</code>
                          </details>
                        ) : null}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : going ? null : (
                <>
                  <div className="grid gap-2 sm:grid-cols-[120px_1fr]">
                    <span className="text-muted">Title</span>
                    <Theirs className="font-medium">{p.title}</Theirs>
                    <span className="text-muted">Description</span>
                    <Theirs className="whitespace-pre-wrap">{p.description || '—'}</Theirs>
                    <span className="text-muted">Captions</span>
                    <span>{p.captions.length ? p.captions.map((c) => c.name).join(', ') : 'None'}</span>
                    <span className="text-muted">Posted as</span>
                    <Theirs>{p.as?.channel}</Theirs>
                  </div>
                  <fieldset className="flex flex-wrap gap-2" aria-label="Who can see it">
                    {PRIVACY.map((o) => (
                      <label key={o.id} className={cx('flex cursor-pointer items-center gap-2 rounded-[10px] px-3 py-1.5 hairline', privacy === o.id ? 'bg-accent-soft' : '')}>
                        <input type="radio" name="privacy" value={o.id} checked={privacy === o.id} onChange={() => setPrivacy(o.id)} />
                        <span className="font-medium">{o.label}</span>
                        <span className="text-[12px] text-muted">{o.says}</span>
                      </label>
                    ))}
                  </fieldset>
                </>
              )}
              {p.notes.length ? (
                <ul className="list-disc pl-5 text-[12.5px] text-muted">
                  {p.notes.map((n, i) => (
                    <li key={i}>{n}</li>
                  ))}
                </ul>
              ) : null}
              {error ? <ErrorPanel error={error} compact /> : null}
              {p.ready ? (
                <div className="flex items-center justify-end gap-2">
                  {sending ? <span className="mr-auto text-[12.5px] text-muted">Sending… it carries on if you leave this page.</span> : null}
                  <Button variant="primary" loading={sending} disabled={sending} onClick={post} data-testid="post-to-youtube-send">
                    {p.posted ? 'Post it again' : 'Post to YouTube'}
                  </Button>
                </div>
              ) : null}
              {p.earlier.length ? (
                <div>
                  <h4 className="text-[12.5px] font-medium text-muted">Posted before</h4>
                  <ul className="mt-1 flex flex-col gap-1">
                    {p.earlier.map((x) => (
                      <li key={x.id} className="flex items-center gap-2">
                        {x.state === 'posted' ? (
                          <CheckCircle size={14} weight="fill" className="text-ok" />
                        ) : x.state === 'sending' ? (
                          <CircleNotch size={14} className="animate-spin text-muted" aria-label="Sending" />
                        ) : (
                          <XCircle size={14} className="text-danger" />
                        )}
                        <Badge>{x.privacy}</Badge>
                        <Theirs>{x.channel}</Theirs>
                        <span className="text-muted">{new Date(x.at).toLocaleString()}</span>
                        {x.url ? (
                          <a href={x.url} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 text-accent-text hover:underline">
                            Open <ArrowSquareOut size={12} />
                          </a>
                        ) : x.state === 'sending' ? (
                          <span className="text-muted">Sending, {percent(x)}%</span>
                        ) : x.error ? (
                          <span className="text-muted">{x.error}</span>
                        ) : null}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

function percent(x: { sent?: number; size?: number }) {
  return x.size ? Math.min(100, Math.floor(((x.sent ?? 0) * 100) / x.size)) : 0;
}

/** A post going out: how much has gone. */
function Sending({ x }: { x: { channel: string; sent?: number; size?: number } }) {
  const pct = percent(x);
  return (
    <div className="flex flex-col gap-1.5" data-testid="post-to-youtube-sending">
      <div className="flex items-center gap-2">
        <CircleNotch size={15} className="animate-spin text-muted" />
        <span>
          Sending to <Theirs>“{x.channel}”</Theirs>: {pct}%. It carries on if you leave this page.
        </span>
      </div>
      <Progress value={pct / 100} label="Sent" />
    </div>
  );
}
