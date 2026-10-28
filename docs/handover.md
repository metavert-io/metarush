# Handing a computer's role to another computer

Some things in a workspace belong to one computer: the dashboard and background work that run there, the
conversations in Claude's panel (Claude Code keeps each one's own record on the computer it ran on), the work that
waits for that computer (recipe runs, "Planning by itself", link imports and other work it was asked to do), its
watched and linked folders, its remote access, the browser extensions paired with it and its keys. **Handing its role
over** moves all of that to another computer of the workspace — when you replace a laptop, move a team onto an
always-on Mac mini, or give a computer away.

It happens in four steps:

```bash
metarush machine handover plan --to "Edit mini"     # here: what happens to each thing (nothing changes)
metarush machine handover export --to "Edit mini"   # here: the handover file, protected by a passphrase
metarush machine handover receive <file>            # on Edit mini: the conversations go on there; the work moves
metarush machine handover clean                     # here, later, only when you're sure: this computer's copies go
```

The new computer must already be a computer of the workspace: invite it and join it there first
(`metarush machine invite`, then `metarush machine join` on it; see [adding a machine](runbook-add-machine.md)).

Every step is an **owner's, and a person's**: never Auto, Claude (in the dashboard or in a terminal), an app connected
through MCP, or a draft on the review desk. Making, receiving and cleaning up after a handover, and taking over from a
computer that's gone, happen at a person's own terminal. Each is in the activity log and the audit.

## The dry run

Settings → Storage → **Hand over this computer's role…** (choose the computer that takes it over), or
`metarush machine handover plan --to <computer>`, lists in plain words everything tied to this computer and what
happens to each:

- **Conversations in Claude's panel**, and whether each one's Claude Code record is on this computer. One whose record
  is here travels in the handover file and goes on where it left off; one whose record isn't here (or that Claude Code
  had already lost) goes on from a summary of the conversation.
- **Recipe runs and "Planning by itself"**: they move, and go on there.
- **Work waiting for this computer**: queued work moves; work running now finishes here, or a person stops it; link
  imports and generated shots that have started finish here (their downloads and requests are this computer's); a move
  of the media and files still being sent from a linked folder stay, as they act on this computer's own files.
- **The watched folder and linked folders**: you say where each is on the new computer when you receive the file, or
  skip it.
- **Remote access**: it goes off here when this computer steps down; turn it on there (its address is different, so
  the links people kept change).
- **Browser extensions paired here**: each person pairs their browser with the new computer again.
- **Keys on this computer, by name only**: they never travel. Each computer keeps its own — set them again there.
- **Abilities set up here**: set them up again there before they run there.
- **The dashboard and background work**: they stop here when this computer steps down; install the background service
  there.
- **The steps only a person takes**, each with its command or its place in Settings (below), and what must happen
  first (Claude still working on a conversation here, say).

Nothing changes.

## Making the handover file

```bash
metarush machine handover export --to "Edit mini" [--out <file>] [--passphrase-stdin] [--no-passphrase]
```

The file holds each conversation's Claude Code record (its session file and the session's own folder of files, when
there is one), each conversation's folder (the plans and attachments of its messages), the list of the work tied to
this computer and the folder settings. It never holds a key.

