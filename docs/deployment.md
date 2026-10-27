# Deployment

MetaRush is a locally installed tool; there is no hosted service in V1. Installing it is covered in
[installation.md](installation.md) (Homebrew, a release archive, or a clone). This page covers running it unattended,
deploying from a checkout, and making releases.

- **Background daemon**: `metarush dashboard` starts `metarush serve` in the background (logs in `<home>/logs`);
  `metarush stop` stops it. One daemon runs per home (a second one refuses to start).
- **Background service**: `metarush service install` runs the daemon at login and restarts it if it crashes (below).
- **Headless worker** (e.g. a Mac mini): `metarush service install --worker-only`, or `metarush worker start` in a
  terminal, on a machine joined to the workspace.
- **Renderer**: every version renders with its own renderer in `<home>/renderer/<release>-<digest>`, set up on
  first use or with `metarush renderer install`; nothing points at a source checkout.

## The background service
```bash
metarush service install                # dashboard + worker, started at login, restarted if it crashes
metarush service install --worker-only  # the worker and background jobs only, no dashboard (a machine that renders)
metarush service status                 # installed? running? which binary? (--details: label, file, pid, last exit code)
metarush service logs [-n 200] [-f]     # the daemon's log, <home>/logs/daemon.log
metarush service restart                # after replacing the binary (waits until the new build answers)
metarush service uninstall              # stop it and remove it
```
Every command takes `--json`; `install` and `uninstall` take `--dry-run` (show the file and the commands, change
nothing).

- **macOS:** a per-user LaunchAgent, `~/Library/LaunchAgents/metarush.daemon.plist` (another home gets
  `metarush.daemon.<hash>`). It runs `metarush --home <home> serve [--worker-only]` with `RunAtLoad` and `KeepAlive`
  `{SuccessfulExit: false}`: launchd restarts it when it crashes (non-zero exit, after 10 s), not when it was stopped
  on purpose (`metarush stop` exits cleanly; it starts again at the next login or with `metarush service restart`).
  It is loaded with `launchctl bootstrap gui/<uid>`, so it runs while you are logged in (see the Mac mini runbook
  for a machine nobody sits at).
- **Linux:** a systemd user unit, `~/.config/systemd/user/metarush.service` (`metarush-<hash>.service` for another
  home), `Restart=on-failure`, enabled with `systemctl --user enable` and started with `systemctl --user restart`. To
  run it while nobody is logged in: `loginctl enable-linger $USER` (`service status` says when that is off).
- The service runs the binary at a path that survives upgrades (a Homebrew Cellar path becomes
  `<prefix>/opt/metarush/bin/metarush`); `--binary <path>` chooses another. Its `PATH` holds the binary's folder,
  the folders where MetaRush found Node.js, ffmpeg, Claude Code and yt-dlp, and the usual system folders.
- **Secrets:** a keychain installation works as before (the LaunchAgent runs in your login session). An env-mode
  installation (`--secrets env`) must keep its variables in `<home>/.env`: the service does not see your terminal's
  environment.
- `service install` stops a daemon that `metarush dashboard` started for the same home first, then waits until the
  service's MetaRush answers. With a service installed, `metarush dashboard` starts the service (not a second daemon)
  when it is stopped; on a machine whose service runs `--worker-only` it says so instead of starting anything.
- Tests use fake `launchctl`/`systemctl` programs (`tools.launchctl`, `tools.systemctl`, or `METARUSH_LAUNCHCTL` /
  `METARUSH_SYSTEMCTL`) and a temporary home folder; under `go test` the real ones are refused.

## The weekly improvement report
With the service (or any daemon) running, MetaRush can make an improvement report once a week by itself: the
activity log's signals, the log entries behind them, Claude Code headless with the `metarush-improve` skill (it can
only read those files and write the report: no shell, no network), then the ideas are saved and wait on the
Improvements page. It is **off by default**; a person turns it on, on one machine:

```bash
metarush improve schedule                          # off, or when the next report is due and how the last one went
metarush improve schedule --weekly on [--day monday --hour 9 --budget 2]   # people only (it spends on Claude Code)
metarush improve report [--since 7d] [--dry-run]   # make one now (--dry-run prepares what Claude Code would read)
```
Settings in `config.yaml`: `improve.weekly`, `improve.day` (default monday), `improve.hour` (local, default 9),
`improve.budgetUsd` (per report, default 2), `improve.model`. The daemon reads them every 15 minutes, so a change
needs no restart. A report another machine saved since this week's slot counts; a failed one is tried again after
six hours. It needs Claude Code installed and signed in on that machine.

