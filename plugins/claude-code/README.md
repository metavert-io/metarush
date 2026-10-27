# MetaRush for Claude Code

Edit video by talking to Claude Code: your footage, your style, your sign-off. This plugin teaches Claude Code
MetaRush's video skills and connects it to MetaRush to look things up.

**It needs the `metarush` program.** The plugin holds instructions, not MetaRush itself: every skill runs
`metarush …` commands, and the connection starts `metarush mcp serve`. Install MetaRush first, set it up, and keep
`metarush` on your PATH:

```bash
brew install --cask metavert-io/metarush/metarush   # or a release archive: put metarush on your PATH
metarush setup --skills=false                        # the plugin brings the skills
metarush doctor                                      # everything ready?
```

Then add the plugin:

```bash
claude plugin marketplace add metavert-io/metarush
claude plugin install metarush@metarush
```

Already ran `metarush setup` with its skills? It put the same skills in `~/.claude/skills`. Keep one copy: remove
those folders (video-project, video-ingest, video-footage-review, video-story-editor, video-edit-revise, video-render, video-qa, video-deliver, video-motion-design, video-workspace-admin, video-voiceover, video-generate, video-remotion, video-recipe, metarush-improve) or don't install the plugin.

## What's inside

**Skills** (Claude Code picks them by what you ask; you never have to say MetaRush):

| Skill | What it covers |
|---|---|
| `video-project` | Start, find or open your video projects |
| `video-ingest` | Bring footage into your video projects |
| `video-footage-review` | Review the footage in your video projects with evidence |
| `video-story-editor` | Build a new cut from your footage with editorial judgment (story, hooks, pacing, who speaks, shape, captions, light graphics, music) as a cut sheet, then check it and save it as a new version |
| `video-edit-revise` | Change an existing cut of your video |
| `video-render` | Render a cut of your video to a playable MP4 |
| `video-qa` | Check a finished render of your video before anyone sees it |
| `video-deliver` | Export approved cuts of your videos to a folder with clear names and a sidecar each (unapproved only when asked; never overwriting or uploading), or hand a cut to Resolve, Final Cut or Premiere as a timeline |
| `video-motion-design` | Add or refine on-screen graphics in a cut |
| `video-workspace-admin` | Check and maintain your video setup without showing secrets |
| `video-voiceover` | Narration and the sound of your video |
| `video-generate` | Make new shots for your video with generative video |
| `video-remotion` | Work with Remotion projects (videos made from code) in your video projects |
| `video-recipe` | Make a video that worked again from its recipe (what changes, what stays, its rules) |
| `metarush-improve` | Find what MetaRush should do better from its activity log or an exported log |

**A read-only connection** (MCP server `metarush`: `metarush mcp serve`): about ten tools — find, describe and run
for any reading step, plus the everyday ones (what's happening now, projects, search, a cut sheet, its check, a
render's quality check). It changes nothing, so you can allow all of its tools. Changes go through the skills'
commands, and Claude Code asks you before each one; only people approve versions, deliver, spend money or use
someone's face or voice. To let another app propose changes as drafts for you to review, see MetaRush's
docs/review-desk.md.

## For maintainers

This folder is generated from `skills/` by `metarush skills package-plugin` (with `plugins/agents` and
`.claude-plugin/marketplace.json`); don't edit it by hand. `go test ./internal/cli -run TestPlugin` fails when it is out of
date, and `claude plugin validate --strict plugins/claude-code` checks it as Claude Code reads it.

`evals/` is a `claude plugin eval` suite with the same everyday requests as `metarush skills test --live --natural`.
Every run is a real Claude Code session on your account, so run it on purpose, never in CI:

```bash
claude plugin eval plugins/claude-code --allow-tools "Bash(metarush *)" --runs 1 --max-cost-usd 5
```
