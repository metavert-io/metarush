# MetaRush as an MCP server

`metarush mcp serve` lets Model Context Protocol clients (Claude Desktop, Claude Code and other agents)
use MetaRush. It is off unless you configure a client to start it — or install the Claude Code plugin, whose
connection is this server's read-only core ([docs/plugin.md](plugin.md)).

- **Local and stdio only.** The client starts `metarush mcp serve` as a child process and talks JSON-RPC over its
  standard input and output. There is no network port and nothing to sign in to; it runs as you, on this machine,
  with this installation's settings (`--home` picks another installation).
- **A few tools, not hundreds**. A client's model chooses better from a short list, and every conversation
  carries the list, so the server offers a small one and finds the rest:
  - **The core** (the default): `find` names the operation for a task described in plain words ("make a review
    copy"; the best eight, one line each), `describe` shows one's input with an example, `run` does one that
    only reads and, with changes allowed, `run_change` one that changes something; beside them a few everyday tools
    (`activity_summary`, `project_list`, `project_show`, `search`, `cut_show`, `cut_check`, `qa_show`, and with changes
    allowed `render_submit`). A client loads about 1,400 tokens before its first call (about 1,900 with changes),
    whatever the number of operations.
  - **Toolsets** (`--toolset review|edit|library|admin`): one kind of work as direct tools, related operations merged
    into one tool that takes a `mode` (`footage` with `mode: "transcript"`, `"moments"`, `"sheet"`…; an operation's
    own `mode` field is `<mode>Mode` there). A group's reads are one tool (`collection`) and its changes another
    (`collection_change`, only with changes allowed). Each toolset has `find`, `describe`, `run` and `run_change` for
    everything else.
  - **Every operation as a tool** (`--toolset all`): one tool per operation, dots in the names as underscores
    (`project.show` is `project_show`), as before; for tests and clients that want them all.

  | Toolset | What it is for | Tools, read-only | With `--propose` | With `--allow-changes` |
  |---|---|---|---|---|
  | core (default) | anything, through find, run and run_change | 10 | 12 | 12 |
  | review | watch and read: footage, versions, renders, quality checks, comments | 18 | 22 | 22 |
  | edit | change cuts, words, sound, narration and graphics | 16 | 22 | 24 |
  | library | footage, collections, imports, pictures and logos, people | 15 | 18 | 24 |
  | admin | the setup: machines, jobs, storage, the log, keys | 14 | 16 | 20 |
  | all | one tool per operation | 231 | 263 | 458 |

  The guards (`internal/mcp`): the core has at most 12 tools and 2,000 tokens, a toolset at most 25 tools, the
  server's instructions at most 150 words, no two tools in a list are described nearly alike, and no tool both reads
  and changes. Inputs and answers are the operations' JSON, whichever tool runs them.
- **Three kinds of connection.** Read-only (the default), **propose-only** (`--propose`: changes are saved as
  drafts that wait for a person on the review desk; docs/review-desk.md) and **changes allowed** (`--allow-changes`,
  each change confirmed). `--agent <name>` names the app (default: the name the client gives itself), so what it saves
  says which app made it.
- **Read-only by default.** Only operations that change nothing run; `find` still lists the others, marked "changes
  something". Reading resources is read-only (`resources.show`, `resources.path` for a repository's read-only copy);
  bringing in a repository or a web page (`resources.fetch`) and making code slides, diagrams and cards
  (`resources.make`) are changes.
- **`--propose`** offers only the changes a draft may hold (an allowlist: docs/review-desk.md), and makes none of them: `run_change`, the tools named
  `…_change` and an operation's own changing tool save the change as a draft (a new version stays beside the cut;
  another change is kept until approved) with the app's reason (`note` on `run_change`, `draftNote` on the others);
  no confirm token is needed. The core offers `draft_list` (the app's drafts and what a person decided) in place of
  `render_submit`, and the answer to the app's next call says once what a person decided. Refused: anything
  people-only, owners', this computer's, paid, a render, or unlocking. It can't be combined with `--allow-changes`.
- **`--allow-changes`** offers the operations that change MetaRush too, and each change is confirmed: the first call
  returns what it would do and a token; calling again with the same arguments plus the token does it (tokens are
  bound to the exact operation and arguments, held only by that server process, and expire after 10 minutes). The
  token goes in `confirmToken`, or in `mcpConfirmToken` for an operation with a `confirmToken` of its own
  (`brand.set`'s, for the cut it may change). The edits that check themselves keep their own check — `cut_check` (or
  `edit_check`) returns the token `cut.apply` (or `edit.apply`) needs, and the cut verbs take `dryRun` first; which
  operations do is a fixed list (`catalog.ChecksFirst`), never a guess from their fields. Through `run_change` the
  token goes beside `name` and `input`; an edit's own token goes inside `input`.
- **Who approves a change: the person, in the client.** The confirm token is the model's own second call, so it is
  no one's approval; what asks a person is the client, which approves by tool (Claude Code's permission rules,
  `mcp__metarush__<tool>`, don't look at arguments; Claude Desktop's "always allow" is per tool). So no tool both reads
  and changes: `run` and every tool whose `readOnlyHint` is true only read, and `run_change`, the tools named
  `…_change` and the operations' own changing tools (`render_submit`; in `--toolset all`, each one marked as not
  read-only) only change. **Allow the reading tools; leave the changing ones on ask**, and each change asks you. In
  Claude Code, for example, after `claude mcp add metarush-edit -- metarush mcp serve --allow-changes`, allow the
  reading tools by name (`mcp__metarush-edit__find`, `mcp__metarush-edit__run`, …) and none of the changing ones
  (`mcp__metarush-edit__run_change`, `mcp__metarush-edit__render_submit`).
