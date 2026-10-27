# The Claude Code panel

The dashboard's **Ask Claude Code** panel holds real conversations with the person's own Claude Code on this
machine: one long-lived `claude -p` per conversation, read-only, resumed with `--resume`, stored in
MongoDB (`assistant_conversations`). Claude reads; every change it asks for is refused for it and becomes a
**card** the person decides on. This page is about how the panel talks to the person and keeps Claude
moving without making them babysit it: plain words, safe steps that run by themselves, Claude hearing when work
finishes, question cards, combined approvals and one-click "save and render". The code is
`internal/assistant`; the dashboard side is `web/src/layout/AskClaude.tsx` and `web/src/layout/assistant/`.

## What the panel avoids

1. **Technical text reaching the person**: step lines such as "Read toolu_….txt" (Claude Code's own spilled tool
   output), "Wrote index.tsx", "Read the notes on SKILL", "Ran a command → blocked", picture paths, "68 lines of
   code", a check's raw error, version codes (`name-bar@9d6f681d`), IDs (`rev_…`), millisecond times and the
   terminal command on cards.
2. **Turns spent waiting**: Claude hears when work a card started has *finished* (a render, a snapshot), not when it
   started, so it doesn't answer "still waiting / not finished yet".
3. **Open cards and questions to hunt for**: they are in a navigator, and Claude's questions are cards, not free text
   in long replies.

## Plain words

