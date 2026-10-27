# Generative video

MetaRush can make new shots with a generative video model: a shot from a few words, a shot that continues from a
frame of your footage or goes between two frames, a shot with an approved **look** of a person (with their consent)
or a fictional character, and new takes, variations, extensions or edits of a clip. Every request is **priced first
and made only with the price a person agreed to**; results are ordinary clips in the project's Sources, **marked as
AI-generated everywhere**, each saying exactly how it was made.

The first provider is Google Gemini (the Gemini API, model Gemini Omni 1.1 Flash). **Veo** is the second, through a
person's own Google Cloud project (Vertex AI): Veo left the Gemini API on 22 October 2026. Other providers fit
behind the same provider layer.

## Veo through Google Cloud
Choose it by its model: `metarush generate shot "a red kite over a hill" --model vertex/veo-3.1-fast-generate-001
--seconds 8 --resolution 720p` (or `vertex/veo-3.1-generate-001`, up to 4K). It is held to everything Gemini's shots
are: the price first, the project's budget and the team's daily limit (the same AI work limits), Content Settings
(rule it out with `--forbid vertex`), looks and consent, records, disclosure. What differs:
- **Lengths and prices.** Shots of 4, 6 or 8 seconds (shots from pictures of what should appear: 8 with Veo 3.1),
  wide or tall, 24 frames a second, with sound; priced **by the second**: Veo 3.1 Fast $0.10 (720p) or $0.12 (1080p),
  Veo 3.1 $0.40 (720p, 1080p) or $0.60 (4K) — Google Cloud's prices read on 25 October 2026 (its page writes the
  unit as "1 count"; Veo is billed by the second of video). Not yet: changing or extending a clip with Veo.
- **Nothing left in the project.** MetaRush names no Cloud Storage bucket, so the video comes back in the answer and
  nothing stays in the project; Google may cache what it was sent for up to a day and doesn't train on it.
- **Stopping.** Google Cloud offers no way to stop a Veo request: a stopped shot finishes there and isn't kept (its
  estimate counts).
- **Retirement.** Google's model page lists Veo 3.1's retirement as "17 November 2026 or later"; the catalogue gets
  `retiredOn` once Google names the day.

**Setting it up (a person, once per computer; Google Cloud's console):**
1. Choose or create a Google Cloud project with a billing account linked to it.
2. Turn on the **Vertex AI API** for the project (APIs & Services → Library → Vertex AI API → Enable).
3. Create a **service account** (IAM & Admin → Service accounts → Create) and give it the **Vertex AI User** role.
4. On the service account's **Keys** tab: Add key → Create new key; Google downloads a key file.
5. Give that file to MetaRush — owners only, never as an argument: `metarush providers set vertex --key-stdin <
   ~/Downloads/<key file>` (or Settings → Integrations). MetaRush checks it is a service account's key that signs in
   only at Google, signs in once to check it, and keeps just the project, the account's address and its private key
   in this computer's keychain (or the installation's private `.env`). **Delete the downloaded file** afterwards.
6. The first shot says what is still missing, if anything (the API off, no billing, the role), at no cost.

How the key is used: for each hour of work MetaRush signs a request for an access token with the private key (RS256)
and sends it only to `oauth2.googleapis.com`; the token goes only to `us-central1-aiplatform.googleapis.com`, in the
`Authorization` header; neither the key nor the token is written to the database, a log or an answer
(`METARUSH_VERTEX_BASE_URL` and `METARUSH_VERTEX_TOKEN_URL` accept only a server on this computer, as tests and
browser runs use: `internal/testutil/fakevertex`). A key file that sends its sign-in anywhere but Google is refused.

## Where it is
- **Generate video in the drop box**: under Choose files, on Home and in a project's Sources, it opens the
  Generate panel below; from Home it first asks which project the shot is for (the one used last is chosen: the
  project of your last generation, else the newest one). Every shot belongs to a project and is paid from its budget,
  so the Inbox isn't offered.
