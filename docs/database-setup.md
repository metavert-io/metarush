# Database setup

MetaRush needs one MongoDB database per team (a replica set: Atlas always is). It stores metadata only —
projects, assets, transcripts, revisions, jobs, renders, audit events — so a small tier goes a long way.

## Atlas
1. **Verify the organization first.** A connection string cannot prove which Atlas organization owns a cluster.
   ```bash
   atlas auth login --profile <name>      # browser sign-in with the right account
   atlas organizations list --profile <name>
   atlas projects list --orgId <org-id> --profile <name>
   atlas clusters list --projectId <project-id> --profile <name>
   ```
   Confirm the org ID and project ID belong to the organization you intend, then pass them to init
   (`--atlas-org-id --atlas-org-name --atlas-project-id`) so the installation records them for audits.
2. **Create a least-privilege user per installation** (Database Access → custom role or built-in `readWrite`
   scoped to the MetaRush database only — e.g. `metarush`). Revoke a machine by deleting its user.
3. **Network access**: add each machine's egress IP (or use private networking/VPN). Avoid `0.0.0.0/0`.
4. Run `METARUSH_MONGODB_URI='mongodb+srv://…' metarush init` (the URI goes to the keychain).

## Self-hosted / local
- Development and tests: `scripts/dev-mongo.sh start` (single-node replica set on 127.0.0.1:27018) or
  `docker compose -f docker-compose.dev.yml up -d`.
- Any reachable MongoDB ≥ 7 works; without a replica set, live updates fall back to polling.

## Migrations, backup, restore
- `metarush migrate` (also run automatically by `metarush serve`) applies additive, idempotent migrations and records
  them in `schema_migrations`. Jobs and revisions carry `schemaVersion` for future data migrations.
- Back up with Atlas backups or `mongodump --db <name>`; restore with `mongorestore`. Media is backed up separately:
  turn on bucket versioning or copy to a second bucket (e.g. `rclone sync`); a local cache is not a backup. Before upgrading across schema versions, take a backup.
