# The receipt

A project's receipt says what left this computer for outside AI services and for Claude, what it cost, and
what came back, in a form a client can be handed. `metarush project receipt <project> [--version rN] --json` (op
`project.receipt`), the project's **Receipt** tab (Download it saves the same JSON) and, in short, every delivered
video's record (`receipt`).

## What it lists

- **Requests to outside AI services**, one line each: the kind of work (`video`, `voice`, `clean-up`, or what the
  record says: `picture`, `music`…), the service and model, when and who asked and why it ran (from the dashboard, a
  terminal, Claude's card someone kept, Auto, a run of Recipes), what was sent in plain words (the request's words
  and how long they are, each picture or frame by its name, a narration line's words, so many seconds of a
  recording), what it cost (`charged`: settled or reported by the service; `estimate`: at MetaRush's dated prices;
  `none`), how it ended, whether MetaRush deleted it at the service afterwards (`deleted`, `kept`, `failed`,
  `unknown`, with a sentence) and the record it comes from (`generations/<id>`, `voice_takes/<id>`,
  `sound_cache/<id>`).
- **Claude Code conversations** about the project: title, messages, tokens, who started it, the computer whose Claude
  Code answered and whose account and terms, and what it cost — only with an API key or a cloud provider; on
  a Claude plan it counts toward the plan's limits.
- **Requests the activity log recorded** for the project, counted by service and what they did (failures too): every
  outside request goes to the log, so a service whose records live elsewhere still shows.
- **Generated media**: generated shots and AI narration lines, marked when the priced version uses them.
- **What Auto spent by itself**: each change that cost money and ran under Auto, for whom and its estimate.
- **The samples runs made first** (`samples`): a run of Recipes with an expensive kind of work makes a sample
  first in its project; its requests are listed above like any others, and this says which of them was the sample —
  run, what it was, what it spent — and who approved it, turned it down (with what they said) or skipped it (with why).
- **Sent out anyway** (`sentAnyway`): each version a person sent out although the quality check or a sign-off
  held it ([ship-gate.md](ship-gate.md)) — who, when, their note in their words, and what held it; one line each
  ("was allowed to go out" until it went out with that note, `wentOut`), also in the delivered video's record.
- **What left this computer** (`left`; also `metarush project leaves`): each receiver things of the project
  went to — AI services with what each request sent, what Claude and AI apps were shown (the activity log's record of
  the operations that show words or pictures, on their channels) and the panel's conversations, the hosts ability runs
  reached, deliveries to sync folders and Recipes' files, review links people outside the workspace were sent (who
  for, what each showed and until when) — and cloud storage holding its things now, then each time
  What may leave kept something here ([what-may-leave.md](what-may-leave.md)). "Nothing of … left this computer" when
  none of that happened.

## Read generically

Generation records are read as documents, not as one Go type: every `generations` document of the project is a
request whatever its `kind` (video when it has none), with the usual fields read loosely — `providerTitle` or
`provider` (`gemini` → Google Gemini, `elevenlabs` → ElevenLabs), `modelTitle` or `model`, `sentPrompt` or
`prompt`, `references` and `inputs` (each by `role` and `ref`/`name`), `settled.usd`, a reported
`outcome.actual.usd`, else `estimate.usd` or `cost.usd`, `outcome.state` and `outcome.outputs[].assetId`,
`forgotten.ok`, `createdBy` and `channel`. A new kind of AI work recorded there appears on the receipt without a
change here. The AI voice (`voice_takes`, source `tts`) and a clearer voice in the cloud (`sound_cache` wet records
of mode `cloud` for the project's footage) keep records of their own and are read as such.

## The cost of the video, at cost

`cost` adds what each service charged (or, where the record has no settlement, MetaRush's estimate at its dated
prices) by service for the whole project, and prices what one version uses (`cost.version`, a `CostEstimate` whose
parts are its generated clips, its AI voice lines and the cloud clean-up its clearer copies use). A clearer copy the
version plays (`mix.enhanced[].variant`) is followed to its `sound_cache` copy record and the processed voices it is
made of (`wets`); only cloud ones are priced, so a copy made on this computer is free whatever else was made of the
same recording. A copy recorded before `wets` was kept is priced only when it says it used the cloud, from its
recording's cloud voices that overlap its stretch and were made before it, one per stretch: the stretches that fit
side by side, and of tries of the same stretch the newest. That is an estimate (the copy never said which tries it
used), but tries are never added up. Nothing is added: the money goes to the services at their own prices.
Cloud clean-up is cached by the recording (nothing is processed or billed twice), so footage
several projects share shows its cost in each project's total. The version's page
shows its cost ("Version 3, at cost: about $0.46", opening to its parts) with a link to the receipt.

## In a delivered video's record

`receipt` in the record (`metarush.delivery/1`): `costUsd` and `cost` (the version's parts), `projectUsd`,
`requests`, `services`, `deleted`, `claude` and `claudeAnswers` (the plan and the terms Claude answered under: "a
Claude Max plan, under Anthropic's consumer terms" — never whose account it is, nor its email, since the file goes to
other people; the dashboard and the CLI say whose), `generated`, `autoUsd`, `lines` (the receipt in a few sentences,
said the same way) and `command` (the full receipt).
