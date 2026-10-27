# MetaRush's skills for Codex and other AI apps

The same video skills as MetaRush's Claude Code plugin, in the Agent Skills format that Codex reads from
`.agents/skills` (and `~/.agents/skills`), as do other apps that read Agent Skills; `AGENTS.md` is MetaRush's part of a
project's AGENTS.md.

**They need the `metarush` program.** Every skill runs `metarush …` commands: install MetaRush and run
`metarush setup` first, and keep `metarush` on your PATH.

Install them from the program (they are the same files):

```bash
metarush skills install --agents --user                  # ~/.agents/skills, for every project
metarush skills install --agents --project ~/my-video    # its .agents/skills and MetaRush's part of its AGENTS.md
```

or copy `skills/*` into one of those folders.

Connecting an app over MCP, and holding it to proposing (every change a draft a person reviews, `--agent` naming
the app): [the review desk](../../docs/review-desk.md) and [MetaRush as an MCP server](../../docs/mcp.md).

This folder is generated from `skills/` by `metarush skills package-plugin`; don't edit it by hand.
