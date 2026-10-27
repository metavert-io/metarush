# Runbook: add (and revoke) a machine

Every machine in a workspace gets **its own object-store key** and, on Atlas, **its own database user**, so a lost
laptop can be cut off without touching anyone else. Three commands do it:

| Where | Command | What it does |
|---|---|---|
| admin machine | `metarush machine invite <name> [--atlas]` | mints the machine's key (and DB user), proves it works, writes a 0600 join bundle |
| new machine | `metarush machine join <bundle.json>` | `init --join` with the bundle (secrets to the keychain), then doctor checks |
| admin machine | `metarush machine revoke <name> [--atlas]` | deletes the key (and DB user), marks the installation revoked, audits it |

`metarush machine list [--keys]` shows every installation, its key label (never a secret), state and last seen.

## 1. Invite (admin machine)
The admin machine is one whose storage key may mint keys: on Tigris, the key that owns the bucket (the one
`fly storage create` printed) or an organization admin key. To use a different admin key for minting than the one
the installation stores media with, set `METARUSH_STORAGE_ADMIN_ACCESS_KEY_ID` / `METARUSH_STORAGE_ADMIN_SECRET_ACCESS_KEY`.

```bash
metarush machine invite studio-mini --atlas          # --atlas needs `atlas auth login` (add --atlas-profile <p> if not the default)
```
It creates:
- **Storage key `metarush-studio-mini`** with scope `no_default_allow` (it cannot list or create buckets, create
  keys or policies), limited by default to **objects under the workspace's store prefix** (an IAM policy on
  `arn:aws:s3:::<bucket>/<prefix>/*` plus listing in-progress uploads on the bucket). `--key-scope bucket` grants a
  bucket role instead (`--role ReadWrite`, the default, or `Editor`). The key is proven with a write / ranged read /
  delete round trip before anything is recorded.
- **Database user `metarush-studio-mini`** (with `--atlas`): `readWrite` on this installation's database only,
  limited to the cluster the installation uses. The password goes to the Atlas CLI on stdin (never in argv) and only
  into the bundle. Without `--atlas`, or if the Atlas CLI is not logged in, the command prints the exact
  `atlas dbusers create …` command instead.
- **An installation record** in state `invited`, and an audit event.
- **The join bundle** `./metarush-join-studio-mini.json` (or `--out`), mode 0600, never overwritten. It contains the
  workspace, database name and host, the database URI with the new user's password (only when a user was created),
  and the media store settings with the machine's own key. Secrets are never printed.

If anything fails part-way, everything invite created (key, policy, database user, file) is removed.

Atlas network access is not changed. If the new machine's IP is not covered, add it yourself:
`atlas accessLists create <ip> --type ipAddress --comment metarush-studio-mini --projectId <project>`.

## 2. Move the bundle
Use a secure channel (AirDrop, `scp`, a password manager's secure note). Do not email or chat it.

## 3. Join (new machine)
```bash
./bootstrap.sh && export PATH="$PWD/bin:$PATH"
metarush machine join metarush-join-studio-mini.json
rm metarush-join-studio-mini.json                    # once the checks pass (join deletes nothing)
metarush skills install --user && metarush dashboard
```
- If the bundle has no database credentials, set `METARUSH_MONGODB_URI` to this machine's own user first (join
  prompts for it in an interactive terminal).
- A brand-new Atlas user can take a minute to activate; join waits up to three minutes.
- `--secrets env` keeps secrets out of the keychain: join writes the database address and the storage secret into
  this machine's `<home>/.env` (0600; never inside a sync folder, and inside a repository only when git ignores it),
  where MetaRush in the background and the Claude Code panel find them. Exporting them in a shell isn't enough: the
  panel never gets MetaRush's secrets from the environment, and `metarush doctor` warns about a secret that is only
  there.
- Join reuses the installation ID from the invite, sets it `active` and records which key it uses. A revoked
  installation's bundle is refused.

## 4. Check
```bash
metarush machine list --keys      # on the admin machine: state, key label, key exists?, DB user, last seen
metarush doctor                   # on the new machine: "this installation … with its own storage key"
```
`machine list` warns about machines that share a key (they cannot be revoked alone), revoked machines whose key
still exists, and invites older than a week that were never joined. `--keys` also lists `metarush-*` keys no
installation uses (e.g. the CI key).

## 5. Revoke (lost or retired machine)
```bash
metarush machine revoke studio-mini --atlas
```
Deletes the key and its policy (access stops immediately; the provider is checked afterwards), with `--atlas`
deletes the database user, and marks the installation `revoked` (`init` and `doctor` then refuse it). It refuses to
revoke the installation you are running, or a key that this or another live machine also uses, unless `--force`.
Revoking twice is safe. Treat a lost device's local cache as exposed (it holds copies of media it worked on).

Only the key that minted a key (or an organization admin key) can delete it on Tigris, so revoke from the admin
machine that invited it, or set the admin-key environment variables above.

