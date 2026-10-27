# What a project keeps: "Keep this?" cards, rules, the brief, sign-offs and checks

The brand kit a website suggests is in [resources.md](resources.md).
Code: `internal/keep` (the kept things and the dedupe), `internal/ops/rules.go`, `internal/ops/signoffs.go`,
`internal/assistant/keep.go` (cards and detectors), `web/src/layout/assistant/KeepCard.tsx`,
`web/src/components/project/ProjectMemory.tsx`.

## Why
Say a conversation about a garden club's open day video mentions, in passing, who the video is for, where it goes,
how it should look, who must OK it, how a name is spelled, the club's website and something MetaRush couldn't do.
Without a place to keep them, all of that would live only in that conversation: the next conversation, a terminal
session or another person would start from nothing.

## Keep this? cards
When a conversation brings in something reusable, MetaRush asks **once**, with a card in the panel's open-questions
navigator: never automatic (a keep card is never a safe step), never asked twice (one record per thing, by
its content, per project).

| What came up | The card | Keeping it |
|---|---|---|
| A website, video or repository the person typed, or a page Claude asked to fetch | "Add fernbrook.example to this project's sources?" | `resources.fetch` (a page snapshot or a repository) or `ingest.url` (a video), into the project |
| A picture that looks like a logo: one the person dropped into the chat, a logo a page snapshot found, a picture link Claude fetched | "Keep this logo in the sources and the brand kit?" (with the picture and the colours) | the picture in the project's resources; it becomes the logo of the project's brand and the colours come from it (or from the site). A project that uses another's settings gets settings of its own first, a copy of what it used: a card never changes other projects' brand |
| Goals, the audience, where it goes | "Keep this in the project's brief?" — "For members, neighbours and the public; by email, on the website and as social shorts (shorts are vertical)" | `project.update` (merged: only the fields given change); deliverables carry their shape, which new cuts, motion checks and renders use |
| A style rule | "Keep this rule for the project? “First names only on screen”" — it asks where: **Just this project** or **Every project using fernbrook's settings** | `project.rules.add`, or a house rule of the project's Content Settings (`content.rules.add`); Claude reads both at the start of every turn, and the quality check checks the ones it can measure |
| A promise made to the client ("we promised them captions on every video") | "Keep this as a promise to the client? “Captions on every video”" — it asks where, like a rule; one MetaRush can't measure makes no card (it stays a sign-off) | `project.rules.add` / `content.rules.add` with `promise` (`metarush keep promise … [--every-project]`): every quality check measures it, and a version that breaks it waits before it goes out ([ship-gate.md](ship-gate.md)) |
| A fact about a person | "Add this to Lena's notes? “Volunteers at FernBrook; runs the garden club”" | `people.update` (a line added to the person's notes) |
| Who must OK the video | "Add this sign-off to the cut? “Lena OKs the final version”" — it asks where: **Just this project** or **Every project using fernbrook's settings** | `cut.signoffs.add`, or a house sign-off of the project's Content Settings (`content.signoffs.add`); open sign-offs are a reminder before approving, exporting and delivering |
| A problem the person raised a second time | "Check this in every quality check of the project? “No split-second flashes of the wrong person at clip edges”" | a rule that is a check (`project.rules.add` with `check`); measured where MetaRush can |
| Something MetaRush couldn't do | "Add this to MetaRush's improvement list? “Steady shaky footage”" | `improve.add` (one idea, `metarush improve`) |
| How a word is spelled | "Put “FernBrook” on this project's spelling list?" (on "the spelling list of “fernbrook”'s settings?" when the project uses another's) | `transcript.spelling.add` on the list of the Content Settings the project uses |

Each card says what keeping does in one plain sentence, with **Keep** and **Don't keep** (it used to be "Not now",
which now means "later" on other cards). Don't keep records the thing as
declined: it is never offered again in that project (the person can still keep it later from the project's page or a
terminal). Decisions reach Claude in the usual batched note.

### Where the cards come from
1. **MetaRush's own detectors** (deterministic, at the end of each turn, after Claude's own cards are known):
   - links in the person's messages of the turn (the same matching as safe steps): one card per link that the
     project doesn't have (no resource or clip from that address) and that no card of the conversation already fetches
     or imports;
   - pictures and PDFs the person dropped or pasted into the message box (**attachments**, below);
   - a page snapshot or picture link a card of this conversation brought in, when it found a logo.
