# Posting to video platforms

MetaRush can post an approved version to **YouTube** through your own sign-in on this computer. It is a
person's step every time: Claude, Auto and AI apps (through MCP or the command line) never post, and nothing is
posted while anything holds the version back. Delivery still writes a checked folder and never uploads;
posting is its own step on top.

```bash
metarush publish platforms                                   # set up? signed in as? what to do next
metarush publish setup youtube                               # an owner, once per computer
metarush publish connect youtube                             # each person, at this computer (the browser opens)
metarush publish plan "Garden walk" --version r3             # exactly what would be posted, and what holds it
metarush publish post "Garden walk" --version r3 [--privacy unlisted|public] [--shape vertical-1080p]
metarush publish connect youtube --forget                    # sign out here and take the permission back at Google
```

Ops: `publish.platforms`, `publish.setup` (owners, people, this computer), `publish.connect` (people, this computer),
`publish.plan` (read-only), `publish.send` (people).

## Setting up YouTube (an owner, once per computer)

MetaRush ships no Google client of its own: it uses yours, so the permission people give is to your own project,
and its daily allowance is yours.

1. In the Google Cloud console, make a project (or use one) and turn on the **YouTube Data API v3**.
2. Set up the **OAuth consent screen**: user type External (or Internal for a Google Workspace organization), your
   app's name, and the scopes `.../auth/youtube.upload` and `.../auth/youtube.force-ssl` (managing captions; it also
   reads the channel's name). While the screen is in *Testing*, add every person who will post as a test user.
3. Under **Credentials**, create an **OAuth client ID** of the type **Desktop app**, and keep its ID and secret.
4. At this computer's terminal: `metarush publish setup youtube` — it asks for the ID and the secret (typed, never
   on the command line) and keeps them in this computer's keychain. `--remove` forgets them.

What Google does with such a client (check these before posting for a client):

- **Videos stay private until the project is reviewed.** YouTube keeps every video sent through an API project it
  hasn't audited private, whatever privacy was asked for; MetaRush says so when it happens. Make the video
  public in YouTube Studio, or apply for YouTube's API audit.
- **Sign-ins last a week while the consent screen is in Testing.** Publish the consent screen to production (for
  these scopes Google may ask for verification) for sign-ins that last; until then people sign in again weekly.
- **The daily allowance** is 10,000 units per project: a video costs about 1,600 and each captions file 400, so
  about five videos with captions a day. MetaRush says when it is used up; it starts again at midnight Pacific time.

## Signing in (each person, at this computer)

`metarush publish connect youtube` at a terminal on the computer that runs MetaRush: the browser opens Google's
sign-in; you choose the account with your channel and allow MetaRush to upload and manage captions. The answer
comes back to a loopback address only this computer can reach (with PKCE; an answer that isn't this
sign-in's is turned away and MetaRush keeps waiting for the real one, and the browser is opened through a
private page — in the app that opens web addresses, removed when the sign-in is over — so the sign-in's address
never appears on a command line or in the activity log), and only the lasting permission (the
refresh token) is kept — in this computer's keychain, under you (an installation that keeps its secrets in its
secrets file keeps it there, under a `METARUSH_PUBLISH_…` name that MetaRush reads from the file when it posts
and never puts into its environment, so neither Claude Code nor any program MetaRush starts gets it; the
client is kept the same way). It never goes into the database, the configuration, the activity log or an answer. Each person posts as their own channel; nobody posts as someone
else. Over Tailscale, signing in isn't offered: the browser must be on this computer.

## What is posted

`publish.plan` (and the command, before it asks) shows exactly this; `publish.send` sends only the plan a person
saw (its fingerprint), so a change in between is refused and shown again.

