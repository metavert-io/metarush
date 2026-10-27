---
name: video-edit-revise
description: "Change an existing cut of the person's video — trim, split, move or remove shots, cut out words or ums, transitions and split edits, captions, graphics, music, the shape, misheard words — then check it and save a new version. Routes to `metarush cut` (its verbs, show, check), `metarush edit check`, `metarush transcript fix|replace`. Works whether or not they say MetaRush (the `metarush` CLI). Use it for: \"tighten the middle\", \"cut out the part where I say\", \"remove the ums\", \"move the kitchen clip to the start\", \"make a 16:9 version\", \"use a dissolve\", \"let her answer start before we see her\", \"a name in the captions is wrong\"."
---

# Edit & revise

**What this covers.** Change an existing cut of one of the person's videos with precise edits on the current version — replace, trim, split, reorder or remove shots, cut out words or the ums, end a shot on specific spoken words, tighten the pacing, change transitions, captions, graphics, music or the shape (vertical, square, 16:9), keep or drop what one person says — by editing its cut sheet (the cut as text) or with typed edits, keeping locked parts fixed, then check it, show what changes and save it as a new version. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

Read [reference/cut-sheets.md](reference/cut-sheets.md) (the friendly way to change a cut) and
[reference/editorial.md](reference/editorial.md). Typed ops are in [reference/edit-plans.md](reference/edit-plans.md).