- **It is protected by a passphrase** you choose (asked for at the terminal, or read from standard input with
  `--passphrase-stdin`; never an argument, so it stays out of your shell's history and the log). It is an
  [age](https://age-encryption.org) file with a passphrase, so you can look inside it anywhere:
  `age -d metarush-handover-….age | tar tz`. `--no-passphrase` writes it unprotected, only once you type
  `unprotected` at the terminal after the plain warning that anyone who gets it can read the conversations.
- **It is private**: readable only by your account (0600), never written over a file that is there, and never into a
  synced folder (iCloud Drive, Dropbox, Google Drive…) or a git repository. By default it goes into
  `~/.metarush-bundles`. **Carry it yourself** — a drive, AirDrop, scp — never through the workspace's shared storage
  or a synced folder. `--out` may be on a USB drive (exFAT or FAT too). The folder is checked first: one MetaRush can't
  write in is refused before anything is marked, and a file that fails part-way leaves everything as it was.
- **Each conversation's Claude Code record is found by its session**, wherever Claude Code keeps it on this computer
  (the Claude Code that MetaRush runs in the background, or the one in your terminal).
- **From now on this computer no longer continues those conversations** (below), and the panel says they are moving.
  It refuses while Claude is working on one, or one of its cards is running: wait until it finishes (or stop it), then
  run it again.
- Running it again makes another file for the same handover. Changed your mind? `metarush machine handover abandon`
  (or **Abandon…** in Settings → Storage) puts everything back here, before the file is received. Abandoning a
  handover that was abandoned already puts back anything still marked as moving, so it is always safe to run again.
- If making the file stops part-way (or the handover is abandoned meanwhile), every conversation it marked goes on
  here again; a handover whose file was made before keeps the conversations that file carries.

## Receiving it

```bash
metarush machine handover receive <file> [--folder OLD=NEW]... [--skip-folder OLD]... [--plan] [--passphrase-stdin]
```

On the new computer. It asks for the passphrase, and for where each folder is on this computer (Enter skips it). It
reads the file's list first — whose workspace, which computer it is for, whether there is room — and unpacks nothing
before; every file in it must be one the list names, of the size and fingerprint it says. Then it:

- **puts each conversation's Claude Code record where Claude Code looks for it here** — in the folder of the Claude
  Code that MetaRush runs in the background (`$CLAUDE_CONFIG_DIR` when that is set for it, else `.claude` in your home
  folder), under `projects/` and the name Claude Code gives MetaRush's working folder on this computer, spelled as the
  disk spells it — and each conversation's folder under this computer's MetaRush folder. A copy that was already there
  is set aside, never deleted;
- **makes this computer the one that holds the conversations**: they go on here, in the same Claude Code session.
  When MetaRush's folder is somewhere else on this computer, the first message here tells Claude, in one line, where
  the files it read are now;
- **goes on from a summary** for a conversation whose record didn't come along or can't be read: the next message
  starts a new Claude Code session with a summary MetaRush makes from the conversation's own history (what was said,
  and the changes Claude suggested), and the panel says "continuing from a summary";
- **moves the work** that waits for the old computer: recipe runs, "Planning by itself" and queued work, each by
  check and set, so nothing runs twice;
- **sets the folders** where you said (the watched folder then takes only new files: the old computer added the rest).

`--plan` says what it would do and changes nothing. While a file is being received the handover can't be abandoned; if
receiving stopped part-way, receive the same file again (what came already stays). If the computer receiving it
stopped part-way and is gone, abandon the handover on the old computer once that computer has been quiet for 30 minutes
(as for a take-over: by the database server's clock, running no work) — what it already took stays with it, the rest
goes on here — or take that computer over, which ends the handover too: what it took comes with the rest of its work,
and the conversations it never took go on with the computer that sent them. Once it was received, receiving the
same file again changes nothing. If the handover was ended on the other side meanwhile, it says so instead of saying it
worked.

## One history

Claude Code adds to the same session file when a conversation goes on, so two computers continuing one conversation
would make two histories. From the moment a conversation is marked as moving, the old computer refuses every message
to it — checked in the database by the computer that holds it, together with marking it as working, so a message and
a handover never cross — and once it lives on the new computer, the old one lets it go, its cards included (a card runs
only on the computer that holds its conversation, and only once). Notes for Claude that arrive meanwhile (a version
turned down with a note, say) wait for the new computer.

## The old computer steps down

Once the new computer has received the file, the old one (while MetaRush runs there in the background) moves what can
still move, finishes its own running work — a post still being sent to YouTube included — and then **steps down**: its background work and remote access for the
workspace stop, it starts again as a **client** and every page says so ("This computer is no longer the host: it
handed its role to “Edit mini” …"). It still works as a client: open projects and edit there; it follows a move of the
database and changes to its `config.yaml` as a host does. Nothing is deleted there. If it is off at the time, it steps
down the next time MetaRush starts.

A computer that receives a handover later (the role handed back) is the host again — also when it hadn't finished
stepping down: the earlier handover ends then, and it can hand its role over again.

## Work asked for on a computer that stepped down

It runs no background work, so work asked for there goes to the computer that has its role now (followed through
later handovers): recipe runs and "Planning by itself", link imports, repositories and web pages, and scene
descriptions. Work that needs the old computer's own keys, sign-ins, set-up or files is refused in plain words naming
the computer to start it on: AI work and generated shots, a link import with this browser's sign-in, bringing in,
refreshing or pushing a Remotion project, an ability's run, and posting to a video platform. An AI voice, a move of the
media and recordings run where they are asked for, as before.

## Steps only a person takes

The dry run lists them with their commands; Settings → Storage shows them too.

- **Claude Code signed in on the new computer** (run `claude` there once; `claude auth status` checks it). From then on
  **the Claude account signed in there answers these conversations** — its plan and its terms, not the old computer's
  (Settings → Claude Code → Whose Claude answers).
- **The keys the new computer needs**: `metarush providers set <name>` (Settings → Integrations); posting to YouTube:
  `metarush publish setup youtube`, and each person signs in to YouTube again there.
- **Abilities set up on the old computer**: `metarush ability setup <name>` there.
- **The background service on the new computer**: `metarush service install`.
- **Remote access**: `metarush remote enable --apply` on the new computer (its address is different: tell people the
  new one), `metarush remote disable --apply` on the old one to stop `tailscale serve` there.
- **Browser extensions**: each person pairs theirs with the new computer (Settings → Browser extension, or
  `metarush extension pair`).

## Cleaning up the old computer

```bash
metarush machine handover clean [--yes]
```

Only on the old computer, only after the new computer received the file, and only when you ask: it removes this
computer's copies of the handed-over conversations' Claude Code records and folders — of every handover from here not
cleaned up yet, one a later handover overtook included — never of a conversation held here now (one that came back,
through any computer), and the handover files made here when they are still where they were written, as they were
written.
It says what it removes and asks first (`--yes` doesn't ask again). Settings → Storage offers it.

## Taking over from a computer that's gone

A computer that broke, was wiped or was given away can't make a handover file. Take over from it on the computer that
goes on:

```bash
metarush machine take-over "Old MacBook" --from <copy of its folders, or a handover file it made> --plan
metarush machine take-over "Old MacBook" --from <…>
metarush machine revoke "Old MacBook"     # last: its storage key stops working
```

- **Only when it is off.** It must not have been seen for 30 minutes — neither MetaRush in the background nor a command
  in a terminal there (by the database server's clock: the computer's own clock doesn't matter) — it must be running no
  work, and you confirm it is off by typing its name. A computer still running would do the same work twice.
- A handover to it that it never finished receiving ends: the conversations it never took go on with the computer that
  sent them.
- **Its conversations come from `--from`**: a plain copy of its folders — Claude Code's `projects` folder
  (`~/.claude/projects` there) and MetaRush's `assistant/conversations` folder, with its `config.yaml` for the folder
  settings (nothing else in it is read: never a key) — or a handover file it made. Conversations whose record isn't in
  the copy go on from a summary.
- **Everything tied to it moves**, link imports and generated shots it had started included: they start again here
  (generated work goes on from the request already sent; nothing is paid for twice).
- If the old computer comes back, it starts as a client.
- Last, remove it from the workspace (`metarush machine revoke`), so its storage key stops working.

## In the dashboard

Settings → Storage → **Hand over this computer's role** (owners): choose the computer that takes it over and read the
dry run, with the steps only a person takes and the commands for a terminal; follow a handover in progress, abandon
one that wasn't received, see what still finishes before this computer steps down, and, on a computer that stepped
down, the clean-up offer. Making and receiving the file, cleaning up and taking over happen in a terminal: the file and
its passphrase never pass through the browser.

## Commands

| Command | What it does |
|---|---|
| `metarush machine handover plan --to <computer>` | what handing this computer's role over would do with each thing tied to it |
| `metarush machine handover export --to <computer> [--out <file>] [--no-passphrase]` | the handover file (a person, at a terminal) |
| `metarush machine handover receive <file> [--folder OLD=NEW] [--skip-folder OLD] [--plan]` | receive it on the new computer (a person, at a terminal) |
| `metarush machine handover status` | where this computer stands — the host, stepping down or a client — and the handovers in progress |
| `metarush machine handover abandon` | abandon a handover that wasn't received |
| `metarush machine handover clean [--yes]` | on the old computer, after it was received: remove its copies of the conversations |
| `metarush machine take-over <computer> --from <folder or file> [--plan]` | take over from a computer that is gone |

Every command takes `--json`; `--details` adds the computers' and conversations' IDs and the folders' paths.

## How it works (for developers)

- `internal/handover` — the operations (`machine.handover.plan|status|export|receive|abandon|clean`,
  `machine.takeover`), the records (`handovers`, `hov_…`; one in progress per computer, a unique partial index), the
  handover file (`file.go`: a gzip'd tar inside age's scrypt mode, `handover.json` first with every file's size and
  SHA-256, each file hashed before writing and checked as written; placed by a hard link, else an exclusive create;
  read strictly into a private folder under `<home>/state/handover`: the list checked before anything is unpacked,
  each entry held to its listed size and hash), moving the work (`work.go`; a post this computer is sending counts as
  its own running work) and the role (`RoleOf`: the newest settled handover involving a computer says whether it is a
  host, stepping down or a client; `Look`/`Watch` step it down, and `settleOvertaken` ends a computer's own handover
  once the role came back to it). States: exporting → exported → receiving (claimed by check and set; abandoned only
  once the receiver passes the take-over's quiet check, or by a take-over of the receiver: `endHandoversTo`) →
  received → done, or abandoned. A mark never outlives its handover: an export looks at its record again after marking
  and puts back what it marked on every way it stops (`putBack`), abandon always unmarks (also when abandoned
  already), and `Look` takes off the marks of handovers that ended (`repairMarks`). Clean covers every received
  handover from here not cleaned up yet (`cleanOffer` for the status), skipping conversations held here now. The passphrase reaches an operation only through its context
  (`handover.WithPassphrase`), never its input.
- `internal/assistant/claudepaths.go` — where Claude Code keeps a session: `$CLAUDE_CONFIG_DIR`, else `$HOME/.claude`
  — for placing, the folder the background MetaRush's Claude Code uses (`RecordClaudeConfig` at its start,
  `<home>/state/claude-code.json`); for finding, any of them (`FindSession`: `projects/*/<session>.jsonl`);
  `projects/<the working folder as the disk spells it, every character but letters and digits made a dash, cut at
  200 with a hash>/<session>.jsonl`, and a folder of the same name beside it. Under `go test` only
  `CLAUDE_CONFIG_DIR` counts.
- `internal/assistant/handover.go` — one history: every message first claims the conversation in the database (held
  by this computer, not handing over, set working with this process as its holder in the same write: `mayContinue`);
  a handover marks only idle conversations with no card running (`MarkHandingOver`); cards are claimed by check and set
  from pending to running on the computer that holds the conversation (`claimCards`); the holder's full saves match its installation, so a computer lets go of a
  conversation that moved; notes from elsewhere wait for the computer that holds it. `AdoptConversation` re-pins a
  conversation by check and set; a moved conversation tells Claude once where its files are now, and one whose session
  didn't come along (or isn't where Claude Code looks when it spawns) goes on from `summaryOf` in a new session.
- Work moves by check and set by the database server's clock: queued jobs and running ones whose lease ended (never
  started link imports and generated shots while their computer runs, never a move of the media or a linked folder's
  upload), recipe runs no look holds (`recipes.LeaseFree`; a run's lease is `$$NOW` + 5 minutes) and "Planning by
  itself" (a trigger's look writes only while the triggers are still its computer's).
- `internal/cli/daemon.go` — a computer that stepped down runs `serveClient`: the dashboard for its own browser
  (`api.Server.RemoteOff`), no worker or background work; `clientWatch` restarts it when the role changes, a move of
  the database starts or `config.yaml` names another media storage or key.
- `internal/app/workhost.go` — `WorkHost` follows a computer's handovers to the computer that has its role; work asked
  for on a client is pinned there (`PinWork`) or refused (`NeedsThisComputer`, reason `handed_over`).
- Take-over's quiet check reads `lastSeenAt` (MetaRush in the background, every minute) and `lastUsedAt` (every
  process that changes something, before its first write); a move of the database waits only on `lastSeenAt`.
- Tests: `go test ./internal/handover/` (two computers in one test, the fake Claude Code keeping sessions where Claude
  Code does), `./internal/cli -run TestHandover`, `./internal/assistant -run TestClaude`, and the browser run
  `scripts/handover-e2e.sh` (ports 5710–5719, macOS: it plays a person at a terminal with `script(1)`).
