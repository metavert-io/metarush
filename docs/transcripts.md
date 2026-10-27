# Fixing transcripts

This page covers fixes, the kept machine transcript and undo; saved versions keeping their words; spelling lists and
transcriber hints; fixes waiting for one new version, the caption check and captions files; and fixes carried over
when a clip is transcribed again. Code: `internal/transcripts` (the engine), `internal/ops/transcripts*.go` (ops), `internal/cli/transcript.go`,
`internal/assistant/transcript_actions.go` (Claude's cards), `web/src/components/transcripts/` (the dashboard).

The automatic captions wrote "Fern-Brook" for "FernBrook". People (in the dashboard) and Claude (in its panel, as cards a
person confirms) fix what was heard; the fix reaches the captions, the Script view, cut sheet quotes, search and
what Claude reads, without a render after every fix.

## What a person sees
- **Retype words**: double-click a word in the cut's transcript (or in a clip's own transcript), or select a
  stretch and press **Fix the words**; type what is really said; Enter saves. Only the words that differ change and
  each keeps the time it was said in. Fixed words are underlined. A toast says what changed, with **Undo**.
- **Who says this?**: select a stretch, press **Who says this?** and pick a person ("this part is Lena, not
  Maya"), someone new, or "Not sure". The same picker as everywhere (docs/speakers.md); it is a fix like the
  others, so it shows in the history and can be undone.
- **Click a caption** in a version's review player: the caption's words open in an editor ("This version says
  “nobody was watching?”"); the fix goes to the transcript, the version keeps its words.
- **Say it differently…** (right-click words): a cut says new words in the speaker's own voice copy, with
  their consent — not a fix: the transcript keeps what was said and marks the words a cut says differently
  ([spoken-words.md](spoken-words.md)).
- **Find and replace** (the cut's transcript, a clip's transcript): one clip, the project, or everything. Every
  place is listed with the words around it, the clip, the time, who says it and a play button, *before* anything
  changes; untick the places to leave alone ("Only the 2 ticked places will change"). Whole words by default
  (punctuation aside: "Fern Brook" finds "Fern. Brook,"), **Match case** and part-of-a-word on request. Joining ("Fern Brook" →
  "FernBrook") and splitting ("Maplegrove" → "Maple Grove") keep the timing. "Also put “FernBrook” on the spelling list".
- **Spelling list**: part of the project's Content Settings (the project's Settings tab → Spelling, also the
  studio header's Spelling) — its own, another project's or the default's, which footage in no project uses:
  "Always write FernBrook", "Also heard as Fernbrooke, Fern-Brooke", "Also fix the transcripts that already write it another way".
  Spellings that differ only in spaces, dashes or capitals are found by themselves. "You keep fixing these the same
  way" offers words fixed again and again; the same question appears in To-dos ("You fixed “Fern Brook” 3 times — add
  “FernBrook” to the spelling list?").
- **History**: a clip's fixes, newest first, with who and when, **Undo** per fix (or a whole find and replace), why
  a fix can't be undone any more (changed again since), and **Show the computer's own transcript**.
- **The fixes wait for a new version**: the project header and the latest version's page say "5 text fixes since
  version 1 of “Garden story”. Make a new version and a review copy to use them." **See them** lists each place
  (playable); **Make the new version** makes one version and one review copy. Nothing renders by itself.
- **Check the captions** (a version's page): words fixed since the version ("The captions say “garage,” where the
  transcript now says “workshop,”… Make a new version to use the fixed words"), words the computer wasn't sure it
  heard, and names not on a spelling list ("“Elmwood” appears 4 times in the captions. Is it spelled right?", with
  **It's right: add “Elmwood” to the spelling list**). The same check is part of the quality check of a render.
- **Captions file (.srt / .vtt)**: a version's captions as a file, with today's words: text fixes reach it without
  a new render. Delivery writes one next to the video where the place takes a captions file (not TikTok or Reels);
  "Update the captions files" rewrites them after later fixes. Captions drawn into the video need a new version.
- **Captions in other languages** (a version's page): **Ask Claude to translate…** picks a language and opens
  the Claude Code panel with the request; Claude translates each line with your Claude account, keeping the names on
  the spelling list as written, and its card ("Keep the Spanish captions for version 3 of “Garden story”: 84 lines,
  machine-translated by Claude") shows examples and any name it didn't keep. Each language lists how many lines are
  translated and how many changed since; **.srt** and **.vtt** download it (the WebVTT file starts with a note
  saying it was machine-translated). Delivery writes `<name>.es.srt` and `<name>.es.vtt` next to the video.

## How it works
### The words and the fixes
- `transcript_segments` always hold the **current** words. Every reader (new versions' captions, the Script view,
  search, cut sheets, `footage transcript`, Claude) follows fixes without knowing about them.
- A **fix** (`transcript_fixes`, `tfx_…`) is one change at one place: kind (`words`, `speaker`, `transcribed`),
  source (by hand, find and replace, the spelling list, carried over, the machine), the words before and after (with
  their times), the stretch, the batch (`tfb_…`: one find and replace, one retype), who (`Actor`), when, and the
  clip's transcript revision (`Asset.textFixes.rev`). Undo marks it `undone` (with the revision and who); a fix the
  new transcript didn't keep is `superseded`.
- The **machine's transcript** is kept once per transcription (`transcript_originals`, gzipped BSON per base) before
  its first change (`footage transcript --original`, "Show the computer's own transcript").
- **Retype** (`transcripts.Retype`): the old and new words are compared word by word (longest common subsequence);
  only the stretches that differ are rewritten. **Spread**: as many new words as old keep their times one for one;
  otherwise the stretch is shared out by letters, the first new word starting exactly where the old ones started and
  the last ending exactly where they ended (cut times and quoted words around stay put); an inserted word borrows
  from its neighbor. New words are marked written by a person (`Word.x`) and carry no confidence.
- A **moment** (`--at`, a clicked word) is the word said then, or in a short pause (≤ 0.25 s) the nearest one; a
  **stretch** is the words whose middle is inside it.
- **Concurrency**: a clip is changed under a lease on its asset (`textFixes.lock`, 30 s, waiting up to 15 s); each
  changed line is written only if its text is still what was read ("This clip's transcript changed while the fix
  was being saved"); the transcriber and speaker detection hold the same lease when they write words. Once started,
  a change finishes even if the page is closed or the connection drops (`keepGoing`): the words and the record of
  the fix are written together, and a new version gets its review copy. A fix of who says a stretch, and its undo,
  is recorded the moment its words move (with the names on the lines); the voices learn from it after, still under
  the lease (`Clip.Then`: `speakers.AssignWords` / `RestoreWords` return that learning). Fingerprinting the voices
  again and refreshing the library (profiles, automatic labels in other clips, the questions about voices and
  faces) take seconds, and the history, the fixes waiting for a new version and a version saved meanwhile never lag
  behind the words. If the learning fails, the change stays recorded and the error says so ("Use Rebuild voice on
  the People page").
- **Undo** takes back one fix or a batch, newest first; a fix whose words were changed again since stays ("Undo
  the later change first"). Undoing who says a stretch gives the words back to the voices that had them, brings
  back voices the fix left without words, takes apart voices that were combined into the named one automatically
  after the fix (the same voice recognized elsewhere in the clip), and learns the voices again.

### Saved versions keep their words
Saving a version pins each clip whose words it uses (`Revision.transcripts`: the clip, its transcript revision and
the SHA-256 of a canonical snapshot of the words the engine reads, kept content-addressed in
`transcript_snapshots`). Everything that resolves a version (preview plans, renders, QA, exports, nested cuts,
deliveries) reads the pinned words (`transcripts.For`); a version saved before pins reads the machine's original
when the clip was fixed since, else today's words. Cut sheets, search and new versions read today's words. The edit
spec is unchanged (cut sheets keep their spec hash); old versions' plans are byte for byte the same (golden tests).

### Spelling lists
- `spelling_entries` (`spl_…`): a word or name (≤ 6 words, ≤ 80 characters), the other ways it is heard, a note,
  who added it; on the list of the project whose Content Settings hold it. Before migration 36 a list was a
  project's or the whole workspace's; the workspace's became the default's settings' list.
- **Matching** compares letters and digits only, so spaces, dashes and capitals are found by themselves ("Fern Brook",
  "Fern-Brook", "fernbrook" → "FernBrook"); possessives keep their ending ("Fern Brook's" → "FernBrook's"). A difference in capitals alone
  is corrected only for names of several words or with a capital inside ("Maple grove" → "Maple Grove", "Fernbrook" →
  "FernBrook"), never for a plain word ("may" stays "may" with "May" on the list).
- **New transcripts**: after a transcript is stored, the lists of the Content Settings of the clip's projects (all
  of them; the default's for footage in no project) correct it (fixes by the spelling list, undoable). Before that, **Parakeet is told to expect the words**: the CLI decoder
  runs modified beam search with the words as hotwords (`--hotwords-score 2.0`, a BPE vocabulary written from the
  model's own tokens, at most 200 words); without hints the resident model server keeps greedy decoding.
  whisper.cpp gets no hints (below).
- **Suggestions**: a word fixed the same way in 3 separate fixes, or in 5 places, that no list has yet, is offered
  (toast, CLI, the Spelling list, a To-do of kind `spelling` whose "add" answer puts it on the project's list).

Measured on a development Mac (2026-10-20) with five names a recognizer gets wrong: three short clips per name, said
by three macOS voices, and a long clip saying each name 6 times:

| Parakeet v2 | name 1 | name 2 | name 3 | two place names | words written over noise, tone or silence (8 clips) | decoding time, 20 clips |
|---|---|---|---|---|---|---|
| greedy (no list) | 0/3, 0/6 | 2/3, 0/6 | 1/3, 0/6 | all | none | 6.1 s |
| beam search, hotwords score 2.0 | 3/3, 6/6 | 2/3, 6/6 | 3/3, 6/6 | all | none | 6.9 s |
| beam search, hotwords score 4.0 | 3/3, 6/6 | 3/3, 6/6 | 3/3, 6/6 | one miss | 6 of 8 ("Elmwood Elmwood…") | — |

A sentence with words close to the names stayed as it was heard with the
list at 2.0. whisper.cpp runs with `-mc 0`, and then its initial prompt changes nothing (0/3 for name 1, with
or without); with context on, the prompt gets the names but whisper wrote a sentence over pink noise ("I'm going to
go ahead and get to the next slide."), so it gets no hints and the list corrects its transcripts after.

### Fixes wait for a new version
`transcript.pending` lists the fixes a version doesn't have: made or undone after its pins, inside the stretch of a
clip the cut uses (± 1 s), anywhere in a conversation's recordings or a narration take, who says a stretch, and
clips transcribed again. `transcript.pending.apply` saves one version from the latest with the same cut (pinned to
today's words) and asks for one review copy (`--no-render` skips it). Claude's request for it is a card ("Make
version 3 of “Garden story” with 12 text fixes, and a review copy").

The **caption check** (`captions.check`; in the quality check as `caption_words`, notes `caption_fixed`
(warning), `caption_unsure`, `caption_name`) reads the version's resolved captions: a caption word that is not the
transcript's word any more is "fixed since" (caption words over the same changed words are one note: "Maplegrove"
→ "Maple Grove"); a word the transcriber gave less than 0.5 is "unsure"; a capitalized word that doesn't
start a sentence and is neither on a spelling list nor a person's name is a name to check. **Captions files**
(`captions.file`): SubRip or WebVTT from the version's captions (or, when the cut shows none, captions of what is
said with the usual settings), with today's words by default or the version's own (`--version-words`). Delivery
writes `<name>.srt` and `<name>.vtt` next to the video when the place takes a file, and the sidecar's `captions`
says which words they have, how many fixes they include and whether captions are also drawn into the video;
`deliver captions` rewrites them after later fixes.

### Captions in other languages
MetaRush makes no AI calls: Claude Code translates, on the person's own Claude plan. `captions.lines` lists a
version's caption lines (numbered as its captions file has them, today's words unless `--version-words`) with each
one's state in a language — `translated`, `followed` (fixed since, the fix carried), `outdated` (fixed since, to
translate again) or `missing` — the project's spelling list as `glossary`, and `howTo`. `captions.translate` keeps a
translation line by line in `caption_translations` (migration 45; one document per project, language and line's
words, with where the words are said: `<footage>@<from>-<to>`), matched to the version's lines by their words or
their number (exactly first, then ignoring capitals), marked `machine` unless a person says they translated it
(`--human`); every line is checked before any is kept, and a name from the glossary missing from a translated line
is a warning. Languages are BCP 47 tags of up to 35 characters, written as the standard writes them whatever way they are typed (`ES`,
`pt_br`, `zh-hant-tw` and `SL-ROZAJ` become `es`, `pt-BR`, `zh-Hant-TW` and `sl-rozaj`), or a language's name. A file in a language is the version's lines looked up by their words (any
version showing the same line has it), else by where they are said: when the transcript was fixed since, the words
that changed are replaced in the translation only when that is certain — a name (a capital inside a sentence, not
the first word of a line or a sentence), a number or a spelling-list word,
found there exactly once as whole words with the same capitals — otherwise (a common word, punctuation only) the line
keeps its old translation and counts as `outdated`; lines never translated stay in the original language and
are said. Each line in Arabic, Hebrew, Persian or Urdu starts with a right-to-left mark (U+200F), so players run it
the right way even when it starts with a number or a name in Latin letters. Machine-translated WebVTT files start
with a `NOTE`; the delivery record's `captions.translations` lists
each language's files, `machineTranslated`, `notice`, `untranslated` and `outdated`. `deliver captions` rewrites
them with today's words and adds languages translated since the delivery, writing over only the files the
delivery's record lists: a captions file someone else put there (a person's own `video.de.srt`) is left as it is,
and said (`leftAlone`). `captions.translation.remove` takes a
language off a project (delivered files stay).

### Transcribed again
Before a clip is transcribed again its machine transcript is kept and its fixes read; after the new transcript is
stored each fix is looked for where it was, by time and by the words the machine had there (the same words
ignoring spaces, dashes and capitals, or close by `textalign.Similar`): written again ("carried"), already right
(the new words are the person's), or missed (the new transcript says something else there; reported with what it
heard). The clip's `textFixes.carry` keeps the report, each person whose fixes didn't carry gets a notification
("2 of your transcript fixes didn't carry over to “Interview”"), the old fixes are marked superseded, the
spelling lists apply to the new words, and the pending list says the clip was transcribed again. Who says what
carries over with the speakers.

## Commands (all with `--json`)
```
metarush transcript show <clip> [--words] [--original]
metarush transcript find "<words>" (--clip <clip> | --project <p> | --everywhere) [--with "<new words>"] [--match-case] [--part-of-words]
metarush transcript replace "<words>" "<new words>" (--clip | --project | --everywhere) [--only <place>…] [--spelling] [--dry-run]
metarush transcript fix <clip> (--at <time> | --from <time> --to <time>) "<what is said>" [--expect "<what it says now>"]
metarush transcript speaker <clip> --from <time> --to <time> (--person "<name>" | --new-person "<name>" | --unsure)
metarush transcript history <clip>
metarush transcript undo (<fix> | --batch <batch> | --last <clip>)
metarush transcript spelling list|add|remove|apply|suggestions [--project <p>]   (no --project: the default's list)
metarush transcript spelling apply --settings-of <p> [--dry-run]                  (every clip using that list)
metarush transcript pending list <project> [--version rN]
metarush transcript pending apply <project> [--no-render]
metarush captions check <project> [--version rN]
metarush captions file <project> [--version rN] [--format srt|vtt] [--version-words] [--out <file>]
metarush captions at <version> --at <time>
metarush captions languages <project> [--version rN]
metarush captions lines <project> --to <language> [--version rN] [--missing] [--version-words]
metarush captions translate <project> --to <language> --from <translation.json> [--version rN] [--human]
metarush captions file <project> --language <language> [--format srt|vtt]
metarush captions remove-language <project> --to <language>
metarush deliver captions <delivered video | its .json | the folder>
```
In the Claude Code panel `transcript show|find|history`, `transcript spelling list|suggestions`, `transcript
pending list` and `captions check|file|at` are reads; `transcript replace|fix|speaker|undo`, `transcript spelling
add|remove|apply` and `transcript pending apply` become cards built and previewed by the server (the places and
videos counted from a real find, the waiting fixes listed); saving a captions file and `deliver captions` are not
started from the panel. `captions languages|lines` are reads; `captions translate` (from a file in the proposals
folder) and `captions remove-language` are cards, and Auto keeps a translation by itself unless it would write over
lines a person translated (those ask).

## Tests
- `go test ./internal/transcripts` (words, retype, spread, find, spelling, pins, carry-over, ranges),
  `./internal/ops` (`TestTextFixesWaitForANewVersion`, `TestWhoSaysAStretchIsAFixThatCanBeUndone`,
  `TestRepeatedFixesSuggestASpellingEntry`, `TestDeliveredCaptionsFilesFollowFixes`,
  `TestUndoingWhoSaysAStretchWaitsForNothing` and `TestUndoOfWhoSaysAStretchStaysRecordedWhenTheVoicesCantBeLearned`:
  who says a stretch with a face on screen and the same voice in another clip, checked while the voices learn),
  `./internal/speakers` (`TestUndoingWhoSaysAStretchTakesBackWhatFollowed`), `./internal/assistant`
  (`TestTranscriptChangesBecomeCards`).
- `scripts/transcript-e2e.sh`: the editing UI in a real browser on a throwaway installation (desktop and phone,
  dark and light; a fake `claude`).
- `scripts/acceptance.sh`, step "text fixes": a spelling list on a new transcript, a retyped word and a join keeping
  their times and a cut sheet's quote, one new version and its review copy, the old version rendered unchanged (its
  quality check names the two fixed words), captions files with today's and the version's words.

## Hooks for what comes next
- **Cleaned-up voices transcribed again** go through `BeforeTranscribing`/`AfterTranscribing` like any other
  transcription: fixes carry over and what doesn't is reported.
- **Descriptive source names** only change how clips are named in hits, history and the pending list
  (`Asset.DisplayTitle`).
- **Questions to people** and **"notice what to keep" cards** can use `transcript.find` (read-only) and the to-do
  kind `spelling` as the pattern for a question answered through ops.