## Moving to another storage
`metarush storage migrate --to <store-config>` moves the workspace's media between S3-compatible storage
(Tigris, R2, B2, AWS, GCS through its S3 interface), native Google Cloud Storage and a folder (local or a shared
mount), in any direction. Describe the new storage in a small YAML or JSON file with the keys of config.yaml's
`media` section (at the top level or under `media:`); secrets only as references, never pasted in:

```yaml
provider: s3
s3:
    preset: r2
    endpoint: https://<account-id>.r2.cloudflarestorage.com
    bucket: team-media
    accessKeyId: <key id>
    secretAccessKey: ${NEW_MEDIA_SECRET}   # an environment variable, or keychain:<name>
```
```yaml
provider: gcs
gcs:
    bucket: team-media
    prefix: metarush
    credentialsFile: ~/keys/metarush-media.json   # or leave it out for Application Default Credentials
```
```yaml
provider: filesystem
filesystem:
    root: /Volumes/Studio/MetaRush media
    shared: true
```
```bash
metarush storage migrate --to new-storage.yaml --dry-run   # what it would copy and download; changes nothing
metarush storage migrate --to new-storage.yaml             # the move (in your terminal: people only)
metarush storage migrate --to new-storage.yaml --read-back # also download everything again and check it
```
- **Checked:** every file's SHA-256 is checked against its content address before it is written, and the new storage
  checks what arrives (SHA-256 for a folder, MD5 per part for S3, CRC32C and MD5 for GCS). `--read-back` also reads
  every file back from the new storage. Files this computer already has (a folder store's files, the local cache)
  are not downloaded.
- **Resumable:** stop it at any time (Ctrl-C, or Stop on the job): run the same command again and it continues
  where it stopped: files already there are skipped, an interrupted S3 multipart upload or GCS session continues, a
  half-done download continues from its last byte. Progress shows in the activity bar ("Moving media: copying 120
  of 4,000 files"), job details and the log.
- **Switches only when everything is there:** after the last file is verified, this installation's config.yaml
  `media` section is replaced (a `${VAR}` secret is stored in the keychain on a keychain installation), MetaRush
  checks the new settings the way it loads them and reads a file back (and puts the old settings back if that
  fails), then the workspace records the new storage. A dashboard running on this computer restarts by itself with
  the new storage, and the move copies anything it added meanwhile.
- **Nothing is deleted:** the old storage is only read; differing files already in the new storage are reported,
  never overwritten. Remove the old storage yourself once every machine has moved.
- **Other machines** keep using the old storage until each runs the same move with its own keys (it copies anything
  new and switches that machine); until then `metarush doctor` there says the workspace uses another store. Keys a
  machine got from `metarush machine invite` belong to the old storage. Run the same command again later on a
  machine that already switched to copy files that reached the old storage since.
- `METARUSH_MEDIA_*`, `METARUSH_S3_*` and `METARUSH_GCS_*` variables override config.yaml, so the move refuses to
  start while one would point MetaRush elsewhere; the dry run lists them.

**From the dashboard**: Settings → Storage → *Move the media to another storage* is the same move for people
who'd rather not write a file. Pick a folder (or a shared drive), online storage or Google Cloud Storage and fill in
its details; *Check what it would copy* is the dry run (nothing is copied or created), *Move the media* asks in a
dialog, then the move runs on this computer as `storage migrate` does, in a process of its own, with its progress in
the activity bar; the dashboard restarts by itself with the new storage and the page says how it went. An S3 secret
key is typed once and kept in this computer's keychain (on installations that keep their keys in the environment
file, add it there as a variable and give its name). On the other machines, Settings shows *This computer still
uses the old storage* (and a dot on the Settings tab) with the same form filled in: add that machine's own keys
and switch it too. `metarush storage status` says the same in a terminal.

## MCP clients
`metarush mcp serve` lets Claude Desktop, Claude Code and other MCP clients use MetaRush's operations as tools over
stdio, read-only unless started with `--allow-changes` (each change then confirmed with a second call); people-only
operations are never offered. Setup and safety: [mcp.md](mcp.md).

## Deploying from a checkout: `scripts/deploy.sh`
```bash
scripts/deploy.sh                         # this machine: ~/.metarush and ~/.local/bin/metarush
scripts/deploy.sh --home ~/studio --bin-dir ~/bin
scripts/deploy.sh --service               # also install the background service if it is not installed
scripts/deploy.sh --host render-1 --worker-only   # another machine over SSH (its own ~/.metarush, ~/.local/bin)
scripts/deploy.sh --dry-run [--host render-1] # every step printed, nothing changed (the remote script too)
```
On this machine it: builds the dashboard and the binary (version, commit and build time stamped in by
`scripts/version.sh`, below: `0.2.0` at the release tag, `0.2.0-dev.3` three commits after it; `--dry-run` prints
them), **replaces the binary with a new file** (copied next to it, then moved into place: a running daemon keeps the
old file until it restarts, and macOS never sees an executable changed in place, which it kills with exit 137),
checks that the installed binary reports this commit and version, then runs `skills install --user`,
`renderer install`, `migrate`, and restarts the background service (or the daemon `metarush dashboard` started),
and finally checks that the daemon on the dashboard port reports this commit and version (`/api/health`).

With `--host`, it asks the other machine for its system and CPU (`uname -sm`), builds for it, copies the binary
to a temporary name in that machine's home folder (`scp`), and runs these steps there over `ssh` (key-based login,
`BatchMode`):

