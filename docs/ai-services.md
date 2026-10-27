# AI services

Besides generated video ([generative-video.md](generative-video.md); Veo through a person's own Google Cloud project), MetaRush makes media with the AI services the keys on this computer already cover — nothing new to
sign up for:

| Kind of work | Google Gemini key | ElevenLabs key | Where the result goes |
|---|---|---|---|
| Pictures | Nano Banana 2.1, Nano Banana Pro | — | the resources library |
| AI speech | Gemini 3.8 Flash TTS, Flash-Lite TTS | Eleven v4, Multilingual v2, Flash v2.5 | Sources, as a clip |
| Voice copies | voice replication (a sample and a consent reading) | instant voice copies | a voice to speak with (ElevenLabs' also join the narration voices) |
| A recording in another voice | — | the voice changer | Sources, as a clip |
| Sound effects | — | Eleven Sound Effects v2 | Sources, as a clip |
| Music | Lyria 3.5 (a song, priced per song) | Eleven Music v2 and v2.5 (priced per minute) | Sources, as a clip |
| Dubs | — | ElevenLabs dubbing | Sources, as a new clip (the picture kept, the sound dubbed) |
| Transcripts in the cloud (on request) | Gemini 3.5 Transcribe | Scribe v2 | the clip's words |
| A picture's background taken away | — | — | the resources library (on this computer: Apple Vision) |
| A picture made larger | — | — | the resources library (on this computer: Real-ESRGAN) |
| Lip sync (with consent) | — | — | Sources, as a clip (sync., its own key: lipsync-2, lipsync-2-pro, sync-3) |

**Presenters** — a presenter reading your lines on camera, a person's own (made with their consent recorded on
camera) or a stock one — are a kind of AI work too, on a D-ID key: [presenters.md](presenters.md).

Every request is **priced first and sent only with the price a person agreed to** (or, for a person whose estimates
are off, within the budgets), counts against **the same project budget and team daily limit as generated
video**, and what comes back is **marked as AI-generated everywhere**, with how it was made kept on a
generation record.

## Models on this computer
Some kinds of work also have models that run on this computer: **free, no key, and nothing leaves it**. They are
providers like any other (provider `local`, "This computer"): the same estimate offers them — a cloud transcript's
estimate shows "Parakeet v2 on this computer: free" as the cheaper alternative — and the same records, receipt and
activity bar follow them. They need no price agreed. A project whose Content Settings allow no AI services still
allows them (nothing of the client's leaves), unless the settings name `local` among the forbidden providers.

| Kind of work | Model | Licence | Set up | Speed (Mac mini, M-series, 4 threads) |
|---|---|---|---|---|
| Transcripts | Parakeet v2 (English) | CC BY 4.0 | by bootstrap or the setup check (nothing downloaded when it runs) | 199 s of speech in 4.6 s (0.023× real time) |
| Transcripts | Whisper (large-v3 turbo, every language) | MIT | the same | 199 s in 4.7 s; Spanish 519 s in 13.4 s; French 873 s in 29.4 s |
| AI speech | Kokoro v1.0: 41 voices in English (American, British), Spanish, French, Hindi, Italian, Portuguese | Apache-2.0 | `metarush ai local setup kokoro-v1` (about 370 MB: the speech program and the model) | 29 s of speech in 8.8 s (0.30× real time) |
| AI speech | Kitten nano: 8 English voices | Apache-2.0 | `metarush ai local setup kitten-nano` (about 50 MB) | 52 s of speech in 8.1 s (0.16×) |
| Background removal | Apple Vision subject lifting (Mac, macOS 14+) | your picture | nothing: MetaRush's own helper | 0.24 s a picture (512 × 512) |
| Larger pictures | Real-ESRGAN general x4v3 (2× or 4×) | BSD-3-Clause | `metarush ai local setup real-esrgan-x4v3` (about 5 MB) | 512 × 512 out in 0.45 s; 4096 × 4096 in 12 s |

`metarush ai local` lists them with whether each can run here and what setting it up downloads; `ai local setup` is
for owners, at a terminal or from the dashboard. Downloads are made only when a person asks: each file is the
publisher's own (sherpa-onnx's GitHub releases; Qualcomm AI Hub's export of Real-ESRGAN), checked against its SHA-256
before anything is unpacked, unpacked without links or anything outside its folder into
`<home>/components/local-models/`, and a program in it (the speech program) is checked against the SHA-256 written
down at setup before every run. Tests never download (`go test` reaches only a server on this computer).

**How they were chosen and measured** (25 October 2026; `go test -tags localeval ./internal/aiservices/local/`):
- **Transcripts**: the recognizers MetaRush already uses for every clip, so accuracy is theirs: Parakeet
  0.8% word errors on a 199 s address, whisper.cpp 2.2% in Spanish and 4.0% in French measured on public-domain readings
  (`scripts/fetch-speech.sh`, `TestMultilingual`). The cloud services' accuracy on the same recordings hasn't been measured: that is a
  paid live check (about $0.01 with Scribe v2).
- **Offline voices**: picked by licence first — Apache-2.0 for the model and its voices, so what they say can be used
  commercially; Piper's voices were left out because their licences differ voice by voice and the best-known
  English one (lessac) was trained on a dataset licensed for research only. Of sherpa-onnx's Apache-2.0 models,
  Kokoro v1.0 has the most voices and speaks six languages MetaRush can use; Kitten nano is a tenth of its size and
  twice as fast, English only. Each said five narration sentences, which Parakeet then
  wrote down: every word came back except numbers written as digits ("14" for "fourteenth") and one "runner" heard
  as "runna" (Kokoro, American voice); a Spanish sentence (Kokoro, ef_dora) came back word for word through
  whisper.cpp. Microsoft's DNSMOS gives all four candidates (Kokoro v1.0, Kokoro v0.19 int8, Kitten nano and mini)
  3.3–3.4 overall, so it doesn't tell them apart; Kokoro's int8 build was slower here than its full one (0.69× real
  time against 0.30×) and was dropped. How they compare with the cloud voices hasn't been listened to side by side:
  that is a person's check (the cloud side costs a few cents).
- **Larger pictures**: five 512-pixel photographs were made four times smaller and then four times larger again; the
  model came 0.4 dB closer to the originals than Catmull-Rom (25.5 against 25.2 dB PSNR), most on fine texture (a
  zebra's stripes +1.9 dB), slightly behind on smooth areas — and visibly sharper, with clean edges and no seams
  between its 128-pixel tiles. PSNR favours smooth guesses, so the gain people see is larger than the number.
- **Background removal**: the same subject mask as the pictures fix "Remove the background", 5 of 5 photos cut
  out. A cut-out is the person's own picture with nothing new made, so it isn't marked as AI-generated (its
  lineage says how it was changed); a larger picture's detail is made up by the model, so it is.

## Lip sync
`metarush ai make lip-sync --clip <footage> --from 12 --to 18 --sound <clip>` matches the lips of the people speaking
in that part of the footage to the new sound (a word changed, a line said again). It is a
likeness: every face in that part (as the footage's look found them) must be named by a person, and everyone seen or
heard in it, and the voice in the new sound, must have consented to lip sync with that service for this project
(`metarush looks consent record … --uses lip-sync --services sync`; an AI voice's voice copy consent counts only if it
says so too). Footage MetaRush hasn't looked at, or where it can tell no face apart, is refused, and so is anyone
recorded as a child or a public figure. The on-screen look samples about once a second, so before anything is sent
MetaRush looks at every quarter second of the part (on a Mac, with Apple Vision) and stops if a moment shows more faces
than people named there; a computer that can't look at what's on screen doesn't send lip sync. What it makes is marked as a likeness. The sound may not be longer than the
footage. The first provider
is sync. (its own API key, `metarush providers set sync`): $0.05 a second of footage with lipsync-2, $0.083 with
lipsync-2-pro, $0.13 with sync-3 (its Hobbyist and Creator prices of 25 October 2026; bigger plans pay less, and its
Free and Hobbyist plans add a watermark). The footage and sound are uploaded directly, the result downloaded, and the
generation and both uploads deleted at sync. right after; its privacy policy says it may train on what is uploaded and
names no opt-out, which the estimate shows.

## Abilities that provide a kind of work
An approved ability whose manifest says `provides: [<kind>]` is a provider of that kind here too (`ability-<name>`):
in the models list and the estimate with its own price and data terms, chosen with `--model ability-<name>/<name>`,
held to Content Settings, consent, budgets and who may start its runs, and run in the sandbox as a run of the AI work.
What it may provide and the inputs each kind fills: [abilities.md](abilities.md).

## Where it is
- **Studio → Sources → Generate**: **What to make** switches the panel from Video (unchanged) to Picture, Speech,
  Sound effect, Music, Voice change, Dub, Transcript, Voice copy, Cut-out, Larger picture or Lip sync. Each kind has its own fields (the words; pictures
  to change; the clip and the part of it; the voice; the length; no singing; the language to dub into; the speakers'
  own voices). The price shows before anything is sent, with **the model and why** (MetaRush's usual one, the one you
  chose, or the one the project's Content Settings choose), **up to two other models** — the cheapest that costs
  less and the best ranked that ranks higher, one click away — the model's **licence** and **what the service keeps**
  (behind Details). The button carries the price. AI work for the project is listed under the panel with where it
  landed, what it cost, **Stop** while it runs, and a note when the service keeps what it made.
- **The project's Settings tab → Content Settings → AI services**: nothing goes to any AI service; which services may
  be used; a model chosen for a kind of work (all of a client's music from one model); a note saying why (shown when
  something is refused). It follows like the brand. Every project a request touches must allow it — the one
  it is for, those holding its clips, the owners of its pictures — for generated video too, checked again just before
  anything is sent. Only a person changes it
  (`ai.rules.set` is people only, like budgets): an agent or an MCP client can't lift a client's rule.
- **Claude's panel**: Claude prices with `metarush ai estimate` and asks with `metarush ai make`, which becomes a card
  with the price, the model and the alternatives; Auto runs it only for a person whose estimates are off. Voice copies
  and Content Settings are never cards.
- **Settings → Integrations** says what each key covers; the activity bar, job details and the bell follow each piece
  of work like a generated shot ("Making a picture", "Ready in the resources library").

A client's **What may leave** ([what-may-leave.md](what-may-leave.md)) applies too: what a request sends
— its words, pictures (frames of footage count as faces), voices and footage — is asked against the rules of every
project it touches, at the estimate (said) and just before it is sent (refused at $0, recorded). Lip sync's part of
a clip counts as footage, with its faces and voices. Work done on this computer — a model here, or an ability whose
network is off — sends nothing anywhere, so "nothing leaves" still allows it, and the receipt's "What left this
computer" never lists it (an ability's run lists the hosts it reached, as the abilities' place); Vertex AI and sync.
are listed like any service.

## The catalogue
`internal/aiservices/catalog.json` holds each provider (its key, the only host the key goes to, its data terms with the
day they were read, consent scripts) and each model (kind, tasks, limits, MetaRush's rank, watermark, licence terms with
the day they were read, notes) with **dated prices**: per picture by size, per second, minute or hour, per song, per
thousand characters or per request, plus what Google counts for the words and pictures sent. A later price table
takes over on its day (the estimate says one is coming); `retiredOn` retires a model (it is refused, and no longer
offered). `metarush ai models [--kind music] [--project <id>]` lists them with their prices and what the project's
Content Settings allow. Re-read a provider's pages before changing an entry and update its `checked` day.

Licence terms travel with what was made: the generation record keeps them (`terms`), delivery notes give a music
track made by an AI service its licence, and warn when it may not cover every use (ElevenLabs' music is for
individuals below its Enterprise plan; Google states no commercial terms for Lyria).

## Consent
One consent record (People → a person, `metarush consent record`) covers a person's face and voice, for MetaRush and
the providers alike. It says what it covers — generated shots and pictures, their voice, a presenter (`--covers
shots,voice,presenters`; records made before cover shots and voice) — and which services may keep the recording and
footage (`--service d-id`); a presenter needs a recording of the person, their own too ([presenters.md](presenters.md)):
- **A voice copy** (`metarush ai voices copy`, people only) needs the person's consent in force for the project, a
  sample where only their voice is heard — named as theirs by a person (an AI app's or Auto's name doesn't count) — and, for Google, a recording of them reading
  Google's consent script that isn't the sample (another clip, or a part of it the sample doesn't cover). MetaRush
  checks it against its own transcript: most of the script's words, in order, within about the time reading it takes.
  A generated clip is never a sample or a consent.
- **New words in a voice copy** (changing a spoken word, [spoken-words.md](spoken-words.md)) need a consent
  that names `new-words` and the service keeping the copy (`--covers voice,new-words --service elevenlabs`), for a voice a
  person named; children and public figures never.
- **Speech or a voice change in a voice copy** relies on its consent. When the consent is withdrawn the copy is marked
  withdrawn at once, so nothing more is said with it, and deleted at the provider: right away where this computer has
  the key, otherwise by a deletion job that any computer with the key takes, tried until it is done (it belongs to no
  project, so clearing a project out never calls it off, and the daemon queues it again if its job is gone). The
  consent's page says where each copy stands. `metarush ai voices remove` withdraws one by hand.
- **Work relying on a consent when it is withdrawn**: still waiting, it is stopped; waiting at the provider, it is
  stopped there; being sent, it is never cut off (its answer would be lost, and a voice copy left at the provider).
  Everything is checked again just before it is sent, and refused at $0 if the consent is gone; what comes back for
  other work isn't kept; a voice copy is withdrawn and deleted as soon as it is done.
- **A voice copy's name at the provider** carries MetaRush's tag (`[mr-…]`), so a copy whose answer was lost on the way
  back is looked up by it and deleted (for ten minutes, in case the provider finishes it late).
- **A dub in the speakers' own voices** needs every speaker named by a person and consenting; otherwise the dub uses
  similar voices from the service's library (copying is turned off).
- **A picture** whose words name someone on the People page needs their consent; pictures sent along need a person to
  say they show no real people, or the consent of those they show.

## How a request runs
`ai.estimate` plans it: the model, the request (provider-neutral: `aiservices.Request`), the files it would send, the
checks, the price and the alternatives. `ai.make` (or `ai.voices.copy`) then needs the agreed price, keeps the exact
bytes sent by their content (a clip's sound as FLAC, a voice sample as WAV, a resources picture as JPEG), sets the
money aside, writes the generation record (with `kind`) and queues a `generate` job pinned to this computer. The job
notes that a send began, sends once, saves the provider's ticket before waiting (a one-shot call's too: it names what
to delete), keeps each result — checked to be what the kind makes — with how it was made, settles with what the
provider counted (seconds, characters) or the estimate, deletes what the provider kept where its API allows, and
tells the person. A send that may have reached the provider is never sent again and counts as spent, and neither is
an answer that came back but can't be used (no picture, no sound, no words: it may have been charged). Once a request
is on its way, a stop asked for meanwhile doesn't cut it off: its answer is waited for and dealt with.

The project's receipt (`metarush project receipt`) reads these records like generated video's: every kind with
its service, model, what was sent, what it cost and whether it was deleted there (a voice copy stays while it is in
use; what a service's API can't delete is said). AI pictures and sound are listed as generated media, and a version's
cost — on its page and in a delivered video's record — includes the AI pictures, clips, sound tracks and cloud
transcripts it uses. With Auto, an `ai make` card runs by itself only where a person set generated video's
project budget and the team's daily limit, which every kind of AI work shares.

What each service keeps afterwards:
- **Google**: requests are sent with `store: false`, so Google keeps no log of them; uploaded recordings are deleted
  after use; a voice copy is stored at Google (it is used by its ID) until it is withdrawn.
- **ElevenLabs**: dubbing projects, transcripts and the history items speech and voice changes leave are deleted after
  the download; sound effects and music can't be deleted through ElevenLabs' API — the record says so
  (`forgotten.kept`), and the data terms say how long ElevenLabs keeps them.

## Transcripts in the cloud
Local transcription stays the default (free, nothing leaves the computer). `metarush ai make transcription --clip <id>`
(or **Transcript** in the panel) sends the whole clip's sound; the words come back with their times and speakers, are
kept as a file, and the clip's ordinary transcribe job takes them instead of the local recognizer — voices, people's
word fixes and the spelling lists are handled as for any transcript, and the clip says which service wrote it.

## Commands
```bash
metarush ai models --kind music --project <id>
metarush ai estimate picture "a watercolour kite over a hill" --size 2K --aspect 16:9 --project <id>
metarush ai make picture "a watercolour kite over a hill" --size 2K --project <id> --max-cost 0.06 --wait
metarush ai make music "calm acoustic guitar" --seconds 30 --instrumental --model elevenlabs/music_v2 --project <id>
metarush ai make sound-effect "a wooden door creaks open" --seconds 3 --project <id>
metarush ai make voice "Welcome to the garden." --voice Kore --style "warm, unhurried" --project <id>
metarush ai make voice-change --clip <clip> --voice <voice> --from 12 --to 40
metarush ai make dubbing --clip <clip> --into es [--own-voices]
metarush ai make transcription --clip <clip>
metarush ai make transcription --clip <clip> --model local/parakeet-v2      # on this computer, free
metarush ai make background-removal --picture logo --project <id>
metarush ai make upscale --picture logo --scale 4 --project <id>
metarush ai make lip-sync --clip <footage> --from 12 --to 18 --sound <clip>
metarush ai local                                                            # models on this computer
metarush ai local setup kokoro-v1                                            # owners: download it once
metarush ai voices copy --person "Ada Lovelace" --name "Ada narration" --sample <clip> --consent-clip <clip>
metarush ai voices list | remove <voice copy>
metarush ai rules show --project <id>
metarush ai rules set --project <id> --forbid elevenlabs --pin music=gemini/lyria-3.5 --note "the client's contract"
metarush ai list --project <id> | show <work, clip or picture> | cancel <work>
```
Every command takes `--json`; `--details` shows the model, the request hash, the files sent, the provider's handles
and what was deleted.

## For developers
- `internal/aiservices` is the contract (`Adapter`: capabilities and estimate from the catalogue; submit, poll, fetch,
  cancel, forget; `VoiceRemover`) and the catalogue; adapters register per provider and kind
  (`internal/aiservices/google`, `internal/aiservices/elevenlabs`). Keys come only from `internal/providers`; an
  adapter sends its key in a header, only to its provider's host or a loopback test server.
- `internal/aiservices/aiconformance.Run` is the suite every adapter passes against its fake: the catalogue entry, the
  estimate, the lifecycle down to deleting at the provider, files people can open (`aiservices.Sniff`), a refused key
  and the request hash. `internal/testutil/fakeai` is a fake provider with a model per kind (lifecycle tests);
  `fakevideogen` answers Google's generateContent and Voices API; `fakeeleven` answers ElevenLabs' endpoints.
- `internal/aiwork` is the lifecycle (ops `ai.*`, the job, the Content Settings part, consent, voice copies). The
  worker hands `generate` jobs whose payload has a kind other than video to `aiwork.Run`.
- A new provider: catalogue entries (with prices, licence and data terms), an adapter, its fake, the conformance
  suite. A model on this computer is a `local` provider's: `internal/aiservices/local`, handed the computer
  through `aiservices.WithEnv`, saying whether it can run here (`aiservices.Here`), its downloads pinned in
  `internal/localmodels/pins.go`; tests stand its programs in with `internal/testutil/fakelocal`. A service reselling other providers' models would be a provider with `route: gateway`; none is built. A
  model reachable only through one names it in `onlyVia`: `ai models` lists it with its terms and "only
  through …, a gateway MetaRush doesn't use", and it is never offered, chosen or sent (`Model.Check` says why).
- Tests never call a real service. A live check of an adapter calls the real service and costs money: run it on purpose, with your own key.
