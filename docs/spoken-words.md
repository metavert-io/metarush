# Changing a spoken word

This page covers a change to the cut's sound; whose voice, whose consent and who decides; making and checking the
new words; disclosure and undo; and the mouth, on request. Code: `internal/spoken` (estimate, make, the checks and the fit),
`internal/ops/cutsay.go` (place, undo, list, show), `internal/editspec/said.go` (how a cut plays it),
`internal/cli/cutsay.go`, `internal/assistant/say_actions.go` (Claude's cards),
`web/src/components/transcripts/SayDifferently.tsx` (the dashboard).

Someone said "twelve" and meant "fifteen". MetaRush can make the cut say "fifteen" in that person's own voice copy —
only with their consent, only when a person asks, checked before anyone can use it, and marked as changed with AI
everywhere. The recording and its transcript never change: they stay the record of what was said.

## What a person sees
- **Say it differently…**: right-click a word (or a selection) in a clip's Transcript tab, or press **Say it
  differently…** on the selection bar. Type the new words. The price shows as you type (with the service, the
  model, and what is sent: the new words with the speaker's words around them). If it can't be made, the dialog
  says why in plain words: nobody named the voice, MetaRush only recognized it, someone else speaks right there,
  there's no copy of their voice, their consent doesn't cover new words with that service, no cut plays those
  words, this computer can't check new words.
- **Make it (about $0.01)** asks for it. MetaRush makes it, then checks it on this computer; the dialog follows
  along. When it passes: **What was said** and **The new words** play the two, with what the recognizer heard and
  how much the voice sounds like the person. **Put it in the cut** saves a new version of the cut and a review copy;
  the toast has **Undo**.
- If it doesn't pass ("The new words don't sound enough like Ada (38% alike; 45% needed)", "In the cut the new words
  sound like “fifty”, not “fifteen”"), nothing goes in the cut. What was paid for is on the receipt.
- **The transcripts**: a clip's transcript marks the words a cut says differently (a wavy underline; hover: which
  cut, what it says now). The cut's transcript shows the new words in their place, marked, with what was said.
- **The mouth** (on request, `metarush cut say mouth <change>`): lip sync of exactly that stretch, with everyone in
  it named and consenting to lip sync with that service. The clip that comes back plays in place of the picture
  there when the change goes in.
- **Consent**: People → a person → their consent → "It also covers": "A word or short phrase … may be changed in
  their own voice copy, kept at ElevenLabs / Google Gemini", and lip sync. From a terminal: `metarush consent record
  … --covers voice,new-words --service elevenlabs` (named uses replace the defaults, so `voice` stays listed for the
  copy itself; add `lip-sync` and `--service sync` for the mouth; `--uses`/`--services` are the same lists).

## From a terminal
| Command | What it does |
|---|---|
| `metarush cut say estimate <project> --clip <c> --from 12.3 --to 12.8 --as "fifteen"` | price it and check it may be made (also `--quote "twelve rows"`: words the cut says once; `--branch` picks the cut) |
| `metarush cut say make <project> … --as "fifteen" [--max-cost 0.01] [--wait]` | ask for it (people only, at a terminal; asks for the price first) |
| `metarush cut say place <change>` | put it in the cut as a new version, with a review copy (people only; checked first like every edit) |
| `metarush cut say undo <change> [--branch <cut>]` | a new version that plays what was said (in that cut; by default the one that plays it — another cut that plays it keeps it) |
| `metarush cut say mouth <change> [--dry-run]` | the mouth matches the new words (people only, priced first) |
| `metarush cut say list <project> [--clip <c>]` · `cut say show <change>` | the changes, how they were checked, whether the cut plays them |
| `metarush captions file <project> --format vtt [--mark-changed]` | a captions file; WebVTT always names changed words in a note, `--mark-changed` puts them in italics |

Every command takes `--json`. In Claude's panel Claude may price and read changes; asking for new words, putting
them in and matching the mouth are cards a person confirms (never Auto), taking them out is a card too. An AI app
connected to propose only can't draft any of it.

## How it works
### Who and what consent
- The words must be **one voice that a person named** (MetaRush records who named each voice: its own recognition, an
  AI app, Auto or Claude naming it isn't enough), nobody else speaking
  within 0.3 s, and — when the person has other recordings — that voice at least 40% like them.
- Only **their own voice copy** (made with their consent), at the service that keeps it; the speech is made by
  that service's best speech model.
- A **consent in force for the project that names `new-words` and that service**, and covers their voice too (new
  words are said in a copy of it; a consent for new words alone is refused). Anyone recorded as a child or a
  public figure is refused outright.
- Priced first through AI work: the project's budget and the team's daily limit, Content Settings' AI services and
  the client's What may leave (the words leave this computer) all apply.
- A consent or voice copy withdrawn later marks the change withdrawn: it can't go in a cut any more. A version that
  still plays it (or a mouth matched to it whose lip-sync consent was withdrawn or has ended) is held back from going
  out — delivery, timelines into a client's folder, posting and review links, with the ship gate off too — and
  nobody can send it out anyway: take the change out of the cut (Undo) and make a new version. The quality check
  finds it as a problem (`changed_words_withdrawn`), and a captions file's note says the consent is no longer in
  force. A cut in another project that plays them (nesting the cut they're in) is held the same way until their
  consent covers that project too. A matched mouth whose consent no longer holds isn't put in with the change (it
  says why).

### Making and checking
1. The service is sent the new words with up to ten of the speaker's words on each side (within the line), so they
   sound like part of the sentence and are long enough to compare voices.
2. On this computer the recognizer writes down what came back; the new words are found in it (numbers count the
   same in figures or words). The whole speech is fingerprinted (TitaNet) and compared with the person's voice:
   at least 50% alike for 3 s of speech, 45% for 2 s, 40% for 1 s; and so is the stretch around the new words
   alone (at least 1.5 s of it), so new words can't pass on the words around them.
3. The new words are cut out between words and fitted between the words around the old ones: sped up by at most a
   quarter when longer than the room the old words had (never slowed down; longer still is refused), at the
   speaker's level there, over a loop of the quietest pause nearby (the room's own quiet), crossfaded out of and
   back into the recording, whose own samples fill the first and last 0.1 s.
4. The recognizer must hear exactly the new words in that sound, as the cut will play it: in their sentence, with
   2.5 seconds of the recording on each side (a lone word is easily misheard). The edges of the old words, and of
   the new words in what came back, are the quietest moments next to them (recognizers' word times are approximate).
5. Passed: a clip marked as AI-generated with a likeness, saying what it replaces. The change is **ready**.

### In the cut
- `mix.said` holds each change: the recording, the stretch, the new sound, where the new words are heard, what is
  said now and what was said, whose voice. The plan crossfades the clip's own sound into the new sound at each end
  (where both are the recording's own samples) and plays the new sound as AI speech on the source bus, with the
  clip's own gain. It's anchored to the footage, so moving or trimming clips keeps it.
- Captions, the speech check of the quality check and the cut's transcript read the new words.
- Putting it in and taking it out are ordinary checked edits of the mix (a confirm token, locks, rebase).
- The new sound plays only there, and a matched mouth only over those words: no edit can put either in as a track or
  clip of its own.
- Putting it in needs the voice still named as that person by a person (renamed or named by an AI app since:
  name it again first).

### Disclosure
- The quality check: a note per change (`changed_words`), or a problem when its consent no longer holds
  (`changed_words_withdrawn`). Changes in a cut this one nests count too.
- Each export's notes: `changedWords` (where it's heard, who, before and after, the new sound and how it was
  made), and the new sound under `generated` with each platform's checklist.
- The receipt: the request ("new words in a speaker's own voice copy") and the version's cost; a nested cut's
  changes are listed with the version (priced on their own project's receipt).
- A promise that nothing generated goes undisclosed (`disclosed`) counts each change as its own part:
  "the words “fifteen” in Ada Lovelace's voice".
- Captions files: a WebVTT note (translated ones too), saying for each change whether the consent still holds;
  italics with `--mark-changed`.

## Limits
- A word or a short phrase: at most 12 words and 6 seconds changed, 20 new words.
- A clip's own recording only: not a call's separate tracks, a nested cut or a dialogue track.
- A clip played faster or slower keeps what was said there.

## Tests
`go test ./internal/spoken/` (fakes only: the fake AI service's voice copy and speech, a stand-in recognizer and
fingerprints), `go test ./internal/editspec/ -run Said`, the panel's cards in `internal/assistant`
(`TestSpokenWordRequestsBecomeCards`), and the browser run `scripts/spoken-word-e2e.sh` (ports 5580–5589; the fake
ElevenLabs speaking pre-made words in the footage's own synthetic voice, the real recognizer and fingerprints on
this computer; a Mac).
