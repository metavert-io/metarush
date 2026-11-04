# MetaRush.app

MetaRush.app is a small menu-bar app for Macs: it sets MetaRush up the first time, keeps it up to date, and opens the
dashboard. It carries the `metarush` program inside it and does everything through that program's `--json` commands,
so it adds no behaviour of its own: what it asks, what it shows and what it changes are MetaRush's.

- **Mac only.** On Linux MetaRush runs as a systemd user service (`metarush service install`); on Windows you use the
  dashboard in a browser ([windows.md](windows.md)).
- **No Dock icon.** It lives in the menu bar (a clapperboard). Quitting it leaves MetaRush running in the background.
- **Never a password or a key.** Anything secret — a cloud bucket's key, a database address with its password, an AI
  service's key — goes through MetaRush's own flows (the keychain, Settings), or the app sends you to Terminal.

## Installing

1. Download `MetaRush-<version>.dmg` from the project's releases, open it and drag MetaRush into Applications.
2. Open MetaRush from Applications. Until releases are signed with a Developer ID, macOS asks once: Control-click
   MetaRush → Open, or open it, then System Settings → Privacy & Security → **Open Anyway**.
3. Answer the setup panels (below). MetaRush opens in your browser when setup is done.

Opened somewhere else — from the disk image, or from Downloads, where macOS runs a temporary copy of an app — it
offers to move itself to Applications first (to `~/Applications` when you can't write to `/Applications`), because the
background service and the terminal link must point at a place that stays. An older MetaRush already there goes to
the Trash; a newer one, or one that is open, stays unless you say so (**Open the Newer One** and **Use That One** are
the suggested answers; versions compared the way Sparkle compares updates). Once you've opened it there, it clears
the download flag from its own parts: macOS would otherwise stop the `metarush` inside it when the background service
starts it on its own.

## The first run

The setup panels open by themselves only on a Mac where MetaRush isn't set up. Where it is (installed with Homebrew,
say, or a script), the app opens to its menu and changes nothing until you choose **Set Up MetaRush…**.

The setup panels are `metarush setup`, asked natively:

- **Where things are kept.** Footage and finished videos in a folder on this Mac (suggested; Choose… picks another),
  and your projects in a database on this Mac that MetaRush downloads from MongoDB and looks after (suggested). A cloud
  storage bucket or a database you already have (MongoDB Atlas, your own server) needs a key or a password, which the
  app never asks for: it copies the setup command for you and opens Terminal, and carries on once setup there is done.
- **This Mac.** Start MetaRush when you log in (suggested), open the app itself when you log in (suggested: it checks
  for updates only while it runs; macOS's own login items, and the menu's **Open at Login** turns it off), teach
  Claude Code MetaRush's video skills (suggested when Claude Code is installed), and let your terminal and Claude Code
  find MetaRush (below). More settings: the dashboard's port.
- **Another copy in the background.** When another copy of MetaRush already starts when you log in (Homebrew's, or
  `~/.local/bin/metarush` from a script), the app doesn't suggest taking its place: it says so, with that copy's
  version, and asks a question of its own, **Start this app's copy instead**, unticked. When that copy is newer than
  the app, it isn't offered at all. Taking its place keeps a worker a worker (`--worker-only`).
