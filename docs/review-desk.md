# The review desk: drafts from any AI app

Any AI app you use — Claude Code in a terminal, Codex, Cursor, Claude Desktop, others that speak MCP — can work on
your videos without being able to change them. Connected so that it may only **propose**, it reads your
footage, cuts and settings, checks its ideas, and saves each change it wants as a **draft**. Every draft waits in
**Awaiting your review** until a person approves it, says **Not quite** with what should change, or turns it
down. Approving, delivering, spending money and everything else that is a person's to do stay a person's.

## Connecting an app so that it only proposes

The safe way is MCP: the app starts `metarush mcp serve --propose`, and nothing it sends can change that. Name the
app with `--agent` (without it MetaRush uses the name the app gives itself), so its drafts say who made them.

**Codex** (`~/.codex/config.toml`):

```toml
[mcp_servers.metarush]
command = "metarush"            # the full path `which metarush` prints, if Codex doesn't find it
args = ["mcp", "serve", "--propose", "--agent", "codex"]
```

or `codex mcp add metarush -- metarush mcp serve --propose --agent codex`. `codex mcp list` shows it.

**Cursor** (`.cursor/mcp.json` in a project, or `~/.cursor/mcp.json` for every project; Cursor Settings → MCP lists
it and shows its tools):

```json
{
  "mcpServers": {
    "metarush": {
      "command": "metarush",
      "args": ["mcp", "serve", "--propose", "--agent", "cursor"]
    }
  }
}
```

**Claude Code**: `claude mcp add metarush-drafts -- metarush mcp serve --propose --agent claude-code`.
**Claude Desktop**: `"args": ["mcp", "serve", "--propose"]` in `claude_desktop_config.json` (docs/mcp.md).

Add `"--home", "<folder>"` before `"mcp"` for another installation, and `"--toolset", "edit"` (or review, library,
admin) for direct tools instead of the core. Installations that keep their database address only in `METARUSH_*`
variables of your shell pass them under `env` in the app's entry; the default keychain setup needs nothing.

**Skills for these apps.** `metarush skills install --agents` gives Codex and other apps that read Agent Skills
MetaRush's video skills and its part of a project's AGENTS.md ([docs/plugin.md](plugin.md)).

**The command line.** An app that runs commands (Codex, Cursor's agent, Claude Code's skills) can be held to
proposing with `--propose` or `METARUSH_PROPOSE=1`, and named with `--agent` or `METARUSH_AGENT`: every change is then
refused with the command that saves it as a draft (`metarush draft save …`). Set both in the environment the app's
commands start with — for Codex:

```toml
[shell_environment_policy]
set = { METARUSH_PROPOSE = "1", METARUSH_AGENT = "codex" }
```

This holds an app that follows its settings, not one that sets out to get around them: an app that can run any
command can run MetaRush without the variable. When that matters, connect it through MCP only and keep its shell
from running `metarush`.

## What a proposing app may do

A draft holds only changes MetaRush can show you exactly, by name and in full. Everything else is refused when the
app tries to save it, with a plain reason, so it asks you instead.

**New versions** (saved beside the cut, with their own review copy and what changes in words):

- a cut sheet (`cut.apply`) or an edit plan (`edit.apply`);
- the cut verbs: `cut.add`, `cut.move`, `cut.trim`, `cut.split`, `cut.delete`, `cut.deleteRange`, `cut.removeWords`,
  `cut.transition`, `cut.framing`, `cut.flashes`, `cut.names`, `cut.update`, `cut.useFixed`;
- the brand or house style on the cut (`brand.apply`, `content.style.apply`);
- the sound mix, levels and a narration line (`mix.set`, `sound.levels`, `voiceover.line`).

**Other changes** (kept until you approve them, then made as you): a review comment (`comment.add`), a version's
note (`revision.note`), a cut's or a clip's name (`cut.rename`, `asset.update`), a project rule
(`project.rules.add`), a house rule or sign-off (`content.rules.add`, `content.signoffs.add`), the spelling list
(`transcript.spelling.add|remove`), the house style (`content.style.set`) and the brand (`brand.set`: colours, font,
logos — not taken from a website).