- **Never offered: what only a person may do** — approving versions, approving motion graphics, deleting footage,
  moving the media, purging the log and the other people-only operations. Calling one, by its tool name or through
  `run` or `run_change`, is refused; so is a change by the tool name of an operation the list doesn't offer.
- **Other catalogues join `find`.** A catalogue registers with `internal/catalog`; its entries are found and
  described like operations — with the stricter of their own flags and their operation's — and run through an
  operation of their own, under the same rules; an entry that is a person's to do, for owners or this computer only,
  or a change, when its operation doesn't hold callers to that, is refused. An entry's name is looked up in every
  catalogue before it is taken as an operation's tool name. Two join today (docs/catalogue.md):
  - **The first-party catalogue**: what MetaRush does, as abilities. One that is an operation as it is
    lends it its title and words, so `find` lists the operation once, saying what it needs here (`footage.visuals —
    … (needs a Mac)`); one with fields fixed is its own entry (`metarush/steady-footage`), run as its operation with
    those fields set — another value for one, or the same key in another case, is refused. Cloud ones say what leaves
    this computer.
  - **The workspace's abilities**: each a change, and a person's to do unless it is free, uses no key and
    sends nothing but words (the run's decider), so `run` refuses it and `run_change` asks before each one —
    or, for a person's, refuses it with `E_HUMAN_ONLY` and the command a person runs (`metarush ability run <name>`); one
    no owner approved, or not set up on this computer, says so in its line ("needs an owner's approval", "needs
    setting up on this computer") and is refused before anything runs. `describe` gives its inputs under `inputs`
    and asks for the plan first: `run` with `ability.plan` (a read) gives the fingerprint `run_change` takes as
    `plan`. Read-only connections list abilities, marked "changes something", and run none.
- **What works here.** With a workspace open, every hit of `find` that is an ability (or an operation one stands
  for) says whether it works on this computer now or what it needs first, in plain words.
- **Everything goes to the activity log** on the `mcp` channel, refusals and confirmation checks included
  (`metarush log list --channel mcp`, or the Log page).

## Claude Code

```bash
claude mcp add metarush -- metarush mcp serve                      # read-only, this project
claude mcp add metarush --scope user -- metarush mcp serve         # read-only, every project
claude mcp add metarush-drafts -- metarush mcp serve --propose     # changes saved as drafts for a person to review
claude mcp add metarush-edit -- metarush mcp serve --allow-changes # changes, each one confirmed
claude mcp add metarush-review -- metarush mcp serve --toolset review # the review tools as direct tools
claude mcp list                                                    # check it connects
```

Claude Code also has MetaRush's own skills (`metarush skills install`), which drive the command line; the MCP
server is for clients that do not run shell commands, or when you prefer tools to commands.

## Claude Desktop

Add the server to `claude_desktop_config.json` (Settings > Developer > Edit Config; on a Mac
`~/Library/Application Support/Claude/claude_desktop_config.json`) and restart Claude Desktop. Use the full path
that `which metarush` prints: Claude Desktop does not search your shell's PATH.

```json
{
  "mcpServers": {
    "metarush": {
      "command": "/opt/homebrew/bin/metarush",
      "args": ["mcp", "serve"]
    }
  }
}
```

Add `"--allow-changes"` to `args` to allow changes, `"--toolset", "edit"` for a toolset, or `"--home",
"/path/to/home"` before `"mcp"` for another installation. Claude Desktop starts stdio servers directly, so **no `mcp-remote` bridge is needed**: that bridge is
only for servers reached over HTTP, and MetaRush's is not one.

Clients start servers with a small environment. Installations that keep their secrets in the keychain (the
default) need nothing more. An installation set up with `--secrets env` reads its database address from
`<home>/.env`, which also works; if you keep `METARUSH_*` variables only in your shell, add them under `"env"` in
the client's entry.

## Codex, Cursor and other apps

Any app that starts MCP servers over stdio works the same way; connect the ones that shouldn't change your videos
with `--propose`. Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.metarush]
command = "metarush"
args = ["mcp", "serve", "--propose", "--agent", "codex"]
```

Cursor (`.cursor/mcp.json`, or `~/.cursor/mcp.json`):

```json
{ "mcpServers": { "metarush": { "command": "metarush", "args": ["mcp", "serve", "--propose", "--agent", "cursor"] } } }
```

docs/review-desk.md says what such an app may do, how its drafts are reviewed, and how to hold an app that runs
commands to proposing on the command line.

## Checking it

```bash
metarush mcp tools                          # what a client sees: the core, read-only
metarush mcp tools --allow-changes          # the core with changes allowed
metarush mcp tools --propose                # the core of a propose-only connection
metarush mcp tools --toolset review --json  # a toolset, with each tool's modes and what the list costs (size)
metarush ops find make a review copy        # what find answers, from the command line (--details: names and commands)
scripts/mcp-check.sh --home <home>          # the official MCP Python SDK against `metarush mcp serve`
```

`scripts/mcp-check.sh` installs the MCP Python SDK in a throwaway virtual environment and checks the core, one
toolset (`--toolset review` unless you name another) and every operation as tools: the handshake, that every tool has
a name clients accept and a valid JSON Schema, `find`, `describe` and `run`, a tool with a mode, the refusal of a
people-only operation and of a change on a read-only connection; `--changes` also checks that no tool both reads and
changes, that `run` refuses a change, and the two-step confirmation through `run_change` and through an operation's
own tool (it creates two projects, so use a test installation). The Go tests
(`internal/mcp`) do the same with a client over pipes, plus the guards, another catalogue joining `find`,
cancellation, expired tokens and the activity log; a test keeps the table above in step with the code.
`scripts/mcp-agents.sh` drives two propose-only clients named as Codex and Cursor name themselves (docs/review-desk.md).
