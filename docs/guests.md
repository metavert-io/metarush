# Guests

A guest is someone outside the workspace — a client approving a cut, a lawyer checking a version, a producer at an
agency — that an owner shares a project, or some of its versions, with. Guests watch the versions' review copies on
MetaRush's **guest page** and, when they may, leave comments pinned to times that come back to the version. They
never see the dashboard, the footage library, settings, keys or anything else of the workspace.

> **Nothing is reachable from the internet yet.** In this release the guest address is this computer's own
> (`http://127.0.0.1:4846` unless you choose another port), so a guest can open it only on the computer that runs
> MetaRush. Use it to try the guest page and to get guests and what they may see set up; a later release adds a
> public address for guests elsewhere. Until then, to send a version to someone on another computer, use
> [Share for review](outside-reviewers.md).

## What a guest can and can't do

A guest is an email with **grants**. Each grant names:

- **what they see** — a project (every version of it that has a review copy, now and later) or some of its versions;
- **what they may do** — *watch* (a viewer) or *watch and comment* (a commenter);
- **until when** — a number of days or a last day, required, at most 90 days at once (share again for more).

A guest can:

- see what was shared with them: the projects and versions, until when, who shared it and their note;
- watch a version's review copy (the newest in the version's own shape);
- as a commenter, leave comments pinned to a time (or for the whole video), and read their own.

A guest can't:

- open the dashboard, the Library, settings, the members' sign-in or any member operation — the guest page is
  served by a server of its own that has none of them;
- see a version their grants don't name, another project's, one deleted since, a draft an AI app made that nobody
  approved, or one whose project was archived — every one of them gets the same answer, "That isn't shared with you";
- see other guests' comments, or members' comments;
- approve a version or upload anything (approvals by guests come later, behind a grant of their own);
- keep going once something is taken back: taking back a grant, everything shared with a guest, or turning guests
  off counts on their very next request; a video playing from this computer stops within seconds, one playing from a
  cloud bucket within two minutes (see below).

A version that plays someone else's video (a reference), anything made with a consent that was withdrawn, or words
changed in someone's voice whose consent is gone is never shown to guests — even where someone sent it out anyway
with a note. When MetaRush can't check that (a lookup that didn't answer, or more nested cuts than it looks
through), the version isn't shown either. What may leave decides too: a client's Content Settings can keep their things from **guests**
(a place of its own), and a guest then can't open their versions — checked when you share and again whenever a guest
opens a version ([what may leave](what-may-leave.md)).

## Turning guests on

Owners turn guests on, at the computer that runs MetaRush: **Settings → People → Guests → Let guests in: On**, or

```bash
metarush guests on [--port 4847]     # let guests in; this computer answers them on 127.0.0.1
metarush guests status               # are guests on, where they are answered, how they sign in
metarush guests set --port 4847      # move where this computer answers them
metarush guests off                  # every guest is signed out at once; unused sign-in links stop working
```

Turning guests on starts this computer's **guest listener**: a second web server on 127.0.0.1 only (never another
address of this computer), at port 4846 unless you choose another. Turning guests off signs every guest out at once;
what you shared stays, and works again when you turn guests back on.

## Sharing with a guest

On a version's page, **Share with a guest…** (beside Share for review…) asks for their email, what to call them, what
they see (this version, or every version of the project with a review copy), whether they may comment, for how long
and a note. Before anything is shared it says what the guest will see and whether What may leave lets it go. Then
**Make a sign-in link** gives a link to send them yourself (nothing is emailed). The version's page lists the guests
who can see it, with **Sign-in link…** and **Take back**; Settings → People → Guests lists who has what and what
guests did.

From a terminal (a person typing; Claude, AI apps and scripts are refused):

```bash
metarush guests share "Spring promo" --email dana@example.com --name "Dana" --role commenter --days 14 [--version r3]
metarush guests share "Spring promo" --email dana@example.com --role viewer --until 2026-11-30 --dry-run
metarush guests link dana@example.com          # a one-time sign-in link to send them
metarush guests list ["Spring promo"] [--all]  # who has what (ended and taken back too with --all)
metarush guests revoke dana@example.com        # take back everything shared with them
metarush guests revoke --grant <id>            # take back one thing
metarush guests activity [--guest dana@example.com] [--refused]
metarush guests view dana@example.com          # what a guest sees (owners)
metarush guests comments dana@example.com --version <id>
```

Every command takes `--json`. Sharing, sign-in links, taking things back, turning guests on or off and reading what
guests did are an owner's; everyone signed in can see whether guests are on and who has what.

## How guests sign in

There are no passwords for guests. A guest signs in one of two ways:

