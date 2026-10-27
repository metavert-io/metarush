# Testing

How MetaRush is tested: five layers, from checks that take seconds to runs that need a Mac or a
live service; what runs where and when; how to run each; how coverage is measured and held; and what cleans up
after a run. One command runs the right set: `scripts/check.sh --quick | --full | --release`.

## The layers

| Layer | What it catches | Where | Run with |
|---|---|---|---|
| 1. Static | formatting, vet, types (Go and TypeScript), a schema out of date, a Linux or macOS build that breaks, likely secrets, scripts that don't parse | anywhere, seconds | `scripts/check.sh --quick`; CI `go-static`, `js`, `secret-scan` |
| 2. Go tests on a real MongoDB | every package's behaviour, ops end to end in-process, jobs and leases, migrations; fakes for every outside service | a test MongoDB that is a replica set, as live updates need (`scripts/dev-mongo.sh`; CI starts `mongo:8.0` as a one-member set with the same settings, `scripts/ci-mongo.sh`) | `go test ./...` (or `scripts/coverage.sh --test`); CI `go` (four jobs) |
| 3. Several machines in one process | two installations sharing one database and one bucket (an in-process S3), failover, collaboration, renderer installs | `tests/e2e` (Go) | part of the Go suite |
| 4. Browser runs with fakes | the dashboard in Chromium (Playwright) on a throwaway installation: styled dialogs only, plain words, phone and desktop, dark and light; the CLI end to end (acceptance) | `scripts/*-e2e.sh` + `web/scripts/*-e2e.mjs`; `scripts/acceptance.sh` | `scripts/check.sh --full`; CI `browser` (nightly, tags, by hand) for the ones that run on Linux |
| 5. Mac-only and live services | Apple Vision and faces, speech fixtures made with `say`, Resemble/DeepFilterNet sound components, real buckets, real Claude Code, the renderer in its jail, release archives | a Mac (and secrets for the buckets) | `scripts/check.sh --full` / `--release`; CI `go-live` when the secrets exist, `bootstrap` (macOS) weekly |

Measurements, run by hand when a recognizer or its settings change (build tag `voiceeval`, never in CI): the
English recognizers' comparison, the loaded models against the CLI, and the multilingual path
(`scripts/fetch-speech.sh` fetches openly licensed readings into the gitignored `tests/fixtures/speech/`,
then `EVAL_SPEECH=tests/fixtures/speech EVAL_LANGS=es go test -tags voiceeval -run TestMultilingual
./internal/transcribe/`, one language at a time).

Fakes, so no test reaches a real service: `internal/assistant/fakeclaude` (Claude Code), `internal/testutil/fakeeleven`
(ElevenLabs), `fakevideogen` (Gemini), `faketailscale` and `fakeserve` (Tailscale), `fakeytdlp` (yt-dlp), `fakerclone`
(rclone and its Google Drives; the real rclone is refused under `go test`), `gcsemu`
and gofakes3 (buckets), `dblink` (a database link a test can cut), `fakenode`, and a fake `brew` and `gh` where
needed. Downloads are refused under `go test` (the stabilizer, the face model) unless a local copy is given, and so are
Google's and ElevenLabs' real addresses (a test that forgets its fake fails before anything is sent).

## What runs where and when

| When | Where | What |
|---|---|---|
| Before a commit | your machine | `scripts/check.sh --quick`: gofmt, vet, scripts parse, secret scan, dashboard types and unit tests, the Go tests of the packages the branch changed |
| Every push and pull request | GitHub (`ci.yml`) | `secret-scan`; `go-static` (gofmt, vet, a macOS build, the CI groups, scripts parse, `scripts/lib/e2e-test.sh`); `go` × 4 side by side (`assistant`, `ops`, `heavy`, `rest` from `scripts/ci-go-packages.sh`, with coverage to Codecov); `js` (types, the dashboard's unit tests with coverage, motion tests, the extension's tests, the dashboard build); `go-live` (real buckets, only with the secrets) |
| Nightly, on version tags, by hand | GitHub (`browser.yml`) | the browser runs that work on Linux, one job each, as coverage builds; a failed run's homes, logs and screenshots as an artifact |
| When the bootstrap changes, weekly, by hand | GitHub (`bootstrap.yml`) | `./bootstrap.sh --dry-run` on macOS |
| Before a release | your Mac | `scripts/check.sh --release`: everything in `--full`, then `scripts/release.sh --snapshot` |

