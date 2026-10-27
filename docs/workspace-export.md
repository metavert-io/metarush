# Exporting and restoring a workspace

An export is one archive of everything about the work in a workspace: projects, versions, transcripts, comments,
people and their consent records, Claude Code conversations, the activity log — and, when you include it, the footage
itself. Use it for a backup, to move a workspace to another computer or another service, or to leave MetaRush with
everything you made. A restore puts an archive into an **empty** database (and its footage into a storage), checks it
against the archive, and brings it up to date for the MetaRush doing the restore.

Exporting and restoring are an owner's, and a person's: never Auto, Claude (in the dashboard or in a terminal), an
app connected through MCP, or a draft on the review desk.

## What an archive is

A folder (or, with `--tar`, one `.tar` file of the same folder):

| File | What it holds |
|---|---|
| `manifest.json` | the archive's format, the MetaRush that made it, when, on which computer and for whom; the workspace's ID and name; **the database's migration level** (every migration it had applied); for each collection its count, how it was made (options and indexes, which a restore only compares with MetaRush's own) and the SHA-256 of its file; what was left out and why; notes |
| `collections/<name>.jsonl` | one file per collection: every document of the workspace in canonical MongoDB Extended JSON v2, one per line, in `_id` order — exact to the byte (numbers, dates and binary data keep their types) |
| `media.jsonl` | every file of the workspace's footage, renders, pictures and long log texts, by key and size (and SHA-256) |
| `media/` | with included footage only: each of those files, stored by its content address and checked as it was copied |

Export and restore stream: no collection is ever held in memory whole, so a workspace with millions of transcript
lines exports in the memory of a small one.

**The archive is private, and it holds everything.** Its folders are made readable only by your account (0700) and its
files likewise (0600); a single file is 0600. But anyone who gets a copy can read all of it, people's consent records
and every transcript included. Keep it as carefully as the work itself. MetaRush never writes an archive into a folder
a cloud service syncs, a code repository or a project folder. It doesn't encrypt archives itself: keep them on an
encrypted disk, or encrypt the one file with a tool you trust (for example `age -p studio.tar > studio.tar.age`),
and decrypt it before restoring.

## Exporting

In the dashboard: **Settings → Storage → Export the workspace** (owners). Choose whether the footage is **listed by
where it's kept** or **copied into the archive**, **Check what it would hold** (counts, sizes, what stays out and why,
the room left where it goes), then **Export…**. The archive is made in the background in MetaRush's own `archives`
folder on the computer MetaRush runs on, as one file; the card shows how far it is and where it is (Details).

In a terminal (a person typing; Claude Code, AI apps and scripts nobody watches are refused):

```bash
metarush workspace export --to ~/Backups/fernbrook --plan                  # what it would hold; writes nothing
metarush workspace export --to ~/Backups/fernbrook                         # a folder, footage listed by where it's kept
metarush workspace export --to /Volumes/Backup/fernbrook.tar --tar --media included   # one file with every file of footage
metarush workspace exports                                                  # the exports made on this computer
```

Every one takes `--json`.

**Referenced or included footage.** Referenced (the default) keeps the archive small: it lists each file by its
content address, and a restore expects the files in the storage the copy uses — the same bucket or folder, or one
you move them to later (Settings → Storage → Move the media). Included copies every file into the archive, checking
each against its content address; files this computer already has are taken from its own copies instead of being
downloaded again, and an export that stops continues where it stopped.

**An export only reads.** It never brings the database up to date, never makes an index, never starts work and never
writes to it — so it reads a database an older MetaRush left as it was, before any newer MetaRush opens it, and a
database user that may only read is enough. Its own entry in the activity log waits on this computer and is written
into this computer's database by the next MetaRush that runs here. A database a *newer* MetaRush brought up to date is
refused: update MetaRush first.

**Another database.** To export a workspace from a database this computer isn't set up with, give its address on
standard input or in an environment variable — never on the command line, where it would stay in the shell's history
— and its storage the way `metarush setup` takes it:

