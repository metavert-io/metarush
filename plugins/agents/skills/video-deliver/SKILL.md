---
name: video-deliver
description: "Export approved cuts of the person's videos to a folder with clear names and a sidecar each (unapproved only when asked; never overwriting or uploading), or hand a cut to Resolve, Final Cut or Premiere as a timeline. Routes to `metarush deliver`, `metarush export timeline`. Works whether or not they say MetaRush (the `metarush` CLI). Use it for: \"export the approved cut to ~/Desktop/x\", \"deliver the finals\", \"copy the 30 and 60 second versions to the client folder\", \"open this cut in DaVinci Resolve\"."
---

# Deliver

**What this covers.** Export finished videos from the person's video projects to a folder on this machine — find the approved cuts they mean (for example the 30- and 60-second versions), check they have final renders in the right formats, then copy them hash-verified with clear file names and a JSON sidecar each. Refuses unapproved cuts unless they explicitly ask, never overwrites files and never uploads anywhere. Also hands a cut to another editing app as a timeline (DaVinci Resolve, Final Cut Pro, Premiere Pro) with its original footage. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

See [reference/cli.md](reference/cli.md) and [reference/safety.md](reference/safety.md).

1. **Identify the cuts.** `metarush project list --json`, then `metarush revision list <prj> --json` (number,
   title, duration, approval). Match the user's words to revisions — "the 30-second variant" means a duration
   within about ±2 s of 30 and/or a title that says so. If two could match, ask. Default to **approved** ones.
2. **Approval is a person's.** If a requested cut is a draft, say so and tell the user they can approve it in the
   dashboard. Pass `--allow-draft` only when the user explicitly asks to export drafts (offer it in plain words,
   "I can export the draft anyway", never by the flag's name), and call them drafts in your report. Rejected
   revisions are never delivered.
   **Ready to deliver?** `metarush deliver ready <prj> --version rN --json` says whether the version can go out or
   what holds it (`holds`: the quality check's problems — promises made to the client not kept among them — a check
   missing or out of date, sign-offs still waiting) and the step that clears each (`paths`). With the gate on
   (`on`), delivery refuses a held version (`E_HELD`): tell the person what holds it in plain words and offer the
   steps (check it again, fix a problem as a new version, render it). Only a person sends a held version out
   anyway, with a note, on the version's page or in their own terminal — never do it for them, and never tick a
   sign-off for someone: the person who gave the OK ticks it. With the gate off, waiting sign-offs are only a
   reminder: name them before exporting and ask whether to go ahead.
3. **Check renders.** `metarush render list --revision <rev> --json`: delivery uses `available` renders with
   `quality: final` in the presets the user wants. Missing one → offer the `video-render` workflow
   (`--quality final`); don't hand off review renders as finals (`--quality review` only if asked).
4. **Check QA when it exists.** `metarush qa show <render-id> --json` — if the verdict is `fail`, tell the user
   what failed before exporting and offer the `video-qa` workflow. No report → say the quality check hasn't been run yet.
5. **Export** to the folder the user named (confirm the path if they did not name one):
   `metarush deliver --revision <rev1> --revision <rev2> [--preset vertical-1080p] --out <dir> --json`.
   It checks everything first, refuses to overwrite existing files (`E_CONFLICT` → pick a new folder; never
   delete the user's files), copies from the verified cache and re-hashes every copy.
6. **Report** each file in plain words: its name and the folder it is in, which version (approved or draft), its
   shape, length and size, that every copy was checked against the render, and every warning (missing music rights,
   quality-check problems, drafts, narration lines still on the scratch voice). Hashes and the side files' paths only
   if they ask.
   When a sidecar's `narration.aiVoice` is true, say the video contains AI narration and offer its `notice` for
   the description. When the sidecar has `captions`, say the captions are also in .srt and .vtt files next to the
   video for places that take them (not TikTok or Reels), and whether captions are drawn into the picture too. The
   record's `receipt` says what the version cost at cost and what the project sent to outside AI services (the whole
   receipt: `metarush project receipt <prj> --json`); mention the cost when the person asks what it cost. Say
   plainly that nothing was uploaded — posting or sending the files is the user's step.
7. **Fixes after delivery.** When the person fixes words after a delivery, the captions files can follow without a
   new render: `metarush deliver captions <folder> --json` rewrites them with today's words (it says how many fixes
   it added). Captions drawn into the video only change with a new version, rendered and delivered again.