| It can | It can't |
|---|---|
| read projects, footage, transcripts, cuts, versions, renders and quality checks | approve a version or a motion graphic, or decide about a draft |
| check a cut sheet or an edit (`cut_check`, `run` with `edit.check`) | put anything in the cut: a draft's version stays beside it |
| save the drafts above, with a note saying why | start a render or a quality check (MetaRush makes a review copy of every draft by itself) |
| list its drafts and read what you decided (`draft_list`) | spend money: generated video, the AI voice, clearer voices, Recipes runs |
| | process footage or sound first (music fitting, clearer voices, calls), copy or play another project's cut in this one, change a project's name or brief |
| | change owners' settings, this computer's files, people, keys or storage, or unlock locked parts of a cut |
| | send a version out past the quality check or a waiting sign-off (a person's decision, with their own note; a drafted rule may be a promise, which a person approves) |

Through MCP every change goes through `run_change` (or a toolset's `…_change` tool) with a `note` saying why; it
is saved as a draft and the answer says so. The reading tools never save anything, so a client may allow them all.
On the command line:

```bash
metarush draft save --sheet intro.cut.md --note "The intro dragged; I took out the first sentence."
metarush draft save content.rules.add --input '{"text": "The name bar never animates"}' --note "You asked twice."
metarush draft list                 # an app's own drafts and what a person decided (--all: everyone's)
```

## Drafts

- **A version draft** is checked like any change (problems stop it; notes are kept) and saved as a numbered
  version beside the cut: it plays, has a cut sheet and gets a review copy (at most twelve of one app's drafts an
  hour), but the cut's latest version doesn't move. A change written on a draft's version is the draft and the change
  together, so an app can build on its last draft and a person can fix a small thing before approving — and
  once a version carrying the draft goes into the cut, the draft leaves the desk as **in the cut**.
- **A change draft** names everything it touches: the project, version, clip (with its file name and projects) or
  picture by its name (and keeps that very one, whatever it's called later), every word in full, a title or end card
  part by part. A setting it can't show refuses the draft. A Content Settings change names the settings it changes —
  the default's at the time, kept even if another project's become the default before you approve.
- **What approving does is measured, twice.** A brand draft shows every part of the brand it changes, from what to
  what — the colours a new logo brings too; a spelling that also corrects existing transcripts shows how many places,
  in which videos and projects. When you approve, MetaRush measures again and refuses (the draft stays waiting,
  saying why) if approving would now do something else or more.
- **Nothing a draft holds costs money.** The page says so plainly ("These changes cost nothing to make"); a change
  that would cost money can't be drafted.
- Every draft records the app (`agent`: codex, cursor, claude-code…), how it reached MetaRush (`via`: mcp or cli),
  the person it works for, when, and its note. An app's words are cleaned of control characters and terminal escapes
  when saved (terminal escapes, invisible and direction-changing characters). One app may have at most 50 drafts waiting.

## Awaiting your review

The Studio page's **Awaiting your review** (and `metarush revision waiting`) lists:

- each cut's latest version that an AI app made (Claude, or any app that named itself) or that someone asked to
  have reviewed;
- each app's waiting draft of a version, marked as not in the cut yet;
- each app's other waiting changes.

A draft opens its page, `/review/<draft>`: what changes (in words, from the cut sheets of the two versions), why
(the app's note and request), the review copy (made by itself about a minute after the draft) and its quality
check, the check's notes, the cost (approving a draft never pays an outside service), and what's waiting. The
decisions are people's, in the dashboard or their own terminal:

- **Approve** — the draft is checked again with today's facts (a clip put in the Trash since keeps it out), then a
  version goes into the cut as its latest version, approved by you. If someone saved a newer version meanwhile, the
  draft's change is made on top of it (a new version, in the app's name) where the two don't overlap; where they do,
  the draft stays waiting and says which version and clips. A change is made as you, and the page says what it did.
  An approval is carried out once, even if two people press Approve together.
- **Not quite…** — say what should change; the version reads Needs changes. The app reads your note with
  `draft_list`, and on MCP the answer to its next call tells it once. Its next draft, saved with `replaces`, takes
  the old one's place.
- **Turn down** — with a reason if you like.

`metarush draft approve|not-quite|turn-down <draft>` do the same in a terminal; approving a draft's version on its
version page (or `metarush revision approve`) is the same decision.

## The packet

Every draft and every version has one page to hand to a client or a colleague: what changed and why, the
checks, the cost, who made it and what is still waiting. The dashboard's page copies it or downloads it as Markdown
(every word escaped, so nobody's words turn into links or pictures);
`metarush revision packet <draft or version>` prints it (`--markdown`, `--out <file>`, `--json`).

## Checking it

```bash
scripts/mcp-agents.sh --home <test home>   # two fake apps (codex, cursor) over the MCP Python SDK: read, propose, refused approving
scripts/review-desk-e2e.sh                 # a person reviews in a real browser: Not quite, the app's next draft, approve (ports 5450–5459)
```

The Go tests cover drafts (`internal/ops/drafts_test.go`: refusals, both kinds of draft, approving on a moved cut,
drafts on drafts, the packet, review copies) and the propose-only connection (`internal/mcp/propose_test.go`).

### A real run with Codex and Cursor

These need a person with the apps installed and signed in:

1. Add the Codex entry above, start Codex in a folder, and ask: "Use MetaRush to look at my latest cut of <project>
   and propose a shorter opening." Check that it reads, saves a draft (Awaiting your review shows "Codex's draft"),
   and that asking it to approve its own draft is refused.
2. Say Not quite on its draft with a note, ask Codex to look at its drafts, and check that it makes a new draft that
   takes the old one's place. Approve it and check the cut.
3. Do the same with Cursor (the `.cursor/mcp.json` entry), asking for a house rule ("never animate the name bar").
4. Note anything either app did that the scripted clients don't (retries, how it read the tool descriptions).
