# The starter pack: seven abilities

Each folder here is an ability (docs/abilities.md): an `ability.yaml` that says what it does, what it reaches and
what leaves this computer, the script or program it runs, and `notes.md` on how to use it well, its licences, cost,
keys and what a person installs. They are examples you can read, check, approve and set up like any other ability:

```bash
metarush ability check abilities/<name>          # runs its test once in the sandbox, network off
metarush ability show <name>                      # what an owner reads before approving it
metarush ability approve <name>                   # an owner, at a terminal or under Library → Abilities
metarush ability setup <name> [--key <name>] [--file <name>=<path>]   # a person on each computer it runs on
metarush ability plan <name> --input words=…      # what a run would do, before anything runs
```

Nothing here runs until an owner approves it and a person sets it up on each computer. Their tests never reach the
real services: `go test ./internal/abilities/ -run Starter` plays each service with a fake on this computer.

| Ability | What it does | Cost | Key | What leaves this computer | Who starts a run |
|---|---|---|---|---|---|
| `pexels-stock` | Stock videos and photos from Pexels, with the licence and the photographer's credit | free | a free Pexels key | the words you search for | a person |
| `open-media` | Public-domain and openly licensed pictures, video and sound from Wikimedia Commons and the Internet Archive, each with its licence and credit | free | none | the words you search for | anyone, Claude too |
| `freesound-effects` | Sound effects from Freesound, CC0 and CC BY, with the credit (Freesound's API is free for non-commercial use only) | free | a free Freesound key | the words you search for | a person |
| `english-captions` | English captions for a clip in another language, translated on this computer by whisper.cpp, kept with the clip | free, local | none (a whisper.cpp model a person sets up) | nothing | anyone, Claude too |
| `comfyui-local` | Pictures from the ComfyUI server on this computer, the one local service an owner allows | free, local | none | nothing through MetaRush (ComfyUI runs outside the sandbox) | a person |
| `content-credentials` | Content Credentials (C2PA) on a delivered video with c2patool — **not yet runnable** | free | a signing key MetaRush would keep | nothing | a person |
| `davinci-resolve` | DaVinci Resolve Studio through its community MCP server — **not yet runnable** | free | none | nothing | a person, each step |
| `vimeo-upload` | Posts an approved version to your Vimeo account, with the title and description from its delivery notes — **not yet runnable** | free | your own Vimeo access token | the video, its title and description, to Vimeo | a person, every run |
| `linkedin-video` | Posts an approved version to your LinkedIn as you — **not yet runnable** | free | your own LinkedIn sign-in, kept the way YouTube's is | the video and its words, to LinkedIn | a person, every run |
| `podcast-episode` | Posts a version's sound as an episode on your podcast host (Buzzsprout first), with the title, show notes and chapters from its delivery notes — **not yet runnable** | free | your own Buzzsprout token | the sound and its words, to the host | a person, every run |

The five marked "not yet runnable" are written in the shape they need; their first line says so, `ability check`
refuses them for exactly what MetaRush doesn't do yet, and their `notes.md` says what that is: the
three that post wait for a version's final render and delivery notes as inputs (people only; LinkedIn also for a
person's own kept sign-in).
