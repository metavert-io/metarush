# Changelog

## Unreleased
- **Videos render on Linux computers without a graphics card**. Review copies there are encoded in software and
  drawn with Chrome's software renderer; they used to fail on Linux machines without an NVIDIA GPU (macOS is
  unchanged).
- **Move the workspace's database, checked before anything switches**. Settings → Storage → **Move the database**
  (owners) or `metarush database plan|move --to here|address` moves your projects, versions, transcripts and notes to
  the database on this computer, or to MongoDB Atlas or your own server. The plan says what moves and how long it
  takes, what each of your computers needs (its own sign-in for the new database) and what waits meanwhile, and checks
  that the new database answers, is empty and takes writes; it names any computer, even one that's off, whose MetaRush
  doesn't know about moves yet (update it or remove it first). While it moves, changes pause on every computer — the
  database connection itself refuses every write, changes already running finish first, and reading and playing keep
  working while every page says so — then every record is copied, checked against the old database, checked once more
  right before the old database is marked, and this computer switches. Times are the database server's, so a wrong
  clock changes nothing. The old database is kept, marked as moved: a computer still using it refuses changes until it
  switches with its own sign-in (`metarush database switch`, or one form in the dashboard), so the two never split;
  MetaRush never deletes it. A move that stops can continue or be abandoned. The new database's address is typed once
  or read from standard input, kept in this computer's keychain only while a move needs it, and never on a command
  line or in the log. A person moves it, never Claude, Auto or another AI app, and clients' What may leave rules keep
  their words, voices and faces (the database holds the voice and face prints MetaRush knows people by) off a database
  on the internet when they say so — a restore into one checks the archive's rules too.
- **Export a whole workspace, and restore it on another computer**. Settings → Storage → **Export the workspace**
  (owners) or `metarush workspace export --to <folder>` writes one archive of everything about the work — projects,
  versions, transcripts, comments, people and their consent records, Claude Code conversations and the activity log —
  with the footage listed by where it is kept, or copied in and checked (`--media included`); `--tar` makes it one
  file and `--plan` says what it would hold first. Passwords, keys and each computer's own settings are never in it,
  and anything in a record that looks like a key is taken out; every kind of record says whether it goes in and why,
  and a manifest checks every file. The archive is readable only by your account. An export only reads: it never
  updates the database, so it also works on a workspace an older MetaRush left, with a database user that may only
  read, and another database's address comes on standard input or from an environment variable, never the command
  line. On the new computer, `metarush setup --restore <archive>` (or `metarush workspace restore <archive>` into an
  empty database) checks every file first, puts the footage in place, writes everything, checks the copy against the
  archive, brings it up to date for this MetaRush and joins it — nothing is ever deleted, and the workspace keeps its
  identity. The copy deletes nothing by itself until an owner says it is now the workspace (`metarush workspace
  adopt`, or Settings → Storage), and work that costs money or acts outside the workspace waits for a person.
  Exporting and restoring are a person's, never Claude's, Auto's or another AI app's.
- **Change a spoken word, in the speaker's own voice**. Right-click words in a clip's transcript and
  choose **Say it differently…** (or `metarush cut say make <project> --clip <c> --from <s> --to <s> --as "fifteen"`):
  the cut says the new words in that person's own voice copy — only with their recorded consent for new words with
  the service that keeps the copy (`metarush consent record … --covers voice,new-words --service elevenlabs`, or the consent
  form's "It also covers"), for a voice a person named, never a child's or a public figure's. It's priced first and
  only a person asks for it and puts it in; it never runs by itself, and an AI app can't draft it. MetaRush sends the
  new words with the speaker's words around them, then checks on this computer that the recognizer hears exactly the
  new words and that the voice sounds like them, fits them between the neighbouring words over the room's own quiet,
  and lets you hear before and after. **Put it in the cut** saves a new version and a review copy; the clip and its
  transcript stay as they were, Undo puts back what was said. The change is marked everywhere: the quality check,
  the export's notes (with each platform's checklist), the receipt, captions files (a note in WebVTT; italics with
  `captions file --mark-changed`), the clip's transcript and the cut's. On request **the mouth matches** too
  (`metarush cut say mouth <change>`: lip sync of the stretch, with everyone's consent). If a consent behind the
  new words (or the matched mouth) is withdrawn, ends or doesn't cover the project of a video that plays them (a
  cut in another project nesting this one), a version that plays them can't go out — with the ship gate
  off too, and nobody can send it out anyway — until they're taken out; the quality check and captions notes say so.
- **MetaRush starts with only what it needs; everything else is a component you add**. MetaRush itself,
  a place for your projects and a storage folder are all it needs to start and open its dashboard. FFmpeg, Node.js
  and the Remotion renderer, transcription on this computer, the link downloader, PDF pages and the rest are optional
  components, each saying what it unlocks and how big it is. `metarush setup` offers them as a checklist with the
  suggested ones ticked (`--components a,b`, `--no-components`; `--json` says what each added unlocks); `metarush
  components` lists them and `metarush components add|remove <name>` adds or removes one; Settings → Components and
  Home's Getting started have Add it. MetaRush adds a component with Homebrew where it is, else with its own pinned
  download checked before use, else says how to add it yourself — never with sudo, and only when a person asks (never
  Auto, Claude or another AI app). A feature whose component is missing says so with the one step: "Rendering needs
  the Remotion renderer (about 400 MB). Add it in Settings → Components." The renderer's confirm says Remotion's
  licence, and a renderer you left out (or that setup by an AI app or a script left for you) never sets itself up;
  `metarush renderer install` is a request to set it up, from a script too (one you left out only with `--force`).
  What MetaRush downloads is checked file by file before it runs, so a program changed afterwards isn't run until you
  add it again. Doctor's fixes point at `metarush components add`, not the developers' `./bootstrap.sh`.
- **Share a version for review with someone outside the workspace**. On a version's page, **Share for
  review…** makes an expiring link — up to a week — for a client or a colleague without an account: they watch the
  version's review copy, read what is said line by line if you allow (with speakers named, as Speaker 1, 2…, or not
  at all), download it if you allow, and leave comments pinned to times, which come back to the version as its
  comments, marked "(outside)", and ring your bell. The page is kept in your own cloud bucket as a private, signed
  page, so nothing new can reach this computer; taking a link back (or its time running out) deletes the page and the
  video at once. Making a link is a person's step; owners decide in Settings → Storage → Review links who may send
  them and for how long; What may leave gains **people outside the workspace**, so a client whose things may not go
  to them can't get a link, and the refusal says why; where the ship gate is on, a version it holds goes out marked as
  a draft for review, or with your note; the receipt lists every link. Claude reads outside comments
  only as quoted feedback. A link ends by itself — its last comments in first — when the media moves, the client's
  rules or someone's consent change, or owners turn links off, and you hear why on the bell.
  `metarush share plan|create|list|link|revoke|sync|settings`. It needs media kept in a cloud bucket (S3-compatible,
  or Google Cloud Storage with a key that signs links).
