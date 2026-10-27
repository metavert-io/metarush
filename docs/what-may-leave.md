# What may leave this computer

What may leave is a part of Content Settings. For every project that uses a project's settings (a
client), it says which of the client's things may leave this computer, and who may receive them. MetaRush asks it
everywhere something leaves, refuses plainly, and the project's receipt lists what left and where.

Nothing set changes nothing: everything MetaRush offers may be used, as before.

## The rule

| | |
|---|---|
| **Things** | `footage` (video and sound files), `voices` (speech cut from footage, voice samples, recordings sent to be written down, changed or dubbed), `faces` (frames of footage, looks, contact sheets), `words` (transcripts, captions, scripts, prompts), `pictures` (pictures, logos, slides, frames) |
| **Places** | `ai` (AI services, by provider), `abilities` (by the host each request goes to), `claude` (Claude in the panel, Claude Code in a terminal, AI apps through MCP; by the Claude account's terms), `storage` (cloud storage), `delivery` (videos and timelines written where a sync service uploads them, and Recipes' files), `reviewers` (people outside the workspace a person sends a review link to) |
| **Per place** | `keep`: the things that never go there. `only`: who may receive the rest (providers such as `gemini` or `elevenlabs`; hosts such as `api.example.com` or `*.example.com`; Claude's terms `commercial` or `cloud`). Empty: anyone the place offers |
| **Nothing** | Nothing of these projects leaves this computer, wherever it would go |

A clip counts as footage, voices and faces. A frame of footage counts as faces and pictures; a picture from the
library counts as pictures. The person's own messages to Claude, and the project's names, rules and notes, are what
the person says; they aren't the client's material.

What may leave and the AI services part both apply: the AI services part chooses which services and models
the work uses; What may leave says what of the client may go there at all.

```
metarush content leave --project "Acme"
metarush content leave set --project "Acme" --nothing --note "the client's contract"
metarush content leave set --project "Acme" --keep ai=footage,voices --only ai=elevenlabs
metarush content leave set --project "Acme" --keep claude=words,pictures,faces
metarush content leave set --project "Acme" --only claude=commercial
metarush content leave set --project "Acme" --keep storage=all
```

In the dashboard: the project's **Settings** tab → Content Settings → **What may leave**: a grid of places and
things (ticked may go), who may receive them, a note, and who set it.

## Who changes it

Only a person (`content.leave.set` is people only: never Claude, an AI app or a script). Anyone signed in may keep
more on this computer; letting more leave than the rules did takes a person who is an owner — never Claude, an AI app
(even one that counts as the owner on this computer), an app that only proposes or Claude Code in a terminal. The same
holds for every other way a client's things end up under looser rules: using other settings (following another
project's, the default's, its own empty or kept-aside ones, a keep card's own settings; copying keeps the rule), and
moving or linking things — a clip moved or taken out of the client's project, archived out of it, or a project made to
use the client's things.

## Where it is asked

| Where | What happens |
|---|---|
| AI work (`ai.estimate`, `ai.make`, voice copies), generated video, the AI voice, voice copies, clearer voices from the cloud | The estimate says it; making it refuses (`E_KEPT_HERE`) and records the refusal; every project whose clips or pictures it sends counts; asked again just before the request is sent (it then stops at $0) |
| Abilities | The plan names the inputs a client keeps here (and the hosts MetaRush will refuse); starting it is refused and recorded; the run asks again before it starts, and its proxy refuses, request by request, hosts the client doesn't allow. What a run sends is what its approved manifest says it sends (`sends`) of the inputs it is given. The one service on this computer an owner agreed to isn't a place things leave to: a run reaches it whatever the rules say, the receipt doesn't list it, and only the ability's other hosts are refused. That program runs outside MetaRush's sandbox and may send things on by itself, which these rules can't see |
| Claude and AI apps | On their channels (the panel's Claude, Claude Code in a terminal through the skills, MCP apps) reads are denied by default: every operation says what it shows (`leave.readsShow`), one that shows kept things is refused unless every project they touch allows them — a version counts its clips', nested cuts', conversation members' and pictures' projects, a project its clips' other projects, its links and its latest versions — and one whose input names no project counts every project. The panel says what the client keeps in a line, and Claude hears it with each message. While any client keeps faces, pictures or words from Claude, the panel's Claude reads neither the scratch nor the cache folder; what it may see is copied into its conversation's folder. Scene descriptions, fitting a script and the weekly report follow the same rule |
| Cloud storage | A cloud bucket off this computer and its network, a folder a sync service uploads, or a database on a server elsewhere is cloud storage. A client whose rule keeps footage or pictures from it can't add them while the media is there, and moving the media there is refused; the part says what is there already and what to do |
| Delivery | Delivered videos and timeline exports into a folder a sync service uploads (every project the version's things belong to); Recipes' files leave out (and say why) the footage and pictures a client keeps from delivery; a Remotion version sent back to its repository (words and pictures, to that host) |
| People outside the workspace | A review link ([outside-reviewers.md](outside-reviewers.md)) sends the version's footage, voices, faces and pictures, and words when it shows what is said or its captions are in the picture; its plan says what stops it, and making it is refused and recorded for every project the version shows — for people outside the workspace, and for cloud storage when the bucket is in the cloud (the page is kept there). No list of receivers: who opens a link isn't known |
| Remotion refreshes | Its steps run with the network on: refused while the client keeps words or pictures from abilities, or lists the hosts that may receive them |
| Link imports, the browser extension | Nothing of a client leaves through them (the link goes to its site, with the person's own sign-in when they ask); what they bring in follows the cloud storage rule when it is added (footage in no project: the default's settings) |

Every refusal is `E_KEPT_HERE` with the rule in plain words, whose Content Settings hold it, who set it and when,
their note, and `details` (`place`, `kind`, `to`, `settingsOf`, `setBy`). It is recorded in the change history
(`leave.refused`) and so in the activity log. Estimates and plans only say it.

Not covered: the browsers of the person's own computers over remote access, and what Claude Code in a terminal or an
AI app opens straight from files on this computer — these rules hold only for what MetaRush hands it (the
dashboard's Claude is the exception: its folders are closed by Claude Code's own read rules).

Claude Code in a terminal is recognised by the `CLAUDECODE` variable Claude Code sets for the commands it starts.
That is a courtesy, not a lock: a command run as `CLAUDECODE= metarush …` passes as a person's. The other way round,
a shell that inherited the variable — a tmux session started from Claude Code, say — is taken for Claude Code; the
refusal says so, and a person there opens a terminal window Claude Code didn't start (or clears the variable).

Every read an AI channel can call says what it shows; one that can carry to-do questions, notifications, activity
lines, card titles, notes or log text counts as words, since those quote what was said (a test runs every read that
says it shows nothing against a client's transcript and to-do). The project so far that MetaRush writes into the
panel's first message leaves out a kept client's to-do questions and card titles.

## Proof

The receipt's **What left this computer** and `metarush project leaves <project>`: every request that reached an AI
service and what it sent, what Claude and AI apps were shown (from the activity log, for as long as it keeps
entries) and the panel's conversations, the hosts ability runs reached, deliveries to sync folders and Recipes'
files, and cloud storage holding the project's things now; then every time What may leave kept something here. When
none of that happened: "Nothing of “Acme” left this computer."
