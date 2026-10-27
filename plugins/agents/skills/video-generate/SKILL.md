---
name: video-generate
description: "Make new shots for the person's video with generative video — from words, a frame, two frames or an approved look; takes, variations, extensions, edits — always priced first and made only at the agreed price; a likeness only with recorded consent. Routes to `metarush generate estimate|shot`, `metarush ai estimate|make` (pictures, speech, music), `metarush looks`, `metarush consent`. Works whether or not they say MetaRush (the `metarush` CLI). Use it for: \"generate a shot of…\", \"AI video of\", \"start from this photo\", \"extend this clip\", \"more takes\", \"what would a generated shot cost\"."
---

# Generated shots

**What this covers.** Make new shots for one of the person's videos with generative video (Google Gemini) — a shot from a few words, a shot that continues from a frame of their footage or goes between two frames, a shot with an approved look of a person or character, and new takes, variations, extensions or edits of a generated clip — always priced first and made only with the price the person agreed to; and answer what generated shots cost, what was made and how. Someone's likeness only through their approved look with their recorded consent; never a child or a public figure. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs, flags or JSON). Read
[reference/cli.md](reference/cli.md) and [reference/safety.md](reference/safety.md).

"Make a shot of…" means this skill. People also start from the dashboard: **Generate video** in the drop box (under
Choose files, on Home and in a project's Sources) opens the same Generate panel, with an optional starting picture (a
photo from their computer, a picture in Resources or a frame of a clip). Every shot belongs to a project (it is paid
from that project's budget), so ask which project when it isn't clear.

A generated shot is made by Google Gemini (the video model Gemini Omni) and paid by the person, per second of video.
It lands in the project's Sources as an ordinary clip, marked as AI-generated everywhere (Sources, the Script, exports
and the quality check), with how it was made kept: the prompt, the pictures used, the model, the price, who asked. A new
take, a variation, an extension or an edit is always a **new** clip; nothing replaces the one the person has.

## The rules (never bend them)
1. **Price first, then the person's yes.** Every request starts with `metarush generate estimate … --json`. Tell the
   person what it makes and what it costs ("a 6-second draft shot, wide, about $0.23"), and what is left of the
   project's budget and today's limit. Only after they agree, run the make command with `--max-cost` set to the price
   they agreed to (never more). Never make shots on your own initiative, never "just try one", never repeat a request
   because you didn't like the result: ask.
2. **No one's likeness without their look and consent.** Never put a real person's name or description in a prompt
   to make them appear. A real person appears only through their **approved look** (`--look <name>`), which needs
   **their recorded consent**. Recording consent and making, testing, approving or withdrawing a look are things
   **only people do** (People page in the dashboard, or their own terminal); never run `metarush consent record`,
   `metarush looks create|update|approve|revoke|test` yourself and never fill in a consent statement for anyone.
   MetaRush refuses a child's likeness and a public figure's (a politician, a celebrity), even with consent; so do
   you, and don't suggest ways around it (look-alikes, "someone who resembles…").
3. **Frames with people.** Continuing a shot from a frame (`--from`) or going between two frames (`--to`) sends that
   frame. If MetaRush says it can't tell whether the frame shows people, look at it (`metarush footage frames`, then
   Read the picture) and tell the person what you see, but **they** say whether it shows real people; add
   `--no-people` only after they do. A frame that shows someone named needs that person's consent.
4. **Keys and money are the person's.** If `generate estimate` says Google Gemini isn't set up on this computer, tell
   them to add their own key under Settings → Integrations → Generative video. Never ask for a key in the chat, never
   read one from files, never run `metarush providers set`. Budgets, today's limit and the model are people-only
   settings (Settings → Integrations, or `metarush generate configure` in their terminal): say which one stops a
   request, never raise it yourself.
5. **Generated clips are marked.** Tell the person a generated clip is AI-generated whenever it goes into a cut, and
   that most platforms ask about it when posting; the delivery folder lists the clips with what each platform asks.

## 1. Ground yourself
`metarush project show <project> --json` (what the video is about, its shape: a tall project makes tall shots) and
`metarush generate settings --project <project> --json`: the model and its price per second at each quality, draft
and final quality, the project's budget and today's limit with what is spent, and whether this computer has the
key. `metarush generate list --project <project> --json` shows what was already made (and cost); look there before
offering something new, the person may already have a take they like.

## 2. Write the request with them
- A **shot from words**: what is in it, the light, the camera ("slow push in", "from above"), the mood. Keep their
  words; offer a fuller prompt and say what you added. 3 to 10 seconds (default 6). Wide (16:9) or tall (9:16).
- **Draft or final**: drafts (360p or 720p) are cheap, for choosing; final (1080p or 4K, upscaled) when the shot is
  settled. Suggest a draft first. Say that the price grows with every second and every take.
- **Takes**: `--takes 2` to `4` makes several different versions at once to choose from; each costs the same.
- **From the footage**: "continue this shot" → `--from <clip>@<time>` (the frame where the new shot starts; Read it
  first), and `--to <clip>@<time>` for a shot that goes from one frame to another. Use their clip's name and a time
  you checked.
- **From a photo**: "start from this picture" → `--from <picture>` (a picture in the project's resources, by its short
  name): the shot starts on it. A photo they have on their computer goes into Resources first (the dashboard's
  Generate panel does it; from a terminal, `metarush resources add <file> --project <project>`). MetaRush can't tell
  from a photo whether it shows real people: they say so (`--no-people`) after you looked at it with them.
