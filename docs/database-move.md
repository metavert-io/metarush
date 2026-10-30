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
address is kept aside (`mongodb-uri-previous`, or `database/previous.secret`). The computer that moved also keeps the
old database's address for its looks at it afterwards (below) — `database-late-<move>` in the keychain, or
`database/late-<move>.secret` — only until the last look, then drops it.

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
  runs, invited and not joined yet, or seen in the activity log running an older MetaRush after a newer one — **even
  when it's off**: started after the move, it would go on changing the old database. Update MetaRush there and open it
  once, or remove that computer from the workspace (`metarush machine revoke "<name>"`), first. (Every MetaRush
  process records the build it runs before its first change, daemon or not);
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
   through, and when the marker can't be read during a move nothing changes. Changes that were already running finish
   first: each says so in the marker before it writes, the move waits for them, and once the copy starts none of them
   writes again (a laptop that slept through the wait and wakes up is refused). MetaRush running in the background on each computer starts again **holding**: the dashboard
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

## Something written late

Closing the old database for good at the very moment it is marked moved would take a transaction around every
change MetaRush makes. Without one there is a small gap: a change a computer sent just before it went to sleep for
more than about 15 seconds (longer than the move waits for a running change to say it still runs) can still land in
the old database after the move marked it moved — and nobody would ever see it there. So the computer that moved the database looks at the old
one again and says what it finds:

- **when**: right after it switched (the move itself), then about **15 minutes, an hour and a day** after the move
  (MetaRush running in the background on that computer; a computer asleep at those times looks when it wakes), and
  whenever an owner asks (`metarush database late-writes`, or *Look again now* in the card). One look runs at a time. A
  look that can't reach the old database is tried again every ten minutes; a day after the last look was due, the looks
  end anyway.
- **what**: every kind of record, compared with fingerprints of every record that the move's last look took under the
  freeze (kept on the computer that moved, in its `state/database-move` folder, until the last look): records
  **added** there since, **changed** and **removed**. Sign-in links, which end by themselves, are never reported as
  removed. **A look only reads the old database**: it never writes to it.
- **how much it reads**: first the old database server's own hash of each kind of record (the server does the reading;
  only the answer travels). A kind whose hash is as it was isn't read at all; one that changed is read over the
  network, in order, beside its fingerprints, one record at a time. **MongoDB Atlas's Free and Flex clusters don't offer
  that hash** (the plan says so when yours doesn't): then every look reads the whole old database over the network. So
  a look keeps to bounds: one MetaRush makes by itself reads at most 256 MB, for at most 5 minutes, with a short pause
  every 1,000 records; past either it stops and says so (*"stopped part-way"*), settles nothing, and isn't tried again.
  One you ask for reads up to 16 GB, for up to 2 hours. When the last look stopped part-way, MetaRush keeps the address
  one more day, so you can look at everything yourself.
- **what you see**: nothing at all when nothing was found. Otherwise owners' bells ring once (refreshed as more is
  found): which kinds of record were added, changed or removed, about when (as the record says), and on which computer
  when the record says. Settings → Storage → Move the database lists them, numbered; `metarush database late-writes`
  too (`--skip`, `--limit` for a long list), and `move-status` and the card say when the next look is. The records of
  one clip's transcript, its first transcript, what was learned from it, its voices or its faces, and one batch of
  transcript fixes, are one entry. One look keeps up to 2,000 records it finds for the first time (20,000 for one
  move): it says how many more it found, and each later look goes on past those it already keeps.
- **what stops being true settles by itself**: a look that saw everything lets go of what it found before and no longer
  finds (a record back as it was at the move, a new one removed again): nothing to carry over any more, and its copy is
  gone. Only the version the latest look saw carries over.
- **deciding** (owners, and a person: never Auto, Claude, an app connected through MCP or a draft on the review desk;
  in a terminal, a person at their own): *Carry it over* puts the old database's version into this database — only
  while this database's own record is still exactly the one the move copied (checked and set in one step). When it
  changed here too, or another record took its place, nothing changes: both versions are shown side by side (*Compare*,
  `late-writes show <n>`) and you decide. An entry of several records goes over whole or not at all, and not while the
  last look left some unlisted. *Leave it* keeps this database's own. A record **removed** from the old database is
  never removed here by MetaRush: if it should go, remove it here yourself, then leave it. Each decision is in the
  change history and the activity log.

```bash
metarush database late-writes            # look again now (on the computer that moved, while it keeps the address) and list
metarush database late-writes show 2     # the old database's version beside this database's
metarush database late-writes carry 2    # carry it over (asks first; --yes)
metarush database late-writes leave --all
```

### Passwords, sign-in links and other secrets

Kinds of record that hold sign-in secrets — passwords, invitation and reset links, an upload's address in the storage —
and every kind a workspace export leaves out or MetaRush doesn't know are **only reported**. MetaRush keeps no copy of
them and not even their IDs, never shows them, and never carries them over: it says what changed and what to do
(*"A password was changed in the old database after the move… whoever set or changed theirs then sets it again here"*).

For every other kind, *Compare* and `late-writes show` leave out what MetaRush never shows anywhere (a review link's key,
where a file is kept, voice and face prints, claims on work, what a conversation keeps for Claude: `models.NeverShown`,
listed as *Left out*) and take out anything shaped like a key, a password in an address or a sign-in token, as a
workspace export does.

### How long copies stay

