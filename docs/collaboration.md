# Collaboration

**Target setup:** several machines, each with its own MetaRush installation (CLI, dashboard, optional worker, local
cache), all pointed at the **same database and the same media store** and joined to the same workspace.

- MongoDB documents are authoritative for shared metadata. The media store is authoritative for bytes.
  Asset references are portable IDs; nothing shared contains a machine path.
- Local caches are copies, verified by SHA-256 and re-fetched (with resume) when missing. Offline *viewing* of pinned
  media works (`cache prefetch --project <id> --pin` fetches everything renders and reviews read; `sync status`
  shows what is local); offline *editing* isn't supported (there is no conflict-aware sync).
- Workers on any machine lease jobs from the shared queue. A Mac mini can run `metarush worker start` unattended;
  it is not special — the database and bucket remain canonical, so every laptop works while that machine is offline.
- Edits from two people: disjoint ops auto-rebase; overlapping changes return `E_REVISION_CONFLICT` with the item IDs;
  nothing is silently overwritten.

## Connecting
Every installation connects to MongoDB over TLS and to the bucket with its own key: a trusted team, directly. Use
per-installation database users scoped to the MetaRush database and per-machine bucket keys so a lost laptop can be
revoked. Restrict Atlas network access (IP access list or private networking) — never `0.0.0.0/0` with a shared
high-privilege user.

## Joining
On an admin machine: `metarush machine invite <name> --atlas` (mints the machine's own storage key and database
user, writes a 0600 join bundle). On the new machine: `./bootstrap.sh`, then `metarush machine join <bundle>`.
Revoke with `metarush machine revoke <name> --atlas`. Full procedure, provider notes and troubleshooting:
[runbook-add-machine.md](runbook-add-machine.md).

Manual alternative (any provider):
`METARUSH_MONGODB_URI=… METARUSH_S3_SECRET_ACCESS_KEY=… metarush init --join <ws_id> --media s3 --s3-preset tigris --s3-bucket … --s3-access-key-id …`.

## Tests
`tests/e2e` runs two installations (separate homes and caches) against one database and an emulated bucket: shared
projects and media, a worker that dies mid-job on one machine and is recovered by the other (the late completion is
rejected), and a render queued on one machine and rendered by the other while the first is offline (skipped where
Remotion is not installed, e.g. CI). CI also runs `TestS3Live` against a real bucket with a key limited to
`ci-test/` when the repository has the `METARUSH_TEST_S3_*` secrets, and `TestGCSLive` against a real Google Cloud
Storage bucket when it has `METARUSH_TEST_GCS_BUCKET` and `METARUSH_TEST_GCS_CREDENTIALS_JSON` (what to set up:
[testing.md](testing.md#the-live-storage-tests)).