```bash
security find-generic-password -w -s old-studio-db | \
  metarush workspace export --db-uri-stdin --db-name metarush --to ~/Backups/old-studio.tar --tar \
  --s3-preset tigris --s3-bucket old-studio-media --s3-access-key-id tid_… --s3-secret-env OLD_STUDIO_S3_SECRET
```

`--workspace <id>` picks one when the database holds several. The address and the storage's key are kept out of the
archive like any other secret.

**An export is a snapshot of the database as it reads it.** If MetaRush is working on another computer meanwhile,
what changes during the export may be only partly in the archive (the plan and the manifest say when MetaRush was
running somewhere). For an exact copy, quit MetaRush on the workspace's computers first.

## What goes in, and what stays out

Every collection MetaRush keeps is classified, and a test fails when a new one isn't, so nothing is ever left out
without a reason. A collection the database holds that MetaRush doesn't know is left out and named in the manifest.
Documents that belong to no workspace, or to another workspace in the same database, stay out and are counted in the
manifest's notes.

Always left out: passwords, sign-in links and any other credential; each computer's own state (its live record, what
it set up for abilities, its caches and scratch files, which live in its own folder anyway); test markers.

Every text in every record is also checked, and what is found is replaced with `[redacted]` (the manifest counts the
records and names the places). Exactly these are looked for:

- a private key, the whole block;
- keys in the shapes MetaRush knows: AWS access keys, storage keys (`tid_…`, `tsec_…`), `sk_…` and `sk-…` API keys,
  GitHub and Slack tokens, Google API keys (`AIza…`), ElevenLabs keys written as `xi-api-key: …`;
- a password in any address (`scheme://name:password@host`, whatever the scheme);
- a signature, key or token in an address's query: `X-Goog-Signature`, `X-Goog-Credential`, `X-Amz-Signature`,
  `X-Amz-Credential`, `X-Amz-Security-Token`, `api_key`, `apikey`, `api-key`, `key`, `token`, `access_token`, `sig`,
  `signature`;
- signed sign-in tokens (JWTs), Google client secrets (`GOCSPX-…`) and Google refresh tokens (`1//0…`);
- every secret this computer keeps for the installation: the database's password, the storage's secret key, providers'
  keys, the keys the workspace's abilities read, the publishing client's secret and the workspace's people's publishing
  sign-ins, the key that signs its confirm links, any key MetaRush read while running, and secret-named values and
  addresses' passwords in its secrets file and its environment. A value of 16 characters or more is taken
  out wherever it appears; a shorter one only where it stands as a word of its own (a short password that is also part
  of a file name or a command stays in those); one shorter than 8 characters is not looked for.

People's own words around them stay, and so do identifiers (a record's ID, any field named `…Id`, and any field a
unique index covers). Where a file came from on someone's computer (a clip's original path, shortened to `~`) is
history, and stays.

