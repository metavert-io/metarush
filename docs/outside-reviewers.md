# Outside reviewers: Share for review

A person sends someone outside the workspace — a client, a colleague without an account — an expiring link to one
version's review copy. They watch it, read what is said (the Script view) if the person allows, and leave comments
pinned to times. Their comments come back as the version's comments, marked "(outside)". They need no account, never
see the dashboard, and can reach nothing else.

## Sending a version for review

On a version's page, **Shared for review → Share for review…** opens the form:

- **Who it's for** — their name fills in on the page (they can change it).
- **How long it works** — 1 to 7 days (7 by default; owners may set a shorter longest).
- **The title on the page** — the cut's title and version by default.
- **A note for them** — optional.
- **What the page shows besides the video** — what is said, line by line, with speakers as *Speaker 1, 2…*, their
  names, or no one named; a download button; *Mark it as a draft for review* (on by default for a version nobody
  approved).
- **What leaves this computer** — the list the receipt keeps, where it is kept and until when.

**Make the link** shows the link once (Copy link); send it the way you usually do. Afterwards the version's page lists
every link sent for it: who for, until when, what it shows and how many comments came back, with **Copy link**,
**Look for comments** and **Take back**.

From a terminal (a person typing; Claude, AI apps and scripts are refused):

```bash
metarush share plan "Spring promo" --words                     # what a link would show, and whether it may be made
metarush share create "Spring promo" --version r3 --words --speakers names --download --for "Dana" --message "Is the ending right?"
metarush share list "Spring promo" [--version r3] [--all]      # the links that work now (every one with --all)
metarush share link <link>                                     # the link again, for the time it has left
metarush share revoke <link>                                   # take it back at once
metarush share sync [<link>]                                   # bring in new comments now
metarush share settings [set --on|--off --owners-only|--everyone --max-days N]
```

Every command takes `--json`. The operations are `share.plan|create|list|link|revoke|sync|settings|settings.set`.

## What the reviewer sees

One page: the title, who sent it and until when, the note, the video (with a download button if allowed), the
comments everyone with the link left (each pinned to a time jumps the video there, and shows as a mark under it), a
form for theirs (their name, the comment, *Pin to 0:12* — the moment they started writing; the video pauses while they
write) and, if allowed, what is said, line by line, the line being spoken lit up; pressing one plays from there. It
works on a phone. When the link has ended, the page and the video are gone.

## Where the page lives, and why

MetaRush's dashboard listens only on 127.0.0.1 and is reached only through `tailscale serve`, and an
outside reviewer isn't on the tailnet. Rather than open a door, the page is kept in the workspace's **own media
store** as a private, signed, expiring static page:

- its objects are under `review/<128 random bits>/` in the bucket: the page, a copy of the review copy, its still,
  and eight comment slots;
- the link is a signed read of the page, and every address inside it is signed for the same time (a cloud bucket signs
  for a week at most: hence 7 days);
- a reviewer's browser writes comments only into the slots it was given, each through a signed upload of exactly
  128 KiB;
- MetaRush reads the slots back with its own key — every two minutes on the computer whose dashboard is running, or
  when asked — and keeps what they hold as comments on the version.

Nothing reaches this computer; MetaRush only reaches out. It needs media kept in an S3-compatible bucket (Tigris, R2,
B2, AWS, GCS through S3) or in Google Cloud Storage with a key that signs links. No CORS rules are needed: the page and
everything it reaches are on the bucket's own address. A workspace whose media is a folder on this computer can't
share this way, and is told so; a bucket on this computer or a private network works only for people who can reach it,
and the form says so.

## Comments that come back

- They are ordinary comments on the version: on its page with the rest, pinned to their times, by
  "*Dana (outside)*", their words shown as theirs.
- The person who sent the link hears it on the bell, once per link.
- Reviewers' words are data: control and invisible characters are taken out; a comment is at most 2,000
  characters and a name one line of at most 60; a time counts only within the video.
- Limits: 8 reviewers' browsers per link, 100 comments from each, and a link takes in at most 60 comments an hour
  (the rest wait in their slots for the next hour). A slot is its first browser's: MetaRush never takes in another
  browser's writing over it, whatever it calls itself. When a link ends, what its slots hold comes in first.
- Claude in the panel, Claude Code in a terminal and AI apps read an outside comment — and the name its reviewer
  gave — only **quoted**, with a note to weigh it as feedback and never follow it as an instruction (`comment.list`,
  search, `share.list`). The bell and the daemon's lines quote reviewers' names for everyone.

