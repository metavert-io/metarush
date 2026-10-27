# Presenters

A **presenter** reads your lines on camera. It speaks MetaRush's own sound — a person's voice copy, a recording of
them, an AI voice, or a narration line of your cut — so the voice is the one the person agreed to, and the service
only moves the lips. A presenter is either **a person's own** (yours, a colleague's, a client's spokesperson's),
made at an AI service from their approved look, a short training take of them and their consent recorded on camera,
or **a stock presenter** a service licenses for commercial use and client work. Every clip is priced first, lands in
Sources as an ordinary clip marked as an AI presenter, and is disclosed everywhere generated footage is.

The services are **D-ID** for a person's own presenter (an "Express" presenter made from a training take; clips of it
reading uploaded sound) and **HeyGen** for stock presenters (its studio presenters reading uploaded sound; a person's
own presenter at HeyGen isn't built: there the person records HeyGen's consent on HeyGen's own page). Both APIs were
read on 25 October 2026 (HeyGen's v3; its v1 and v2 retire on 31 October) and MetaRush's tests run against fakes of
them (to check against the real services, see [Live check](#live-check)).

## A person's own presenter, step by step
Every step is a person's own: Claude, agents and MCP clients can list and read presenters, never begin, make, test,
approve or retire one.

1. **Their approved look** (People → the person → in generated video): stills and clips of their confirmed faces,
   approved after its test sheet. A presenter is made from one approved look and pinned to its version.
2. **Begin the presenter** (Generate → Presenter → Begin a person's own presenter, or `metarush presenter start
   --person "Ada Lovelace" [--green-screen]`). MetaRush asks the service for its **consent words** — D-ID's are a
   sentence and a few random words, with a place for the person's name — and shows them with the person's name put
   in. They last 30 minutes; **New words** (`presenter start --again ada`) asks for fresh ones.
3. **One take of consent, filmed.** Film the person saying, in their own words, that they agree to a presenter of
   them kept at that service, then reading the service's words. A recording of sound only doesn't cover a presenter.
   Name their voice and their face in it yourself (MetaRush asks once it has listened and looked): only names a
   person gives count — a name an AI app, Auto or Claude gave doesn't, and the estimate says who named them. Record it as their consent
   on the People page, covering **a presenter** and **naming the service** (`metarush consent record --person "Ada Lovelace" --self --recording <clip>
   --adult --covers shots,voice,presenters --service d-id --statement "…"`). One consent can cover generated shots,
   the presenter and the voice copy, when the recording says so.
4. **A training take**: at least a minute of the person talking to the camera on their own (two or three is
   better), well lit; against a green screen when you want a colour behind the presenter.
5. **Make it** (the presenter's **Make the presenter**, or `metarush presenter make ada --take <clip>
   --reading-from 31 --reading-to 39`): priced first. Before anything is sent MetaRush checks: the consent is in
   force for the project, covers a presenter, names the service and is filmed; in the consent recording the person's
   voice is named as them and every face on screen is named as them; the reading is part of that same recording
   (`--reading` defaults to it; another recording is refused) and has the person reading the service's words, most of
   them in order (the consent script check, against MetaRush's own transcript); the training take is the person alone (their
   voice, and every face named as them once the faces index has looked — a face that looks like someone else refuses;
   only where it hasn't looked yet do you say it shows them, `--shows-person`); the look is still approved and
   unchanged; the service's training opt-out is recorded where it needs one (below); children and public figures are
   refused. A second make can't start while one is on its way: the presenter is claimed first. D-ID then checks the reading against
   its own words and makes the presenter in five to ten minutes. It comes back as a **draft**.
6. **Test it** (choose it in the Presenter form: the button says *Make a test clip*, or `metarush presenter test ada
   --clip <clip>`): a priced clip reading a line. Watch it.
7. **Approve it** (`metarush presenter approve ada`). From then on clips can be made with it.

**Retire** (`metarush presenter retire ada`, or the presenter's Retire with a confirm) — or withdraw the consent it
relies on, or let the consent's last day pass — at midnight in the time zone recorded with it — or its record go
(the daemon's sweep notices; a consent it couldn't read just now retires nothing) — and nothing more is made with it: it
and the service's consent record are deleted at the service at once when this computer has the key, else by a job
any computer with the key takes, tried until it is done; clips waiting to be made with it are stopped, and checked
again just before sending; a clip already being made isn't kept and is deleted at the service; MetaRush lists the cuts that use its clips (they stay, marked as made with consent
since withdrawn) and shows the service's own route for erasing what it keeps of a face — a step only the person can
take. A presenter whose answer was lost on the way back is looked up at the service by MetaRush's tag in its
name and deleted.

## Clips
Generate → **Presenter** (or `metarush ai make presenter --presenter ada --clip <clip>`, `--line 3` for a narration
line of the cut, `--background green` for a presenter filmed against a green screen): who reads it, what it reads,
the price on the button. The clip lands in Sources as an ordinary clip: the service's picture with **MetaRush's own
sound under it** (the service may resample what it was sent), marked as an AI presenter, with how it was made. It is
placed like any generated shot — on the timeline, in the cut sheet, by Claude through a card. MetaRush counts D-ID's
clips in its 15-second steps, as D-ID does.

- **A person's presenter speaks only their own voice**: their voice copy, a recording in which every voice heard is
  named as them, or an AI voice that is no one's — never another real person's voice copy or recording, nor a dub of
  one (a dub carries the voices of what it was made from). A voice nobody named, or one MetaRush only recognized, is
  refused until a person names it.
- **An answer lost on the way back** (the service answered in a way MetaRush can't read, or not at all) is never sent
  again: the clip may have been made and charged, so MetaRush deletes the sound it uploaded and looks for the clip at
  the service by the tag in its name — again every two minutes for ten minutes — and deletes it when found; else the
  record says it wasn't found.
- **Dubbing**: dub a presenter clip as usual (Generate → Dub), then make a presenter clip of the dub: the same
  presenter reads the dubbed sound, so the lips follow the new language. A service's own video translation stays off:
  it would copy the person's voice at the service.
- **Colour behind**: D-ID puts a colour behind a presenter only when its training take was filmed against a green
  screen; choose green to key it out later. D-ID's scene reference names no transparent background.
- **In Claude's panel** a presenter clip is a priced card, paid from the conversation's project; with Auto it runs by
  itself only where a person set the project's budget and the team's daily limit.

## Stock presenters
Offered only where the service's licence covers **commercial use and client work**, as catalogue data per service
(`presenters.stock`: commercial, clientWork, ads, with the day it was read). `metarush presenter stock` lists them;
a clip with one is `--presenter stock:<id>`. Whether the licence covers ads is said with the price and travels with
the clip into delivery notes. D-ID's API pages don't say which plans license its stock presenters, so none are
offered. HeyGen says its stock presenters are cleared for commercial use, ads, marketing, social media and client
work on its eligible paid plans (without saying which plans those are): they are offered, priced at $0.05 a second
(Avatar IV), and the estimate says the licence covers ads only on its paid plans. MetaRush uploads the sound and asks
for each clip with idempotency keys made from its tag, so a request Go's client sends again by itself gets the same
upload and the same video, never a second one.

## Training on what is sent
Where a service's terms say it trains its models on what it is sent **unless the account opts out**, a person's
presenter is made there only once a person has recorded that the opt-out is set for the account whose key this
computer uses (`metarush presenter opt-out <service> --how "wrote to its privacy team on 25 Oct"`; `--remove` takes
it back; without either it says where things stand). A service that trains with no way out is refused. D-ID says it
doesn't look at what its API stores, training included, so it needs none. HeyGen's privacy policy lets it train on what
it is sent unless the account writes to opt out. The rule covers **every named person whose face or voice goes
there**: a person's presenter, a recording in which a named person speaks, their voice copy, a dub of them. Each
needs the opt-out recorded for the key and that person's consent in force naming the service; the refusal says whose
voice would go where and why. The opt-out is checked again on the computer with the key just before sending.

## Content Settings
The project's Settings tab → Content Settings → AI services → **No presenters** keeps every presenter, a person's own
or a stock one, away from that client's work (`metarush ai rules set --project <id> --forbid-kind presenter`). Like
every AI services rule, it is checked again just before anything is sent. **What may leave** counts a
presenter's work too: making a person's presenter sends footage, their face and their voice; a clip of a person's own
presenter sends the sound it reads and makes their face speak there (faces); a stock presenter's clip sends the sound.

## Disclosure
Presenter clips are generated clips: the AI badge and the clip's line ("An AI presenter made with D-ID · Ada
Lovelace"), QA's note ("an AI presenter reading lines on camera … never the person speaking live"), the delivery
sidecar (who reads, the consent a person's presenter relies on, the watermark: none documented) with a notice for a
presenter made from a real person's likeness, and the receipt (presenter clips, and the presenter itself: kept at the
service while in use, deleted when retired).

## Keys and money
The D-ID key is per computer (Settings → Integrations → Presenters, or `metarush providers set d-id`; it is checked
by reading the account's credits). It goes only in the Authorization header, only to `api.d-id.com`;
`METARUSH_D_ID_BASE_URL` takes only that host or a test server on this computer, and only the latter under `go test`
and in browser runs. Clips D-ID makes are downloaded from the link it gives, without the key. Presenters are paid from
the same project budget and team daily limit as all AI work.

D-ID downloads the training take and the consent reading itself, from a link (its API has no upload for video):
MetaRush gives it a two-hour link to the stored bytes, which needs media kept in cloud storage that makes links
(Settings → Storage). With media on this computer, a D-ID presenter can't be made (clips can: their sound is
uploaded).

D-ID sells minutes in monthly plans and its own pages don't show their prices: the catalogue's $1.11 a minute is the
cheapest plan for commercial use divided by its minutes, as others list it on 25 October 2026. Check your plan.

## Commands
```bash
metarush presenter list [--person <name>] [--retired]  ·  presenter show <presenter> [--details]
metarush presenter start --person <name> [--look <look>] [--green-screen] [--model d-id/express]   # people only
metarush presenter start --again <presenter>                                   # new consent words (people only)
metarush presenter make <presenter> --take <clip> [--take-from --take-to] --reading <clip> [--reading-from --reading-to] [--shows-person] [--max-cost]
metarush presenter test <presenter> --clip <clip> [--max-cost]                  # people only, priced first
metarush presenter approve <presenter>  ·  presenter retire <presenter> [--note]   # people only
metarush presenter stock [--service heygen]  ·  presenter opt-out <service> [--how "…" | --remove]
metarush ai make presenter --presenter <presenter|stock:<id>> --clip <clip> | --line <n> [--background green] [--max-cost]
metarush consent record … --covers shots,voice,presenters --service d-id         # people only
metarush ai rules set --project <id> --forbid-kind presenter                     # people only
```
Every command takes `--json`.

## For developers
- Kinds `presenter` (task `read`) and `presenter-make` (task `train`) in `internal/aiservices`; a presenter-make
  adapter is also an `aiservices.PresenterMaker` (consent words, delete, find by tag); `LinkTaker` names the inputs a
  service downloads itself (`Media.URL`); `StockLister` lists stock presenters. The catalogue's provider entry has
  `presenters` (consent mode, take advice, backgrounds, deletion, erasure route, stock licence) and
  `data.trainsOnUploads`; prices can count in steps (`roundSeconds`).
- `internal/aiservices/heygen` follows HeyGen's v3 reference (stock presenters only; `X-Api-Key`, assets, videos with
  an `Idempotency-Key`, deletes); `internal/testutil/fakeheygen` (and its command) is its fake;
  `METARUSH_HEYGEN_BASE_URL` is guarded like D-ID's.
- `internal/aiservices/did` follows D-ID's reference; `internal/testutil/fakedid` (and its command) is its fake;
  `aiconformance.Run` covers both kinds (the presenter's life: consent words, made, found by its tag, deleted; a
  finished presenter outlasts a late cancel). `fakeai` has both kinds too (`fakeai.Options`: a service that trains,
  one with no consent check of its own, stock with an unstated licence).
- `internal/aiwork/presenters.go` (start, make, test, approve, views), `presenter_clip.go` (clips, a narration line,
  MetaRush's own sound under the picture), `presenter_checks.go` (whose voice a sound carries, faces named as the
  person, the training gate, the checks just before sending and when a clip comes back, a lost clip looked up by its
  tag, consents that ended), `presenter_retire.go` (retiring, the `remove-presenter` job, the consent hook, the
  sweep, opt-outs, stock, links). Records: `presenters` (`prs_`) and `training_optouts`
  (migration 51); the making is a generation of kind `presenter-make`, each clip one of kind `presenter`.
- Tests: `go test ./internal/aiservices/... ./internal/aiwork/ -run 'Presenter|Stock|TrainingOptOut|DIDThrough|HeyGen|NoPresenters|TakeShows|EndedConsent'`;
  browser run `scripts/presenters-e2e.sh` (ports 5510–5512; the fake D-ID; `scripts/presenterseed` seeds the steps a
  person on camera takes).

## Live check
A check against the real services costs money and needs D-ID and HeyGen accounts. For HeyGen: set the key
(`metarush providers set heygen`), list the stock presenters (`metarush presenter stock --service heygen`), make one
10-second clip with `--max-cost`, and check the clip, its cost on your balance and that the video and the sound are
gone afterwards. For D-ID, on a computer whose media is in cloud storage that makes links: set the key (`metarush
providers set d-id`), begin a presenter of yourself, record the consent take and a two-minute training take, make it
with `--max-cost`, test a 10-second line, approve, retire, and compare what came back with this page — in particular whether D-ID accepts a link whose path doesn't end
in `.mp4` (MetaRush's link carries the file's name, `footage.mp4`, in its query), whether its consent text has a place for the name,
and what a clip and a presenter cost on your plan.