| Collection | Goes in | Why |
|---|---|---|
| `abilities` | as it is | abilities, their checks and owners' approvals |
| `ability_runs` | as it is | abilities' runs: what each sent, made and cost |
| `ability_setups` | left out | what each computer set up for an ability (programs and files on that computer, pinned there): set up again on the new computer |
| `asset_speakers` | as it is | voices heard in each clip |
| `assets` | as it is | footage: each clip's record, where its bytes are kept and what was learned from it |
| `assistant_conversations` | changed | Claude Code conversations: their text, cards and decisions (the Claude Code session itself stays on the computer that held it); one answering at the time is marked as waiting |
| `audit_events` | as it is | the change history: who changed what, and when |
| `auth_failures` | left out | counts of wrong passwords, kept a short while to slow guessing: about sign-ins to this database only |
| `auth_tokens` | left out | invitation and password-reset links: credentials; owners send new ones |
| `auto_spending` | as it is | what Auto spent by itself |
| `caption_translations` | as it is | captions in other languages |
| `catch_ups` | as it is | which catch-up steps already ran (so the copy doesn't run them again) |
| `collections` | as it is | collections in the Library |
| `comments` | as it is | comments on versions, outside reviewers' included |
| `consents` | as it is | consent records, whole, as the proof they are |
| `content_defaults` | as it is | which Content Settings new projects use |
| `content_settings` | as it is | Content Settings: brands, spelling lists, voices, house rules and what may leave |
| `conversations` | as it is | call recordings lined up on one clock |
| `drafts` | as it is | what AI apps proposed, waiting for a person or decided |
| `edit_revisions` | as it is | every version of every cut |
| `faces` | as it is | faces followed through clips |
| `footage_insights` | as it is | what MetaRush learned from the footage (scenes, pauses, what is on screen) |
| `generation_spend` | as it is | what was spent on AI work, per project and day |
| `generations` | as it is | AI work that was asked for, what it made and cost (written once) |
| `improvement_items` | as it is | gaps and friction the weekly report found |
| `improvement_reports` | as it is | the weekly reports |
| `ingest_sessions` | changed | upload records; an unfinished upload's resumable address is taken out (it allows writing into the storage) |
| `installations` | as it is | the workspace's computers as history: their names, systems and MetaRush versions, and the names (never the secrets) of their storage keys and database users; a computer that restores the archive joins as a new one |
| `jobs` | changed | work: what finished is history; what was running, and paid work that was waiting, is held for a person (a copy beside a running original never does it twice); other work asked for on one computer still waits for that computer |
| `keep_items` | as it is | what projects kept from conversations |
| `linked_folders` | as it is | linked folders' shared records (where each folder is on a computer stays in that computer's settings) |
| `log_entries` | as it is | the activity log (written without keys or passwords) |
| `looks` | as it is | looks of people and characters |
| `motion_components` | as it is | motion graphics, their checks and who approved them |
| `notifications` | as it is | each person's notifications |
| `people` | as it is | named people and their voice and face profiles |
| `presenters` | as it is | people's presenters at AI services and their states |
| `project_links` | as it is | projects using each other's things |
| `projects` | as it is | projects: names, briefs, members and settings |
| `qa_reports` | as it is | quality checks of renders |
| `recipe_runs` | as it is | recipes' runs: what each made and spent (a run goes on only on the computer that started it) |
| `recipe_versions` | as it is | recipes' versions |
| `recipes` | as it is | recipes, with their triggers (a trigger still works only on the computer it was turned on at) |
| `recordings` | as it is | screen recordings and what each became (pieces not yet made into footage stay on the computer that received them) |
| `remotion_pushes` | as it is | Remotion changes pushed back to repositories |
| `remotion_refreshes` | as it is | Remotion refreshes people started |
| `remotion_renders` | as it is | Remotion renders already made |
| `remotion_sources` | as it is | Remotion projects kept in projects |
| `remotion_versions` | as it is | Remotion projects' versions and who approved them |
| `renders` | changed | renders and where their files are kept; one being made is held with its work |
| `restore_lock` | left out | the marker a restore keeps in the database it writes into: that copy's own state |
| `database_move` | left out | the marker a move of the database leaves in the database it moved from: that database's own state |
| `resources` | as it is | pictures, logos, fonts, PDFs, repositories and web pages, with where each came from |
| `review_links` | as it is | links sent to outside reviewers, as the record of what was shared |
| `schema_migrations` | in the manifest | the database's migration level: written in manifest.json and set from it by the restore, which then brings the copy up to date |
| `sound_cache` | as it is | clearer voices already made (made once, kept by content address) |
| `source_copies` | as it is | fixed copies of footage |
| `spelling_entries` | as it is | spelling lists |
| `spoken_changes` | as it is | words changed in a speaker's own voice, as disclosed |
| `test_run` | left out | a test database's marker |
| `todos` | as it is | questions for people and how they were answered |
| `training_optouts` | as it is | AI services' training opt-outs that were set |
| `transcript_fixes` | as it is | people's fixes to transcripts |
| `transcript_originals` | as it is | transcripts as the machine wrote them, before the first fix |
| `transcript_segments` | as it is | transcripts: every clip's words with their times |
| `transcript_snapshots` | as it is | the words saved versions were made with |
| `user_credentials` | left out | passwords (kept as hashes): credentials never go into an archive; people set a password again |
| `users` | changed | people: names, emails, roles, pictures and preferences; passwords are not in the archive, so “password set” is cleared and people set one again |
| `voiceover_takes` | as it is | narration takes and what was spent on them |
| `worker_devices` | left out | each computer's live record of what MetaRush can do there right now (its programs, its battery): written again when MetaRush starts on a computer |
| `workspaces` | as it is | the workspace itself: its name, its settings and where its footage is kept (never a password or a key) |