- **A person or character**: `metarush looks list --json` shows the looks and whether each can be used now (approved,
  consent in force). Use only one that can be; if the person wants someone without a usable look, explain what
  they do on the People page (record consent, make a look, check its test shots, approve it).
- **Pictures**: `--image <resource>` uses a picture from their resources library (their own artwork, a product);
  if it might show people, ask them first, the same as frames.

## 3. Price it, then make it
`metarush generate estimate "<prompt>" --project <project> [--seconds 6] [--quality draft] [--takes 2] [--look <name>]
[--from <clip>@<t>] --json`. `allowed: false` comes with plain reasons (`problems`): consent missing, budget or
today's limit, no key; say them as they are. When allowed, give the price and what it buys, and ask.

After their yes: `metarush generate shot "<prompt>" … --max-cost <the price they agreed> --json`. It answers right
away (the shot is being made, usually a minute or two). Follow it with `metarush job wait <job> --json` or
`metarush generate show <generation> --json`; when it is ready, say where it is (Sources, marked AI-generated) and what
it cost. In the dashboard's Claude Code panel the same command becomes a card with the price on it; the person
presses it.

New takes of something already made (each is priced first the same way, `--dry-run` prices it):
- "Again" or "variations": `metarush generate again <generation or clip> --takes 3 --dry-run --json`, then without
  `--dry-run` with `--max-cost`.
- "A few more seconds": `metarush generate extend <clip> --seconds 5 [--prompt "what happens next"]` (a generated
  clip or their own footage; the extension is a new clip).
- "Make it night": `metarush generate edit <clip> "make it night, with the street lights on"`.

## 4. When it doesn't work
- **Declined by Google's safety filters** (`E_SAFETY_BLOCKED`): say so plainly and what it was about if Google said;
  `generate show` says what, if anything, was charged. Offer a different prompt only if the request was fine (a
  brand, a crowd, a word that reads badly); never try to get around a refusal about a person, a child or violence.
- **Consent** (`E_CONSENT_REQUIRED`): explain what's missing and that the person (or the person shown) does it on the
  People page.
- **Price changed or not agreed** (`E_CONFIRM_REQUIRED`): price it again and ask again.
- **Budget, today's limit, Google's own spending limit or too many requests**: say which, and when it frees up; a
  person may raise a budget in Settings.
- **The free tier** (`generate show` says the Google project is on the free tier): Google makes video only for
  projects with billing turned on, and nothing was charged. The person turns on billing for the key's project in
  Google AI Studio (the hint has Google's page); don't offer the shot again until they say they have.
- **Couldn't be made** (Google had a problem, the model was retired): `generate show` says why and whether anything
  was charged. Ask before trying again.

## 5. Other AI media: pictures, speech, sound effects, music, voice changes, dubs, cloud transcripts
The same rules hold for the other kinds of AI work (`metarush ai`): Google Gemini's key also makes pictures, AI speech,
music and cloud transcripts; ElevenLabs' makes sound effects, music, speech, a recording in another voice, dubs and
cloud transcripts. Price first with `metarush ai estimate <kind> [words] --project <prj> --json` (kinds: picture,
voice, sound-effect, music, voice-change, dubbing, transcription): it names the model and why (the usual one, or the
one the project's Content Settings choose), up to two other models (cheaper, better — offer them, never pick for the
person), the model's licence terms (say when they depend on the person's plan or say nothing about commercial use)
and what the service keeps. After the person's yes, `metarush ai make <kind> … --max-cost <price>`. Pictures land in
the resources library, sound and dubs in Sources, a cloud transcript replaces the clip's words (the local one is free
and stays the default: only on request). A dub in the speakers' own voices, speech in a copied voice and pictures of a
named person need those people's consent; copying a voice (`metarush ai voices copy`) and choosing which services a
client's work may use (`metarush ai rules set`) are the person's to do. When Content Settings refuse a service, say
so and what they allow (`metarush ai rules show`). `metarush ai list|show` say what was made, what it cost and what the
service still keeps.

## 5b. A presenter reading lines on camera
A presenter reads a clip's sound, or a narration line of the cut, on camera (`metarush ai make presenter --presenter
<name> --clip <clip>` or `--line <n>`, priced first with `metarush ai estimate presenter …`). Who reads it is a
person's own presenter (`metarush presenter list`: only approved ones read; a draft needs the person's test clip and
approval) or a stock presenter (`metarush presenter stock`: only where the service's licence covers commercial and
client work; say when it doesn't cover ads). A person's presenter speaks only their own voice copy, a recording of
them or a voice that is no one's. Beginning, making, testing, approving and retiring a person's presenter, recording
their consent on camera (covering a presenter, naming the service) and a service's training opt-out are always the
person's own steps: say what they do and where (Generate → Presenter, the People page), never do them. A presenter
clip is AI-generated footage like any other: disclose it.

## 6. Using a generated clip
To put a generated clip into the cut, use the usual editing workflow (video-edit-revise): it is footage like any
other. Mention that it is AI-generated, and before a video with generated clips goes out, point the person to the
delivery checklist (video-deliver): platforms ask whether a video contains realistic altered or synthetic content.

Never approve revisions, never print secrets, never spend money without the estimate and the person's yes.
