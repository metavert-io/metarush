---
name: video-render
description: "Render a cut of the person's video to a playable MP4 — a quick review copy or the final, vertical, landscape or square — wait for it, check the file and say where to watch it. Routes to `metarush render submit|status`. Works whether or not they say MetaRush (the `metarush` CLI). Use it for: \"render it\", \"make a review copy\", \"I want to watch it\", \"render the final vertical version\"."
---

# Render

**What this covers.** Render a cut of the person's video to a real, playable MP4 — a fast review copy (half resolution) or the final quality, vertical 1080x1920, landscape 1920x1080 or square — wait for it, verify the file, and say where to watch it. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

See [reference/cli.md](reference/cli.md).

1. **Pick the revision.** Default to the project's head (`metarush project show <prj> --json`); if the user names a
   version ("r3", "the energetic one"), find it in `revisions`. Note its output preset and approval state.
2. **Render.** `metarush render submit --revision <rev> --quality review --wait --json` for a quick look, or
   `--quality final` for delivery. Add `--preset landscape-1080p|square-1080|vertical-1080p` only if the user asked
   for a different shape than the revision's. `--wait` renders in this terminal when no worker is online (it can
   take a few minutes; say so). Identical requests reuse the existing render.
3. **Verify, don't assume.** The command only succeeds when the MP4 exists, decodes, and matches the planned
   duration and size. If it fails, tell the person what went wrong and what to do next, in the error's own plain
   words (its message and hint); do not retry blindly.
4. **Show it.** Say which version it is, how long and how big, and give the dashboard link to watch it (the render's
   ID only if they ask). For a local file:
   `metarush render fetch <rnd> --out <dir> --json` (never overwrites). A quality check of the finished file
   (black/frozen frames, loudness, silence, layout, and listening to the speech) is queued automatically; offer the
   `video-qa` workflow to read it (`--no-qa` skips it only when the user asks).
5. **Approval** is the user's: tell them they can approve this version in the dashboard. Never approve it yourself.

A queued render is not a finished render — never report success until `state` is `available`.
See [reference/safety.md](reference/safety.md).