- **A one-time sign-in link** you send them. It opens the guest page at the guest address; the browser signs in only
  when the guest presses **Continue** (so a mail scanner opening the link uses nothing), and that browser is the one
  signed in. A link works once, for a day at most (never past what was shared), and making a new one for a guest
  takes back their unused ones. The link's secret part is after the `#`, which browsers never send to a server.
- **Their own organization account**, when people sign in to this MetaRush with their organization's account
  (Settings → People → sign-in with your organization's account). The account's email must be one the provider
  vouches for, and the email you shared with. The same rules as for people apply: Google vouches for Gmail and Google
  Workspace accounts; Microsoft only for members of your own directory — never its guests or personal Microsoft
  accounts, so guests from other organizations sign in with a link. Add the guest address's sign-in address (shown in
  Settings → People → Guests and by `metarush guests status`, like `http://127.0.0.1:4846/guest/sso/callback`) to
  your app at the provider. The first account a guest signs in with is theirs: another account of the same provider
  showing their email is refused.

A guest stays signed in for 12 hours at most, and is signed out after 2 hours unused. Every sign-in makes a new
cookie on the guest address only (`mr_guest`; `__Host-mr_guest` and Secure over HTTPS), HttpOnly and SameSite=Strict;
the dashboard doesn't know it, and the guest listener doesn't know the dashboard's.

## What owners see

Settings → People → Guests lists every guest request, newest first, in plain words — who, what they opened, watched
or wrote, from which address, and what was turned away — and says when a guest asks unusually often (more than 600
requests in an hour; owners also get a bell note, once a day per guest). Guests' comments show on the version as
"<name> (guest)" and ring the bell of whoever shared it; Claude reads them only as quoted feedback. The activity log
records every guest request under its own channel; the project's receipt lists what was shared with guests.

## Where the video comes from

- **Media kept in a cloud bucket**: the guest's browser plays the review copy straight from the bucket, through a
  link signed for 2 minutes; the page asks for a new one when it ends, and each new one checks what was shared again.
  The video never goes through this computer. A link already handed out can't be called back, so a video from the
  bucket stops within two minutes of being taken back. A bucket whose key can't sign links can't be watched by
  guests.
- **Media kept on this computer**: the guest listener plays it, with ranges, checking the guest's sign-in and grant on
  every request.

## Under the hood

- The guest listener (`internal/api/guests*.go`) has its own router: the guest page, its sign-in steps
  (`/guest/api/link`, `/guest/api/sso/start`, `/guest/sso/begin`, `/guest/sso/callback`), `/guest/api/*` and
  `/guest/media/*`. Nothing else exists there. Its Host check accepts the guest address only; Tailscale's identity
  headers and forwarded headers are taken off every request and never believed (the visitor's address is the
  connection's); every request is bounded (small bodies, exact JSON, no query on the API); changes need the guest
  page's own header from the same origin; each address has limits, the sign-in steps tighter ones.
- The guest operations (`guest.shared`, `guest.watch`, `guest.comments`, `guest.comment`) answer only for the guest
  whose sign-in the listener checked; members can't call them as a guest (owners may look at what a guest sees, read
  only). The owners' operations are `guests.status|list|share|link|revoke|activity|settings.set`.
- Records: `guests`, `guest_grants`, `guest_links` (by their secret's SHA-256), `guest_sessions` (by the cookie's
  SHA-256) and `guest_activity`. Links and sign-ins are left out of [workspace exports](workspace-export.md);
  clearing a project out ends what was shared of it.
- `scripts/guests-e2e.sh` (ports 5820–5829) is the browser run: an owner turns guests on and shares a version, the
  guest signs in by link in another browser, watches and comments, the owner takes it back and the guest is out.
- What a guest typed (a comment) reaches the activity log only as its size; the log keeps the IDs a guest's request
  named, never their words. A link's secret leaves the page's address as soon as the page opens.

## Before guests get a public address

Things that hold on this computer's own address and need another look when a later release puts something in front
of the guest listener:

- **Limits per address are one bucket today.** Every guest request comes from 127.0.0.1, so the per-address limits
  (every request, and the sign-in steps) count all guests together. A relay in front has to carry each visitor's
  address in a header of its own, believed only on the relay's own port, for the limits and the activity list to tell
  visitors apart.
- **Cookies don't keep to a port.** A browser shares `127.0.0.1`'s cookies across its ports, so a page another program
  serves on this computer could set or clear `mr_guest`. It can't sign anyone in (only a sign-in made here counts) but
  could sign a guest out. Over a public HTTPS address the cookie is `__Host-mr_guest` (Secure, no domain), which
  nothing else can set.
- **Every video request is checked from scratch.** Each range request of a review copy kept on this computer runs the
  guest's checks again — the sign-in, the grant, What may leave, the versions guests never see — and a video being
  sent asks again every few seconds. Before many guests watch at once, keep those answers per sign-in for a few
  seconds.