2. **Claude**, at the end of a turn, reports the rest in a fixed form: one `metarush keep <kind> …` command per thing
   (`brief`, `rule`, `person`, `signoff`, `check`, `improve`, `spelling`; `source` and `logo` too, for things it found
   itself; the long forms `project rules add`, `cut signoffs add` and `improve add` count too). Like every change, the
   command is refused for Claude and becomes the card (`internal/assistant/keep.go`, `describeKeep`); a thing offered
   before makes no card, and every message's context lists what the project keeps and what was offered before
   ("Already offered to keep in this project (never offer these again): kept: …; the person said not now: …"), so
   Claude doesn't ask again. The panel's system rules and the skills say what is worth keeping and to word it the way
   the person said it.

```
metarush keep source <link> --project <p>          a website, video or repository → the sources
metarush keep logo <picture> --project <p> [--brand] [--from-site <page>]
metarush keep brief --project <p> [--goal …] [--audience …] [--tone …] [--deliverable "by email"]…
metarush keep rule "<rule>" --project <p> [--measure …]
metarush keep check "<problem>" --project <p> [--measure …]
metarush keep person "<name>" "<fact>"
metarush keep signoff "<who OKs what>" --project <p>
metarush keep improve "<what MetaRush couldn't do>" [--why …]
metarush keep spelling "<word>" --project <p> [--heard-as …]
metarush keep list --project <p> [--state asked|kept|declined]   ·   metarush keep decline <item>
```

At a terminal each keeps the thing at once (the skills ask the person first); `--json` everywhere.

