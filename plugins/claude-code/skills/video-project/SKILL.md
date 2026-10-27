---
name: video-project
description: Start, find or open the person's video projects — create one with a brief, list them, sum up a project's footage, cuts and renders, or keep a cut as files for git. Routes to `metarush project list|show|create|checkout`. Works whether or not they say MetaRush (the `metarush` CLI).
when_to_use: '"what video projects do I have", "start a new video for…", "what''s in this project", "open the Test project", "keep the cut in git"'
allowed-tools: Bash(metarush project list *) Bash(metarush project show *) Bash(metarush project status *) Bash(metarush workspace show *) Bash(metarush asset list *) Bash(metarush ops find *) Bash(metarush ops describe *)
---

# Video project

**What this covers.** Start, find or open one of the person's video projects — create a project with a short brief (goal, audience, tone, platforms, target length), list existing projects, summarize a project's footage, cuts, renders and dashboard link, or keep a project's cut as files in a folder for git (checkout, status, pull, push). For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

MetaRush is installed as the `metarush` CLI. Read [reference/cli.md](reference/cli.md) for the command table.

1. **Identify the project.** Run `metarush project list --json`. Match the user's words to a project name; if two
   could match, ask which. If none exists and the user is starting something new, go to step 2.
2. **Create (only when asked or clearly starting new work).** Turn the request into a brief:
   `metarush project create "<name>" --goal "…" --audience "…" --tone "…" --platform <shorts|tiktok|reels|youtube> --target-seconds N --constraint "…" --json`.
   Brand or style rules the user gives go in `--constraint`, and each also as a project rule
   (`metarush project rules add <id> "<rule>" --json`: Claude follows them and the quality check measures what it
   can). Where the video goes ("by email", "social shorts") goes in the brief with
   `metarush project update <id> --deliverable "<place>" --json` (one per place; shorts are vertical). Don't invent
   rules they didn't state.
3. **Summarize.** `metarush project show <id> --json` — report footage count and readiness (preview copies and
   transcripts), the latest version (number, length, approved or not), latest renders, anything still processing,
   and the dashboard link.
4. **Hand off.** If there is no footage, suggest the `video-ingest` workflow; if footage is ready, the
   `video-footage-review` or `video-story-editor` workflows.
5. **Keep a cut in files or git** (only when asked): `metarush project checkout <id> <folder> --json` writes the cut
   sheet `cut.md`, a footage reference `footage.md` and `.metarush/lock.json` (no media, no secrets).
   `metarush project status <folder> --json` shows local edits and newer revisions; `project push` saves `cut.md`
   as a new revision (check first with `--dry-run`, then `--confirm <token>`); `project pull` updates the folder.
   See [reference/cut-sheets.md](reference/cut-sheets.md).

Pass IDs to commands exactly as printed, but name projects and cuts by their titles when you talk to the person. If a
command fails with `E_NOT_CONFIGURED`, run `metarush doctor` and relay the fix in plain words.
See [reference/safety.md](reference/safety.md).