1. move the binary into `~/.local/bin/metarush` as a new file, print `metarush version` and check that it says the
   version built here;
2. stop if `~/.metarush/config.yaml` does not exist (join the workspace there first:
   [runbook-add-machine.md](runbook-add-machine.md));
3. `metarush skills install --user` (skip with `--no-skills`), `metarush renderer install` (a renderer someone left out
   on that computer stays out: it says so and the deploy goes on), `metarush migrate`;
4. `metarush service restart` when the service is installed (with `--service`/`--worker-only`: install it when it
   is not), then `metarush service status`.

`--dry-run --host render-1` prints that script without contacting the machine.

## Mac mini runbook (an unattended render worker)
1. **The machine:** macOS on Apple Silicon, signed in as the user who will run MetaRush. System Settings →
   Users & Groups → **Automatically log in as** that user (a LaunchAgent runs in a login session; FileVault must be
   off for automatic login, or someone types the password after each restart). Energy → **Prevent automatic
   sleeping** and **Start up automatically after a power failure**. Remote Login (SSH) on, with your key in
   `~/.ssh/authorized_keys`.
2. **Tools:** `brew install ffmpeg node` (and `whisper-cpp` if it should transcribe; Claude Code only if it should
   make the weekly improvement report). whisper.cpp keeps its model loaded between clips only with `whisper-server`
   from the same build next to `whisper-cli` and a version MetaRush checked (1.9.5; `metarush doctor` says): build
   both from the v1.9.5 tag (`cmake -B build && cmake --build build`) and copy `build/bin/whisper-cli` and
   `build/bin/whisper-server` together onto PATH; otherwise it loads the model for every clip.
3. **MetaRush:** `brew install --cask metavert-io/metarush/metarush` (see [installation.md](installation.md)), or
   from a checkout elsewhere: `scripts/deploy.sh --host render-1 --no-restart`.
4. **Join the workspace:** on an admin machine `metarush machine invite render-1`, then on that machine `metarush
   machine join <bundle>` ([runbook-add-machine.md](runbook-add-machine.md)). Keychain secrets work with automatic
   login; `--secrets env` keeps them in `~/.metarush/.env` instead.
5. **Renderer:** `metarush renderer install` (it sets the renderer up even with no terminal, as here over SSH;
   only a renderer someone left out on that computer needs `--force`).
6. **Service:** `metarush service install --worker-only`, then `metarush service status` and `metarush worker list`
   from any machine (it should be online and list `render`). Heavy jobs on a desktop never wait for a battery.
7. **Updates:** `brew upgrade --cask metarush && metarush migrate && metarush service restart`, or from a checkout
   `scripts/deploy.sh --host render-1`. The new version sets up its renderer on its first start; `metarush renderer
   prune` keeps the disk tidy. Settings → General on any computer (or `metarush version --details`) lists that machine
   with the version it runs and says so when it is older than the computer you are looking from.
8. **When something is off:** `metarush service logs -n 200`, `metarush doctor`, `metarush renderer status`.

## Versions
The version number is written in one place, `VERSION` at the top of the repository (the release, e.g. `0.2.0`).
`scripts/version.sh` turns it into what a build calls itself, and every way of building stamps that in, with the
commit and the build time:

| The build | Says |
|---|---|
| at the release tag `v0.2.0`, nothing uncommitted | `0.2.0` |
| 3 commits after the tag (or uncommitted changes: `0.2.0-dev.0`, commit `…-dirty`) | `0.2.0-dev.3` |
| no tag `v0.2.0` yet, or no git; a plain `go build`/`go run`/`go test` | `0.2.0-dev` |