To carry a record over once the old database's address is gone, MetaRush keeps a copy of its old version in this
database. A copy goes as soon as a person decided or the finding settled. One nobody decided on is **let go after 30
days**: the finding stays in the list, and says it can't be carried over any more. (Never while a restored copy's
background deletions are paused.) A workspace export leaves those copies out; a later move of the database carries the
ones still waiting.

### The old database's address

After the last look MetaRush drops the old database's address and the fingerprints. If the computer that moved never
runs MetaRush again, or its own switch failed and was never done again, nothing drops them. To remove them by hand on that
computer:

```bash
ls <home>/state/database-move/*.prints              # the moves still holding fingerprints: <move>.prints
rm <home>/state/database-move/<move>.prints
rm <home>/database/late-<move>.secret               # a home that keeps its keys in files
```

In a home that uses the keychain, delete the item whose account contains `database-late-<move>` (service `MetaRush`), in
Keychain Access on a Mac. The copy kept aside as `mongodb-uri-previous` is separate, and MetaRush never drops it by itself
(see above).

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
| `metarush database late-writes [--no-look] [--skip n] [--limit n]` | Look at the old database again for anything written there after the move (on the computer that moved), and list what was found (owners) |
| `metarush database late-writes show\|carry\|leave <n>… [--all]` | Compare both versions of one / carry it over (only while this database's own is the one the move copied) / leave it (owners, a person at a terminal) |

Every command takes `--json` and `--details`. The operations are `database.plan`, `database.move`, `database.switch`,
`database.abandon`, `database.move.status`, `database.late.writes`, `database.late.show`, `database.late.carry` and
`database.late.leave`; MCP's admin toolset offers the plan, the status and the list of what was written late.

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
  compared with `$$NOW`), and `copying` once the move stopped waiting (`MoveCopying`, set in one step only while no
  running change is listed). `Migrate` refuses a frozen database.
- `internal/store/guard.go` — the freeze in the database connection itself: `DB.C` returns a `Coll` whose writes check
  the marker (`MoveMarkerForChange`: a look at most `MoveLookTrusted` old, every connection, watched or not; unreadable →
  refused unless a recent look showed no move); `Admit` gives a running change a pass that ends with it (work detached
  from it gets none) and counts it; a pass writes during a move only after `listPassage` listed its process in `busy`,
  in one step refused once `copying`; never once moved. `OnFirstWrite` runs `app.sayBuild` before a connection's first
  write.
- `internal/jobs/queue.go` — leases end by the server's time (`$$NOW` + `LeaseDuration` in pipeline updates, compared
  with `$$NOW`), so `store.LiveLease` agrees whichever computer wrote them.
- `internal/ops/databasemove.go`, `databasemove_state.go` — the operations, the freeze check in `dispatch`
  (`refuseWhileMoving`, exempting the move's own), the address kept for the move, the switch of this computer's
  settings, each move's record (`<state>/database-move/<id>.json`) and the dashboard's one-time grant.
- `internal/app` — `MoveMarker` and `Held` (from the connection's look at the marker): a held process writes no audit,
  touches no installation and its activity log waits in the spool (`logbook.Options.Hold`, asked at every write). Each
  installation's record says its build knows about moves (`knowsMoves`, `MovesMark`, written by every process before
  its first write); the plan compares the others with `ops.FirstMoveVersion` and reads the activity log for an older
  build after the record's (`olderBuildLogged`).
- `internal/cli/daemon.go` — `watchForMove` and `serveHeld` (`api.NewHeld`: reads only, `heldGuard` refuses other
  changes); the terminal worker stops.
- `internal/dbmove/late.go`, `internal/ops/databasemove_late.go` — something written late: `RecheckPrinting` takes
  every record's fingerprint (`_id` and the first 16 bytes of the SHA-256 of its export line, per kind the server's
  `dbHash`, in a gzip'd file `<state>/database-move/<move>.prints`) in the same read as the last look; `Look` reads only
  the old database (`dbHash` per kind; a changed kind streamed in `_id` order, one class of `_id` type at a time, and
  merged with its fingerprints, holding one record at a time; `LookOptions` `Each`, `MaxBytes`, `MaxTime`, `Pace`); the
  old database's address for the looks (`keepLookAddress`: keychain `database-late-<move>` or
  `database/late-<move>.secret`), dropped with the fingerprints after the last look that saw everything
  (`endLateLooks`). Records: `late_looks` (per move: due times by the moving computer's clock, looks taken, `more`,
  `partial`, the one-look-at-a-time `lookingUntil`) and `late_writes` (`lwr_…`, per record found: the old version up to
  8 MB while it waits, its fingerprint and the move's, `group`); `wsarchive.ReportOnly` (the catalogue: left out, marked
  `Secret`, or unknown) keeps no copy or ID; `models.NeverShown` (a test keeps it in step with `json:"-"`) and the
  export's scrub for what `show` returns; `RunLateLooks` in the daemon, with `DropOldLateCopies` (`LateCopyKeep`,
  `DeletionsPaused`); a look that saw everything settles what it didn't find (`seenBy`); carry-over: claim on the
  fingerprint, then check and set (`$expr: {$eq: ["$$ROOT", {$literal: <the record read>}]}`).
- Tests: `go test ./internal/dbmove/ ./internal/ops/ -run 'TestDatabaseMove|TestCopy|TestLook|TestLate'`, `./internal/cli -run
  TestDatabaseMove` (with a second computer's home; into this computer's own database with a real `mongod`), and the
  browser run `scripts/database-move-e2e.sh` (ports 5690–5699).