### The record and the dedupe
`keep_items` (`kep_…`, migration 32): workspace, project, kind, the content key, the words, the details (address,
resource, person, brief fields, rule check, word), state (`asked`, `kept`, `declined`), the conversation and card, who
decided and when, and what keeping did. The key is the kind and the content normalized (lower case, letters and digits
only, a link by `linkKey`): `(workspace, project, key)` is unique, so two machines or two turns can't ask twice, and a
thing already kept or declined is never asked again. `metarush keep list --project <p>` shows them;
`metarush keep <kind> …` at a terminal keeps a thing at once (a person's terminal; the skills ask first);
`metarush keep decline <item>` declines one.

### Attachments
The panel's message box takes pictures (PNG, JPEG, WebP, GIF, SVG) and PDFs: drop them on it, paste them, or use the
paperclip; at a terminal `metarush assistant ask|continue … --attach <file>` (repeatable). Each file goes to
`assistant.attach` (people only; 20 MB at most, from the dashboard too: the op raises its request limit, `ops.Flags.MaxInput`), which sniffs its
type, reads a picture's size and stores it in the media store by its SHA-256 — in the conversation, not the project.
The message names its files (`AssistantMessageIn.attachments`: name, SHA-256, size, type, picture size; checked
against the store), the turn and its thread event keep them (`Turn.attachments`, shown on the person's message from
`/api/assistant/conversations/<id>/attachments/<sha256>`, which serves only files the conversation's messages name),
and when the turn starts MetaRush copies each into the conversation's proposals folder (`proposals/attachments/`, the
one place outside its working folder Claude may Read) and says so in the message ("The person attached
"fernbrook-logo.png" (a picture, 240 × 120, 9 KB): <path> — Read it to look at it…"). At the end of the turn each becomes a
keep card for the project's sources (one that looks like a logo — named logo, icon or brand, an SVG, or a small
roughly square picture — also for the brand kit). Nothing is added to the project until the person keeps it.

## Project rules (and checks), and house rules

A rule kept for **every project using the same Content Settings** is a **house rule**: kept in the settings
(`content.rules.list|add|remove`, `metarush content rules`, `metarush keep rule … --every-project`), read by Claude
and the quality check next to each project's own rules and labelled ("House rules from “fernbrook”'s settings…", "The
house rule … (from “fernbrook”'s settings)"). A "Keep this rule?" card asks which: `assistant.act` with `keepWhere`
(`project`, the default, or `settings`; `metarush assistant act … --keep-where settings`).
`Project.Rules` (`project.rules.list|add|remove`, `metarush project rules`): plainly worded rules ("First names only
on screen", "Captions at the top", "Nothing too long for an email"), each with who added it, when, where it
came from, and whether it is a **check** (a problem the person doesn't want to see again). Claude reads them at the
start of every turn (the message context says "This project's rules: …"), the skills read them with `project show`,
and the dashboard shows and edits them on the project's page.

**Measured where MetaRush can** (`measure`, recognized from the words when a rule is added, or given):

| Measure | Recognized from | What the quality check does |
|---|---|---|
| `captions-top` / `captions-bottom` | "captions at the top/bottom" | the version's captions must sit there; fix: `setCaptions` |
| `first-names` | "first names only" | names in name bars and lower thirds must be one word; fix: the first word; `cut names` uses first names |
| `no-flashes` | "no flashes", "no quick flips", "split-second" | `edge_flash` findings are errors |
| `max-length:<s>` | "under 4 minutes", "at most 90 seconds" | the video must not be longer |
| `shape:<landscape|vertical|square>` | "widescreen", "vertical", "square" | the version's shape |
| `max-length:<s>@<shape>` | "under 60 seconds for shorts" | only videos of that shape must not be longer |
| `captions` | "captions on every video", "always subtitled" | captions on wherever someone speaks, large and slow enough to read |
| `captions:burned-in` | "the captions are already in the footage" | the cut's own, or the footage's own read by the on-screen index (unread: couldn't be measured) |
| `logo-end:<s>` / `logo-start:<s>` | "logo in the last 3 s", "end card at the end", "logo in the first 5 seconds" | a logo (or an end card) through the last seconds / within the first |
| `speech-over-music:<steps>` | "no music louder than speech", "voices 8 dB over the music" | the voices that much louder than the music wherever both play |
| `disclosed` | "nothing generated without disclosure", "label AI clips" | every AI-made part told about on screen or in the description |
| `faces-in-frame` | "no faces cut off" | no face cut off by the edge (the on-screen check, on a Mac) |
| `min-size:<px>` / `min-fps:<n>` | "at least 1080p", "4K", "30 fps" | the picture's shorter side, the frame rate |

A rule (or house rule) can be a **promise made to the client** (`promise`: `--promise`, the rules lists'
"It's a promise to the client", a keep card): it must be measurable, every quality check measures it with a plain
sentence and a result, and a broken one is a problem that holds the version back from going out
([ship-gate.md](ship-gate.md)). Taking a promise back is a person's.

A rule it can't measure is listed in the report as one for a person to look at (check `project_rules`, information).

## The brief and deliverables
`Brief.Deliverables` (`[{name, shape, platform}]`) next to the goal, audience and tone: "by email" and "on the website"
are widescreen (`landscape-1080p`), "social shorts" vertical (`vertical-1080p`, platform `shorts`), "square" square.
They set: the shapes a motion graphic's test frames are drawn in when no cut uses it yet, the platform for
loudness and safe areas when the cut says `generic` (as `Brief.Platforms` did), and the shapes the dashboard's render
menu offers first. The brief is in the message context of every turn too.

## Sign-offs
`Project.Signoffs` (`cut.signoffs.list|add|done|undo|remove`, `metarush cut signoffs`): a checklist on the cut ("Lena
OKs the final version"), each open or done (who ticked it, when, a note). Where the ship gate is on
([ship-gate.md](ship-gate.md)) an open sign-off holds a version back from going out, and ticking or removing one is a
person's. Where it is off, open sign-offs are a reminder, never a lock:
`revision.approve` and `deliver.export` say "Still waiting for 2 sign-offs: …" (their JSON has `signoffs`), the
dashboard shows them on the latest version and asks once, in a styled dialog, before approving or downloading the
final render while any is open.

**House sign-offs**: a sign-off every project using the same Content Settings needs ("Legal OKs it before
export") is kept in the settings (`content.signoffs.list|add|remove`, `metarush content signoffs`, the House
sign-offs part of the Settings tab; refused through a project that uses another's, like every part). A cut's
checklist (`cut.signoffs.list`, `ops.SignoffsFor`) is the project's own sign-offs for it, then the house's, each
marked `house` with whose settings hold it (`houseOf`; the project page shows "from fernbrook"). Ticking a house sign-off
(`cut.signoffs.done|undo`) records the OK for this project's cut only (`Project.houseSignoffs`), so one video's OK
never clears another's; taking it off a cut is refused with the command that takes it out of the settings, and
taking it out of the settings takes the OKs given for it too. Every reminder reads both (`OpenSignoffs`), and so does
the message context Claude gets ("Sign-offs still waiting …"). A "Keep this sign-off?" card asks where, like a rule's
(`assistant.act keepWhere`, `keep.add where: settings`, `metarush keep signoff … --every-project`).

## Tests
House sign-offs: `internal/ops` `TestHouseSignoffs` (both levels, ticks per project, the reminder, refusals,
keeping where), `internal/assistant` `TestKeepSignoffAsksWhereAndClaudeHearsTheHouseStyle`, and `keep-e2e` (the card's
two places; kept for every project it is a house sign-off).
`internal/keep` (keys, dedupe, decisions), `internal/ops` (rules, measures, sign-offs, brief merge, improvement item,
keep.add for every kind), `internal/assistant` (Claude's `keep` commands → cards, the detectors, never twice, Not now,
attachments, the message context), `internal/qa` (`project_rules`), the acceptance step (a link, a logo, a rule, a
sign-off) and `web/scripts/keep-e2e.mjs` (the cards, the project page, screenshots desktop light/dark and phone).