`scripts/deploy.sh`, `scripts/release.sh` (goreleaser), `bootstrap.sh` and `start.sh` use `scripts/version.sh
--ldflags`. People see the release and its day: Settings → General ("MetaRush 0.2.0 · 20 Oct 2026", with a note when
a build has changes that aren't in a release yet), `metarush version`. The exact build is behind Details,
`metarush version --details` and the JSON: `metarush version --json` and `/api/health` carry `version`, `release`,
`dev`, `devCount`, `commit`, `date` (and `releaseDate`); `metarush version --details --json` (op `version.show`)
adds what's new and every computer's version. A build N commits past a release counts as newer than it
(`version.Compare`, like `git describe`; not semver's order for pre-releases). Each computer's daemon records the
version it runs every minute, so About flags a computer that runs an older MetaRush, in plain words.

**What's new** comes from the CHANGELOG.md built into the binary: a release shows its own section, a build past a
release shows `## Unreleased` first as "Coming in the next version". Each entry's bold lead is its headline when it
reads plainly; the rest of the entry (and entries without one) wait behind Details.

**Cutting a release** (`scripts/bump-version.sh`):
```bash
scripts/bump-version.sh 0.2.0 --dry-run   # what it would do; changes nothing
scripts/bump-version.sh 0.2.0             # the first release: VERSION already says 0.2.0 and nothing is tagged yet
scripts/bump-version.sh patch|minor|major # later ones: 0.2.0 → 0.2.1 | 0.3.0 | 1.0.0
```
It refuses a working tree with uncommitted changes (untracked files too), an empty `## Unreleased`, a version not
newer than `VERSION` (or `VERSION` itself once it is tagged) and a tag that exists already. Then it moves the
Unreleased entries under `## 0.2.0 — 20 Oct 2026` (today), leaves an empty `## Unreleased` above, writes `VERSION`,
commits both ("MetaRush 0.2.0") and makes the annotated tag `v0.2.0`. **It never pushes.** Afterwards
`scripts/deploy.sh` installs it (the build at the tag says `0.2.0`), `git push origin HEAD v0.2.0` shares it, and
`scripts/release.sh v0.2.0` publishes it.

## Releases: `scripts/release.sh`
```bash
scripts/release.sh --snapshot         # dry run: goreleaser release --snapshot --clean, then verify (nothing published)
scripts/release.sh v0.2.0 --plan      # the dry run, then print what publishing would do
scripts/release.sh v0.2.0             # the dry run, push the tag, build from it, verify, publish, update the tap
```
- **The release is cut first** with `scripts/bump-version.sh 0.2.0` (above): `release.sh` checks that `VERSION`
  says it, that CHANGELOG.md has its section and that the tag `v0.2.0` is on this commit, and publishes exactly that.
- **Builds** (`.goreleaser.yaml`): darwin/arm64, darwin/amd64, linux/arm64, linux/amd64, `CGO_ENABLED=0`,
  `-trimpath`, version, commit and date stamped into `internal/version` (a release from its tag; a snapshot with
  `scripts/version.sh`'s version, e.g. `0.2.0-dev.3`). Each `.tar.gz` holds `metarush`,
  `renderer/` (the renderer package, written by `metarush renderer export` from the binary itself), `skills/`,
  `docs/`, the README, license and changelog; `checksums.txt` has their SHA-256.
- **Verification** (both in the dry run and after building from the tag): the checksums match, every archive has
  the expected files and no `node_modules`, each binary is the right CPU type, and the native binary reports the
  version and carries byte for byte the archive's `renderer/`.
- **Publishing** happens only after the dry run passes and only from a clean tree: the tag pushed to GitHub,
  `gh release create` with the four archives and `checksums.txt` (a pre-release when the version has a suffix such
  as `-rc1`), then a check that the published `checksums.txt` is the one built.
- **Homebrew tap:** `metavert-io/homebrew-metarush` (created when missing). `scripts/brew-formula.py`
  writes `Casks/metarush.rb`, `Formula/metarush.rb` and the README from the release's asset IDs and checksums, and
  they are committed through the GitHub API as your git identity (`scripts/release.sh vX.Y.Z --tap-only` rewrites
  them for a release that is already published). Both download through the GitHub API
  (see [installation.md](installation.md)). The **cask** (recommended) unpacks the
  binary and clears the quarantine flag (`postflight_steps`); the **formula** depends on `ffmpeg` and `node`,
  installs the binary, `renderer/` and `skills/` (into `share/metarush`) and the docs, and its test runs `metarush
  version` and `renderer export`. The formula has no bottles, so on macOS Homebrew needs current Command Line Tools
  to install it (and to run `brew audit`); `brew style` checks both.
- **Signing**: after each macOS build, before archiving, `scripts/macos-sign.sh` signs with the Developer ID
  (hardened runtime, `scripts/macos-entitlements.plist`) and notarizes with `notarytool` when the Mac making the
  release is set up for it (`METARUSH_SIGN_IDENTITY`, `METARUSH_NOTARY_PROFILE`, in the environment or the git-ignored
  `release-signing.env`); verification then checks every macOS archive's signature, and the release notes say whether
  the builds are notarized. Without a Developer ID one line says the builds aren't signed and everything else is the
  same. The one-time steps: [installation.md](installation.md#signing-and-notarizing-releases).
