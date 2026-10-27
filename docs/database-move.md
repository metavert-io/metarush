# Moving the database

MetaRush keeps your projects, versions, transcripts, comments, people and the activity log in a MongoDB database; the
footage itself is in the storage (see [Moving the media](deployment.md)). **Moving the database** copies all of it into
another, empty database and switches to it, the way moving the media does: a plan first, a copy, a check of every
record, and then each computer switches — only once everything is checked. The old database is never deleted.

Where it can go:

- **The database on this computer** — MetaRush's own, set up the way `metarush setup` does it (MongoDB downloaded from
  MongoDB's own server, a port of its own, a sign-in only MetaRush on this computer has). Only this computer reaches
  it, so it suits a workspace this computer alone uses.
- **Another MongoDB** — MongoDB Atlas or your own server. Every computer of the workspace can reach it, each with its
  own sign-in.

Moving the database, switching a computer to it and abandoning a move are an **owner's, and a person's**: never Auto,
Claude (in the dashboard or in a terminal), an app connected through MCP, or a draft on the review desk. An owner may
ask for the plan anywhere, an AI app included.

## The new database's address

The address of another MongoDB holds its sign-in, so MetaRush never takes it on a command line (it would stay in the
shell's history) and never writes it to its settings file, the activity log, an answer or the move's marker:

- in a terminal, `--to address` reads it from standard input (or asks for it, hidden, at a terminal), and
  `--to env:NAME` reads it from an environment variable;
- in the dashboard (Settings → Storage → Move the database) it is typed once into a password field.

It is kept in this computer's keychain for the move (or, for a MetaRush that keeps its keys in files, a private file in
its `database` folder), so `--to saved` uses it again: to continue a move, or after a plan. It is kept only while a move
needs it: a plan that can't go ahead, a move refused before anything paused and an abandoned move forget it (a move
that stopped part-way keeps it to continue; the sign-in MetaRush made for the database on this computer is that
computer's own and stays). Once the computer switches it moves to where MetaRush keeps the database's address (`mongodb-uri` in the keychain, or the home's `.env`), and the old
address is kept aside (`mongodb-uri-previous`, or `database/previous.secret`).

Each computer uses **its own** sign-in: a database user of its own (in Atlas: Database Access → Add New Database User),
never another computer's. Sign-ins are never copied between computers.

## The plan

```bash
metarush database plan --to here                                   # the database on this computer
metarush database plan --to address --name studio < atlas-address.txt
metarush database plan --to saved --name studio --json             # the address an earlier plan kept
```

It changes nothing (it doesn't even set the database on this computer up) and says, in plain words:

- **what moves**: every kind of record with its count and size (`--details`, `--json`), and headline counts ("4
  projects, 120 clips, 31 versions…");
- **how long it takes**, about;
- **the workspace's computers** and what each needs to use the new database — its own sign-in and a switch on that
  computer, or, for the database on this computer, that the others couldn't reach it;
- **what waits meanwhile** (below);
- whether the new database **answers, holds nothing and takes writes** (MetaRush asks the server what the sign-in may
  do: nothing is written to find out);
- clients' **What may leave** rules that keep their words, voices or faces from it;
- every computer whose MetaRush doesn't know about moves — older than the first release that does, not saying which it
  runs, or invited and not joined yet — **even when it's off**: started after the move, it would go on changing the old
  database. Update MetaRush there and open it once, or remove that computer from the workspace (`metarush machine
  revoke "<name>"`), first;
- that the old database is kept, and how you remove it later yourself.

The database on this computer is refused while other computers use the workspace — they couldn't reach it — unless you
say this computer will be the only one (`--only-this-computer`, or the checkbox in the dashboard).

## The move

```bash
metarush database move --to saved --name studio      # a person at a terminal; asks before it starts
```

1. **Changes pause.** A "being moved" marker goes into the old database. From then on every computer refuses changes,
   plainly: *"The workspace is being moved to another database; changes wait until it's done."* (`E_MOVING`, reason
   `database_moving`). It isn't only the commands: the database connection of every MetaRush process refuses every
   write while the marker is there (it looks at the marker about every second), so nothing that writes by itself slips
   through. Changes that were already running finish first; the move waits for them. MetaRush running in the background on each computer starts again **holding**: the dashboard
   reads and plays as usual, every page says changes wait, nothing works in the background (renders, transcripts,
   Recipes, review links…), work that was running stops and is handed back to run again later, and the activity log
   waits on that computer until it can be written where the workspace is. Each computer says in the marker that it
   paused; the move waits until every computer running MetaRush did and no work runs. Every time compared across
   computers is the database server's own, so a computer whose clock is wrong can't look paused or stopped when it
   isn't. A move doesn't start while the media is being moved.
2. **The copy.** The new database is locked first, so nothing uses a half copy. It gets MetaRush's own collections and
   indexes, made by MetaRush's updates up to the old database's level (as a restore does; an index of the old database
   MetaRush doesn't make is named, never made), then every record is copied in order; then every kind of record is read
   again on both sides and must match exactly (the same check as the workspace export's). Sign-in links, which end by
   themselves, are put in place one by one and compared without the ones about to end, so a database user that may only
   read and write records is enough. The migration level goes in last. Right before the old database is marked, it is
   read once more: a kind of record that appeared or a record that changed after the check stops the move. Passwords and
   invitations move with it (it is the same workspace in another place); the old database's own marker and lock, and
   what MetaRush writes again by itself (each computer's live record, counts of wrong passwords), stay behind.
3. **The old database says the workspace moved** — where, in words (*"MongoDB Atlas"*), never a sign-in. From then on
   every computer still using it refuses changes there for good, so the two can never split.
4. **This computer switches**: its settings name the new database (checked the way MetaRush loads them, and put back
   when they don't hold), and MetaRush running in the background starts again with it.
5. **Nothing is deleted.** The move says where the old database is kept and how to remove it yourself once every
   computer uses the new one: in Atlas, with your server's own tools, or by removing the `database` folder of the
   database on this computer. MetaRush never does.

The move is recorded in the new database's activity log and change history.

From the dashboard, **Settings → Storage → Move the database** plans the same way, asks in a dialog, and starts the move
in a process of its own (it keeps going while the dashboard starts again); the card shows how far it is.

### A move that stopped

Stopped part-way (Ctrl-C, a lost connection, the computer went to sleep), changes still wait on every computer:

- **run the same move again** on the computer that started it: it continues where it stopped (what is in the new
  database stays; nothing is copied twice) and checks everything again; or
- **abandon it** (`metarush database abandon`, or Abandon the move in the card): changes go on in the old database on
  every computer. A move that is still running is abandoned only on its own computer (stop it there first); its marker
  says it runs every few seconds, so another computer can tell. The new database keeps its lock, so nothing ever uses that copy; move again into a new, empty
  database when you want.

If something did change the old database while it was copied (a computer that didn't pause), the check notices it and
nothing switches: abandon the move and move again into a new, empty database.

If the copy is whole but this computer's own switch doesn't hold (a setting in its environment names another
database, the keychain didn't answer), the old database already says the workspace moved: this computer refuses
changes there and its card offers the switch, as on any other computer — `metarush database switch --to saved` (the
address is still kept here), or `--to here` for the database on this computer. It never ends up on the new database
while the old one could still be abandoned and changed.

## The other computers

After the move, MetaRush on every other computer still uses the old database, and refuses changes there: *"The workspace
moved to another database: switch this computer too."* Every page says so, and Settings → Storage → Move the database
shows the steps. At **that** computer, with **its own** sign-in:

```bash
metarush database switch --to address --name studio < this-computers-address.txt
metarush database move-status                         # what this computer uses, and the steps
```

or type its address into the card's switch form. MetaRush checks the address reaches the very copy the move made (the
new database's lock names the move), switches, and starts again with it.

## Client rules

The database holds clients' words — transcripts, names, notes — and the **voice and face prints** MetaRush knows their
people by (fingerprints of voices and faces, made from the footage). A database elsewhere on the internet (MongoDB
Atlas, or a server that isn't on this computer or your network) counts as **cloud storage** for What may leave, with
**words, voices and faces** as what goes there, as for a workspace's database anywhere. While any project's Content
Settings keep words, voices or faces from cloud storage, the plan names the rule and what the database holds, and the
move refuses it (`E_KEPT_HERE`, recorded as `leave.refused`). Restoring an exported workspace into such a database
checks the archive's own rules the same way, before any record goes in. The database on this computer and a server on
your network are fine.

## Commands

| Command | What it does |
|---|---|
| `metarush database plan --to here\|address\|env:NAME\|saved [--name] [--only-this-computer]` | What moving would do; changes nothing (owners) |
| `metarush database move --to … [--name] [--yes]` | Move the database (owners, a person at a terminal) |
| `metarush database switch --to address\|env:NAME\|saved\|here [--name]` | Switch this computer after another one moved the workspace, or after its own switch didn't hold (owners, a person at this computer) |
| `metarush database abandon [--yes]` | Lift the pause of a move that stopped (owners, a person) |
| `metarush database move-status` | Which database this computer uses; a move under way or done; what to do here |

Every command takes `--json` and `--details`. The operations are `database.plan`, `database.move`, `database.switch`,
`database.abandon` and `database.move.status`; MCP's admin toolset offers the plan and the status.

## For developers

- `internal/dbmove` — the copy, on the workspace export's engine (`internal/wsarchive`): `SurveySource`, `CheckTarget`
  (reads only; `connectionStatus` with its privileges), `Copy` (the restore's lock with kind `move`; MetaRush's own
  collections and indexes by `store.DB.MakeForRestore` before the first record; streamed in `_id` order by
  `EachDocWhere`/`Inserter`, never scrubbed; a stopped copy continued after the prefix that is in; every collection's
  `HashWhere` on both sides; `schema_migrations` last; `wsarchive.IndexesNotMade` names the old database's other
  indexes; sign-in links put in place by `_id`, without the ones ending within 15 minutes), `Recheck` (the last look
  before marking). `LeftOut` says what stays behind.
- `internal/store/movemarker.go` — the old database's marker (`database_move`): `moving`, `moved`, `abandoned`;
  `Holding`, `Switched` and `busy` (changes running) per computer; every time in it is the server's (`$currentDate`,
  compared with `$$NOW`). `Migrate` refuses a frozen database.
- `internal/store/guard.go` — the freeze in the database connection itself: `DB.C` returns a `Coll` whose writes check
  the marker (looked at by a watcher `app.OpenWith` starts, trusted for `MoveLookTrusted`); `Admit` counts the changes
  running and publishes them in the marker; only the move's own marker writes and a released job lease pass.
- `internal/ops/databasemove.go`, `databasemove_state.go` — the operations, the freeze check in `dispatch`
  (`refuseWhileMoving`, exempting the move's own), the address kept for the move, the switch of this computer's
  settings, each move's record (`<state>/database-move/<id>.json`) and the dashboard's one-time grant.
- `internal/app` — `MoveMarker` and `Held` (from the connection's look at the marker): a held process writes no audit,
  touches no installation and its activity log waits in the spool (`logbook.Options.Hold`, asked at every write). Each
  installation's record says its build knows about moves (`knowsMoves`, `MovesMark`); the plan compares the others with
  `ops.FirstMoveVersion`.
- `internal/cli/daemon.go` — `watchForMove` and `serveHeld` (`api.NewHeld`: reads only, `heldGuard` refuses other
  changes); the terminal worker stops.
- Tests: `go test ./internal/dbmove/ ./internal/ops/ -run 'TestDatabaseMove|TestCopy'`, `./internal/cli -run
  TestDatabaseMove` (with a second computer's home; into this computer's own database with a real `mongod`), and the
  browser run `scripts/database-move-e2e.sh` (ports 5690–5699).
