---
name: video-workspace-admin
description: "Check and maintain the person's video setup without showing secrets — doctor, machines and workers, stuck jobs, the cache, migrations, remote access, joining another computer. Not for finding projects (video-project). Routes to `metarush doctor`, `metarush workspace show`, `metarush worker list`, `metarush job list`. Works whether or not they say MetaRush (the `metarush` CLI). Use it for: \"is my setup healthy\", \"jobs stuck\", \"why is nothing processing\", \"which machines are connected\", \"cache usage\", \"open the dashboard from my other computer\"."
---

# Workspace admin

**What this covers.** Set up, diagnose and maintain the person's video editing setup without exposing secrets — check health with doctor (database, media store, tools, migrations, security), show the workspace, its machines and workers, local cache usage and stuck jobs, apply additive database migrations, explain how another machine joins, and relay fix instructions. Never prints connection strings or keys and never runs destructive commands. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

See [reference/cli.md](reference/cli.md) and [reference/safety.md](reference/safety.md).

1. **Diagnose.** `metarush doctor --json`: report each failing or warning check and its fix in plain words.
   `metarush workspace show --json` (workspace, database name, media store, installations — no secrets) and
   `metarush version --json`. Summarize what is healthy, what is not, and the exact next step. `metarush version
   --details --json` also lists every computer with the MetaRush it runs (`standing`: `older`, `newer`, `same`,
   `unknown`) and what's new in this version: say it plainly ("the Mac mini runs an older MetaRush; update it there to
   get the new features"), never the version's build details unless asked. Settings → General in the dashboard shows the same.
   **What works on this computer**: `metarush catalogue list --json` lists what MetaRush does built in and the
   abilities the workspace added, each with `worksHere` and, when not, `missing` ("needs FFmpeg", "needs an
   ElevenLabs key", "needs an owner's approval"); `--works-now` keeps only what works. Say what is missing in those
   words and the next step (Library → Abilities shows the same).
2. **Workers and jobs.** Start with `metarush activity --json`: everything in flight, and for anything queued a
   `waiting.code` with a plain `text` (`battery`: the machine is on battery below its threshold; `no_worker`: no
   online machine can run that job type; `heavy_disabled`; `busy`; `retry_backoff`). Then `metarush worker list
   --json` (online, job types, battery), `metarush worker settings --json` (this machine's threshold and whether it
   is holding big jobs) and `metarush job show <id> --json` for one job (`advice` says what to do in plain words).
   A worker starts with `metarush worker start` or `metarush dashboard`; CLI `--wait` flags process jobs in the
   terminal. Tell the person in words ("nothing is processing because MetaRush isn't running on any machine; opening
   the dashboard starts it"), not the commands. Retrying (`metarush job retry <id>`), running battery-held jobs now (`metarush job run-now <id>`) and
   changing the threshold (`metarush worker configure --heavy-battery-threshold <pct>`, people only) are changes:
   propose them, don't run them unasked.
3. **Watched folder and uploads.** `metarush watch status --json` (watching, off, folder missing, not running on this
   machine) and `metarush ingest sessions --json` (interrupted imports). Setting the folder (`metarush watch set
   <dir> --create`) is a change the user approves; it applies without a restart.
4. **Cache.** `metarush cache status --json`: the cache is local and never authoritative. Pruning
   (`metarush cache prune`) only evicts local copies — propose it with the size it frees; the user decides.
5. **Migrations.** When doctor reports pending migrations, propose `metarush migrate --json` (additive only:
   indexes and fields; it never drops data) and run it only after the user agrees.
6. **Another machine.** Run `metarush --help`; if it lists a `machine` command, use `metarush machine --help`
   and follow that. Otherwise: on the new machine, install MetaRush and run `metarush init --join <workspace-id>`
   with the same database and media store; the person types secrets into the prompt or keychain there.
   Workspace IDs come from `workspace show`.
7. **Renderer and background service.** `metarush renderer status --json`: every version of MetaRush renders with
   its own renderer (`state` ready, missing, installing, removed or damaged, with `problem` and `fix` in plain
   words). Setting it up (`metarush renderer install`) downloads packages; propose it, don't run it unasked.
   `metarush service status --json`: whether MetaRush runs in the background (started at login, restarted if it
   crashes). For a machine that only renders (a Mac mini), `metarush service install --worker-only` is the change to
   propose; the person runs it on that machine.
8. **Moving the media to another storage** (another bucket or provider, a shared folder): the person describes the new
   storage in a small file (the keys of config.yaml's media section; secrets only as `${VARIABLE}` or
   `keychain:<name>`, never pasted in) and checks it with `metarush storage migrate --to <file> --dry-run --json`
   (how many files, how much is downloaded, files already there, settings in the environment that would get in the
   way). The move itself is theirs to run in a terminal (`metarush storage migrate --to <file>`): it copies every
   file checked, can be stopped and started again, switches only once everything is there, never deletes the old
   storage, and shows in the activity bar. Each other machine runs the same move with its own keys.
9. **Claude Desktop or another MCP client.** `metarush mcp tools --json` lists what a client sees. Setup is the
   person's: `claude mcp add metarush -- metarush mcp serve` (Claude Code), or the `mcpServers` entry in Claude
   Desktop's config with the full path of `metarush` and `["mcp", "serve"]`. It is read-only unless they add
   `--allow-changes` (then every change is confirmed with a second call); things only a person may do are never
   offered.

10. **The dashboard from another computer (Tailscale).** `metarush remote status --json`: whether remote access is
   on (`on`), the address (`url`), whether it works now (`ready`), what is left in plain words (`steps`: turning on
   HTTPS certificates in the Tailscale admin console, under DNS; starting Tailscale's secure address with the
   `serveCommand`), and who signs in with their Tailscale account (`people`). Turning it on
   (`metarush remote enable --allow <their Tailscale account> --apply`), letting someone in or taking them off the
   list (`metarush remote allow|remove <account>`), signing out other computers (`metarush remote signout --all`,
   for a lost device) and turning it off (`metarush remote disable --apply`) are people-only: give the person the
   exact command for their own terminal and say what it does. A browser on another computer without a listed
   account signs in with a one-time link the person makes (`metarush dashboard --print-link --remote`; it signs
   that browser in as them). Never run `tailscale serve`, `tailscale set` or anything else that changes Tailscale.

**Never** print, paste or ask the user to paste connection strings, keys or tokens (if they appear in output,
don't repeat them); never run `init --reconfigure`, delete files, drop or clear databases, or edit
`config.yaml` secrets. Point to `metarush doctor` fix lines instead.