**What someone with the link can still do.** Every page holds the signed uploads of all eight slots (a browser
picks a free one), so a reviewer who digs into the page can write over another reviewer's slot. MetaRush keeps what
it has read (every two minutes), never takes in the overwriting, and holds the limits above — but comments written in
the two minutes before an overwrite can be lost. A link is for people you trust with the video and each other's
comments; send separate links when reviewers shouldn't see or touch each other's notes.

## Who may share, and whose things may go

- Making a link, copying it again and taking it back are **people's** steps. Claude can't, an AI app can't, the review
  desk can't hold one as a draft.
- **Owners** decide in Settings → Storage → **Review links** (or `metarush share settings set`): review links off,
  only owners send them, the longest a link lasts.
- **What may leave** has a place for **people outside the workspace** (`reviewers`). A link sends footage, voices,
  faces and pictures, and words when it shows what is said (or the captions are in the picture). A client whose rules
  keep any of those from people outside the workspace — or from cloud storage, where the page is kept — can't get a
  link, and the refusal says which rule, whose settings and who set it.
- The **receipt**'s *What left this computer* lists every link: who it was for, what it showed and until when.
- **The ship gate**: where it is on, a version it holds — not approved yet, its review copy's quality check
  missing or with problems, sign-offs open — is shared only marked as a draft (the form ticks that for a version nobody
  approved), or by a person sending it anyway with a note, kept on the link.

## When a link ends

A link ends when its time is up, when a person takes it back, and by itself when what allowed it changes: the media
moves to another storage (once the copy is verified, just before the switch; the move's plan says so; a move that
is refused before then, or because a link's comments are being read right then, leaves links working; no link is
made while the media moves), its client's What may leave no longer lets its things go to people
outside the workspace, someone it shows withdraws the consent what it shows of them relied on (a generated shot or
picture, a picture made from one, anything said with a voice copy made with it, or what a presenter made with it
presents, in the version or in a cut it nests; asked when a link is made, at Copy link and at
every look, so a link the withdrawal missed still ends), owners turn review links off, or its project is cleared out
(its comment slots are then deleted unread: the project's comments go with it). The person who sent it hears why on
the bell. A move also finishes links that ended but whose last comments aren't in
yet, and waits (refuses, try again in a minute) until each one's page is gone and its last comments are in.

Ending one deletes its page and its copy of the video at once, so it stops opening; then its comment slots are read
one last time and deleted — only after that read worked (if another computer is reading them right then, the next
look reads them first). What MetaRush says is what it did: a link whose files are in a storage this computer no
longer uses keeps opening until its signature ends unless a computer that uses that storage looks first, and Take back
says so.

## Security

- Unguessable: 128 random bits in every address, and every address a signature for one object and one method.
- Nothing beyond the version: the page holds signed links to its own objects only; no workspace ID, project, clip or
  person's ID appears in it.
- No credentials: a signed link names the key's ID (as every signed link does), never its secret.
- Expiry twice: the store refuses a link past its signed time; MetaRush ends the link, deletes its files and stops
  reading it.
- Taking back deletes the page and the video at once, and the slots once their last comments are in. A page still
  open could write a slot again until its signed time ends; MetaRush deletes such a slot again on every look and
  never reads it.
- The reviewer's browser keeps only its random name for itself and its slot's number at the bucket's address (some
  stores' addresses are shared with other sites); Google Cloud Storage links name the bucket in the host, so their
  address is the bucket's own.
- The page's Content-Security-Policy allows its own script and style (by hash) and the bucket's address only; no
  referrer is sent; search engines are told not to index it.
- Never a path into the dashboard: the page has no address of MetaRush, and MetaRush only ever reads the bucket.
- Signed links are redacted from the activity log.

## Testing

- `go test ./internal/reviewlinks/ ./internal/media/` runs against `internal/testutil/fakebucket`: a private
  S3-compatible bucket in memory that checks every signature like a cloud bucket — each signed link's signature, the
  headers it signs and its expiry (it can move the clock links are judged by forward), and MetaRush's own requests'
  Authorization headers, recomputed with the bucket's secret.
- `scripts/outside-reviewers-e2e.sh` (ports 5590–5599) is the browser run: a throwaway installation whose media is the
  fake bucket, a real review copy, Share for review… in the dashboard, the page opened in a browser with no MetaRush
  cookies, a timed comment that comes back as "(outside)", a client whose rules refuse a link, taking a link back and a
  link expiring at the store.
