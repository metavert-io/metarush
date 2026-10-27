---
name: video-qa
description: Check a finished render of the person's video before anyone sees it — length, size, black or frozen frames, loudness, silence, captions and graphics in the way — look at frames, tell problems from taste and propose fixes as new versions. Routes to `metarush qa show|run`, `metarush captions check`. Works whether or not they say MetaRush (the `metarush` CLI).
when_to_use: '"what''s wrong with the latest render", "check it before I post", "any black frames", "is it loud enough", "check the captions"'
allowed-tools: Bash(metarush project show *) Bash(metarush revision list *) Bash(metarush revision show *) Bash(metarush render list *) Bash(metarush render status *) Bash(metarush qa show *) Bash(metarush job show *) Bash(metarush edit check *) Bash(metarush footage frames *) Bash(metarush footage evidence *) Bash(metarush captions check *) Bash(metarush transcript find *) Bash(metarush transcript pending list *) Read Write Bash(metarush ops find *) Bash(metarush ops describe *)
---

# Render QA

**What this covers.** Check a finished render of the person's video for real problems before anyone watches or posts it — decode the actual file and measure length, size and frame rate against the plan, black and frozen frames (planned fades, dips and end cards are told apart from defects), loudness against platform norms, silence where there should be speech, and caption or graphics collisions — then look at frames at every cut and finding, separate observed problems from judgment, and propose fixes as new versions in a bounded repair loop. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

See [reference/cli.md](reference/cli.md), [reference/edit-plans.md](reference/edit-plans.md) and [reference/safety.md](reference/safety.md).

A cut of a conversation (one call's recordings) gets three more checks: everyone heard about as loud
(`conversation_quiet`, `conversation_inaudible`; the fix turns that person up), the picture (one person alone on
screen longer than the style allows, someone talking at length unseen, screen time far from talking time) and the
call's own glitches (a frozen webcam on screen, a sound dropout, recordings lined up unsurely). Say them in plain
words ("Sam is hard to hear next to Maya"); the measured loudness of each person is in the report.

1. **Find the render.** `metarush project show <prj> --json` (head revision, latest renders) or
   `metarush render list --revision <rev> --json`. Use an `available` render; if there is none, offer the
   `video-render` workflow first. Note the revision number, its approval state, preset and quality.
2. **Run QA.** `metarush qa run <render-id> --wait --json` (decodes locally; runs in this terminal when no worker
   is online). If the render already has a report you get it back; `--force` only after something changed.
   Later reads: `metarush qa show <render-or-report-id> --json`.
3. **Look before you judge.** Read `report.contactSheet.path` and `report.momentsSheet.path`, then the frames
   each finding lists (`finding.frames` → `report.frames[].path`). Frames are labeled with timeline time; the
   moments sheet outlines the plan safe area (amber) and the caption band (cyan).
4. **Report in two parts.**
   - **Observed defects** — every `error`/`warning`: what was found, around when, whether it was measured on the
     file (`basis` observed) or estimated from the plan (heuristic), and what you saw in its frame. Say it in plain
     words, never the finding's `kind`.
     Loudness targets are conventions (≈ −14 LUFS for social/YouTube, −16 generic, ≤ −1 dBTP), not rules; tell the
     person in plain words ("about right for phones", "a bit quiet") and give the numbers only if they ask.
   - **Your judgment**, clearly labeled as opinion (pacing, framing, legibility) — only
     about frames you actually Read. Then the report's `limitations`.
   What's on screen (check `on_screen`): MetaRush places the footage's faces and its own text (the on-screen index)
   through each clip's crop and finds a face the crop cuts off, captions or a graphic over a face, and captions over
   text that is in the footage itself; where a Mac looked at the rendered frames the finding is `observed`. Look at
   the finding's frame, say what you see ("the crop cuts Ada's face off at the right edge around 0:03"), and offer
   its fix (often letting the crop follow movement, or moving the captions to the top) — the person chooses.
   Caption words (check `caption_words`): a word fixed in the transcript since the version was made ("the captions
   say “Maple Grove” where the transcript now says “Maplegrove”") is fixed by **one new version** with every waiting
   fix (`metarush transcript pending apply <prj> --json`, the person confirms), never by editing the captions; words
   the computer wasn't sure it heard and names on no spelling list are questions for the person ("“Elmwood” appears 4
   times — is it spelled right?"): a right name goes on the spelling list (`metarush transcript spelling add`), a
   wrong one is fixed in the transcript. `metarush captions check <prj> --json` gives the same list for any version.
   Call recordings (check `call_footage`): a split-second of the wrong person at a clip's start or end ("Clip 3 opens
   with a split-second of Maya before Lena appears") is fixed for every clip at once by trimming at the exact
   switch (`metarush cut flashes <prj> --json`, the person confirms); names the call app printed that still show, or
   a name bar naming the wrong person, are fixed by `metarush cut names <prj> --json`.
   The project's rules (check `project_rules`): a rule the version breaks ("captions at the top", "first names only",
   a length, the shape) is said with the rule in the person's words and its fix; rules MetaRush can't measure are
   listed for the person to look at — look at the frames yourself and say what you see.
   Say "no defects found" only when the verdict is `pass`, and still list what was not checked.
   Narration checks (cuts with a voiceover): narration over on-camera speech, music or camera sound too loud
   under the voice, clipping, recording vs. script mismatch, lines starting before their spot. The speech check
   listens to the render word by word and flags repeated, missing or unexpected words with a timecode ("around
   0:27 a word repeats") — say what you heard there, in plain words.
   Cuts and transitions: a split the footage was too short for ("Sam's voice starts 0.2 s early instead of 0.4 s"),
   a sound change that cuts or loses words, two people talking at once over a dissolve, a shot that flashes by in a
   frame or two, a moment of stray sound, and a voice that suddenly gets much louder or quieter at a cut (measured
   on the render). Their fixes are `setTransition` (often sound `auto`), `setMix` (`levelSpeech` on) or a clip's
   volume; explain them as what the viewer will hear ("Sam's voice will start in the pause before his answer").
5. **Repair loop — at most 2 rounds, and ask before each apply.** For fixable findings, write an ops plan
   against the project's current head (`baseRevisionId`), using each `fix.ops` as a hint, not a command:
   check it fits the user's intent, never touch locked items, then `metarush edit check --plan <file> --json`,
   show the diff and ask. After `edit apply` (a new revision; nothing is overwritten), render a review copy
   (`video-render` workflow) and run QA on the new render. Stop after the second round and report what remains.
   Findings with no ops (e.g. a probable render defect not explained by the plan or the source) are reported,
   not edited around, unless the user asks.

Never approve revisions. If the checked version was approved, say the fix creates a new draft version that a person
must approve again — the approved one stays as it is. A quality check still waiting to run is not a finished report.