- **Posting to Vimeo, LinkedIn and a podcast host, written as abilities**. Three abilities in `abilities/`
  say exactly what posting there will take — always a person's step, the video and its words going only to that
  service — and are marked not yet runnable: they wait for a version's final render and its delivery notes as
  inputs (and, for LinkedIn, a sign-in kept the way YouTube's is). Their notes say what each needs from you.
- **Bring files in from Google Drive or Dropbox without syncing them**. With your own rclone set up on the
  computer that runs MetaRush (rclone's own setup signs you in; MetaRush never sees it), `metarush import cloud
  dropbox:Shoot/Day 1 --list` lists a folder and `--name A001.mov` (or `--all`) brings files in: each fetched by its
  ID, checked against the folder's listing and added like the Add box does, saying where it came from. Dropbox
  works like Google Drive already did for recipes; `metarush doctor` says whether each is set up.
- **Post an approved version to YouTube, as you**. Once an owner registers MetaRush with their own Google
  Cloud project on the computer that runs it, each person signs in with their own channel in their browser there
  (`metarush publish connect youtube`), and can post an approved version: its final render, the delivery notes'
  title, description and chapters, captions in every language it has, private unless they say otherwise, with
  YouTube's AI-content setting when the video has AI footage or narration. MetaRush shows exactly what will be sent
  and what holds it first, and records every post on the version and the project's receipt. A version goes to a
  channel once at a time (two tabs or a double click send one video), its page shows how much has gone, and posting
  it again asks first; a post MetaRush stopped part-way goes on from where YouTube got to. Only a person posts,
  every time: Claude, Auto and AI apps never do. From a terminal: `metarush publish post "Garden walk" --version
  r3`.
- **Bring an edit in from another editing app**. Drop a timeline exported from another editor — an
  OpenTimelineIO file (.otio), FCPXML 1.8 to 1.11 (.fcpxml or an .fcpxmld bundle), Final Cut Pro 7 XML (.xml) or a
  CMX 3600 EDL (.edl) — in the Add box, and MetaRush makes it a new cut of the project: each clip matched to your
  footage (by MetaRush's own export, the same file, or its name, length and timecode), dissolves, fades, wipes,
  split edits, sound levels and speed kept, chapter markers as the version's chapters and other markers as notes on
  the clips. Footage it can't find is a plain question — use a clip, add the file, or leave it out — and what can't be
  carried over (titles, effects, level changes inside a sound…) is listed before anything is saved. From a terminal:
  `metarush import timeline rough-cut.fcpxml --project "Garden walk"`.
- **MetaRush as a Claude Code plugin**. `claude plugin marketplace add metavert-io/metarush`, then
  `claude plugin install metarush@metarush`: the video skills and a read-only MetaRush connection (MCP's core of about
  ten tools, so every tool can be allowed) in one install. It needs the `metarush` program — the plugin is
  instructions, not MetaRush — and `metarush setup` now says so, and that the plugin brings the same skills it
  installs (use one or the other). The plugin is generated from the skills (`metarush skills package-plugin`) and a
  test fails when it drifts; it carries an eval suite for `claude plugin eval`, which only a person runs
  (docs/plugin.md).
- **The skills for Codex and other AI apps**. `metarush skills install --agents` puts the same skills where
  Codex looks for them (`~/.agents/skills` with `--user`, or a project's `.agents/skills` and MetaRush's part of its
  AGENTS.md), in the Agent Skills format; connecting an app over MCP and keeping it to drafts is the review desk's.
- **Claude Code keeps the skills' descriptions**. Every skill wrote when to use it in a way that isn't valid
  YAML, which Claude Code's validator says it drops whole, description and all; the skills are fixed, and
  `metarush skills test` now fails a skill whose frontmatter doesn't read.
- **A publish check for the repository**. `scripts/publish-check.sh` runs the open-source-prep
  standard's `scripts/check-public.sh` (deny-listed terms from a local list nobody commits, real email addresses,
  home folders, phone numbers, authors other than the public one, and the history) and then MetaRush's own checks:
  work notes in the tree, tailnet host names and the repository's secret patterns. Each finding says where and what
  (docs/publish-check.md).
- **Lip sync, with consent**. `metarush ai make lip-sync --clip <footage> --from 12 --to 18 --sound <clip>`
  matches the lips of the people speaking in the footage to new sound — the groundwork for changing a spoken word.
  Every face in that part must be named, and everyone seen or heard there, and the voice in the new sound, must have
  consented to lip sync with that service (`metarush looks consent record … --uses lip-sync --services sync`);
  children and public figures are refused. Before sending, MetaRush looks at every quarter second of the part and
  stops if a face nobody named passes through. The first
  service is sync., with your own key, priced by the second of footage; what it was sent is deleted there after the
  download (its privacy policy lets it train on uploads, and the estimate says so).
- **Veo, through your own Google Cloud project**. Veo left the Gemini API on 22 October; MetaRush now makes Veo
  3.1 shots (and Veo 3.1 Fast's) through Google Cloud's Vertex AI, priced by the second ($0.10–$0.60 a second by
  model and resolution) and held to the same budgets, Content Settings, looks and consent as Gemini's. An owner sets
  it up once: a Google Cloud project with billing and the Vertex AI API on, a service account with the Vertex AI User
  role, and its key file given to MetaRush (`metarush providers set vertex --key-stdin < key-file`, never an
  argument). Videos come back in the answer, so nothing is left in your project. `metarush generate shot "…" --model
  vertex/veo-3.1-fast-generate-001 --seconds 8`. The AI services catalogue can also list a model reachable only
  through a gateway — shown with its terms, never offered.
- **An ability can be a provider of AI work**. An ability whose manifest says `provides: [upscale]` (or
  picture, voice, voice-change, sound-effect, music, background-removal) is offered, once an owner approves it, beside
  the AI services and the models on this computer: priced from its manifest, held to Content Settings, consent and
  budgets, started by a person when its runs need one, and run in MetaRush's sandbox as a run of that work. `ability
  check` says what each kind needs from the manifest. `metarush ai make upscale --picture logo --model ability-<name>/<name>`.
  Ruling a service out in Content Settings rules out the abilities that reach it too (by its hosts or its key; any,
  for an ability with an open network).
- **AI models on this computer: free, no key, nothing leaves it**. The same estimate that prices a cloud
  service now offers models that run here — "on this computer: free" — and they are recorded like any other AI work:
  the local recognizers (Parakeet, whisper.cpp) as a transcription provider beside the cloud ones; **offline voices**
  (Kokoro v1.0: 41 voices in English, Spanish, French, Hindi, Italian and Portuguese; Kitten nano: 8 small English
  voices; both Apache-2.0); a picture's **background taken away** (Apple Vision, on a Mac: the cut-out is your picture,
  not marked as AI-generated); and pictures made **2× or 4× larger** (Real-ESRGAN, marked as AI-made). A person sets up
  the ones that need a download, once — the files are pinned by their checksums (`metarush ai local`, `ai local setup
  kokoro-v1`). Content Settings that allow no AI services still allow models on this computer, unless they name them.
  In Studio → Sources → Generate: **Cut-out**, **Larger picture** and **Lip sync** join What to make. From a terminal:
  `metarush ai make background-removal --picture logo`, `ai make upscale --picture logo --scale 4`,
  `ai make voice "Welcome" --model local/kokoro-v1 --voice af_heart`.
- **Start from a recipe that ships with MetaRush**. Two starter recipes come with MetaRush: **a call
  becomes an episode with shorts** (give it a call recording and a title: Claude cleans the conversation up in its
  order, the voices are made clearer, captions follow your house style, up to three shorts of the call are picked,
  and sign-offs remind you of the chapters and show notes) and **a release tag becomes a release video** (two code
  slides and a diagram Claude makes from the repository at the tag, each saying its repository, commit, file and
  lines, narrated in an AI voice over a title slide and an end card in your layout pack). The Library's Recipes tab
  and Home offer them: **Add the starter recipes**, or **Use this one**. Each comes in as a draft you look at and make
  current before it runs. `metarush recipe starters [--add]`.
- **A sample first, before the expensive part**. A run that uses an expensive kind of AI work makes one small,
  priced sample of each first — a short line in the AI voice, one generated shot, a stretch of the call made clearer
  in the cloud — in a short cut of its own. You agree only to the sample's price, watch and listen to it on the run's
  page, then **Approve the sample** (the rest is planned at today's prices and asks for your OK) or say **Not quite…**
  (the run stops). You can skip the sample with a reason, which is kept. Making a run again samples only what
  changed (a new voice, new footage, a new shot). The sample's cost is on the project's receipt, even when the run
  stops, and its line and shot are used again, never paid twice. `metarush recipe sample <run> --approve`,
  `metarush recipe run … --skip-sample "why"`.
- **Shorts of any footage in a recipe's pack**. A recipe can make its shorts from a recording a run gives it,
  not only from a conversation: each short is a new cut of the run's project. A clearer-voice setting for a recording
  now follows the new footage that takes its place, and a recipe's AI voice can simply be the narration voice your
  Content Settings name.
- **Clean-up estimates match what is sent**: a sample's and the shorts' clean-up are priced as the sound is
  processed, and the clean-up of a long recording at most as long as the recording you give.
- **A version waits before it goes out until the quality check and its sign-offs are done**.
  With the gate on, delivering a version — or exporting its timeline into a client's delivery folder — waits while
  the quality check found problems, hasn't checked the final render (or checked it before today's promises), or a
  sign-off is still waiting; things worth checking never hold it. The version page says **Ready to deliver** or
  **Held by …** with a step for each: fix the problem (saved as a new version), check it again, tick the sign-off.
  A person can still **Deliver anyway…** with a note: it is kept with their name and the time, written next to the
  delivered video and shown on the receipt. Auto, agents and Claude never can, and while the gate is on, ticking or
  removing a sign-off is a person's too. New workspaces start with it on; older ones are asked once on the To-dos
  page and nothing changes until an owner answers (Settings → General → Before it goes out, or `metarush deliver
  gate on|off`); a client's Content Settings can say always or never for their projects. From a terminal:
  `metarush deliver ready <project> --version r5 [--json]` and `metarush deliver override … --note "…"`. A note
  covers exactly what the person saw (if something else holds it by then, they look again), every note is kept for
  good and goes out with the delivery, and the receipt says "allowed to go out" until it actually does. Moving a
  project to settings that would drop the gate, a promise or a house sign-off is an owner's, as a person; computers
  on an older MetaRush, which may deliver without waiting, are pointed out.
- **Promises made to the client are checked on every render**. Mark a rule as a promise — "captions
  on every video", "under 60 seconds for shorts", "logo in the last 3 s", "no music louder than speech", "nothing
  generated without disclosure", "no faces cut off", "at least 1080p", "30 fps" — for the project or every project
  using its settings, and every quality check measures it and says what it found in a sentence ("It lasts 1:12, 12
  seconds longer than a minute."). A broken promise holds the version; one MetaRush can't measure on this computer
  is only worth checking, and the version then says "Ready, but 1 promise couldn't be checked here". A disclosure
  promise needs the line MetaRush writes from the records ("Made with AI: the shot “Beach at dusk” (Google Gemini).")
  in the description, or a title saying so on screen; generated clips in cuts played inside this one count, and
  captions already in the footage count when MetaRush can read them there. When you tell Claude what was promised, it offers a **Keep this as a promise to the
  client?** card; what can't be measured stays a sign-off. `metarush project rules add … --promise`, `metarush keep
  promise "…"`.
- **Say what of a client may leave this computer, and MetaRush keeps to it**. Content Settings gain
  **What may leave**: for every project using them, which of the client's things — footage, voices, faces, words,
  pictures — may go to AI services (and which ones), abilities (and which places they reach), Claude and other AI apps
  (and only under a Claude account's commercial terms, if you say so), cloud storage and delivery. Or tick **Nothing
  leaves this computer**. Nothing set changes nothing. Every place that sends something asks first and says plainly
  which rule kept it here, whose settings hold it and who set it: AI work, generated video, the AI voice, voice copies
  and clearer voices from the cloud (asked again just before anything is sent); an ability's run (its plan says which
  inputs stay here, and MetaRush refuses its requests to places the client doesn't allow; the one service on this
  computer an owner let it use, ComfyUI for instance, isn't a place things leave to); Claude in the panel, Claude
  Code in a terminal and AI apps through MCP (no transcripts, captions, cut sheets, frames or contact sheets of that
  client, and the panel says so in a line); scene descriptions and fitting a script; footage added while the media is
  in a cloud bucket, and moving the media there; videos and timelines written to a folder a sync service uploads; and
  Recipes' files, which leave a client's footage out and say why; Remotion projects sent back to their repository or
  refreshed. Claude and AI apps are shown nothing of a client's unless every project it belongs to allows it (a clip
  in two projects follows both), and while any client keeps things from Claude, the panel's Claude reads only copies
  of what it may see. Anyone can keep more here; letting more leave takes a person who is an owner (never Claude, an
  AI app or a script) — so does switching a project to looser settings, or moving or linking a client's footage into
  a project with looser rules. From a terminal: `metarush content
  leave set --project <client> --keep ai=footage,voices --only claude=commercial`.
- **What left this computer, on every receipt**. A project's receipt now says what of it left and where —
  each AI service and what was sent, what Claude and AI apps were shown, the places abilities reached, deliveries to
  sync folders and Recipes' files, and cloud storage that holds its footage — with every time What may leave kept
  something here, and "Nothing of … left this computer" when that is so. `metarush project leaves <project>` is that
  part alone.
- **Share abilities between workspaces**. **Export to a file…** on an ability's page (or `metarush ability
  export <name>`) makes a file another workspace brings in with **Bring in an ability file…** on Library → Abilities
  (or `metarush ability import <file>`). It comes in as a draft: never approved or set up there, whatever it was where
  it came from (that is shown, as information). A person checks it on a computer there (`metarush ability check
  --version <name@version>`), then an owner approves it. A file that was changed, holds anything else or carries a key
  is refused.
- **A starter pack of seven abilities**. `abilities/` holds seven abilities to read, check, approve and
  set up: stock videos and photos from Pexels, public-domain and openly licensed media from Wikimedia Commons and the
  Internet Archive, sound effects from Freesound, English captions by whisper.cpp, and pictures from ComfyUI on this
  computer run today; Content Credentials with c2patool and DaVinci Resolve through its MCP server are written in the
  shape they need and say what MetaRush doesn't do yet. Each one's notes give its licences, cost, keys (Pexels and
  Freesound need a free one), what leaves this computer and what you install. None reaches anything until an owner
  approves it.
- **What an ability brings in keeps its licence and credit, and deliveries are ready to paste**. Stock clips,
  openly licensed pictures and sound effects say their licence, who made them and the credit line the licence asks
  for ("“Harbour light” by Sea Painter, CC BY 4.0, via Wikimedia Commons"). A clip records them as its rights, so the
  music rights question doesn't come up, and delivering a video that uses them writes the credits into its
  `.notes.md` file, ready to paste into a description.
- **An ability can use ComfyUI on this computer, once an owner agrees**. Abilities still never reach this
  computer or a local network, except one service an owner agrees to when approving the version (ComfyUI on port
  8188, for instance): plain HTTP to that port, through MetaRush, every request recorded. What the owner agrees to
  is the program listening there, shown in full when they approve — for Python or Node.js, also the folder it was
  started in and the script it runs, and for ComfyUI an answer only ComfyUI gives. Every plan shows it; another
  program on that port, or MetaRush's own, stops a run before it starts or while it runs. A person starts each run.
- **English captions for a clip in another language, made on this computer**. The English captions ability
  translates a clip's speech with whisper.cpp and keeps the captions with the clip: `metarush footage captions
  <clip> --language en` shows them, `--save` writes the file. Abilities can now read files a person sets up on each
  computer (a model: `metarush ability setup <name> --file whisper-model=<path>`, or the setup dialog, which shows where
  the file really is and what it is first), copied into each run and checked. Only owners give files, and never keys,
  keychains, browsers' saved data or files from the system's folders.
- **See everything MetaRush can do, and what works on this computer**. Library → Abilities now
  opens on a menu of about fifty things MetaRush does built in — understand footage, fix sources, make media (with
  each kind of AI work), bring things in, shape the cut, check and deliver — beside the abilities your workspace
  added. Each says **Works here** or what it needs first: a Mac, FFmpeg, a key for the AI service, Claude Code, an
  owner's approval or setting it up on this computer; priced ones say they are priced first, ones that use a key that
  the service may charge, and cloud ones what leaves your computer. Filters show only what
  works here, only what is built in, or only what was added. From a terminal: `metarush catalogue list
  [--works-now] [--group fix]` and `metarush catalogue show steady-footage`.
- **Agents find your abilities the way they find everything else**. `find` (MCP, `metarush ops find`) now
  lists the abilities your workspace added, with what each costs, what leaves the computer and whether it works
  here; one waiting for an owner or not set up here says so and can't be run. An agent runs one only through the
  same plan and confirmation a person sees, and never one that a person must start (it is told so, with the command
  to hand to you). An added ability can't take the name of one of MetaRush's own.
- **A presenter reads your lines on camera, in your consented look and voice**. Generate → **Presenter**:
  a person's own presenter — yours, a colleague's, a client's spokesperson's — reads a clip's sound or a narration
  line of your cut, so it speaks in the voice they agreed to (their voice copy, a recording in which every voice is
  named as them) or an AI voice that is no one's, never someone else's or a dub of someone else, while the service
  only moves the lips; priced first, the price on the button, landing in Sources as an ordinary clip marked as an AI
  presenter, with your own sound under it. A person's presenter is made at an AI service (D-ID first) from their
  approved look and a minute or more of them talking to the camera, with their consent recorded **on camera** —
  your own too — naming the service, in one take that also has them reading the service's own consent words, which
  MetaRush checks against its transcript before sending; their voice and every face in that recording, and in the
  training take, must be named as them once MetaRush has looked. A test clip comes first, then a person approves it.
  Retiring it, withdrawing the consent or the consent's last day passing deletes it at the service, stops clips
  waiting (a clip already being made isn't kept) and lists the cuts that use its clips. Stock presenters (HeyGen's)
  only where the service's licence covers commercial use and client work (ads flagged); a service that trains on what
  it is sent needs its opt-out recorded first and the consent, naming it, of everyone whose face or voice goes there,
  checked again just before sending; Content Settings can say **No presenters** for a client, also checked just before
  sending. An answer lost on the way back is never sent again: MetaRush looks for what was made and deletes it.
  Only names a person gave count for a presenter, a voice copy's sample, an own-voice dub, lip sync or a
  generated shot's faces: MetaRush now records who named each face and voice (a person, an AI app, Claude's Auto), and a voice named
  before this version is named again once. A consent's last day ends at midnight where it was recorded. A client's
  What may leave counts presenters: making one sends footage, a face and a voice; a person's presenter's clip, a face. Every presenter clip is disclosed: QA's note, the delivery notes, the receipt. From a
  terminal: `metarush presenter start|make|test|approve|retire|list|show|stock|opt-out` and `metarush ai make
  presenter --presenter <name> --clip <clip>`; see docs/presenters.md. The adapters are tested against fakes of D-ID and HeyGen.
- **Consent says what it covers and where it may go**. Recording someone's consent now says whether it covers
  generated shots, their voice and a presenter, and which AI services may keep the recording; records made before
  cover shots and voice as they did.
- **Shorts from any footage**. Ask Claude for shorts (the button on a project's Moments, or just ask in the
  panel): it reads what your footage says and where the picture changes, picks complete moments with you and proposes
  each as a card. **Make the short** turns it into a new cut of the project for phones (tall), framed as your house
  style says (the crop follows whoever moves only when you ask for it), captions on in your house style's look (else
  Big and bold), named from its first words; you watch, change and approve it like any other cut. From a terminal: `metarush footage short <project> --clip <clip>
  --from 0:12 --to 0:41` (several stretches with `--piece`). Shorts from call recordings stay `conversation short`.
- **MetaRush from Windows 11, in Edge or Chrome**. There is no Windows build: Windows people open the
  dashboard of the Mac or Linux computer that runs MetaRush over Tailscale, and docs/windows.md says how to set it up
  and what works. Downloads now get names every computer can keep — a name with a colon or a question mark arrives
  with a dash in its place, and names with accents or other scripts arrive whole (they used to come out garbled) —
  and Windows' own files (Thumbs.db, desktop.ini, a Recycle Bin) are left out when you add a folder.
- **Each clip is written down in the language it is spoken in**. MetaRush now listens for a clip's language
  before writing its words down — where its speech is, not its first seconds — and English goes to the English
  recognizer while Spanish, French, German, Portuguese, Japanese and about 90 other languages go to whisper.cpp in
  that language. Until now every clip went to the English recognizer unless the whole workspace was set to another
  language. A clip's info card says "Spoken in Spanish (MetaRush heard it)" with **Change…**, which writes its words
  down again in the language you pick; when MetaRush wasn't sure it says what the clip sounded like and offers that
  language. `metarush footage language <clip> [--set es]` does the same. The quality check listens to a render in
  the language of its clips. Setting `transcription.language` to one language in config.yaml turns the listening off.
- **Pauses and words in other languages land where they are said**. In languages other than English, words
  used to start up to two seconds early, inside the pause before them, so pauses hardly showed and a cut in a pause
  could clip a word. Words now follow the sound, and pauses are as long as they are. Thanks and subtitle credits that
  whisper.cpp invents over silence in Spanish, French, German, Portuguese and Japanese are dropped, and Japanese and
  Chinese words are short pieces you can cut between, find and quote, shown without spaces. Measured on public-domain readings in
  five languages.
- **Six caption looks, chosen per cut**. Captions come in six looks drawn in your brand's colours and font:
  Clean, The spoken word lights up, Boxed, and new **Big and bold** (a few large capitals at a time, for phones),
  **A box follows the spoken word** and **Plain subtitles**. Pick one under the player on the Edit tab (Undo puts
  the captions back), or `metarush cut captions <project> --look bold`; `metarush captions looks` lists them. The
  house style in Content Settings offers every look, and the quality check notes when a cut's captions differ from
  the house style, with the change that follows it.
- **Any AI app can propose changes; only people decide**. Connect Codex, Cursor, Claude Desktop or Claude Code
  with `metarush mcp serve --propose` and it reads and checks as before, but each change it asks for is saved as a
  draft for you to review: a new version of a cut stays beside the cut (it plays and gets a review copy), and a
  comment, a name, a rule, the spelling list, the house style or the brand waits until you approve it. A draft
  names every project, version and clip it touches, shows every word in full — a brand draft every part it changes,
  the colours a new logo brings included; a spelling every place it corrects — and is checked again when you
  approve, so it does exactly what it showed or nothing; changes it can't show that way — spending money, processing footage, bringing in another project's
  footage, anything that is a person's — are refused with a plain reason. Each draft says which app made it and why
  (`--agent` names the app; otherwise the name it gives itself). An app that runs commands can be held to proposing
  too: `--propose` or `METARUSH_PROPOSE=1`, with `metarush draft save` for its changes.
- **Every app's drafts in Awaiting your review**. The Studio page and `metarush revision waiting` now list the
  drafts AI apps propose beside the versions waiting: what changes in words, the review copy MetaRush makes by
  itself, what the checks found and what it costs. Open one to **Approve** (it is checked again against how things
  are now — a clip put in the Trash keeps it out — then a version goes into the cut, keeping changes saved meanwhile
  where they don't overlap; approving happens once, even with two clicks), say **Not quite…** with what should change
  (the app hears it on its next call and its next draft takes the old one's place), or **Turn down** — people only, in
  the dashboard or with `metarush draft approve|not-quite|turn-down`. Approving a draft's version on its page does the
  same.
- **One page to hand a draft to someone**. Every draft and version has a review page — what changed and why,
  the checks, the cost, who made it and what's still waiting — that copies or downloads as Markdown;
  `metarush revision packet <draft or version>` prints it (`--markdown`, `--out`).
- **Changes on a draft keep the draft**. Editing a draft's version, or an app building its next draft on its
  last one, keeps the draft's own changes; once that goes into the cut, the draft leaves Awaiting your review as in
  the cut.
- **Record your screen, camera and voice in the dashboard, with the teleprompter**. **Record your
  screen** in the Add box records a window, a tab or a whole screen, your camera if you want it and your
  microphone, each as its own file sent to MetaRush while you record, so a closed tab or a dropped connection loses
  nothing that was recorded (the next time the recorder opens it offers to send the rest and keep it). The
  teleprompter shows your script (the project's or one you paste) at an easy pace while you show another window, and
  can float above other windows; it never shows in what you share, and a whole-screen recording says so. Pause stops
  everything; Stop adds it to the Library with the screen, the camera and your voice lined up on one clock (checked
  by sound) as one conversation in the project, and **Start a cut** shows them with the camera in a corner, side by
  side, or one of them. Works from another computer over Tailscale. `metarush recording list|show|finish|link|cut|discard`.
- **A microphone or camera that never answers no longer leaves a recorder waiting**. Nine seconds after you
  allow it, the narration recorder and the screen recorder say which device didn't answer and let you choose another
  (the browser's permission question isn't counted).
- **MCP clients get a few tools that reach everything**. `metarush mcp serve` now offers a core of
  10 tools (12 with changes allowed) instead of one per operation: `find` names what does a task you describe in
  plain words, `describe` shows how to use it, `run` does a read and `run_change` a change, beside everyday tools for
  projects, search, cut sheets, review copies and quality checks — about 1,400 tokens before a client's first call.
  `--toolset review|edit|library|admin` offers one kind of work as direct tools (related ones as one tool with a
  mode), and `--toolset all` keeps one tool per operation. No tool both reads and changes, so you can always allow
  the reading tools in your client and keep every change asking you; what only a person may do is refused, and
  changes need `--allow-changes` and a confirmation. `metarush mcp tools` (and its `--json`) now lists the core
  unless you name a toolset (`--toolset all` for every operation, as it listed before).
- **Find what does a task from the command line**. `metarush ops find make a review copy` lists what does
  it, best first, one line each; `metarush ops describe <name>` says how to use one.
- **Claude Code keeps every video skill in view**. The skills' descriptions are shorter (7,236 characters
  in all, from 17,385), so Claude Code no longer drops any from its list; what they said is in the skills
  themselves, and each one leads anything it doesn't cover to `metarush ops find`.
- **A set of graphics per client, chosen once**. A project's Content Settings now hold a layout pack: which
  template draws each title card, lower third, end screen, quote card and chapter card, and how call recordings'
  picture-in-picture and side-by-side layouts look — a round camera bubble in a corner, a line in the brand's colour,
  the brand's background between people — all in the brand's colours, font and logos. Until you choose one, cuts use
  MetaRush's own pack in your house style. In a cut, name a graphic's part (`- intro: title card at 0:00 for 3 s` in a
  cut sheet, or `"role": "title-card"` in a plan) and MetaRush draws it with the pack; Claude does the same. Settings →
  Layout pack previews every part in your brand and offers the latest cuts the change; `metarush content pack
  show|set|apply|preview` does it all from the command line.
- **Claude proposes a client's graphics; you decide**. Ask in the Claude Code panel: it writes the custom pieces as motion
  graphics, which you approve one by one as always, and proposes the pack on a card only you decide — never Auto. It
  becomes the pack once its graphics are approved and you accept it (on the card, under Settings → Layout pack, or
  `metarush content pack accept` in your terminal); a pack proposed before a later change can't undo it.
- **An end screen with room for the next videos**. The new end screen graphic puts your headline and logo
  beside empty space for the videos and subscribe button a video platform places over your last 5–20 seconds.
- **The quality check notes graphics that stray from the pack**: a part the pack doesn't have, or one drawn
  with another graphic or look than the pack's — each with the change that follows the pack.
- **A tool-selection check**. `metarush skills eval` scores how often MetaRush's search picks the right
  operation for 166 everyday requests: on the requests it was not tuned on, 85% first and 93% in the first three;
  `--live` asks real Claude Code the same requests, with the skills or an MCP toolset, using your Claude account.
- **Remotion refreshes never reach this computer's own services**. A refresh's steps still use
  the internet, but not MetaRush's database, dashboard or other services on this computer: on a Mac nothing on this
  computer's loopback address or MetaRush's ports, and nothing listens; on Linux the steps get a network of their own
  and reach the internet through MetaRush. Each step starts with no terminal and what it leaves running stops with it,
  and the new version is made from MetaRush's own copies of what the steps wrote, never following a link. A step that
  serves something to itself on this computer's loopback no longer works on a Mac.
- **Abilities: add what MetaRush can do without a new MetaRush**. An ability is a folder with an
  `ability.yaml` — what it does, what it runs, its inputs and what it brings back, its cost, the keys and places it
  uses and what leaves this computer — and the script or program it runs. `metarush ability check <folder>` runs it
  once in this computer's sandbox with the network off; an owner reads what it may do (what it installs, what it
  reaches, its keys and where each goes, what leaves, what it costs, its licence) under **Library → Abilities** — To-dos
  and the bell ask — and approves that exact version; a person sets it up on each computer (`metarush ability setup`:
  the programs it runs, pinned; keys typed by an owner); then members run it (`ability plan` shows what a run does and
  costs first; `ability run`, or **Run…**). A run happens in the sandbox, reaches only the places it declared and only
  through MetaRush, which adds its keys so its code never sees them and records every request; what it brings back is
  added like a file you add, saying which ability made it. A free ability that uses no key and sends nothing but words
  can be run by anyone, Claude included; anything that uses a key, costs money, sends pictures, sound or footage away or
  changes something outside MetaRush asks a person each time. `metarush ability find "<task>"` lists at most eight that might do it.
  On Linux abilities run in bubblewrap; where the system's security settings stop it, MetaRush says so and what an
  administrator can do, and doesn't run them. How it works: [docs/abilities.md](docs/abilities.md).
- **Auto spends money only within limits a person set**. With Auto on and your estimates off, a change that
  costs money runs by itself only where a person set both the project's budget and the team's daily limit with that
  service — the usual budget and the usual daily limit don't count, nor does "no limit" — and MetaRush now keeps who
  set each one (Settings → Integrations shows it). Otherwise the card waits and says why ("Auto didn't start it:
  nobody set the team's daily limit for Google Gemini"). It pays only from the budget of the project the conversation
  is about, and only a person sets a project's budget (Claude and scripts can't). ElevenLabs has no daily limit for
  the team, so its changes ask first. What Auto spends goes on the project's receipt at what the work really cost, and
  the bell has one note a day, "Auto spent $0.84 by itself today" ("up to" while some of it is still being made),
  which opens what Auto spent today in Settings → Preferences. `metarush assistant spending --project <project>`
  says where Auto may spend and what it spent today.
- **A receipt for every project, and what each video cost**. A project's new **Receipt** tab (and `metarush
  project receipt`) lists every request MetaRush sent to an outside AI service — which service and model, what kind
  of work, what was sent, what it cost, whether it was deleted there afterwards and why it ran (you in the
  dashboard, Claude's card, Auto) — the Claude Code conversations about it and whose account answered, the
  generated shots and AI narration, and what Auto spent by itself. It adds the cost up at the services' own prices,
  with nothing added, for the whole project and for what each version uses; a version's page shows its cost. Every
  delivered video's record carries the receipt in short. **Download it** saves the whole receipt for a client.
- **Every conversation says whose Claude answers, and under which terms**. The Claude Code panel runs the
  Claude Code of the computer that hosts MetaRush, signed in to that computer's Claude account, so whoever uses the
  panel is answered by that account. Each conversation now starts with "Answered by Claude Code on Studio, signed in
  as … (Claude Max) · Anthropic's consumer terms", and opening it says what the terms mean for the transcripts,
  frames and messages Claude reads: on Claude Free, Pro or Max, whether they may help improve Claude follows the
  account's privacy settings; on Claude Team or Enterprise or an API key, the commercial terms, which don't train on
  them. Settings → Claude Code starts with **Whose Claude answers**, and `metarush assistant status` and
  `metarush assistant conversations show` say it too.
- **Captions in other languages, translated by Claude on your own plan**. On a version's page, **Ask Claude
  to translate…** picks a language and asks Claude in the panel; it translates each caption line, keeping the names
  on the spelling list as written, and its card shows examples (and any name it didn't keep) before you keep them.
  The files are marked as machine-translated, download as .srt or .vtt, and go out with the video when it's delivered
  (`name.es.srt`, `name.es.vtt`). When a word is fixed in the transcript later, the fix is carried into the
  translation where the word appears in it (a name), and other changed lines are listed to translate again; "Update
  the captions files" brings delivered ones up to date, leaving alone any captions file it didn't make. Lines in
  Arabic, Hebrew, Persian and Urdu read right to left in every player. From a terminal: `metarush captions lines <project> --to es`,
  `metarush captions translate <project> --to es --from es.json`, `metarush captions file <project> --language es`.
- **Titles, descriptions, chapters and show notes, written by Claude on your own plan**. A version's page has
  **Delivery notes**: **Ask Claude to write them** opens the Claude Code panel with the request, and Claude reads the
  version's words with their times and writes a title (and two others to choose from), a description, chapters and
  show notes; its card shows them and you keep them with one click (Auto keeps them by itself). Or write them
  yourself with **Write them…**. Chapters are checked against the video — the first at 0:00, none after the end — and
  what YouTube won't take is said plainly (fewer than three chapters, one shorter than ten seconds, a description
  too long with its chapters). They sit on the version without changing it, and delivery writes them into its
  record and a `.notes.md` file next to the video, ready to paste. From a terminal: `metarush deliver notes show
  <project>` and `metarush deliver notes set <project> --from notes.md` (notes say who wrote them: you, Claude in
  the panel or Claude Code in a terminal, or an agent through MCP or a script; only a person can say otherwise, with
  `--by claude` when pasting Claude's). MetaRush makes no AI calls of
  its own for this: Claude Code writes them with the Claude account the panel uses (yours, when MetaRush runs on
  your computer; Settings → Claude Code says whose), so there are no credits and no extra bill.
- **Tighten pauses anywhere**. The Transcript tab's **Tighten pauses** finds every pause between words longer
  than a length you choose (1 s unless you say otherwise) and shortens it to another (0.5 s), in any footage with a
  transcript, not only call recordings. You hear each one shortened first (or as it is now), then shorten it, keep it
  as it is, or shorten all the rest; they go on the timeline as one change you can undo. `metarush cut pauses
  <project> --longer-than 0.6s --to 0.3s --dry-run` lists them, `--pick 1,3` saves some, `--confirm <token>` saves.
  In the Claude Code panel it is a card, and Auto runs it by itself (it can be undone). Pauses at the cuts between
  clips belong to the cut and are left alone.
- **Pictures, speech, sound effects, music, dubs and more from the keys you already have**. The Generate
  panel's **What to make** adds Picture, Speech, Sound effect, Music, Voice change, Dub, Transcript and Voice copy next
  to Video, with Google Gemini's key (pictures, speech, music, voice copies, cloud transcripts) and ElevenLabs' (sound
  effects, music, speech, the voice changer, dubs, cloud transcripts) — `metarush ai make <kind>` from a terminal or
  Claude Code. Every request is priced first, counts against the same project budget and daily limit as generated
  video, lands marked as AI-generated (pictures in the resources library, sound and dubs in Sources; a dub keeps the
  clip's picture) and is deleted at the provider where its API allows; what can't be deleted is said. With Auto, Claude's
  AI services cards run by themselves only where a person set generated video's project budget and daily limit. The
  project's receipt lists every kind with what it cost and whether it was deleted there, and a version's cost includes
  the AI pictures, sound and cloud transcripts it uses.
- **See the model and the other choices before you spend**. Every estimate names the model and why — the usual
  one, yours, or the project's choice — offers up to two others (a cheaper one, a better ranked one) one click away,
  and shows the model's licence and what the service keeps. `metarush ai models` lists them all with dated prices.
- **Keep a client's work away from AI services**. Content Settings gain an **AI services** part: nothing at
  all, never a provider, or one model for a kind of work, with a note saying why. Generated video follows it too.
- **Copy a voice with consent that satisfies everyone**. A voice copy needs the person's recorded consent, a
  sample of their own named voice and, for Google, a separate recording of them reading Google's consent script —
  checked against MetaRush's transcript, in order. Withdrawing the consent withdraws the copy at once and deletes it at the provider —
  by a job any computer with the key takes, tried until it is done, and shown on the consent's page; work still waiting
  on that consent is stopped, and a copy being made is deleted as soon as it is done. Dubs keep speakers' own voices
  only with everyone's consent; otherwise they use similar voices.
- **Transcripts in the cloud, when you ask**. `metarush ai make transcription --clip <clip>` (or Transcript in
  the panel) has Google or ElevenLabs write a clip down; the words replace the clip's through the usual transcript
  step, keeping people's fixes and spelling lists. Your computer's own transcription stays the default.
- **Delivery notes name AI pictures and sound, and AI music's licence**. Pictures an AI service made that a cut uses
  are disclosed with generated clips; a music track made by an AI service carries its licence, with a warning when it
  may not cover every use.
- **One command sets MetaRush up: `metarush setup`**. A few questions, each with a suggested answer you can
  take by pressing Enter: where your footage and finished videos are kept (a folder on this computer; a cloud bucket
  as an advanced choice), where your projects are kept (a database on this computer, or one you already have such as
  MongoDB Atlas), the dashboard's port, MetaRush starting when you log in, and Claude Code learning MetaRush's video
  skills. It ends by saying what works now and opens the dashboard. Every answer is also a flag and `--json` prints
  the result, so an agent can run it unattended; running it again changes nothing that is set up and fixes what is
  missing. `metarush init` stays for the technical way.
- **A database on this computer, set up and looked after by MetaRush**. No account, no address to copy:
  setup downloads MongoDB's free server from MongoDB (about 85 MB, checked against its published fingerprint), keeps
  it and your projects in MetaRush's own folder, listens only on this computer and always asks for a password (MetaRush's
  own may use only MetaRush's projects; both are generated at setup and kept with your other secrets), starts
  it whenever something needs it and keeps it running; `metarush stop` stops it too. It is never kept in a folder
  that syncs to the cloud, and each MetaRush on a computer gets its own port. `metarush database
  status|start|stop|upgrade` look after it, and the checkup (doctor) checks it. A newer version is never installed
  behind your back: `metarush database upgrade`, run by you in your own terminal, moves it when it suits you, and
  one that stopped half way is finished the next time. MongoDB Atlas and your own servers work
  as before.
- **Setup and the checkup say whether Claude Code is ready**: installed or not, signed in or not (with a
  Claude plan or an API key), and what to do — in Claude Code's own words. MetaRush never signs in for you and never
  reads Claude Code's sign-in.
- **The first run says what works with no keys, and what a key adds**. Home's new **Getting started** —
  shown until your first footage or project arrives — and `metarush setup status` list what works free on this
  computer (transcripts and captions, editing, finished videos, the check of every finished video, a stand-in voice
  for narration), each saying what it needs when it isn't ready yet, then what a key adds (generated shots, AI
  voices: priced before they run and paid to the service directly), Claude Code's sign-in and where your projects
  are kept.
- **Homes that keep secrets in a file keep the database address there too.** `metarush init --secrets env` (and
  `metarush machine join --secrets env`, which uses it) now writes the database address — and the storage secret —
  into the home's `.env` (private to you, checked like every secrets file), so MetaRush in the background and the
  Claude Code panel find them without the terminal that set it up. The Claude Code panel no longer receives
  MetaRush's passwords and keys from the environment MetaRush was started in; the checkup (doctor) warns about a
  password that is only in a terminal's settings.
- **Mac builds signed and notarized, once a Developer ID is set up**. Release builds for macOS are signed
  with the hardened runtime and notarized by Apple when the computer making the release has a Developer ID and a
  notarization profile (kept on that computer only); without them the release says so in one line and is made as
  before. The Homebrew cask now brings FFmpeg and Node.js with it.

## 0.14.0 — 25 Oct 2026
- **Recipes travel between workspaces**. `metarush recipe export <recipe>` — or **Export to a file…** on a
  recipe's page, **Export** on its card in the Library — writes the recipe as one file: the version and its history, its sheet, the pictures it pins, the
  footage its cut plays and its approved motion graphics and Remotion versions by hash. `metarush recipe import <file>`
  — or **Import a recipe…** on the Library's Recipes tab, which first says what would come in — brings it into another
  workspace as a draft: a new version of the same recipe when their history meets (a newer version of one you
  brought in before), else a new recipe (the next free name when its own is taken), with its pictures, footage and code
  in one project. Its words are treated as data, and its code waits there: a motion graphic is checked
  (`metarush motion check --version name@version`) and approved by a person, a Remotion project's packages are allowed,
  checked and approved, before anything renders with them; a person makes the version current. What didn't come along
  is said plainly.
- **A Google Drive folder as a recipe input, without syncing it**. Point a pictures or footage input at a
  folder of your Google Drive (`metarush recipe update <recipe> --from-drive art=gdrive:Teaser/Art`, or `from drive`
  in the recipe's sheet), read through your own rclone set up with its own Google sign-in — MetaRush never holds a
  Google token and needs no Google Cloud setup. Each run's plan lists the folder and says how many files it fetches;
  the run fetches only those, keeps them by their content (a picture's new art is a new version of the same picture)
  and records each file's Drive ID and hash. Without rclone or the remote the plan says how to set it up, and
  `metarush doctor` has a Google Drive check.
- **Recipes learn from your runs**. MetaRush compares each approved run with the version the run made and,
  when people keep making the same change — "You shortened “intro” in 4 of 5 runs: from 3 s to about 2 s", the same
  new words on a graphic, captions off, the music quieter, a line rewritten differently each time — suggests it on the
  recipe's page with its reasons and what a draft would change. **Save as a draft** (or `metarush recipe suggestions
  <recipe> --save`) makes a draft; it is always a suggestion: only a person makes it current. Claude sees them too.
- **A recipe can plan a run by itself, and never starts one**. Turn on **Planning by itself** on a recipe's
  page (or `metarush recipe triggers <recipe> --new-files on`, `--weekly "monday 09:00"`): when new footage or pictures
  arrive where an input takes them from — a linked folder, a collection, a project, a repository's folder — or every
  week, MetaRush plans the run and rings your bell ("New footage on the collection “Gala” for “Event recap”: the run is
  planned, about $1.20" — **Run…**). Nothing starts until you run it, whatever Auto or your estimate preference say;
  the plan waits at the top of the recipe with **Run…** and **Set aside**. Everyone sees whom it tells and which
  computer looks; only that person or an owner changes it.
- **Claude's panel: first picks, recipe sheets and suggested changes are cards.** When Claude keeps or changes a
  waiting run's first picks, saves a recipe sheet it wrote, or saves the suggested changes, you get a card saying what
  it keeps or changes (the run's picks as they stand) or what the draft changes in plain words; drafts only, and none
  runs by itself.
- **Claude's recipe cards in the panel**. Ask Claude to make a video again, save a cut as a recipe, change a
  recipe or make a version current, and its request becomes a card: a run shows its plan — where the video goes, what
  this run gives each input, each step and who does it — and one estimate that opens to the services, models and
  sources it draws on; a save shows what would change each run and what would stay, and saves a draft; a change says
  what it changes in plain words; making a version current says what it changes and the rules it no longer keeps.
  Every one waits for you: Auto never starts a run, not even with estimates off, and a run is agreed to on its own
  card in a styled question. Not quite… sends any of them back to Claude. When a run you started from a card waits
  for Claude to choose the moments, Claude hears at once what to do; it hears too when the run waits for you or is
  ready for review. A card whose plan changed meanwhile shows the new plan instead of starting.
- **What changed between versions of a recipe, and two runs side by side**. A recipe's page says, for each
  version, what it changed from the one before — what changes each run, its sections, rules, sign-offs and hints, the
  sound, graphics, pictures, and the video's shape and length — in plain words; a version that isn't current shows
  what it changes from the current one, and **Make current…** says so before you decide (`metarush recipe diff`,
  `recipe update --dry-run`). Tick two runs and **Side by side** plays their review copies next to each other, with one
  Play, one seek bar and the sound from either, above what changed between them. Studio's Home has **Start from a
  recipe**: the recipes that can run, each with **Run with…**.
- **A recipe as a sheet you can read, edit and keep in git**. `metarush recipe checkout <recipe>` writes a
  recipe version as Markdown — what it makes and where runs go, its inputs, sections, shorts, bindings, rules,
  sign-offs and hints, and its cut as an ordinary cut sheet — and reading it back gives exactly the same version.
  `metarush recipe check <file>` says what is wrong with an edited sheet, line by line, and what it changes in plain
  words; `--save` saves it as a draft (a new version, or a new recipe when the sheet has a new name). Claude writes
  and changes recipes this way; only a person makes a version current.
- **Shorts with every run**. A recipe saved from a cut that plays a conversation can make its shorts too
  (`metarush recipe save --pack shorts`, or the sheet's `## Pack`): each run picks the conversation's best stretches,
  waits for Claude to keep or change them, and makes each short as its own project — vertical, captions with names,
  the voices made clearer like the episode — with a review copy and the quality check, waiting in Awaiting your
  review with the main video. One estimate and one agreement cover the video and its shorts; each short says which
  run made it.
- **First picks for the moments**. When a run brings new footage, MetaRush picks the moments first —
  whole stretches of what is said, a question and its answer before the rest, else what moves on screen — and says
  why for each; Claude (or you) keeps them or changes them (`metarush recipe picks <run> --keep`, or one at a time
  with `--part`, `--n`, `--from` and `--to`), and the run goes on.
- **A run is moved on by one thing at a time.** A run started from the dashboard or a terminal while MetaRush works
  on it in the background could be stepped by both at once and fail ("Someone saved another change…"); now each look
  at a run holds it alone and the other waits its turn.
- **Art kept in git: a repository keeps the pictures in its folders**. Name a folder of a GitHub repository
  ("art/characters") and each PNG, JPEG, WebP, GIF or SVG file in it becomes a picture of its own, next to the
  repository, that says exactly where it came from (`acme/widget@1a2b3c4d:art/characters/alice.png`) and can go in any
  graphic, a Remotion picture setting or the brand kit. Files are told by what they are, not by their names; SVGs are
  cleaned; files stored with Git LFS, links, animated GIFs and files larger than the limit are left out, and you're
  told why. Getting the repository's newest commit makes new versions of the pictures that changed, adds new files and
  marks the ones that went away; cuts keep the versions they show until you update them. In the repository's details
  on the Resources tab: **Pictures it keeps**, with **Keep its pictures** for another folder; in the terminal
  `metarush resources pictures <repo> --keep <folder>` (or `resources add <repo> --pictures <folder>`); Claude asks
  with a card. A recipe's pictures input can take its pictures from such a folder: a run records the commit and each
  picture's hash, and a file's new art takes its own place in the video. Delivery notes credit each picture with its
  file at the commit.
- **A render that uses a graphic nobody approved is refused when you ask for it.** A cut whose motion graphic was
  never approved, or whose approval was withdrawn, used to wait in the queue and then fail as "the render didn't
  finish"; now `metarush render submit` and the dashboard say at once which graphic needs a person's approval and
  how to give it.
- **Gemini's status shows only what applies to video.** `metarush provider status gemini` no longer lists the AI
  voice's narration settings (voice, model, budget, favourites), which belong to ElevenLabs.

## 0.13.0 — 24 Oct 2026
- **The Library's Recipes tab**. Library → **Recipes**, next to People and Motion, shows each recipe as a card:
  a still of the video it was saved from, what it makes, where runs go, what changes each run, its last runs (who,
  when, approved, what they spent) and **Run with…**. Run with… is the recipe's inputs — words, a choice, the AI
  voice, clips or pictures, each with its limits — and the plan MetaRush makes from them: where the video goes, each
  step and who does it (MetaRush, Claude or you), and one estimate, "about $0.13", that opens to the services, models
  and sources it draws on. A paid run starts once you agree to the estimate in a styled question; with estimates off
  (Settings → Preferences) there is no money question, only the line that budgets and limits still apply. A recipe's
  page has its versions (which is current, Claude's drafts marked, **Make current…** naming rules a new one no longer
  keeps), inputs, sections, rules and hints in plain words, its runs and **Compare**; a run's page follows the run
  live, says what it waits for with **Go on** and **Stop**, and links to its project, the version and the review copy.
  **Save as a recipe…** in a project's "…" menu shows what would change each run and what would stay, and why, to
  accept or change; the project and versions a run made say "Made with the “…” recipe, run 1" with **Run again…**.
  Runs that wait show on To-dos (Go on, Stop), the bell and the activity bar. A run that asks to spend more now says
  how much, and going on agrees to exactly that (`metarush recipe continue` asks for it too); a step that costs more
  than planned asks for its own part, so the steps after it don't ask again.
- **Recipes: make a video that worked again**. Save a cut as a recipe (`metarush recipe save --from
  <project>`): MetaRush proposes what changes each run — the project's own footage, the words in graphics, the words
  of clips made from code, pictures that aren't the brand's, the AI voice — and what stays (sections, music, captions,
  the look through Content Settings, rules and sign-offs). A run is planned first, with one estimate ("about $0.60")
  that opens to each step's outside service, model, what it draws on and price, and only a person starts it (with
  estimates off in Settings → Preferences, without agreeing to a price; budgets and limits still hold); it makes a
  new cut (or a new project) through the ordinary steps — fixed sections locked, clips made from code drawn again
  with new words and new art (no code to approve), moments Claude chooses from new footage, AI voice, a review copy
  and the quality check — and waits in Awaiting your review.
  "Again with the new art and a warmer voice" is a new version of the same cut that changes only what changed; going
  past the estimate asks first; budgets and daily limits still hold. Runs that wait say so on To-dos, the bell and the
  activity bar. `metarush recipe list|show|propose|save|update|check|publish|archive|plan|run|again|runs|compare|
  continue|stop`, and the `video-recipe` skill for Claude.
- **Replace the pictures in a Remotion project without changing its code**. Sources → a Remotion project →
  **Pictures** lists the pictures its code draws from its own files; one change to its code that a person approves
  makes them settings (`metarush remotion pictures setup`). After that **Replace…** takes a picture you upload or one
  of the project's resources (PNG, JPEG, WebP or SVG, cleaned): it is drawn with no network and checked — does the
  new picture show, does its shape fit the old one — then the latest cuts' clips are made again with it, with no new
  version of the code to approve (`metarush remotion pictures list|set <name> HERO <file or resource>`, `--original`
  for the project's own again). A clip can have pictures of its own (`metarush remotion use … --picture
  HERO=<file or resource>`), the way recipes will fill in new art. Sending changes back takes MetaRush's helper out
  again, and Claude's steps on Remotion projects now read plainly in the panel.
- **Estimates you can open**. Generated shots, the AI voice, clearer voices in the cloud and Claude's cards for
  them say what an edit is expected to cost as an estimate — "about $0.40" — that opens to what it draws on: the
  service, its model, the clip, script line or picture, the units (seconds of video, characters, credits) and each
  part's price, with the date of the prices it used. The AI voice and a clearer voice in the cloud are said in
  dollars now too, at ElevenLabs' pay-as-you-go prices (a plan's credits may cost less; budgets still count credits).
  In a terminal, `generate estimate`, `voiceover estimate` and `sound enhance --estimate` list the parts (`--details`
  adds each price).
- **Settings → Preferences: your own choices**. A new section after People: "Estimate costs of outside AI
  services before edits" (on by default), Ask first or Auto for Claude's changes, and light or dark (moved from
  General; old links go there). With estimates off, paid steps run within your budgets, daily limits and project
  spending limits without the estimate step: a generated shot needs no agreed price, the dashboard and a terminal
  don't ask about the price first, and with Auto on Claude's changes that cost money run by themselves. Consent,
  approvals and people-only steps still ask. An owner can ask everyone for estimates. With estimates on, a terminal
  now also shows the estimate and asks before the AI voice and a clearer voice in the cloud. `metarush account
  preferences [--estimates on|off] [--require-estimates on|off]`.
- **Clearer names: "recipe" now means only Recipes**. A generated clip's details are **How it was made**, a
  fixed copy is made by its **fix steps**, a Remotion refresh runs its **refresh steps**, and voice clean-up offers
  kinds of clean-up. Nothing you made changes: every fixed copy is still used, and the database's names are updated
  when MetaRush updates (`--json` says `madeWith`, `fixSteps`, `fingerprint` and `refreshSteps`).

## 0.12.0 — 23 Oct 2026
- **Five named logos in the brand, each uploaded right there**. The Brand part of a project's Settings tab has
  Main, For dark backgrounds, For light backgrounds, Square icon and Wordmark — upload a file (a picture, an SVG or
  a PDF, kept in the resources like any) or choose one from the resources. Title cards, name titles, the name bar,
  end cards and title slides show the logo made for their background (chosen by contrast with the colour behind
  them), else the main one; every project using the settings gets them, and the latest cuts are offered the change.
  A brand's logo is its main logo now; nothing moved. `metarush content logo set <main|dark|light|icon|wordmark>
  <file or resource> --project …` and `content logo remove`.
- **A house style that new cuts start with**. In Content Settings: captions (on or off, their look, size and
  position), the name bar (a band or a tab, about half as tall — it still covers the printed name — and whether
  the name animates: fades, slides or never), the name titles' look, framing, sound at cuts, and an intro and end
  card. New cuts of every project using the settings start with it, `metarush cut names` writes its bar that way,
  and after a change each latest cut is offered "Use the house style on the latest cut" (a checked change). Claude
  follows it, and the quality check finds captions missing, or a name bar that animates or is taller than the
  style says. `metarush content style show|set|apply`.
- **Captions in three sizes; a name bar half as tall that never animates**. Captions are small, medium or
  large (`captions: size large` in the cut sheet); the built-in name bar takes `height half` and `animate never`
  or `slide` (it fades names in, as before, when nothing is said).
- **House sign-offs every project using the settings needs**. Add one in Content Settings ("Legal OKs it
  before export", `metarush content signoffs add`): each project's Sign-offs list it from those settings, it
  reminds before approving, exporting and delivering like the project's own, and each project ticks its own. A
  "Keep this sign-off?" card asks whether it is for this project or every project using its settings.
- **Delete a project: it moves to Archived projects, where anyone can restore it**. "Delete project…" in a
  project's "…" menu (or `metarush project delete`) asks first; the project leaves Home, the project lists and
  search, its footage stays in the Library, and its conversations with Claude Code can still be read ("“Old promo” is
  archived — Restore"). Home's Projects links to **Archived projects**, which lists them with who deleted them and
  when; **Restore** brings one back as it was, and a link or notification to a deleted project says so.
- **Owners delete archived projects for good, and footage is never lost**. **Delete for good…** on an
  archived project, or **Clear out archived projects…**, first lists what goes — versions, renders, quality checks,
  conversations with Claude Code, what it kept, rules, comments, links and its Content Settings — and what moves:
  footage only it had goes back to the Inbox, footage another project's cut uses joins that project, its pictures
  move to the workspace library, and projects that used its settings use the default ones (you are told). Render
  files go only when nothing else uses the same bytes. It waits while another project's cut plays one of its cuts.
  `metarush project purge <project>|--all-archived [--dry-run]`, in your own terminal.
- **Everyone pairs their own browser with the Send to MetaRush extension**. Settings → Browser extension →
  **Pair a browser** is open to everyone now; what your browser sends — videos, links, page snapshots — is yours in
  MetaRush. **Your browsers** lists yours, each with Remove; owners also see **Everyone's browsers** and can remove
  any. A turned-off or removed account's browsers stop at once. Browsers paired before are the owner's.
  `metarush extension list` and `metarush extension revoke <id>|--all|--everyone`.
- **Balance colour and brightness lifts a dark clip instead of darkening it.** A clip still dark after its colours
  were balanced came out darker, and a bright one much brighter; now a dark clip comes out a little brighter and a
  bright one a little calmer. A copy made before keeps playing until you untick the fix and tick it again, which
  makes a new one.
- **“An 8-second shot” wherever a generated shot is named.** The activity bar, the Generate panel's price, Claude
  Code's card to make a shot, a shot's state in Sources and the bell, and the activity log said “a 8-second shot”
  (also for 11 and 18 seconds); they all say “an” now.
- **A linked folder tries again a file it couldn't add.** A file that couldn't be added (say its project was cleared
  out) was never tried again until it changed, and its problem was gone from the folder at the next look. Now it is
  tried again by itself after a minute, five minutes, half an hour, then every six hours (a large file isn't read
  again in between), at once when it changes or when you press **Look again** (`metarush resources folder <name>
  --rescan`), and the folder says so until the file is added or goes away — “1 file couldn't be added; MetaRush
  tries it again in about 5 minutes”, with the file and why below it, in the terminal too.

## 0.11.0 — 22 Oct 2026
- **Content Settings: a project's brand, spelling list, narration voice and house rules, shared live**. Each
  project uses the default settings, another project's (changes there show up here, through chains; a choice that
  would go round in a circle is refused) or its own (empty, or a copy). "Use for new projects" makes a project's
  settings the default — anyone may tick it, and ticking another moves it. A project that uses another's shows them
  read-only ("From fernbrook — edit them there, or give this project its own"), and changing settings others use says
  "Also used by 3 projects". If a followed project is gone, its followers use the default again and you are told.
  `metarush content show|follow|own|default --project …` and `metarush content rules …`.
- **The project tab "What it keeps" is now Settings**: Content Settings on top — Uses, Use for new projects,
  Brand, Spelling, Narration voice, House rules — and This project below (brief, its own rules, sign-offs, what
  it keeps). Old links still open it; the studio header's Spelling opens it at the spelling list.
- **The brand and spelling lists moved out of Settings into each project**. Settings → Brand and Settings →
  Footage → Spelling are gone; their old addresses open the default's Content Settings. A clip in several projects
  is corrected by all their spelling lists, and footage in no project by the default's.
- **The narration voice is part of Content Settings; budgets stay each project's**. The voice, its model and
  settings, the region and favourite voices follow the settings a project uses; spending limits and credits used
  never do, and a line's own voice still wins.
- **House rules for every project using the same settings**. Claude and the quality check read them next to
  a project's own rules, labelled. Claude's "Keep this rule?" card asks where: "Just this project" or "Every
  project using fernbrook's settings"; a logo kept for a project that uses another's gives it settings of its own.
- **Changes reach new work; saved versions are offered them**. New cuts, transcripts and narration lines use
  a change at once; after a brand change MetaRush offers "Use the new brand on the latest cut" for each project
  using those settings, and a new spelling can fix existing transcripts too.
- **Today's brand, spellings and voice moved into Content Settings with nothing changing** (migration 36). The
  workspace's become the settings of the most recently active project that used them, now the default; projects
  with their own kit, spellings or voice get their own settings with exactly what they used; the rest use the
  default.
- **Only owners change this computer's settings**. Everyone signed in sees every setting, but Claude Code, the
  watched folder and link imports, the browser extension, the storage and the Trash, keys and the team's budgets and
  limits, the log and the weekly improvement report, remote access and machines change only for an owner — as do
  browsing and linking this computer's folders, setting up studio quality, lending this computer's browser sign-in to
  an import, review copies that follow the latest version and deleting a clip for good — on this
  computer and from another one alike, and MetaRush itself refuses anyone else, not just the dashboard. Members see
  the controls greyed with "Only an owner can change this. Owner: …"; a project's own voice and budget stay theirs to
  change, and so does each person's Ask first or Auto. This computer's own command line counts as the owner.
  `metarush auth status` names the owners.
- **The browser extension has its own place in Settings**. Settings → Browser extension (`/settings/extension`)
  installs it and pairs or unpairs browsers; older links to it in Settings → Footage go there.

## 0.10.0 — 22 Oct 2026
- **A conversation with Claude stays about its project**. Whatever page you type from, Claude gets that
  project's latest version and what it keeps; the clip or the playhead you picked goes along when you're on the
  project's own page. Opening another project switches the panel to that project's conversation (or a fresh one). The
  panel's header names the project (or says Whole workspace) and has a New conversation button; the list says each
  conversation's project, in full screen too.
- **Claude starts with the project so far**. A new conversation in a project tells Claude what happened there:
  the versions and their notes, which were approved or need changes, the review copies and finals with what their
  quality checks found, earlier conversations and what you decided on their cards. `metarush project so-far
  <project>` shows the same.
- **Claude hears what's new after an update**. The first message after MetaRush is updated tells Claude what
  changed, so it stops saying something can't be done when now it can.
- **Not quite… on every card**. Say what should change right on the card: Claude gets your note at once and
  makes a new version. `metarush assistant act <conversation> <card> --not-quite "…"` does the same. Not now now
  means "later", not "never".
- **Your notes on a turned-down version reach Claude**. Rejecting a version, or turning down a graphic, with a
  note sends it to the conversation that made it. When no conversation did, Send to Claude starts one with your note.
- **Auto: changes that can be undone run by themselves**. Choose Ask first or Auto by the message box (or
  `metarush assistant mode auto`). With Auto, Claude's saves (with their review copy), sound and transcript fixes,
  links and graphic checks run without a click; anything that costs money, final renders, approvals and deleting
  still ask. An owner can turn off everything that runs by itself in Settings → Claude Code.
- **Cards and messages say when**. "Approved by Maya · Today 3:10 PM" on decided cards, and the time under
  each message. Code in Claude's replies folds away behind See code, and a graphic's review page shows the picture
  first, its code behind See code.
- **A review copy follows the latest version**. About a minute after the last change to a cut (by you or by
  Claude), MetaRush makes a review copy of its latest version by itself, so the real video is usually ready by the
  time you look. A newer version takes the place of an older one's copy that hasn't started yet; one that is already
  rendering finishes. It waits on battery like other big jobs (the cut says why), gets a quality check like any
  render, and rings the bell only if it fails or the check finds something. Turn it off for the workspace on the
  Renders page or with `metarush render follow off`.
- **The Edit tab shows the real thing**. Above the timeline, switch between the **Live preview** (drawn in your
  browser, unsaved changes included) and the **Review copy** of the latest version, which plays by default when there
  is nothing unsaved. While one is being made the tab says "Making a review copy of version 8…" with its progress or
  why it waits; when the copy is of an older version, **Watch it** plays that one. A version's page lists its review
  copies and final renders, with when they were made, and plays them in its player.
- **Motion graphics look like themselves in the live preview**. A graphic whose check drew its test frames
  shows the still nearest the moment, in its place and shape, marked "still — moves in renders"; only a graphic never
  checked keeps the dashed box. The pictures are drawn in the render jail; the dashboard still never runs a graphic's
  code.
- **One set of words**. The editing tab is now called **Edit** (links still work), a quick half-size render is a
  **review copy** everywhere, and versions are **Draft**, **Approved** or **Needs changes**. "Awaiting your review"
  lists the versions Claude made and the ones someone asked to have reviewed (by asking for a review copy, or with the
  new **Ask for review** on a version's page, `metarush revision ask-review`), not every change you make by hand;
  `metarush revision waiting` lists the same.
- **Dates you can see**. Renders, versions, groups of hand edits, "What was asked", "Awaiting your review", the
  Library's tiles and list (on a phone too), the Sources strip and a clip's header, its details and the resources say
  when, at a glance ("Today 3:04 PM", "Yesterday 9:12 AM", "Tue, Oct 20, 3:04 PM"), with the exact date on hover.
- **Fewer, better questions about who is on screen**. The voice heard while a face is alone on screen now
  counts only after 6 seconds (shorter, it was right about a third of the time in calls, whose picture switches late
  and often shows the listener), and on its own it never asks "Is this Lena?": that takes a face that looks like
  Lena's or her name shown under it. Otherwise MetaRush asks "Who is this?" with Lena as one quick pick. A clear
  match by looks is never held back by who is talking, faces MetaRush named by their looks count when it checks whose
  voice it is, and a "(talking then)" answer never contradicts the looks or a name shown. Words like "I'm" and
  "You're" are never read as names from the transcript. The open questions are looked at again after the upgrade:
  the ones that no longer hold close, junk answers go, and everything you answered stays answered.
- **Answer many face questions at once**. "None of these are Lena" answers every question that suggests her
  (above the grid on the To-dos page), "Not Lena in this clip" on a tile says she's nowhere on screen in that clip
  (faces found later included), and "Skip all in this clip" / "Skip all in this project" skip the rest. Tiles can be
  selected (x) and answered together, with Select all. Keys: c (not them in this clip), S (skip the whole clip).
  `metarush todos not "Lena"`, `metarush footage faces not-here <clip> "Lena"`, `metarush todos skip-faces`.
- **MetaRush keeps score of its hints about people**. Every answer records what the names it offered rested
  on — who was talking, the looks, a name shown or said, the voice — and whether you agreed; a kind of hint right
  less than 80% of the time (after 10 answers) stops suggesting names and only offers them as picks, and the To-dos
  page says so. After two "not Lena" answers in a clip that went by her voice, her voice is no hint there.
  `metarush people evidence`.
- **Steady the camera works for call recordings**. A shaky webcam in a call can be steadied too: MetaRush
  measures the shake between the picture's switches and steadies each stretch between two switches on its own, so
  one person's shaky camera never drags the next person's picture; every frame stays where it was. In a conversation
  of several recordings each person's own recording is steadied on its own.

## 0.9.0 — 21 Oct 2026
- **A shorter top bar, and Settings in sections**. The bar keeps Studio, Library, Renders and To-dos; People
  and Motion are now tabs of the Library, and the brand kit, looks and the activity log are sections of Settings.
  Settings opens from the account menu at the right (your picture when people sign in, a menu button when it's just
  you), which also switches light and dark and signs you out. Each Settings section has a page of its own, so old
  links and bookmarks still land in the right place. The live dot moved onto the activity button.
- **Your picture**. Add a picture in Settings → People (or `metarush account avatar photo.jpg`); MetaRush crops
  it to a square and shows it in the account menu, the list of people and your messages to Claude.

## 0.8.0 — 21 Oct 2026
- **Accounts: choose how people sign in, and invite them**. A new install is just you on your computer. In
  **Settings → Users** you choose email and password (or, as an option, Tailscale accounts), set up your own sign-in at
  the same step, and invite people with a one-time link you copy and send. They choose a password and sign in from
  their own computer; anyone who used MetaRush before keeps their history when you invite their email. Owners make
  reset-password links, turn accounts off and on, and decide who else is an owner. Passwords are stored safely
  (argon2id), common ones are refused, and wrong guesses are slowed down and locked. One-time links from `metarush
  dashboard` now sign in only you, on this computer. Remote access works with any HTTPS address, not only Tailscale's.
  `metarush auth status|method|reset`, `metarush users …`, `metarush account …`.

## 0.7.0 — 21 Oct 2026
- **Trim the flashes and Cover the printed names work in conversations and in cuts played inside other cuts**.
  A conversation of several recordings is checked at each of its shots and at a speaker view's own switches;
  a nested cut at its own cuts and the call footage inside it. The name bar follows who is on screen there too and
  hides where nothing is printed; in a side-by-side or grid layout, each person's tile gets its own name tag over the
  name the call app printed. A nested cut that has its own name bar keeps it, and its bar now covers the names in
  the right place when the cut is played at another shape. Cut sheets write those name changes in words.

## 0.6.0 — 21 Oct 2026
- **Claude Code conversations count tokens, not dollars, and stay quick**. On a Claude plan nothing is
  billed per message, so the $25 "spending limit" is gone: a conversation's limit is now 50 million tokens (the
  pieces of text Claude reads and writes), changeable for one conversation with `/limit 80` or for all in Settings.
  Long conversations are summarized once Claude's memory passes 200,000 tokens, right after a reply, so each next
  message reads far less (the conversation still shows in full, and the project keeps everything you made).
  Claude's memory stays ready for an hour through a pause, your clicks on its cards go with your next message
  instead of each starting one of its own (safe steps and failures still reach it at once), and Claude can hand
  heavy looking — many frames, long transcripts — to a helper. The new **Settings → Claude Code: usage and memory**
  shows how much of your Claude plan's week and five hours is used and every setting; the panel shows the tokens a
  conversation used. `metarush assistant settings --limit --compact-at --cache-hour --hold-news`, `assistant
  configure <id> --limit`.
  - For developers: token counts per message and conversation (`tokens`, `limitTokens`), summaries as turns
    (`Turn.Compact`, `compactions`, `compactedTo`), the plan's usage in `assistant.status` and `assistant.settings`
    (`planUsage`); news marked told only when the message carrying it was sent; `--json` on one line for Claude.

## 0.5.1 — 21 Oct 2026
- **Attach pictures and PDFs of up to 20 MB from the dashboard.** The Claude Code panel used to stop at about 6 MB
  (the request carried the file written out as text); it now takes what MetaRush itself takes, as the terminal did.
- **Names on call recordings trust the app's printed name over a face MetaRush only thinks it recognizes**.
  A face someone confirmed still comes first; a "looks like" match nobody confirmed now comes after the name
  the call app printed, so the name bar no longer shows a guessed name over a printed one. `footage switches` says
  "by looks: not confirmed" for those shots.
- **A version saved while a text fix lands no longer offers that fix again**. It is told the
  transcript revision its words belong to.
- **When a speaker fix is saved but the voices couldn't be learned again, the message says it was saved** — with
  how to rebuild the voices — and the page shows the change, instead of saying it couldn't change who says it (or
  that it wasn't undone).
- **Plain words for finding where the picture changes** in the activity bar, job details and the terminal (it used
  to say "switches").
- **A saved version that plays an older fixed copy says the database is down during an outage**, quickly, instead
  of waiting and then saying the copy doesn't exist.
- **Remotion renders no longer fail now and then with "port … is not available".** The renderer's port is checked
  the way Remotion checks it and held until the render starts.
  - For developers: `ops.Flags.MaxInput` raises an op's HTTP request limit (8 MB by default); test databases carry
    a run marker and time-readable names, and `go run ./scripts/sweeptestdbs [--drop]` finds the ones whose run is
    gone; `go test` runs with `-timeout 30m`.

## 0.5.0 — 21 Oct 2026
- **Refresh a Remotion project from its own steps**
  ([docs/remotion-sources.md](docs/remotion-sources.md#refreshing-the-projects-own-steps-started-by-a-person)).
  Many Remotion projects build themselves — make a voice-over, capture a website, pull in new data. Under Sources,
  **Refresh from its own steps** lets you run those steps (its npm scripts) from MetaRush: you pick the steps, the
  keys it gets from Settings → Integrations and the name each goes under (MetaRush suggests the one the code reads),
  and where it may write; before anything runs you see every command, the places it may reach and how it is kept
  apart. Only you start it — never by itself, never Claude. It runs on this computer with the network on, but in a
  fresh copy with nothing of MetaRush's: no settings, keys or files of MetaRush's, nothing in your home folder, no
  keychain, and never the project's `.env`. What it wrote becomes a new version, checked like any other (the
  change in words, before/after stills), that waits for you to approve it; nothing reaches a cut before that. Its
  progress shows in the activity bar, and what it printed is kept with the keys hidden. `metarush remotion refresh
  plan|start|runs|log`.
- **Send MetaRush's changes back to the project's repository**
  ([docs/remotion-sources.md](docs/remotion-sources.md#sending-changes-back)). **Send changes back** shows what
  an approved version changed since it was brought in — in plain words: the words that changed, the files, the
  packages — and puts it on a new branch of the GitHub repository it came from (or its folder's remote, or the
  folder's own repository), with a pull request on GitHub. Nothing is ever forced, the main branch is never
  touched, and a branch MetaRush didn't make is never changed; a later version can go on top of the branch MetaRush
  made. MetaRush's words helper stays out: your constants go back as plain code, with today's word settings written
  in only when you ask. People only. `metarush remotion push plan|start|list`.
  - For developers: ops `remotion.refresh.plan|start|runs|log`, `remotion.push.plan|start|list`
    (`start` is HumanOnly, with the plan's fingerprint); jobs `remotion-refresh` and `remotion-push`, pinned to the
    computer that asked and never retried by themselves; version kind `refresh`; collections `remotion_refreshes`
    and `remotion_pushes` (migration 34); `internal/remotion/refresh.go`, `refresh_run.go`, `isolate.go`
    (`sandbox-exec` profiles, bubblewrap arguments; the tools `sandbox-exec` and `bwrap` found only in system
    folders), `push.go`; `analyze.mjs words-bake`; activity rows for both jobs; the `video-remotion` skill reads plans
    only.

## 0.4.0 — 21 Oct 2026
- **Undoing “this part is Lena” no longer leaves a fix waiting for a new version**
  ([docs/transcripts.md](docs/transcripts.md#the-words-and-the-fixes)). The words went back at once, but the undo
  was only written down after the voices had been learned again and your other clips checked — several seconds, now
  that the questions about faces read the voices too. Meanwhile the fixes waiting for a new version still counted it
  (“6 text fixes” for 5), and a version made then kept waiting for it for good. A fix of who says a stretch, and its
  undo, are now written down the moment the words move, with the names on the lines; the voices learn from them
  after. If learning the voices again fails, the change stays made and the message says so: **Rebuild voice** on the
  People page learns them again.
- **Clips, cuts and versions have names that say what they are** ([sources](docs/sources.md)). A clip is called
  “Lena's call with Maya (32 min)” instead of `IMG_0042.MOV`: MetaRush names it from its first look (who's in it,
  what it's about, a title on screen, a link's title, the recording date) and keeps the file name on its info card;
  until it can tell, the file name stays. A name you typed is never replaced: when MetaRush finds a better one later it
  waits next to the title ("Better name: … · Use it"), and a project's Sources offers all of them at once (**Use all**).
  A cut has a name too (“Main open day video”, suggested from the brief, never “garden main v2”), renamed without a new
  version or losing its approval, and every version carries a short note (“flicker fixes and headline cards”) you can
  change. The Claude Code panel, cut sheets, to-dos and the activity log use the same names. In a terminal:
  `metarush asset update <clip> --suggested`, `metarush asset names --accept-all`, `metarush cut rename`,
  `metarush revision note`.
- **Every clip has an info card**. "About this clip" under the player says what it is: its name and original
  file, where it came from (the link, the camera and the day it was recorded, a generated shot's recipe), what MetaRush
  found (length, picture and sound, the people, words on screen, what it's about), where it's used and its fixes.
  `metarush asset card <clip>` shows the same.
- **Fixes: steady the camera, remove a hum, clean up voices and more**. Each kind of source has two or
  three fixes as checkboxes, and MetaRush marks the ones that fit with why ("The camera shakes a lot", "There's a
  hum"): camera video (steady, colour and brightness, grain), calls and screen recordings (each person's volume,
  cleaner voices, blockiness), voice recordings (cleaner voices, even loudness, hum), music (even loudness, silent
  ends) and pictures and logos (remove the background — on a Mac a photo's subject is cut out with Apple Vision —,
  trim empty edges, larger and sharper). Ticking makes a fixed copy on a computer that's on (the activity bar shows
  it); the original is always kept and unticking goes back to it. Everything after uses the copy: the transcript and
  voices are made again from cleaner sound (names you gave and words you fixed stay), and Claude and new cuts use
  it. Cuts you saved keep playing what they played: "Use the steadier picture in “Main open day video”" saves a new
  version with one click. The card shows the measurement before and after ("shake: 0.65% → 0.01% of the picture's
  width a frame").
  Steadying uses vid.stab (measured against ffmpeg's basic steadying and Apple Vision, docs/sources.md), set up on
  first use from a pinned, checked download; your ffmpeg is never changed. In a terminal: `metarush asset fixes <clip>
  --tick steady`, `metarush cut use-fixed <project> <clip>`.
- **Generate video from the drop box**. Under Choose files, on Home and in a project's Sources, **Generate video**
  opens the Generate panel (describe the shot, the length and quality, the price before anything is sent); on Home it
  first asks which project, with the one you used last chosen. A shot can now start from a photo: one from your
  computer (added to the project's Resources) or a picture already there (`metarush generate shot … --from <picture>`).
- **See what's inside a Remotion project: its scenes, words, pictures and sounds**
  ([docs/remotion-sources.md](docs/remotion-sources.md#the-project-map)). Under Sources, **Scenes and sounds** shows
  each composition scene by scene with a picture of each, its exact times and what sets its length, the words on
  screen (and words that spill out of their box), its pictures, and its music, voice lines and sound effects with
  when they play and how loud. Claude reads the same with `metarush remotion map <source> --json`.
- **Change a Remotion project's words without touching its code**. Under **Words on screen**,
  **Make its words settings** writes one small change to the project (a version you approve, which looks the same);
  after that its headlines and lines are a form: change them, **Check the new words** (MetaRush draws them and says
  whether the video gets longer and whether they still fit), then **Use the new words** — the latest cut's clips are
  made again with them, with no new version of the code to approve. `metarush remotion words set teaser
  END.tagline="…" --check` does the same from a terminal.
- **Bring a Remotion project's music and voice in as the cut's own tracks**. **Use in the cut, its sounds as
  tracks** (or `metarush remotion use … --sound layers`) puts in the picture alone and places its music, voice lines
  and effects as tracks and narration where the project played them — with their levels, fades and dips — so you can
  change them on the Sound tab or let a new voice take over. Using a composition with its own sound stays the default.
- **Turn a scene into a motion graphic any cut can use, with the project's look shared**
  ([docs/motion-graphics-as-code.md](docs/motion-graphics-as-code.md#shared-libraries-and-graphics-lifted-from-a-remotion-project)).
  **Make it a motion graphic** on a scene (or `metarush remotion lift teaser End --name end-card`) makes the scene a
  motion graphic whose settings are the scene's own (its tagline, say), and the project's colours, fonts and
  components one **shared library** that every graphic made from the project uses; its Google Fonts join the
  workspace's resources. You approve the graphic and the library once; a new version of the library is one approval
  for every graphic that uses it, and the latest cuts move to it. Cut sheets write `motion end-card@3fa9c2d1+teaser-theme@7e3a91c0`.
- **A Remotion version waiting for you shows up in To-dos and the bell**. "Can version 3 of “Teaser” go into
  cuts?" — look at it, then approve it or turn it down right there.
  - For developers: ops `remotion.map`, `remotion.words`, `remotion.words.setup`, `remotion.words.set`,
    `remotion.lift` (CLI `remotion map|words|lift`, `use --sound layers`), job type `remotion-task`;
    `video/render/source/` `project.mjs`, `map.mjs`, `words.mjs`, `lift.mjs` and the map's `probe/`; shared motion
    libraries (`library.json`, `@metarush/lib/<name>`, `MotionRef.libs`, `useFont`, a component's `motion.json`);
    to-do kind `remotion_approval`, notification kind `remotion_waiting`; no migration (fields only).
- **A project's Claude Code conversations are everyone's, each saying who started it**
  ([docs/assistant.md](docs/assistant.md#conversations-people-share)). With remote access people sign in as
  themselves, and a conversation someone had while signed in as someone else seemed gone. Now the Claude Code panel's
  list, in a project, shows everyone's conversations in it ("Started by Theo", "You"), with **Everyone / Just mine**
  (remembered in your browser) and a way to every project's; the list outside a project has the same choice and
  shows yours until you pick. "Pick up where you left off" follows it.
  - **Continue anyone's conversation**: your messages are yours ("Maya" or "You" over each message once more than
    one person wrote), and so are the cards and questions Claude raises answering you — they count in your badge
    and your ‹ › navigator, and you are the one notified. A card waiting for someone else shows in the thread as
    "Waiting for Theo"; anyone there may decide it, and the card then says who did ("Done by Maya").
  - Archiving from the list is offered on your own conversations (someone else's can still be archived from its menu).
  - From a terminal: `metarush assistant conversations list --project <prj>` lists everyone's in the project
    (`--mine` for yours, `--all-users` outside a project); `conversations show` names who wrote and decided what.
- **Videos keep playing when the database has a hiccup**. MetaRush now remembers where each clip's and
  render's files are, so when its database briefly stops answering — MongoDB Atlas electing a new primary, a dropped
  connection — the cut preview, the clip viewer and renders keep playing, and a cut you opened plays to the end,
  clips it hasn't reached yet included. When something really can't play, the player says so in plain words —
  "Reconnecting…" or "MetaRush can't reach its database right now; it keeps trying." — instead of sitting at 0:00
  with "Buffering…", tries again by itself with growing pauses, and goes on from the same moment once it can (Play,
  Pause or a jump pressed meanwhile counts). A clip that's gone, or a file that can't be played in this browser, is
  said plainly instead of being tried forever.
  - What MetaRush remembers follows the database: while it answers, it decides everything as before; a clip that
    changes is read again, a clip in the Trash is never remembered, and only people signed in to the dashboard get
    anything from it. A clip MetaRush doesn't know yet is refused within a few seconds with that plain message
    (before, after 8 seconds, it said the clip didn't exist).
  - For developers: `internal/playback` (`AssetFor`, `RenderFor`, `Remember`, `Changed`), `store.GetByIDAnyMember`,
    the `X-MetaRush-Record: memory` header, `E_DATABASE` (503, `reason: database_unreachable`) from the media
    endpoints, the players' recovery in `web/src/player/` (`recovery.ts`, `useMediaRecovery`, `usePlayerRecovery`,
    `RecoveringVideo`), a cuttable link to the test MongoDB for tests (`internal/testutil/dblink`,
    `testapp.NewWith`), `web/scripts/recovery-test.mjs` and the browser run `scripts/playback-e2e.sh`.

## 0.3.0 — 20 Oct 2026
- **MetaRush asks who someone is once, not every time they appear**
  ([docs/visual-index.md](docs/visual-index.md#one-question-per-person-not-per-appearance)). A call
  recorded in speaker view starts a new picture of someone at every switch, and MetaRush used to ask "Is this
  Lena?" for every one of them, dozens of times in one call. Now the faces of a clip are grouped by how they
  look and by who is talking while they are on screen, and each person is asked about once: "Is this Lena?" (or
  "Who is this?") with a few of their faces and "Seen 24 times in “Lena's call”". One answer names all of them; a
  face that looks less like the others is left out and asked about on its own later. The questions you already have
  are folded together the next time MetaRush starts.
  - **The names a video shows or says are suggestions.** A call app's name under each picture (Zoom, Teams, Meet), a
    lower third, a title before someone appears, "Hi, I'm Iris", "Thanks, Lena": each question says what it saw
    or heard ("The video shows “Lena Okoye” under this face"). **Use the names shown in the video** names everyone
    in a clip in one click (`metarush todos accept-shown <clip>`). Nothing is named or learned from them until you
    accept.
  - **Only what matters is asked**: people who never speak, and faces seen for under a second, aren't asked about;
    one question per person per video at a time; faces in your cut come first ("In your cut").
  - **Answer many at once**: the To-dos page shows who is on screen as a grid — their faces, why a name is suggested,
    Yes, a name picker, **Same as…** (two questions about one person become one) and Skip, with keys (j/k or the
    arrows, 1 yes, n no, Enter to type a name, s to skip). From a terminal: `metarush todos groups`, `todos answer
    <question> "Lena" | --suggestion 1 | --same-as <question>`, `todos accept-shown <clip>`.
  - A voice's question suggests the person whose face you named on screen while it talks, and the name it says for
    itself — suggestions only: a voice is never named from a face.
  - **A face is no longer mistaken for the next speaker's.** The on-screen index took each frame about half a second
    late, so in a call's speaker view the last moment of one person's picture could be the next person's face, and
    naming one taught MetaRush the other's looks (Ben's face then "looked like" Ada). Frames are now taken at their
    moments, and in clips looked at before, a frame that doesn't look like the rest of its face is left out.
- **See and hear what a name was learned from, and fix an earlier mistake right there**
  ([docs/speakers.md](docs/speakers.md#hearing-and-seeing-what-a-name-was-learned-from)). "These two voices sound
  quite different — one is much lower. Is this really Maya?" now plays this voice next to the voices Maya's voice
  was learned from, says how alike each is and marks the one that sounds unlike the others; **Not Maya** on it lets
  you say who it really is, Maya's voice is learned again at once and the warning goes away, so the answer you were
  giving is finished with the mistake fixed. "Both voices in this clip will be Maya" plays both voices: **Same
  person — combine**, **Different — rename the other one** or **Keep things as they are**. "Not Maya?" on a face
  named by its looks shows the faces hers was learned from, the one it looks most like marked, each with **Not
  Maya**. From a terminal: `metarush people learned "Maya" [--asset <clip> --voice "Speaker 2" | --face <face>]`
  and `people unlearn "Maya" (--asset <clip> --voice "Speaker 1" | --face <face>) --as "<who it is>" | --unsure`.
  - A voice's sample shows the picture a moment after it starts: in a call's speaker view the picture switches to
    whoever talks a moment late, so the first frame showed the person before.
  - For developers: ops `todos.groups`, `todos.acceptShown`, `people.learned`, `people.unlearn`; `todos.answer`
    takes `sameAs`; to-do kind `face_group` (context `faces`, `appearances`, `onScreen`, `inCut`, `evidence`, `shown`);
    faces carry `group`, `skipped`, `confirmedVia`, `confirmedGroup`, `asideFrom`; a group answer teaches through its
    six clearest faces; `E_CONFIRM_REQUIRED` details name the person and the voices; catch-up step `face-questions`
    v1; the on-screen index keeps where each showing of a text is (a name tile in speaker view and in a gallery);
    `scripts/make-call-fixtures.sh` (a speaker-view call with name tiles), `scripts/people-e2e.sh`.
- **A website's logo, colours and fonts come with its snapshot** ([resources](docs/resources.md#logo-colours-and-fonts-from-a-website)).
  When MetaRush takes a snapshot of a site's page, it now also finds the site's logo — the picture in its header that
  links home, a logo drawn in the page, its app icon — and keeps the three most logo-like ones as pictures of their
  own next to the page ("fernbrook-example-logo", "fernbrook-example-logo-2", "fernbrook-example-icon"), ready for a title card, instead of
  leaving you to cut it out of a screenshot. A menu button, other companies' logos and big photos don't count. It
  reads the site's colours (its theme colour, the colours its styles name, its header, buttons and links) and its
  fonts; a Google font's file is kept too (an open licence), while a site's own font is only named. The same logo
  again is nothing new, a changed logo is a new version, and a picture you removed stays removed.
- **Take a website's brand kit in one step**. `metarush brand suggest --project "Spring promo"` says which
  logo, colours (primary, accent, text, background) and font the site suggests, and why each ("Background #1B1B1F: the
  colour behind the logo in the site's header"); text is always readable on the background. `metarush brand set
  --project "Spring promo" --from-site fernbrook-example --apply` takes it and puts it on the project's latest cut as a
  checked change — a new version, so its title cards and graphics use the colours; nothing is saved until you say so.
  In the dashboard the website's details and the Brand page show "Suggested from …" with **Use for this project**,
  which asks first when it would save a new version of the cut.
- **A project can have its own brand kit**. A team with several clients keeps one kit per client: the Brand
  page has a picker ("Brand kit of" the workspace or a project), and `metarush brand show|set --project <p>` and
  `brand apply` use the project's own kit when it has one, else the workspace's as before. The first change starts
  it as a copy of the workspace's; its logos and fonts stay in the project; "Use the workspace's kit instead"
  (`--use-workspace`) drops it.
- **Split-second flashes of the wrong person at clip edges are caught and trimmed at the exact frame**
  ([call recordings](docs/call-recordings.md)). For a call recording whose picture flips to whoever talks (Zoom's
  speaker view), MetaRush now finds every switch to the frame, not to the second. The edit check, the cut sheet check
  and the quality check say "Clip 3, Lena's answer, opens with a split-second of Maya (0.2 s) before Lena
  appears", and **Trim the flashes** (`metarush cut flashes <project>`, a card in the Claude panel) trims every one at
  the exact switch in one checked change — starting the clip's sound a moment earlier when that would cut words.
  `metarush footage switches <clip>` lists the switches and who is on screen in each shot, and what's on screen
  (`footage visuals`, the clip's On screen tab) now starts and ends each shot on those exact frames.
- **Cover the names Zoom printed with your own name bar**. MetaRush notices the names a call app burns into a
  corner of the picture, and **Cover the printed names** (`metarush cut names <project> [--first-names]`) puts one
  name bar on the whole video, in your brand's colours, over that corner: it shows who is on screen and changes at the
  exact frame the picture switches, so it is never a frame late. It replaces the cut's name titles for the same people,
  and uses first names when the project's rules say so. The quality check flags printed names left showing and a bar
  that names the wrong person.
- **Graphics can last a section or the whole video**. A graphic can now be set to stay up for the whole video
  or from one clip to another, and follows those clips through every edit (no more chains of one-minute pieces). It
  can change what it says while it stays up, at moments in the footage, so a name stays right whichever clip shows
  that moment. Cut sheets spell it plainly: `- names: name.bar for the whole video · with name "Lena"` with
  `Change: at lena-call 2:15.40 · with name "Maya"` lines under it. Project motion graphics that say
  `persistent` may stay up as long as that too.
- **Test frames in the video's own shape**. A motion graphic is now checked in the shape of the cuts that use it
  — vertical for a short, widescreen for the main video, both when it is in both — and its test frames and preview
  are grouped by shape. A graphic no cut uses yet is checked in the shapes the project's brief says the video goes in.
- **"Keep this?" — what you say once is kept for the project** ([keeping](docs/keeping.md)). When a
  conversation brings in something worth keeping, the Claude panel asks once with a card — **Keep** or **Not now**,
  never automatically, never twice: a website you typed (into the sources), a picture or logo you attached (into the
  sources and the brand kit), who the video is for and where it goes (into the brief: "social shorts" are vertical),
  a style rule ("First names only on screen"), a fact about someone (into their notes in People), who must OK the
  video, a problem you raised a second time (checked in every quality check), something MetaRush couldn't do (onto
  the improvement list) and how a name is spelled. `metarush keep …` does the same from a terminal, and
  `metarush keep list --project <p>` shows what was offered.
- **Project rules Claude follows and the quality check measures**. A project now has rules in plain words —
  `metarush project rules add <project> "Captions at the top"` or a Keep card. Claude reads them in every conversation,
  and the quality check measures the ones it can: captions at the top or bottom, first names only, no flashes, a
  length limit, the video's shape; the others are listed for you to look at.
- **Sign-offs before it goes out**. `metarush cut signoffs add <project> "Lena OKs the final version"` (or a
  Keep card) puts a checklist on the cut; approving a version, exporting and downloading the final render remind you
  of the OKs still waiting ("Still waiting for 2 sign-offs: …") — a reminder, never a lock. Tick them on the project's
  page or with `metarush cut signoffs done`.
- **Attach pictures and PDFs to a message**. Drop a logo or a PDF on the Claude panel's message box, paste it
  or use the paperclip (`--attach <file>` with `metarush assistant ask|continue`): Claude can look at it, and MetaRush
  asks whether to keep it in the project. The project's brief now says where the video goes too
  (`metarush project update <p> --deliverable "by email" --deliverable "social shorts"`).
- **A Google key on the free tier is told so on the first shot, instead of waiting for ever**
  ([generative video](docs/generative-video.md#how-a-request-runs)). Google makes video only for projects with billing
  turned on. With a key whose project was on the free tier, Google took the request and then turned away every check
  on it ("too many requests", with a limit of zero), and the shot sat at "Google Gemini is making it" until it was
  stopped by hand. Now the first such answer ends it: "This Google project is on the free tier, which can't make
  video. Turn on billing for the project in Google AI Studio, then try again.", with Google's page for the project's
  tier and limits. Nothing is charged, the money set aside for it is free again, MetaRush asks Google to stop and
  delete the request, and the bell, the activity bar, the shot's record and the Claude Code panel say so.
  - **Checks that only hear "too many requests" stop waiting.** While a shot is being made the activity bar says when
    Google is too busy to answer; after 10 minutes of nothing else the shot waits in line for 10 minutes, and after
    two such waits MetaRush stops, asks Google to stop it and says so (the estimate counts, in case it was made).
  - **The key check no longer promises video.** It says "Connected. Google makes video with Gemini Omni 1.1 Flash
    only for projects with billing turned on; MetaRush tells you on the first shot if this one can't." Google offers
    no free way to see a project's tier.
  - `metarush generate show` (and `--wait`) prints what to do under a shot that couldn't be made.
  - For developers: `videogen.KindTier` (Google's error bodies are read loosely: codes as words or numbers),
    `generate.PollLimitWait`, `PollLimitPause`, `PollLimitPauses`; the fake Gemini's `SetFreeTier(status|start)`
    (`-free-tier`), `[fake:free-tier]`, `[fake:free-tier-start]` and `[fake:poll-rate[:n]]`.

## 0.2.0 — 20 Oct 2026
- **See which version of MetaRush you have, and what's new in it** ([versions](docs/deployment.md#versions)).
  Settings now starts with **About MetaRush**: the version and its day ("MetaRush 0.2.0 · 20 Oct 2026"), what's new
  in it from the notes built into the program (a few headlines first, each opens to say more), and every computer in
  your workspace with the version it runs; a Mac mini still on an older MetaRush is flagged, with how to update it.
  Opened from another computer, it says which computer MetaRush runs on. `metarush version --details` says the same
  in a terminal.
  - **Real version numbers.** The number is written once, in `VERSION` (0.2.0). A build at the release tag says
    `0.2.0`; a build after it says `0.2.0-dev.3` (three commits later), with its commit and build time, the same in
    `metarush version --json`, `/api/health` and the dashboard. `scripts/bump-version.sh 0.2.0|patch|minor|major`
    cuts a release (`VERSION`, this changelog's section, a commit and the tag; it never pushes); `scripts/deploy.sh`,
    `scripts/release.sh`, `bootstrap.sh` and `start.sh` stamp the version in (`scripts/version.sh`), and deploy
    checks it on the installed binary and the running daemon. Builds past a release that leave the renderer alone
    keep the renderer they already set up.
  - For developers: `internal/version` (`Info`, `Compare`, `ParseChangelog`, `WhatsNew`), op `version.show`,
    installations record `commit` and `built` (kept current by the daemon every minute), `scripts/about-e2e.sh`.
- **A render waiting in a terminal no longer hangs after a failed try**. With no computer running MetaRush
  online, `metarush render submit --wait` (and `qa run --wait`) does the work in the terminal. When the first try
  failed in a way worth trying again — on a very busy machine a render can give up waiting for one frame of the
  footage — the render went back in line for a second try, but the terminal had already stopped working on it, so
  the command waited forever ("No machine running MetaRush can render videos right now"). The terminal now takes the
  second try itself as soon as its short wait is over (10 seconds, then twice as long each time). And if the
  terminal ever stops working on something it waits for that still needs doing, it picks it up again — or, when it
  can't, says so plainly instead of waiting forever.
- **The Claude Code panel speaks plainly** ([docs/assistant.md](docs/assistant.md)). What Claude did reads like
  an editor's notes, not a log: "Looked at 19 stills from “Lena interview”" on one line with its thumbnails, "Wrote
  the graphic “name-bar”", "Read the rest of a long result", "Read MetaRush's notes on graphics" — never file names,
  "Ran a command → blocked", picture paths, version codes, IDs or millisecond times. Cards say what a graphic's check
  found in plain words ("It can stay on screen for at most a minute"), and the line under a card you decided says what
  happened ("You saved version 2") while Claude still gets the details it needs. Older conversations read the same
  way. `metarush assistant conversations show` prints the same plain thread (`--details` adds the IDs and commands).
- **Safe steps run by themselves**. Checking a graphic Claude made, rendering a review copy, and bringing in a
  link you typed in the conversation (a page's snapshot, a repository, a download, a video) start without a click,
  as you, and say so on their card; approvals, saving a version, final renders and anything that costs money still
  wait for you. Turn it off in Settings → Claude Code or with `metarush assistant settings --safe-steps off`; the
  activity log and the change history say Claude started them under your setting.
- **Claude hears when work finishes**. When a card starts something that takes a while — a render and its
  quality check, a snapshot, an import — Claude isn't woken to say it started; it hears once it has finished, with
  how it went (the quality check's findings, the snapshot's name), and carries on by itself. Several things finishing
  close together make one message. It keeps watching after MetaRush restarts, and news it hadn't heard yet reaches
  it then; nothing it already heard is told again. The dashboard does this; with no dashboard running, Claude hears
  with your next message (`metarush assistant act` no longer starts Claude Code in a terminal that is about to close).
- **Question cards**. Claude asks its questions as cards: pick one answer, tick several, or write your own;
  your answer goes to Claude as your reply (`metarush assistant act <conversation> <card> --choice 2 --answer "…"`).
- **One card for several approvals; save and render in one click**. When one reply leaves several things to
  OK, they are one card: see them all, **Approve all**, or decide each — Claude hears about them in one message
  (`metarush assistant act <conversation> --all`). A card that saves a new version offers **Save and render a review
  copy** first (`--save-only` on the CLI skips the copy).
- **Setup no longer runs out of time after a long database update**: on a busy or far-away database, the storage
  test and the workspace that follow the update get time of their own (`metarush init`).
- **Find what waits for you; see pictures large.** A badge by the message box counts the cards and questions waiting
  for you and ‹ › jumps to each; the conversation list, its button and the top bar's Ask Claude Code show the count
  (`metarush assistant status` says it too). Any still Claude looked at, a graphic's test frames and its preview open
  large: ‹ › or the arrow keys step through them, Esc closes; it works on a phone and over remote access. While the
  panel is open, pop-up notices appear beside it (at the top on a phone), never over the message box.
- **Bring a Remotion project into a project and use its videos as clips**
  ([docs/remotion-sources.md](docs/remotion-sources.md)). Paste the folder of a Remotion project, or its GitHub
  address, in a project's Sources (or run `metarush import remotion <folder | address> --project <name>`): MetaRush
  keeps its files as version 1 without ever changing the original, leaves out `.env` files and anything that looks
  like a secret, installed packages, build caches and settings for coding assistants (and says what it left out),
  installs the package versions its lockfile pins with install scripts off, keeps its Google Fonts, and reads its
  compositions — each video its code makes, with size, frame rate and length — in a browser that can't reach the
  internet. The videos already in its `out/` folder are added as footage right away. Sources shows each Remotion
  project with a picture of every composition.
  - **Compositions become clips once a person approves the version.** **Use in the cut** (or `metarush remotion use`)
    renders a composition with the project's own Remotion version, in the same sandbox, once per version, composition
    and props, and puts it in the cut; it is then footage like any other (trim it, caption it, put narration and music
    over it, start a generated shot from one of its frames). Props and parts work too (`--props`, `--frames`), and cut
    sheets can name a composition: `4. intro: remotion teaser Intro 0:00 → 0:04 · with headline "Ship faster"`.
  - **Claude can edit the code, and you decide.** `remotion checkout` writes the newest version to edit;
    `remotion check` makes the next version and shows the change in plain words (old → new on-screen words), a type
    check, the compositions it affects and before/after stills of where the picture changed. The version waits for
    you under Sources → **Review**, with **Approve** and **Turn down**; approving it makes the latest cut's clips again
    from it in a new version of the cut, while older versions of the cut keep theirs. Withdrawing an approval refuses
    new renders until someone approves again, and new packages need a person too (**Allow the packages**). Code that
    tries to reach the internet is blocked and fails its check.
  - For developers: `internal/remotion` (ops `remotion.*`, jobs `remotion-import` and `remotion-render`, collections
    `remotion_sources`, `remotion_versions`, `remotion_renders`, migration 26), `video/render/source/` (`analyze.mjs`
    static read, `run.mjs` jailed runs), the `video-remotion` skill, `tests/fixtures/remotion/fixture-film`, an
    acceptance step and `scripts/remotion-e2e.sh`.
- **Fix what the captions got wrong** ([docs/transcripts.md](docs/transcripts.md)). Double-click a word in the
  cut's transcript (or a clip's own), click a caption in a version's review player, or select a stretch and say who
  really says it ("this part is Lena, not Maya"). Only the words that differ change and each keeps the time it
  was said in, so joining "Fern Brook" into "FernBrook" or splitting a word leaves the cut where it was. MetaRush keeps its
  own transcript; every fix says who made it and when, and can be undone from the clip's history.
  `metarush transcript fix|speaker|history|undo`.
- **Find and replace across videos**. Find "Fern Brook" in one clip, the project or everything: every place is
  listed with the words around it and a play button before anything changes, and you untick the ones to leave
  alone. Whole words, match case, and "also put it on the spelling list". `metarush transcript find|replace`.
- **Spelling lists**. Tell MetaRush how names and terms are written, for a project (the studio's Spelling
  button) or for every project (Settings → Spelling list), with the other ways they get heard. Transcripts you
  already have can be corrected, new ones are corrected by themselves, and the transcriber is told to expect the
  words (on this computer it then writes "FernBrook" itself). Fix the same word three times and MetaRush offers to add it
  ("You fixed “Fern Brook” 3 times. Add “FernBrook” to the spelling list?"). `metarush transcript spelling …`.
- **Fixes wait for one new version**. A saved version keeps the words it was made with, so fixing words
  never changes a video you already reviewed. The project says "12 text fixes since version 3 of “Garden story”";
  one click makes one new version and one review copy. Nothing renders by itself. `metarush transcript pending
  list|apply`.
- **Check the captions before a video goes out**. Words fixed since the version, words the computer wasn't
  sure it heard, and names that aren't on a spelling list ("“Elmwood” appears 4 times in the captions. Is it
  spelled right?"), on the version's page and in every quality check. Captions files (.srt and .vtt) have every fix
  without a new render: download one from the version's page, delivery writes them next to the video where the
  place takes one, and `metarush deliver captions` updates delivered ones. Captions drawn into the video need a new
  version. `metarush captions check|file`.
- **Fixes carry over**. When a clip is transcribed again, your fixes are put back where they were; the ones
  the new words don't fit are listed for you to check, and you get a notification.
- **Claude fixes transcripts with you**. Tell Claude in the panel how a name is spelled: it looks first,
  then asks with one card ("Change “Fern Brook” to “FernBrook” in 37 places in 3 videos, and add it to the spelling list"),
  and another for the new version with every fix and its review copy.
- **Faces are recognized across clips** ([docs/visual-index.md](docs/visual-index.md#faces-and-people)). Name a
  face once and MetaRush recognizes that person in your other clips, on this Mac: a face that looks very much like
  the faces you confirmed (and clearly unlike anyone else's) gets their name — "looks like Maya", with a dashed
  outline, **That's Maya** and **Not Maya?** on the On screen tab and on Maya's page, and one bell per person
  ("Recognized Maya's face in 2 more clips"). A face that only looks somewhat like someone becomes a question ("Is
  this Maya?"). MetaRush learns what someone looks like only from faces you confirmed, never from its own guesses,
  and checks again whenever you correct anything; a confirmed face that doesn't look like that person's other faces is
  asked about again. Measured on 164 faces of 20 people: no face was ever named as the wrong person, and with two
  confirmed faces of a person two thirds of their other faces are named by themselves. It uses a 39 MB face model
  (SFace, Apache-2.0) downloaded the first time and checked before use; older clips are looked at again once, by
  themselves, keeping every name you gave. `metarush footage faces` shows names given by looks; `footage faces
  reject` takes one back.
- **Crops can follow movement**. When someone walks across a landscape shot, a vertical video can follow them:
  **Follow movement** under the player of the latest version (beside Keep faces in frame; **Hold still** goes back,
  **Turn off** keeps the middle), `framing: follow` in a cut sheet (or `· follow movement` on one clip), or
  `metarush cut framing <project> follow`. The crop moves smoothly with the main face — or with what stands out where
  there is no face — holds still while they only shift a little, and jumps only where the footage itself cuts. It is
  always your choice: new cuts still keep faces in frame in one place, and versions you already have never change.
- **The quality check looks at what's on screen**. After every render MetaRush checks where the footage's faces
  and text land in the finished picture: a face the crop cuts off, captions or a lower third over someone's face, and
  captions on top of a sign or a title that is in the footage itself — each with a fix you can save as a new version
  (follow the movement, move the captions to the top). On a Mac it also reads the rendered frames to confirm what it
  found.
- **Gestures and text in any language**: what's on screen now includes someone pointing (and which way), a hand
  raised, a thumbs up or an open hand — searchable ("points at the screen") — and reads on-screen text in any language
  Apple Vision knows, not only English.
- For developers: `internal/onnx` (onnxruntime through purego, moved out of `internal/sound` and shared),
  `internal/faceid` (alignment, SFace, matching, the model download), the on-screen helper's protocol 2 (`--landmarks`,
  `--saliency`, `--hands`, `--pose`, `--languages auto`; protocol-1 helpers still work), index `vision-2`, QA `qa-5`,
  catch-up `visual-index` v2, `tools/vision/eval/faces.py --metarush` with a 20-person faceset, `visual-b.mp4` and three
  small public-domain face fixtures, self-test variants `follow` and `qa-zones`, and notes on what the index needs to
  run on Linux and Windows.
- **Open MetaRush from your other computers, over Tailscale**
  ([security](docs/security.md#remote-access-over-tailscale)). `metarush remote enable --allow <their Tailscale
  account>` gives the dashboard an HTTPS address on your tailnet (`https://<computer>.<tailnet>.ts.net`) and prints
  the one command that starts it, `tailscale serve --bg 4845` (`--apply` runs it for you after asking). MetaRush
  itself still answers only on this computer: Tailscale brings the visits to it.
  - **People you list sign in with their Tailscale account**, no link needed, and each is a person in the workspace:
    what they do is theirs in the audit trail, the activity log, "Approved by", notifications and Claude Code
    conversations. Anyone else sees a plain page saying how you can add them; a one-time link
    (`metarush dashboard --print-link --remote`) works for a computer without a listed account.
  - **Settings → Remote access** shows whether it works, the address to copy, who may sign in (add people at this
    computer, remove them from anywhere: they are signed out at once), a "sign out other computers" button for a
    lost device, and plainly says when HTTPS certificates still have to be turned on in the Tailscale admin console.
  - **The dashboard knows which computer it is on.** From another computer, the watched folder, linked folders,
    Show in Finder and Send to editing app say "on <its name>"; the folder picker (which would open on the other
    screen) gives way to browsing that computer's folders or sending a folder from your browser; an editing-app
    export and the browser extension come to your browser as downloads. The microphone records over the HTTPS
    address.
  - **The browser extension** (0.3.0) can pair with the tailnet address as well as this computer's.
  - Safety: the tailnet name is accepted only from `tailscale serve` on this computer (loopback); headers that only
    Tailscale may send are refused anywhere else; Tailscale Funnel (the public internet) is refused; the session
    cookie is `__Host-`, Secure, HttpOnly and SameSite=Strict over HTTPS and is tied to the Tailscale account on every
    request. `metarush remote status|disable|allow|remove|signout`, `doctor`'s remote checks, all people-only where
    they change who gets in. Tested end to end in a browser through an HTTPS stand-in for `tailscale serve`
    (`scripts/remote-e2e.sh`).
- **Move the media to another storage from the dashboard.** Settings → Storage → *Move the media to another
  storage*: pick a folder (or a shared drive), online storage (Tigris, Cloudflare R2, Backblaze B2, Amazon S3 or
  another S3-compatible service) or Google Cloud Storage, fill in its details and *Check what it would copy*
  (nothing is copied or created). *Move the media* asks first in a dialog, then runs the same checked, resumable
  move as `metarush storage migrate`, with its progress in the activity bar; when every file is there, MetaRush
  starts again by itself with the new storage and the page says how it went. Nothing is ever deleted: the old
  storage stays as it is. A secret key is typed once and kept in this computer's keychain, never shown again. On
  your other computers, the Settings tab gets a dot and the page says **This computer still uses the old storage**,
  with the same form filled in: add that computer's own keys and switch it too (it copies anything only it has).
  `metarush storage status` says the same in a terminal (`--details`, `--json`).
- **The plain-words checks no longer mistake your own names for codes.** A clip named by a call app
  (`riverside_sam_raw-video-cfr_garden_show_0002`), a title with a technical-looking word or a person named like a
  code is yours, not MetaRush's: the dashboard now marks people's own words (titles, file names, people's,
  projects' and resources' names) and the command line puts them in curly quotes, the voice guide's mark, which the
  checks read as yours. Everything around them is checked as before, and an internal ID is still caught even inside
  the quotes. The command line's clip lists, clip details, moments and conversation details quote titles and file
  names now (“Garage interview” (“interview-a.mov”)).
- **Ask Claude in the Claude Code panel to cut a call, clean it up, change an angle, make shorts, and fix the sound.**
  Each request becomes a card you confirm with one click, worded plainly and checked first with nothing saved:
  - **Conversations**: make the cut in a style ("Make an automatic cut of “Garden call”: reaction-heavy", with the
    shots and that it replaces the current cut, which stays in History); accept or skip a clean-up suggestion, or
    all that are open ("Cut out the talk about the call at 0:00–0:02"); change what a conversation clip shows ("Show
    Maya and Sam side by side from 0:12"); make a short from the best moments (its new project's name); look a
    call over for glitches. A confirmed cut remembers its style; accepted suggestions are marked with the version
    they made.
  - **Sound**: a person's level ("Make Maya louder", only while she speaks), the music quieter under voices,
    music added or fitted to the video, and a clearer voice: on this computer (free), in the cloud with **its price
    first** and what the project and your ElevenLabs plan have left (it can't be confirmed over the budget), or in
    studio quality with **its one-time setup shown first** (about 1.8 GB), which the card then does before making
    the voice clearer (your decision, in a styled confirmation). Setting clearer voices up is a card too.
  - Claude can read conversations and the sound (`conversation list|show|styles|moments`, `sound show|compare`);
    a call folder is added from the Add box, and the Conversation tab's own changes point there. The panel's steps
    say what Claude looked at in plain words ("Looked for the best moments in “Garden call”").
- **The setup check says what makes voices clearer on this computer.** `metarush doctor` (and Claude, which reads
  it) now has a Clearer voices part: whether Clean is set up (or sets itself up the first time), whether Studio is
  set up, set up only in part or for an earlier MetaRush, and how fast Studio measured when it was set up ("about
  1.6 seconds of work for each second of speech"), with the next step in words. `--details` and `--json` have the
  versions, where each part runs and its folder.
- **The Claude Code panel's `metarush` is no longer inside Claude's working folder.** The read-only copy Claude
  runs now lives in a folder of its own next to the working folder and is reached only as `metarush` on PATH, like
  the live skill tests since the last release: Claude could find the old copy in the working folder's `bin` and run
  it by its path, which the permissions refuse. Claude can't change it either: nothing allows writing there, an
  explicit rule forbids it, and every launch and resume writes it again (a changed copy or a link left in its place
  is replaced, never written through). An upgraded installation loses the old copy; what Claude may run, read and
  write is unchanged.
- **Conversations line up to the sample**. In new cuts (and after `metarush conversation pin`) every
  person's recording plays at the very sample sync found, in the render and in the preview: a recording no longer
  starts up to a frame early or late (measured: 1.7–16.4 ms before, under 0.02 µs now), and a recording whose clock
  runs fast is played from a copy made at the call's own pace instead of being sped up on the fly (that wandered by
  up to 17 ms; now within 2.3 µs). Older cuts play exactly as before.
- **Music follows the whole song and ends on a phrase**. MetaRush now reads a music track from start to
  finish — not just the first 15 minutes — at its own tempo as it changes, finds its bars and its phrases (4 or 8
  bars), and music added or fitted to a video ends where the music starts its next phrase (or on a bar line, or a
  beat, when there is no phrase close enough). Measured on generated music with tempo changes: beats found 99%
  (51–53% before), bar lines 97–99%, endings on a bar line 97–100% (2–27% before). The Sound tab and cut sheets say
  how a track ends ("ends on a phrase"); `metarush sound music add|set --ending phrase|beat`. Tracks fitted before
  end as they always did.
- **whisper.cpp keeps its model loaded between clips**: with whisper.cpp's `whisper-server` from the same
  build next to `whisper-cli`, transcripts in other languages come 1.6–2.5× faster on short clips (1.5× over a mixed
  set), word for word the same. `metarush doctor` says whether the model stays loaded and, if not, why.
- **Studio quality loads its voice model once per run**: making several recordings clearer at once (people
  in a call, clips from different recordings) is about twice as fast (measured 1.8–2.0× for 3–5 recordings). On
  Linux, studio quality now uses an NVIDIA graphics card: setup picks the right build for the card and its driver,
  and falls back to the processor, saying why, when the card can't run it.
- **`metarush machine rekey`**: a machine that joined before each machine had its own storage key gets one in
  place — made, tested and switched to, with everything put back if anything fails — so it can later be removed on
  its own. People only; `--dry-run` says what it would do. The old shared key is never deleted by MetaRush: the
  command says when it can go (once no machine uses it) and how.
- **A live test for Google Cloud Storage** (`TestGCSLive`) runs the storage checks against a real bucket when one is
  provided ([docs/testing.md](docs/testing.md#the-live-storage-tests) says exactly what to set
  up), and in CI when the repository has the bucket's secrets.
- **Generative video** ([docs/generative-video.md](docs/generative-video.md)). Make new shots with Google
  Gemini (Gemini Omni 1.1 Flash): from a few words, continuing from a frame of your footage, between two frames, or
  with an approved look of a person (with their consent) or a character; then Again, Variations, Extend and Edit,
  each a new clip that never replaces one. **Generate a shot** in Sources shows the price before anything is sent and
  the button carries it; nothing is made without that yes (`--max-cost` from a terminal), and the money is set aside
  against the project's budget and today's limit for the team (Settings → Integrations → Generative video, defaults
  $10 each), then settled with what it cost. Each shot runs as a job that survives a restart without being sent
  twice, lands in Sources as an ordinary clip marked AI-generated (badges, a filter, the Script's "Generated" line,
  the cut sheet, the quality check and every export's notes with a checklist per platform), and keeps its full
  recipe (prompt, pictures, model, price table, cost, what Google's filters said); what Google stored is deleted
  right after the download. **Looks and consent**: on the People page, record someone's consent (your own statement,
  or their recording) and make their look from their confirmed faces, check its test shots and approve it; character
  looks are made from pictures. MetaRush never makes a child's or a public figure's likeness, and a prompt naming
  someone or a frame showing them is refused without their consent. Add your own Google Gemini key under Settings →
  Integrations (or `metarush providers set gemini`); `metarush generate|looks|consent …` and the `video-generate`
  skill do it all from a terminal or Claude Code, where making a shot is a priced card.
- **MetaRush no longer stops when two frame grids are drawn at the same moment.** Contact sheets, smart sheets, the
  quality check's sheets and motion-graphic test sheets shared one font face, which isn't safe to use from two
  places at once; two clips finishing together (two takes of a generated shot, for example) crashed the background
  worker and the dashboard with it. Each sheet now draws with its own (`internal/fontface`).
- **Renders no longer fail with "render not found".** A render is now saved before its job is queued, so even a
  machine that takes the job the instant it appears finds it; the same request made twice at the same moment makes
  one render, and a render whose job couldn't be queued is taken back instead of waiting forever. A render job that
  still finds no render waits a moment and looks again (three times, not counted as tries), then says plainly that it
  couldn't find the render and to render the version again. The live skill tests now reach `metarush` through PATH
  like a person's own install, not from a `bin` folder inside Claude's working folder (Claude ran that copy by its
  path, which the permissions block, so `natural-cut` failed for a reason no person would meet), and `--case
  natural-cut` runs just that case (an exact name wins over the cases whose names contain it).
- **Conversations from online calls** ([docs/conversations.md](docs/conversations.md)). One recording
  per person — from Zoom (with "Record a separate audio file for each participant"), Riverside, SquadCast or
  recorded on their own — becomes one **conversation**, lined up by the recordings' sound: each microphone hearing
  the other through the speakers is enough, recordings that started at different times are placed, a clock running
  fast is followed, and MetaRush says how sure it is (very sure, fairly sure, unsure). Nudge a recording a frame,
  or line it up on a clap or on moments you mark. Drop a call's folder in the Add box and it offers **Make a
  conversation from these**; `metarush conversation import <folder>` does the same from a terminal.
  - **Everyone's own microphone plays all the time**; the picture is what switches. Each microphone gets a little
    quieter while its person listens (measured: 17.6 dB less of the listener's microphone), so echo and the other
    voice through the speakers stay out. Captions name each person exactly, from their own recording.
  - **Make the cut** in a style — host and guest podcast, off-camera interviewer (questions heard over the guest,
    or as text cards), panel, reaction-heavy, talking head with cutaways — and the picture follows whoever talks:
    their voice starts a moment before we see them, switches land in pauses (never inside a word), a laugh or an
    "mm-hmm" shows the listener, and people talking or laughing together are shown together (side by side,
    stacked on vertical, picture in picture, a grid for panels), with matching face sizes and eye lines and name
    tags from People. Every shot says why it is there; click any shot on the timeline to change the angle.
  - **Clean up the call**: talk about the call ("can you hear me?", "you're on mute"), long silences, a frozen
    webcam, a sound dropout and the app's name labels on a picture are suggestions to accept (the cut changes:
    cut out, someone else shown, cropped) or skip.
  - **Match the webcams**: brightness, contrast and colour fitted to one person's picture, measured before and after
    ("colour difference 19 → 3.9, brightness 17 → 0").
  - **Shorts**: the best moments (a question and its answer, a laugh, a strong first line) become new projects
    with vertical cuts of their own and captions, or the episode's stretch as it is.
  - **Quality checks** say when someone is much harder to hear than the others, stays alone on screen longer than
    the style allows or talks unseen, and when a frozen webcam or a dropout shows (QA version qa-4).
  - A **Conversation tab** in the studio, `metarush conversation …` (create, import, sync, nudge, cut, angle,
    settings, cleanup, decide, match, moments, short, …, all with `--json`), the Script tab and cut sheets show
    conversations (`## Conversations`, `Shot:` lines), timeline exports carry the shots, and the skills know how to
    edit calls.
- **MetaRush speaks plainly.** Everything you read — the checks on a version before it renders, quality-check
  findings, to-dos, notifications, the activity bar, job details, the activity log, errors, the dashboard's own
  words and the terminal's plain output — now says what it means for the video and what to do next, in everyday
  words: "version 3" (not revision), "preview copy" (not proxy), "quality check" (not QA), "the loudest moments"
  (not true peak), "the music dips under voices" (not ducking). IDs, file paths, codes, commands and raw numbers
  are still there for anyone who asks: under **Details** in the dashboard, with **`--details`** (now on every
  command) or `--json` in a terminal. Errors give the exact command that fixes them only with `--details`; a
  check that still needs your OK ends with "Ready to save. Nothing has changed yet."
  - The version page's "preflight warnings" are now "things to check before it renders", without the codes, and
    narration lines read "Narration line 3 (“It started…”)"; older versions' saved warnings show in today's words.
  - A quality check says "The loudest moments hit the ceiling, so they may crackle once YouTube re-encodes the
    video" instead of "true peak 0.0 dBTP exceeds the -1 dBTP convention"; its measurements, fix steps and frame
    files are behind `--details`.
  - Failed work says what went wrong and what to do ("The transcript couldn't be made. Try again…"), with the
    program's error under Details; a job waiting for a machine says "No machine running MetaRush can make
    transcripts right now. It starts as soon as one is."
  - `doctor` speaks in plain lines ("Database: connected, and live updates work"); versions, paths and fixes are
    behind `--details`. `ingest --wait`, `render submit`, `qa show`, `edit check`, `revision list`, `asset show` and
    the rest name clips, versions and renders instead of printing IDs, job types and JSON.
  - Exports never show full file paths in their messages, and the last narration line no longer says "no pause
    before the next line".
- **Claude talks about changes, not commands.** The skills now ask "Shall I turn the music down a little?" instead
  of asking you to approve a command, send you to the Sound tab for levels, voices and music, and report versions,
  renders and exports by name instead of IDs, paths and hashes. The voice guide has a short glossary of the words
  MetaRush uses.
- **Kept plain by tests.** One checker (`internal/plainwords`) runs over every message in the code, a scripted run
  of the plain CLI output, every string in the dashboard (`npm test -w web`) and a Playwright pass over the main
  pages, and judges Claude's replies in the live skill tests; exceptions are listed with a reason.
- **Slides from the Claude Code panel.** Ask Claude in the dashboard to bring in a GitHub repository or a web page,
  or to make a code slide, a diagram or a repository card: it reads the repository's files and shows each step as a
  card you confirm (a diagram's boxes are listed on its card), and the dashboard runs it as you. Checking a motion
  graphic Claude wrote in the panel now shows its card too (the request was refused without one).
- **GitHub repositories as resources.** Paste a repository in the Add box, or `metarush resources add
  https://github.com/acme/widget --project <project> --wait`: MetaRush keeps it at its current commit (or `--ref` a
  branch, tag or commit) — its text files (pictures, Git LFS files, files over 512 KB and links are left out and
  listed), with an overview: README, top-level folders, languages, licence, recent commits and contributors (from
  GitHub through your own `gh` sign-in when it can, else from the commits fetched). Private repositories use your
  own git/gh sign-in; MetaRush never asks for, reads or keeps a token, and never runs anything from a repository.
  `metarush resources path widget` gives Claude a read-only copy to read. `metarush resources refresh widget`
  pins the newest commit as a new version.
- **Web page snapshots.** A pasted web page (or `metarush resources add https://acme.com`) becomes a dated snapshot:
  screenshots for a computer and a phone, the whole page (graphics name them `homepage`, `homepage#2`,
  `homepage#3`), its readable text (searchable), title, description, theme colour, preview picture and icon.
  `--depth 1` also takes the same-site pages it links to (5 at most by default, `--max-pages`). The snapshot browser
  has network access but never reaches this computer or the local network, refuses downloads and dialogs, and has
  time limits; a refresh takes a new, dated snapshot. A picture's address downloads the picture instead.
- **Code slides, diagrams and repository cards.** `metarush resources make code widget --file internal/queue/queue.go
  --lines 46-63 --highlight 52-55` draws the lines highlighted with Shiki, with line numbers and the file in a header;
  `make diagram widget --from architecture.mmd` draws a Mermaid (or, when installed, D2) diagram Claude wrote as an
  SVG; `make stats widget` draws a card (languages, files, contributors, licence). They are drawn in the same
  locked-down browser as renders, in the brand's colours, and each says exactly where it came from
  (`acme/widget@1a2b3c4d:internal/queue/queue.go:46-63`). When the repository moves to a newer commit, code slides
  follow their code (new versions; a slide whose code changed stays and says so) and cards are made again; cuts
  move to them only with `metarush cut update`.
- **Slides.** Six vetted templates for videos that explain software: `slide.title`, `slide.bullets` (points that
  appear one by one), `slide.code`, `slide.architecture`, `slide.screenshot` (a page in a browser or phone frame; a
  whole-page snapshot scrolls) and `slide.quote`, in the brand's colours and font, in every shape of video, with
  plain fields and cut sheet lines (`- loop: slide.code at 0:09 for 6 s · with title "The worker loop", code
  "main-loop"`). The skills know "make an architecture slide from the repo", "show the homepage at the end" and "put
  a code snippet of the main loop on screen".
- **Send this page** (browser extension 0.2.0): the extension's popup sends the page you're looking at — a picture of
  what's on screen and the page's text, from your own signed-in browser — to the workspace library as a web page
  snapshot. The extension's token gains one permission for it; a browser paired before is asked to pair again
  (sending video keeps working).
- **Project motion graphics can draw pictures.** `useResource("logo")` (or a field marked as a resource) from
  `@metarush/motion` draws a resource by name; `motion check` checks the names against the project and draws the
  test frames with them, a cut that uses the graphic pins them, and renders serve them by hash inside the network
  jail. Still no imports beyond the allowed list.
- **More tags and words found.** On a Mac, SVGs (drawn as pictures first), the first pages of PDFs and web page
  snapshots get Apple Vision's tags and their words (searchable), like pictures.
- **Credits for code and pages.** Delivery notes credit code slides, diagrams and cards with their repository,
  commit, file and lines, and page snapshots with their address and date; QA notes (never blocking) pictures from a
  page or a repository whose licence isn't known. The activity bar shows repositories and pages being brought in.
- **A Sound tab.** Everything about how a cut sounds is in one place, next to the preview: how loud the camera sound,
  narration, music and effects are, and now **each person** — louder or quieter only while they speak, everyone else
  as they are — the music tracks, the automatic help (music getting quieter when someone talks, voices evened out,
  smooth cuts) and the overall loudness. Changes are heard in the preview at once and saved as one new version. The
  Voiceover tab keeps the script and recordings and links to it. `metarush sound show` and `metarush sound levels
  --speaker "Maya" --gain 3` do the same in a terminal.
- **Clearer voices.** Make a person's voice, the narration or everyone clearer without touching the recordings: a
  clearer copy plays instead, over that person's speech only, and switches off and on again for free (A/B), or
  compare every voice as recorded in the preview. Three ways: **Clean** (on this computer, fast and free: less hiss,
  hum and room echo, softer harsh s sounds and pops, an even level; it sets itself up the first time, about 30 MB),
  **Studio** (on this computer, slower: rebuilds the voice for a studio sound and checks every word, so nothing
  anyone said changes; a one-time setup of about 1.8 GB) and **Cloud** (ElevenLabs voice isolation, priced first,
  within the project's budget). A strength slider keeps some of the room. Each result says what changed in plain
  words — the voice clarity score before and after, how much quieter the background is, and a warning when a voice
  may sound processed or not quite like the person — and QA lists the clearer voices a render plays. Timelines sent
  to DaVinci Resolve, Final Cut Pro or Premiere Pro play the clearer voices too (the copies go beside the footage).
  Nothing is processed or paid for twice. `metarush sound enhance Maya --mode clean`, `--estimate`, `sound compare`.
- **Music that fits by itself.** Drop a music file on the Sound tab or on the timeline's new music lane, or choose
  sound already in the project: it starts with the video and ends with it on a beat, with a smooth fade (a shorter
  track fades out early, or repeats). Its level and whether it gets quieter when someone talks are its own; drag it
  (or its ends) on the music lane, or type the times, to place it by hand. You are still asked where the music comes
  from. `metarush sound music add song.m4a`, `sound music set music --level -16 --duck off`.
- **Send a cut to DaVinci Resolve, Final Cut Pro or Premiere Pro.** `metarush export timeline <version> --format
  otio|fcpxml|xmeml --out <file> [--media <folder>]` writes the version as an OpenTimelineIO timeline (Resolve),
  FCPXML 1.8 (Final Cut Pro, Resolve) or Final Cut Pro 7 XML (Premiere Pro, Resolve), built from the same plan the
  renderer draws: clips with their in and out points, split edits with their own sound, transitions on the edit
  points, narration and music tracks with levels, fades and ducking (where the format has them), captions and
  graphics as markers, nested cuts; captions also as an .srt. `--media` puts the original footage beside it under
  readable names (instant copies on the same disk, `--link` for links), and nothing is ever overwritten. Each export
  lists, in plain words, what its format cannot carry (reframing, MetaRush-drawn graphics, speed ramps). In the
  dashboard: **Send to editing app** on finished renders and the Renders tab, with **Show in Finder**. Checked with
  the official OpenTimelineIO library and the FCPXML and Final Cut Pro 7 XML DTDs (`scripts/interchange-check.sh`,
  docs/timeline-export.md).
- **Faster transcription.** The worker now keeps the Parakeet model loaded in a model server between clips instead of
  loading it for every batch; it stops after 10 idle minutes, restarts itself when it has grown too large, and
  falls back to the previous way if it fails. Transcripts are identical word for word. Measured on 11 recordings
  (6 s to 31 min): short clips 2–6× faster, long recordings about 1.3×, 1.38× in all.
  `transcription.resident: false` (or `METARUSH_TRANSCRIPTION_RESIDENT=0`) turns it off; `doctor` says which way
  this machine transcribes.
- **Move your media to another storage.** `metarush storage migrate --to <store-config>` moves the workspace's media
  between S3-compatible storage (Tigris, R2, B2, AWS), Google Cloud Storage and a folder, in any direction. Every file
  is checked by SHA-256 on the way, files this computer already has are not downloaded, and the move can be stopped
  and started again where it left off. It switches this computer and the workspace only once everything is there
  and checked, puts the old settings back if the new ones don't work, restarts a running dashboard onto the new
  storage, and never changes or deletes the old storage. `--dry-run` says what it would copy; progress shows in the
  activity bar. People only (run it in your terminal); each other machine runs the same move with its own keys.
- **MetaRush as an MCP server.** `metarush mcp serve` lets Claude Desktop, Claude Code and other MCP clients use
  MetaRush's operations as tools over stdio. Read-only unless started with `--allow-changes`, and then every change
  takes a second call to confirm; approving versions, deleting footage, moving the media and the other people-only
  actions are never offered. Every call is in the activity log (channel `mcp`). `metarush mcp tools` lists what a
  client sees; setup for Claude Desktop and Claude Code in docs/mcp.md.
- **The Library: an Inbox you can empty without losing anything.** A new **Library** page (in the top bar, and
  linked from the Inbox) shows every clip wherever it is, with a count on each view: **Inbox** (footage nobody has
  filed yet), **Collections**, **Archived**, **In projects** and **Trash**. Its search looks through all of them, the
  Trash included: titles, file names, tags, where an import came from, collection names and what was said. Each clip
  leaves the Inbox one of four ways: added to a project, put on a collection, archived or moved to the Trash, from its
  "…" menu or for several selected clips at once, each with Undo. `metarush library [inbox|collections|archived|
  projects|trash] [--search …]` does the same in a terminal. Asking Claude Code in the dashboard to tidy the Inbox
  gives cards you confirm (archive, put on a collection, move to the Trash, restore); deleting for good stays yours.
- **Collections.** Shelves you name for footage you want to keep together ("B-roll", "Family", "Brand footage",
  "Stock"). Add to collection… finds one as you type, or creates it ("Create “Stock”"); a clip can be on several and in
  projects too. A project can use everything on a collection, like another project: **Use in a project…** on the
  collection, or **From another project** in a project's Sources (the clips show "from B-roll", nothing is copied,
  and clips you put on it later show up by themselves). Taking a clip a project's cut uses off the collection is
  refused, and deleting a collection never deletes a clip (clips a project's cut uses through it join that project).
  `metarush collection create|rename|add|remove|list|show|delete`.
- **Archive.** One "keep for later" shelf: archived clips leave the Inbox without going to a project. From a project's
  Sources, Archive takes the clip out of that project too (refused while its cut uses it). `metarush asset archive`,
  `asset unarchive`.
- **The Trash, in two steps.** **Move to Trash** is instant and can be undone: the clip leaves its collections and the
  Archive (and, from a project's Sources, that project), its open questions wait, and **Restore** puts all of it back.
  It is refused while another project has the clip or any project's latest cut uses it. **Delete forever** and
  **Empty Trash** are for people only: the dashboard first says how much goes ("Permanently delete 3 videos
  (1.2 GB)? … This can't be undone.", with Cancel ready), and a terminal asks you to type "delete"; scripts and Claude
  Code can't. A clip a cut uses (any version) stays in the Trash and says why; stored bytes something else uses are
  kept. Nothing is deleted on its own unless a person turns on **Empty the Trash after 7, 30 or 90 days** (Settings →
  Trash, or `metarush trash settings --empty-after 30`); then MetaRush deletes older clips once an hour, never two
  machines the same one. Dropping a file that is in the Trash again brings its clip back. `metarush asset trash`,
  `asset restore`, `trash list`, `trash empty`.
- **Where it's used** and the search palette say where filed clips are ("On the B-roll collection", "In the Archive",
  "In the Trash"), the palette leaves the Trash out, and on a phone the "…" menu opens on the side with room, and the
  actions of a clip in a list are there too.
- **Fix: waiting in a terminal no longer cuts other work off.** When no worker was online, `metarush render submit
  --wait` (and other `--wait` commands) ran jobs in the terminal, and could start the render's automatic quality
  check; when the render was done the terminal stopped and that check failed ("context canceled"). A terminal now
  runs only the jobs it waits for, and any job a stopping worker or daemon was running goes back in the queue as it
  was (the attempt doesn't count); it is never marked failed. `metarush qa run --wait` then runs that same check.
- **Resources: logos, pictures, PDFs and fonts.** A new library next to footage: PNG, JPEG and WebP pictures (HEIC and
  AVIF photos become JPEG), SVGs (cleaned of scripts and anything that loads from elsewhere), PDFs (each page becomes a
  picture when poppler is installed; `metarush doctor` says so) and fonts (TTF, OTF, WOFF2). Each resource has a short
  name cuts use ("logo"), versions (adding a file with the same name again, or a changed file in a linked folder, is a
  new version), its size and main colours, a thumbnail, where it came from, and suggested tags ("logo", "screenshot",
  "icon"…, with why) you can keep or reject. On a Mac, MetaRush also looks at each picture with Apple Vision, on this
  computer: what it sees is suggested as tags ("dog", "beach", "screenshot") and the words in it become searchable.
  Resources belong to a project or to the workspace library ("Promote to
  workspace"). The project's new **Resources** tab lists what it can use, with filters by kind and origin, and a
  resource's details (rename, tags, versions, where it's used, use as the brand logo or font, download).
- **One Add box, everywhere.** Home, a project's Sources tab (prominent when the project is empty, a compact bar once it
  has sources) and its Resources tab share the same Add box: Choose files, Choose a folder, paste a link, and (in a
  project) From another project. Files go where they belong — video and audio to Sources, pictures, SVGs, PDFs and fonts
  to Resources — and dropping anywhere in a project adds to that project, saying so while you drag ("Drop to add to
  Test project"). Links go through the same classifier as a paste anywhere: videos are imported; GitHub repositories
  and web pages become resources (see above); a link that could be either asks. `metarush add <files,
  folders, links>…` does the same in a terminal. The upload tray says "Added to Resources as “logo”".
- **Linked folders.** "Choose a folder" in a project (or `metarush resources link <folder>`) links it: its pictures and
  fonts become resources and its footage sources, and new or changed files come in by themselves while MetaRush runs
  (files still being copied wait). A file that goes away leaves its resource, marked "missing from folder". Small files
  upload at once and large ones when a cut first uses them (`--upload small|all|on-use`); the folder's path is per
  machine, and the Resources tab shows how it stands on each (pause, resume, look again, stop).
- **The brand kit** (`/brand`, `metarush brand show|set|apply`): the workspace's logos for light and dark backgrounds,
  colours taken from the logo (change any and MetaRush keeps yours) and font (a bundled one or a font file). New cuts
  start with it; "Put it on a project's cut" (`brand apply`) adds it to an existing one as a new version.
- **Logos and pictures in graphics.** The title card, both lower thirds and the end card take an optional `logo`; the
  title card and end card also an `image` behind them (a PDF page as `"deck#3"`); the new `logo.corner` keeps the logo
  in a corner for as long as you like. Cuts pin each resource by name to an exact version (`resources:` in the cut
  sheet, written for you when you name one); a newer version changes nothing until you update (`metarush cut update`).
  The cut's brand can name a font file. Renders draw the pinned files from this computer under the render's network
  jail; QA notes pictures from the web whose licence isn't recorded (information only), and delivery sidecars list
  every resource with where it came from — those shown through a nested cut too, saying which project's cut.
- **Use another project's things.** Link a project to another (`metarush project link "Spring promo" Brand`, or "From
  another project" in the Add box): everything from it by default — footage, resources, approved motion graphics and
  its cut — shown "from Brand", nothing copied, new things appearing by themselves; or only some of it (by kind, tag,
  the people in the footage, a linked folder, or picked items). Stopping (`project unlink`) keeps whatever a cut
  already uses and says so. Search and the Claude Code panel include what links bring in ("12 resources here, plus
  40 from Brand").
- **Cuts inside cuts.** Play another project's cut inside this one as one block, pinned to a version (`metarush cut
  nest`, or "Play it at the end" when linking): picture, sound (narration and mix included), captions and graphics as
  they play there, frame-accurate. The timeline shows it as one block that opens read-only, the cut sheet spells it
  out (`2. intro: cut from brand 0:00 → 0:12.5`, pinned under `cuts:`), and a newer version is pointed out; updating
  is your call. A cut can't contain itself, and nesting stops at three deep. Or copy its clips in as a starting point
  (`metarush cut copy-in`). When nobody says where, a nested cut (or a clip added with `cut add`) goes at the end —
  just before a locked ending, which stays last.
- **Move, remove, delete — and where it's used.** Each source on a project's Sources tab (and in the Inbox) has a
  "…" menu: **Move to another project…** (a type-to-find project picker), **Remove from this project** (the footage
  stays in the workspace; in the Inbox when no project has it; Undo), **Where it's used** (projects, cuts and their
  versions, linked projects, nested cuts; each a link), and **Delete…**: a person only, after a styled confirmation,
  greyed out while another project has it ("Also in fernbrook"), refused while any version of a cut uses it (saying
  where), and it deletes the preview, transcript, speaker and analysis data too, but never stored bytes something else
  still uses. `metarush asset move|where|delete` do the same (`delete` only in a person's terminal, with `--forever`).
  Project names work wherever a project is asked for (`--project fernbrook`).
- **Safer drags onto projects.** A project card lights up only after the pointer stays on it for a moment (or is well
  inside it), only one at a time, and nothing moves under the pointer; a label by the pointer says "Add to fernbrook"; the
  toast after adding offers **Undo** and **Move…**.
- **MetaRush notices how the picture changes.** Besides hard cuts, the footage analysis now finds slow changes (a pan,
  a zoom, light changing, someone coming into view) and bursts of movement (someone walking in), and how busy the
  picture is second by second, so one-shot phone footage is no longer one long blank. `metarush footage moments`
  lists them as visual moments with the frames worth looking at; `footage evidence --json` has `visualChanges` and
  `activity`. Clips analyzed before this catch up by themselves.
- **Contact sheets show every shot.** Sheets now pick one frame per shot first, then the moments the picture changes,
  then new speakers, then enough frames that nothing goes unseen too long (up to 20 by default), and each frame is
  labeled with its time and who is saying what (`0:42.0 · Maya: “and then the bunny…”`). `metarush footage sheet
  <asset> [--from --to] [--mode smart|even] [--details]` (even = the old evenly spaced sheet) and `footage keyframes`
  (which frames and why, without an image); the clip's Frame grid tab uses the same frames.
- **What's on screen, on a Mac.** MetaRush looks at about one frame a second of each clip, plus the moments it
  changes, with Apple Vision on your Mac (nothing is uploaded, nothing is paid for): people and faces, close-up /
  medium / wide, the place, things and animals, and text on screen. The clip's new **On screen** tab reads it out line by
  line ("0:06–0:14 no people · outdoors, beach · no text"), `metarush footage visuals <asset> [--from --to]` prints the
  same, and search (⌘K, `metarush search`) finds clips by what's in them and by text on signs and titles. New clips are
  looked at after their preview is made (a "What's on screen" step of the clip in the activity bar, or "Looking at
  what's on screen in “Kitchen chat”" when it runs on its own; it waits on battery like other big jobs), and older
  clips catch up. It needs the Xcode Command Line Tools: `bootstrap.sh`,
  `metarush doctor` or the first clip builds the small helper; Settings → Looking at your footage and `footage visuals
  status` say whether it works here. On Linux and Windows it is skipped, and a Mac in the workspace does it.
- **"Is this Maya?"** When a face is alone on screen while a person you named is speaking, To-dos and that person's
  page ask whether it is them, with the face outlined. Yes names the face in that clip (the readout then says "1 face
  (Maya)"); No is remembered. `metarush footage faces <asset>`, `footage faces name <face> "Maya"` and `footage
  faces reject <face>` do the same. Faces are not recognized in other clips by their looks: in our tests Apple's image
  fingerprint mixed people up too often (details in docs/visual-index.md).
- **Keep faces in frame.** New cuts keep the main face of each cropped clip in frame (landscape footage in a vertical
  or square video), from what's on screen; the cut says so (`framing: faces` in its cut sheet's output settings), and
  a clip with a focus of its own keeps it. Cuts made before keep their framing exactly, even after their footage is
  looked at; to switch one on, press **Keep faces in frame** under the preview of the latest version (or add the
  `framing: faces` line to its cut sheet), which saves a new version. One clip can also ask for it with `· focus on
  faces`.
- **Written scene descriptions (optional).** Turn them on for the workspace or a project (Settings → Looking at your
  footage, or `metarush footage describe settings --on [--project <id>]` in a terminal) and your own Claude Code writes
  a sentence or two per scene of new clips, or of a clip you ask about (`metarush footage describe <asset> --wait`);
  search finds them. They are off by default because they use your Claude Code usage.
- **Claude looks before it talks about the picture.** The editing skills read what's on screen and the smart sheet,
  then pull frames at the moments the picture changes before saying who is in a shot, what a sign says or how to frame
  it, and treat unconfirmed faces as questions.
- **"… is ready" comes once.** Finding who speaks, looking at what's on screen or describing the scenes later no
  longer sends a clip's "is ready" notification again (a clip renamed in between used to get it again, unread).
- **Record narration live while the video plays.** The recorder now starts in live mode: press Record and the
  video plays from where it stands while you speak. Your take lands exactly where you said each line; nothing is
  chopped up and moved around. MetaRush keeps the whole recording, finds your lines in it, cuts it only in the
  pauses between them (never inside a word) and moves each line's start point to where you said it, tied to the
  shot playing there, so a later edit moves your narration with its shots. Until an edit moves a shot, the take
  plays as one piece, exactly as recorded. A line it didn't hear stays attached to the line before and is flagged.
  **One line at a time** stays as the second mode.
- **A teleprompter that follows the video.** The line to say now is large; the next one waits dimmed and counts
  down from about two seconds before its moment; a mini timeline shows the shots, where each line is due and the
  playhead. Lines without a moment of their own come one after another at your pace, and Space or a presentation
  clicker brings the next one in early. A quiet guide sound is optional.
- **Check a take before keeping it.** The take shows on the narration lane where it landed, with each line's planned
  spot outlined. Lines that run into the next shot or past the end are flagged, with one-click fixes. **Snap lines to
  their planned spots** is a button (never automatic), and **Back to where I said them** undoes it.
- **Re-record a stretch.** Record again from any point, or choose "only line 3": playback starts a second early and
  only that line is replaced, with short crossfades; the line before keeps its last words. Every take is kept, so
  you can still compare takes.
- **Your script as written, or fitted to the video by Claude.** After you paste or import a script you choose. **Use my
  script as-is** (the default) makes one line per paragraph (or per sentence) and guesses no timing: recording sets
  it. **Fit it to the video with Claude** has your own Claude Code read the cut and propose a version: lines tied to
  the right shots, words trimmed or extended to fit, your voice and style kept, and a reason for every change. You
  compare it side by side with yours (changed words highlighted), take Claude's line or keep yours line by line, and
  switch between the two versions at any time. Your original script is always kept exactly as you gave it, including
  in the cut sheet (`### Original script`). Recording and the AI voice use the version in use.
- **CLI.** `metarush voiceover record --live --from <time> [--until <time>]` records while you play the video and
  places the take where you spoke; `metarush voiceover takes place <recording> --from <time>` places a recording made
  that way, and `takes place rd_… --planned` or `--at-recorded` puts a take's lines at their planned spots or back
  where they were said. `metarush voiceover script fit` asks Claude Code to fit the script (inside Claude Code,
  `--propose <file>` checks the version Claude wrote) and `voiceover script use original|fitted` switches.
  `voiceover script show` says which version is in use and lists live takes. `voiceover script import --spread` keeps
  the old way of spreading lines over the shots.
- **Fix: the studio's section tabs stay visible below the top bar** while the activity strip shows (they stick below
  the bar's measured height).
- **Fix: work you ran from a screen is said once.** On a busy machine the bell could announce finished work (a
  cleaned-up line, a placed take) before the screen that ran it had shown the result, so you heard it twice; the bell
  now gives the screen a moment and keeps its notice, read.
- **Each version renders with its own renderer.** The binary now carries the renderer package (the `video/` Remotion
  project, the schema types it imports and the npm lockfile) and sets it up in `<home>/renderer/<version>-<digest>/`:
  `npm ci` with your Node.js, then Remotion's headless browser, shared by every renderer with the same Remotion
  version. The daemon and workers do it the first time they start with a new version (renders wait for it and say
  so); `metarush renderer install|status|prune` manage it (`--json`). Renders, stills and motion-graphic checks use
  only that folder, so a source checkout or a merge into main no longer changes what an installed MetaRush renders,
  and a missing package can no longer break renders after a merge. Renders record which renderer drew them
  (`rendererId`). A renderer that was deleted or damaged is not reinstalled behind your back: doctor, the render's
  error and the daemon log say to run `metarush renderer install`, and the machine stops offering renders until then.
- **Moving older installations.** An installation whose `tools.videoDir` pointed at a checkout now renders with the
  version's own renderer and says so once (daemon log, `metarush migrate`, `renderer install`; doctor keeps an
  information line). The setting stays in `config.yaml`, unused, so an older binary still works. Developers render
  from a live checkout with `metarush renderer dev <checkout>/video` (`tools.videoDir` + `tools.devRenderer`;
  `--off` goes back); `init --video-dir` now means the same, and `./start.sh` turns it on for `.dev/home`.
- **Background service.** `metarush service install [--worker-only]|uninstall|status|logs|restart` runs the daemon
  at login and restarts it if it crashes: a LaunchAgent on macOS, a systemd user unit on Linux, logging to
  `<home>/logs/daemon.log`. `--worker-only` (also `metarush serve --worker-only`) runs the worker and background
  jobs without the dashboard, for an unattended machine such as a Mac mini; `metarush dashboard` there says so
  instead of starting a second daemon. Only one daemon runs per home now; a second one refuses to start.
  `/api/health` reports the commit, so deploys can check what is running.
- **`scripts/deploy.sh`.** Builds the checkout, replaces the installed binary with a new file (never overwritten in
  place), runs skills install, renderer install and migrate, restarts the service or the daemon and checks that
  the running daemon is the new build. `--host <machine>` does the same on another machine over SSH; `--dry-run`
  prints every step (and the remote script).
- **Weekly improvement report.** `metarush improve report` runs the whole improvement pipeline in one step: the
  log's signals and the entries behind them go to Claude Code headless with the `metarush-improve` skill (it can
  only read those files and write the report: no shell, no network), and the ideas are saved for the Improvements
  page. `metarush improve schedule --weekly on` (people only; off by default) makes the daemon do it once a week
  (`improve.day`, `improve.hour`, `improve.budgetUsd`).
- **Release builds and Homebrew.** `scripts/release.sh` builds macOS (arm64, x86-64) and Linux (arm64, x86-64)
  archives with goreleaser, each with the binary, the renderer package, the skills and the docs, plus SHA-256
  checksums, and verifies them; publishing (a GitHub release with `gh`, then the Homebrew tap
  `metavert-io/homebrew-metarush`) only runs after that dry run passes. The tap has a cask (recommended: `brew install
  --cask metavert-io/metarush/metarush`) and a formula; both download the release files through the GitHub API.
  First pre-release: v0.1.0-rc1. These builds were not signed or notarized (signing came later): the cask clears
  macOS's quarantine flag itself, and the first-run step for browser downloads is in docs/installation.md.
- **Import videos from links.** Paste a YouTube, TikTok or X link into the Inbox's new "Or paste a link" field, press
  ⌘V anywhere outside a text box, drop a link on the window, or run `metarush import <link>… [--project <id>]
  [--wait] [--json]`. MetaRush looks the video up, downloads it on this computer (best quality up to 4K, H.264 where
  the site offers it at that quality, merged to MP4) and adds it through the normal pipeline: duplicates, preview,
  transcript, speakers, notifications and job details. The activity bar shows "Looking up the video", "Downloading"
  (percent, speed, time left) and "Adding to MetaRush"; it survives a reload, and a failed import is retried in place
  (a partial download continues). A video already in MetaRush is found before downloading. Playlists and channel
  pages import nothing unless asked (`--playlist 5`, or "Import the first 5"). `--dry-run` looks a link up without
  importing it (title, account, length, license; for a playlist or channel, the videos it would import).
- **Where a clip came from.** Imported clips keep the site, address, video ID, account and handle, upload date,
  original title, a shortened description, the license the site states, and when and how they were downloaded. The
  clip shows "From YouTube · @handle · 12 Mar 2024 · open original" (and more under Details); the default title is
  the original title. Cut sheets name the source in the footage list, `footage.md` lists it, quality checks add an
  information note about outside footage (never a warning), and delivery sidecars get `credits` ("Includes footage
  from @handle on YouTube: <url>"), which `metarush deliver` also prints.
- **When a site blocks the download.** Errors say what happened and what to do in plain words. A bot check or sign-in
  wall offers **Use my browser's sign-in** (`--browser-signin chrome|safari|firefox|brave|edge`): the downloader uses
  that browser's sign-in for this one import; MetaRush never copies or keeps your cookies, and only remembers which
  browser you chose last as a suggestion. Copy-protected (DRM) video is refused.
- **The downloader stays current.** yt-dlp (with Deno for YouTube) is an optional component: `bootstrap.sh` offers it,
  `metarush doctor` checks its version, age, JavaScript runtime and challenge scripts, `metarush import
  --update-tools` updates it (Homebrew, which may also update Python and Deno, or `yt-dlp -U`), and the daemon updates
  yt-dlp, and only yt-dlp, about once a week while nothing is importing (`metarush import --auto-update off` or
  Settings → Link imports to turn that off). The weekly update never upgrades Python, Deno or other Homebrew packages:
  when a newer yt-dlp needs them, it waits, and doctor and Settings say so until you run the update.
- **"Send to MetaRush" browser extension** (Chrome, Brave, Edge; `extension/`). On YouTube, TikTok and X video pages
  it can ask MetaRush to import the original, or save the video in your own signed-in browser (the file itself when
  the page plays one, otherwise a real-time recording of the playing video) and upload it to MetaRush with where it
  came from. Pair it with a one-time code from Settings → Browser extension or `metarush extension pair`; a paired
  browser can only send videos, never read anything, and Settings or `metarush extension revoke` unpairs it.
  `metarush extension path` prints the folder to load unpacked ([browser-extension](docs/browser-extension.md)).
- The activity bar's "time left" rounds to whole seconds first; it could read "2m 60s left".
- **A Voiceover tab in the studio.** **Add voiceover** on the Current cut panel and on the new Voiceover tab covers the
  whole video or one shot, and asks how to start: write your own script, ask Claude to suggest one (the Claude Code
  panel opens with the request ready, in a new conversation), use a recording you have, record it now, or an AI voice.
  Each line is a card with its words, where it starts said the way you would say it ("0.5 s after Kitchen starts",
  "right after the line before", "when Sam says “…”" with the word picked from the transcript, or an exact time),
  delivery notes and an optional voice of its own. A line that runs long says so in plain words with one-click fixes
  (hold the shot longer, start earlier in the pause before it, move the next line, a slightly faster AI voice) or
  "Ask Claude to shorten it". Each card shows the picture where the line starts. Paste a whole script and it becomes
  lines; for one shot it replaces only the lines over that shot. The Script tab's Narration section links here.
- **A narration lane on the timeline.** The Current cut tab's timeline shows every narration line in its own lane, from
  where its speech starts to where it ends. Drag a line and it is tied to the shot it lands on (or keeps following the
  moment or line it followed), snapping to clip starts, words and the playhead; hold Alt to pin it to the exact time;
  the arrow keys move a focused line (Shift: a second at a time). Like every timeline gesture it is previewed at once,
  saved in the background and undone with ⌘Z. An empty lane offers **Add voiceover**.
- **Record narration in the browser.** Pick the microphone and watch its level; the cut plays muted (or quietly, as a
  guide) after a 3-2-1 count-in, under a teleprompter with the line large and the next one dimmed. Record one take per
  line, or the whole script in one go (matched to the lines afterwards). Trying again keeps every take; the Takes
  dialog plays them, puts another one on the line, and switches between the original and the cleaned-up version. A
  refused or missing microphone is explained in plain words with what to do. Recorded takes get no "file ready"
  notice of their own.
- **A simple mixer** on the Voiceover tab: Camera sound, Narration, Music and Effects with mute and "Only this", "Turn
  off the camera sound" (the whole video or single clips), "Music gets quieter when someone talks" and how much, "Even
  out voices", "Smooth cuts", and how loud the video should be, named for where it goes. You hear a change in the
  preview before saving it; saving makes one new version.
- **AI voice on the Voiceover tab**: voices with samples, how it sounds (speed, delivery) in plain words, the price
  before anything is made and what the project has spent, and making or remaking single lines (unchanged lines are
  free; a remade line keeps its old take). Without a key, a plain link to Settings → Integrations.
- **New operations behind the tab** (CLI first, as always): `voiceover.line` (`metarush voiceover beat add|update|move|
  remove`; `--to 0:12.4` ties a line to what plays at that point, `--exact` keeps the clock time), `mix.set` (`mix set`
  builds the change on the server; `--apply` saves it straight away, `--loudness-auto` goes back to the platform's
  usual loudness) and `mix.preview` (`mix set --preview --json`: the plan the cut would play, nothing saved), and
  `voiceover script import --clip <shot>`.
- **Fit warnings follow time, not script order.** A line has until the next line that starts after it (or the end of
  the video), so lines placed out of order never show negative seconds ("-6.8 seconds"); the last line says "before
  the video ends" (not "before the next line"), and a line placed after the end says so. Cuts sound exactly as
  before; only the room the plan reports for out-of-order lines changed.
- **The preview plays mono recordings as loud as the render.** The render plan says how many channels each sound
  source has (`sourceChannels`), and the preview turns single-channel sources down by the same 3 dB the renderer's
  mono-to-stereo step does (measured with `web/scripts/mono-preview-check.mjs`).
- **In the Claude Code panel**, AI voice cards name the voice ("Make the AI voice for 3 lines, with Ada (about 1,200
  credits)"); choosing the narration voice or a spending limit (`providers settings`) and moving a line to a time
  (`voiceover beat move <line> --to 0:12.4`) become cards too.
- **Notifications**: narration work that was put in the cut says "It is in the cut now as version 7." and links to that
  version; work the Voiceover tab ran is reported by the tab itself (the bell keeps it, read); a sound file's "ready"
  notice says "listen to it" instead of "watch it … see its scenes".
- The Voiceover tab's preview no longer floats over the lines and the mixer. In a window narrower than 1280 px a
  landscape cut's preview sits above them, yet it stayed pinned in place as the page scrolled and covered them. Now it
  stays in view only beside them, in its own column under the section tabs; above them (and always on a phone) it
  scrolls with the page.
- The studio's section tabs stay fully visible while the activity strip shows. The top bar grows by the strip (about
  98 px instead of 56) and used to cover the tabs, which stuck at a fixed 56 px; the dashboard now measures the bar
  (`--mr-header-h`) and the tabs, the Voiceover tab's preview and mixer bar, and the selection bars of the Studio,
  To-dos and Revisions pages stick below its real height.
- **Edit the cut by hand on a timeline.** The studio's Current cut tab is now an editor without gaps: drag clips to
  reorder them (a marker shows where they land), drag footage in from the project or the inbox (the part marked with
  I and O or the handles, or all of it), drag a clip off to remove it (with Undo), drag an edge to trim (the rest
  moves up; with ⌥ the cut between two clips moves instead), split at the playhead (S), mark a stretch on the ruler
  and delete it across clips. It snaps to the playhead, the cuts and word boundaries (⇧ turns snapping off), zooms
  (pinch, ⌘-scroll or the slider), shows each clip's frames and sound, and has a key for every drag (J/K/L, arrows,
  [ and ], ⌘Z — press ? for the list); on a phone the handles are finger-sized and a long press drags.
- **Transitions from the timeline**: click the marker between two clips to pick from picture tiles with a looping
  preview of that very cut, lengths in words ("a moment", "half a second") with the exact seconds, and the sound in
  plain words ("Hear the next clip a moment early", "Let this clip's sound carry over", "Change sound in a pause").
- **Changes save themselves.** Each change plays at once (MetaRush checks it as you go) and is saved as an ordinary
  version a moment after you stop, or when you leave the page, with a plain summary ("Moved 2 clips, trimmed 1").
  Undo and Redo go back through saved changes too; unsaved changes survive a reload; if someone else changed the same
  clips meanwhile, you see what they did and choose Keep mine or Keep theirs. Revisions groups your hand edits.
- **Edit by transcript.** The Transcript beside the player shows what the cut says, the word being played lit up.
  Select words (drag, Shift-click or Shift+arrows; on a phone tap one word, then another) and press Delete or "Cut
  out": they come out in the pauses around them, as one change you can undo. The speaker chip on a line still gives
  the line to someone else. **Remove ums and restarts** suggests filler sounds, words said twice and false starts;
  listen to each one taken out (or as it is now), then take it out, keep it, or take out all the rest. A clip's
  transcript in Sources selects words too: cut them out of the video, or mark them to add just those words.
- **Small edits from the command line**: `metarush cut add|move|trim|split|delete|delete-range|remove-words` change
  the latest cut one step at a time, naming clips the way the cut sheet does (or by number) and times as `0:12.4`,
  `+0.5` or quoted words. Each shows the change in plain words with its warnings first, and the same command with
  `--confirm <token>` saves it (`--dry-run` never saves, `--json` for scripts). `cut remove-words "um, anyway"` takes
  words out; `--ums` lists suggestions to `--pick` from. `metarush edit preview --plan …` shows what any plan would
  do without saving. The skills use these for single changes, and in the Claude Code panel they become cards you
  save with one click.
- **New edit ops** `splitClip`, `trimClip` (ripple or roll), `slipClip`, `deleteRange` and `removeWords` go through
  the usual check, confirm, locks and rebasing; clip times are in the footage, so they survive someone else's edit.
- **Project motion graphics.** When no template fits, a project can carry its own Remotion components in
  `motion/<name>/` (`metarush motion new` starts one from a working example). `metarush motion check` reads the code
  and refuses anything outside the allowed imports (Remotion, React, zod, a few `@remotion/*` packages,
  `@metarush/motion` and the component's own files): `fs`, `child_process`, `import()`, `eval`, `new Function`,
  `require` and the like are named with their file and line. It then type-checks, bundles with MetaRush's own
  settings, draws test frames in a browser that can't reach the network (safe area, the region the graphic says it
  uses, the same frame drawn twice, slow frames) and makes a contact sheet and a short preview. Each version is the
  SHA-256 of its sources, so changing one character makes a new version.
- **Nothing a model wrote renders until a person approves it.** `motion approve name@version` (or `--reject`) and
  `motion revoke` are for people only: the new **Motion graphics** page shows the code, its imports, what changed
  since the last approved version, the contact sheet and the preview, with Approve and Turn down; in a terminal they
  ask you to type the name. Workers and QA refuse a cut that uses a version nobody approved, or whose approval was
  withdrawn, and say which step is missing. In the Claude Code panel, Claude can write and check a motion graphic; the
  card that follows links to the review and never approves.
- **Cut sheets and edit specs name motion graphics**: `- route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from
  "Lisbon", to "Porto"` under `## Graphics`, with the full version pinned under `motion:` in the settings. An overlay
  has `motion: {name, hash}` instead of `template`; its fields are checked against the component's own schema, and
  its length against what the component allows. `project checkout|pull|push` carry the sources, and the lock file
  records each version and whether it is approved. The studio's Script tab names the graphic in words ("Motion
  graphic: Route map") and shows whether a person approved the version the cut uses, with a link to its review.
- **Every render runs in a network-jailed browser**, templates included: only the render's own local media server is
  reachable, frames and whole renders have time limits, and a motion graphic's bundle is limited to 2 MB. A component
  that tries to reach the network fails its check and says what it tried.
- The `video-motion-design` skill prefers vetted templates, writes a component only when none fits, checks it and
  asks you to approve it.
- **Split edits built in.** A cut's `transitionIn.audio` can start the next clip's sound early (`lead`: "Sam's voice
  starts 0.4 s before we see him", a J-cut) or let the outgoing clip's sound carry on (`carry`: an L-cut); the other
  sound fades over a short blend, so the outgoing clip's own sound is turned down for real. `auto` moves the sound
  change into the nearest pause using the transcript's word timings and who is talking, so no word is cut. The
  footage limits how far a sound can reach, with a plain warning ("Sam's clip has only 0.2 s of sound before it
  starts, so Sam's voice starts 0.2 s early instead of 0.4 s"). Captions follow the sound (words appear when they are
  heard), and so do music ducking and QA's speech check; narration lines stay tied to the picture.
- **A transition library.** Dissolve (`crossfade`), fade through black (`dip-to-black`) or white (`dip-to-white`),
  fade in (`fade`), wipe, slide, push, zoom and whip, with a direction (left/right/up/down) and pacing (smooth, even speed,
  speeds up, slows down); fade in at the start and out at the end of the whole cut. MetaRush plans every frame of them
  and draws them identically in the preview and the render. `metarush edit transitions` lists them in plain words,
  and `schema/transitions.json` is the same list for the dashboard.
- **Better sound at every cut.** New cuts blend the sound for 0.1 s at straight cuts so they never click
  (`mix.cutBlend`), dissolves blend at an even loudness, and speech is evened out between clips recorded on
  different cameras or microphones (`mix.levelSpeech`, from the loudness measured when footage is imported; each part
  a clip plays is measured on its own). Older cuts keep their exact sound until you turn these on
  (`metarush mix set --level-speech on --cut-blend 0.1`).
- **New edit op `setTransition`** sets one cut's picture and sound (or a clip's fade-out); it conflicts with a
  concurrent change to either clip or a reorder, and respects locks on both clips.
- **A transition style per project** (Natural, Snappy, Smooth, Playful): `metarush project create|update
  --transition-style …`; the editing skills follow it and new rules for which transition when (straight cuts and
  split edits in conversation, a dissolve for time passing, fade through black for endings, flashy ones only when
  the style asks).
- **QA checks at cuts** (QA version qa-3): splits the footage is too short for, a sound change that cuts or loses
  words, two voices at once, single-frame flashes and very short shots, a blip of sound, and loudness jumps across a
  cut measured on the render, each with a suggested fix. `edit check` warns about the same things.
- Footage analysis keeps the loudness every 0.1 s (not shown in `footage evidence`); footage analyzed before uses its
  overall loudness for speech leveling until it is analyzed again.
- **Transitions and split edits in cut sheets.** The `↳` line knows every transition, the way the moving ones go
  (`↳ slide left 0.4 s`), their pace (`· slows down`) and the sound at the cut, written the way an editor says it:
  `↳ cut · Sam's voice starts 0.4 s early`, `↳ cut · voice carries 0.3 s`, `↳ cut · sound changes in a pause` (or
  `auto`), `… with a 0.2 s blend`. The mix settings gain `even out voices: on` and `blend at cuts: 0.1 s`. Saving a
  changed `↳` line is one `setTransition` change, so it conflicts with someone else's change to either clip and a
  locked clip before the cut refuses a transition that reaches into it (also when a plan sets `transitionIn` through
  a clip update).
- **`metarush cut transition <project> <clip> <type> [seconds]`** (op `cut.transition`) changes one cut of the latest
  version: the transition (`dissolve 0.5`, `slide 0.4 --direction left --easing "slows down"`), the sound (`--lead 0.4`,
  `--carry 0.3`, `--auto`, `--with-picture`, `--blend`) or a clip's fade-out (`--end`); the clip is its name or its
  number, and anything you leave out stays as it is. It says the change in plain words ("Maya's clip now comes in
  with a cut, Maya's voice starts 0.4 s early") with its warnings, asks in a terminal, and otherwise saves with
  `--confirm <token>`. In the Claude Code panel it becomes a card that was checked when it was made; the activity log
  reads "Changed the transition into Maya's clip in Spring promo".
- **Script tab** (was "Cut sheet"): the cut sheet three ways. **Formatted**, the default, reads like a script: a
  summary of the cut (shape, length, captions, the footage, with IDs and hashes behind Details), then each clip with a
  still, its name, where it starts and ends and how long it is, who says what, the notes and why it is there; how one
  clip leads into the next as chips (sound phrases after the picture transition included); the narration, graphics,
  sound and captions in plain words. Click a clip to play the cut from there, or open its footage. Anything the view
  does not recognize is shown as written. **Markdown** shows the sheet as highlighted text (read only, with Copy),
  **Edit** is the editor as before. Any version opens with `?rev=` (older ones read only), and "View script" is on the
  Current cut and on every version in Revisions. Old links (`?tab=sheet`) still open it.
- **Fix who said what, line by line**: every transcript line shows who says it; pick someone else, someone new or
  "Not sure" and the words move (lines split where the speaker changes), the voices and speaking times update, and
  "Not sure" asks who it is. CLI: `metarush footage speakers assign <asset> --from <t> --to <t> --person "<name>"`
  (`--new-person "<name>"`, `--unsure`). "There's someone else talking here" listens for one more voice and keeps the
  names you gave (`speakers hint --more`).
- **Two voices become one person only when you say so**: naming a second voice of a clip with a name another voice
  there has asks "Both voices in this clip will be Maya — treat them as one person?", with "No, let me pick another
  name" as the default and a warning when one voice is much lower. Every combination, asked or automatic, has Undo
  (`metarush footage speakers unmerge`).
- **Voices are learned only from what you confirmed**: each person's voice is rebuilt from the clips you named or
  confirmed, and every correction (a new name, "Not Maya?", a line given to someone else, separating voices)
  re-checks automatic labels everywhere, so labels a wrong name produced go away and are asked about again. People →
  **Rebuild voice** (`metarush people rebuild <person>|--all`) does it on request and points at clips where two
  different-sounding voices were combined. Re-detection keeps names and "not them" answers by when people speak.
- **Safer recognition**: while fewer than two other people are named, a voice is named automatically only at a 75%
  match; below that it becomes "Is this Maya?". Automatic names look different (dashed, "recognized by voice, 68%
  match") with a one-click "Not Maya?", and are announced once per person: "Recognized Maya's voice in 2 more
  videos" with Check.
- **Same person?** questions offer "No, not the same" first (the default); the answer is never asked again, also after
  voices are found again, and closes itself when the voices stop sounding alike. "Same as someone else" on the People
  page combines people later, with the same safe default and the pitch warning.
- **Video samples**: voice samples on People, the Speakers tab and To-dos are small video tiles (the frame where the
  sample starts, from a cached `GET /media/assets/{id}/frame?t=&w=`); a click plays exactly that moment with sound and
  stops at its end. People shows a few samples across videos, so a wrong label stands out. "Is this Maya?" shows a
  moment you named as Maya next to it.
- **One person picker**: wherever a name is typed, a combobox finds people as you type (case and accents ignored, the
  start of any word, small typos with "Did you mean Maya?" first, the people you picked recently first), with
  their face and how many videos they are in; someone new is only added through the explicit "Add someone new"
  option. Full keyboard support and screen reader announcements. The CLI resolves names the same way: `No one called
  "Mya" — did you mean Maya?`, and an ambiguous name lists the candidates.
- **Notes about people** (up to 2,000 characters): who they are, how to name or title them, what to highlight or
  avoid. Edit them on the People page (saved as you type) or with `metarush people update <person> --notes "…"`
  (`--notes-file`, `--clear-notes`). They show next to the voice in the Speakers tab, in `people show`, `footage
  speakers` and the project folder's `footage.md`, and Claude reads them before choosing shots, writing name tags and
  captions (the skills and the Claude Code panel, which now gets the people in view with their notes). The activity
  log records that notes changed, never the text.
- **Existing footage catches up after an upgrade**: when the daemon starts and at the end of `metarush migrate`, clips
  with a transcript but no speaker detection get it (normal jobs: battery policy, activity bar, the bell), every
  person's voice is rebuilt from confirmed clips, and projects get the questions earlier versions did not ask.
  Each step runs once per version (`catch_ups`). `metarush footage speakers detect [<asset>…|--all] [--wait]` finds
  who speaks on request.
- `npm test -w web` checks the person picker's matching against the Go matcher's table and the Script view's reader
  against the cut sheet fixtures (also in CI).
- Commands that wait for a job and run it in the terminal (`--wait` with no worker online) now let it finish
  recording itself before they exit: a review copy made that way was sometimes missing from the activity log.
- **Narration and the mix in cut sheets**: `## Voiceover` lists the narration lines in order, each with its words
  and where it starts in plain words ("0.5 s after hook starts", "when garage ends", "after the previous line",
  "at interview-a 0:12.3 in hook", "at 0:20"), with Delivery:, Take:, Why: and Evidence: lines; a derived line under
  each says where it lands and whether it fits. A `Recording:` line holds one continuous read. The settings gain
  `voiceover:` (layout, speaking rate, fitting, pause, lock) and `mix:` (camera sound, levels for clip sound,
  narration, music and sound effects, ducking, loudness, lock); captions can take their words from the narration
  (`timing: voiceover` or `both`). Saving turns narration changes into line-level edits, so a reworded line rebases
  past someone else's change to another line. The change list says each narration and mix change in plain words,
  the way the person hears it ("When a narration line runs long, MetaRush now asks you first.", "Music now gets
  14 dB quieter while the narration plays (was the usual amount).", "The whole video is now made about as loud as
  YouTube and the social apps expect."), findings about a narration line sit on its line and call it by its name,
  and the editor marks the shot and footage names a line's start point refers to. The skills send requests about
  sound levels alone to the voiceover skill's mix steps.
- **Cut sheet proposals in the Claude Code panel**: a cut sheet Claude writes (`<name>.cut.md` in its proposals
  folder), or asks to save, becomes a card with the changes in plain words and the changed lines; one click saves it
  as a new version (the revision records where it came from and what was asked: the sheet's `change:`, else your
  message), and Claude hears the outcome. The assistant's cut sheet commands read files only from its own proposals
  folder. The activity log has these cards like any other (proposed on Claude's turn, then what you decided).
- The activity log tells cut sheets and project folders in plain words ("Saved a new version of Spring promo from a
  cut sheet", "Put Spring promo in a folder"), and log exports and bug reports recognize a project folder by the same
  `.metarush/lock.json` that `project checkout` writes.
- Cut sheet checks list their notes in sheet order, errors first.
- **`scripts/acceptance.sh` cleans up through `store.DropTestDatabase`** (`go run ./scripts/droptestdb`, which
  refuses anything but a local `metarush-test-*` database), also when a step fails.
- **Cut sheets** (`.cut.md`; ops `cut.show`, `cut.check`, `cut.diff`, `cut.apply`, `cut.compare`;
  `metarush cut show|check|diff|apply`): a cut as Markdown you can read, change, compare and keep in git. Settings at
  the top, a footage list (short names made from each clip's title), numbered clip lines with times and their first and last
  words, transitions in plain words ("dissolve", "fade through black"), `## Graphics`, `## Sound`, `## Captions`, and
  `Why:` and note lines. Lossless: printing a version and reading it back gives exactly the same cut (checked with
  fixtures, random cuts, fuzzing, and a completeness test that fails when a new edit field has no spelling). Reading is
  strict: every problem has a line and column, a plain message, a hint and a stable code. Saving turns the sheet into
  the smallest set of changes against the version it came from and goes through the usual check and confirm step;
  changes someone saved meanwhile are folded in when they don't overlap, and explained in plain words when they do
  (`E_REVISION_CONFLICT`). See docs/cut-sheet.md.
- **Cut sheets name the people speaking**: the derived quote under each clip says who speaks (`> Ada: “…”`, and
  the next speaker when the voice changes), the footage list shows who talks in each piece of footage, and
  `footage.md` lists who speaks with names on every transcript line. Names come from the voices people named;
  unnamed voices show as "Speaker N" when there are several. Captions can show speaker names from the sheet
  (`speaker names: true`).
- **Quoted words instead of times**: `from "so I quit" to "in my tiny garage"` finds the words in that clip's
  transcript (ignoring case and punctuation, tolerating different word splits and small spelling differences), cuts
  just before the first word and just after the last with a little room (never into a neighboring word), and shows the
  exact times it chose. Missing or repeated phrases list the closest matches or where each one is.
- **Notes on clips, sounds and graphics** (`notes` in the edit spec): a person's own text, kept with the cut and never
  shown in the video.
- **Project folders** (ops `project.checkout`, `project.status`, `project.pull`, `project.push`;
  `metarush project checkout <project> <folder>` and `project status|pull|push [folder]`): `cut.md`, `footage.md` and
  `.metarush/lock.json` (which version the files came from); no media, secrets, logs or caches. `status` says what
  changed here and whether someone saved a newer version; `pull` refuses to overwrite local changes (naming the files)
  unless `--force`; `push` saves `cut.md` as a new version after a dry run and confirm. `--details` (and `sheetUrl`
  in `--json`) gives the dashboard link to the Cut sheet tab.
- **Studio: Cut sheet tab**: the cut as an editable sheet (CodeMirror, loaded only when the tab opens) with checks as
  you type (problems underlined on their line, with the hint), Apply changes through the usual confirm dialog, older
  versions read-only, and a side-by-side comparison of any two versions as sheets. Works on phones and in both themes.
- **`metarush revision compare <a> <b> --format md`**: what changed in plain sentences plus the changed cut sheet lines.
- **Edit ops**: `set` in `updateClip`, `updateOverlay` and `updateAudio` is now a JSON merge patch (nested objects
  merge, `null` removes a field, unknown fields are rejected), and the cut an ops plan produces is checked against the
  schema too, so values such as a speed outside 0.25–4 are caught at check time.
- **Skills** teach cut sheets (`skills/_shared/cut-sheets.md`; video-edit-revise, video-story-editor,
  video-motion-design, video-project) and `skills install` adds `metarush cut show|check|diff` and
  `metarush project status` to the read-only allowlist; `skills test` has cut sheet cases.
- Cut sheets reserve `motion: name@version` for project motion graphics and report it as not supported yet.
- **A log of everything MetaRush does.** Every command and dashboard change, every job from queued to finished
  (progress at quarters, failures with their advice, retries, stops), the programs it runs (never their output; the
  end of the error output when one fails), ElevenLabs requests (what they did, how long, characters billed — never the
  key or the text) and every exchange with Claude Code (what was asked, the final answer, the steps it took, the
  changes it asked for and what you decided, conversations started, resumed, interrupted, stopped or out of budget),
  with the machine and versions involved. Passwords, keys, tokens and cookies are removed before anything is saved.
  Dashboard look-ups are counted every five minutes instead of listed one by one. Read it on the new **Log** page
  (filters for problems, Claude, jobs, programs and services, a period, a project, search; every entry opens to its
  full prompt and answer, error output and what belonged to it; live updates; save as a file) or with `metarush log
  list|tail|show|search|export`; "See log" on failed jobs and problem notifications opens the entries behind them.
  `metarush log bundle` writes a bug report zip (entries, failed jobs with their error output, setup check,
  versions, settings summary, a README) that is checked for secrets before it is written. Entries stay until you
  delete them (`log purge`, Settings → Log) unless `log.retentionDays` is set. What you ask Claude is kept with the
  workspace by default — `log configure --prompts local|off` (or Settings → Log) keeps it on this machine only or not
  at all. Exports and bug reports are never written into code repositories, synced folders or project folders.
- **Ideas from the log.** `metarush improve signals` lists what keeps going wrong (errors, failed service requests,
  retries, refused or ignored changes, people correcting Claude, Claude saying MetaRush cannot do something, long
  tool chains, slow steps); the new `metarush-improve` skill has Claude Code group them into missing features and
  friction, rank them and save a report; the new **Improvements** page (and `improve list|show|keep|dismiss|export`)
  lets you keep or dismiss each idea and export them as Markdown.
- **Claude Code terminal sessions can be recorded** (off by default): `metarush log claude-hooks enable` adds hooks to
  your Claude Code settings (backed up first; `disable` removes only MetaRush's). They run in every session but keep
  only the ones that used MetaRush: what you asked, Claude's answer and the MetaRush commands it ran.
- Skill tests (`metarush skills test --live`) are recorded in the log too, each case as its own conversation.
- **Narration in the Claude Code panel.** Claude can read the narration script, check that every line fits, price an
  AI voice, look at voices, models and takes, read the sound mix and check the AI voice setup. Narration and mix
  changes it asks for become cards with plain titles ("Add a narration line: …", "Make the AI voice for 3 lines (about
  1,200 credits)", "Turn the music down under speech", "Make the narration fit") that put the change in the cut as a
  new version when you confirm. An AI voice card is priced before anything is spent and cannot be confirmed without a
  key on this computer or over the project's budget. Adding, testing or removing an API key, copying a voice and
  recording with the microphone are never done for you, not even as cards. A cut without narration suggests "Write a
  voiceover script for this cut". `voiceover scratch|tts|cleanup|import --apply` puts the result in the cut directly.
- **Narration shows up like any other work.** Making scratch or AI voice, cleaning up narration, placing a recording
  and making a voice copy are recorded as jobs (new job type `voiceover`, run on the machine that asked, which holds
  the key and the files): the activity bar says what is happening ("Making the AI voice for line 3"), job details
  give a plain title, the version it belongs to, what it made and advice when it fails, and the bell says when it is
  ready to review ("AI voice ready for 3 lines") or could not be done. The work can be stopped between lines; input
  mistakes never create a job; the audio files MetaRush made for narration no longer appear as files of their own or
  send "file ready". Narration jobs are started again where they were asked for, not retried elsewhere; if MetaRush
  stops while making narration, a worker closes the job once its lease runs out and the bell says so ("MetaRush
  stopped while doing this. Start it again: lines that were already made are reused at no cost.").
- **Every render gets a quality check.** QA, including listening to the speech word by word, is queued automatically
  after every review and final render for the person who asked for it (`render submit --no-qa` skips it). The QA
  notification leads with what the speech check heard ("Around 0:27 a phrase plays twice in the rendered video") or
  says every expected word was heard; job details add the same in plain words.
- The migration runner applies every version not yet recorded (not only versions above the highest), so builds that
  merge in a different order never skip a migration; `doctor` counts waiting updates the same way.
- **Narration (voiceover) in the edit spec.** A cut's `voiceover` is an ordered script of lines, each placed by an
  anchor Go resolves to frames: a clip's start or end (+ offset), a moment in the footage (asset + source time,
  followed through whichever clip shows it), after the previous line (+ gap), or a clock time. Clip- and
  moment-anchored lines move with re-edits. New edit ops `setScript`, `updateVoiceover`, `addBeat`, `updateBeat`,
  `moveBeat`, `removeBeat` (lockable: a line, `voiceover`); validation of anchors, order and fit (155 words a minute
  until a recording measures the narrator's pace: "Line 3 … has 28 words but only 4.1 seconds before the next line —
  it needs about 9.5 seconds"); `fitPolicy` auto | ask | shorten and `voiceover fit [--apply …]` building the fix
  (extend the shot, start earlier, move the next line, speed AI speech up ≤ 8 %). Plain-text script import with
  `[@clip+0.5]`, `[@clip:end]`, `[0:12.4]`, `[after+0.8]` and `(delivery notes)`; paragraphs without markup are
  spread over the shots. Captions can follow the narration (`captions.source` voiceover | both); with speaker names
  on (`showSpeakers`), narration lines are named "Narrator" next to the on-camera speakers when captions mix both,
  and narration-only captions show no names (one voice, nothing to tell apart).
- **Narration audio**: free scratch voice (`voiceover scratch`: macOS `say`, Linux `espeak-ng`, otherwise "not
  available"); ElevenLabs text-to-speech with word timing (`voiceover tts`, `--estimate` first, per-project spending
  limit, cache keyed by provider + model + voice + settings + text so unchanged lines are reused for free, one line
  regenerated with `--beat`); importing a recording (`voiceover import`: stored as an ordinary asset, transcribed
  locally, aligned to the script, split at each line or kept continuous, narrator pace measured); recording from the
  terminal (`voiceover record`: ffmpeg avfoundation/ALSA/PulseAudio, on-screen prompter, count-in, per-line takes);
  cleanup presets (`voiceover cleanup`: high-pass, noise reduction, compression, de-essing, loudness, silence trim) as
  a second version with A/B per line (`voiceover takes choose --original|--processed`); a voice copy of your own voice
  (`voiceover clone`, people only, consent statement in the audit log; anyone else's voice needs their recorded
  consent). Takes live in the new `voiceover_takes` collection (migration 6, additive indexes; the migration runner now
  applies every version not yet recorded, so builds merging in any order never skip one).
- **Mixer**: `mix` in the spec — buses `source` (the clips' own sound), `voiceover`, `music`, `sfx` with gain, mute and
  solo; mute the original sound of every clip or one clip; automatic ducking (music −14 dB and clip sound −18 dB
  under narration, music −12 dB under on-camera speech, smooth ramps); a master loudness target per platform with
  one static gain from measured source loudness and a true-peak guard (mono sources count 3 dB lower, as the
  renderer's equal-power upmix plays them; 0.5 dB is kept free for AAC overshoot; `mix set --peak-ceiling`). Go resolves per-element gain envelopes into
  the RenderPlan; Remotion interpolates them, so the preview and the render mix identically (cuts without narration
  or a mix render exactly as before). `mix show` / `mix set`, op `setMix`; `render:selftest` gains a mixed-audio
  check (muted source, ducked music, narration level).
- **Integrations (API keys) from the CLI or the dashboard.** New ops `provider.list`, `provider.status`,
  `provider.set`, `provider.test`, `provider.remove`, `provider.settings`; `metarush providers list|status|set|test|
  remove|settings`. Keys are per machine — OS keychain, or `<home>/.env` in env mode — and are read from a hidden
  prompt, `--key-stdin` or `METARUSH_ELEVENLABS_API_KEY`, never from argv. They are never stored in MongoDB or
  `config.yaml`, never logged, audited or returned (status shows `••••3f2a`, where it is kept, the last check and plan
  usage). New dashboard screen **Settings → Integrations**: add/replace a key (checked before saving, shown once),
  check the connection, remove it (styled confirm), choose the default ElevenLabs voice from your voices or the public
  library with play-and-stop samples streamed through the daemon (`GET /api/providers/{name}/voices/{voice}/preview`),
  favorites, a voice per project, the model and how the voice sounds (plain labels; raw values behind Details), and a
  per-project spending limit. Voice and model choices are workspace defaults → project overrides → per-line
  overrides; models come from the live model list (the default prefers high-quality multilingual models with word
  timing, never a hard-coded ID). `voiceover voices [--search] [--library] [--preview <voice> --out <file>]`,
  `voiceover models`. New stable error codes `E_PROVIDER`, `E_BUDGET`, `E_CONSENT_REQUIRED`.
- **QA `qa-2`**: narration checks (narration over on-camera speech, music or clip sound less than 10 LU under the
  voice, narration distortion and clipped recordings, recordings that do not match their script, lines that start
  before their spot) and a **speech continuity** check that transcribes the render locally and aligns it with the
  words the plan expects — repeated words, missing words and unexpected speech with timecodes ("Around 0:27 a word
  repeats in the rendered video"), errors for a whole repeated or dropped phrase; `qa run --no-speech-check` skips it.
  For cuts whose mixer sets the overall level, loudness and peak findings suggest mix settings (loudness target, peak
  ceiling, music level where nobody talks) instead of per-clip gain changes the mixer would undo, and say plainly
  when the narration's loudest moments set the limit or the narration itself is turned far down.
- **Delivery sidecars** describe the narration: lines by source (recorded, imported, tts, scratch), `aiVoice` with
  the voices and models used, a plain notice ("This video contains AI-generated narration.") and warnings for lines
  still on the scratch voice or without audio.
- New skill **video-voiceover** (conversational narration workflow; live-eval cases for scratch timing, cost and the
  mix); story-editor, edit-revise, QA and deliver skills and the shared CLI/edit-plan references cover narration.
- **Secrets stay out of git and sync folders.** The keychain remains the default. Every file-based secret — an
  env-mode `<home>/.env`, join bundles (`machine invite`), the CI dotenv (`machine ci-key`) — is checked before it is
  written or used: refused inside iCloud Drive, `~/Library/CloudStorage/*` (Google Drive, OneDrive, Dropbox, Box),
  `~/Dropbox*`, `~/Google Drive`, `~/OneDrive*` and Box folders (no override), and inside a git work tree unless git
  ignores the path and `--allow-unsafe-secret-path` is given (writing; an existing ignored `.env` is still loaded).
  New stable error `E_UNSAFE_SECRET_PATH`. Every installation home gets a `.gitignore` containing `*`; bundles and CI
  dotenv files default to `~/.metarush-bundles/` (0700) instead of the current directory; `machine join` deletes the
  bundle after a successful join (prompt in a terminal, `--keep-bundle` to keep). `doctor` adds home/`.env`
  location checks (fail in sync folders or where git would commit them), secret-file permissions, the home
  `.gitignore`, and stray join bundles / credential dotenv files (with paths). `scripts/secret-scan.sh` (CI job
  `secret-scan`) fails on likely secrets in tracked files, with `scripts/secret-scan.allow` for documented placeholders.
- **Activity bar and drawer** (op `activity.summary`, `metarush activity [--watch] [--details]`): uploads, footage
  being prepared (per-step chips), renders, quality checks and Claude Code requests with stage, percent, speed/time
  left, the machine doing the work, and why anything waits (battery, no machine that can run it, big jobs turned off,
  busy, retrying), with Process now and Retry. Rebuilt from server state, so it survives reloads and matches every
  machine. The home page's "In progress" list and per-clip status chips use the same summary.
- **Trustworthy uploads**: persisted server stages (sending → checking → fingerprinting → storing → processing →
  ready) with bytes and rate; a stalled upload (15 s without progress) reconnects and resumes from the server's
  offset; after a reload the tray shows interrupted uploads ("Interrupted at 18%"), and dropping the same file again
  resumes where it stopped (matched by name, size and fingerprint). A chunk cut off mid-transfer marks the upload
  interrupted at once. The tray folds away once everything is stored and starts folded on phones.
- **Duplicates caught before uploading** (op `asset.precheck`, `metarush ingest precheck`): size + fingerprint of the
  first and last MiB, confirmed by a sampled hash (look-alike files upload normally), also within one drop; older
  assets get fingerprints on demand. A styled dialog offers Skip (default) or Keep both, which records the filename
  as an alias on the stored clip (op `asset.duplicate`, `metarush asset duplicate`, `ingest --keep-both`); the bytes
  are never stored twice. The watched folder keeps both; CLI and folder digests report duplicates.
- **Battery policy**: big jobs wait while a machine is on battery below `worker.heavyBatteryThreshold` (default 25%,
  0 = never), set from Settings or `metarush worker configure --heavy-battery-threshold`; the device publishes its
  battery and reason, one notification per episode (also across restarts), Process now (`job.expedite`,
  `metarush job run-now`) runs them anyway, and `--wait` runs inline regardless. `METARUSH_FAKE_BATTERY` simulates a
  reading for tests.
- **Notification bell** (ops `notification.list`, `notification.read`; `metarush notifications list|read`): footage
  ready (one per clip), render finished/failed, quality check done, Claude Code proposals, duplicates skipped,
  battery pauses, interrupted uploads; per person, on every machine, unread badge survives reloads, toasts for new
  ones. Sentences are plain; technical errors are kept in details.
- **Titles**: clips get an editable title (default: the filename without its extension), renamed inline in the
  dashboard or with `metarush asset update <id> --title`, audited, used in search, lists, activity, notifications and
  CLI output; the original filename and any aliases stay visible as file info.
- **Library**: grid or list view per gallery (remembered), j/k/Enter/x keys, multi-select, drag clips onto a project
  (highlighted drop targets, a count on the drag image, Undo toast); the picker is the keyboard alternative.
- **Watched folder made easy**: the inbox header shows its state (watching, off, folder not found) and opens a setup
  dialog with a home-folder browser (create folders, validation that refuses the disk root, the home folder and
  MetaRush's own folders); Settings has a Watched folder section with an on/off switch, the native macOS folder
  picker, Open in Finder, Use default and Import what's already there. Changes write only `worker.watch` (comments
  and references in `config.yaml` survive) and apply without a restart. CLI: `metarush watch
  status|set|on|off|import|choose|reveal|browse|mkdir`; ops `watch.*`.
- **Search** (op `search`, `metarush search`): what was said (exact phrase > all words > word forms > partial words,
  with timecodes from word timing), titles, filenames and aliases, projects, versions and comments, grouped and
  highlighted; the palette opens a clip at the moment with the word highlighted, and the transcript has find-in-page.
- **Preview playback**: preview media plays from this machine's cache or streams same-origin through the daemon
  (never redirected), `preview.prepare` (`metarush preview prepare`) caches a version's sources with progress, the
  Player props are stable across renders, a "Buffering…" chip appears while it waits and a "may stutter" hint only
  when buffering keeps happening.
- **Job details**: every job opens in a dialog (deep link `?job=<id>`, also from activity and notifications) with a
  plain title, state, who started it, when, on which machine, how long, what it made (a playable render, a quality
  check summary, a transcript size), attempts, Retry with the raw error under Details, and one "Go to …" link to the
  result. `job.show` returns the same (`related`, `target`, `advice`); `metarush job show [--details]`.
- **Plain-language output**: activity, notifications, job details and CLI output speak to the person editing; IDs,
  links and commands appear with `--details` (CLI) or under Details (dashboard), and `--json` has everything.
- New mark: a clapperboard (favicon, touch icon, header) that reads at 16 px in dark and light themes.
- Fixes: dialogs opened inside the app now render within the router (job details could not open); "Go to" from a
  job no longer bounces back; on phones the activity strip shows short labels and buttons meant for wider screens
  stay hidden; the Ask Claude Code button in a project no longer shows on phones.
- Migration 3 (additive): `notifications` collection and indexes; indexes on `assets(workspaceId, size)` and job
  lookups by asset/state and finish time.
- **Conversations with Claude Code in the dashboard.** The Ask Claude Code panel (one button in the top bar; the
  project page's own Ask buttons are gone) now holds real multi-message conversations with the person's local Claude
  Code: one long-lived `claude -p` process per conversation (stream-json in and out, messages on stdin, never in
  argv). Follow-ups typed while Claude works are queued (and can be removed); Stop interrupts the current reply and
  keeps the conversation; "Stop Claude Code" ends the process; idle sessions sleep after `assistant.idleMinutes` and
  the next message resumes them with `claude --resume`, under the same locked-down flags on every spawn. `/fresh`
  starts over, `/model`, `/effort`, `/budget` and `/title` change the conversation; ↑/↓ recall earlier messages,
  drafts are kept per conversation, ⌘↵ sends.
- Replies stream as they are written (safe Markdown with code highlighting) with plain activity rows ("Pulled stills
  from interview-a.mov", "Checked the plan “tighten” → looks good, 2 notes"), thumbnails of the stills Claude looked
  at (served only from the scratch, cache and assistant folders), Claude's checklist, and per-message footers; a
  meter shows how full Claude's context is. "Show raw prompts and tool details" (CLI `--raw`) reveals the exact
  messages, system rules and tool input/output.
- **Action cards**: a change Claude asks for but may not make (apply a plan, render, check a render, approve, create
  a project, analyze footage, add a note) becomes a card with a plain summary and the technical diff under Details;
  a person runs it with one click (as themselves, on the human channel) or says "Not now", and Claude is told the
  outcome. A plan Claude asks to apply shows one card, not two. Checked plans and render requests still appear as
  cards at the end of each reply.
- Full screen (Esc leaves it): the conversation list, the thread and the current cut side by side. Streams continue
  while the panel is closed and reconnect after a reload (SSE with Last-Event-ID).
- Conversations are stored in MongoDB (`assistant_conversations`, migration 5): owner, machine, title (Claude names it
  from the first reply; renamable), turns, queue, cost, a bounded transcript and action cards. Browse, search and
  archive them from any machine; only the machine that holds a conversation's Claude Code session continues it.
  Earlier single requests are imported as one-turn conversations.
- The activity bar and drawer show Claude Code conversations (working now, or a recent reply with plans ready or
  waiting for your OK), and a reply that leaves plans or asks for your OK notifies the person who wrote the message;
  both open that conversation (`?ask=<conversation>`; links to earlier single requests still work).
- The Claude Code panel works with speakers and questions: Claude can check who speaks in a clip, the people you
  named and the open questions; naming or correcting a voice, renaming, merging or splitting people, and answering
  or skipping a question become cards you confirm. When voices are unnamed or questions are open, the empty panel
  suggests going through them.
- Billing mode aware: with a Claude plan sign-in no dollar amounts are shown (the per-conversation limit is an
  API-equivalent safety limit, $25 by default, and the raw view labels estimates "API-equivalent estimate (not billed
  on your plan)"); with an API key the budget ($2 by default) and spend are shown. `assistant status` prints the
  billing mode.
- **Settings → Claude Code**: the installation's default model and effort, chosen from what the installed Claude Code
  offers (asked with Claude Code's `initialize` request, no prompt sent; validated on save), and the
  per-conversation limit. Conversations can override them; the choice is re-applied when a session resumes.
- New ops, each with a `metarush assistant` command (`--json`): `ask`, `continue`, `interrupt`, `stop`, `dequeue`,
  `rename`, `archive`, `configure`, `conversations list|show`, `proposals`, `apply`, `act`, `models`, `settings`,
  `suggestions` and `status`. Commands that need the live process go through the local daemon. The empty panel
  lists a few "Things to try" (`assistant suggestions`).
- Skills and the assistant speak plainly to amateur editors (`skills/_shared/voice.md`, installed with every skill);
  skill descriptions trigger on everyday requests that never say "MetaRush"; `metarush skills test` checks the voice
  and gains `--live --natural`.
- **Who is speaking.** Speaker detection now runs for every transcript, Parakeet and whisper.cpp alike (words take
  the speaker they overlap most; segments split where the speaker changes), at clustering threshold 0.7. A repair
  step with local voice fingerprints (TitaNet-Small through the sherpa-onnx C library, loaded with purego) folds
  short split-off lines into the right speaker and merges one person split in two: on a public-domain 39-minute
  press conference 23 detected clusters became the 6 real speakers with no speaker confusion (docs/speakers.md). Each voice gets 2–3 short listening samples.
- **People across the library.** Name a voice once (`metarush footage speakers rename <asset> "Speaker 2" "Ada"`,
  the dashboard, or a to-do) and Ada is recognized in other videos: automatically when the voice match is high,
  as "probably Ada (86% match)" in between. Profiles keep several voice samples per recording and only learn from
  names a person confirmed. Naming two voices of one video the same merges them. `metarush people
  list|show|rename|merge|split`; `footage speakers reject` ("not Ada"); `footage speakers hint --count N` re-detects
  voices without re-transcribing (job type `diarize`). Names flow into stored transcripts (`speakerName`),
  `footage transcript --speaker "Ada"`, footage moments and captions (`captions.showSpeakers`, rendered above each line).
- **To-dos.** Questions MetaRush cannot answer itself (unnamed voice, voice match, music without rights, missing
  brief goal/audience/tone when a story edit is requested, two people who sound the same, a cut waiting for
  approval) are created idempotently (collection `todos`, one open per group key), closed automatically when the data
  changes, and answered through the normal operations: `metarush todos list|show|answer|dismiss|count|sync`
  (`--json`), a To-dos page (`/todos`: listen to the voice, see the moment, pick a suggestion or type a name,
  keyboard flow, answer several at once) and badges with open counts on video cards, the filmstrip, the source
  header, project cards, the project header and the navigation (`todos.counts`, live over SSE).
- Music rights can be recorded on the music file (`metarush asset rights`; `assets.rights`); validation and delivery
  sidecars accept them.
- Dashboard: speaker-colored transcript with names and a one-person filter, a "who speaks when" lane under the
  waveform, a Speakers tab (listen, name, confirm or reject suggestions, "how many people talk?"), a People page.
- New ops: `footage.speakers`, `footage.speakers.samples` (local only), `footage.speakers.rename`,
  `footage.speakers.reject`, `footage.speakers.hint`, `people.list|show|rename|merge|split`,
  `todos.list|show|answer|dismiss|counts|sync`, `asset.rights`; media endpoint `/media/assets/{id}/snippet`.
  Migration 4 (indexes for `asset_speakers`, `people`, `todos`). Skills check open to-dos and ask instead of
  guessing; the read-only allowlist gains `footage speakers list`, `people list|show`, `todos list|show|count`.
- **Speakers and to-dos meet activity, job details, the bell and search.** Listening again ("how many people
  talk?") shows in the activity bar as "Finding who's speaking in “Kitchen chat”", waits on battery like other big
  jobs (with Process now), opens the clip's Speakers tab from job details ("Go to the speakers") and notifies when
  the voices were found (notification kind `speakers`). Job details, the activity bar and the bell say when a
  finished clip has open questions ("2 questions about this clip") and link to the To-dos page filtered to it
  (`questions` on `job.show`, `activity.summary` items and `notification.list` rows, built by one helper). Search
  finds people by name (videos with `matchedIn: "speaker"`) and transcript hits carry `speakerName`; a search hit
  opens the clip's transcript even if another tab was open. To-do questions, speaker summaries and the People page
  use clip titles, and a clip's questions follow it when it is renamed or dragged into (or out of) a project; video
  cards and list rows show to-do badges; the CLI's speakers, people and to-dos output keeps IDs and commands behind
  `--details`; worker cards name job types in plain words. The top bar fits every width from phones up (section
  labels from wide screens, People reachable from the To-dos page on phones).
- Developer: `schema export` gives `todos.Counts` the TypeScript name `TodoCounts` (it collided with
  `activity.Counts`) and now refuses any two types that would share a definition name. The watched-folder check
  explains a blocked folder the same way every time (it depended on map order).
- **Per-machine credentials:** `metarush machine invite <name>` mints the machine's own storage key on Tigris (IAM
  API; scope `no_default_allow`, limited to the store prefix by an IAM policy, or a `ReadWrite` bucket role with
  `--key-scope bucket`), proves it with a round trip, optionally creates an Atlas database user with `readWrite` on
  this database only (`--atlas`, via the Atlas CLI; password on stdin) and writes a 0600 join bundle (secrets never
  printed; everything rolled back on failure). `machine join <bundle>` sets the machine up (keychain secrets) and
  runs the doctor checks; `machine list [--keys]` shows installations, key labels, key status and last seen;
  `machine revoke <name> [--atlas]` deletes the key (and user), marks the installation revoked and refuses to cut off
  the current installation or a shared key without `--force`. Other providers (R2, B2, S3, GCS, custom) get exact
  manual steps and `--access-key-id`. `machine ci-key` mints the CI key (only `ci-test/*`). New ops `machine.list`,
  `machine.invite`, `machine.revoke`. Runbook: docs/runbook-add-machine.md.
- Installations gain additive fields (`state`, `storageKeyId`, `storageKey`, `dbUser`, invite/join/revoke times).
  `init` now upserts only the fields it owns (an invited record keeps its key details), records the storage key ID
  in use, and refuses a revoked installation; `doctor` fails a revoked installation and shows the machine's key.
- `atlas` joins the allowlisted tools; tool invocations can pass stdin.
- CI: a live-bucket step runs `TestS3Live` against the real bucket when the `METARUSH_TEST_S3_*` secrets exist
  (skipped on forks); DB tests must not skip in CI (`METARUSH_REQUIRE_MONGO=1`).
- Tests: cross-machine worker failover (a worker dies mid-job on A, B recovers the lease, A's late
  completion/heartbeat/failure are rejected, the result is visible from A) and cross-machine render pickup (render
  queued on A, A offline, B renders; skipped without Remotion).
- **Native Google Cloud Storage driver** (`--media gcs --gcs-bucket … [--gcs-prefix …] [--gcs-credentials <key.json>]`):
  service-account key file (path stored, contents never copied) or Application Default Credentials — no HMAC keys.
  Large uploads use GCS resumable sessions whose URI and committed offset are kept in the ingest session, so an upload
  interrupted by a crash or restart continues from the last byte GCS acknowledged (on any machine); expired sessions
  start over automatically; GCS validates the CRC32C/MD5 declared at session start and the stored object is checked
  again. Ranged reads, V4 signed URLs when the credentials can sign (otherwise the dashboard streams), `ingest
  reconcile` cancels abandoned sessions it recorded. `doctor` reports credentials, key-file permissions, signing and
  `STORAGE_EMULATOR_HOST`. The S3-interop preset (`--s3-preset gcs`) remains as an alternative.
- **Cache management.** The daemon (and `worker start`) enforces `cache.maxGB` every `cache.pruneIntervalMinutes`
  (default 10, and soon after downloads): least-recently-used objects go first, pinned objects and objects in use by
  a running job, render or operation on this machine are never evicted (in-use leases are shared between processes);
  results are logged and shown in `cache status`.
- `metarush cache prefetch --revision <id> | --project <id> [--pin] [--dry-run]` downloads, resumably and verified,
  everything a render or review needs offline (render sources — the CFR mezzanine for VFR footage — proxies,
  thumbnails, peaks, the latest render), with progress.
- `metarush cache repair [--dry-run]` re-hashes every cached object, removes corrupt objects, abandoned partial
  downloads and stray temporary files, and re-fetches pinned objects.
- `metarush sync status [--project <id>] [--json]` (op `sync.status`, read-only): per asset variant and render,
  local copy vs canonical-only (pinned/in use/partial), pending or interrupted ingest sessions, queued/running jobs,
  daemon and worker liveness, database and media-store reachability with latency, and a short summary. Works without
  a database connection (reports local state only).
- New ops: `cache.status`, `cache.prune`, `cache.prefetch`, `cache.repair` (the last three local-only), `sync.status`.
- **Render QA** (`metarush qa run <render|revision> [--wait] [--force]`, `metarush qa show <id>`; ops `qa.run`,
  `qa.show`; worker job type `qa`): decodes the finished MP4 and checks frame count/duration (±1 frame), size,
  frame rate, codec/pixel format and audio presence against the RenderPlan; black and frozen frames, with planned
  fades, dips, fade-outs and full-frame cards (and black/static spans in the source evidence) told apart from
  defects; EBU R128 integrated loudness and true peak against platform conventions (≈ −14 LUFS social/YouTube,
  −16 LUFS generic, ≤ −1 dBTP); silence where transcripts have speech (word timing); layout heuristics from plan
  geometry (captions stacked with or hidden by overlays, overlapping overlays, flashing captions, end cards that
  stop just before the end, platform safe areas). Findings carry severity, kind, timecode, basis (observed | heuristic) and suggested fixes as edit ops.
  A labeled contact sheet, a moments sheet (safe area and caption band outlined) and stills at every cut, overlay
  and finding are stored in the media store, so any machine can show them. Reports live in `qa_reports`
  (migration 2) and a summary on the render (`render.qa`). `qa run --wait` works without a daemon.
- **Delivery** (`metarush deliver --revision <id>… --out <dir> [--preset …] [--quality final|review]
  [--allow-draft]`; op `deliver.export`): copies finished renders with descriptive file names and a JSON sidecar
  each (title, project, revision number/ID, spec hash, approval, instruction and rationale, render details,
  source assets, music rights, data-card claims, QA summary). Checks everything before writing, re-hashes every
  copy, never overwrites (hard-link placement), never uploads; drafts only with `--allow-draft`, rejected never.
- **Skills**: `video-qa` (bounded repair loop: at most two rounds, asks before each apply, never approves),
  `video-deliver`, `video-motion-design` (templates, brand tokens, timing and collision rules, sourced data cards)
  and `video-workspace-admin` (doctor, workers, cache, migrations, joining a machine; never prints secrets).
  `skills test` now also fails when a skill pre-approves a command that is not read-only; four new live-eval cases.
  `qa show` and `worker list` join the read-only allowlist.
- Render and QA share one plan-resolution function, so QA checks exactly what was rendered.
- **Dashboard assistant**: the Ask Claude Code panel now runs the local Claude Code headless
  (`claude -p`, stream-json) in a per-installation workspace (`<home>/assistant`) with the MetaRush skills, a read-only
  `metarush` shim and a strict allowlist (dontAsk mode, read-only metarush commands, writes only to the run's
  proposals folder). It streams the transcript (text, condensed tool rows, blocked steps, cost) over SSE, can be
  stopped, keeps a run history, and turns the edit plans it wrote and checked into cards with the diff and warnings:
  **Apply** (styled confirm → `edit.apply` through the ordinary ops endpoint) and **Render review**. Plans written
  against a head that has since moved show as out of date. Falls back to copy-a-prompt, with the reason, when Claude
  Code is missing, too old or signed out. Endpoints `GET /api/assistant`, `GET|POST /api/assistant/runs`,
  `GET /api/assistant/runs/{id}`, `…/events` (SSE), `POST …/cancel`, `GET …/proposals`; CLI `metarush assistant status|runs`;
  config `assistant.{maxBudgetUsd,timeoutMinutes,model,disabled}` and `tools.claude`.
- Revisions record `origin` (`assistant:<run>/<file>` for applied assistant proposals); history shows "Assistant
  proposal, applied in the dashboard". Assistant runs are audited (`assistant.run`).
- Defense in depth for headless use: with `METARUSH_VIA=assistant` (set by the shim) the CLI allows only read-only
  commands (no `--out`/`--dir`/`--home`) and ops refuse every mutating or human-only operation.
- **Contract cleanup**: `metarush schema export` also writes `schema/api.schema.json` (status, session,
  uploads, SSE payloads, waveform peaks, stored technical evidence, assistant endpoints → `schema/ts/api.ts`); the
  dashboard's hand-written mirrors are gone. One generated template catalog, `schema/templates.json`, is imported by
  the Remotion package (the old `manifest.json` and per-template regions are gone; a drift test guards it).
- Renders store `renderedByName` and job attempts `workerName` when they are written; the dashboard shows those names.
- `internal/tools`: Claude Code is an allowlisted tool; tools can run with a clean environment; an overlong stdout line
  no longer stalls a streaming tool.

## 0.1.0 — 2026-10-17 (the first local version)
- Single Go binary: CLI, local dashboard daemon, job worker, watched inbox.
- `init` (interactive and non-interactive; database and media store chosen independently; keychain secrets),
  `doctor` (tools, architecture, database, TLS, Atlas org record, storage round trip, cache, disk, workers).
- Media stores: S3-compatible (Tigris, R2, B2, AWS S3, GCS interop) with resumable multipart uploads and presigned
  ranged reads; filesystem (local or shared mount). Content-addressed objects. GridFS intentionally not implemented.
- One ingest pipeline for CLI, browser uploads (resumable) and the watched inbox: content sniffing, SHA-256 dedupe,
  verify, then proxies, thumbnails, contact sheets, waveform peaks, CFR mezzanines for VFR sources.
- Local evidence: Parakeet TDT v2 transcripts (sherpa-onnx) with word timings and speaker diarization, whisper.cpp fallback for other languages (the choice measured on a development Mac); scene, silence, black, freeze and loudness detectors.
- Edit spec, plans and typed ops with schema + semantic preflight, immutable revisions, locks, compare-and-swap
  branch heads, safe auto-rebase, conflicts, HMAC confirm tokens, human-only approval.
- Durable job queue on MongoDB (atomic leases, heartbeats, retries, recovery, idempotency, cancel).
- Remotion package with vetted templates; renders verified by decoding before they are marked available.
- Dashboard server: one-time sign-in links, Host/CSRF guards, ops endpoint, media streaming, SSE live updates.
- Six Claude Code skills (project, ingest, footage review, story editor, edit/revise, render) with an installer that
  never overwrites your edits, a CLAUDE.md block and a read-only permission allowlist; static and live (`claude -p`)
  discovery tests.
- `bootstrap.sh` for fresh clones; `start.sh` for development.