**Computers, work and conversations.** The workspace's computers go in as history (names, systems, MetaRush versions);
a computer that restores the archive joins as a new one. Work that was running when the archive was made, and work
waiting that costs money or acts outside the workspace (AI services, the AI voice, abilities, Claude Code's scene
descriptions, downloads, pages and repositories fetched, a Remotion project brought in, a source's own steps, pushes to
a repository), is **held for a person** in the copy: stopped, never taken by any computer by itself, so a copy beside an
original that is still running never does the same work twice or pays for it twice. The restore lists it; start each
piece again (Retry, or `metarush job retry <job>`) once you're sure the original isn't doing it. Other waiting work
waits as before. Work asked for on one computer, Recipes' runs (each goes on only on the computer that started it) and
recipes that plan by themselves (on the computer they were turned on at) stay tied to that computer: on a copy restored
elsewhere they wait for it. Claude Code conversations go in whole — their text, cards and decisions — but the Claude
Code session that continues one stays on the computer that held it; one that was answering is marked as waiting.
Moving those to another computer is a computer's handover, not a restore.

## Restoring

A restore writes only into an **empty** database: one with anything in it is refused (MetaRush never deletes
anything; restore into a new database name, or empty that one yourself). It:

1. checks every file of the archive against the manifest's SHA-256 — and, with included footage, every file of it;
   one changed byte anywhere and nothing is written. An archive naming a kind of record an export never writes
   (passwords, say, or one MetaRush doesn't know), or a collection made as a view or with any setting (capped, a
   validator, a collation: MetaRush sets none), is refused the same way;
2. puts its marker in the database: until the copy is whole, checked and up to date, MetaRush doesn't work in that
   database at all — no command, background service, worker or assistant connection opens it, no computer is set up
   with it, it isn't brought up to date and it can't be exported; the checkup (`metarush doctor`) says so;
3. looks for referenced footage in the storage, and names what isn't there;
4. looks at the database again — anything written into it meanwhile and the restore stops, merging nothing;
5. makes MetaRush's own indexes, exactly as its migrations make them at the archive's level — never the archive's
   list of indexes (one could delete records after a time); any index the archive lists that MetaRush doesn't make
   is named in the report — then streams each collection's records in, checking each against the archive (count and
   SHA-256) as soon as it is in;
6. with included footage, asks each client's rules (What may leave) whether their footage may go into that storage,
   then copies it in by content address, each file checked on the way in;
