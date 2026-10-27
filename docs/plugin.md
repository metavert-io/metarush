# MetaRush in Claude Code's plugins, Codex and other AI apps

MetaRush ships its video skills three ways, all from the same files in `skills/`:

| For | What | Where it comes from |
|---|---|---|
| Claude Code, installed by MetaRush | the skills in `~/.claude/skills` (or a project's `.claude/skills`) | `metarush setup`, `metarush skills install` |
| Claude Code, as a plugin | the plugin `metarush`: the skills, a read-only MetaRush connection (MCP) and an eval suite | `plugins/claude-code`, listed in `.claude-plugin/marketplace.json` |
| Codex and other apps that read Agent Skills | the skills in the Agent Skills format, and MetaRush's part of a project's AGENTS.md | `metarush skills install --agents`, or `plugins/agents` |

**Every one of them needs the `metarush` program.** The skills and the plugin are instructions: they run `metarush …`
commands, and the plugin's connection starts `metarush mcp serve`. Install MetaRush and run `metarush setup` first, and
keep `metarush` on your PATH (setup says so when it can't find it).

## The Claude Code plugin

```bash
claude plugin marketplace add metavert-io/metarush
claude plugin install metarush@metarush
```

From a checkout, `claude plugin marketplace add ./` (the repository is the marketplace) or, for one session,
`claude --plugin-dir plugins/claude-code`.

- **One copy of the skills.** The plugin's skills are the ones `metarush setup` puts in `~/.claude/skills`. Use one or
  the other: answer No to setup's skills question (`metarush setup --skills=false`), or remove the folders setup
  wrote there (`video-*`, `metarush-improve`). Two copies cost Claude Code's skill listing twice.
- **The connection is read-only.** Its server, `metarush`, is MCP's core: `find`, `describe`, `run` for any
  reading step and the everyday tools, about ten in all. It changes nothing, so allowing all of its tools
  (`mcp__plugin_metarush_metarush__*`) is safe. Changes go through the skills' commands, and Claude Code asks you
  before each one; approving, delivering, spending and faces or voices stay a person's. A connection that may propose
  drafts is one you add yourself (`claude mcp add metarush-drafts -- metarush mcp serve --propose --agent claude-code`;
  [the review desk](review-desk.md)).
- **The version** is MetaRush's (`VERSION`); `scripts/bump-version.sh` writes it into the plugin with each release, so
  people who installed the plugin get the skills of each release.

## Codex and other apps

Codex reads skills from `.agents/skills` in a project (and the folders above it) and from `~/.agents/skills`; other
apps that read Agent Skills do the same, and most read AGENTS.md.

```bash
metarush skills install --agents --user                 # ~/.agents/skills, for every project
metarush skills install --agents --project ~/my-video   # its .agents/skills and MetaRush's block in its AGENTS.md
```

The skills there are the same files with a name and a description only: Claude Code's `when_to_use` is folded into the
description and its `allowed-tools` (Claude Code's permission rules) is left out. The AGENTS.md block says how
MetaRush works with an app (checks first, evidence, only people approve) and points to the review desk.
Connecting an app over MCP and holding it to proposing — `metarush mcp serve --propose --agent codex`,
`METARUSH_PROPOSE=1` for its commands — is [the review desk](review-desk.md)'s, and every tool the connection offers is
in [MetaRush as an MCP server](mcp.md).

## How it is made and checked

- `metarush skills package-plugin` writes `plugins/claude-code`, `plugins/agents` and `.claude-plugin/marketplace.json`
  from the skills the binary carries (run it from the checkout, or `--repo <dir>`); `--check` writes nothing and fails
  when the committed copy is out of date. Never edit those folders by hand.
- `go test ./internal/cli -run 'TestPlugin|TestAgents'`: the committed copy is exactly what the generator writes
  (`METARUSH_UPDATE_PLUGIN=1` rewrites it), every folder in `skills/` is in the build, the manifests agree, the MCP
  arguments are this binary's read-only core, and the skills for other apps are the plugin's in that format.
- `metarush skills test` adds three rows: the plugin's connection, the skills for other apps, and the eval cases. It
  also fails a skill whose frontmatter isn't valid YAML: Claude Code drops such frontmatter whole.
- `claude plugin validate --strict plugins/claude-code` and `claude plugin validate --strict .` check the plugin and
  the marketplace as Claude Code reads them (no network, no model).

## The eval suite

`plugins/claude-code/evals/` is a `claude plugin eval` suite of the everyday requests `metarush skills test --live
--natural` uses (generated from the same cases): each says which skill should take the request, which command should
run, and which must never run. Every run is a real Claude Code session on your account, each case runs with and
without the plugin, and nothing in CI runs it. To run it on purpose:

```bash
claude plugin eval plugins/claude-code --allow-tools "Bash(metarush *)" --runs 1 --max-cost-usd 5
```

`--ablation none` halves it (no run without the plugin); results go to `plugins/claude-code/evals/results/`, which git
ignores.