- **Start from a picture**: in the panel, a photo from this computer (added to the project's Resources first), a
  picture already in Resources, or a frame of one of the project's clips (with a slider for the moment). The shot
  starts on it (`generate.shot` `from.picture`; `metarush generate shot … --from <picture>`). MetaRush can't tell from
  a photo whether it shows real people, so you tick "This picture shows no real people" before it can be sent.
- **Studio → Sources → Generate a shot**: the prompt, who appears (a look), the length (3–10 s), wide or tall,
  draft or final with the price a second, takes (1–4). The price shows before anything is sent and the button
  carries it as an estimate ("Generate (about $0.13)"); **What it draws on** opens the estimate: the video at its resolution,
  what is sent with the prompt (the prompt, pictures and clips) and the thinking allowance, each with its tokens,
  price and the price table's date. With your estimates off (Settings → Preferences) the button doesn't wait for the
  price. On a clip, **Continue this shot** starts from the frame in the player; then
  **End the shot on this frame** on another moment makes a shot between the two. The project's generated shots are
  listed under the panel with **Again**, **Variations**, **Extend**, **Edit** and **Stop**.
- **A generated clip's header**: what made it, its length, quality and cost, the prompt, and **How it was made** behind
  Details (model and stage, price table, the prompt as sent, references with their SHA-256, request hash, the
  provider's ID, whether it was deleted at Google, who asked, where and when).
- **The Script**: a generated clip says "Generated with Google Gemini", the prompt behind Details. Sources has an
  AI-generated filter; tiles carry an AI badge.
- **People → a person → in generated video**: record their consent, make their look, check its test shots, approve
  or withdraw it. **Looks and consent** (`/looks`) lists every look and the consent on record, and makes character
  looks from pictures.
- **Settings → Integrations → Generative video**: the Google Gemini key for this computer, the model and its prices,
  draft and final quality, today's limit for the whole team and each project's budget.
- **The activity bar, job details and the bell** say what is happening ("Google Gemini is making it (40 s so far)",
  "Ready in Sources") and when a shot is ready or couldn't be made.

## From a terminal (and Claude Code)
```bash
metarush providers set gemini                                   # hidden prompt, or --key-stdin; checked before saving
metarush generate settings --project "Pond film"                # models, prices, budgets, today's spending
metarush generate estimate "a red paper boat drifting on a pond" --project "Pond film" --seconds 3 --resolution 360p
metarush generate shot "a red paper boat drifting on a pond" --project "Pond film" --seconds 3 --resolution 360p --max-cost 0.20 --wait
metarush generate shot "the camera keeps drifting right" --from interview-a@0:12 --no-people --project "Pond film"
metarush generate shot "walks along the pond" --look captain-dog --project "Pond film"
metarush generate again <generation or clip> --takes 3 --dry-run     # variations: priced; add --max-cost to make them
metarush generate extend <clip> --seconds 5 --prompt "the boat reaches the far shore"
metarush generate edit <clip> "make it night, with the street lights on"
metarush generate list --project "Pond film"  ·  metarush generate show <generation> --details  ·  metarush generate cancel <generation>
metarush generate configure --daily-cap 20 · --project "Pond film" --budget 5 · --draft 360p · --final 1080p   # people only (who set a limit is kept: Auto spends only within limits a person set)
metarush consent record --person Ada --self --adult --statement "I, Ada, agree to …"                           # people only
metarush looks create --title Ada --person Ada --description "…" --still <clip>@0:04 --still <clip>@0:19     # people only
metarush looks test ada --max-cost 0.30  ·  metarush looks approve ada  ·  metarush looks revoke ada            # people only
```
Without `--max-cost`, a terminal asks with the price; anything else gets `E_CONFIRM_REQUIRED` with the estimate
(nothing sent, nothing spent) — unless the person turned estimates off (`metarush account preferences --estimates
off`): then no price is needed, the estimate is what is set aside and the budgets still decide (an owner can
ask everyone for estimates). `generate estimate` and the dry runs list the estimate's parts (`--details` adds the
prices). `--wait` makes it in this process and waits until the clip is in Sources; without it a
worker on this computer makes it. Every command has `--json`; ops: `generate.estimate|shot|again|extend|edit|list|
show|cancel|settings|configure`, `looks.list|show|create|update|approve|revoke|test`, `consent.list|show|record|revoke`.
The `video-generate` skill teaches Claude Code the rules below. In the dashboard's Claude Code panel, making a shot
is a card with the price on it; configuring, looks and consent are never offered to the assistant.

## Money
- **Prices are data** (`internal/videogen/catalog.json`), dated: each estimate and record keeps the table it used.
  Gemini Omni 1.1 Flash (table of 2026-10-19, checked 2026-10-20): video output $17.50 per million tokens at
  1,931 / 5,792 / 8,688 / 17,376 tokens a second for 360p / 720p / 1080p / 4K (about $0.034 / $0.10 / $0.15 / $0.30
  a second; 1080p and 4K are made lower and upscaled by Google), text and thinking $9 per million, input $1.50 per
  million (a picture is 1,120 tokens). The estimate adds an allowance of 3,000 thinking tokens, so a 3-second 360p
  draft is about $0.13 and usually costs a little less.
- **Reserve, then settle**: before a request is sent its estimate is set aside against the project's budget
  and today's limit for the whole team (`generation_spend`, per project and per UTC day); when it ends, the hold is
  replaced by what it cost (Google's reported usage, or the estimate when it reported none). Defaults: $10 a project
  and $10 a day; people change them. Requests over either are refused before anything is sent (`E_BUDGET`).
  Google also limits spending per 10 minutes by usage tier ($10, $50, $200); a 429 says whether it was the rate or
  the spending, and MetaRush waits or says so. A project on Google's **free tier** can't make video at all: its 429
  ("limit: 0 … on Free Tier") is never waited out, and the shot ends at once at no cost (below).
  The same budget and daily limit cover the other kinds of AI work — pictures, speech, music, dubs
  ([ai-services.md](ai-services.md)) — and a project's Content Settings may keep its work away from Google
  altogether (`metarush ai rules`), which generated video follows too.
- **Never automatic**: no import, render, catch-up or assistant ever starts a generation; only a person's request
  with the agreed price does — or, for a person who turned estimates off, their request within their budgets,
  and under their Auto a card Claude asked for (`AutoPriced`). Looks' test shots always need the agreed price.
- **Estimates you can open**: every estimate (`GenEstimate.cost`, the shared `costs.Estimate`) opens to its
  parts — the video, what is sent and the thinking allowance — with the model, the source (the prompt and its
  pictures or frames), the tokens and each part's price; its total is the request's own estimate, the price agreed.

## How a request runs
A request becomes a `generations` record (`gen_…`, written once, part by part) and a durable `generate` job leased
to the computer that asked:
1. **Sending**: references are uploaded first (video through the Files API, checked by SHA-256 and waited on until
   active; pictures inline). The interaction is created in the background (`POST /v1beta/interactions`,
   `response_format` video, `background: true`). Each send attempt is recorded; an attempt that was interrupted is
   **never sent again** (it may have reached Google and been charged): it ends, and its estimate counts as spent.
2. **Making**: the provider's ticket (interaction and file IDs) is saved **before** waiting, so a restart resumes
   polling instead of resubmitting. Polling every 5 s; Stop cancels at Google. **"Too many requests"**: a 429
   that says the project can't make video (the free tier, a limit of zero) ends it at once, when sent or on any
   check: failed, "This Google project is on the free tier, which can't make video. Turn on billing for the project
   in Google AI Studio, then try again." (Google's page https://ai.dev/rate-limit in the hint), $0, the hold
   released, Google asked to cancel and delete it. Any other 429 is waited out (the activity bar says Google is too
   busy to answer), but after 10 minutes of nothing else the job waits in line for 10 minutes, and after two such
   waits in a row MetaRush gives up, cancels it at Google and counts the estimate, in case it was made.
3. **Downloading**: inline results or the file are streamed (capped at 2 GB), probed with ffprobe and hashed.
4. **Adding**: `ingest.File` adds the clip with its `Asset.Source` (platform `generated`), tag `ai-generated`; the
   provider's original file is kept as the clip's original.
5. **Clean-up at Google**: the interaction and every uploaded or generated file are deleted right after the
   download (`forgotten` records when and whether it worked). Gemini's background mode requires `store: true`, so
   deleting is how MetaRush keeps likeness pictures from staying at Google.
6. **Settling** the money and telling the person (the bell; a look's test shot links to the look).
The daemon sweeps every few minutes: generations whose job was stopped or gave up get an outcome, and money held for
requests that were never saved is released.

The record keeps: provider, model, launch stage, price-table date, the prompt verbatim and as sent, the template and
its variables, references (role, resource name@version or clip and time, SHA-256, who it shows), the parameters, the
canonical request hash (pictures and clips by SHA-256, never a key), the provider's IDs and times, every send, the
estimate and the actual cost, the safety outcome (filtered count, support codes, category, Google's words), outputs
(clip, SHA-256, size, length, resolution), lineage (parent take, source footage; cuts using it are looked up), who
asked, on which machine, through which channel.

When Google declines (`E_SAFETY_BLOCKED`), the record says which side (the request, or what it made), the category
when Google said, and the support codes. A shorter result than asked is kept and said so.

## Likeness and consent
- **Consent** (`consents`, `cst_…`): your own likeness needs a statement in your own words; anyone else's needs a
  recording of them saying they agree (a clip with sound in MetaRush, never a generated one), with what it covers
  (every project or some) and until when (a year unless you say). It records who recorded it, on which computer and
  when; withdrawing it stops new shots and withdraws the looks that rely on it. MetaRush never records consent for a
  child (an adult must be confirmed) or a public figure. Only people record or withdraw consent. A consent says what
  it covers (generated shots, the person's voice, a presenter) and which AI services may keep the recording:
  a presenter reading lines on camera needs a recording of the person, their own too ([presenters.md](presenters.md)).
- **Looks** (`looks`, `lok_…`): a person's look (with their consent) or a fictional character's. 2 or 3 stills and up
  to three short clips (3 s or less) of the person from your footage (their confirmed faces), or pictures of the
  character that show no real person; a fixed description sent with every shot; defaults; realistic or stylized; a
  test sheet of short 360p drafts. A look starts unapproved; only people make, test, approve or withdraw one;
  changing what it shows needs approving again.
- **Every request is checked**: a prompt that names someone on the People page needs their look; a frame from your
  footage is checked against the faces index (a named face needs that person's consent; an unnamed face, or a clip
  nobody has looked at for faces, needs the person to say the frames show no real people); a look of a person needs
  consent in force for the project. Missing consent is `E_CONSENT_REQUIRED`, before anything is sent. Google's own
  filters still apply and may decline likenesses.

## Disclosure
Generated clips are flagged everywhere: AI badges, the clip header, the Script, an AI-generated filter, the cut
sheet ("from AI-generated footage"), the quality check (an `ai_generated` note), and delivery. Each export's sidecar
lists the generated clips (generation, model, SHA-256, where the cut uses them, seconds, whether a likeness, the
SynthID watermark) with a checklist per platform: YouTube (answer Yes to altered or synthetic content), TikTok (the
AI-generated label), Instagram and Facebook (the AI info label) and the EU AI Act (article 50). Google adds an
invisible SynthID watermark to everything it makes.

## Keys
The key is per computer, through the providers registry (keychain, or `<home>/.env` in env mode;
`METARUSH_GEMINI_API_KEY` overrides). It is checked before it is saved (listing models is free), never written to the
database, logs, audit or responses, never in a URL (only the `x-goog-api-key` header), and sent only to
`generativelanguage.googleapis.com`; `METARUSH_GEMINI_BASE_URL` accepts only Google's hosts or a loopback test
server. Making video needs a Google project with billing turned on (the paid tier). The check can't see the tier:
a free-tier project lists the video model too, and nothing a key can call shows the tier without spending or
starting something at Google. So it says "Connected. Google makes video with Gemini Omni 1.1 Flash only for projects
with billing turned on; MetaRush tells you on the first shot if this one can't." Details: docs/security.md.

## Testing
- `internal/testutil/fakevideogen` (and `cmd/fakevideogen`): the Interactions and Files APIs with tiny MP4s keyed by
  the request hash; scripts and prompt markers for delays, celebrity and child blocks, partial results, 429 rate or
  spending (when sent, or `poll-rate` while waiting), 5xx, expired outputs and retired models; the free tier word for
  word as Google answered it on 2026-10-20 (`SetFreeTier` or `-free-tier`: `status`, every check of an accepted
  request, or `start`; `[fake:free-tier]` and `[fake:free-tier-start]` for one request); `/__calls` says what is
  still kept and whether a key ever appeared in an address.
- `internal/testutil/fakevertex`: Google Cloud's token endpoint (the signed request checked) and Veo's
  predictLongRunning and fetchPredictOperation, MP4s inline; scripts for delays, filters holding a video back, a
  blocked prompt, the API off, a missing role, quota and server errors.
- Go tests: `internal/videogen` (prices, capabilities, the Gemini client against the fake, Veo's client, key and
  refusals against its fake, the free tier in its
  wordings against real rate and spending limits), `internal/generate` (budget reserve and settle, consent refusals,
  looks, resume after a restart without resending, provider trouble, the free tier ended at once at $0, the cap on
  429s while waiting, the key never written to the database, the log or the spool), QA and delivery.
- `scripts/generate-e2e.sh` → `web/scripts/generate-e2e.mjs`: the Settings key flow (a wrong key refused, never in
  the page, a response or browser storage), budgets, Generate a shot, Variations, how it was made, Continue this shot,
  consent refusals and recording, a person's look (made, never used), a character look (tested, approved, used), a
  free-tier key's shot (ended at once, said plainly, nothing charged), the Script's Generated line, withdrawing
  consent, removing the key; screenshots for review.
- `scripts/acceptance.sh` (generated video step): priced first, refused without the price and over a budget, made by
  the fake, how it was made, nothing left at the provider, a free-tier key's shot ended at once at no cost, a named person
  refused, a cut with the shot rendered, the quality check's note and the delivery sidecar.
- **Live**: checked against the real service with a 3-second 360p shot (estimated $0.13, capped at $0.50). Two answers
  Google gives before making anything are handled: a project denied access (403 "Your project has been denied
  access"), recorded with $0 settled and the advice that only Google can unblock it; and a project on the free tier
  (every status check a 429 with a zero limit), which ends the shot at once at $0 instead of waiting it out as a rate
  limit. The key was found nowhere in the home, the record or the activity log, and the key check doesn't promise
  video. The request follows
  Google's API reference; inline delivery is left to Google's default because its guide and reference name it
  differently (`base64`, `inline`), and both name `uri`.
