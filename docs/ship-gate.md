# Before it goes out: the ship gate and promises made to the client

A version can't go out to a client while the quality check found problems in it, or a sign-off is still waiting —
unless a person sends it out anyway with a note, which is kept and shows on the receipt. What was
promised to the client ("captions on every video", "under 60 seconds for shorts") is written down as a promise, and
every quality check measures it. One answer serves every surface.

## What holds a version

`delivery.ready` (`metarush deliver ready <project> [--version rN] [--json]`, the version page's **Ready to
deliver** / **Held by …**) looks at what would go out — the newest final render of each shape (`--quality review`
for review copies, `--preset` for some shapes) — and lists what holds it:

| Hold | Says | One step | Sent out anyway? |
|---|---|---|---|
| `approval` | "Version 5 isn't approved yet." | Approve it (a person) | no: delivery's own rule (`--allow-draft`) |
| `render` | "Version 5 has no final render yet." | Make the final render | no: nothing to send |
| `qa-missing` | "The quality check hasn't looked at its final render yet." | Check it (`qa.run`) | yes |
| `qa-running` | "The quality check of its final render isn't finished yet." | See how far it is | yes |
| `qa-failed` | "The quality check of its final render couldn't finish." | Check it again | yes |
| `qa-stale` | "… is out of date: it was checked before the promise “…”" (or its description changed, for a disclosure promise) | Check it again (`qa.run --force`) | yes |
| `qa-errors` | "The quality check found 2 problems in its final render." with each problem, its time and its fix | Fix it (the problem's own change, saved as a new version), See the problems, Check it again | yes |
| `signoff` | "Still waiting for the sign-off “Lena OKs the final version”." (house ones say whose settings) | Tick it (a person) | yes |
| `consent` | "At 0:02 Ada Lovelace says “fifteen” (words changed with AI in their voice copy), but her consent … is no longer in force …" (or a mouth matched to them) | Take them out (`cut.say.undo`, a new version) | no: it holds with the gate off too |

Things worth checking (warnings) never hold. `--json` gives every hold with its stable `key`, `kind`, `says`,
`problems` (message, time, fix, the fix's edit ops) and `paths` (the op and input the dashboard runs, the command),
the renders and their checks, the promises as measured, and every note that sent the version out anyway.

## Where it holds

- **Delivery** (`deliver.export`, `metarush deliver`): a held version is refused with `E_HELD` (exit code 4), the
  holds in plain words, `details.held` and the command `metarush deliver ready …`. Nothing is written.
- **Timelines exported into a delivery folder** (`export.timeline`): a folder that already holds a delivered video's
  record (a `.json` with `"format": "metarush.delivery/1"`), any folder up to three levels inside one, and every folder
  delivery wrote into on this computer is a client's delivery folder, so a timeline written there (or its footage folder)
  waits the same way. The home folder, the top of a disk or volume and a sync folder's root are never one, and the
  search upward stops there. The folders are remembered in `<home>/state/private/delivery-folders.json` (a 0700 folder,
  a 0600 file, refused inside a sync folder or a repository that would commit it); deleting it only makes the gate
  forget them — the check of the version's final renders, else its review copies; a
  version never rendered hasn't been checked. A timeline for your own editing app goes anywhere else.
- **Share for review** asks `ops.ShipGate` the same way ([outside-reviewers.md](outside-reviewers.md)).

## Whether it is on

- **The workspace** (owners; Settings → General → **Before it goes out**, `metarush deliver gate on|off`,
  `delivery.gate.set`): new workspaces start with it on. A workspace made before the gate stays off — reminders only,
  as before — until an owner answers the one-time question on the To-dos page ("Should versions wait for the quality
  check and their sign-offs before they go out to a client?"), so nothing changes silently.
- **A client's Content Settings** (owners; the project's Settings tab → **Before it goes out**, `metarush content gate
  on|off|workspace [--project]`, `content.gate.set`): their projects always wait, never wait, or follow the workspace.
  It's a Content Settings part, so projects following those settings follow it too.
- Turning it off is a person's (`E_HUMAN_ONLY` otherwise), and so is setting the one-time question aside — an
  owner's, which keeps reminders only. With it off, delivery still writes what would have held the version into its
  record, and the sign-off reminders work as before.
- Moving a project to other settings — its own empty ones, another project's, or making another project's the
  default — that would drop a gate that's on, a promise or a house sign-off is a person's who is an owner (copying the
  settings it uses keeps them all, so anyone may).
- **Older computers:** a computer in the workspace on an older MetaRush may deliver without waiting. While the gate is
  on, the version page, `metarush deliver ready` and Settings → General say how many there are; update them. A
  version held by changed words' consent (`consent`) says so with the gate off too: an older build doesn't check it.

## Sending it out anyway

Only a person: **Deliver anyway…** on the version page, or `metarush deliver override <project> --version rN
--note "…"` in their own terminal (it lists what holds the version and asks them to type `send`). The note (3–500
letters) names the holds the person saw (`holds`: their keys); if something else holds the version by then, it is
refused (`E_CONFLICT`) and the person looks again. It is kept on the version for good (`Revision.shipOverrides`: who,
when, the note, the holds it covered; never capped) and covers only those holds — a sign-off added later, or a new
check that finds something, holds it again. Delivery then writes, for every note that covers something holding it:

- `gate` in each video's record: on, why, what held it, `sentAnyway` (the note with who and when) and the promises as
  measured;
- a `## Sent out anyway` section in its `.notes.md` (written even when the version has no delivery notes);
- a line on the project's receipt (`sentAnyway`, [receipt.md](receipt.md)), also in the record's receipt lines: "was
  allowed to go out" until it actually goes out with that note (`Revision.sentOut`), then "went out".

Auto, agents and Claude never send a version out anyway: the operation is people only, refused to Claude in the panel,
over MCP and to scripts, and the review desk refuses it as a draft. While the gate holds a project, ticking or removing
a sign-off is a person's too, and taking back a promise always is.

## Promises made to the client

A promise is a rule or house rule marked `promise` (`metarush project rules add <p> "<words>" --promise`, `metarush
content rules add "<words>" --promise`, "It's a promise to the client" under the rules, or a keep card). It must be
measurable — what MetaRush can't measure stays a sign-off — and the same words as an existing rule make that rule a
promise. Every quality check (`qa-7`) measures each one: what it measures in a sentence ("The video is at most a minute
when it is vertical.") and what it found ("It lasts 1:12, 12 seconds longer than a minute."). A broken promise is a
problem, so the version waits; one MetaRush can't measure here (faces, without a Mac's on-screen index) is worth
checking and never holds — but then the page says "Ready, but 1 promise couldn't be checked here", not just ready.

| Promise | Measure | How it is measured |
|---|---|---|
| "Under 60 seconds for shorts" | `max-length:60@vertical` | the decoded length, within a frame; other shapes: doesn't apply |
| "Captions on every video" (also "burned-in" or "open" captions: MetaRush's own, drawn into the video) | `captions` | the cut's own captions over at least 90% of the speech, letters at least 3.5% of the picture's shorter side, no line faster than 25 letters a second |
| "The footage already has its captions" | `captions:burned-in` | the cut's own captions as above, or the footage's own captions read low in the picture over 90% of the speech by the on-screen index; when nothing could read them it couldn't be measured (never kept) |
| "Logo in the last 3 s" | `logo-end:3` | a graphic showing a logo (or an end card) through the last 3 seconds |
| "Logo in the first 5 seconds" | `logo-start:5` | a graphic showing a logo within the first 5 seconds |
| "No music louder than speech" | `speech-over-music:0` | wherever someone speaks over music, the voices' level against the music's, from each recording's measured loudness and its gain in the mix |
| "Nothing generated without disclosure" | `disclosed` | every generated clip (cuts played in this one included), AI voice and AI picture is told about by a graphic saying it was made with AI while it plays, or by MetaRush's line in the version's description, written from the records: `Made with AI: the shot “Beach at dusk” (Google Gemini).` (the check's fix gives it; a stray "generated" or an out-of-date line doesn't count) |
| "No faces cut off" | `faces-in-frame` | the on-screen check's faces cut off (qa-5), kept only when every second shown was looked at; cuts played in this one, calls and footage not indexed are named and leave it unmeasured |
| "At least 1080p", "4K" | `min-size:1080` | the picture's shorter side (a review copy: the cut's own size) |
| "30 fps" | `min-fps:30` | the decoded frame rate |

The older measures (`captions-top`, `first-names`, `no-flashes`, `shape:`) can be promises too. The promises show on
the version page and in `metarush deliver ready` as the latest check measured them; each broken one carries its fix
where there is one (Fix it saves it as a new version).

**From a conversation**: when the person says what was promised, Claude runs `metarush keep promise "<words>"`
(or `project rules add … --promise`), which becomes a "Keep this as a promise to the client?" card that asks where —
just this project, or every project using its settings. Nothing is kept until a person keeps it; Claude's context then
lists the promises apart.

## Tests

`internal/measures` (words → measures), `internal/qa` `TestPromisesGolden` (`testdata/promises.golden.json`:
`UPDATE_GOLDEN=1` rewrites it), `internal/ops` `TestShipGate`, `internal/receipt`
`TestReceiptSaysWhatWentOutAnyway`, `internal/keep` `TestKeepPromise`, `internal/assistant` `TestPromiseKeepCards`,
and the browser run `scripts/ship-gate-e2e.sh` (ports 5560–5569, fakes only).
