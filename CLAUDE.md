# MetaRush — developer instructions

Read `docs/architecture.md` before structural changes.

## Standing rules
- **NEVER drop or clear the database autonomously.** The only drop in the codebase is `store.DropTestDatabase`, which
  refuses anything but `metarush-test-*` on localhost. Never run destructive commands against Atlas.
- **Never use browser alert/confirm/prompt** — the dashboard uses the styled `useDialog()` provider.
- **Keep README updated as features are added or changed** (overview, feature list, architecture, commands, config), and add a
  `CHANGELOG.md` entry under `## Unreleased` for every user-visible change, led by a plain bold headline (`- **Faces
  are recognized across clips**. …`): Settings → General shows those headlines (D130).
- D-numbers in comments (`D123`) are labels for the maintainers' design log, which isn't part of this repository.
- **Dual use: CLI for agents and power users, web for humans.** Every feature must be controllable from the CLI with
  stable `--json` output; the dashboard is a convenience layer and never the only way to do something (pure
  presentation such as grid vs list view excepted). New operations get a dedicated CLI command; a parity test enforces it.
- Product code stays organization-neutral: no specific organization names, IDs, URIs or credentials in code, defaults,
  tests, skills or examples. Installation-specific values belong in a user's `config.yaml`/keychain.
- Never commit secrets: `.env*`, `.dev/` and generated fixtures are gitignored; check `git status` before committing and
  run `scripts/secret-scan.sh` (CI runs it too). Secret files are never written into sync folders or where git would
  commit them (`internal/secretpath`); test fixtures with fake keys build them at run time or carry `secret-scan: allow`.

## Key architectural decisions
- **One operation registry** (`internal/ops`): CLI, dashboard HTTP API (`POST /api/ops/{name}`) and the MCP
  adapter (`metarush mcp serve`, D95) call the same typed ops; flags `Mutating`, `HumanOnly`, `LocalOnly` enforce
  safety on every surface.
- **Go structs are the schema source of truth**: JSON Schema + TypeScript are generated (`metarush schema export`,
  `npm run gen:types`); never hand-edit `schema/`. Same-named types in two packages need an `opsSchemaNames` entry
  (export refuses collisions).
- **MongoDB holds metadata only; media lives in a separate store** — S3-compatible (Tigris default; R2/B2/AWS/GCS
  interop), native GCS, or filesystem. Objects are content-addressed by SHA-256. GridFS is not used.
- **Edit specs are authored in seconds; Go resolves a frame-accurate RenderPlan** that Remotion renders verbatim — the
  only timing implementation, so preview and render match. AI supplies vetted template IDs and validated params, never code — except project-local motion graphics behind content addressing, an import allowlist, browser-only rendering and human approval per hash (D68); vetted templates remain the default and the safe path.
- **Revisions are immutable**; branch heads move by compare-and-swap; disjoint ops auto-rebase, overlaps conflict;
  locks enforced; `edit check` issues an HMAC confirm token that `edit apply` requires; approval is human-only.
- **Cut sheets (`.cut.md`, narration and mix included) are a lossless projection of the edit spec; JSON stays canonical** (`internal/cutsheet`,
  D46, docs/cut-sheet.md): print→parse keeps the spec hash; saving = minimal ops plan (`editspec.DiffOps`, merge-patch
  updates) through the same check/confirm/rebase/locks; quoted words resolve to exact times when checked.
- **Durable jobs on MongoDB leases** (heartbeats, retries, recovery, idempotency; a lease ends by the database server's
  time, `$$NOW` + `LeaseDuration`, and is compared with `$$NOW`); workers coordinate only via the DB;
  heavy jobs defer on low battery (threshold, override) — never silently. A stopping worker releases its job (attempt
  not counted, never failed); a terminal's `--wait` runs only the jobs it waits for (`LeaseAmong`) and keeps them
  to the end: retries are taken in the terminal, and `waitJobsOpt` restarts a stopped terminal worker or fails
  plainly, never waits forever (D177); a job no computer can run (this one lacks its component, no other running
  MetaRush takes it) isn't waited for: the rest finishes, then `waitingErr` says what waits and the component that
  adds it (`E_TOOL_MISSING`, exit 6, `details.waiting`/`components`/`command`; D340).
- **Claude Code does the editorial reasoning and visual review** (reads contact sheets/frames the CLI extracts); no
  extra AI API calls. Local transcription: Parakeet v2 via sherpa-onnx (English), whisper.cpp fallback.
- **The dashboard assistant runs the user's local Claude Code read-only**: a long-lived process per conversation, resumed
  with `claude --resume` under the same locked-down flags, persisted in MongoDB; people apply its plans and changes (D24, D39).
- **Speaker identity**: diarization runs for every recognizer, `internal/voice` repairs it with local TitaNet
  fingerprints (sherpa-onnx C API via purego) and recognizes people a person confirmed (exemplar profiles; auto-label
  at a high threshold, suggest in between, ask otherwise; stricter while few people are named). Profiles are derived
  only from voices a person confirmed and rebuilt on every correction, so one wrong label cannot spread; giving a
  second voice in a clip the same name asks first (default "no") and can be undone (D35, D36, D60, docs/speakers.md).
- **Catch-up after upgrades**: `internal/catchup` runs named, versioned, idempotent steps (e.g. speaker detection for
  older clips, missing questions) on daemon start and after `migrate` (D61).
- **To-dos** (`internal/todos`, collection `todos`): system-created questions, one open per group key, never re-asked
  once answered/dismissed, closed automatically when the data changes; answering goes through the normal ops; skills
  ask the person instead of guessing; job details, activity and the bell show a clip's open questions with one
  helper (`todos.AboutClip`, `links.Todos`) (D37).
- **Installations are independent homes** (`--home`): config, keychain namespace, cache, state; per-machine storage
  keys and DB users (`metarush machine invite/join/revoke`; `machine rekey` gives an older machine its own key in
  place, people only, never deleting the old key, D122).
- **Dashboard security**: 127.0.0.1 bind, Host allowlist (DNS rebinding), one-time sign-in nonces → HttpOnly cookie,
  CSRF header on mutations, no credentials in the browser.
- **Playing through a database outage** (D180, `internal/playback`, `web/src/player/`): media endpoints look clips and
  renders up with `playback.AssetFor/RenderFor` — the database first (any member while there is no primary; 0.75 s
  when memory could answer, 3 s when not), the memory only when it didn't answer (`X-MetaRush-Record: memory`;
  nothing remembered → a quick plain `E_DATABASE`), always after the Host check and sign-in; `Broker.publish` →
  `playback.Changed` keeps it following the database (Trash and renders not ready never remembered),
  `preview.prepare` remembers the cut's clips; a clip's record holds the fixed copy it plays and the original's set-aside
  variants, so `?media=` (D152, `api/fixed.go` `mediaOf`) reads it too (another pinned copy still asks the database).
  Players (`useMediaRecovery`, `usePlayerRecovery`, `RecoveringVideo`)
  turn a media error or 6 s without progress into a plain line, retries with backoff and a resume at the same
  time/frame (the Remotion preview is unmounted meanwhile). Tests cut the database in-process
  (`internal/testutil/dblink`, `testapp.NewWith`; browser run `scripts/playback-e2e.sh`), never mongod.
- **Remote access over Tailscale** (D128, D129, `internal/remote`, `internal/api/access.go`, `internal/sessions`,
  docs/security.md): the bind stays 127.0.0.1; `tailscale serve` is the only way in. `dashboard.publicURL` (read live
  via `cfg.Remote()`, never `cfg.Dashboard` directly) is accepted only from loopback; `Tailscale-*` headers anywhere
  else are refused; Funnel refused; `X-Forwarded-Proto` believed only there (→ `__Host-mr_session`, Secure). Listed
  `dashboard.tailscaleLogins` sign in by themselves (session bound to the account, re-checked every request; never on
  cross-site requests) and map to `usr_` people (`remote.PersonFor`); handlers act as the request's person through
  `s.appFor(r, via)` (never `*s.App` directly), ops see `ops.Caller.Elsewhere`. MetaRush only reads `tailscale
  status`/`serve status`; `remote enable|disable --apply` runs serve for a person at a TTY; tests use
  `internal/testutil/faketailscale` (the real one is refused under `go test`) and `scripts/remote-e2e.sh`
  (`fakeserve`, an HTTPS stand-in for serve).
- **Activity is computed from server state** (`activity.summary`: uploads, per-clip steps, renders, QA, Claude Code
  conversations, wait reasons from `jobs.Why`); every surface reads it, so reloads and other machines agree.
- **Duplicates are caught before upload** by size + `fp1` fingerprint confirmed by an `sp1` sampled hash; "Keep both"
  records an alias, bytes are never stored twice; the full SHA-256 stays authoritative.
- **Notifications are per person, group-keyed** (one per outcome; refresh re-marks unread only on change); links come
  from `internal/links`, the same helper job details and activity use.
- **Watched-folder and battery settings live in `config.yaml`** (edited node-by-node with `config.SetField`) and
  hot-reload in the daemon; browser upload records live in `<home>/state/uploads`.
- **Plain output for people** (D109, D110): every message, dashboard string and plain CLI line says what it means for
  the video and what to do next, with the glossary in `skills/_shared/voice.md` (version, preview copy, quality check…);
  IDs, tokens, paths, JSON, codes, flags and commands go behind Details / `--details` (on every command: `detailsMode`)
  / `--json`. Errors keep the fixing command in `details.command` (`apperr.WithCommand`); `failErr` and `readyToSave`
  print the plain form. `internal/plainwords` (+ `rules.json`, shared with `web/scripts/plainwords.mjs`) is enforced by
  `TestMessagesArePlain`, `TestPlainOutputSpeaksPlainly`, `npm test -w web` (`test:plain`) and
  `web/scripts/plain-words-e2e.mjs`; exceptions go in the allowlists with a reason. Usage errors about the command just
  typed may name its flags. Dashboard preview media is local-first and same-origin (never a redirect).
- **Narration and the mix are part of the spec**: lines anchor to clips, source moments, the previous line or a clock
  time; Go resolves anchors and per-element gain envelopes (ducking, buses, master gain) into the RenderPlan.
- **Provider API keys (ElevenLabs) are per machine** (keychain or `<home>/.env`), never in MongoDB/config/logs/audit/
  responses and never in argv; voice/model choices are Content Settings (D193) → line, budgets per project. TTS is
  estimated, budgeted and cached by provider+model+voice+settings+text; tests use `internal/testutil/fakeeleven`.
  Narration work runs where it was asked for and is recorded as a job leased to that machine (`Queue.Start`), so the
  activity bar says "Making the AI voice for line 3". Every finished render gets QA (with the speech check) unless
  `--no-qa`. Migrations run every version not yet recorded (merge order never skips one). In the Claude Code panel
  narration/mix changes are server-built action cards (AI voice priced first; `fit --apply` refused on the read-only
  channel); keys, cloning and recording are never offered to the assistant (`internal/assistant/voiceover.go`).
- **Everything MetaRush does goes to the activity log** (`internal/logbook`, `log_entries`, D47–D51): `ops.Call`,
  job events, `internal/tools` runs, provider requests and Claude turns are recorded in the background and redacted
  before anything is written (new secret kinds: register them, e.g. `logbook.RememberSecret`); prompts follow
  `log.prompts` (workspace | local | off); log data is never written into repos, sync or project folders
  (`logbook.CheckOutput`); new operations need a plain phrase in `logbook/describe.go` (`opBase`).

- **Link imports** (D65–D67): `ingest.url` (`metarush import`, Inbox "Paste a link", ⌘V/drop) queues `import` jobs
  pinned to the asking machine (`Job.Installation`; only its worker leases them) that run yt-dlp (typed args,
  `--ignore-config`, link after `--`) and hand the file to `ingest.File` with `Asset.Source` provenance; advice codes
  say what to do; the browser sign-in is opt-in per import (`--cookies-from-browser`, never a cookie file); the
  browser extension uses `/ext/*` with an upload-only token bound to its origin (never `/api`). DRM is refused.
  The daemon's weekly update upgrades yt-dlp only (`BackgroundUpdate`: never Python, Deno or other Homebrew packages;
  a newer yt-dlp that needs them waits for `metarush import --update-tools`).

- **Editing by hand and small edits** (D57–D59): timeline gestures, transcript deletes and `metarush cut
  add|move|trim|split|delete|delete-range|remove-words` are typed ops (`splitClip`, `trimClip`, `slipClip`,
  `deleteRange`, `removeWords`; clip times in source seconds) through the same check/confirm/rebase/locks; the studio
  edits a browser working copy (`web/src/lib/editops.ts`, `words.ts`, golden-tested against Go) that `edit.preview`
  answers and background saves apply (origin `timeline:studio`). `cut pauses` (D236, `cutsheet.Pauses`) shortens long
  pauses between words heard in one clip as word-less `removeWords` stretches (`WordRemoval` kind `pause`), reviewed in
  the Transcript tab's Tighten pauses like the ums.
- **Delivery notes** (D237, `internal/ops/deliverynotes.go`, `internal/assistant/delivery_actions.go`,
  `web/src/components/DeliveryNotes.tsx`): a version's title, other titles, description, chapters and show notes are
  `Revision.deliveryNotes` (outside the spec, like its note; `writtenBy` claude|person), written by Claude Code on the
  person's own plan (the panel's `deliver notes set` card from a proposals file; Auto keeps it) or by a person
  (`delivery.notes.set`, `metarush deliver notes set --from notes.md|.json`); chapters checked against the video (first
  at 0:00), YouTube's limits as warnings; delivery writes `notes`/`notesFile` in the record and `<base>.notes.md`.