- **One describer, server-side.** Every step line is written in Go (`labels.go`, `stream.go`) and both the dashboard
  and `metarush assistant conversations show` print it. A step says what happened for the video: "Looked at 19
  stills from “Lena interview”", "Wrote the graphic “name-bar”", "Read the rest of a long result", "Read
  MetaRush's notes on graphics", "Tried something that isn't allowed here". Claude Code's spilled tool output
  (`…/tool-results/toolu_….txt`, files outside MetaRush's folders) is "the rest of a long result"; files Claude
  writes are named by what they are (a change, the cut as text, a graphic, a question, a diagram), never by file
  name; denied calls that became cards are not repeated as steps.
- **Grouping.** A step carries a group key and a plural label (`ToolCall.Group`, `ToolCall.Many`): consecutive steps
  with the same key print as one line with a count and their pictures together ("Looked at 19 stills from
  “Lena interview”"). The CLI printer and the dashboard group the same way from the same fields, so the
  wording lives only in Go.
- **One name helper.** Clips, versions, renders, projects and people in step lines and cards are named through one
  helper (`names.go`: `Manager.displayName`, quoted with `theirs` for people's own words), which is also where
  editable descriptive names come in, and only there.
- **Notes have two texts.** What Claude reads after a card (with IDs, version codes and the exact problem) is the
  turn's prompt; what the person sees is `Turn.Shown`, a plain line ("You saved version 1", "The review copy of
  version 2 is ready; the quality check found 2 things to look at"). Older conversations' notes are cleaned by
  `PlainNote` (Go) and `plainNote` (dashboard).
- **Cards.** No "lines of code", no version codes, no IDs, no commands; times are rounded to the second; the motion
  check's problems are said in plain words (`motionProblemWords`: "It can stay on screen for at most a minute");
  the exact problem, file and line go to Claude and behind Details. Pictures are thumbnails that open large
  (below); plain CLI output prints none (`--details` prints where they are). Versions read "version 2" everywhere in
  the panel (the line under each message, the cut pane, plan cards) and in `assistant proposals|apply`, never "r2".
- **Claude's own words.** The panel's system rules and the skills' voice guide say: talk like a video editor to a
  creator (versions, clips, graphics, captions, music), never about code, files, folders, components, commands or
  checks' internals; ask questions as question cards; never say "send me a message when it's done".
- **Guards.** `plainwords.CheckPanel` adds the panel's stricter rules (`rules.json` → `panel`: code file names,
  version codes, millisecond times, "blocked", "lines of code", tool IDs) to `CheckShown`; the dashboard twin is
  `checkPanel` in `web/scripts/plainwords.mjs`. Go tests feed the describer and the card builders the exact bad
  cases above; `scripts/cards-e2e.sh` checks the panel's visible text with `checkPanel`, and the plain-words sweep
  opens the panel on a conversation fixture.

## One project per conversation; the project so far; what's new

A message typed from a page without a project still carries the conversation's project and its history, so Claude
never asks for a version to be saved that was already saved and approved.

- **A conversation keeps the project it started in**, for good; one started outside any project stays about the
  whole workspace. Later messages never move it (conversations from before keep the project they have). Each message
  carries that project's context — current version, people, library, what it keeps — wherever it was typed
  (`Manager.scopeContext`); the page's selection (the version in view, the clip, the playhead) goes along only on
  that project's page, footage picked outside any project too. Another project's page is a one-line hint ("The person
  wrote this while looking at another project, “Garden show”"; for a workspace conversation "The person is looking
  at project …"). Notes, answers and the other messages MetaRush sends use the conversation's own context
  (`convContext`: its current version now, not the last page's).
- **The panel follows the project in view** (`follow.ts`, `store.ts`): opening another project shows that project's
  conversation this tab last had open, else its most recent one anyone can see, else a fresh start there
  (remembered per project in the tab's session storage, `mr.assistant.byProject`); a page outside any project keeps
  what is open, and so does a conversation opened on purpose (a link, the list). The header says what the
  conversation is about — the project (a link to it) or Whole workspace — and has a labelled **New conversation**
  button (`/fresh` stays); every row of the conversation list says its project, in full screen too; the message
  box's line says what the message carries.
- **The project so far.** A conversation that starts in a project gets, with its first message, a Go-built record
  of what happened there, newest first and bounded (9,000 characters, about 2,250 tokens; the oldest lines of the
  longest part go first and it says so): versions (number, who saved them, from Claude's card or not, when, the note,
  approved or needs changes with the person's note), renders (review copy or final, of which version, when, the
  quality check), earlier conversations on the project (title, who started it) with what was decided on their
  cards (done, not now, not quite and the person's note, answers) and the cards still waiting, and the open to-dos
  about the project's clips. The brief and rules are in every message already, so they aren't repeated. Claude
  reads it again with `metarush project so-far <prj_…> --json` (op `project.sofar`, on its read-only list), and
  the summary request (`/compact`) carries it so it survives summaries.
- **What's new.** Each conversation records the MetaRush version of its latest message (`metarushVersion`). The
  first message on a newer build starts with "MetaRush was updated from X to Y. New things you can do: …" (the
  CHANGELOG headlines between the two, at most 12) "… If you told the person earlier that something wasn't
  possible, check again." Once per upgrade; a conversation from before starts recording with its next message.

## Not quite, Not now, turned-down notes

- **Not quite…** on every card that does something (one card, each row of a combined card, a plan card; question
  cards already take free text and a keep card is kept or not): an inline note ("What should change?") marks the
  card `not_quite` (who, when, the note) and goes to Claude at once as the person's own message: "Not quite: <title>.
  <note>. Make a new version with that change." A plan card's note goes the same way (it is a message, the plan
  isn't marked). CLI: `metarush assistant act <conversation> <card> --not-quite "<note>"` (people only).
- **Not now means wait.** Claude reads "Not now: <title>. Leave it for now; I may come back to it." (it used to read
  "Don't ask for it again unless I bring it up", which made it drop a requested change for good). A keep card's no is
  for good (that is what keeping means): its button reads **Don't keep**, and Claude reads "Not kept: …".
- **Turned-down notes reach Claude.** Rejecting a version with "What should change?" or turning down a motion graphic
  with a note — in the dashboard or with `metarush revision approve --reject --note` / `metarush motion approve
  --reject --note` — sends the note to the conversation that made it as that person's message ("I turned down
  version 7: …", Claude also gets the version and "Make a new version with that change"). Which conversation: the
  version's origin when Claude's plan saved it, else the project's latest conversation whose done card links to or
  names it; for a graphic, the latest conversation whose card checked it. The daemon holding the conversation sends
  it at once (queued behind a running reply like any message); from a terminal command or another computer it waits
  in the conversation's `inbox`, which the holding daemon sends within a few seconds (or before the next message).
  `revision.approve` / `motion.approve` answer `toldClaude` (the conversation, or nothing); with nothing, the
  dashboard offers **Send to Claude**: a fresh conversation in the project with the note filled in.

## Ask first or Auto

Each person chooses, in the switch by the message box (or `metarush assistant mode ask|auto`; ops `assistant.mode`,
`assistant.mode.set` people only; kept on their `users` record, so it follows them everywhere):

| | Ask first (default) | Auto |
|---|---|---|
| safe steps (a check of Claude's graphic, a review copy, a link you typed) | run by themselves | run by themselves |
| saving a version (with its review copy), sound settings (not the cloud), transcript and footage fixes, names, links Claude found, motion checks, making resources, notes | wait for your OK | run by themselves, as you |
| anything priced (a cloud voice, generated video, AI narration) | wait for your OK | wait for your OK — unless your estimates are off **and** a person set the project's budget and the team's daily limit with every service the card spends with: then they run by themselves, as you, within them |
| final renders, approving a version or graphic, consent and looks, deleting, setup, questions, keep cards, people-only decisions | wait for your OK | wait for your OK |

- A priced card carries its estimate: "Estimate: about $0.40", opening to the services, models, sources and
  each part's price. "Estimate costs of outside AI services before edits" is each person's, in Settings → Preferences
  (on by default; an owner can ask everyone for estimates, which keeps priced cards waiting under Auto too).
- A card Auto ran says "Done by itself — Auto is on. Change" ("…, and your estimates are off" for a priced one, whose
  Change opens Settings → Preferences), what it did and a way to the version; Claude hears it at
  once (like a safe step). It runs as the person the card is for (in a shared conversation, the mode of the person
  whose message Claude was answering), on the human channel with `Via: assistant-auto`; a save is checked with its
  confirm token first, like a terminal (only the dashboard channel saves without one). A change that can't be saved
  as it stands (its check found problems) waits.
- The machine's `assistant.safeSteps` (Settings → Claude Code, "Let Claude's steps run by themselves on this
  computer") is the owner's master switch: off, nothing runs by itself, Auto isn't offered and choosing it is refused.
- **Auto spends only within limits a person set**. Each outside service tells MetaRush its
  limits for a project (`ops.RegisterSpendingLimits`, keyed by the service's name in estimates): generated video's
  project budget and the team's daily limit (`generate configure`), ElevenLabs' project budget in credits (it has no
  daily limit for the team, so its cards ask). A limit counts only when a person set it — the usual budget and the
  usual daily limit don't, nor does "no limit" — and who set it and when is kept (`budgetBy`, `dailyCapBy`,
  `budgetCreditsBy` on the provider settings; setting them is people-only, and limits set before this was kept don't
  count until a person sets them again). A priced card
  whose limits aren't set waits with the reason on it ("Auto didn't start it: nobody set the team's daily limit for
  Google Gemini. Set the limits"); a conversation outside any project never spends by itself, and a card paid from
  another project than the conversation's (`AssistantAction.spendsIn`, the project its operation pays from: a take's
  parent's, a clip's own) asks. Each change Auto ran
  that cost money is recorded (`auto_spending`: for whom, the project, the card, its estimate, the work it started); what
  it spent is what that work settled at (a refused or failed shot costs nothing), "up to" its estimate while it is still
  being made: the project's receipt lists it, and the bell has one note a day per person (the day on the host's
  clock), updated as the day goes on and when the work ends, opening Settings → Preferences → What Auto spends ("Auto spent $0.84 by
  itself today", opening the project's receipt). `assistant.spending` (`metarush assistant spending [--project
  <p>]`) says, service by service, whether Auto may spend in a project, who set each limit, and what it spent for you
  today.

## Times

Messages and decided cards say when, readably (`stamp()`: "Today 3:10 PM", the full date on hover): under each
message, in each reply's footer, and on decided cards ("Approved by Maya · Today 3:10 PM", "You said not quite:
“shorter” · Yesterday 9:12 AM", "Done by itself — Auto is on · …").

## Code folded away

Code longer than six lines in Claude's replies folds behind **See code (N lines)**. A motion graphic's review page
shows its test frames and preview first; its source is behind **See code** (the "Only what changed" option is inside).

## Pictures open large

Any picture in the conversation (what Claude looked at, a motion graphic's test frames, its contact sheet) and its
preview video open in a lightbox (`web/src/ui/Lightbox.tsx`): ‹ › or the arrow keys step through the pictures of
that step or card, Esc closes it, the preview video plays large; swipe on a phone; same-origin URLs only, so it
works over remote access. Cards about a motion graphic carry its test frames (`AssistantAction.Frames`, labels and
same-origin URLs) and its preview (`AssistantAction.Preview`).

## Safe steps run by themselves

A setting, on by default: `assistant.safeSteps` in config.yaml, `metarush assistant settings --safe-steps on|off`,
Settings → Claude Code. It is the computer's master switch: off, nothing runs by itself (not even under a
person's Auto). When it is on, a card for a **safe step** starts by itself as soon as Claude asks (and with Auto, more:
see "Ask first or Auto"):

| Safe (free, local, reversible) | Waits for the person |
|---|---|
| checking a motion graphic Claude wrote in this conversation (`motion check`) | approving or withdrawing a motion graphic, approving a version |
| a review-quality render (`render submit --quality review`) | final renders, export, anything outward-facing |
| a snapshot or download of a link **the person typed in this conversation** (`resources add <link>`) | links Claude found itself (a page it read, a repository's README) |
| importing a video from a link the person typed (`import <link>`; no playlists, no browser sign-in) | playlists, imports with the browser's sign-in |
| | saving a version (plans, cut sheets, edits), anything that spends money (AI voice, cloud clean-up, generated video) |

The card shows **Started by itself** (with a way to the setting) and its progress; the person can still cancel a
render. It runs as the person the card is for (whose message Claude was answering) on the human channel,
never on Claude's read-only channel: the op's actor is that person with `Via: assistant-auto`
(`models.ViaAssistantAuto`, logged on the dashboard channel), the audit event `assistant.action` records `auto:
true`, and the activity log says "Claude Code started this by itself, as your setting allows". Links count as typed
when the card's address matches one in people's own messages of this conversation (scheme, `www.` and a trailing
slash ignored) — never one from a page or file
Claude read, so nothing Claude reads can make MetaRush fetch something by itself. Only the dashboard daemon starts
safe steps: in a conversation a terminal holds (no daemon running) they wait for `metarush assistant act` like any
card.

## Claude hears when work finishes

- A card whose op starts async work records it (`AssistantAction.Work`): a render (and the quality check that
  follows it), a fetch job (snapshot, repository, download) or an import job. The card says what is happening;
  Claude is **not** woken when the work starts.
- The machine that holds the conversation watches the work (`work.go`): every few seconds it reads the render, its
  quality check job and report, or the jobs, from the database (so work done by another machine counts). When the
  work ends — finished, failed, canceled, or still not finished after 12 hours — the card gets its outcome and
  Claude gets one note with it: the render is ready (or why not) with the quality check's verdict and its top
  findings, the snapshot's resource name, the imported clip. Claude carries on by itself.
- **Batching.** Cards are told to Claude by one function (`tellClaude`): everything decided or finished and not yet
  told goes into one note. Cards decided together (Approve all, or save and render) are told together, once all
  their work has finished; work that finishes on its own is told a short moment after the last change, so several
  finishing close together make one note; a note waiting in the queue behind a running turn takes in later news
  instead of queueing another. A decision isn't held for the turn's other cards: Claude often offers alternatives,
  and the one not chosen would hold the news forever.
- The watches are part of the conversation document and survive a restart: on start the daemon resumes watching
  every conversation of this machine with running work (a sync step that was running in the background is put back
  for a click). Synchronous steps (a motion check, which draws its test frames inside the call) need no watch: the
  card finishes when the call returns.
- News is neither lost nor repeated across restarts and upgrades: the conversation also stores how many outcomes
  Claude hasn't heard (`untold`), so a MetaRush that stopped before the note went sends it when it starts again (one
  that is stopping starts no Claude Code). Cards an earlier MetaRush decided carry no batch and count as told (it told
  Claude the moment they were decided), so the first message after an upgrade doesn't repeat old news.
- Only the dashboard daemon does any of this in the background. A terminal command has its own manager
  (`assistant.NewTerminal`; `assistant.For` makes one too) even while the daemon runs — read-only commands such as
  `metarush assistant conversations show` don't go through the daemon — so it never starts safe steps, follows work,
  resumes what a stopped MetaRush left, or wakes Claude: it would race the daemon, and anything it started would end
  with the command. With no daemon running, a card decided in a terminal waits to be told: the person's next message
  brings the conversation's work up to date and takes the news along (or the daemon tells Claude when it starts).
- A finished note that leaves new cards for the person notifies them like any reply that needs them.

## Usage and memory

- **Tokens, not dollars.** A conversation's limit is tokens (`assistant.tokenLimitMillions`, default 50M; `/limit`
  in the message box, `metarush assistant configure <id> --limit <millions>`): new input, writing and reading the
  prompt cache, output and summaries. Every step reads the whole conversation again, so cache reads are most of
  it. MetaRush counts each reply's usage as it streams (one count per reply ID, helpers' replies too) and stops a
  message that reaches the limit there (an interrupt; the turn ends "Reached the limit"); at the end of each
  message it counts from Claude Code's running totals for the session (`modelUsage`, which carry across resumes
  and include a summary's own tokens), as the change since the last total. A conversation from an older version
  starts counting with its next message. Raising the limit needs no restart (MetaRush enforces it).
- **A Claude plan has no dollar cap.** Its usage counts toward the plan; Claude Code's `rate_limit_event` says how
  much of the plan's five-hour and weekly windows is used, which Settings and `assistant status` show. With an
  API key `maxBudgetUsd` stays (real spend) and is the only case `--max-budget-usd` is passed.
- **Summaries.** When Claude's memory passes `compactAtTokens` (default 200,000) MetaRush sends Claude Code's own
  `/compact` with what to keep for a video edit, right after a reply (the cache is warm, nothing waits). It runs as a
  turn of its own (`Turn.Compact`, shown like a note: "Summarizing the conversation so far…"); `compact_boundary`
  gives the memory before and after, shown as a notice. It isn't tried again until the memory grows by half the
  setting past the last summary (a failed one counts as reached). `--autocompact` (`autoCompactTokens`, default
  300,000) is Claude Code's own summary point inside one long message.
- **Cache.** `cacheHour` (default on) starts Claude Code with `CLAUDE_CODE_PROMPT_CACHE_TTL=1h` (and the same for
  helpers), so a pause doesn't make the next message read everything again at full cost. A resumed process reads
  the cache like the old one did (the start of the conversation is the same), so ending idle processes is fine.
- **News with the next message.** With `holdNews` (default on) `tellClaude` starts a note only when one of the
  ready outcomes is urgent — a safe step that ran by itself or anything that failed — and then tells everything
  ready; the rest goes with the person's next message (`takeNews`). Outcomes are marked told only when the message
  carrying them was sent (`untellLocked`).
- **Keeping it light.** Claude may run a helper (the Task tool, under the same allow and deny rules) for heavy
  looking, the system rules ask it not to repeat output and to read the project again rather than keep it, and the
  CLI prints `--json` on one line when Claude is the reader (`METARUSH_VIA=assistant`).
- **Settings → Claude Code (Usage and memory)** (`UsageSettings.tsx`) shows the plan's usage and every setting;
  `metarush assistant settings --limit N --compact on|off --compact-at 200k --autocompact 300k --cache-hour on|off
  --hold-news on|off`. The panel header shows tokens used of the limit; each reply's footer its tokens.

## Question cards

Claude asks the person something by writing `<proposals>/<short-name>.question.json` and ending its reply:

```json
{"question": "Which of these shorts should I set up?",
 "choices": ["The garden in spring (about 0:55)", "Building the shed (about 0:50)", "Lessons learned (about 0:55)"],
 "multiple": true, "other": true, "why": "Each one becomes its own vertical short; the main video isn't touched."}
```

- `choices` (up to 12): pick one, or tick several with `multiple`; `other` adds a box for an answer of their own;
  no choices means a free-text question; yes/no is two choices. At the end of the turn each file becomes a card.
- The person answers on the card (or `metarush assistant act <conversation> <card> --choice 2 --choice 3 --answer
  "…"`); the answer goes to Claude **as the person's reply** ("My answer to “Which of these shorts…”: …"), shown in
  the thread as their message. "Not now" sets it aside and tells Claude like any card.
- Question cards count with the other cards everywhere: the conversation's `pendingActions`, the list, the
  navigator.

## One card for several approvals; save and render

- **Combined card.** When one turn leaves two or more cards waiting for the person, the dashboard shows them as one
  card: each with its title and key detail, **Approve all**, or decide each (and open one for its details).
  `assistant.act` takes several cards (`actionIds`; `metarush assistant act <conversation> <card>… | --all`): they
  run in order (approvals before saves, since a save may need an approval), each card gets its own outcome, and
  Claude hears about all of them in one note. A people-only card in the set makes Approve all ask once, with the
  list.
- **Save and render a review copy.** A card that saves a new version of the cut (a plan, the cut as text, a
  transition, a small edit: `AssistantAction.SavesVersion`) offers **Save and render a review copy** first and
  **Save only** second. With `reviewCopy` (`--save-only` turns it off on the CLI) the new version is saved and its
  review copy queued in the same step; Claude hears once, when the copy and its quality check are done.

## Keep this? cards

When a conversation brings in something reusable — a link the person typed, a picture they dropped into the message
box, a logo a snapshot found, the goals and audience, a style rule, a fact about a person, a sign-off, a problem raised
a second time, something MetaRush couldn't do, a spelling — MetaRush asks once with a **Keep this?** card (Keep / Not
now), never automatically and never twice. Links, attachments and logos are found by MetaRush itself at the end of
each turn; Claude reports the rest with `metarush keep <kind> …` (refused for it, turned into the card). Every turn's
message carries the project's rules and brief, so Claude follows what was kept. Details: [keeping.md](keeping.md).

- **The card** (`AssistantAction.keeping`: the record, the kind, the person's words, what keeping does in one
  sentence, who noticed it, a logo's colours; `image` for a picture) runs `keep.add` as the person; Don't keep
  (`dismiss`) records the thing as declined (`keep_items`), so it is never offered again in that project. Keep cards are
  never safe steps and never part of a combined approval: each is decided on its own, from the open-questions
  navigator too.
- **Attachments**: pictures and PDFs dropped, pasted or picked in the message box (`assistant.attach`, then
  `attachments` on the message; `--attach <file>` at a terminal) are copied into the conversation's proposals folder
  when the turn starts and named in the message, so Claude can Read them; the person's message shows them.
- **What the project keeps, in every message**: the project's rules (and what the quality check measures), the house
  rules of its Content Settings (labelled "House rules from “fernbrook”'s settings"), the brief and where the video
  goes, the sign-offs still waiting, and what was offered to keep before (kept, not now, waiting). A "Keep this
  rule?" card asks where: just this project, or every project using its settings (a house rule).
- Call recordings and memory have cards too: `footage find-switches`, `cut flashes`, `cut names`,
  `project rules remove`, `cut signoffs done|undo|remove`; the read commands `footage switches`, `project rules list`,
  `cut signoffs list` and `keep list` are allowed.

## Open questions navigator

By the message box: a badge with how many cards and questions wait for the person in this conversation, and ‹ ›
that jump to each (scroll and highlight). The conversation list shows the same count per conversation ("3 waiting
for you"), the panel's list button shows the count in other conversations, and the top bar's Ask Claude Code button
shows the total (`AssistantStatus.waiting`: the cards for the person on this machine, in anyone's conversation). All
of them count only the person's own cards (below); a card for someone else is never counted for two people.

## Whose Claude answers

The panel runs the Claude Code of the computer that holds the conversation — the host — signed in to the host's
Claude account. Everyone who uses the panel on that MetaRush (a colleague over Tailscale, a link session) is
answered by that account, and what they ask, with the transcripts and frames Claude reads, goes to Anthropic under
that account's terms. So the panel says so instead of leaving it implied:

- **Read from Claude Code itself.** The availability check reads `claude auth status --json` (loosely: its fields
  differ between versions) into `AssistantStatus.account` (`ClaudeAccount`: `machine`, `email` and `organization`
  when it says, `plan` — free, pro, max, team, enterprise, plan (one it didn't name), api, bedrock, vertex, foundry or
  unknown — `planName`, `terms`, `termsName`, `dataUse`, `who`).
- **Terms.** Claude Free, Pro and Max are under Anthropic's consumer terms: whether what Claude reads may be used to
  improve Claude follows the account's privacy settings at claude.ai. Claude Team and Enterprise and an API key are
  under Anthropic's commercial terms (not used to train models); Bedrock, Vertex AI and Foundry under the cloud
  provider's terms for Claude. A plan Claude Code didn't name is said to be consumer unless it is a Team or
  Enterprise plan; a sign-in it didn't describe is "terms MetaRush can't tell" — nothing is assumed.
- **On every conversation.** Each message records the account that answered it (`Conversation.account`, with when);
  the thread starts with "Answered by Claude Code on Studio, signed in as … (Claude Max) · Anthropic's consumer
  terms" (open it for what the terms mean), a new conversation says who will answer, and `metarush assistant
  conversations show` prints it. Settings → Claude Code begins with **Whose Claude answers** (readable by everyone;
  changing the sign-in is done in Claude Code on the host). `metarush assistant status` prints it too.

## What a client keeps from Claude

A client's Content Settings can keep their words, pictures or faces from Claude, or let only an account under
commercial terms read them ([what-may-leave.md](what-may-leave.md)). The conversation then says so in a line above
the thread, and Claude hears it with each message; the operations that show transcripts, captions, cut sheets,
frames or contact sheets refuse that client's projects on Claude's channel (`E_KEPT_HERE`), and searching every
project is refused while any client keeps its words. A conversation about such a client can't read the frame and
cache folders at all; any other can't read that client's clips' frames. Scene descriptions and fitting a script ask
first, and the weekly report leaves those clients' log entries out.

## Conversations people share

Remote access signs people in as themselves, while a one-time link signs a browser in as the person who
runs MetaRush, so a conversation can be "someone else's" although you had it. Conversations are not private to
whoever started them: every machine of the workspace reads them, and opening or continuing one never checked who
started it. So the panel lists them for everyone and says whose each part is.

- **Who started it**: `Conversation.userId` / `userName`; `mine` (computed for the person asking). The list's rows
  say "You" or "Started by Maya" once someone else's conversation is among them.
- **Whose list**: a project's list is everyone's conversations in that project; the workspace-wide list (outside a
  project, or "Every project" from a project) is the person's own. **Everyone / Just mine** changes either, shows once
  someone else has a conversation in the list's scope (`ConversationListOut.others`), and is remembered per viewer
  (`mr.assistant.whose` in the browser's storage; until it is picked, each list keeps its default). "Pick up where you
  left off" on a new conversation lists the same way. The operation: `assistant.list` with `projectId` defaults to
  everyone's, without to the asker's; `allUsers` / `mine` force either; `allUsers` in the answer says which it was.
- **Who sent each message**: `Turn.userId` (and the turn event's), with `user` the name: the person who typed it,
  the person who answered a question card, or for a note (an outcome told to Claude) the person whose decision it
  reports — the latest one when a note tells several; for a safe step or work that finished by itself, the person
  that card was for. Claude reads "(from Maya)" in each message. The thread puts "You" or the name over every
  message once someone other than the viewer wrote in the conversation; the turn's audit event (`assistant.turn`) and
  its activity-log entry are the sender's. Messages from older versions carry no person: they are the person's who
  started the conversation.
- **Who a card is for**: the person whose message Claude was answering when it asked (`AssistantAction.forId` /
  `forName`, computed from the turn's sender, never stored). It counts only for them: their badge, their navigator,
  the conversation's `waitingForYou`, and the "Claude Code needs your OK" notification for that reply. The stored
  per-person counts (`Conversation.waitingFor`) let lists and the badge count without loading the cards;
  conversations saved by older versions count all their cards for the person who started them.
- **Someone else's card is never lost**: everyone who opens the conversation sees it in the thread, marked "Waiting for
  Theo" (the combined card: "Claude needs Theo's OK on 2 things"), the header says "1 thing waits for Theo", the list row
  "1 waiting for Theo". Anyone there may decide it (approval counts for any signed-in person); the decision is
  theirs (`by` / `byId`, the audit, the log), and the card then says "Done by Maya" / "Maya said not now" to
  everyone else; a question card answered by someone else says "Maya's answer". A safe step runs as the person its
  card is for.
- **Archiving** hides a conversation for everyone, so the list offers the quick Archive button only on the person's
  own; someone else's can still be archived from its menu, and restored by anyone.

## CLI

| Command | What |
|---|---|
| `metarush assistant act <conversation> <card>… [--all] [--save-only] [--choice N]… [--answer "…"] [--dismiss] [--not-quite "…"] [--quiet]` | decide one or several cards, answer a question card, send one back with what should change (people only, at a terminal) |
| `metarush assistant settings --safe-steps on\|off` | the computer's master switch: steps run by themselves (and Auto is offered), or every step waits for a click |
| `metarush assistant mode [ask\|auto]` | yours: Ask first, or Auto (changes that can be undone run by themselves); choosing is people only |
| `metarush project so-far <project>` | what happened in a project so far (what a new conversation there starts with) |
| `metarush assistant settings --limit N --compact on\|off --compact-at 200k --autocompact 300k --cache-hour on\|off --hold-news on\|off` | usage and memory |
| `metarush assistant configure <id> --limit <millions>` / `/limit N` | one conversation's token limit |
| `metarush assistant conversations show <id>` | the thread in plain words, who wrote and decided what (`--details`: IDs, the commands to run cards, where pictures are; `--raw`: exact prompts and tool I/O) |
| `metarush assistant conversations list [--project <prj>] [--mine \| --all-users]` · `metarush assistant status` | what waits for you, per conversation and in all; with `--project` everyone's conversations in it (`--mine`: yours), without it yours (`--all-users`: everyone's), others' saying who started them |

## Tests

- `internal/assistant`: `TestStepLinesSpeakPlainly`, `TestCardTextsSpeakPlainly` (the exact bad cases above),
  safe steps (`TestSafeStepsRunByThemselves`), finish notes and batching (`TestClaudeHearsWhenWorkFinishes`),
  question cards, combined decisions and save + review copy.
- `internal/assistant`: conversations people share (`TestSharedConversationsArePeoples`: the lists' defaults,
  `allUsers`/`mine`, `others`, every message, card, note, answer and decision attributed, the badges per person, the
  stored counts, the turn's audit and notification), older conversations (`TestOlderConversationsAreTheirStartersCards`),
  notes and safe steps (`TestNotesAndSafeStepsArePeoples`).
- `internal/assistant` (`feedback_test.go`): a conversation keeps its project across pages and a workspace
  one stays one; the project so far and its bound; what's new once per upgrade; Not quite (state, note, the person's
  message) and the new Not now line; turned-down notes reach the conversation (at once, through the inbox from a
  terminal, none for a hand-made version; a graphic's conversation); Auto runs what can be undone and still asks for
  the rest; the master switch.
- `internal/cli`: the conversation printer's plain output (`TestConversationPrintsPlainly`, `TestConversationSaysWhose`).
- `scripts/cards-e2e.sh`: the navigator, the lightbox, the combined card, question cards, a safe step that runs by
  itself (a motion graphic's check in the jailed browser and a review render) and the notes when they finish,
  checked in a real browser with the fake `claude`; screenshots desktop light/dark and phone.
- `scripts/remote-e2e.sh` (two people): Sam starts a conversation in a project; Maya, signed in with her
  Tailscale account, finds it ("Started by Sam Lee"), "Just mine" is remembered across a reload, she continues it
  and decides the card her message brought (her badge and navigator only); Sam's view says who wrote and decided
  what; the command line lists the project's conversations the same way.