| Part | From |
|---|---|
| The video | the version's newest final render (the landscape one when there are several; `--shape` picks another) |
| Title | the delivery notes' title; without one, the cut's name, with a warning |
| Description | the delivery notes' description, then the chapter lines ("0:00 Intro"), then what delivery discloses (AI-generated footage, AI narration) and credits for outside footage |
| Chapters | the delivery notes' chapters, in the description (YouTube shows them from three, each at least 10 s) |
| Captions | a WebVTT file of the version's captions with today's text fixes, and one per language it is translated into, machine translations named so |
| Privacy | private unless you say unlisted or public |
| AI disclosure | YouTube's altered or synthetic content setting, when the video shows AI-generated footage or AI narration |

## What holds it

Nothing is sent while any of these hold it; each says what to do: the version isn't approved (or needs changes);
it has no final render; the ship gate holds it (the quality check of the final render or a sign-off, until it
is fixed or a person sends it out anyway with a note — a post past it is recorded as having gone out, so the
receipt says so); the client's rules keep its things from leaving (What may leave: place delivery,
receiver YouTube; a plan only says so, a refused post is recorded); YouTube isn't set up on this computer; you
aren't signed in here.

## What is kept

Every post is recorded on the version (`Revision.publications`: who, when, the channel, the privacy, the file's
SHA-256, the captions languages, the video's address; a failed one with why), in the change history
(`publish.posted`, `publish.failed`) and the activity log (each request to Google, never a token), and on the
project's receipt ("Version 3 was posted to YouTube as “Garden Club” (private)"). A dropped connection resumes
where YouTube says it got to; a dashboard tab closed mid-way doesn't stop it.

**Once, unless you say so.** A render goes to a channel once at a time: the check and the new post are one write,
so two tabs or a double click send one video and the other is told it is going out. While it goes, the version
page (and `publish plan`) shows how much has gone and holds another post. A render already on the channel is
posted again only when a person says so (the dashboard asks; `metarush publish post … --again`, or the
terminal asks): it makes a second video there.

**When MetaRush stops part-way** (the computer slept, the daemon restarted), the computer that was sending keeps
YouTube's resumable session for that post in `<home>/state/publish/` (0600; never the database or the log). The
daemon looks every minute for posts whose sender stopped saying it is still sending (two minutes): it goes on
from where YouTube got to and records the video, or marks the post as not posted, saying why, when no session
was kept or YouTube no longer has it (it keeps one about a day). Another computer's post is given up after a day.
A connection that drops (or a busy YouTube) leaves the post sending with its place kept rather than failed: the
daemon goes on with it the same way, up to eight times within the day; a person who stops it in the terminal
stops it. Before going on, it asks the plan again as the person who posted it: if anything holds it now (the
approval, the ship gate, the client's rules, the sign-in), the post is marked as not posted, saying why, and that
person's bell says so. One sender at a time owns a post (a claim each new sender takes); a sender that finds
another has taken it over stops, and reports what the version records. What is kept says why in plain words, never the upload's address (it lets anyone send to the upload; it
stays in the 0600 file, is kept out of the log, and the sign-in is sent only to YouTube's own upload address). A
post sent past the ship gate says it went out however it finished. A post marked as not posted that reaches
YouTube after all stays marked so, with a note and the video's address (if it was posted again since, delete one).

## Why a person's own sign-in

Google has no command-line tool of its own for posting to YouTube, and the third-party ones keep tokens in plain
files. A sign-in in the person's own browser with the owner's own client keeps the permission theirs, in the
keychain, revocable at Google, and makes "who posted this" a fact.

## Tests

`go test ./internal/publish ./internal/ops -run 'YouTube'` against `internal/testutil/fakeyoutube` (sign-in,
tokens, resumable sessions that drop once, captions, quota). The real YouTube and the browser are refused under
`go test`; `METARUSH_YOUTUBE_BASE` takes only a server on this computer.

## Other platforms

Vimeo, LinkedIn and a podcast host (Buzzsprout first) are abilities written in the shape they need
(`abilities/vimeo-upload`, `abilities/linkedin-video`, `abilities/podcast-episode`): people-only every run,
and **not yet runnable** — they wait for a version's final render and its delivery notes as an ability's inputs,
and LinkedIn for a person's own sign-in kept the way this page's YouTube sign-in is. Each one's `notes.md` says
what is missing and what you will set up.