- **Captions in other languages** (D238, `internal/ops/captiontranslations.go`, `transcripts/languages.go`,
  `web/src/components/transcripts/CaptionLanguages.tsx`): Claude Code translates (no AI calls of MetaRush's own);
  `captions.lines` (lines, states, the spelling list as `glossary`) → `captions.translate` keeps them line by line in
  `caption_translations` (migration 45: per project, language and line's words, with `place`; `machine` unless
  `--human`); lookups by words, else by place with the fixed words carried when they appear once (`followFix`),
  else `outdated`; `captions.file --language`, delivery's `<base>.<lang>.srt|.vtt` + `captions.translations`,
  `deliver captions` follows fixes; panel cards `captions translate|remove-language` (Auto keeps a translation).
- **Each clip in its own language** (D243, D244; `internal/transcribe/language.go`, `languages.go`, `snap.go`,
  `internal/worker/language.go`, `internal/ops/language.go`, `web/src/components/sources/SpokenLanguage.tsx`): the
  transcribe job decides `asset.language` (`models.SpokenLanguage`, by heard|person|settings) — a person's, else the
  one heard before, else `transcribe.Detector` (`whisper-cli -dl` on two 30 s stretches of speech; another language
  than the workspace's only from 0.75), else `transcription.language` (set: no detection) — and `transcribe.Languages`
  sends English to Parakeet v2 and every other language to whisper.cpp (never Parakeet v3). `footage.language[.set]`
  (`metarush footage language <clip> [--set es]`; in the panel `--set` is a card); QA hears a render in its cut's
  language. Tests that stand in `w.Tx` set `w.Langs = nil`. whisper.cpp's words (D244): DTW starts (`-dtw <preset>
  -nfa`, CLI and whisper-server alike), pause edges from ffmpeg's silences (`snapToSilences`, adaptive `pauses`), a
  gate for other languages' fillers, credits and invented sentences over true quiet, and one piece per token in
  unspaced scripts, joined by `textalign.JoinWords` (TypeScript twins in `video/src/core/text.ts`,
  `web/src/lib/words.ts`). Measured with `scripts/fetch-speech.sh` + `TestMultilingual` (`-tags voiceeval`).
- **Caption looks** (D245; `internal/editspec/captionlooks.go`, `internal/ops/cutcaptions.go`,
  `video/src/captions/CaptionLayer.tsx`, `web/src/components/studio/CaptionLook.tsx`): six vetted looks in
  `editspec.CaptionLooks`, exported to schema/caption-looks.json (renderer, dashboard, QA's caption geometry read the
  same numbers); `captions.style` takes them (no new spec fields: D73 hashes hold); `SetCaptionLook` carries the
  look's words per line; word-timed looks fall back without word timings. `cut.captions` (`metarush cut captions`, a
  checked change and a panel card Auto may run), `captions.looks`; QA `house_style` adds `style_caption_look` (info).
- **Shorts from any footage** (D246; `internal/ops/footageshort.go`, `internal/assistant/short_actions.go`):
  `footage.short` (`metarush footage short <project> --clip <ast> --from --to` or `--piece <ast>@<from>-<to>`) builds
  a new branch (`short`, `short-2`…) whose first version plays the stretches for phones (vertical, platform shorts),
  framed as the house style says (`NewCutDefaults`; follow only when asked, and Auto never runs a short that asks to
  follow, D115), captions in the house
  look else bold, named by `cut.rename`; checked with a confirm token like `cut.framing`; a panel card (savesVersion,
  in `autoOps`); `origin` for other features' runs; `ops.ParseShortPiece` is the one stretch parser.
- **Windows 11 through the browser** (D247; docs/windows.md, `internal/api/attachment.go`, `web/src/api/uploads.ts`
  `isSystemFile`, `scripts/windows-e2e.sh`): downloads go through `attachment(name)` (Windows-safe `downloadName`,
  RFC 6266 with `filename*` for non-ASCII); uploads keep a path's last part; folders leave Windows' own files out;
  the browser run plays Windows (UA, Win32, 1366×768 @125%) in Chromium and Edge when installed (ports 5440–5449).
- **Project motion graphics** (D68–D70, `internal/motion`, `video/render/motion`, docs/motion-graphics-as-code.md):
  `motion/<name>/` components are versioned by the SHA-256 of a canonical archive Go builds; `motion check` scans the
  parsed TSX against the import allowlist, type-checks, bundles with MetaRush's esbuild config and draws test frames
  in the jailed browser, then records the version for a person; `motion approve|revoke` are `HumanOnly`; workers and
  QA refuse unapproved versions; overlays carry `motion: {name, hash}` (cut sheets: `motion name@version` plus a
  `motion:` pin). Every render page is network-jailed (refusing proxy with a loopback bypass list, host-resolver
  rules, CSP, Node download guard; per-frame, total and bundle limits); the dashboard never runs component code.
- **Resources, project links and nested cuts** (D79–D84, `internal/resources`, `internal/intake`, `internal/projectlinks`, docs/resources.md): logos, pictures, SVGs (sanitized), PDF pages and fonts are versioned, content-addressed resources with short names; one classifier routes everything added (`add.route`, the dashboard Add box, `metarush add`); linked folders keep a shared record and a per-machine path (config.yaml `resources.folders`, the daemon's scanner); cuts pin resources by name to a version (`spec.resources`, params marked `format=metarush-resource`) and renders serve them by hash from the worker's loopback media server; the brand kit is a Content Settings part (D193) and seeds new cuts; projects use each other's things by reference (`project_links`, whole or filtered, kept when a cut uses them); a nested cut is a clip with source `cut:<project>/<revision>` that Go resolves into `RenderPlan.nested` (sound flattened into the outer mix); `asset.delete` is people only and refused while anything uses the footage. The visual index tags pictures and reads their words through `resources.RegisterTagger`/`RegisterTextFinder` (`internal/visual/still.go`, Mac only); later features plug in through those, `RegisterURLSource`, `editspec.ResourceParamsInSchema` and `ops.RegisterDeleteHook`.
- **The Library: collections, the Archive and a two-step Trash** (D96–D98, `internal/ops/library.go`, `collections.go`, `trash.go`): the Inbox is computed (`ops.InboxFilter` / `Asset.InInbox`: no project, collection, Archive or Trash); collections are their own documents (`collections`, `asset.collectionIds`) and a project link's source can be one (`projectlinks.IsCollection`/`SourceFootage`); Move to Trash moves the clip's memberships into `asset.trash` (Restore puts them back; refused while another project has it or any latest cut uses it); `trash.empty` and `asset.delete` are `HumanOnly` and share `deleteCheck`; nothing empties itself unless a person sets `trash.configure` (`RunTrashSchedule` claims each clip).
- **Repositories, web pages, slides** (D104–D108, docs/resources.md, docs/security.md): GitHub repositories and web pages are `fetch` jobs pinned to the asking machine (`internal/resources/repo.go`, `site.go`); git runs with hooks, prompts and other protocols off and blobs are read raw (nothing from a repository ever runs; the person's own git/gh sign-in, no token stored; tests use `scripts/make-fixture-repo.sh` + `METARUSH_GIT_BASE`); snapshots run `render/snapshot.mjs` in Chromium whose only way out is `render/web/proxy.mjs` (refuses this computer and local networks, also inside IPv6 forms; `METARUSH_PAGES_ALLOW_LOCAL=1` only in tests; `render/web/test.mjs`); code slides, diagrams and cards are generated resources drawn in the render jail (`render/generate.mjs`) with `repo@commit:path:lines` provenance and how it was made (`source.madeWith`; `resources make`, refreshed with the repository); six vetted `slide.*` templates; motion graphics draw resources only through `useResource` with a quoted name or a marked parameter (scan + `motion check`, cuts pin them); the extension's `pages` scope (`/ext/pages`) is granted only at pairing; in the Claude panel `resources add|make|refresh` are action cards (`internal/assistant/resource_actions.go`). Skills treat repository and page text as untrusted data (`skills/_shared/safety.md`).
- **Pictures kept from a repository's folders** (D216, `internal/resources/repopics.go`, `internal/ops/respictures.go`,
  `internal/recipes/repopictures.go`, docs/resources.md): a repository's `picturePaths` (`.` = all); every fetch of the
  commit it shows reads the blobs under them raw and keeps files whose **bytes** say PNG/JPEG/WebP/GIF/SVG (SVGs
  cleaned) through `AddFile` with `repoFile` as pictures next to the repository (`repo@commit:path` provenance; a newer
  commit → a new version of the same picture, a file gone → `missing`, never removed; LFS pointers, links, animated
  GIFs and files past `resources.repos.pictureMB|picturesMB|pictures` left out with the reason); each repository
  version indexes `repo.pictures`/`picturesLeftOut`; `resources.pictures[.set]` (`--keep|--drop`: a pictures-only
  fetch at the commit shown, no new version of its files); migration 40: one picture per `repoFile` (unique). A
  recipe pictures input `from: {repo, path}` takes them at the commit shown, each file keeping its place, and records
  `commit` and hashes.
- **Picture and sound at cuts** (D52–D56): `Timeline` places the picture, `resolveSound` each clip's sound (split edits, blends, auto pauses); new cuts get `mix.levelSpeech`/`cutBlend`, older cuts sound as before (golden test). Cut sheets spell them on the `↳` line (`cutsheet.TransitionClauses`); a changed cut is one `setTransition`.
- **What MetaRush sees** (D71–D74, docs/visual-index.md): evidence finds cuts, slow changes and movement (`visualChanges`, `footage moments` kind `visual` with frames to pull); sheets are smart by default (per shot, changes, new speakers, coverage; tiles labeled time · speaker · words); on a Mac `metarush-vision` (Swift, source embedded, built by bootstrap, doctor or the worker on first use; allowlisted) indexes ~1 fps with Apple Vision (heavy job `visual` → insight `visual` (`vision-2`) + `faces`; elsewhere `catchup.ErrNotHere`); framing follows faces only where the spec says so (new cuts get `output.framing: "faces"` from `NewCutDefaults`; a clip may say `framing.auto: "faces"`; cuts without it render identically once indexed, legacy golden); scene descriptions are opt-in, `HumanOnly` to turn on, and run the person's Claude Code read-only.
- **Faces across clips, following movement, on-screen QA** (D114–D118, docs/visual-index.md): faces are fingerprinted
  with SFace (Apache-2.0, downloaded on first use, SHA-256 pinned) through onnxruntime (`internal/onnx`, purego,
  shared with the clarity score; `internal/faceid`) after the helper's five landmarks align them; recognized by the
  voices' rules (`internal/faces`: only confirmed faces enroll, `recognized` ≥ 0.60 with a 0.15 lead, questions ≥ 0.38,
  `Refresh` re-scores the library on every correction, odd confirmed faces asked again; measured with
  `tools/vision/eval/faces.py --metarush`: precision 1.00). **Asked once per person, not per appearance** (D155–D159,
  `internal/faces/ask.go`, `groups.go`, `shown.go`, `said.go`): unnamed faces of a clip are grouped (fingerprints at
  ≥ 0.45 average linkage, then the voice heard while alone on screen; `faces.group` = `fgr_…`, carried over on
  re-index); one `face_group` to-do per group whose person speaks (≥ 1 s on screen, one open per person per clip, cut
  first), suggestion from the voice, looks, names shown (`names.FromScreen`, tile/lower third/title under or near the
  face) or said (transcript) — evidence only, nothing learned before a person answers; `faces.NameGroup` confirms the
  faces that fit (≥ 0.40, `confirmedVia: group`, six clearest teach) and sets the rest aside (`NameGroups`: all of a
  clip's, then one re-score); saving a cut re-marks "In your cut" (`faces.MarkCuts`); ops `todos.groups`,
  `todos.acceptShown`, `todos.answer` `sameAs`; catch-up `face-questions` v1 folds old questions. **Evidence that
  holds** (D189): the voice cue needs 6 s (`MinSeconds` = `models.VoiceCueSeconds`; 2 s `SpeaksSeconds` only says
  someone speaks) and never asks on its own — "Is this X?" needs looks or a name shown (`suggestion.strong`), else
  "Who is this?" with quick picks; looks names are never blocked by the voice; looks-named faces count in "the voice is
  someone else's"; pronouns are never said names. Suggestions carry `basis`, answers record `Todo.guesses`
  (`todos.Grade/Record/Scored`), `todos.Scores`/`Muted` (≥ 10 answers, < 80% right: no longer suggests), `CueOff`
  (two "not X" in a clip on X's voice); bulk ops `todos.notPerson`, `footage.faces.notHere` (`Asset.NotOnScreen`),
  `todos.skipFaces`, `people.evidence`; catch-up `person-questions` v1. Index frames are
  taken at their moments (`fps=…:round=up`); a fingerprint unlike the rest of its face's (< `PrintKeep`) is never
  used (older indexes held the next shot's face at a track's end). `people.learned` /
  `people.unlearn` (D158) show and take back what a name was learned from (the comparison dialogs; `ConfirmNeeded`
  details carry person and voices). `framing: "follow"` (output or clip, only a person's
  choice; `cut.framing`) resolves to a keyframed `framing.path` (`FollowPath`: main face, else attention; jumps only at
  the footage's cuts); `editspec.CropRect` mirrors the renderer's placement for QA's `on_screen` check (qa-5: faces cut
  off, captions/graphics over faces, captions over footage text; observed on rendered frames on a Mac; fixes as ops).
- **Voiceover studio** (D62–D64): every action on the Voiceover tab is one small checked plan from the same ops as the CLI (`voiceover.line`, `mix.set`; `mix.preview` tries a mix without saving); dragged or typed times re-anchor conversationally (`ReanchorAt`, and its browser twin in `web/src/components/studio/narration.ts`), exact time only on request; the timeline's narration lane moves lines with `moveBeat` through the working copy (save in background, undo/redo); a line's room runs to the next line in time. The browser recorder saves WAV takes through `/api/uploads` + `voiceover.addTake` (never offered to the assistant). Preview plans carry `sourceChannels` and the preview plays mono sources −3 dB like the renderer (`web/src/player/compensate.ts`).
- **Live takes and script versions** (D85–D87): a live take is placed where it was spoken (`voiceover.place`: kept whole, cut only in pauses by `editspec.SplitRead`, each line pinned with `PinAnchor`, contiguous pieces play as one element), never moved to planned spots unless the person presses Snap; the browser syncs by measurement (`capture.ts` `takeStart`) and the teleprompter schedule is `prompter.ts`; a re-recorded stretch trims the line before only where the new sound begins. Scripts are used as-is (`ParseScriptAsIs`); Claude's fit is a card (`voiceover.askFit` → `voiceover.fitted`), `voiceover.original` is never rewritten, `active`/`parked` switch with `voiceover.use`. Tests: `scripts/voiceover-live-e2e.sh` (fake microphone + witness).
- **Sound** (D88–D91, `internal/sound`, docs/sound.md): the Sound tab and `metarush sound …`; a person's level and clearer voice are spec settings (`mix.voices`) applied over their speech only; clearer voices are hidden copies (`mix.enhanced`, plan `variant`, tag `enhanced`), never edits, content-addressed in `sound_cache` (nothing processed or billed twice); clean = DeepFilterNet + polish (set up on first use), studio = Resemble Enhance (people-only setup) with every word checked by the local recognizer, cloud = ElevenLabs isolation priced and budgeted first (tests use fakeeleven only); results are scored (DNSMOS, background, likeness) and said in plain words — never "LUFS", "ducking", "de-ess" or "DNSMOS" in UI copy; music `fit: auto` with `ending: phrase` ends on a phrase, else a bar line, else a beat from the whole-track music grid
  (evidence `beats-2`, D120; tracks without `ending` keep `beats-1`); studio loads its model once per run and picks a
  CUDA build on Linux (`pickTorch`, D121), `duck` per track. Tests: `scripts/sound-e2e.sh`, `METARUSH_TEST_SOUND_HOME` for the component tests.

- **Screen recordings made in the dashboard** (D258–D262, `internal/recorder`, `internal/api/recordings.go`,
  `web/src/screens/Record.tsx`, `web/src/screens/record/`, docs/recordings.md): the browser records the screen
  (`getDisplayMedia`, this tab never offered), camera and microphone as separate files (MP4/WebM in 2 s pieces with a
  keyframe each — Chromium's MP4 recorder writes a piece only at the keyframe ending it and holds a write made right
  after another, so it gets a 1 s timeslice and `requestData()` every 0.5 s, `formats.ts` `pacing`, D342; the voice
  16-bit from D63's tap) and sends each piece at once to `PATCH /api/recordings/{id}/tracks/{t}`
  at the host's offset (`HEAD`), kept in IndexedDB until the host has it; pieces wait in `<home>/state/recordings/<id>/`
  on the receiving installation (`recordings`, `rcd_…`, migration 47) until `recording.finish` makes footage (WAV,
  ffmpeg remux, ingest; detached from the request; claimed once with a renewed lease that a later finish, discard or
  the daemon's recovery takes over when stale; `--keep` after a closed tab). Only the maker or an owner ends one that
  isn't footage; limits `recordings.maxGB|minFreeGB|keepDays` (`Sweep` expires untouched pieces after a warning). One clock
  (`clock.ts`): recorders start together, the voice's start and drift from block arrivals, pauses drop the same
  stretch everywhere; the clock goes to the host while recording. Linked into a project it is a conversation
  (`conversation.FromRecording`, members `screen`/`camera`/`voice`, method `clock`), lined up again by the
  microphone's copy in the pictures when sure or within 0.15 s of the clock (`RefineBySound`); `recording.cut` is
  `conversation.LayoutCut` (one pip/side-by-side/member shot). Teleprompter: `prompter.ts` schedule on the recording
  clock, optional document PiP window. Opening a camera/microphone has a 9 s deadline (`web/src/lib/deadline.ts`,
  shared with the narration recorder). Tests: `internal/recorder`, `web/scripts/recorder-test.mjs`,
  `scripts/recorder-e2e.sh` (ports 5480–5489; fake devices asked for by ID, a canvas screen; pieces of every track
  must reach the host while recording, a picture piece at least every 3.5 s, before the tab is closed).
- **Conversations from online calls** (D99–D103, `internal/conversation`, `internal/editspec/conv_*.go`,
  docs/conversations.md): a conversation is a record (`conversations`, `cnv_…`) plus a pin in the cut
  (`spec.conversations`) and clips with source `conv:<name>` and `shots` — footage to the engine (`wrapFacts`), the
  picture switching while every member's own track plays (member audio elements with the gate); recordings lined up by
  onset cross-correlation (two-way pick-up at the midpoint, drift from windows); whose words are whose decided by each
  track's loudness; `sampleAligned` pins place members to the sample (`trimFraction`, drift copies the render worker
  resamples, D119); layouts' geometry in Go (`LayoutTiles`, matched faces) drawn by `ConversationPicture`; the automatic
  cut, clean-up (suggestions people accept or skip), webcam match, imports, moments and shorts are ops with
  `metarush conversation …` (parity test); QA checks voices, the picture and the calls' glitches (qa-4).
- **Panel cards for calls and sound; the shim outside the working folder** (D111): Claude's refused `conversation
  cut|decide|angle|short` and `sound levels|enhance|music add|music set` become server-built cards
  (`internal/assistant/conversation_actions.go`, `sound_actions.go`), previewed by side-effect-free twins of the ops
  (`conversation.Preview*`, `sound.Preview*`: nothing saved, processed, spent or remembered); cloud voices priced
  first and budget-checked, studio's people-only setup carried on the card (`AssistantAction.SetUp`). The shim is
  `<home>/assistant/shim` (0700, first on PATH, `Edit(<shim>/**)` denied, rewritten every launch) — never in Claude's
  working folder. Fake-claude browser run: `scripts/cards-e2e.sh`.
- **The panel's plain words, safe steps, finish notes, questions, combined approvals** (D140–D144, docs/assistant.md):
  step lines are written in Go (`labels.go`) and grouped from `ToolCall.Group`/`Many` by both the CLI printer
  (`internal/cli/assistant_print.go`) and the dashboard; clips/versions/projects are named only through
  `names.go` (`displayName` — the place a later build's descriptive names plug in); notes keep Claude's text in
  `Turn.Prompt` and the person's line in `Turn.Shown`; older conversations are cleaned when shown (`plainEvents`,
  `plainCard`); `plainwords.CheckPanel` (+ `rules.json` `panel`, `checkPanel` in `web/scripts/plainwords.mjs`)
  guards panel text. Safe steps (`assistant.safeSteps`, default on, the master switch for Auto too; `autorun.go`) run motion checks of the
  conversation's graphics, review renders and links the person typed in the conversation as that person with
  `Via: assistant-auto`, never on Claude's channel. Cards record the async work they start (`AssistantAction.Work`:
  render + QA, jobs); `work.go` polls it on the holding machine (resumed after restarts via `watching`) and
  `tellClaude` (`notes.go`) tells Claude once — cards decided together once their work is done, other outcomes
  debounced, queued notes merged; no note when work merely starts; news not yet told survives restarts (`untold`),
  cards decided by older builds carry no `batch` and count as told. Only the daemon's manager (`assistant.New`) runs
  safe steps, watches work and tells Claude in the background; terminal commands use `assistant.NewTerminal` (and
  `For` makes a terminal one), which never act on conversations in the background — their next message refreshes
  the work and takes the news along. Questions are `<proposals>/*.question.json` →
  cards answered through `assistant.act` (`answer`) as the person's reply; `assistant.act` takes `actionIds`
  (approvals first, one note) and `reviewCopy` (render.submit review of the saved version). Tests keep safe steps
  off in `testApp`; `finishRender` plays a worker finishing a render and its QA.
- **Conversations people share** (D179, `internal/assistant/whose.go`, docs/assistant.md): `assistant.list` with a
  project is everyone's, without one the asker's (`allUsers`/`mine` force; `others` counts the rest); the panel's
  list follows the project in view with a remembered Everyone / Just mine. Every message has its sender
  (`Turn.userId`; a note: the latest decision's person; older turns: the starter's), a card is for its turn's
  sender (`forId`, computed) and counts only for them (`Conversation.waitingFor` stored per person →
  `waitingForYou`, the badge, the navigator, the turn's notification and audit); anyone may decide it (`byId`).
  Present conversations through `present(…, uid)` / `m.snapshot(ctx, c)` so these are computed for the asker.
- **Whose Claude answers** (D239, `internal/assistant/account.go`, `web/src/layout/assistant/WhoAnswers.tsx`): the
  probe reads `claude auth status --json` loosely into `AssistantStatus.account` (`ClaudeAccount`: plan, terms
  consumer|commercial|cloud|unknown, `who`, `dataUse`); each message stores it on `Conversation.account`; the thread,
  a new conversation, Settings → Claude Code (**Whose Claude answers**, readable by all) and the CLI say it. Never
  assume terms Claude Code didn't report.
- **The receipt** (D240, `internal/receipt`, `web/src/components/project/Receipt.tsx`, docs/receipt.md):
  `project.receipt` reads every `generations` document generically (any `kind`, fields loosely: new kinds of AI work
  appear without changes), AI voice takes, cloud clean-up wets, Claude's conversations (whose account, billed only
  with an API key), the log's provider requests, generated media and `auto_spending`; cost at cost by service, per
  project and per version (`cost.version`: what its cut uses); delivery records carry `receipt` via
  `ops.DeliveryReceiptFor`.
- **Usage and memory** (D181, `internal/assistant/usage.go`, docs/assistant.md): a conversation's limit is tokens
  (`tokenLimitMillions`, default 50M), counted per reply as it streams (once per reply ID; a message at the limit is
  interrupted) and at each message's end as the change in Claude Code's session totals (`modelUsage`, which carry
  across resumes and include `/compact`'s own tokens — never sum per-reply usage and totals). No dollar cap on a
  Claude plan (`--max-budget-usd` only with an API key). Past `compactAtTokens` MetaRush sends `/compact` as a turn
  of its own right after a reply (`Turn.Compact`); `compact_boundary` sets the context. `holdNews`: only urgent news
  (safe steps that ran by themselves, failures) starts a note; outcomes are marked told only once sent. Tests run
  with `HoldNews` off (their note turns); `TestNewsWaitsForTheNextMessage` covers holding.
- **One project per conversation, the project so far, what's new** (D185, `internal/assistant/manager.go`
  `scopeContext`/`convContext`, `sofar.go`, `whatsnew.go`, `web/src/layout/assistant/follow.ts`, docs/assistant.md):
  a conversation keeps the project it started in (never re-scoped by a message's page; workspace ones stay so); every
  message carries that project's context, the page's selection only on its own page, another project as a one-line
  hint; MetaRush's own messages use `convContext`. The panel follows the project in view (per project, per tab). A
  conversation starting in a project gets `project.sofar` (bounded, newest first; read-only op on Claude's list,
  carried into `/compact`). `Conversation.metarushVersion` → the first message on a newer build gets the CHANGELOG
  headlines between versions, once.
- **Not quite, Not now that waits, turned-down notes, Auto** (D186, `notquite.go`, `turneddown.go`, `automode.go`,
  `internal/ops/turneddown.go`): `assistant.act` `notQuite` marks a card `not_quite` (who/when/note) and sends the note
  at once as the person's message; Not now tells Claude to wait. `revision.approve`/`motion.approve` rejections with a
  note go through `ops.OnTurnedDown` to the conversation that made it (origin, else a done card naming it) as that
  person's message — sent by the holding daemon, else left in the conversation's `inbox` (never `$set` by `save`;
  taken atomically, sent by the daemon's loop or before the next message); `toldClaude` says where. Auto (per person,
  `users.assistantMode`, `assistant.mode[.set]` people only) runs `autoOps` cards by themselves as the card's person
  (`startAuto`, `runAuto` through `runCard`; confirm tokens like a terminal — only `Via: dashboard` skips them); priced
  (unless the card's person turned estimates off, D208: `pricedStep`, `AutoPriced`; and only where a person set the
  project's budget and the team's daily limit with every service it spends with, D241: `autoMaySpend`,
  `ops.RegisterSpendingLimits`, `autoWaits`, who set them in `budgetBy|dailyCapBy|budgetCreditsBy`; Auto's spending in
  `auto_spending` → the receipt and one `auto_spent` bell note a day), final renders, approvals, setup,
  questions, keep cards and HumanOnly always ask; `assistant.safeSteps` is the master switch. Panel times use `stamp()`.
- **Accounts and sign-in** (D183, `internal/auth`, `internal/api/signin.go`, docs/accounts.md): the workspace's
  method (`auth.Method`, cached 5 s) is single | password | tailscale; `signedIn` honours it (Tailscale headers count
  only in tailscale; link sessions = the owner, local only; password sessions check `sessionsValidAfter` through a
  0.75 s lookup with a 10-minute fallback so D180 playback survives outages). Never store or log a password outside
  `user_credentials` (argon2id), never read one from a CLI flag, never answer differently for unknown emails.
  Owner-only ops use `ops.Flags.OwnerOnly`; the method changes only at the host. `auth reset` keeps every record.
- **Owners change this computer's settings** (D191, `internal/ops/owner.go`, docs/accounts.md): `OwnerOnly` covers
  people and sign-in, remote access, storage and the Trash, keys, Claude Code's settings, the watched folder and link
  imports, the log and the weekly report, battery, machines and clearing archived projects out (D196) (`internal/cli`
  `ownerOnlyOps` is the list; a test keeps it whole; extension pairing left it with D197); ops with one owners' path (the workspace's choice) call `ops.RequireOwner`; HTTP
  handlers outside the registry use `s.ownerOnly`. Every signed-in request carries its person (`ops.OverHTTP`, role
  looked up lazily: `auth.SignedInOwner`, link sessions = owner), so handlers and ops calling ops are held to it;
  `Caller.Remote` = any HTTP; no HTTP request behind a call (CLI, MCP, daemon) = the owner. The check precedes
  LocalOnly/HumanOnly ("Only an owner of this MetaRush can change that.", `details.owners`). The dashboard reads
  `api.Session.owner/owners` and wraps owner-only controls in `OwnerOnly` (a disabled fieldset + one "Only an owner can
  change this. Owner: …" line, `components/OwnerOnly.tsx`); reading stays open.
- **Deleting a project archives it; owners clear archived projects out for good** (D196, `internal/ops/projectdelete.go`,
  `purge.go`, `purgemedia.go`, `web/src/components/project/ProjectDelete.tsx`, `/projects/archived`): `project.delete`
  (people only, any member) sets `archived`/`archivedAt`/`archivedBy` (`project.update` `archived` is the same, people
  only); `project.restore`; archived projects leave the lists and search, their footage stays in the Library, their
  conversations read with "… is archived — Restore" (new messages refused), links land on `ArchivedProject`.
  `project.purge` (owners, people; `dryRun`, `forever`, `allArchived`) clears archived ones only: versions, renders
  (stored files only when nothing else uses the same bytes: every collection is in `purgeCatalogue` with how it uses
  stored files — add new collections there, a test keeps it whole; `RegisterMediaUsers` for files only a stored
  manifest names), checks, conversations (`RegisterPurgeHook`; folders elsewhere swept at daemon start; a
  conversation's record is inserted only by its first save, so a purged one never comes back), kept things, comments,
  own to-dos and notifications, links both ways, Content Settings (`content.ProjectGone`), Remotion projects. Footage
  is never deleted (Inbox, or joins a project whose cut uses it through a link), pictures move to the workspace library.
  Refused while another project's cut nests one of its cuts; `purging` marks it, every step can run again.
- **Browser extension pairings are each person's** (D197, `internal/browserext`, `auth.MaySend`, `internal/api/extension.go`):
  anyone pairs their own browser (`extension.pair` people only; the host's CLI pairs for the owner); codes wait per
  person; `Browser.userId` is credited for what it sends (the request's person); `extension.status` yours (owners:
  `everyone`), `extension.revoke` yours, `all` yours, others' or `everyone` (owners, `ops.RequireOwner`). Every `/ext`
  request checks the person cheaply: turned off, removed, not joined (or not the owner under "Just me") → 403, token
  kept. State file version 2: older pairings are the installation's owner's (`browserext.Of(a)`).
- **Where things are in the dashboard** (D184, `web/src/layout/AccountMenu.tsx`, `web/src/screens/settings/`): the bar
  is Studio, Library (tabs incl. People, Motion, Recipes and Abilities), Renders, To-dos; Settings sections are routes
  `/settings/<general|people|preferences|claude|footage|extension|storage|components|integrations|log|cli>` (preferences: D208, light/dark
  moved there from General; extension: D191; its old
  Footage anchors redirect through `movedPart`; Brand left for Content Settings, D193: `/settings/brand` and
  `/settings/content` open the default's) opened from the account menu (avatar
  when sign-in is on, a menu button when not). Links to pages go through `internal/links` (Person → `/library/people`,
  Log → `/settings/log`, a project's Content Settings → `links.ContentSettings`); user-facing text names places as "Settings → <Section>"
  (→ <part>). Old anchors and pages redirect — keep the redirect map when renaming a section. Avatars are 256 px PNGs in
  the media store (`account.avatar`, `/media/avatars/{user}`), never data URLs in `users`.
- **A review copy follows the latest version; the Edit tab shows the real thing** (D187, `internal/ops/follow.go`,
  `web/src/components/studio/ReviewCopy.tsx`): only daemons (`ops.RunReviewFollow`, never terminal commands) queue a
  review copy of each cut's head once it has been quiet `ReviewFollowQuiet` (1 min) and was saved within
  `ReviewFollowWindow` (2 h); the job key is render.submit's (`render:<rev>:<preset>:review`), so several daemons never
  double it and a key ever used (failed/stopped included) is never re-queued by itself; graphics nobody approved mean
  no copy; a newer version cancels older follow copies of the cut only while `queued` (`Queue.CancelQueued`, never a
  leased job). Follow copies: heavy, `Render.follow`, made `via: worker` for the version's saver, bell only on failure
  or QA problems; `workspace.reviewFollow` via `render.follow.show|set` / `metarush render follow`. The Edit tab and
  the version page switch between the live preview and finished renders (`CutPreview.playRender`, D180 recovery).
  Preview plans (never renders) carry `motionStills`; `MotionOverlay` shows a checked graphic's nearest test frame
  from `/media/motion/{hash}/still-{n}` (backdrop keyed out by `motion.Still`), unchecked ones keep the placeholder.
- **One set of words; awaiting review; dates** (D188): tab **Edit** (URL `cut`), **review copy** (`RenderLabel`),
  versions **Draft / Approved / Needs changes**; `review.waiting` (`metarush revision waiting`) lists heads that are
  drafts Claude made (`madeByClaude`: `assistant:` origin or via assistant/assistant-auto/cli/mcp, never `timeline:`)
  or someone asked about (`Revision.reviewAsked`: a person's review-copy request in render.submit, or
  `revision.askReview`); dashboard dates are `stamp()` as text with `absolute()` on hover.
- **Moving the media from the dashboard** (D112, `internal/ops/storage_dashboard.go`, `internal/api/storage.go`):
  Settings → Storage writes a storage description from a form (typed text taken as written; an S3 secret once to the
  keychain), plans with `storage.plan`, and after a styled confirm starts `metarush storage migrate --dashboard-move
  <id>` with `tools.Start` (own session, survives the daemon's restart; reaped by `ops.ReapDashboardMoves`) and a
  one-time grant on stdin (SHA-256 kept, 2 minutes, `O_EXCL` claim). `storage.status` / `metarush storage status`
  drive the "this computer still uses the old storage" prompt and the Settings tab's dot. Never deletes. Browser
  run: `scripts/storage-e2e.sh` (two installations).
- **People's own words are theirs** (D113): `<Theirs>` (`data-user-content`) in the dashboard, curly quotes
  (`theirs()`, “…”) in plain CLI output and in MetaRush's own sentences; `plainwords.CheckShown` / `checkShown` skip
  them except for IDs and leaked tokens (shared vectors in `internal/plainwords/testdata/theirs.json`).
- **Generative video** (D123–D127, `internal/videogen`, `internal/generate`, docs/generative-video.md): providers with
  models and dated prices as data (`catalog.json`; Gemini Omni via the Interactions API, its key in `internal/providers`);
  never automatic — every request is priced and sent only with `maxUsd` ≥ the estimate (or, for a person whose
  estimates are off, D208, the estimate itself), reserved against the project
  budget and the team's daily limit (`generation_spend`) and settled; a `generate` job pinned to the asking machine saves
  the ticket before polling (a restart resumes, never resends) and deletes the interaction and files at Google after the
  download; `generations` are write-once (`setOnce`); a likeness only through an approved look with consent (`looks`,
  `consents`, HumanOnly; children and public figures refused; names in prompts and faces in frames checked first,
  `E_CONSENT_REQUIRED`); clips carry `Asset.Source` platform `generated` and are flagged everywhere (QA note, delivery
  sidecar with a per-platform checklist). Tests use `internal/testutil/fakevideogen` only (`scripts/generate-e2e.sh`).
  Google's free tier (a 4xx naming it, or a 429 with a zero limit; error bodies read loosely) is `KindTier`: never
  retried, failed at $0, cancelled and deleted at Google; other 429s while polling postpone after 10 min and give up
  after two postponements; the key check never promises video (D178). **Veo on Vertex AI** (D282,
  `internal/videogen/vertex.go`, `internal/providers/vertex.go`): provider `vertex`, chosen by model
  (`vertex/<model>`; `generate` `plan.provider`, budgets unchanged); predictLongRunning + fetchPredictOperation, no
  bucket (video inline), priced by the second (`perSecondUsd`), 4/6/8 s (`secondsAllowed`); the "key" is a service
  account's key file (`KeyShaper`: checked, trimmed, kept like any key; `--key-stdin` only), RS256 token requests only
  to oauth2.googleapis.com, tokens in memory; tests use `internal/testutil/fakevertex`.
- **AI services: one contract per kind of work** (D224–D228, `internal/aiservices`, `internal/aiwork`, docs/ai-services.md):
  pictures, speech, voice copies, voice change, sound effects, music, dubbing and cloud transcription are
  `aiservices.Adapter`s per provider and kind (`aiservices/google` on the Gemini key, `aiservices/elevenlabs`), each
  passing `aiconformance.Run` against its fake (`fakevideogen`, `fakeeleven`; lifecycle tests use `testutil/fakeai`);
  models, dated prices, retirement, licence and data terms are `aiservices/catalog.json`. `ai.estimate|make` (CLI
  `metarush ai …`) choose the model in the open (asked, Content Settings' `aiServices` part — none / forbid / pin per
  kind, over every project the work touches, generated video too, via `generate.RulesAllow`/`TouchedBy` — or the usual one) with up to two alternatives,
  check consent (D227: voice copies people only with a consent record, their named voice in the sample and Google's
  script read; own-voice dubs need every speaker's consent; withdrawing consent deletes voice copies through
  `generate.OnConsentRevoked`), price against generated video's budget and daily limit (`generate.ReserveWork`), keep the
  bytes sent and queue a `generate` job with `payload.kind` that `aiwork.Run` handles: ticket before waiting, results kept
  with how they were made (pictures → resources with `madeWith` kind `ai`; sound and dubs → clips with
  `source.generated.kind`; transcripts → the clip's transcribe job with `payload.generationId`), deleted at the provider
  where its API allows (`forgotten.kept` when not). Records are `generations` with a `kind` (absent = video; video's
  lists keep to video). Never call a real service in tests.
- **Presenters** (D273–D277, `internal/aiservices/presenter.go`, `aiservices/did`, `internal/aiwork/presenter*.go`,
  `web/src/components/generate/Presenters.tsx`, docs/presenters.md): kinds `presenter` (a presenter reads supplied
  sound: a clip or a cut's narration `--line`; MetaRush's own sound put back under the picture) and `presenter-make`
  (a person's presenter at the service; `aiservices.PresenterMaker`: consent words, delete, find by `[mr-…]` tag;
  `LinkTaker`: D-ID downloads the take and the reading from a two-hour presigned link, a loopback link server only for
  a provider on this computer). Records `presenters` (`prs_`, waiting → training → draft → approved, or failed /
  retired) and `training_optouts` (migration 51). People only: `presenter.start|make|test|approve|retire|optout.set`;
  a make needs a consent in force that covers presenters (`Consent.uses`), names the service (`providers`) and is a
  recording (own too; D274), the reading checked with D227's script check, a take where only the person is heard and
  seen, the look approved and unchanged, the opt-out recorded where `data.trainsOnUploads` is `unless-opted-out`
  (D276; `trainingGate`: every named person's face or voice sent there, each with their consent naming it); the
  consent recording filmed, its voice and faces named as the person, the reading in it; faces in the take named
  once looked (the person's word only before); a person's presenter speaks only their own voice (every voice heard
  named as them, or their copy) or one that is no one's (`whoseVoice`); `stillAllowed` re-checks No presenters, the
  presenter, the opt-out; lost answers are `uncertain`, looked up by tag (`TagCleaner`) for ten minutes and deleted;
  only names a person gave count (`models.Naming` on faces and voices, stamped via `internal/naming` from `ops.Call`;
  `NamedByPerson()`), for D126/D227 and lip sync (D279) too; a consent that can't be read (`E_DATABASE`, `generate.Unreadable`) never
  retires or throws anything away; the make claims the presenter first; a consent's last day ends in its own
  `expiresZone`; stock only
  where `presenters.stock` covers commercial and client work (D275; D-ID's unstated: none). Retire / consent
  withdrawn → `retired`, deleted here or by a `remove-presenter` job (any computer with the key, tried until done),
  clips waiting stopped, cuts listed (`asset.where`), the making's generation `withdrawn`/`removed` (D277). Disclosed
  in QA (`GeneratedClip.Presenter`), the sidecar (`presenter`, `consent`, no watermark), the receipt. Content Settings
  `aiServices.forbidKinds` (`ai rules set --forbid-kind presenter`). In the panel `ai make presenter` is a priced card;
  every presenter step is never a card. `METARUSH_D_ID_BASE_URL` (D-ID's host or loopback; loopback only under go test
  and browser runs); HeyGen (`aiservices/heygen`, v3) reads stock presenters only (an `Idempotency-Key` per clip,
  `METARUSH_HEYGEN_BASE_URL` guarded alike), and a clip carrying a person's voice there needs its training opt-out;
  tests use `internal/testutil/fakedid`, `fakeheygen` and `fakeai` presenters; browser run `scripts/presenters-e2e.sh`
  (ports 5510–5513, `scripts/presenterseed`); the adapters' tests run against fakes only.
- **AI models on this computer** (D278–D280, `internal/aiservices/local`, `internal/localmodels`, docs/ai-services.md):
  provider `local` (route `local`: no key, $0 price tables, nothing leaves) beside the cloud ones in the same
  estimate, records and conformance suite; adapters get the computer through `aiservices.WithEnv` and say whether a
  model runs here (`aiservices.Here`, `ReadyHere`; offered only where it can). No price to agree at $0; "no AI
  services" in Content Settings still allows `local` unless forbidden by name (`aiwork.allows`). Models: Parakeet and
  whisper.cpp as a transcription provider (words through the transcribe job, D228); Kokoro v1.0 and Kitten nano
  (Apache-2.0) through sherpa-onnx's speech build (`tools.SherpaTTS`, set only with `Use`); kinds `background-removal`
  (Apple Vision's mask; an edit, tagged `ai-edited`, not `ai-generated`) and `upscale` (Real-ESRGAN x4v3 in 128-px
  tiles through `internal/onnx`), both sending the picture's own file (`generate.PictureAsIs`); `lip-sync` (D279:
  footage part as MP4 + `--sound`, consent of everyone speaking in both; sync. via `internal/aiservices/syncso`,
  multipart upload, deleted after, `internal/testutil/fakesync`). Downloads are pinned
  (`localmodels/pins.go`: URL + SHA-256, files only, a program's SHA-256 checked before each run), made only by
  `ai.local.setup` (owners, people only), never under go test (`METARUSH_LOCAL_MODELS_BASE`, loopback); tests use
  `internal/testutil/fakelocal`; measurements `-tags localeval`.
- **Abilities that provide a kind of AI work** (D281, `internal/abilities/provides.go`, docs/abilities.md): `provides:
  [<kind>]` (validated by `providesProblems`: fixed input names per kind; no video, voice copies, dubs or
  transcription yet) makes an approved ability provider `ability-<name>` (route `ability`) through an
  `aiservices.Source` (`Refresh` before plans, lists and runs); its adapter prices from the manifest, enforces D234's
  decider (`aiservices.PersonOnly`), runs the version via `sandboxRun` (the core `RunJob` shares) as an ability run
  with `work` = the generation (no bell of its own) and hands outputs to the AI work. `aiservices.NothingLeaves`
  (local, or an ability with its network off) is the test for "no AI services" and the pictures' "no people" question.
- **Estimates you can open; Settings → Preferences** (D208, `internal/costs`, `internal/prefs`,
  `web/src/components/CostEstimate.tsx`, `web/src/screens/settings/Preferences.tsx`): what paid outside AI services
  cost is one `costs.Estimate` (schema `CostEstimate`): "about $0.40" opening to `costs.Part`s — service, model, source,
  units, each part's price and its dated rate. Generated shots (`GenEstimate.cost`), the AI voice (`voiceover.estimate`
  `cost`, ElevenLabs at `providers.ElevenLabsPrices`), cloud clean-up (`EnhanceEstimate.cost`) and Claude's priced cards
  (`AssistantAction.estimate`) carry it; the dashboard shows every one with that component (amounts like Go's: shared
  cases in `internal/costs/testdata/money.json`); a recipe run adds its steps with `Add(step, …)`. "Estimate costs of
  outside AI services before edits" is each person's (`users.preferences.estimatesOff`, default on; owners:
  `workspaces.estimates.required` via `ops.RequireOwner`); every paid path asks the hook `prefs.EstimatesFirst(ctx, a,
  userID)` (`EstimatesFirstForAsker`): off, `generate.*` needs no `maxUsd`, the dashboard and a terminal skip the price
  question, and Auto runs priced cards too (`pricedStep`, `AutoPriced`; only within limits a person set, D241) — budgets, limits, consent, approvals, looks
  and HumanOnly unchanged. `account.preferences[.set]` (people only), `metarush account preferences`;
  `/settings/preferences` sits after People, and light or dark moved there from General (old anchors redirect).
- **Google Drive and Dropbox through the person's rclone** (D220, D306, `internal/drive`, `internal/intake/cloud.go`,
  docs/cloud-folders.md): rclone is an allowlisted tool (`tools.rclone`/`METARUSH_RCLONE`); MetaRush runs `version`,
  `listremotes --long`, `lsjson` and `backend copyid` (Drive) or `copyto` after listing the folder again to find the
  Dropbox ID (Dropbox, checked against Dropbox's content hash) with typed arguments and `--ask-password=false`, only on
  remotes of the folder's own type (`Folder.Kind`; `ParseAny` reads it from rclone's list), never reads its settings or
  a token; the real rclone is refused under `go test` (`internal/testutil/fakerclone`, like the Tailscale fake).
  `add.cloud.list|add.cloud` (`metarush import cloud`, LocalOnly) bring files in like the Add box.
- **Packaging** (D75–D78): every binary embeds its renderer package (`renderer_package.go`) and renders only from `<home>/renderer/<release>-<digest>` (`internal/renderer`: `renderer.New(cfg, tools).Ready()`, set up on first use or `metarush renderer install`; `tools.videoDir` + `tools.devRenderer` is the explicit developer checkout); `metarush service` installs a LaunchAgent / systemd user unit (fakes and a temp home in tests, the real ones refused under `go test`; one daemon per home via `state/daemon.lock`); `scripts/deploy.sh` replaces binaries with a new file; `scripts/release.sh` publishes goreleaser archives only after a passing snapshot dry run, and the Homebrew tap (a cask, recommended, and a formula) downloads through the GitHub API; `improve.report` runs Claude Code headless with no shell (weekly when `improve.weekly` is on).
- **Versions and what's new** (D130, `internal/version`, `VERSION`, `scripts/version.sh`, `scripts/bump-version.sh`):
  `VERSION` is the only place the number is written; every build is stamped by `scripts/version.sh --ldflags` (`0.2.0`
  at the tag `v0.2.0`, `0.2.0-dev.N` N commits after it; a plain `go build`/`go test` says `<VERSION>-dev`), and
  `version.Current()` is what every surface reports (`metarush version --json`, `/api/health`, op `version.show` →
  Settings → General and `metarush version --details`). Compare versions only with `version.Compare` (`dev.N` is newer
  than its release, unlike semver). A release is cut with `scripts/bump-version.sh 0.2.0|patch|minor|major` (VERSION,
  CHANGELOG's Unreleased moved under `## X.Y.Z — 20 Oct 2026`, a commit and an annotated tag; never pushes;
  `release.sh vX.Y.Z` publishes that tag). CHANGELOG.md is embedded (`version_files.go`) and read by
  `version.ParseChangelog`. Every process records its computer's build on its installation before its first write (`OnFirstWrite` →
  `app.sayBuild`), the daemon also every minute (`TouchInstallation`); the
  renderer folder is named by the release, not the exact build.
- **Remotion sources** (D131–D138, `internal/remotion`, `video/render/source`, docs/remotion-sources.md): a whole
  Remotion project in a project (`remotion_sources`); versions are canonical manifests of content-addressed files
  (`remotion_versions`, SHA-256 = the version, numbered, compare-and-swap decisions); imports never touch the
  original and leave secrets, `node_modules`, `.git`, build caches, `out/` (its videos become footage) and
  coding-assistant/tool settings out with reasons. Its own Remotion: per-lockfile envs (`npm ci --ignore-scripts`,
  registry and integrity checked; bundler/renderer = remotion's version), MetaRush's bundler settings,
  `remotion.config.ts` read as text, Google Fonts kept as objects; compositions read statically (`analyze.mjs`,
  nothing evaluated) and in the jail; every page in the D70 jail (fail closed). Compositions become clips by
  rendering once per (version, composition, props, frames, settings) (`remotion_renders`, `remotion-render` job,
  provenance `platform: remotion`); only versions a person approved (`remotion approve|revoke|allow` HumanOnly;
  refused by `use|render`, `edit check`/`cut check` `remotion_not_approved`, `render submit` and the worker); the
  editing loop is `checkout` → Claude edits → `check` (words, packages, type check, affected compositions, frame
  diff, before/after stills) → a person approves → the latest cuts' clips move. Cut sheets: `remotion <source>
  <Composition>[@version]`. Tests: `scripts/remotion-e2e.sh`, the acceptance step, `node video/render/source/test.mjs`.
  The map, words and lifting (D167–D172): `remotion-task` jobs; the **project map** (`remotion map`: the static read joined with
  MetaRush's probe — `remotion`/`@remotion/transitions` aliased to thin wrappers for project files only, in the jail —
  scenes with exact times, words with `file:line`, sounds from `renderFrames` assets, a still per scene); **words as
  settings** (`remotion words setup`: one approved version wraps copy constants in `words("NAME", …)` reading the
  input prop `metarushWords`; then `remotion words set` changes data, checked in the jail, part of the render key,
  clips moved — no code approval); **pictures as settings** (D209, `internal/remotion/pictures.go`,
  `render/source/pictures.mjs`: `remotion pictures setup` wraps each picture the code draws — literal
  `staticFile("…")` paths and imported picture files — in `picture("NAME", …)` reading `metarushPictures`; then
  `remotion pictures set` replaces one with a checked resource served from the bundle by SHA-256
  (`/__metarush_pictures/`, `serve-pictures.mjs`), drawn in the jail first, part of the render key, clips moved — no
  code approval; a clip's own `pictures` on `remotion.use|render` are the recipes' binding point; push-back unwraps
  both helpers with `analyze.mjs bake`); **sound in layers** (`use --sound layers`: muted render, `<Audio>` placements as
  the cut's tracks and narration); **lifting** (`remotion lift`: the scene's file + a generated index as a motion
  graphic, the rest of its imports as the `<source>-theme` **shared library** `@metarush/lib/<name>` with fonts as
  workspace resources via `useFont`; libraries are approved once per version, cuts pin them per graphic
  (`MotionRef.Libs`, `motion g@v+lib@v`), approving one moves the latest cuts); a `remotion_approval` to-do and
  `remotion_waiting` bell for versions waiting for a person.
  Refreshes and pushes (D173–D176): **refreshes** — a source's own steps (`npm run <script>`, `node <file>`, `npx --no <tool>`; typed
  arguments) planned in plain words first (`remotion.refresh.plan`: commands; keys from Settings → Integrations under
  the names the code reads, names that steer programs refused; outputs; hosts; the plan's fingerprint) and started
  only by a person (`remotion.refresh.start` HumanOnly); a `remotion-refresh` job pinned to that machine, never retried
  (`Ran` marks a run cut short), runs them outside the render jail with the network on in a fresh copy (copies, never
  cache links; packages an APFS clone or read-only) with an environment built from nothing (`refreshEnv`) and the
  OS sandbox (`isolate.go`: `sandbox-exec` profile — MetaRush's folders, the person's home except Node, the original
  folder and the keychain denied, writes only in its room — or bubblewrap; refused where neither exists); logs both
  streams redacted (≤ 1 MB); only changes inside the outputs, under the import's rules, become a `refresh`-kind
  version on the head through `checkVersion` and a person's approval. **Pushing back** (`remotion.push.plan|start`,
  HumanOnly start, `remotion-push` job): the newest import version vs an approved one, written as one commit with
  git plumbing on a fresh bare repository (left-out files untouched), pushed to a new branch without `--force`
  (the remote's HEAD, `main`, `master` refused; only MetaRush's own branch moves forward), `gh pr create` on GitHub;
  the words helper is unwrapped (`analyze.mjs words-bake`). Collections `remotion_refreshes`, `remotion_pushes`
  (migration 34); tests use local bare repositories and a fake `gh`.
- **Fixing transcripts** (D145–D149, `internal/transcripts`, docs/transcripts.md): `transcript_segments` always hold
  today's words (every reader follows fixes); each fix is a `transcript_fixes` record (who, when, before/after, batch,
  undoable) and the machine's transcript is kept (`transcript_originals`); retyping changes only differing words and
  keeps times (joins/splits share the old stretch by letters, outer times exact, `Word.x`); a clip changes under its
  `textFixes.lock` lease with lines written only if unchanged, and ops finish once started (`keepGoing`); a fix of
  who says a stretch (and its undo) is recorded when its words move and the voices learn after, under the lease
  (`Clip.Then`, `speakers.AssignWords`/`RestoreWords`), so slow learning never leaves the record behind. Saved
  versions pin each clip's words (`Revision.transcripts` → content-addressed `transcript_snapshots`; `transcripts.For`
  in every resolver: plans, renders, QA, exports, nested cuts, deliveries); cut sheets, search and new versions read
  today's words. Fixes wait for one new version (`transcript.pending[.apply]`, one review copy, nothing automatic);
  spelling lists (`spelling_entries`, a Content Settings part, D193) correct new transcripts and give Parakeet hotwords (CLI beam search, score 2.0;
  whisper none); re-transcription carries fixes by time and text and reports misses (`fixes_missed`); the caption
  check is in QA (`caption_words`); captions files (SRT/VTT) follow fixes and delivery writes them where allowed. In
  the Claude panel changes are server-built cards (`internal/assistant/transcript_actions.go`). Browser run:
  `scripts/transcript-e2e.sh`.
- **Changing a spoken word** (D308–D312, `internal/spoken`, `internal/ops/cutsay.go`, `internal/editspec/said.go`,
  docs/spoken-words.md): a change to the cut's sound, never the recording or its transcript — `mix.said` (a new sound
  over a footage-anchored stretch, crossfaded in over `SaidEdge` where both are the recording's own samples, AI speech
  on the source bus; `heardWords` gives captions and the speech check the new words). `cut say estimate|make|place|
  undo|list|show|mouth` (records `spoken_changes`, `swc_`, migration 58); make/place/mouth people only, never Auto,
  never drafted. Only the person's own voice copy with a consent naming `new-words` and the copy's service
  (D274's unified `uses`/`providers`, `Consent.CoversUseAt`, `generate.ConsentForUse`, AI work's Go-only `MakeIn.Use`), a
  voice a person named (D274's naming record: `AssetSpeaker.NamedByPerson`), alone there; the speech is asked
  with the speaker's words around it and kept by AI work's keeper (`aiwork.RegisterKeeper`, origin `say:`), which
  checks it on this computer (`spoken.Hear`, `spoken.Embed`: exact words, TitaNet 0.40–0.50) and fits it over the
  room's quiet. Disclosed in QA (`changed_words`), the sidecar (`changedWords`), the receipt, WebVTT notes, both
  transcripts. The mouth (`cut say mouth`, lip sync) plays in place of the picture by splitting the clip.
- **Sources: descriptive names, info cards, fixes** (D150–D154, `internal/sources`, `internal/fixes`, docs/sources.md):
  a source's `title` records who chose it (`titleBy` file|metarush|person); MetaRush names it from its first look
  (`sources.Refresh` on `ops.OnSourceFacts`: renames while `nameAuto`, suggestions (`nameSuggestion`) afterwards,
  never over a person's name; `asset.names[.accept]`); cuts are named on the branch (`branches.<b>.title`,
  `cut.rename`, outside the spec) and versions carry a note (`Revision.note`, `revision.note`, a plan's `note`, a cut
  sheet's `change: note`); the panel names everything through `internal/assistant/names.go`. `asset.card` is the
  info card. Fixes: `inspect` jobs measure and mark, ticked fixes make a content-addressed fixed copy
  (`source_copies`: its `fixSteps`, named by their `fingerprint`) in a heavy `prepare` job, then the asset's current media switches (`fixes.copy`,
  `fixes.originalVariants`; the original is never touched) and what the fix affects is analysed again; analysis is
  kept per media (`footage_insights.media`, `store.MediaFilter`); saved versions pin copies (`spec.fixed`, `setFixed`,
  cut sheets' `fixed:`; new footage pinned automatically by `editspec.PinNewFootage`; `cut.useFixed` for the rest), and
  renders, previews (`?media=`), QA and exports take the pinned media (D73). Call recordings are steadied too (D190,
  `fixes/stretches.go`): shake measured between switches (`ShakeBetween`), each stretch between switches steadied on
  its own (accurate seek + `trim` so vid.stab never reads into the next picture) and joined frame for frame through
  one encoder (`steadyByStretch`, YUV4MPEG); a conversation's recordings each on their own. Steadying is vid.stab through the
  allowlisted `metarush-stabilizer` tool (the configured ffmpeg, or a pinned download checked by SHA-256; never
  the system ffmpeg; `deshake` fallback; downloads are refused under `go test`); a photo's subject comes from the
  vision helper's `mask` command. A sound fix's new words come from the ordinary transcribe job, which hears the fixed
  copy and carries people's word fixes over (`transcripts.BeforeTranscribing`/`AfterTranscribing`, misses reported;
  `TestSoundFixRetranscriptionKeepsWordFixes`). Generate video in the drop box opens the Generate panel; a shot can
  start from a resources picture (`generate.shot` `from.picture`).
- **Call recordings and graphics that stay up** (D160–D164, `internal/calls`, `internal/editspec/switches.go`,
  `span.go`, docs/call-recordings.md): picture switches found to the frame (job `switches`, insight `switches-1`:
  ffmpeg's per-frame scene score in ±0.6 s windows around candidates; `footage switches|find-switches`); a sliver of
  another picture at a clip edge is `edge_flash` in the edit/cut check and QA `qa-6` (`call_footage`), fixed by `cut
  flashes` on the frame grid (`StartAfter`/`EndBefore`, sound lead/carry when words would be cut); names a call app
  printed (`calls.NameTiles`) are covered by the vetted `name.bar` via `cut names`. Overlays take `span` and
  footage-anchored `changes` (`{footage, at, params}`) that Go resolves into frames and plan `segments` (omitempty, so
  D73 hashes hold; only `TemplateInfo.Persistent` templates and `meta.persistent` motion graphics outlast their
  limit). Motion checks draw test frames in each shape of the cuts that use the graphic (`MotionComponent.Shapes`;
  the brief's deliverables when none does). Inside nested cuts and conversations (D182, `callclips.go`): always get a
  clip's switches with `clipFootage(s, i, wrapFacts(s, facts))` (a conversation's depend on the clip's shots; a nested
  cut's are on its timeline); the bar's changes are keyed by the clip's own key, a conversation's cover comes from the
  tile (`convShotCover`/`tileArea`), layout tiles cover their own printed names (`PlanTag.Cover`) when the cut has a bar.
- **A website's brand and a project's own kit** (D165, `internal/resources/site_brand.go`, `video/render/web/brand.mjs`,
  `internal/ops/brand.go`, docs/resources.md): a page snapshot (first page) also finds the site's logos and icons
  (scored in the page; the three most logo-like kept as pictures of the page's scope, `<site>-logo`, `-logo-2`,
  `-icon`; a logo drawn in the page as an SVG with the page's CSS applied), its colours (theme-color, CSS variables,
  header, buttons, links; each with where it came from) and fonts (a Google font's regular WOFF2 only when the page
  loaded it) into `SiteSnapshot.Brand`. `brand.suggest` picks primary, accent, text and background (contrast-checked)
  and a font, each with a plain why; `brand.set --from-site` takes it and `--apply` puts it on the latest cut as a
  checked change (outside the dashboard nothing is saved until the confirm token). The kit is a Content Settings part
  (D193; `ops.KitFor` resolves through them for new cuts, `brand.apply`, motion checks and slides).
- **Content Settings** (D192, D193, `internal/content`, `internal/ops/content.go`, `store.MoveContentSettings`,
  `web/src/components/project/ContentSettings.tsx`, docs/resources.md): a project's brand, spelling list, narration
  voice and house rules are a record that belongs to a project (`content_settings`, `cnt_…`); `Project.content` says
  whose it uses — the default (`content_defaults`, the project whose "Use for new projects" is ticked; any member),
  another project's (followed live through chains; circles refused) or its own (empty or a copy; never deleted, set
  aside when it follows another). Resolve only through `content.Load`/`Graph.Resolve`/`For`/`Owners` (a gone
  project's followers go back to the default and are told); parts register once (`content.Register`) and get
  following, copies and the Settings tab for free. Editing a part through its op with a follower is refused
  (`E_CONFLICT`); without a project the ops edit the default's and say so (`Target.Note`). Spelling: a clip uses the
  lists of all its projects' settings, footage in no project the default's. Voice choices follow; money never does.
  House rules (`content.RulesFor`, labelled) reach Claude's context and QA; keep cards for rules ask where
  (`keepWhere`). Brand changes offer the latest cuts (`latestCuts` → `brand.apply`); `transcript.spelling.apply
  settingsOf` fixes existing transcripts. Migration 36 moved today's data so nothing anyone sees changed. The project
  tab "What it keeps" is **Settings** (`?tab=memory` still works): Content Settings, then This project.
- **Logo files, the house style, house sign-offs** (D198–D200, Content Settings parts; `internal/editspec/logos.go`,
  `style.go`, `internal/ops/logos.go`, `style.go`, `signoffs.go`, `internal/qa/style.go`,
  `web/src/components/project/HouseParts.tsx`): the brand has five named logos (`BrandKit.logo` = Main, `logoDark`,
  `logoLight`, `logoIcon`, `logoWordmark`; `content.logo.set|remove`; cuts pin `logo`, `logo-dark`, `logo-light`,
  `logo-icon`, `logo-wordmark`) and a cut whose `brand.logo` names its main logo gets, in the plan, the logo made for
  each template's background (`editspec.PickLogo`: WCAG contrast with `brand.background`; `logo.corner` keeps the
  main; cuts without it unchanged). The house style (`ContentSettings.style`, `editspec.HouseStyle`) is what new cuts
  start with: `NewCutDefaults(s, base, style)` fills a first cut's unset settings (nothing set = byte for byte as
  before), `cut names` writes its bar with it, `content.style.set` answers `latestCuts` and `content.style.apply`
  (`editspec.StyleOps`, a diff of the conformed copy) is the checked change; Claude's context (`ops.StyleContext`) and
  QA `house_style` (captions missing, a name bar that animates or is taller than it says) read it. New spec fields:
  `captions.size`, `name.bar` `height`/`animate`, `brand.logo|logoDark|logoLight` (all omitempty; D73 hashes hold).
  House sign-offs (`ContentSettings.signoffs`) join each cut's checklist (`ops.SignoffsFor`; every reminder through
  `OpenSignoffs(ctx, a, p, branch)`), ticked per project (`Project.houseSignoffs`); a sign-off keep card asks where.
  No migration (39 unused): a kit's logo is its main logo.
- **Layout packs** (D263–D267, a Content Settings part; `internal/editspec/packs.go`, `packs_preview.go`,
  `internal/content/parts_pack.go`, `internal/ops/packs.go`, `internal/qa/pack.go`, `internal/assistant/pack_actions.go`,
  `web/src/components/project/LayoutPackPart.tsx`, docs/layout-packs.md): `ContentSettings.pack` maps roles
  (`title-card`, `lower-third`, `end-screen`, `quote-card`, `chapter-card`; variants `lower-third:guest`) to a vetted
  template or an approved motion graphic with the pack's `fields` (choices among fixed options = its look, always the
  pack's; text = words used only where a cut gives none), `seconds`, `logo`, plus `layouts` (picture in picture,
  side by side); no pack = `editspec.StarterPack(houseStyle)`, which also fills roles a pack leaves out
  (`content.PackFor` → `editspec.Packs`). Overlays name `role` beside the template they were drawn with (omitempty;
  saved versions never change): `prepare` runs `fillPackRoles` (roles without a template; every role of a new cut;
  conversations a change adds get `ConversationPin.layoutStyle`), `PackOps` is `content.pack.apply` (checked change),
  `set|accept` answer `latestCuts`. Cut sheets: `- intro: title card at …` in, `title.card … · as title card` out.
  Code reaches a pack only through a person: `content.pack.propose` saves `packDraft` (anyone, Claude included),
  `content.pack.accept` is HumanOnly and needs every motion graphic approved and the draft's base version current;
  `content.pack.set --motion` only for a person. In the panel the refused `content pack propose --file` is a HumanOnly
  card running accept (kept as the draft while a graphic waits); never Auto. New vetted template `end.screen`
  (room for a platform's end screen elements). Layout looks in Go (`LayoutTilesStyled`, `PlanTile.border`,
  `PlanShot.backdrop`). QA `house_style`: `pack_role`, `pack_override` notes (`editspec.PackDrifts`).
  `content.pack.preview` = a plan per role (no footage) the dashboard plays. Browser run `scripts/layout-packs-e2e.sh`
  (ports 5490–5499); self-test variant `layout-pack`.
- **The ship gate and promises made to the client** (D298–D302, `internal/ops/shipgate.go`, `internal/measures`,
  `internal/qa/promises.go`, `web/src/components/ShipGate.tsx`, docs/ship-gate.md): `ops.ShipGate` is the one answer
  (`delivery.ready`, `metarush deliver ready --json`, the version page): holds `approval`/`render` (never overridden)
  and `qa-missing|running|failed|stale|errors`, `signoff` (stable keys, one-step `paths`); warnings never hold.
  `deliver.export` and `export.timeline` into a client folder (`ClientFolder`: a `metarush.delivery/1` record there
  or up to three folders up, or one delivery wrote to, `<home>/state/delivery-folders.json`) refuse a held version
  with `E_HELD`. On per workspace (`Workspace.shipGate`, owners, new workspaces on; older ones a one-time `ship_gate`
  to-do via catch-up `ship-gate` v1) and per Content Settings (part `gate`, owners); off is people only.
  `delivery.override` is HumanOnly with a note and the hold keys shown (`holds`; others → `E_CONFLICT`) →
  `Revision.shipOverrides` (append-only); delivery writes every covering note into `gate.sentAnyway` + `## Sent out
  anyway` and appends `Revision.sentOut`; the receipt lists `sentAnyway` (allowed vs `wentOut`); settings switches
  that drop a gate, promises or house sign-offs go through `gateSwitch` in `leaveSwitch` (owner, as a person);
  dismissing the `ship_gate` to-do is an owner's; older computers warned (`olderWarning`); the review desk refuses it
  (`notDrafted`, checked before the generic reason); `loosening()` makes ticking/removing sign-offs (gate on) and
  taking back promises people only. Promises are `ProjectRule.promise` (measurable only; `measures.Parse/For/Words`
  shared with ops, cards, recipes and QA): QA `qa-7` check `promises`, `QAReport.promises[]`/`notesSha`,
  `promise_broken` (error) / `promise_unmeasured` (warning), golden `internal/qa/testdata/promises.golden.json`;
  plain rules with these measures warn; `disclosed` wants `qa.DisclosureLine` (from the records) or explicit
  on-screen words and walks nested cuts (`shownClips`); `faces-in-frame` needs every shown second looked at
  (`facesUnseen`); `captions` accepts burned-in captions (index, or `captions:burned-in`); unmeasured promises change
  the Ready headline. Keep kind `promise` (`metarush keep promise`, `project rules add --promise` →
  a card a person keeps). Browser run `scripts/ship-gate-e2e.sh` (ports 5560–5569).
- **What a project keeps** (D166, `internal/keep`, `internal/ops/keep.go|rules.go|signoffs.go`,
  `internal/assistant/keep.go|attachments.go`, docs/keeping.md): "Keep this?" cards come from Claude's refused
  `metarush keep …` (and `project rules add`, `cut signoffs add`, `improve add`) and from MetaRush's detectors at turn
  end (links the person typed, attachments, a snapshot's logo); `keep_items` (migration 32) holds one record per
  project and content key, so nothing is asked twice and Not now declines for good; keep cards are never safe steps
  nor combined. `keep.add` keeps through the ordinary ops. Project rules (and the house rules of its Content
  Settings, D193) go into every turn's context and QA `project_rules` measures the measurable ones; sign-offs remind on approve/export/deliver and lock only where the ship gate is on (D298); the brief's
  deliverables carry shapes. Attachments: `assistant.attach` (people only) stores by SHA-256, the message names them,
  the turn copies them into `proposals/attachments/` for Claude's Read, `/api/assistant/conversations/{id}/attachments/{sha}`
  serves only the conversation's own.
- **Setup in five minutes; the database on this computer** (D253–D257, `internal/cli/setup.go`, `internal/localdb`,
  `internal/firstrun`, docs/installation.md): `metarush setup` is a local admin command built on `init` (run quietly):
  an interview with suggested answers, every answer a flag, `--json` (`SetupResult`), safe to run again (never moves
  where things are kept). The database on this computer is MongoDB Community Server downloaded at setup from
  MongoDB's server (never in our archives: its licence is the SSPL), pinned per Mac/Linux distribution by SHA-256,
  under `<home>/database` (refused in sync folders/repositories), a one-member replica set on 127.0.0.1 (port from
  27150, never 27018, remembered per computer in the config folder), `--auth` + a 0600 key file, an admin user
  (upgrades only; keychain or `<home>/database/admin.secret`, never an env var) and a `metarush` user with readWrite on
  MetaRush's database only; `localdb.Prepare` resumes every step and takes only this server's saved addresses
  (`Ours`); mongod gets a clean environment, its pid file is believed only for its own program and data folder;
  Claude Code never gets `secretpath.IsSecretEnvName` variables; doctor checks a connection without a sign-in is
  refused; another database's saved address replaced only with a yes or `--replace-saved-database` (kept aside); a
  setup failing before the admin exists stops the server; `database.local.port` in config; systemd
  `KillMode=process` + `ExecStopPost` `metarush service reap` (never mongod);
  `app.OpenWith` starts it, the daemon `Watch`es it, `metarush stop` stops it, `metarush database
  status|start|stop|upgrade` (upgrades only by a person at a terminal, `--dry-run` for agents, one series at a time,
  never back, an unfinished one offered again). Linux kernels 6.19–7.0.13 (D339, `localdb/kernel.go`): MongoDB 8
  refuses them, so `KernelRefuses` reads the kernel as the installed MongoDB does (uname's release; Ubuntu's
  `/proc/version_signature` from 8.0.35) and a new home there gets `localdb.Older` (7.0.45; `KernelNote` said before
  the download); an 8.0 home there never goes back: `Start` returns `ErrKernel` with plain words and what to do
  (`Error.Next`), `Status.CantStart|UpgradeWaits`, doctor, `app.OpenWith`; upgrades wait for the kernel. Downloads refused under
  `go test` (stand-ins: `METARUSH_MONGODB_PIN` with an optional `older`, loopback only; `METARUSH_MONGODB_ARCHIVE`;
  `localdb.ReadKernel` replaced for a kernel); tests use a real mongod
  when one is installed (`METARUSH_TEST_MONGOD`, else skipped). `firstrun` says what works with no keys / with a key
  and checks Claude Code by asking it (`--version`, `auth status --json`; never a sign-in, never its credentials;
  refused under `go test` unless `tools.claude` is a fake); op `setup.status` feeds Home's Getting started. Release
  builds are signed (hardened runtime) and notarized by `scripts/macos-sign.sh` only when the release Mac has
  `METARUSH_SIGN_IDENTITY` / `METARUSH_NOTARY_PROFILE` (environment or git-ignored `release-signing.env`). Browser
  run: `scripts/install-e2e.sh` (ports 5460–5469).
- **Components: only what MetaRush needs is required** (D324–D328, `internal/components`, `internal/cli/components.go`,
  `setup_components.go`, `web/src/components/Components.tsx`, docs/installation.md#components): one registry —
  required `metarush`, `database`, `storage`; every other outside program or download an optional component with
  plain words (title, `Thing`, `Feature`, what it unlocks, size, time, `Suggested`, licence — the renderer says
  Remotion's), one detection (`Env.Find`; setup, doctor, firstrun's `WorksItem.component`, `setup.status`
  `components`, the worker's `Lacks`) and ways per system: Homebrew where it is (`brew install --formula`, never sudo),
  else a pin (`components/pins.go`: URL + SHA-256, unpacked inside, into `<home>/tools` with `tools.*` set, or the
  models folder), else words for a person. `metarush setup` checklist (`--components`, `--no-components`, `--yes`
  = suggested; `SetupResult.components`); choices in `<home>/state/components.json` (`components/choice`; a left-out
  renderer never sets itself up). `components.list|add|remove` (`metarush components [add|remove]`): add/remove
  `HumanOnly` + `OwnerOnly`, only at a person's own terminal (never `CLAUDECODE`/`--agent`), and setup run by an AI app
  installs nothing. Missing programs say which component adds them (`tools.Advise` → `ErrMissing.AppError`, `details.
  component`); waiting jobs `needs_component` (`WorkerDevice.lacks`); `Worker.Refresh` after an add. Clearer and
  studio voices come from `internal/sound` (`components.Provide`). Tests: `internal/testutil/fakebrew`,
  `METARUSH_COMPONENTS_BASE` (loopback), `METARUSH_TOOL_DIRS` (only these folders), stand-in `Env.Steps`; never a
  real download or install. Browser run `scripts/components-e2e.sh` (ports 5650–5659).
- **Escape hatches and plumbing** (D92–D95): `export.timeline` writes OTIO / FCPXML 1.8 / FCP7 XML from the resolved RenderPlan (`internal/interchange`; validate with `scripts/interchange-check.sh`, docs/timeline-export.md); the worker keeps Parakeet (and whisper.cpp through `whisper-server` from the same build, loopback under a random
  path, D121) loaded in `transcribe-model-server` (purego, idle stop, memory cap, CLI fallback; transcripts identical); `storage.migrate` (HumanOnly; `storage.plan` is the dry run) copies by content address with SHA-256 checks, resumes, switches config.yaml + the workspace only when verified and never deletes (`internal/storagemove`; a daemon re-executes itself when config.yaml names another store); `metarush mcp serve` exposes non-HumanOnly ops over stdio (the core by default, D222), read-only unless `--allow-changes` (confirm token per change), channel `mcp` (`internal/mcp`, `scripts/mcp-check.sh`, docs/mcp.md).

- **Timelines from other editors** (D303, D304, `internal/interchange/read*.go`, `flatten.go`, `flat_spec.go`,
  `internal/ops/timelineimport.go`, `web/src/components/TimelineImport.tsx`, docs/timeline-import.md): Go readers per
  format (`interchange.ReadTimeline`: OTIO, FCPXML 1.8–1.11 with `media-rep` and `.fcpxmld`, FCP7 XML, CMX 3600 EDL;
  AAF refused) into one model; OpenTimelineIO (pinned by hash, `scripts/interchange-requirements.txt`) is only the
  check (`scripts/interchange-check.sh` also has it write files the readers read back). `Flatten`: the topmost
  picture, gaps closed, transitions as overlaps, own sound with ≤ 3 s split edits, other sounds added, chapter
  markers → delivery-notes chapters, markers → clip notes, everything else in `Lost` (plain, never silent).
  `timeline.read`/`timeline.import` (`metarush import timeline`, the Add box's dialog) match footage by MetaRush's
  note, fingerprint, then name/length/timecode, and ask (relink, add, skip) before anything is made; the cut is a
  new branch through check/confirm (origin `import:timeline`). Reading a file path is refused over the network.
- **Posting to video platforms** (D305, `internal/publish`, `internal/ops/publish.go`, docs/publishing.md): people
  only, every time (`publish.send` HumanOnly and refuses the assistant, Auto and any `Actor.Agent`). YouTube through
  the owner's own Google client (`publish.setup`, OwnerOnly, keychain) and each person's own sign-in in their browser
  on this computer (`publish.connect`: loopback + PKCE, refresh token in the keychain per person, never DB/config/log;
  in a secrets file under `METARUSH_PUBLISH_*`, which `secretpath.NeverInherited` keeps out of the environment, tool
  runs and Claude Code).
  `publish.plan` = exactly what goes (final render, delivery notes' words and chapters, captions per language,
  disclosures, privacy default private, the channel) and what holds it, the ship gate (`ShipGate`, overridable holds;
  a post past it `recordSentOut` what `post`) and client rules (`MayLeave`, place delivery, receiver the platform; quiet
  in a plan through `ops.LeaveQuiet`) included. One post of a render to a channel at a time (check-and-set), resumed
  by the daemon's `RunPublishRecovery` (migration 60: partial index of versions with a post sending; session in
  `<home>/state/publish`, never the DB or log). Posts are `Revision.publications` and the
  receipt's `posted`. Tests use `internal/testutil/fakeyoutube`; real YouTube and the browser are refused under go test.
- **Recipes** (D201–D207, `internal/recipes`, docs/recipes.md): a recipe is a workspace-wide, content-addressed
  document (`metarush.recipe/1`: inputs, fixed and chosen sections, the rest of the cut, bindings, rules, hints;
  `recipes`, `recipe_versions` `<workspace>/<sha256>`, `recipe_runs`, migration 38); `recipe.check` runs on every save
  and plan (bindings vs template schemas, word settings, approvals, a dry assembly). Saving proposes what varies;
  Claude's saves and `recipe.update` (merge patch) are drafts, only people make a version current. `recipe.plan` gives
  one estimate — D208's `costs.Estimate`, the services' own parts added under each step's label (`Add(step, …)`) — and
  a fingerprint; `recipe.run|again|continue|publish|archive` are people only, and one agreement in dollars covers a run
  (`maxUsd`; past it waits for a person; budgets still stop spending; Auto never starts one) unless
  `prefs.EstimatesFirst` says the person turned estimates off (`agreed.skipped`). Runs make ordinary cuts (origin
  `recipe:<run>`; a new cut `<recipe>-<n>` or a new project linking the recipe's), advance from the database
  (`recipes.Advance`: the daemon's `RunEngine`, a terminal's `--wait`; only on the computer that started it, leased),
  as the person in the `recipe` channel (`models.ViaRecipe`); fixed sections locked, chosen ones get stand-ins
  Claude replaces through a cut sheet; again rebinds only changed inputs on the cut's newest version. Clips made from
  code are drawn again with the run's words (D168) and pictures (`pictures:<clip>.<NAME>` → the clip's own pictures,
  remotion.use's binding point, D209) by `remotion.ClipWith`: the same approved version, no approval. Waiting shows on
  the activity bar, `recipe_run` to-dos and the bell; a run asking to spend more carries `waitingUsd` (going on agrees
  to it) and steps keep the later steps' share of the agreement (`leftFor`). The dashboard (D208): Library → Recipes
  (`web/src/screens/Recipes.tsx`, `web/src/components/recipes/`: gallery, `?recipe=`, `?run=`, the run dialog with the
  estimate, Save as a recipe…, "Made with…"); browser run `scripts/recipes-e2e.sh` (ports 5390–5392, fakes only). The
  word "recipe" means only this feature (D210). **In Claude's panel** (D211, `internal/assistant/recipe_actions.go`)
  the refused `recipe run|again|save|update|publish` are server-built cards read from the Recipes ops by name (the
  panel never imports `internal/recipes`: that package's tests read the activity bar, which reads conversations): a
  run's plan with its fingerprint and `maxUsd` = the estimate (Claude's `--max-cost` ignored), a save as a draft, a
  change (`recipe.update` `dryRun`, a patch only from the proposals folder) and a version made current with their plain
  differences; none starts by itself (`startAuto` refuses `recipeCard` first), run/again/publish are `HumanOnly` and
  decided alone (`decide_alone`; the panel never combines them), a stale plan (`E_CONFLICT`) is planned again in place
  (`staleRecipe`), and a run a card started is the daemon engine's to take on (`recipeCaller` only wakes it, as Run
  with… does — never steps inside the click) and the card's work (`WorkRun`: Claude hears when it waits for Claude or a
  person, or is ready). **The recipe's page** (D212): `recipe.diff` (`internal/recipes/differences.go`) says what changed
  between versions part by part in plain words (per version on request, a non-current version against the current
  one, Make current…); `recipe.compare` `copies` feed `?compare=<run>&with=<run>` (`SideBySide.tsx`: one Play for
  both review copies); Home's Start from a recipe (`StartFrom.tsx`). Sheets, picks and packs (D213–D215): **the sheet** (`sheet.go`, `sheetparse.go`):
  Markdown — settings, `## Inputs|Sections|Pack|Bindings|Rules|Sign-offs|Hints`, `## The cut` (a cut sheet whose
  clips the sections name) — that reads back to the same SHA-256 (versions are saved normalized, `NormalizeDoc`; a
  reflection test wants a spelling for every new field, else a `{…}` block carries it); `recipe.checkout`,
  `recipe.check` `sheet` (problems by line, `DocChanges`), `recipe.write` (`check --save`: always a draft, a new recipe
  when `name:` is new without `base:`). **First picks** (`picks.go`): chosen sections get scored stretches from
  `footage.moments` (else movement, else stand-ins); the shorts' from `conversation.moments`; the run waits until
  each is kept or changed (`recipe.picks`, under the run's lease; `standIns[].kept`, `run.picks`). **Packs**
  (`pack.go`, `RecipeDoc.pack`, `RecipeRun.outputs`): shorts each made by `conversation.short` (`origin` passed, so
  versions say `recipe:<run>`) after the picks, clearer voices like the main video (priced in the one estimate), review
  copy + QA, askReview with the main video; `recipe.runs --project` matches `outputs.projectId` (migration 39).
  Suggestions, triggers and files: **suggested changes** (D217, `suggest.go`): each approved run's version against the one it made, part by
  part of its recipe (fixed clips' lengths or taken out, chosen moments in all, graphics, captions, music levels,
  narration words; inputs and Claude's picks never count); a change in more than half of the runs (at least two)
  becomes a draft with reasons (`recipe.suggestions[.save]`, a draft only a person makes current; one the version has
  already isn't offered). **Planning by itself** (D218, `triggers.go`): `Recipe.triggers` (new files where an input
  takes them from — compared as ID@version, settled two minutes — or a weekly time), off until a person turns them on
  (`recipe.triggers.set` people only; that person or an owner changes them), looked at by the daemon's
  `RunTriggers` on the computer they were turned on at; a plan made as that person goes on the recipe
  (`Recipe.prepared`) and the bell (`recipe_planned`, link `&planned=1` opens Run with…); it never calls `recipe.run`,
  and any run that starts uses the plan up. Claude's `recipe picks`, `recipe check --save` and `recipe suggestions
  --save` are panel cards (`picks`, `sheet`, `suggested`); turning triggers on never is.
  **Between workspaces** (D219, `export.go`, `importfile.go`): `recipe.export` (LocalOnly) writes a zip —
  `recipe.json` (`metarush.recipe-file/1`: version, lineage, document, what each file is, what was left out), the
  sheet, `files/<sha256>` (pinned resources, the footage the cut plays, approved code by hash: motion archives,
  Remotion manifests and files); `recipe.import` (Mutating, LocalOnly; the dashboard's `GET /api/recipes/export` and
  `POST /api/recipes/import`, `internal/api/recipes.go`) reads it strictly and makes a **draft** — a new version of
  the recipe whose lineage it shares (`recipe_versions.imported`, migration 42), else a new recipe (a free name) —
  with its things in one project (`AddFile` `Fresh`, clips keeping provenance, `motion.Keep` with no record until
  `motion check --version`, `remotion.ImportFromFile`: a new source, packages not allowed, never approved); imported
  words are data, imported code waits for people. **A Google Drive folder** (D220, `drive.go`, `internal/drive`):
  `from.drive` (`gdrive:Teaser/Art`, `recipe update --from-drive`) is listed by the person's own rclone (allowlisted
  `tools.rclone`, only `drive` remotes, no token ever held; refused under `go test`, fakes in
  `internal/testutil/fakerclone`) and only the files a run uses are fetched by Drive ID in its first step (pictures
  tied by `resources.driveFile`, migration 42); checks never list a Drive; `doctor` has a GOOGLE DRIVE check.
  **Starter recipes** (D293, D296; `starters.go`, `internal/recipes/starters/<name>/`, source and generator
  `starters_def_test.go`, `METARUSH_UPDATE_STARTERS=1 … -run TestStarterFiles`): `call-episode` and `release-video` as
  embedded recipe files (+ `starter.json`: makes, gives, `uses` = `metarush/<id>` names, D269); `recipe.starters` (here /
  newer / new, by lineage) and `recipe.starters.add` (not people only: packs each into a zip and runs `importRecipe` →
  a draft, never current, examples in the project "Starter recipes"; a newer starter comes in as a draft of the same
  recipe); the Recipes tab and Home's StartFrom offer them (`Starters.tsx`). **A sample first** (D294, `sample.go`):
  `planSample` reads the plan's priced steps (voice, shots, sound/pack → kinds) at ≥ `SampleFloorUSD` ($0.05); a run
  starts as `inputs, place, sample, approve` with only the sample's price agreed (`run.sample`); the sample is a short
  cut `<cut>-sample` (`sampleSpec`: a 30 s stretch made clearer, the shot — `run.Made`, reused — or the start of the
  video under the first line, reused from the TTS cache) with a review copy; `recipe.sample` (HumanOnly) approves →
  `planRest` (`planIn.Into` [+ `Repeats`]: same inputs and place, today's prices; the approver's own `prefs.EstimatesFirst`
  decides whether the rest waits for its `waitingUsd`) or not-quite (stops); `recipe.run|again` `skipSample` (a reason,
  recorded); `recipe.continue` refused while the sample waits; a run again samples kinds whose inputs changed
  (`kindChanged`, unless `approvedKinds`); a stopped/failed run ends its sample (`endSample`, spend copied every look);
  the voice sample is the shortest whole line; the receipt's `samples`; the panel's run card agrees to the sample's price. **Pack shorts of
  footage** (D295: `RecipeOutput.footage`, `footage.short` cuts `<output>-<k>` in the run's project), `followFootage`
  (a recording's clearer voice moves to the new footage; import remaps `mix.voices`), a voice input without a voice
  reads the Content Settings' narration voice; clean-up priced as sound pads it (`sound.WindowSeconds`, D297).
  Browser run `scripts/starter-recipes-e2e.sh` (ports 5550–5559).
- **A small tool surface: the catalogue, find, MCP's core, skills as routers** (D221–D223, `internal/catalog`,
  `internal/mcp/toolsets.go`, docs/mcp.md): every operation is a catalogue entry (`commands.json` is generated by
  `TestCommandIndex`; `METARUSH_UPDATE_COMMANDS=1` rewrites it when commands change) that `find` ranks
  deterministically (BM25F; `opWords` and `synonyms` in `words.go` hold people's words — tune them on the eval's
  non-held-out requests only); `ops.find`/`ops.describe` (`metarush ops find|describe`). Other catalogues join with
  `catalog.Register` and run through their own operation (`RunWith`, input under `"input"`; never beyond its flags,
  `catalog.RunTarget`); changes that check themselves are a list (`catalog.ChecksFirst`). `mcp serve` offers the
  core (find, describe, run for reads, run_change for changes + everyday tools: ≤ 12 tools, ≤ 2,000 tokens) or
  `--toolset review|edit|library|admin` (≤ 25, related ops as one tool with `mode`, reads and `<name>_change` apart)
  or `all`. **No tool both reads and changes** (clients approve per tool; the confirm token is not a person's OK);
  guards in `internal/mcp/guards_test.go` (counts, 150-word instructions, no near-duplicate descriptions, no mixed
  tool, docs/mcp.md's table). Skills are routers: description + `when_to_use`
  ≤ 700 characters each and 8,000 in all (`skillsinstall.MaxListingEntry|MaxListing`), detail in the body. The
  selection eval (`internal/catalog/selection.json`, 186 requests, every third held out) gates find in go test
  (`catalog.SelectionGate`, also with 500 made-up entries; `catalog.EvalEntries`: what find searches, sample abilities
  included, D271); `metarush skills eval --live` runs real Claude Code, opt-in, never in CI.
- **Ready to publish: the plugin, other apps' skills, the publish check** (D319–D323, docs/plugin.md,
  docs/publish-check.md): `plugins/claude-code` (the Claude Code plugin `metarush`: the skills, MCP's read-only core
  started as `metarush mcp serve` by name, a `claude plugin eval` suite from the natural live cases), `plugins/agents`
  (the skills in the Agent Skills format + an AGENTS.md block; `metarush skills install --agents`) and
  `.claude-plugin/marketplace.json` are generated from `skills/` by `metarush skills package-plugin` — never edit
  them; **a change to a skill means regenerating them** (`TestPluginIsCurrent` fails otherwise;
  `METARUSH_UPDATE_PLUGIN=1`). The plugin's version is VERSION's (`scripts/bump-version.sh` writes it). Skill
  frontmatter must be valid YAML (`skillsinstall.Frontmatter`; `when_to_use` single-quoted) or Claude Code drops it.
  `scripts/publish-check.sh` runs `scripts/check-public.sh` (the open-source-prep standard's, verbatim: never edit
  it here) and then MetaRush's own checks (work notes, `*.ts.net` hosts, `scripts/secret-scan.sh`); deny-lists in
  `local/publish-denylist.txt` (git-ignored) and `~/.config/open-source-prep/denylist.txt`, allowed emails in
  `scripts/check-public.allow`, skipped paths in `scripts/check-public.skip`. It is a step of `check.sh --release`; nothing here publishes anything.
- **Abilities: capabilities added without a release** (D230–D235, `internal/abilities`, `internal/sandbox`,
  docs/abilities.md): a folder with `ability.yaml` (`abilities.Manifest`, read strictly; schema generated as
  `schema/ability.schema.json`) and its script or program, versioned by the SHA-256 of a canonical archive
  (`metarush.ability/1`; links, installed packages and credentials (`secretpath.SecretShape`) refused). One op family
  (`ability.list|find|show|check|approve|revoke|setup|plan|run|runs`; collections `abilities`, `ability_setups`,
  `ability_runs`, migration 44). `check` (LocalOnly) runs the manifest's test once in the sandbox with the network off;
  `approve|revoke` are OwnerOnly + HumanOnly, never Claude (`ability_approval` to-do, `ability_waiting` bell);
  `setup` (people, on that computer) pins programs by SHA-256, keys only from owners (`providers.StoreKey`; MetaRush's
  own integrations' keys only to their services). A run is planned (fingerprint), started by whom the manifest allows
  (free, no key, words only: anyone; a key: a person; priced: a person agreeing to the estimate; media leaving, outside changes, an open
  network: a person; likeness: not yet), an `ability-run` job pinned to that computer, never retried, in
  `sandbox.Room` (sandbox-exec or bubblewrap + `metarush sandbox-forward`; refused in plain words where neither can
  run) with the network off or through a per-run `sandbox.Proxy` (declared hosts, keys put in for their hosts over
  HTTPS, only in the header or query parameter the manifest names, keys taken out of answers in every written form
  (`KeyForms`), requests recorded; words-only abilities: one 256 KB outbound budget per run); reads only the program's
  own installation (`InstallationOf`), hidden folders always win, writes only its output, home and tmp folders, runs
  `tools.Spec.Contained` (own session, its group killed when it ends); an open network goes through the proxy too
  (`AnyHost`); declared outputs only, copied out without following
  links, through `ingest.File`/`resources.AddFile` with `madeWith` (`models.AbilityMadeWith`). Node's fetch needs the
  preload (`sandbox.NodePreload`). Tests: `go test ./internal/abilities/ ./internal/sandbox/` (local test servers
  only), `scripts/abilities-e2e.sh` (port 5420). **In find** (D268, `internal/abilities/catalogue.go`): kind
  `ability`, each entry a change and a person's to do unless the decider says safe, `Blocked` with a plain `missing`
  ("needs an owner's approval", "needs setting up on this computer") that `catalog.RunTarget` refuses; `RunWith`
  `ability.run` (`ref: name@hash`), which takes find's form (`input: {inputs, plan, projectId, maxUsd}`), plan first.
- **What may leave this computer, per client; ability files** (D288–D292, `internal/leave`, `internal/receipt/left.go`,
  `internal/abilities/files.go`, docs/what-may-leave.md): a Content Settings part (`ContentSettings.leave`: `nothing`,
  per place `keep` kinds and `only` receivers; places ai, abilities, claude (Claude and AI apps), storage, delivery;
  kinds footage, voices, faces, words, pictures; empty = today's behaviour) beside the AI services part (both must
  allow). Everything that sends a client's things asks `leave.Check` with what, to whom and every project whose things
  they are; refusals are `E_KEPT_HERE` (rule, whose settings, who set it) recorded as `leave.refused` unless the context
  is `leave.Quiet` (estimates, plans). Wired in: AI work and generated video (plan + `stillAllowed`), the AI voice,
  `voiceover.clone`, cloud clean-up; ability plans/runs and the proxy's `Refuse` per host; `ops.dispatch` → `ops.AIReads`
  for reads on AI channels — deny by default: every non-mutating, non-people-only op declares what it shows in
  `leave.readsShow` (a guard test; new reads add a line), touched projects come from `leave.ProjectsIn` (versions'
  clips, nested cuts, members, pictures; projects' shared clips, links and heads; records by ID); panel line
  `Conversation.keptHere`, Claude hears `leave.ClaudeRule`, and while any client keeps things from Claude the spawn
  denies the scratch and cache folders and `ops.AIOutput` copies files an answer names into the conversation —
  describe, askFit, the weekly report; `ingest.File` (`leave.MayStore`) and storage moves
  (`ops.LeaveStorage`); deliveries/timelines into sync folders; Recipes' files leave kept things out. New places that
  send something must ask it. `content.leave.set` is people only; loosening (`leave.Looser`) takes a person who is an owner
  (`leave.mayLoosen`: never the assistant, MCP, propose-only apps or Claude Code in a terminal), also via
  `content.follow|own|setDefault` (`ops.LeaveSwitch`) and moving or linking things (`ops.LeaveCarry`: asset.move/link/
  archive, project.link/use). Remotion pushes and refreshes ask it. Proof: the receipt's `left`, `project.leaves`.
  Ability files: `ability.export|import` (LocalOnly; `/api/abilities/export|import`), zip with `ability.json`
  (`metarush.ability-file/1`) + canonical `archive.json`; imports are state `imported` (never approved or set up),
  `ability check --version` checks them here, then an owner approves.
- **The starter pack** (D283–D287, `abilities/` at the root, `internal/abilities/starter_test.go`, docs/abilities.md):
  seven abilities as shipped folders; tests check each from its folder and run it against a fake of its service (the
  proxy's dial seam `testDial`, `fakeHosts`; never the real service). `result.json` takes `credit`/`author` (D283):
  clips and sounds record them as rights (D38, never over a person's), pictures as `Author`, `madeWith.Credit`;
  `AbilityMadeWith.CreditLine` is the delivery credit, and delivery writes credits into `.notes.md` (a file of its own
  when only an ability's licence asks). `local: {port, service}` (D284): one loopback port, plain HTTP through the
  run's proxy (`ProxyConfig.LocalPort`, dialled as the address; the run reads `$METARUSH_LOCAL_URL`), only after `ability approve --local-service`
  (`localPort` + `localProgram`, `listener.go`: the program listening then — lsof `txt`/`cwd` (`-b -w`, 5 s) on a Mac,
  `/proc` on Linux; for interpreters also folder and script/module from argv — plus the manifest's `local.probe`; the
  owner sees it (`HereLocal` on show/setup) and the request carries it (`sawProgram`); `setup --agree-local` per
  computer, otherwise kept; plans show it and fingerprint it; runs check it at start and the proxy's `LocalCheck` on
  requests, 3 s memory, breaking the run off),
  reserved: MetaRush's, the renderer's 3000–3100, 4840–4899, databases', control ports, ≥ 32768 (`ReservedPort`,
  `remotion.OwnPorts`), `decides: local` (a person); proxy records carry the port; answers past the size limit are
  broken off. `needs.files` (D285, `files.go`): `ability setup --file name=path` (owners; `look` first; checks on the open file by its
  stored path — `F_GETPATH`, case folded, and dev/inode of its folders — never MetaRush's folders, `~/Library`, `/etc`, `/proc`, `/dev`, hidden
  paths, hard links, key stores by bytes or name, key-like text; `kind: whisper-translator` reads the ggml header) pins size/time/SHA-256;
  runs get a checked read-only copy (`clone_darwin.go`) at `$METARUSH_FILE_<NAME>`. Output `captions` (SRT/WebVTT ≤ 2
  MB, `language` from result.json) is kept on the run's one clip (`Asset.captions`, purge knows it);
  `footage.captions` / `metarush footage captions`. Not yet runnable (D286 c2patool, D287 Resolve MCP): `# NOT YET
  RUNNABLE.` first, notes say what's missing, `TestStarterNotYetRunnable` pins the refusal;
  `install-c2patool.sh` pins c2patool 0.28.3 by SHA-256. Browser run: `scripts/starter-e2e.sh` (ports 5530–5539).
- **The first-party catalogue and the menu** (D269–D272, `internal/ops/offers.go`, `internal/catalog/firstparty.go`,
  `menu.go`, docs/catalogue.md): what MetaRush does is about fifty abilities, each an `ops.Offer` annotation next to
  its operation (stable `ID`, group, title, sentence, fixed `Input`, `Needs`: a first-run item, OS, programs, keys,
  Claude, `ops.RegisterReady` checks); `firstparty.json` is generated (`METARUSH_UPDATE_CATALOGUE=1 go test
  ./internal/catalog -run TestFirstPartyCatalogue`; IDs never vanish: `catalog.RetiredFirstParty`), names
  `metarush/<id>` = `madeWith`; a preset runs its op's own input with its fields fixed (`RunOwnInput`). Entries say
  `worksHere`/`missing` from `firstrun.Works` (D256) and the rest; `catalog.Searchable` folds an ability that is an
  operation as it is into that operation, so find lists each operation once. `catalogue.list|show` (`metarush
  catalogue …`) and Library → Abilities show built-in and added side by side; browser run `scripts/catalogue-e2e.sh`
  (ports 5500–5509).
- **The review desk: AI apps propose, people decide** (D248–D252, `internal/ops/drafts.go`, `packet.go`,
  `internal/mcp/propose.go`, `web/src/screens/ReviewDraft.tsx`, docs/review-desk.md): `mcp serve --propose` (CLI
  `--propose`/`METARUSH_PROPOSE`; `App.ProposeOnly`, checked in `ops.Call`: nothing mutating or people-only but
  `draft.save`) saves every change as a draft (`drafts`, `drf_`, migration 48), from two allowlists only: version
  drafts (`versionDraftOps`: a sheet's check, a plan's `prepare`, a verb's dry run, `mix.set`/`sound.levels`/
  `voiceover.line` without apply) saved beside the cut (`commitDraftRevision`, `Revision.draftId`, origin `draft:<id>`;
  only `commitRevision` and an approval move heads), and change drafts (`changeDrafts` in `draftchanges.go`: each
  field and how it reads — names for projects/versions/clips/pictures, kept by ID; full words; cards part by part;
  an unlisted field refuses). Everything else is refused (`whyNotDrafted`, `ops.Draftable`, `paidOps`). Drafts are
  priced at save and approval (`draftPrice`), cleaned of control characters (`cleanText`), capped (50 waiting per app,
  `DraftCopiesPerHour` review copies). `Actor.agent` (`--agent`, `METARUSH_AGENT`, MCP clientInfo →
  `models.AgentSlug`) names the app; `madeByAgent` generalises `madeByClaude`. `review.waiting` adds draft versions
  (`why: draft`) and `changes`. `draft.decide` (HumanOnly; `revision.approve` of a draft's version routes there):
  approve claims the draft (→ `approving`, check-and-set), prices it again, re-checks the version through `prepare`
  with today's facts (its own version when nothing changed, else a new one in the app's name; overlaps name the
  clips), then marks it approved or puts it back with `tried`. Unfolded saves record `Revision.includes`; a head
  carrying a draft closes it as `in_cut` (`closeIncludedDrafts`, `headCarries`). The MCP answer to the app's next
  call carries decisions once (`decision.seen`). `review.packet` is the one page (Markdown escaped, `mdText`).
  MCP lists take an `Access` (ReadOnly, Propose, Changes): propose lists offer only draftable changes and `draft_list`.
  Tests: `drafts_test.go`, `propose_test.go`, `scripts/mcp-agents.sh` (fake codex/cursor, MCP Python SDK),
  `scripts/review-desk-e2e.sh` (ports 5450–5459).
- **Outside reviewers: Share for review** (D313–D317, `internal/reviewlinks`, `internal/media/share.go`,
  `internal/testutil/fakebucket`, `web/src/components/ShareForReview.tsx`, docs/outside-reviewers.md): a review link is
  a private, signed, expiring static page in the workspace's own bucket (`review/<128 random bits>/`: `index.html`, a
  copy of the version's newest review copy, its still, slots `c0..c7.json`), never a way in — no Funnel (D129), the page
  holds no key and no MetaRush address; CSP by hash, bucket origin only, `no-referrer`. Every address is signed until
  the link ends (≤ 7 days, `media.MaxSignedTTL`); slots take one signed PUT of exactly `SeatSize` (`media.PutSigner`:
  SigV4 signs length and type, GCS `x-goog-content-length-range`); copies inside the bucket (`media.Copier`). Ops
  `share.plan|create|list|link|revoke|sync|settings|settings.set` (`metarush share …`): create/link/revoke people only
  (the review desk refuses), settings owners (off, owners only, max days); `review_links` (`rvl_`, migration 59).
  `share.plan` asks What may leave quietly, create with `leave.Check` for `leave.RevisionTouches` — place `reviewers`
  (D317) and `storage` when the bucket is cloud; kinds footage, voices, faces, pictures (+ words with the Script view or
  burned-in captions). The Script view is `ops.VersionLines` (pinned words, speakers names|labels|none). Comments come
  back pull-only: the daemon (`reviewlinks.Run`, 2 min; `share.sync`) reads slots whose `ObjectInfo.ETag` changed under
  a lease, keeps them as `comments` with `outside` (unique link+writer+n), author "<name> (outside)" via `outside`,
  cleaned (`ops.CleanText`), ≤ 100 per browser, ≤ 60 an hour per link (the rest wait), bell `outside_comments`; AI
  readers get them and the names quoted (`ops.QuoteOutside`, `OutsideName`, `AIReader`); a slot is its first browser's
  (`owners`). Ending (D318, `end.go`: `endLink` → `finish`) deletes page and video first, reads the slots one last time
  and deletes them only after that read (`lastRead`), again on every look until the signature ends (`removed`), and
  says only what it did (`endedWords`); links end by themselves when the media moves (`ops.ReviewLinksOnMove` in
  `storage.migrate`/`plan`), rules tighten (`leave.OnRulesChanged`, every look, `share.link`), consent is withdrawn
  (`generate.OnConsentRevoked`), owners turn links off, the project is cleared out (bell `review_link_ended`). The
  receipt lists links under `reviewers`; the ship gate decides too (`reviewlinks.Gate` → `ops.ShipGate(…, "review",
  nil)`; held: as a draft, or `anyway` with a note; tests stand in for it). Tests: `go test ./internal/reviewlinks/` on
  `fakebucket` (checks link and header signatures, signed headers, expiry; `Ahead` moves the links' clock); browser
  run `scripts/outside-reviewers-e2e.sh` (ports 5590–5599).
- **Exporting and restoring a workspace** (D329–D333, `internal/wsarchive`, `internal/ops/workspacearchive.go`,
  `internal/cli/workspace_archive.go`, `web/src/screens/settings/WorkspaceExport.tsx`, docs/workspace-export.md): an
  archive is a private folder (0700/0600) or `--tar` file: `manifest.json` (format, maker, the migration level as every
  applied version, per collection count/options/indexes/SHA-256, what stayed out and why), `collections/<name>.jsonl`
  (canonical Extended JSON v2, one document per line, `_id` order, streamed both ways), `media.jsonl` and, included,
  `media/` (a folder store filled by `storagemove`). Every collection is in `wsarchive.Catalogue` (as-is / transformed
  / left-out / in-manifest with a why; `TestEveryCollectionIsClassified` and the docs table keep it whole — **add new
  collections there**); unknown ones are named, credentials and computers' own state left out; strings are scrubbed only of
  secretpath's narrow credential shapes, the archive's own (any address's password, signing/key query parts, JWTs,
  Google client secrets and refresh tokens), `logbook.KnownSecrets` and `publish.Secrets` (≥ 8 characters; under 16
  only as a word of their own), never `_id`, `…Id` fields or unique-index fields (byte-exact unless changed; the
  manifest counts them) — **docs/workspace-export.md lists exactly what is looked for: keep it in step**; running and
  paid or outside waiting work is held for a person (`holdJob`, `paidWork`: add new pinned kinds that act outside). Export only reads (`app.App.ReadOnly`; the CLI's log entry waits
  in the spool; `ReadLevel`, refuses `newer_database`); another database's address only via `--db-uri-stdin|-env`.
  Restore (not an op; `metarush workspace restore`, `setup --restore`) only into an empty database and only
  plain collections the catalogue exports (`CheckCollections`: any collection option is refused): verify every
  checksum, the `restore_lock` marker (`store.CheckNoUnfinishedRestore`: `app.OpenWith` — every command, worker,
  MCP — Migrate, init, the daemon and export refuse a database it holds; doctor names it), referenced media looked for,
  emptiness again, MetaRush's own indexes from its migrations at the archive's level (`store.DB.MakeForRestore`; the
  archive's index list only reported), each collection checked by hash as it goes in, included media after What may
  leave (`MayStore` → `ops.LeaveStorage`), the level last, `MigrateRestoring`, every archive record still there by
  ID, background deletions paused (`workspaces.deletionsPaused`, `store.DB.DeletionsPaused`: the Trash schedule,
  review links' endings and file deletion, log retention check it — **a new automatic deletion must too**; lifted by
  `workspace.adopt`), `RecordRestore`; the same restore started again re-checks the copy (exact hashes before
  migrations, IDs after) and continues; keeps the workspace ID; never deletes. `workspace.export|exports|adopt`
  are OwnerOnly + HumanOnly + `archivePeopleOnly`; the CLI needs a person's terminal (`personAtTerminal`); dashboard
  archives go to `<home>/archives`. Browser run `scripts/workspace-export-e2e.sh` (ports 5670–5679).
- **Moving the database** (D334–D338, `internal/dbmove`, `internal/ops/databasemove.go`, `databasemove_state.go`,
  `internal/store/movemarker.go`, `internal/store/guard.go`, `internal/cli/database_move.go`, `internal/api/database.go`,
  `web/src/screens/settings/DatabaseMove.tsx`, docs/database-move.md): `database.plan|move|switch|abandon|move.status`
  (`metarush database plan|move|switch|abandon|move-status`; move/switch/abandon OwnerOnly + HumanOnly +
  `databasePeopleOnly`, the CLI a person's terminal; plan owners, any surface). Targets `here` (item 8's server via
  `localdb.Prepare`, `localdb.Secrets*`; refused while other computers use the workspace unless `onlyThisComputer`) or
  `saved`: another MongoDB whose address is **never** an op input, argv or log — the CLI's `--to address|env:NAME`
  and the dashboard's form keep it via `SaveMoveAddress` (keychain `database-move`, or `<home>/database/move.secret`),
  `rememberAddress` redacts it; `forgetUnneededAddress` drops it after a plan that isn't ready, a refused move or an
  abandon (kept while a stopped move or a switch needs it, and for our own local sign-in). The plan checks What may
  leave first (`ops.LeaveDatabase`: storage, words + voices + faces — the database holds the voice/face prints —
  cloud per `leave.DatabaseKept`; a restore asks `leave.ArchiveDatabaseCheck` via `RestoreOptions.MayHold` before any
  record), names every non-revoked computer whose record doesn't show a build that knows moves (`knowsMoves` ==
  `app.MovesMark(version, commit)`, written by every process before its first write via `OnFirstWrite`, or version ≥
  `ops.FirstMoveVersion`; invited/unknown count; an older build in the activity log after the record's, `olderBuildLogged`), then the target (`dbmove.CheckTarget`: reads only, `connectionStatus` privileges). The move:
  `StartMoveMarker` (`database_move` in the old database: moving → moved | abandoned, `Holding`, `Switched`) freezes
  it — `store.DB.C` returns a `Coll` whose every write refuses while frozen (`guard.go`: every connection looks at the
  marker, trusted `MoveLookTrusted`, fail closed when unreadable during a move; a running change's pass — `Admit`,
  `WriteThroughFreeze` — writes only after `listPassage` lists it in `busy`, never once `copying` (`MoveCopying`, set
  only while nothing is listed) or moved, never after the change ended), `dispatch` refuses mutating ops up front
  (`refuseWhileMoving`, `E_MOVING` reason database_moving|database_moved; the move's own ops exempt) and `Admit`s the
  rest (counted, published in the marker's `busy`; the move waits for them), job leases end by `$$NOW` + `LeaseDuration`, every
  time compared across computers is the server's (`$currentDate`, `$$NOW`: `MoveAlive`, `SeenWithin`,
  `LiveLease`), `app.Held` keeps audit/TouchInstallation out and the log in the spool
  (`logbook.Options.Hold`), `Migrate` refuses, daemons `watchForMove` and restart into `serveHeld` (`api.NewHeld`,
  `heldGuard`: reads only, no background work, `HoldForMove` acks), the terminal worker stops; `waitQuiet` waits for
  every computer seen within `RunningWithin` to ack and no live leases. `dbmove.Copy` (the restore lock, kind `move`;
  `MakeForRestore` before the first record — never the source's index list, `IndexesNotMade` says it; `wsarchive`
  `EachDocWhere`/`Inserter`/`HashWhere`, never the archive scrub; resumes after the copied prefix; TTL collections
  upserted by `_id` without records ending within 15 min, no `collMod`; `LeftOut`; level last; `ErrChanged`) →
  `dbmove.Recheck` under the freeze (late collections, source hashes again) → marker `moved` **before** `switchDatabase` (keychain/.env, previous kept aside, config.yaml's
  `database` section, checked as loaded) → `recordMove` into the new database. No job (the old database is frozen):
  progress is `<state>/database-move/<id>.json`; the dashboard starts `database move --dashboard-move <id>` with
  `tools.Start` and a one-time grant like the media's move. Never deletes either database. **Something written late**
  (D352–D359, D371, D374, `internal/dbmove/late.go`, `internal/ops/databasemove_late.go`, `web/src/screens/settings/DatabaseLateWrites.tsx`):
  the last look's read keeps every record's fingerprint (`RecheckPrinting` → `<state>/database-move/<move>.prints`)
  and the move keeps the old address (`keepLookAddress`: keychain `database-late-<move>` / `database/late-<move>.secret`,
  never `forgetUnneededAddress`'s); the moving computer looks right after its switch (`startLateLooks`, also after a
  `database.switch` of its own), then at `LateLookAfter` (15 min, 1 h, 24 h; the daemon's `RunLateLooks` →
  `LookDueLate`, retries, ends `LateLookGrace` after; one at a time, `lookingUntil`) and on demand (`database.late.writes`
  `look`, owners, any surface, "-" in readsShow); `dbmove.Look` only reads (`dbHash` per kind — Atlas Free/Flex don't
  offer it, the plan says so — then changed kinds streamed in `_id` order and merged with their fingerprints; `Each`,
  `MaxBytes`/`MaxTime`/`Pace`: `lateBackground` 256 MB, 5 min, stops and says so, the next look goes on (`From`,
  `Position`, `latePass`); `lateAsked` 16 GB, 2 h). **"Nothing was written" only after a complete look**: `complete`
  (a pass read everything, nothing only counted, no failed look since) → `LateLookView.Checked`, `AllClear` (over and
  complete: what a script trusts), `lateSummary`, `tellOwnersIncomplete`. **An all-clear needs a complete pass begun at
  or after the last look's due time** (`lateFinal`; `lookLate` starts a pass over past that time, so a sleeper's write
  into a record already passed, or a kind made mid-pass, can't slip by). `LateLookGrace` counts from the first look tried
  once the last was due (`graceFrom`, `lateGraceOver`, `LookUntil`). No looks on record: `lateNoLooks` says it can't tell
  (a finished move's lock) or that no move came here, never "nothing was written". An old database with no records is
  one finding with its own words (`Emptied`, `Gone`, `lateEmptiedWords`);
  records `late_looks`/`late_writes` (migration 62; `late_writes` left out of exports), bell `late_writes` only when
  something is found; a look that saw everything settles what it didn't find (`seenBy`, copy dropped); it keeps
  `lateKeepPerLook` new records (the rest counted, `more`; later looks go on past them). **Secrets**:
  `wsarchive.ReportOnly` (left out, in the manifest, `Entry.Secret` or unknown: **mark a new kind that holds secrets**) keeps
  no copy or ID, shows nothing, never carries; `show` leaves out `models.HiddenField` (`NeverShown`: every `json:"-"`
  field, a test keeps it whole; `LooksSecret`: any secret-looking name, at any depth) and runs the export's scrub. Copies go on a decision, and after `LateCopyKeep` (`DropOldLateCopies`, checks
  `DeletionsPaused`). `database.late.show|carry|leave` OwnerOnly + HumanOnly + `databasePeopleOnly`, the CLI
  `lateAtTerminal`; `lateGroupBy` entries (one clip's transcript…) go whole or not at all; carry = a claim on the
  fingerprint, then check and set on the whole record (`$$ROOT` vs `$literal`), gone records never removed;
  the address and prints dropped after the last look that saw everything (`endLateLooks`). Browser run
  `scripts/database-move-e2e.sh` (ports 5690–5699; a late write by `scripts/latewrite`).
- **Handing a computer's role to another computer** (D344–D348, `internal/handover`, `internal/assistant/handover.go`,
  `claudepaths.go`, `web/src/screens/settings/Handover.tsx`, docs/handover.md): `machine.handover.plan|status|export|
  receive|abandon|clean` and `machine.takeover` (`metarush machine handover …`, `machine take-over`), OwnerOnly +
  HumanOnly + `peopleOnly` (never Auto, Claude, MCP, AI apps or drafts); export/receive/clean/take-over LocalOnly and the
  CLI a person's terminal (`handoverAtTerminal`). Records `handovers` (`hov_`, migration 61: one `active` per `from`).
  The handover file is a gzip'd tar in age's scrypt mode (`age -d` opens it; `--no-passphrase` only after the person
  types `unprotected`), `handover.json` first (sizes and SHA-256s; receive checks it before unpacking and holds every
  entry to it), 0600, placed by a hard link or an `O_EXCL` create (exFAT), the folder checked writable before anything
  is marked, never in a sync folder or repository; the passphrase only through `handover.WithPassphrase` (never an op
  input, argv or log). Sessions are found by ID (`FindSession`: `projects/*/<id>.jsonl`) and placed where the
  background MetaRush's Claude Code looks (`ClaudeConfigFor`: recorded at its start, else `CLAUDE_CONFIG_DIR`, else
  `$HOME/.claude`; under go test only `CLAUDE_CONFIG_DIR`) + `projects/<ClaudeProjectKey(the working folder as the
  disk spells it)>/<session>.jsonl` and its folder. **One history**: a message claims its conversation first
  (`mayContinue`: held here, no `handingOver`, set working with `holderPid` in one write); `MarkHandingOver` marks only
  idle ones with no card running; cards run only where their conversation is, claimed pending → running by check and
  set (`claimCards`); `save` matches `installationId` once stored (a computer lets go of a conversation that moved);
  inbox notes wait for the holder. States exporting → exported → receiving (receive's claim; abandon only once the
  receiver passes `quietEnough`, or a take-over of it ends it: `endHandoversTo`) → received → done | abandoned; every
  final write's match is checked; a role that came back ends the computer's own earlier handover (`settleOvertaken`).
  A mark never outlives its handover (export's `putBack` on every stop and a look again after marking, abandon always
  unmarks, `Look`'s `repairMarks`); cards are claimed by writing only their states. Receive takes the role first, then
  `finishReceive` moves the work and folders (`workMovedAt`; the receiver's `Look` finishes a stopped one); the hosts'
  `Look` re-pins work left on a client to its role holder (`rehomeWork`; never a live lease or `ownKinds`). Receive/take-over `AdoptConversation` by check and set;
  `Conversation.moved` tells Claude once where files are now, and `moved.summary` (session missing or unreadable, also
  found at spawn) starts a new session primed with `summaryOf`. Work moves by check and set on `$$NOW`
  (`moveWork`: queued and lease-ended jobs, never started `import`/`generate` while their computer runs, never
  `storage_move`/`resource-upload`; recipe runs free by `recipes.LeaseFree` — run leases are `$$NOW` + 5 min, saved in
  one write; triggers write only while still their computer's). `RoleOf` (the newest settled handover involving a
  computer) → host | stepping-down | client; the daemon `Look`s at start and `Watch`es, and a client runs
  `serveClient` (`api.Server.RemoteOff`, no worker or loops, `clientWatch`: role, database move, config.yaml; doctor
  and every page say so); work asked for there is pinned to the role's computer (`app.WorkHost`/`PinWork`) or refused
  where it needs this computer's own keys, sign-ins or files (`NeedsThisComputer`, `handed_over`) — new pinned kinds
  pick one; a post being sent is work the old computer finishes before stepping down. Take-over only when the gone
  computer's `lastSeenAt` and `lastUsedAt` (every writing process, `sayBuild`) are older than `TakeOverQuiet` (30 min,
  `SeenWithin`) with no live lease and a person types its name; it reads a plain copy (`scanCopy`: session files, `conversations/<id>`, only folder fields of
  `config.yaml`) or a handover file. `clean` removes only after a confirmed receive (and the handover files made there,
  if unchanged). Tests: `go test ./internal/handover/`, browser run `scripts/handover-e2e.sh` (ports 5710–5719).
- **Testing and coverage** (D194, D195, docs/testing.md): five layers (static → Go tests on a real MongoDB → several
  machines in one process (`tests/e2e`) → browser runs with fakes → Mac-only and live services); `scripts/check.sh
  --quick|--full|--release` runs them; test databases are copies of a per-process template and stale ones are swept
  first; every browser run sources `scripts/lib/e2e.sh` (stops what it started, drops its databases, keeps a failed
  run's folders a day); coverage from Go tests, coverage builds in browser runs (`internal/coverflush`) and the
  dashboard (c8; Chromium's coverage of its pages) goes to Codecov over OIDC; `coverage-baseline.json` is the ratchet.

## Layout
`cmd/metarush` (binary) · `internal/*` (Go; see architecture.md) · `skills/` (embedded Claude Code skills) ·
`plugins/` (generated from `skills/`: the Claude Code plugin and the skills for other apps) ·
`schema/` (generated JSON Schema + TS — regenerate, don't hand-edit) · `video/` (Remotion package) · `web/` (dashboard).

## Ports and names
| | |
|---|---|
| Dev dashboard (Vite) | 4840 |
| Dev daemon | 4841 (installed default: 4845) |
| Remotion Studio | 4842 (also `metarush motion studio`) |
| Dev MongoDB | 127.0.0.1:27018, replica set `rs0` (`scripts/dev-mongo.sh`) |
| The database on this computer (`metarush setup`) | 127.0.0.1:27150–27199, replica set `metarush` (`<home>/database`) |
| Dev database | `metarush-dev` (tests: `metarush-test-*`) |
| Env prefix | `METARUSH_` |
| Dev installation home | `.dev/home` — wrapper `.dev/mr` (= `bin/metarush --home .dev/home`) |

`start.sh` and the scripts find Go and Node on PATH, else in Homebrew's folders; `METARUSH_TOOLCHAIN_PATH` (folders,
like PATH) goes first for a toolchain kept elsewhere.

## Build checks (run before committing)
`scripts/check.sh --quick` before a commit, `--full` before a merge or a release (`--release` adds the snapshot
dry run): it runs the checks below in order, logs each step in a folder it prints and ends with a table; exit 1 on
any failure. What runs where and when (CI, nightly browser runs, Mac-only), coverage and cleanup: docs/testing.md.
```bash
scripts/check.sh --quick|--full|--release                 # the set below, with a sweep of leftovers before and after (--full)
scripts/coverage.sh --test                                # the Go suite with coverage, per-package table, the ratchet (coverage-baseline.json)
go build ./... && go vet ./... && go test -timeout 45m ./...   # tests need scripts/dev-mongo.sh start
./bin/metarush schema export --dir schema && npm run gen:types   # after changing editspec/ops/models/api types (also writes schema/templates.json)
npm run typecheck && npm run build -w web
npm run test:motion -w video                              # motion graphics: import scan, archive, types, bundler (no browser)
(cd video && npm run render:selftest)                     # renders in the jailed browser, incl. motion and motion-security
./bin/metarush skills test                                # static; add --live to run real Claude Code sessions
scripts/acceptance.sh                                     # end-to-end acceptance on a throwaway local DB
scripts/secret-scan.sh                                    # no likely secrets in tracked files
scripts/interchange-check.sh                              # timeline export vs. OpenTimelineIO and the FCPXML/xmeml DTDs (temp venv)
scripts/mcp-check.sh --home <test home> [--changes] [--toolset edit]  # `mcp serve` (the core, a toolset, all) vs. the official MCP Python SDK (temp venv)
scripts/release.sh --snapshot                             # release dry run: every archive built and verified, nothing published
scripts/about-e2e.sh                                      # Settings → General and `metarush version` with an older second computer
scripts/playback-e2e.sh                                   # players through a database outage (D180); review copies and stills in the Edit tab and on a version's page (D187)
scripts/accounts-e2e.sh                                   # sign-in methods, invites, sign in/out, reset links, password change (D183)
scripts/delivery-e2e.sh                                   # delivery notes, translated captions, whose Claude answers, the receipt (D237–D241; ports 5410–5419)
scripts/ship-gate-e2e.sh                                  # the ship gate and promises: Held by…, a promise measured on a real render, Deliver anyway… with a note, the receipt (D298–D302; ports 5560–5569)
scripts/windows-e2e.sh                                    # the dashboard as Windows 11 uses it: Windows file names, folders, downloads, line endings, Ctrl (D247; ports 5440–5449; Edge when installed)
scripts/presenters-e2e.sh                                 # presenters with the fake D-ID and HeyGen: a test clip, approve, a clip, a stock clip, consent words, No presenters, retire (D273–D277; ports 5510–5519)
scripts/workspace-export-e2e.sh                           # Settings → Storage → Export the workspace, then a CLI restore into a fresh home and the dashboard on the copy (D329–D333; ports 5670–5679)
scripts/database-move-e2e.sh                              # Settings → Storage → Move the database with a second computer: the plan, changes refused while it moves, the switch on both, a late write found and carried over (D334–D338, D352–D359; ports 5690–5699)
scripts/components-e2e.sh                                 # only the required components, then Settings → Components and Getting started's Add it with a stand-in Homebrew (D324–D327; ports 5650–5659)
scripts/resources-e2e.sh                                  # the Add box, Inbox clips dragged onto project cards, the Sources "…" menu, Delete…, a nested cut (D79–D84; ports 5660–5669)
scripts/handover-e2e.sh                                   # two computers: the dry run in Settings → Storage, the handover file at a terminal, receive, the conversation going on in the same session, the old computer stepping down to a client, clean (D344–D348; ports 5710–5719)
```

## Conventions
- New operation = `ops.Def(name, summary, flags, fn)` with typed In/Out structs; add a CLI command that calls it;
  regenerate schemas/types so the dashboard sees it. Mutating ops audit via `a.Audit`.
- Errors: return `apperr` codes (stable; skills branch on them).
- `.github/copilot-instructions.md` is the short review guide Copilot reads on pull requests, drawn from this file:
  when a rule it summarises changes here, change it there too (this file wins where they differ).
- Edit timing lives only in `editspec` (Go). Remotion renders the resolved RenderPlan verbatim.
- External programs only via `internal/tools` (allowlist, typed args, no shell).
- HTTP payloads outside the ops registry are Go types listed in `internal/api` `ContractTypes`; the dashboard imports
  them from `@metarush/schema` (`api.*`) — never hand-mirror a Go struct in `web/`.
- The dashboard assistant (`internal/assistant`) only proposes; keep its allowlist read-only (changes become action
  cards a person runs on the human channel, never assistant-channel ops) and test UI changes with a fake `claude`
  (`internal/assistant/fakeclaude`, or `tools.claude` pointing at a script); `web/scripts/assistant-e2e.mjs` runs a real one.
- **"Recipe" is the Recipes feature's word only** (D210): a fixed copy has **fix steps** (`fixSteps`) named by their
  **fingerprint** (`fixes.Fingerprint`, the same hash as before, so every copy made stays in use); a generated clip or
  picture says **How it was made** (`madeWith`); a Remotion refresh has **refresh steps** (`refreshSteps`); voice
  clean-up has kinds of clean-up. Migration 37 renamed the stored fields in place; until every computer runs a build
  with D210, readers take both names (the decoders in `internal/models/renamed.go`, `oldnames.FingerprintIs`/`MadeFrom`;
  the old name wins, as only older builds write it) and every write of a renamed field unsets the old one; old names
  live only in `internal/oldnames`. `TestRecipeMeansOnlyTheFeature` (`internal/plainwords`) keeps the word out of Go,
  TypeScript/JavaScript and skills outside the feature's files (`recipeFeature`) and names (`recipeNames`).

## Operating notes
- **Load**: run heavy checks with `nice -n 10` and `go test -p 4` (`-p 2` when other builds share the machine; `scripts/check.sh` uses `GO_TEST_P`, default 2); more than ~3–4 parallel builds starves real renders (the 45 s no-frame watchdog stops them) and has crashed the shared test mongod (127.0.0.1:27018). Test DB setup gives migrations 5 min (`internal/testutil`); `metarush init` gives them 10 min.
- **CI** (D194, `.github/workflows/`): `ci.yml` on every push (static, Go tests as four jobs from `scripts/ci-go-packages.sh`, dashboard and Remotion package checks); `browser.yml` nightly, on tags and by hand (the browser runs that work on Linux); `bootstrap.yml` (macOS) when the bootstrap changes and weekly; `secret-scan.yml` (gitleaks over the whole history) on every push to main and every pull request. Actions are pinned by commit SHA with the version in a comment; Dependabot (`.github/dependabot.yml`) proposes updates monthly. Golden files must not depend on the CPU (arm64 fuses multiply-adds, amd64 doesn't), and tests must not depend on the runner's environment (e.g. GitHub sets `HOMEBREW_NO_AUTO_UPDATE`). The Go jobs' MongoDB is a one-member replica set like the dev one (a standalone server has no change streams, so live updates fall back to polling), started by `scripts/ci-mongo.sh` with the dev one's settings (Docker gives a container 1,024 open files, which mongod itself calls too low; with Docker's defaults it went away part-way through the Go tests; after a failure `ci-mongo.sh report` says how it ended). It runs on the host's network (no port-forwarding hop, as on a Mac). A test's new connection once waited 8 s for its first answer while mongod kept answering the others; ten probe runs since saw at most 0.3 s, so `ci-mongo.sh watch` samples a new connection's first answer, the load, CPU steal and dropped connection requests every 2 s while tests run, and a failed job keeps mongod's whole log and the samples as an artifact. MongoDB 8 can't run on Linux kernels 6.19–7.0.13 (MongoDB's SERVER-125742), which GitHub's newer ubuntu-24.04 images have: there CI's server is 7.0, elsewhere 8.0. Ubuntu packages come through `scripts/ci-apt.sh` (the files kept in GitHub's cache: the runners' Ubuntu mirror is sometimes very slow). GNU tools differ from macOS's: `mktemp -d "${TMPDIR:-/tmp}/name-XXXXXX"`, never `mktemp -d -t name`; bash 4+ reports a `set -u` stop itself; git in fixtures and in MetaRush runs with `-c maintenance.auto=false -c gc.auto=0` (newer git maintains in the background after a commit and races the folder's removal).
- **Toolchain**: on Apple silicon use arm64 Go and Node (put them first with `METARUSH_TOOLCHAIN_PATH` when an amd64 one comes first on PATH): under an amd64 Go, ONNX (faces, voices) and Studio tests fail with "incompatible architecture" / "needs an Apple silicon Mac", and a stale-looking studio golden — not real failures.
- **Test databases** (D194): a test process migrates its first database and copies it as a template for every later
  one (`internal/testutil`; `METARUSH_TEST_DB_FRESH=1` migrates each); mongod-bound, so `t.Parallel()` doesn't help;
  share one app across subtests that only read status. Go's default 10-minute test timeout is too short for the
  assistant and ops packages on a loaded machine — always pass `-timeout 45m` (ops came close to 30 minutes under
  load before the template; 8.4 min at `-p 2` with coverage on a busy machine after). Every test process first drops the databases
  of runs that are gone (`internal/testutil/testdbs`); `go run ./scripts/sweeptestdbs [--drop]` is the same by hand
  (`--legacy` once for those made before run markers).
- **Browser runs clean up after themselves** (D194, `scripts/lib/e2e.sh`): every `scripts/*-e2e.sh` and
  `acceptance.sh` has `. scripts/lib/e2e.sh && e2e_run "$DB" <folders>` right after its exit trap and ends with
  `exit 0` (`scripts/lib/status.sh`: on macOS's bash 3.2 a stop on an unset variable would otherwise pass) — new
  scripts too (merge rule). On exit it stops what the run started (Chromium included), drops its databases and removes its
  folders when it passed; a guard cleans up after a run killed outright; kept folders (failures, `KEEP=1`) go a day
  later. `scripts/lib/e2e.sh list|sweep [--now]`.
- **Temp files**: macOS `TMPDIR` ends in `/` — build paths with `${TMPDIR%/}`. Remove e2e homes only through the
  library's sweep (only folders whose `config.yaml` names a `metarush-test-` database). Drop test DBs only with
  `go run ./scripts/droptestdb <uri> <name>`. A test that makes a temp folder removes it (check a package with a
  private `TMPDIR`).
- **Coverage** (D195, docs/testing.md): `scripts/coverage.sh` (merged report, per-package table, the ratchet against
  `coverage-baseline.json`: a package of ≥ 100 statements may not drop more than 1 point; `--update-baseline`);
  `COVERAGE=1` makes a browser run a coverage build (`-coverpkg` must include `./cmd/...`, or the binary writes
  nothing). CI sends coverage to Codecov through OIDC and never fails because of it.
- **People-only means a person**: `remote enable/allow`, approvals, consent and similar refuse non-interactive callers by design; never work around that (no fake TTY). Same for `tailscale serve`, killing processes and resetting branches — ask the person you work for.
- **Helpers in git worktrees**: a new worktree may start from `main`, not the branch you are on — merge the working branch
  in first. Give it `node_modules` as APFS clones (`cp -cR <main checkout>/node_modules node_modules`, same for `web/` and
  `video/`; the `@metarush/*` links are relative, so they resolve inside the worktree). E2E scripts use fixed ports:
  give each helper its own suites, and hold them while a full check run is going.
- **Releases**: `scripts/bump-version.sh minor|patch` → `git push origin main vX.Y.Z` → `scripts/deploy.sh` (verifies the daemon's version). Parallel feature branches merge cleanly when migration versions and port ranges are assigned up front; CHANGELOG entries always go under `## Unreleased`.