## Titles, descriptions, chapters and show notes

When the person wants a title, a description, chapters or show notes for a video ("write the YouTube description",
"add chapters", "show notes for the episode"), you write them — with their Claude account, no other AI service — and
keep them on the version:

1. **Pick the version** as in step 1 (default: the latest; say which number you used).
2. **Read what is said, with its times:** `metarush captions file <prj> --version rN --version-words --format vtt --json`
   (the `content` lists every line with its time in the video). Look at frames only when the words don't say what is
   shown (`video-footage-review`).
3. **Write the notes** to a file: Markdown with `# <title>`, `## Other titles` (two or three, one a line),
   `## Description` (a few plain sentences; no links you didn't find in the footage or that the person didn't give),
   `## Chapters` (one a line, `1:05 The garden`: times into the video, the first at `0:00`, at least three, each at
   least ten seconds long for YouTube) and `## Show notes` (what is said, people and links mentioned). Use the names as
   the spelling list writes them (`metarush transcript spelling list --project <prj> --json`), and never put words in
   someone's mouth.
4. **Keep them:** `metarush deliver notes set <prj> --version rN --from <file> --json` (MetaRush marks them as Claude's); it checks the
   chapters against the video (an error says what to fix) and says what YouTube won't take (`warnings`). Show the person
   the title and chapters in plain words. Delivery writes them next to the video as a `.notes.md` file.

## Captions in other languages

When the person wants the captions in another language ("Spanish subtitles", "translate the captions"), you
translate them — with their Claude account, no other service — and MetaRush keeps them and writes the files:

1. `metarush captions lines <prj> --to <language> [--version rN] --missing --json` lists the lines to translate
   (`n`, `source`, `state`: `missing`, or `outdated` when the transcript changed since) and the `glossary` (names and
   terms from the spelling list: keep them exactly as written).
2. Translate each line on its own, keeping its meaning and about its length (it shows for the same time), into a
   file: `{"language": "es", "lines": [{"n": 1, "source": "<the line>", "text": "<its translation>"}]}`.
3. `metarush captions translate <prj> --to <language> [--version rN] --from <file> --json` keeps them, marked as
   machine-translated (`--human` only when a person translated them). Say how many lines it kept and any `warnings`
   (a glossary name not kept). `metarush captions file <prj> --language <language> --format vtt` gives the file;
   delivery writes one per language next to the video.

## A timeline for another editing app

When the person wants to keep working on the cut in another editor ("open it in Resolve", "our editor uses
Premiere", "Final Cut"), export the version as a timeline (approval is not needed: it is not a finished video):

1. **Pick the version** as in step 1 (default: the latest; say which number you used).
2. **Pick the format by the app:** DaVinci Resolve → `otio`; Final Cut Pro → `fcpxml`; Premiere Pro → `xmeml`
   (Resolve also opens the other two). If they did not say which app, ask.
3. **Export** into the folder they named (confirm it if they did not):
   `metarush export timeline <rev> --format <fmt> --out <dir>/<name> --media <dir>/Footage --json`. `--media` puts the
   original footage next to the timeline under readable names (instant copies on the same disk), so the folder can
   go to another computer; without it the timeline points at MetaRush's own copies on this machine only. Existing
   files are never overwritten (`E_CONFLICT` → another folder or name; `--replace` only when they ask).
4. **Report** in plain words: `summary`, `open` (how to import it in that app, word for word), and each of `notes`
   (what the format cannot carry exactly, such as reframing, graphics drawn by MetaRush or speed changes), plus that
   the captions are also in an .srt file when `captions` is set. Nothing was uploaded.