**Conversation clips** (`conversation <name> …` in the sheet; one call's recordings lined up, see "Conversations
from online calls" in [reference/editorial.md](reference/editorial.md)): "show Sam when he answers", "both of them
here", "a card with the question" are changes of angle — `metarush conversation angle --project <prj> --at 0:31.2
--member sam --json` (or `--layout side-by-side`, `--card "…"`, `--remove`), or the clip's `Shot:` lines in the
sheet. "Take out the can-you-hear-me bit", "the frozen picture" are clean-up suggestions
(`metarush conversation cleanup "<name>" --json`, then `conversation decide <name> <c1> --accept`); "Sam's mic is
too quiet" is the Sound tab or `metarush sound levels --speaker Sam --gain 3`; "her room noise comes through" is
`metarush conversation settings --project <prj> --quieter true`. Never split a conversation into separate clips of
each recording.

**Call recordings with speaker view** (one recording whose picture flips to whoever talks, often with Zoom's names
printed in a corner): trims and name changes must land on the exact frame of a switch — read them from
`metarush footage switches <ast> --json` (`at` = first frame of the new picture), never from `footage visuals`'
one-second lines. "There's a flash of the wrong person", "it flips at the start of the clip" = `metarush cut flashes
<prj> --json` (shows every flash and the trims; the person agrees, then `--confirm <token>`). "Cover Zoom's names with
ours", "a name bar the whole way through" = `metarush cut names <prj> [--first-names] --json`: one `name.bar` graphic
for the whole video that changes at every switch. Never chain one-minute name titles: a graphic can last the whole
video or a section (`for the whole video`, `from <clip> to <clip>` in the sheet, with `Change: at <footage> <time>`
lines; see [reference/cut-sheets.md](reference/cut-sheets.md)).

**The project's rules and sign-offs.** Read `metarush project rules list <prj> --json` before changing a cut and keep
to every rule ("first names only", "captions at the top"); the quality check measures the ones it can. When the
person states a new rule, offer to keep it (see "What the project keeps" in [reference/cli.md](reference/cli.md)).
Keep to the house style of its Content Settings too (`metarush content style show --project <prj> --json`: captions,
the name bar, name titles, framing, sound at cuts, the intro and end card); new cuts start with it, and
`metarush content style apply --project <prj> --json` puts it on the latest cut (the person agrees, then `--confirm`).
Before asking for an approval, say which sign-offs are still waiting (`metarush cut signoffs list <prj> --json`: the
project's own and the house sign-offs of its settings).

**Wrong words in the captions are a transcript fix, not an edit.** "The captions say Fern Brook, it's FernBrook", "she says
make, not build", "this part is Lena, not Maya": fix the transcript (see "Fixing transcripts" in
[reference/cli.md](reference/cli.md)). Look first — `metarush transcript find "Fern Brook" --project <prj> --with FernBrook
--json` — and tell the person how many places in how many videos; then `metarush transcript replace "Fern Brook" "FernBrook"
--project <prj> --spelling --json` for a name (they confirm), `metarush transcript fix <ast> --at <t> "make" --json`
for one word, `metarush transcript speaker <ast> --from <t> --to <t> --person "Lena" --json` for who speaks. Times
never move. Saved versions keep their words: when the person is done fixing, `metarush transcript pending list <prj>
--json` says what waits and `metarush transcript pending apply <prj> --json` makes **one** new version and one review
copy. Never render after each fix, and never retype captions in the sheet. Before a video goes out, `metarush captions
check <prj> --json` lists fixed words the version doesn't have yet, unsure words and names to check.

**Small edits first.** One clear change to the latest cut (move a clip, trim or split one, remove a clip or a
stretch of the video, take out some words or the ums) is one editing verb — see "Small edits without a sheet" in
[reference/cut-sheets.md](reference/cut-sheets.md): `metarush cut move|trim|split|delete|delete-range|add|remove-words
<prj> … --json` prints the change in plain words with a confirm token; show it, and after the person agrees run the
same command with `--confirm <token>`. "Cut out the part where I say 'um, anyway'" = `metarush cut remove-words <prj>
"um, anyway" --json`; "move the kitchen clip to the start" = `metarush cut move <prj> kitchen start --json` (find the
clip's name in `metarush cut show <prj> --json` when unsure). "Remove the ums" = `--ums`: list the suggestions in
plain words and let the person choose (`--pick`). Several changes at once, or anything else: the sheet below.

1. **Read the cut as a sheet.** `metarush cut show <prj> --out cuts/<project>.cut.md --json` writes the latest
   revision; Read the file. Clip names, footage names, times, transitions, locks, graphics, sound, the mix and the
   narration (`## Voiceover`) are all there, and the quoted first and last words say who is speaking. Identify
   exactly which lines the request targets ("the second shot" = clip line `2.`). If ambiguous, ask.
2. **Gather what you need**: words and times from `metarush footage transcript <asset> --json` (footage IDs are in
   the sheet's `footage:` list), moments, and frames for visual problems (captions over a face: look at frames at
   those times before deciding where to move them). For requests about a person ("remove the interviewer's
   questions", "cut away when the interviewer speaks", "only the guest"), find their ranges with
   `metarush footage transcript <ast> --speaker "<name>" --json` (or "Speaker 2" when unnamed) and check
   `metarush footage speakers list <ast> --json`. If you don't know which voice is the interviewer, ask the user
   (`metarush todos list --asset <ast> --json` shows open "who is this?" questions); do not guess. Name tags,
   titles and speaker names follow each person's `notes` (`metarush people show "<name>" --json`).
   **Things that aren't footage** — "the homepage", "our logo", "the pricing page", "the repo", "a code
   snippet", "the architecture diagram" — are resources: look them up first with
   `metarush resources list --project <prj> --json` (web page snapshots are kind `website`, repositories `repo`;
   what linked projects and the workspace library hold counts too), never by searching the footage. A page goes on
   screen with `slide.screenshot` (`image "<name>"`; `#2` the phone picture, `#3` the whole page, which scrolls);
   code and diagrams with `slide.code` / `slide.architecture` — see [reference/resources.md](reference/resources.md).
3. **Edit only the lines that change.** Trim by changing a time, or end on spoken words
   (`… → "very quickly"`; MetaRush cuts in the silence after them). Move lines to reorder, delete a line to remove
   a clip. To drop one person's lines, trim or split clips at turn boundaries; to cut away, cover their ranges with
   other shots. To show names on captions, set `speaker names: true` under `captions:`. Never touch locked items
   (`· locked`); to lock something, add `· locked`. Unlocking needs the user's explicit request and
   `--allow-unlock`. Put the user's words in the settings: `change: {request: "…", why: "…", note: "…"}` — `note` is
   a few words naming the new version ("flicker fixes and headline cards"; without one MetaRush takes the request's
   first clause). In a typed plan it is `"note"`.
   **Transitions and sound at cuts** follow editorial.md and the project's transition style (`project show`): change
   the `↳` line above the clip the cut leads into. "Let her answer start before we see her" = `↳ cut · Maya's voice
   starts 0.5 s early` — and if her clip starts right at her first word, start it later by the same amount so her
   words, not silence, start early. "Keep his voice over her reaction" = `↳ cut · voice carries 0.4 s`. "Use a
   dissolve between the scenes" = `↳ dissolve 0.6 s`. When the check says words get cut or the footage is too short,
   write `↳ cut · sound changes in a pause` or move the in/out points. "Even out the voices" = `even out voices: on`
   under `mix:` in the settings. For one cut on its own, `metarush cut transition <prj> <clip> dissolve 0.6 --json`
   checks the change and the same command with `--confirm <token>` saves it (`--lead`, `--carry` or `--auto` for the
   sound; `metarush edit transitions` lists every transition).
4. **Check** `metarush cut check <file> --json`; fix every problem (each has a line and a hint) and re-check.
   Show the user the `changes` list (plain words) and warnings that matter; for quoted words, say where they landed.
5. **Apply** after agreement: `metarush cut apply <file> --confirm <token> --write --json`.
   - `rebased: true` means someone else changed other parts meanwhile; MetaRush kept both. Mention it.
   - `E_REVISION_CONFLICT` means you both changed the same clip: `metarush cut diff <file> --json` shows their edit,
     explain the overlap, re-read the latest and redo the change there.
6. To compare with an earlier version: `metarush revision compare <old> <new> --project <prj> --format md`. To
   "undo", write the earlier values back into the sheet (history is immutable; nothing is deleted).
7. **Narration follows the cut.** Lines placed at a shot or a moment move with it; after trims, reorders or
   removals, run `metarush voiceover fit --revision <new> --json` and mention lines that no longer fit or lost
   their shot ("line 3 was tied to the garden shot you removed"). "Keep the voiceover locked" means `locked: true` under
   `voiceover:` in the settings. Script, takes and the mix in depth: `video-voiceover`; a request only about sound
   levels ("the music drowns out the narration", "I can't hear her") goes there.

Typed ops (`metarush edit check --plan <file>`) remain fine for mechanical changes such as QA fix suggestions.
To keep a cut as files in a folder (for git), use `metarush project checkout <prj> <folder>` (see the cut sheet
reference), not `cut show --out`: the folder then tracks its version and can be pushed and pulled.
Say which version it is ("version 5", with its note), what changed (the `changes` list, in plain words) and give
the dashboard link. Offer a review render.

**Names of cuts and versions**: a cut is called by its descriptive name ("Main open day video",
`project show` → `cuts[].name`), never by its branch or the words of its first version's title ("fernbrook main v2").
"Call this cut …" = `metarush cut rename <prj> "<name>"` (`--suggested` takes MetaRush's name; no new version, approvals
stay). "Rename version 4 …" = `metarush revision note <version> "<note>"` (only the note changes).

**Fixed copies in a cut**: when a clip in the cut got a fixed copy (steadier picture, cleaned-up voices,
trimmed music) the saved versions keep playing what they played. "Use the steadier picture" = `metarush cut use-fixed
<prj> <clip> --json`, then with `--confirm <token>` (a new version; `--original` goes back). The sheet shows it under
`fixed:` (the clip's name and its copy); don't write that section by hand.
Music without recorded rights shows up as a question for the user (`todos list`); ask rather than assume it is
licensed.
See [reference/safety.md](reference/safety.md).