- **Optional components.** The same checklist as `metarush setup` and Settings → Components, the suggested ones ticked
  (the Remotion renderer among them, with Remotion's licence shown when it is ticked), with what the ticked ones
  download and how long they take.

Then setup runs (a few minutes: the database download and the components are the longest part; Stop leaves it to
carry on next time), the background service starts, and the dashboard opens on its welcome. The last panel says what
was set up and what works now. On a Mac where MetaRush is set up already, the panels skip where things are kept
(setup never moves them) and check that everything is in place.

Where the answers come from: `metarush setup questions --json` gives every question with its suggested answer and the
components checklist, changing nothing; the app sends the answers back as setup's flags (`--storage folder
--media-root … --database here --port … --service=… --skills=… --components …` with `--yes --json`). Another copy's
service is `service.otherCopy` there (with `otherVersion`, `newer` and `workerOnly`; `suggested` is then false), and
setup leaves it as it is unless it gets `--take-over-service`, which it refuses when that copy is newer.

### Your terminal and Claude Code

Claude Code's skills and your terminal call MetaRush by name. With your yes, the app puts a link in your own programs
folder, `~/.local/bin/metarush`, pointing at the program inside the app, so they always use the app's copy and it
stays up to date with the app. If another `metarush` is already there (installed with Homebrew or by hand), the app
asks first, showing both versions (**Keep the Other One** is the default), and keeps the other one beside it as
`metarush.before-app`. When MetaRush's background service starts MetaRush from that very path, the app says that the
service then runs the app's copy from its next start; when the copy running there is newer than the app, the link
isn't offered. The menu's **Let Your Terminal Use MetaRush…** does the same later. If your terminal still can't find
it, add `~/.local/bin` to your shell's `PATH`.

## The menu

- What MetaRush on this Mac is doing: running or not, the version (and the version still running in the background
  until it restarts), whether another copy than the app's runs it, and the first problems the checkup found
  (`metarush doctor`), in its own plain words.
- **Open MetaRush** — the dashboard in your browser, signed in (`metarush dashboard`, which starts MetaRush when it
  isn't running).
- **Set Up MetaRush…**, **Check for Updates…**, **Start MetaRush** / **Stop MetaRush**, **Show Logs** (the background
  log in Console), **Open at Login** (a tick when the app opens when you log in), **About MetaRush** (with the
  third-party notice), **Quit**.

The app looks again every minute and when you open the menu; the checkup runs every half hour while MetaRush runs
(it would start the database on this computer, which Stop MetaRush leaves stopped). It uses MetaRush's
own home (`METARUSH_HOME`, else `~/.metarush`); a Mac can point it at another one with `defaults write <the app's
bundle identifier> MetaRushHome <folder>`.

## Updates

The app updates itself with [Sparkle](https://sparkle-project.org) 2.10.1. It checks once a day while it runs (and
when you choose Check for Updates…), so it is suggested to open at login; an update found in the background waits in
the menu, with a dot on the icon, instead of a window over your work. Sparkle shows what's new (the version's
headlines from CHANGELOG.md), downloads it, checks it, replaces the app and opens the new one.

- **Every update is signed, and so is the feed.** Sparkle checks the archive's EdDSA (Ed25519) signature against the
  public key inside the app before it unpacks anything (`SUVerifyUpdateBeforeExtraction`: an archive with a wrong or
  missing signature, or changed, is refused, and the app's code signature is never a way around it), and takes only a
  feed signed with the same key (`SURequireSignedFeed`: an unsigned or changed feed is refused before anything
  downloads). Both settings are in the app from its first release. The private key exists only in the keychain of the
  Mac that makes releases. A build without a real key (any build from a checkout, unless you give it one) never
  updates.
- **The feed's address** is a setting. Each build carries one (by default the repository's latest release:
  `https://github.com/<owner>/<repo>/releases/latest/download/appcast.xml`); a Mac can use another with
  `defaults write <the app's bundle identifier> UpdateFeedURL https://…` (https only; plain http only to this
  computer, for tests). The official builds' identifier is `io.github.metavert-io.metarush`.
- **After an update**, the new app finishes what `scripts/deploy.sh` does, with its own program, showing each step:
  Claude Code's skills updated (only where they are installed), this version's renderer set up (`renderer install`; a
  renderer you left out stays out), your projects' records brought up to date (`migrate`), MetaRush restarted in the
  background (`service restart`, or stopped and started again when the dashboard started it), and a check that what
  answers is the new build (`service status --json`: the running MetaRush's version and commit). It does this by
  itself once, and only for a build newer than the last one that finished (Sparkle's comparison): an older build put
  back in place never runs the steps. A step that fails says why in MetaRush's words, with Try Again; until every step
  is done the menu offers **Finish the Update…**, which shows the steps with Try Again — nothing restarts by itself at
  later starts.
- **Another copy running here.** Before any step, the app looks at what runs MetaRush for this Mac: each MetaRush
  records the copy it runs (`service status --json`: `daemon.program`, `sameCopy`, `newer`), and a background
  service that starts MetaRush through a link (`~/.local/bin/metarush`) counts as another copy, since the link could
  lead elsewhere later. When another copy runs it — the background service, or one opened by its dashboard — nothing
  is changed or stopped: the app says so and asks, **Keep the Other One** being the default. **Use This App's
  MetaRush** has the service run the app's copy from then on (`service install --keep-newer`, with `--worker-only` for
  a worker), or restarts one opened by its dashboard as the app's copy. When that copy is newer than the app, the app
  only says so; `--keep-newer` refuses too, for a copy that isn't running and so only its program can say its version.

## Building

The app is a Swift package in `desktop/mac`, built with Apple's Command Line Tools alone (no Xcode project):

| Target | What it is |
|---|---|
| `MetaRushCore` | Everything the app decides, without AppKit or Sparkle: running the program and reading its answers, setup's flags, the steps after an update, the menu's status, the terminal link, where the app runs, the feed and the key, the words it shows, the mark it draws. Tested headlessly. |
| `MetaRushApp` (the `MetaRush` product) | The menu (an AppKit status item), the windows (SwiftUI) and Sparkle. Thin. |
| `AppHarness` | For tests and the build only, never shipped: applies an update with the real Sparkle headlessly, runs the steps after an update, compares versions as Sparkle does, draws the icon. |

```bash
scripts/mac-app.sh build                       # dist/mac/MetaRush.app, MetaRush-<version>.zip and .dmg (universal: Apple silicon and Intel)
scripts/mac-app.sh build --arch arm64 --skip-web --no-dmg --out /tmp/app   # a quick local build
scripts/mac-app.sh test                        # the app's own tests (swift-testing)
go test ./internal/macapp/                     # the update test with the real Sparkle, the feed, the app's plain words
```

- **The bundle**: `Contents/MacOS/MetaRush` (the app), `Contents/Helpers/metarush` (the program, built with
  `scripts/version.sh --ldflags` for arm64 and amd64 and joined with `lipo`), `Contents/Frameworks/Sparkle.framework`,
  `Contents/Resources/AppIcon.icns` (drawn from the MetaRush mark), `THIRD_PARTY.md` and `LICENSE`. The background
  service runs the program where it is inside the app, so an update, which replaces the app in place, reaches it.
- **Versions**: `CFBundleShortVersionString` is MetaRush's version (`0.15.0`, `0.15.0-dev.25`); `CFBundleVersion`,
  what Sparkle compares, is written so that Sparkle orders builds as MetaRush does: `0.15.0-dev.25` becomes
  `0.15.0.25` (newer than `0.15.0`; Sparkle would read the `-dev` the other way round) and `0.16.0-rc1` becomes
  `0.16.0rc1` (older than `0.16.0`). `go run ./scripts/macapp bundle-version <version>` prints it.
- **The bundle identifier** comes from the repository the checkout pushes to (`io.github.<owner>.<repo>`;
  `--bundle-id` or `METARUSH_APP_BUNDLE_ID` gives another). Keep it the same from release to release.
- **The feed address and the update key** are filled into `desktop/mac/Info.plist`'s placeholders at build time,
  value by value with `plutil` (`--feed-url`, https only; `--ed-public-key`, `METARUSH_SPARKLE_PUBLIC_KEY`, or
  `--key-from-keychain`). A key given is checked against the keychain's (`generate_keys -p`) when the Mac has one: an
  app built with another key would refuse every update signed there. The repository holds no key.
- **Signing**: ad hoc by default (macOS then asks once before it opens a downloaded copy). With a Developer ID set up
  for the CLI's releases (`METARUSH_SIGN_IDENTITY`, and `METARUSH_NOTARY_PROFILE` for notarization: environment or
  `release-signing.env`, [installation.md](installation.md#signing-and-notarizing-releases)), the build signs Sparkle's
  helpers, the framework, the program (hardened runtime, `scripts/macos-entitlements.plist`) and the app, then
  notarizes the zip and staples the ticket to the app.
- **The Command Line Tools' gaps**, worked around: swift-testing's macros need the toolchain's `plugins/testing`
  folder on the plugin path (`scripts/mac-app.sh test` adds it when it is there), and SwiftUI's `@State` is a macro
  whose plugin comes only with Xcode, so the app's views keep their state in `ObservableObject` models.

### Sparkle's own tools

`scripts/mac-app.sh tools` downloads Sparkle 2.10.1's release (`Sparkle-2.10.1.tar.xz`, checked against its SHA-256)
into `~/Library/Caches/metarush/sparkle/2.10.1` and prints the folder; its `bin/` has `generate_keys` and
`sign_update` (`METARUSH_SPARKLE_TOOLS` names another folder of them; the tests use the ones that come with the
package's Sparkle). `desktop/mac/Package.swift` pins the framework (the release's archive for Swift Package Manager,
by version and SHA-256), and a test keeps the two at the same version.

## Releasing the app (later)

Nothing publishes the app yet: `scripts/release.sh` publishes the CLI's archives and the Homebrew tap, and adding the
app to it is a later step. By hand, on the Mac that makes releases:

**Once: the update key.** It is made once and kept for good: every copy of the app checks updates against it.

1. `"$(scripts/mac-app.sh tools)/bin/generate_keys"` makes an EdDSA key pair, keeps the private key in your login
   keychain ("Private key for signing Sparkle updates") and prints the public key. Run it again any time to see the
   public key; it never replaces a key that is there.
2. Keep a copy of the private key offline: `generate_keys -x <file>` exports it; store that file somewhere safe and
   off this computer (a password manager, an encrypted drive), never in a repository. Without it no new update can be
   signed for copies already installed.
3. Give the build the public key: `METARUSH_SPARKLE_PUBLIC_KEY=<the public key>` in `release-signing.env` (git ignores
   it), or build with `--key-from-keychain`.

**Each release**, after `scripts/bump-version.sh` has cut it:

```bash
scripts/mac-app.sh build                                     # the update key from release-signing.env (or --key-from-keychain), checked against the keychain's
scripts/mac-app.sh appcast dist/mac/MetaRush-0.16.0.zip      # dist/mac/appcast.xml, what's new from CHANGELOG.md; the archive and the feed signed with the keychain's key
```

`appcast` then checks both signatures against the key inside the archive's app and stops when they don't match (or
when the app has no key), so a release the installed apps would refuse never leaves the Mac. Edit `appcast.xml` only
by running `appcast` again: any change after signing makes the apps refuse the feed.

then attach `MetaRush-0.16.0.dmg`, `MetaRush-0.16.0.zip` and `appcast.xml` to the GitHub release `v0.16.0`. The
appcast names the archive at `https://github.com/<owner>/<repo>/releases/download/v0.16.0/MetaRush-0.16.0.zip`
(`--url` gives another). Apps read the feed of the latest release, so a pre-release isn't offered (it isn't "latest").
`scripts/mac-app.sh sign-update <archive>` prints an archive's signature by itself.

## Tests

- **The app's logic** (`scripts/mac-app.sh test`, swift-testing): reading the program's answers, setup's flags from
  the panels' answers (another copy's service a question of its own, never offered over a newer copy, one more try at
  the service never over another copy's), the steps after an update in deploy.sh's order (skills only where they are,
  a renderer left out kept out, a failed step stopping there, the old build still answering, another copy in the
  background or opened by its dashboard asked about and never stopped without a yes, a newer copy never taken over, a
  worker kept a worker, a dashboard started by hand, nothing running), the marker (only a newer build finishes by
  itself, once; a downgrade never does), the menu's status, the terminal link (asked before replacing, both versions,
  what it means for the service, the other copy kept), where the app runs, the download flag, moving to Applications
  (a newer or open copy asked about), opening at login, the feed and the key, the mark. They run the stand-in
  `metarush` (`Tests/MetaRushCoreTests/Fixtures/fake-metarush.sh`) in temporary folders only, keep the marker in
  memory and use a stand-in login item: nothing is written to your preferences or registered with macOS.
- **The answers** the tests read are written from the Go types the commands print (`internal/cli`
  `TestMacAppFixtures`; `METARUSH_UPDATE_APP_FIXTURES=1` rewrites them), so a renamed field fails a test.
- **The update test** (`go test ./internal/macapp/`, on a Mac with Swift): two test bundles (0.1.0 installed, 0.2.0
  offered) with the Info.plist the app ships, signed ad hoc, carry the stand-in program; a key made at run time, never
  kept, signs the 0.2.0 archive and the feed; the feed is served on a loopback port; the real Sparkle applies the
  update as a person pressing Install would; the steps after it run from the new bundle against a temporary home. An
  unsigned, changed or foreign-signed feed is refused before anything downloads; a wrong, missing or changed archive
  signature is refused before anything is unpacked (and a control without `SUVerifyUpdateBeforeExtraction` shows
  Sparkle would otherwise unpack it first); an older release isn't offered. Sparkle's installer keeps a preference and
  a cache folder under the test's own bundle identifier (`io.metarush.app-update-test`) in your Library, and the test
  removes them. The same package checks that its feed signature is the one Sparkle's `sign_update` verifies, and runs
  `scripts/mac-app.sh appcast` (a release signed with another key, or an app without one, stops there) and the
  build's refusals (a plain-http feed, a key that isn't the keychain's).
- **Plain words**: `TestAppSpeaksPlainly` runs every string the app shows through the same check as the CLI and the
  dashboard.
- CI builds the app with the dashboard and runs these on macOS when the app, or what it runs and reads, changes
  (`.github/workflows/mac-app.yml`); its artifact is a test build without an update key, so it never updates itself.

Third-party software in the app: [desktop/mac/THIRD_PARTY.md](../desktop/mac/THIRD_PARTY.md) (Sparkle, MIT).