`scripts/check.sh --full` (an hour or more) runs, one after another: a sweep of leftovers, the dashboard (types,
build, unit tests), `bin/metarush` and `go build ./...`, the schema check (`metarush schema export` + `npm run
gen:types` must change nothing), gofmt, a Linux build, vet, scripts parse, `metarush skills test`, the secret scan,
`npm run test:motion -w video`, the render self-test, the e2e library's checks, the Go suite with coverage and the
ratchet (`scripts/coverage.sh --test`), `scripts/acceptance.sh`, every `scripts/*-e2e.sh` one at a time (they use
fixed ports), a sweep again, and with `COVERAGE=1` the merged coverage report. Each step's output is in its own log
in a folder it prints (`.agent-home/check/<time>-<mode>/`), and a table at the end says what passed, what failed and
how long each took; the exit status is 1 when anything failed. `--stop` stops at the first failure;
`ONLY_E2E="about storage"` and `SKIP_E2E="remote"` choose browser runs; `GO_TEST_P` sets how many packages test at
once (default 2: three other builds may share the machine and the test mongod).

### Browser runs on Linux and on a Mac

| Run | Linux (CI `browser`) | Why not on Linux |
|---|---|---|
| about, accounts, content, keep, playback, recipes, recorder, remotion, windows | yes | — (their footage is made with ffmpeg in the run; recipes talks only to the fake ElevenLabs and Gemini; recorder uses Chromium's fake camera and microphone and a canvas for the screen; windows plays a Windows browser in Chromium, and in Edge where it is installed) |
| cards, delivery, generate, library, people, resources, sources, storage, transcript, voiceover, voiceover-live, acceptance | Mac only | they need `tests/fixtures/gen`, which `scripts/make-fixtures.sh` makes with macOS `say` (speech with known words) |
| sound | Mac only | its fixtures are made with `say` too (`scripts/make-sound-fixtures.sh`) |
| remote | Mac only | its check of spoofed Tailscale headers sends from `fe80::1%lo0` (`lo0` is macOS's name for loopback) |
| abilities, catalogue | Mac only (for now) | they check an ability in the sandbox; on Linux that needs bubblewrap allowed to make its own sandbox (docs/abilities.md), which the CI runner gets only in the `sandbox-linux` job |
| install | Mac only (for now) | it serves a stand-in for MongoDB's download made from a copy of a real `mongod` (a local `mongod`, or `METARUSH_TEST_MONGOD`); CI runs MongoDB in a container, with no `mongod` to copy |

The nightly runs keep the Linux list honest: a run that fails there for a Linux reason moves to the Mac list here
and leaves `browser.yml`'s matrix.

### What a Mac needs for the fixtures

- **ffmpeg and `say`** for every fixture script. The speech is read by fixed voices (Samantha, Daniel, Karen, Moira;
  the call's Tessa, Rishi and Moira), picked by `scripts/lib/voices.sh`: newer macOS lists many voices with their
  language ("Samantha (English (US))"), and the bare name still picks the same voice, so every Mac writes the same
  footage. A voice that isn't installed falls back to the next one listed, and the fixtures' timings change with it
  (the browser runs that cut at fixed times then cut elsewhere): install the voices in System Settings →
  Accessibility → Spoken Content → System voice → Manage Voices. The ground truth in `tests/fixtures/truth/` is
  written again by every rebuild; a few milliseconds' difference from another Mac's `say` is expected there, and
  isn't committed.
- **ImageMagick** (`magick`; `brew install imagemagick`) for `scripts/make-conversation-fixtures.sh` and
  `scripts/make-call-fixtures.sh` (cards, people, acceptance): they draw the people, tiles and name labels with it and
  stop with a plain message without it, as plain boxes would leave the face and name checks nothing to find.
- **The face model** for the face tests (`internal/visual`, which skip without it): a copy at
  `${TMPDIR%/}/metarush-faceid-test-cache/face_recognition_sface_2021dec.onnx`, or `METARUSH_FACE_MODEL=<file>` (which
  `people` and `acceptance` also copy into their homes, so they don't download it). Tests never download it:

  ```bash
  mkdir -p "${TMPDIR%/}/metarush-faceid-test-cache" && cd "${TMPDIR%/}/metarush-faceid-test-cache"
  curl -fL -o face_recognition_sface_2021dec.onnx \
    https://huggingface.co/opencv/face_recognition_sface/resolve/3d7082438a6e4551e840c9b2bb60b71e8da4b524/face_recognition_sface_2021dec.onnx
  shasum -a 256 face_recognition_sface_2021dec.onnx   # 0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79
  ```

## How to run

```bash
scripts/dev-mongo.sh start                       # the test MongoDB: 127.0.0.1:27018, replica set rs0
scripts/check.sh --quick                         # before a commit
scripts/check.sh --full                          # before a merge or a release (an hour or more)
go test -p 2 -timeout 45m ./internal/ops/        # one package (always -timeout 45m: ops and assistant need it under load)
scripts/coverage.sh --test                       # the Go suite with coverage, the per-package table and the ratchet
scripts/about-e2e.sh                             # one browser run (each script's head says its ports and options)
COVERAGE=1 scripts/about-e2e.sh                  # … as a coverage build (Go and the dashboard's pages)
scripts/lib/e2e.sh list                          # runs the e2e library knows about
scripts/lib/e2e.sh sweep --now                   # clean up after every run that is gone, kept ones too
go run ./scripts/sweeptestdbs [--drop]           # test databases whose run is gone
```

Environment: `METARUSH_TEST_MONGODB_URI` (default the dev replica set; only 127.0.0.1/localhost is accepted),
`METARUSH_REQUIRE_MONGO=1` (fail instead of skip without a MongoDB: CI), `METARUSH_TEST_DB_FRESH=1` (migrate every
test database instead of copying the template), `METARUSH_TEST_DB_STATS=<file>` (note how each test database was made
and how long it took), `KEEP=1` (a browser run keeps its home, database and dashboard running), `SKIP_BUILD=1` (use
the `bin/metarush` and `web/dist` already built), `NO_BROWSER=1` (start the dashboard and stop there).

### The live storage tests

`TestS3Live` and `TestGCSLive` (`internal/media`) run the storage drivers against a real bucket; without their
variables they skip, and the same checks run on every `go test` against emulators, so the harness itself is known to
work. `TestS3Live` takes `METARUSH_TEST_S3_*` from `metarush machine ci-key`
([runbook-add-machine.md](runbook-add-machine.md#ci-key-live-bucket-test)).

`TestGCSLive` checks an interrupted resumable upload resumed by the same driver and by a new one (a restart), ranged
reads, a no-op re-upload, missing objects, a canceled session, and a V4 signed URL read with a `Range` header (when
the credentials can sign). It writes only under `<prefix>/<timestamp>-<random>/` and deletes what it wrote, also when
a check fails. To set it up once:

1. A bucket with uniform bucket-level access (a test bucket, or the real one: the test stays under its prefix):
   `gcloud storage buckets create gs://<bucket> --location=<region> --uniform-bucket-level-access`
2. A service account that can reach only `ci-test/` in it:
   `gcloud iam service-accounts create metarush-ci --display-name "MetaRush CI"`, then
   `gcloud storage buckets add-iam-policy-binding gs://<bucket> --member=serviceAccount:metarush-ci@<project>.iam.gserviceaccount.com --role=roles/storage.objectUser --condition='expression=resource.name.startsWith("projects/_/buckets/<bucket>/objects/ci-test/"),title=metarush-ci-test-only'`
3. A key for it, kept outside repositories and synced folders:
   `gcloud iam service-accounts keys create ~/.metarush-bundles/metarush-ci-gcs.json --iam-account=metarush-ci@<project>.iam.gserviceaccount.com`
   (where an organization policy forbids keys, use Application Default Credentials instead: leave
   `METARUSH_TEST_GCS_CREDENTIALS` empty after `gcloud auth application-default login --impersonate-service-account=metarush-ci@<project>.iam.gserviceaccount.com`; signed URLs are then not checked).
4. On a machine: `METARUSH_TEST_GCS_BUCKET=<bucket> METARUSH_TEST_GCS_CREDENTIALS=~/.metarush-bundles/metarush-ci-gcs.json go test -count=1 -run TestGCSLive -v ./internal/media/`
   (`METARUSH_TEST_GCS_PREFIX` changes `ci-test`; `STORAGE_EMULATOR_HOST` must not be set).
5. In CI: repository secrets `METARUSH_TEST_GCS_BUCKET` and `METARUSH_TEST_GCS_CREDENTIALS_JSON` (the key file's
   contents: `gh secret set METARUSH_TEST_GCS_CREDENTIALS_JSON < ~/.metarush-bundles/metarush-ci-gcs.json`, then delete
   the file). The CI step writes it to a 0600 file for the test and removes it; forks have no secrets and skip it.

## The tool-selection eval

How well MetaRush's tool search (`find`) chooses the operation for a plain request is measured on
`internal/catalog/selection.json`: 166 requests, each with the operation that should be chosen. Every third request
is held out: tune `find` (the words in `internal/catalog/words.go`) on the others only, so the held-out score says how
it does on requests nobody tuned it for. `go test ./internal/catalog` scores it with no model and fails below
`catalog.SelectionGate` (right first, in the first three, held out, and with 500 made-up entries beside the
operations); `METARUSH_EVAL_MISSES=1` lists the tuned misses. `metarush skills eval [--details]` prints the same.
`metarush skills eval --live [--mcp core|review|…] [--limit N]` asks real headless Claude Code (layer 5: your Claude
account, a few cents a request; never in CI, which it refuses) and reports how often it chose right first and the
tokens it carried before its first call; logs go to `tests/out/selection-eval/`. The MCP lists' guards (core ≤ 12
tools and 2,000 tokens, toolsets ≤ 25, no near-duplicate descriptions) and the skill listing's budget (700
characters a skill, 8,000 in all) are go tests too (`internal/mcp`, `internal/cli`).

## Test databases

`internal/testutil.DB(t)` gives every test its own database (`metarush-test-<ULID>`, dropped after the test) that
looks exactly like a freshly migrated one. Migrating takes seconds (36 migrations: 47 collections, 112 indexes, each
index a file mongod creates and syncs), so a test process migrates only its **first** database and keeps it as a
**template** in memory (each collection's options and indexes, and every document — the migration records — but not
the test marker); every later database is made from the template, sixteen collections at a time, each with all its
indexes in one `createIndexes`. `TestTemplateCopiesMatchAFreshMigration` compares a fresh migration with a copy
(collections, options, every index field, documents) and checks that a copy has nothing left to migrate;
`METARUSH_TEST_DB_FRESH=1` brings the old way back. Measured on a busy development Mac (23 Oct 2026): a full run makes 391 test databases; migrating one took 10.1 s (median of 39 — each process's first),
copying the template 3.1 s (median of 352). The same four packages (catchup, machines, jobs, transcripts) run fresh,
template, template, fresh: 281 s, 161 s, 166 s, 280 s — 1.7× faster, their databases' setup 412 s → 155 s. On
Linux (CI's MongoDB in Docker) migrating is far quicker, so the gain there is smaller.

Each test database carries a marker (`test_run`: process, computer, time). Before its first database, every test
process sweeps (`internal/testutil/testdbs`): it drops the test databases of runs that are gone — a marker naming a
process on this computer that no longer runs, a script's database (`metarush-test-<name><unix seconds>`) or one from
another computer older than a day, an unmarked `metarush-test-<ULID>` older than ten minutes (a late write brought it
back). Only local `metarush-test-*` databases are looked at, and every drop goes through `store.DropTestDatabase`,
which refuses anything else. `go run ./scripts/sweeptestdbs` is the same sweep by hand (listing unless `--drop`).

## Cleaning up after runs

Every browser run and `scripts/acceptance.sh` source one shared library, right after their own exit trap:

```bash
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"   # the run's database, then the folders it makes
```

- On exit — passed, failed, Ctrl-C — after the script's own cleanup, it stops everything the run started that still
  runs (daemons, workers, fake servers and what they started, Remotion's Chromium too: TERM, then KILL after 10 s;
  never a mongod), then drops the run's databases (the one named and any `metarush-test-*` a `config.yaml` in its
  folders names), and removes its folders **when the run passed**. A failed run keeps its folders and prints where.
- A **guard** (the same file run as `guard`, in the background in its own process group) keeps the list of what the
  run started, every two seconds, by process and start time. A run killed outright (no exit handler) is still
  cleaned up by it: its programs stopped, its databases dropped, its folders kept for a look.
- `KEEP=1` keeps everything, still running. Kept folders and kept runs are removed **a day later** by the sweep at the
  start of any later run (`scripts/lib/e2e.sh sweep --now` does it at once) — only folders under the temp folder or
  the checkout's `.agent-home` whose `config.yaml` names a `metarush-test-` database.
- A folder you named (`METARUSH_HOME`, any `METARUSH_…_DIR` or `METARUSH_…_HOME`) is never removed.
- New scripts start background programs with `e2e_bg <log> <command…>` (their own process group).
- Every script ends with `exit 0`. On macOS's `/bin/bash` 3.2, a script with an EXIT trap that `set -u` stops
  on an unset variable reaches the trap with status 0 and then exits 0, so a run that never got to its checks
  passed. `scripts/lib/status.sh` (which `e2e.sh` loads) notes every `exit`, and the exit handler counts a run
  that ended without one as failed. Other scripts with an EXIT trap use it the same way, `scripts/lib/e2e-test.sh`
  checks they all do, and `scripts/check.sh` fails any step whose log says a script stopped on an unset variable.
- **No paid AI service, ever**: `e2e_run` drops every `METARUSH_*_API_KEY` from the environment and any
  Google, ElevenLabs or D-ID address that isn't on this computer, and exports `METARUSH_NO_PAID_SERVICES=1`, so everything
  the run starts — coverage builds and fake servers included, which aren't under `go test` — refuses the real
  addresses as `go test` does. A script that uses the fakes sets their addresses and keys after `e2e_run` (in the
  home's `.env`, or the environment); the library's own test checks it (`scripts/lib/e2e-test.sh`).

What it guards against: a browser script that leaves a `metarush` daemon running after the run (with its worker,
against a database the run has dropped), and runs that never remove their folders, passed or not. The library stops
what a run started and removes its folder when it passes.

The runs it knows about are listed in `$TMPDIR/metarush-e2e-runs` (`scripts/lib/e2e.sh list`;
`METARUSH_E2E_RUNS` moves it). `scripts/lib/e2e-test.sh` checks all of this with stand-in runs (pass, fail, KEEP=1,
killed outright, Ctrl-C, a named folder), in a private temp folder, in about 40 s.

Temp folders tests used to leave in `$TMPDIR` are fixed at the source: a shared motion library's check
(`metarush-motion-lib-*`, ended by `process.exit` before its `finally`), whisper-server's private folder when a
transcription was canceled (`metarush-whisper-*`: now named by the model server's process, and removed by the next
one when that process is gone), the visual tests' helper build (`metarush-vision-test-*`) and the Remotion source
test's copies (`metarush-source-*`). Checked with a private `TMPDIR` around each package's tests.

## Coverage

| What | How | Where it goes |
|---|---|---|
| Go tests | `go test -coverpkg=./internal/... -coverprofile=… ./...`; `scripts/covreport merge` keeps each block once, covered when any test binary covered it | `.coverage/go-unit.cov`; Codecov flag `go-unit` (CI, every push) |
| Go in browser runs and acceptance | `COVERAGE=1`: the script's `go build` makes coverage builds (`-cover -covermode=atomic -coverpkg=./internal/...,./cmd/...` — without the main package a binary writes nothing), every program the run starts writes to `GOCOVERDIR=.coverage/e2e/<run>`, and a daemon writes its counters the moment it is asked to stop (`internal/coverflush`); `go tool covdata textfmt` + merge | `.coverage/go-e2e.cov`; flag `go-e2e` (nightly browser runs) |
| The dashboard's unit tests | `npm run test:coverage -w web` (c8; the parity tests bundle `src/lib/editops.ts` and `words.ts` with inline source maps so they count there) | `web/coverage/lcov.info`; flag `web` |
| The dashboard in browser runs | `COVERAGE=1`: the dashboard is built unminified with hidden source maps (`METARUSH_WEB_SOURCEMAP=1`), every page a browser run opens records Chromium's JS coverage (`web/scripts/coverage/collect.mjs`, loaded with `node --import`; the test scripts are unchanged), and `web/scripts/coverage/report.mjs` maps it onto `web/src` | `.coverage/web-e2e.lcov`; flag `web-e2e` |

`scripts/coverage.sh` merges what is there and prints the per-package table (`.coverage/table-all.txt`). On a development
Mac (23 Oct 2026): the Go tests cover 66.5% of 118,048 statements in `internal/` (98 packages, 46 at 80% or
more; the largest gaps: `cli` 38.7% of 15,786, `remotion` 31.6% of 6,983 — both mostly exercised by browser runs
— `ops` 69.9%, `assistant` 68.9%); one `about` browser run as a coverage build adds 0.2 points (it reaches 6.0%
on its own); the dashboard's unit tests cover 97.9% of the 2,047 lines they load (7 files), and the same `about`
run's pages 19.4% of the dashboard's 53,969 lines.

**The ratchet.** `coverage-baseline.json` holds each package of 100 statements or more (Go tests only: browser runs
differ between machines). `scripts/coverage.sh` fails (and with it `scripts/check.sh --full`) when one drops more
than 1 point; `scripts/coverage.sh --update-baseline` records the new numbers (after adding tests, or when a drop is
right — say why in the commit). A few paths are reached in one run and not the next (`internal/notify` can move a few points:
`ClipLooked` is reached only when a requested look finishes in time), so the baseline is each package at its lowest
of two full runs (`go run ./scripts/covreport baseline -o
coverage-baseline.json run1.cov run2.cov`). Raise it as packages gain tests.

**Codecov** (`codecov.yml`): flags `go-unit`, `go-e2e`, `web`, `web-e2e` (carried forward when a commit doesn't run
them: the browser runs are nightly); components assistant, ops, editspec / cutsheet, api / auth, cli, media /
storage, faces / voice, remotion, web; the project status fails only on a drop of more than half a point, the patch
status wants 80% of new and changed lines; a comment on each pull request. CI uploads through GitHub's OIDC
(`use_oidc: true`, no token secret) and never fails because of Codecov (`fail_ci_if_error: false`). The repository
must be added in Codecov once (sign in with GitHub; the Codecov app needs access to it).

## Writing tests

- Share one app across subtests that only read; each `testutil.DB` is a database.
- Never `t.Parallel()` for speed with databases: mongod is the bottleneck.
- Use the fakes; a test that needs a real service skips without it and says how to run it.
- Floats in golden files: write them from inputs every computer computes the same way (Go fuses `a*b+c` on arm64,
  not on amd64; round a product with an explicit `float64(…)` before adding), and compare with a tolerance.
- A new browser run: copy an existing script's head, give it ports no other run uses, put
  `. scripts/lib/e2e.sh && e2e_run "$DB" <its folders>` right after its `trap … EXIT`, and add it to `browser.yml`'s
  matrix if it needs nothing Mac-only (else to the Mac list above).