7. sets the migration level the archive was made at, last of the copy's records;
8. brings the copy up to date with this MetaRush's migrations, and checks that every record of the archive is still
   there, by ID (migrations change records and may add the workspace's own, never take one away), and that nothing
   in the database belongs to another workspace;
9. pauses the copy's background deletions (below), marks the restore finished and writes it into the copy's activity
   log and change history, and says what happened in plain words.

If it stops while the records go in, the database holds part of the copy and its marker stays: MetaRush says so,
and you restore again into a new, empty database (it never deletes anything; empty that one yourself if you want it
back). If it stops while copying the footage or bringing the copy up to date, running the same restore again goes on
from there — after checking the copy against the archive once more. Stopped while copying the footage: every
collection's count and SHA-256 must be exactly the archive's, with nothing in a collection the archive hasn't.
Stopped while bringing the copy up to date (migrations change records, and may add the workspace's own): every
record of the archive must still be there, by ID, and nothing may belong to another workspace. Anything else and it
stops, the marker staying.

**Clients' rules.** An export writes only to this computer (or a drive attached to it): never into a folder a cloud
service syncs, a code repository or a project folder, so making one sends nothing of a client's anywhere. The archive
holds every client's things, though: where you take it afterwards is your own step. A restore that copies the
footage into storage a client's rules keep their footage from (a cloud bucket, a synced folder) is refused before
any footage moves, naming the rule and whose settings set it, and the refusal is in the copy's change history; run
the same restore again with the footage going into a folder on this computer. Referenced footage isn't copied
anywhere.

On a new computer, setting MetaRush up from the export does all of it and joins the restored workspace:

```bash
metarush setup --restore ~/Backups/fernbrook.tar
```

This computer works as the person whose email you give (`--user-email`), else as the workspace's owner.

Into an empty database of your choosing, with the storage the copy uses:

```bash
metarush workspace restore ~/Backups/fernbrook.tar --plan --db-uri-env COPY_DB --db-name fernbrook-copy --storage folder --media-root ~/fernbrook-media
metarush workspace restore ~/Backups/fernbrook.tar        --db-uri-env COPY_DB --db-name fernbrook-copy --storage folder --media-root ~/fernbrook-media
```

`--plan` checks the whole archive and says what would happen, writing nothing. Without the database flags the restore
goes into this computer's own database, which must then be empty. Restoring is never done from the dashboard.

**The workspace keeps its ID**, so a restore is a backup brought back, or the same workspace moved: everything that
names it — records, the storage's keys — still fits. With included footage the copy's workspace names the storage it
was copied into. A copy *as a new workspace* (a new ID beside the old one) isn't offered.

**After a restore.** People with a password set it again (the archive carries none): an owner sends them a link in
Settings → People. Keys for AI services and storage are per computer and never in an archive: set them up on the new
computer.

**A copy deletes nothing by itself at first.** A restored copy starts with its background deletions paused: the Trash
doesn't empty on its schedule, review links whose time is up (or that a client's rules or a withdrawn consent no longer
allow) don't end and keep their pages, and old activity isn't cleared out — the original may still use the same
footage and pages. Comments on review links still come in. The restore, setup and Settings → Storage say so. Once the
original is retired, or the copy has storage of its own, an owner says the copy is now the workspace, in person:

```bash
metarush workspace adopt
```

or Settings → Storage → **This copy is now the workspace…**. What a person deletes is never held back: a copy that uses
the same storage as the original shares it, so anything a person deletes in either one (emptying the Trash, clearing
out an archived project, ending a review link) is gone for both — for a trial copy, include the footage and restore it
into other storage.

## For developers

The work is `internal/wsarchive`, a library: `Export`, `MakePlan`, `Restore`, `Open`/`Verify`, the read-only reads of
a source database (`ReadLevel`, `ListCollections`, `ListIndexes`), the copy engine (`EachDoc`, `LineWriter`,
`InsertLines`, `Spec.Make`, `HashCollection`; a restore makes MetaRush's own indexes instead, `store.DB.MakeForRestore`) and the classification (`Catalogue`, kept whole by
`TestEveryCollectionIsClassified`). Copying one database into another and checking the copy before anything switches
uses the same pieces. The operations are `workspace.export`, `workspace.exports` and `workspace.adopt`
(`internal/ops/workspacearchive.go`);
the commands are in `internal/cli/workspace_archive.go` and `metarush setup --restore`.