## 6. Re-key a machine that joined before per-machine keys
Machines set up before `machine invite` existed share one storage key, so none of them can be revoked alone
(`machine list` says so on each). On each such machine, in a terminal:
```bash
metarush machine rekey --dry-run      # what it would do: the key it has, who shares it, the new key's scope; changes nothing
metarush machine rekey                # type "rekey" to go ahead
```
It mints `metarush-<this machine>` limited to the store prefix (as invite does), round-trips it, keeps its secret
where the machine keeps secrets (a new keychain entry, or `<home>/.env`), switches `config.yaml`, and checks
MetaRush works with it as it loads its settings; if anything fails, everything is put back and the new key is
deleted. MetaRush running in the background restarts itself to use the new key. The minting needs a key that may
make keys: the machine's current (shared) key if it owns the bucket, else the admin-key variables above.

Then:
- The command names the machines still on the old key: run it on each of them.
- The last one is told no machine uses the old key any more. Delete it yourself in the Tigris console (Access keys)
  once nothing outside MetaRush uses it either (a CI job, a script). MetaRush never deletes it.
- A re-keyed machine's key can't make keys: to invite or revoke from it later, set
  `METARUSH_STORAGE_ADMIN_ACCESS_KEY_ID` / `METARUSH_STORAGE_ADMIN_SECRET_ACCESS_KEY` to a key that may, for that command.

Providers without a key API: make the key by hand (`--dry-run` lists the steps), then
`read -s METARUSH_MACHINE_S3_SECRET_ACCESS_KEY && export METARUSH_MACHINE_S3_SECRET_ACCESS_KEY` and
`metarush machine rekey --access-key-id <key id>`. Environment variables that set the storage key
(`METARUSH_S3_ACCESS_KEY_ID`, or a `METARUSH_S3_SECRET_ACCESS_KEY` from your shell rather than MetaRush's `.env`) would
win over the new key, so the command refuses until they are removed.

## Providers without a key API
Tigris keys are minted automatically. For R2, B2, AWS S3, GCS (S3 interoperability) and custom endpoints,
`machine invite` stops with `E_NOT_CONFIGURED` and prints the provider's exact steps (console clicks or CLI
commands, e.g. `b2 key create --bucket … --name-prefix …`). Create the key, then:
```bash
read -s METARUSH_MACHINE_S3_SECRET_ACCESS_KEY && export METARUSH_MACHINE_S3_SECRET_ACCESS_KEY
metarush machine invite studio-mini --access-key-id <key id>
```
`machine revoke` then marks the machine revoked and tells you which key to delete in the provider's console.
A shared filesystem store has no keys: give the machine its own account on the file server.

## How Tigris key minting works
MetaRush calls Tigris's IAM API (`https://iam.storageapi.dev`, override with `METARUSH_TIGRIS_IAM_ENDPOINT`;
`https://iam.storage.dev` and `https://fly.iam.storage.tigris.dev` answer too), SigV4-signed (service `s3`, region
`auto`) with the admin key — the same requests the official Tigris CLI makes:

| Action | Used for |
|---|---|
| `CreateAccessKeyWithBucketsRole` (`Req` = `{name, buckets_role, access_key_scope}`) | mint a named key with bucket roles and scope |
| `ListAccessKeys` (`/?Detailed`, optional `KeyId`) | key name, roles, scope, existence |
| `DeleteAccessKey` | revoke |
| `CreatePolicy`, `AttachUserPolicy`, `ListUserPolicies`, `ListPolicies`, `ForceDeletePolicy` | prefix-scoped keys |

Verified against a real account (2026-10-17): the key `fly storage create` returned behaves as a standard
(non-admin) key that owns its bucket. It can mint keys with roles on that bucket, create and attach policies naming
that bucket, and list and delete the keys it created (it sees no other keys, not even itself). A minted `no_default_allow` key gets `403` on
`ListBuckets` and on any prefix outside its policy; a deleted key fails immediately with `InvalidAccessKeyId`.

**Prefix scoping:** Tigris bucket roles (`ReadOnly`, `ReadWrite`, `Editor`) are bucket-wide. A key *can* be limited
to a prefix by giving it no bucket role and attaching an IAM policy on `bucket/prefix/*` — MetaRush's default when
the store has a prefix, and how the CI key is built. Policy conditions are limited to IP and date, so `s3:ListBucket`
cannot be narrowed to a prefix; a prefix-scoped key can therefore list the bucket's object names (content hashes)
but cannot read, write or delete outside its prefix. `Editor` additionally allows bucket settings and bucket
deletion, so MetaRush never uses it by default.

## CI key (live-bucket test)
```bash
metarush machine ci-key [--replace]                          # key metarush-ci, limited to <bucket>/ci-test/*
gh secret set -f metarush-ci-s3.env --repo <owner>/<repo> && rm metarush-ci-s3.env
```
The file (0600) holds `METARUSH_TEST_S3_{BUCKET,ENDPOINT,REGION,ACCESS_KEY_ID,SECRET_ACCESS_KEY}`. CI runs
`TestS3Live` with them (it writes under `ci-test/<timestamp>/` and cleans up); forks have no secrets and skip it.
`--replace` deletes older `metarush-ci` keys and their policies once the new key works — rotate by running it and
loading the new file.

## Troubleshooting
| Symptom | Fix |
|---|---|
| `could not mint a key … HTTP 403` | the installation's key cannot mint keys: run invite on the admin machine or set the admin-key variables |
| `refusing to revoke … shares its storage key` | give the other machines their own keys first, or `--force` |
| join: `could not connect with the bundle's database user` | wait a minute (new Atlas users), or add the machine's IP to the Atlas access list |
| join: `installation … was revoked` | ask for a new invite |
