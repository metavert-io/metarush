# Abilities

An ability is a capability someone adds to MetaRush without a new release: a folder with an `ability.yaml` that says
what it does, what it needs and what it may reach, plus the script or program it runs. Running one follows the same
rules as anything else MetaRush does: an owner approves each version, a person sets it up on each computer, it runs
in the operating system's sandbox, its network goes only through MetaRush, and it never sees a key. Code is in `internal/abilities` and `internal/sandbox`.

## Writing an ability

A folder: `ability.yaml`, the script or program it runs, optionally `notes.md` (how to use it well; Claude reads it
with `ability show`) and anything else it needs (up to 256 files, 8 MB each, 32 MB in all). Hidden files are left
out; links, installed packages (`node_modules`, `site-packages`, `venv`) and anything that looks like a key are
refused. Its version is the SHA-256 of a canonical archive of the folder: the same files are the same version
on every computer, and one changed byte is a new version that waits for an owner again.

```yaml
ability: 1
name: stock-photos                    # lowercase letters, digits and dashes
title: Stock photos
does: Finds a stock photo that matches a few words and brings it in with its licence.
runs: python3 fetch.py                # a program it needs, or a file of the folder (./tool); no shell
inputs:
  words: {kind: words, max: 100, says: What the photo shows}
  count: {kind: number, min: 1, max: 6, default: 1}
outputs: [picture]                    # footage, sound, picture, document, font
cost: {service: Example Photos, usd: 0.02, per: photo, from: count, as-of: 2026-10-01}   # or: free
keys: [{name: example-photos, env: PHOTOS_KEY, hosts: [api.photos.example], header: X-Api-Key}]  # or query: api_key; or just [example-photos]
hosts: [api.photos.example, cdn.photos.example]
sends: [words]                        # what leaves this computer: nothing, words, pictures, sound, footage
changes-outside: no                   # publishing, uploading or driving another app: people only
likeness: no                          # MetaRush doesn't run likeness abilities yet
needs: {programs: [python3], os: [mac, linux]}
licence: MIT
search: [stock, photo, picture]
limits: {minutes: 10, mb: 2000}       # the defaults
test: {inputs: {words: test}}         # run once by `ability check`, with the network off
```

Two more things a manifest may say:

```yaml
needs: {programs: [python3, whisper-cli], files: [{name: whisper-model, says: A whisper.cpp model that can translate}]}
local: {port: 8188, service: ComfyUI} # one service on the computer it runs on, plain HTTP, once an owner agrees
```

`needs.files` are files a person sets up on each computer (`metarush ability setup <name> --file
whisper-model=/full/path`): pinned by size, time and SHA-256, copied into each run's room read-only (an APFS clone on
a Mac) and checked, at `$METARUSH_FILE_WHISPER_MODEL`; the sandbox never reads them in place, so they may live
anywhere, your home folder included — except MetaRush's own folders, where apps keep sign-ins (`~/Library`, `/etc`), hidden files and folders (where keys
live, like `.ssh`; MetaRush's models folder is fine) and anything that looks like a key, which MetaRush refuses to
give. Like keys, owners choose them, after seeing where each really is and what it is (`--file` asks for a yes; the
dashboard's setup dialog looks first). `kind: whisper-model` or `whisper-translator` makes MetaRush read a whisper.cpp
model's header: one that can't translate (English-only, turbo) is refused. SQLite files are refused whatever they hold
(browsers keep cookies and saved passwords in them), MBTiles and GeoPackage maps included, until a declared kind allows
one; so are `.key` files. `local` lets the run reach one port of this computer's loopback, over
plain HTTP through the run's proxy (its address is `$METARUSH_LOCAL_URL`), nothing else here: ports 1024–65535, never MetaRush's own (4840–4849, the
dashboard's), a database's (27017–27019, 27150–27199) or a well-known control port (Docker's, a browser's remote
control…), never 32768 and up (ports handed out for a moment), never with an open network, never a key; its
requests don't count against a words-only run's budget. What an owner agrees to is the program listening there when
they approve, which they see in full: its executable, and for an interpreter (Python, Node.js…) the folder it was
started in and the script or module it runs; a manifest may also give a `probe` (`{path: /system_stats, expect:
[system.comfyui_version, devices]}`) the service must answer as described. Plans show it and check it, each run checks
it as it starts and its proxy asks again on its requests; another program there, or MetaRush's own, is refused and a
run is broken off. On another computer an owner agrees to that computer's program with `ability setup --agree-local`. An owner agrees to
it when approving (`ability approve --local-service`), and a person starts each run, because the service runs outside
the sandbox. What reaches only that service doesn't leave this computer through MetaRush, though the
service itself may send things on.

The full schema is `schema/ability.schema.json` (generated from `internal/abilities.Manifest`). Rules a schema can't
say are checked too: a key goes only to hosts the ability declares (a key named after one of MetaRush's own
integrations, `elevenlabs` or `gemini`, only to that service) and in one place — a header (`Authorization` unless it
names another; not one that steers the request, like `Host` or `Cookie`) or a query parameter; a key can't be read
from a variable that steers programs (`PATH`, `NODE_OPTIONS`, `*_PROXY`, `METARUSH_*`…); an ability that takes
footage, pictures or sound and reaches the network says whether they leave (`sends`); `network: open` can't use keys.

**What a run gets.** Its inputs as a file (`$METARUSH_INPUTS`, JSON: words, numbers, choices, yes/no as values;
footage, pictures and sound as the path of a copy in its folder) and one variable each (`$METARUSH_INPUT_WORDS`); an
empty folder for what it makes (`$METARUSH_OUTPUT`); each key's stand-in under the variable its manifest names; a
home and a temporary folder of its own; `$METARUSH_TEST=1` in the check's run; each file a person set up at `$METARUSH_FILE_<NAME>`. Next to its outputs it
may write `result.json`: `{"files": [{"path": "photo.jpg", "title": "…", "licence": "CC BY 4.0", "source":
"https://…", "author": "…", "credit": "“Harbour light” by Sea Painter, CC BY 4.0, via Wikimedia Commons"}]}`, which
MetaRush keeps with each file: a clip or sound records the licence, the credit and the page as its rights
(never over rights someone recorded), a picture its author, and a delivery lists the credits and writes them into
its `.notes.md` file, ready to paste into a description. A captions file says its `language` (and `"translated":
true` when a machine translated it, and `model`: what made it). A service's own terms a person should know go in the
manifest's `terms` (one line), shown on the approval page and on every plan. Only regular files of the kinds it declared come back, each through the same
classifier as a file you add; anything else is left out with the reason. Captions (`outputs: [captions]`: SRT
or WebVTT, at most 2 MB, read by their bytes) are kept with the run's one clip input; `metarush footage captions
<clip> [--language en] [--save file]` shows them.

**Writing the code.** Read the key's stand-in from the environment and send it in the place the manifest names (the
`Authorization: Bearer …` header by default): MetaRush puts the real key there on the way out, only to that key's
hosts, only over HTTPS; a request with the stand-in anywhere else is refused. Write only in `$METARUSH_OUTPUT`,
`$HOME` and `$TMPDIR` (the ability's own files are read-only), and don't leave programs running: when the program
ends, everything it started ends with it. Use Python or Node.js, or a program that honours the usual proxy and certificate variables (see
[how programs reach the network](#how-programs-reach-the-network) below); a test that needs the network should check what it can offline when `METARUSH_TEST` is set.

## What happens to it

| Step | Who | What |
|---|---|---|
| Check | anyone at this computer (Claude in a terminal too) | `metarush ability check <folder>`: the archive, the manifest, then its test once in the sandbox with the network off. A pass waits for an owner; a failed test is kept as failed; manifest problems keep nothing. |
| Approve | an owner, a person | After reading what it may do: what it installs, what it reaches, its keys and where MetaRush adds each, what leaves this computer, what it brings back, its cost and licence (`ability show`; Library → Abilities). To-dos and the bell ask. `--open-network` agrees to any public host, still through MetaRush, when its manifest asks; `--local-service` to the one service on this computer it names. |
| Set up | a person on that computer | `metarush ability setup <name>`: the programs it runs, pinned by fingerprint; files it reads (`--file name=/full/path`, owners); keys only by owners (`--key name`, typed, never on the command line). |
| Plan | anyone | `metarush ability plan <name> --input words=…`: the inputs, the estimate, the places and keys, what leaves on this run, what comes back and where, who decides, the plan's fingerprint. |
| Run | depends on the manifest | `metarush ability run …` (or Run… in the dashboard): a free run that uses no key and sends nothing but words starts from anyone; one that uses a key needs a person each time, whatever its manifest says it costs; a priced one needs a person who agrees to the estimate; one that sends pictures, sound or footage away, changes something outside MetaRush or has an open network needs a person each time. A job on this computer, never retried. |
| Keep | MetaRush | Outputs added with how they were made (`madeWith`: `name@version`, the run, its inputs, licence and source); the run keeps every request it made through MetaRush (hosts and paths, never keys or query values) and the end of what it printed; the bell says how it went. |
| Take back | an owner, a person | `metarush ability revoke <name>`: nothing runs that version until an owner approves it again. |

`metarush ability list` and `find "<task>"` (at most eight matches, one line each: what it does, whether it is set
up here, its cost, what leaves) are how Claude learns what exists; `ability show <name>` gives the manifest, inputs and
notes. `ability runs [name] [--id run]` lists runs, or one with its requests and log. Every command takes `--json`.

**In find, and beside what MetaRush does built in**. The workspace's abilities are a catalogue of their
own in `find` (`metarush ops find`, MCP's `find`): each entry is a change; a person's to do unless the run's decider
says anyone may start it (free, no key, nothing but words leave); priced or free; what leaves this computer; and
whether it works here. One no owner approved says "needs an owner's approval", one not set up here "needs setting
up on this computer" (or "needs the <name> key on this computer"), and `run` refuses both before anything runs. `run`
reaches an ability only through `ability.run`, never beyond its rules: a person's ability is refused there
(`E_HUMAN_ONLY`, "Only a person starts …", with `metarush ability run <name>` as the command to hand a person), and a
safe one is a change `run_change` asks about. One that says `cost: free` but uses a key is never called free: the
service its key is for may charge (cost class `keyed`). An ability can't be named like one of MetaRush's own
(`docs/catalogue.md`, present or retired IDs): `ability check` and `ability approve` refuse it. `ability.run` takes
find's form of a run: `{"ref": "<name>@<version>", "input": {"inputs": {…}, "plan": "<fingerprint>", "projectId":
"…", "maxUsd": …}}` — the plan from `ability.plan` first, always. `metarush catalogue list` and Library → Abilities
show the workspace's abilities beside the first-party ones (docs/catalogue.md), each saying whether it works here.

**In the dashboard**, Library → Abilities lists them under "Added to this workspace", beside what MetaRush does
built in (filters: Works here, Built in, Added, Waiting for an owner); an ability's page shows what it
may do with Approve / Turn down / Take back for owners, its check, what this computer has (Set up on this computer),
its inputs, versions and runs; Run… asks for the inputs, shows the plan and starts it. The dashboard never runs an
ability's code; it shows what the server says.

## Abilities that provide a kind of AI work
An ability whose manifest says `provides: [<kind>]` becomes, once an owner approves it, a provider of that kind of AI
work beside Google, ElevenLabs and the models on this computer ([ai-services.md](ai-services.md)): `metarush ai models`
lists it as `ability-<name>/<name>`, the estimate prices it from its manifest (its dated price, by the number input it
names) and may offer it as an alternative (ranked 2: MetaRush hasn't judged its results), Content Settings can pin it
or rule it out by that name as soon as it is approved, and ruling a service out rules out every ability that reaches
it (by its hosts, like `api.elevenlabs.io`, `*.elevenlabs.io` or `elevenlabs.io`, or its key; an ability with an open network may reach any service, so
ruling any out rules it out), consent for faces and voices is checked as for any provider, and the money counts against the same
budget and daily limit. `metarush ai make upscale --picture logo --model ability-grow/grow` runs the newest approved
version in the sandbox as a run of that work: the run is recorded like any other (its requests, the end of what it
printed, `work` naming the AI work), and what it brings back goes to the AI work, which keeps it with how it was made
(a picture in the resources library, sound as a clip) and tells the person. Who may ask is the run's decider: a
free ability that sends nothing but words, anyone; one that sends pictures or sound away, uses a key or costs money,
a person. An ability whose network is off sends nothing anywhere, so a client's "no AI services" allows it, like a
model on this computer, and a picture sent to it needs no "no people" answer. Its runs go through the same core as
its own, so the starter pack's rules hold for them too: a service on this computer only once an owner agreed to it,
checked as each run starts and on its requests, and only with the files a person set up here; one
reaching only such a service says what it gives it goes to that service, outside the sandbox.

| Kind | Inputs it fills (names are fixed) | Brings back |
|---|---|---|
| `picture` | `prompt` (words); optional `picture`, `size`, `aspect` | a picture |
| `voice` | `prompt` (words); optional `voice`, `language`, `style` | sound |
| `voice-change` | `audio` (sound); optional `voice` | sound |
| `sound-effect` | `prompt`; optional `seconds` (number) | sound |
| `music` | `prompt`; optional `seconds`, `instrumental` (yes-no) | sound |
| `background-removal` | `picture` | a picture |
| `upscale` | `picture`; optional `scale` (number or choice) | a picture |

Any other input needs a default or must be optional (nothing else is given). `ability check` refuses a manifest that
provides a kind it can't: video has its own steps; voice copies and dubs copy people's voices, a likeness abilities
can't make yet; transcripts need words back, and an ability's documents are PDFs.

## Between workspaces

A version travels as an **ability file**, like Recipes' files. `metarush ability export <name[@version]>
[file]` (or **Export to a file…** on its page, `GET /api/abilities/export`) writes a zip: `ability.json`
(`metarush.ability-file/1`: the version's number, hash and state, its lineage — the versions it descends from, those
it was brought in from too — and its approval record as information only: decisions with names, never accounts),
`archive.json` (the canonical archive, whose SHA-256 is the version) and, for people, `ability.yaml` and `notes.md`.

`metarush ability import <file> [--dry-run] [--as-version]` (or **Bring in an ability file…** on Library →
Abilities, `POST /api/abilities/import`: a dry run first, then a styled question) reads it strictly — only those
files, each within its size; `ability.json` with no unknown fields and its format named; the archive canonical, its
SHA-256 the version it names, no keys or credential files, its manifest sound — and records it **imported**: never
approved and never set up here, whatever it was there (shown as information). The version it already is brings
nothing new; one whose lineage shares a version with an ability of the same name here is that ability's next version;
an unrelated ability of the same name is refused unless `--as-version` says it is the same one. Nothing runs during
the import. A person then checks it on a computer here (`metarush ability check --version <name@version>`: its test
once in the sandbox with the network off), and an owner approves it (an unchecked import can't be approved). Both
operations are LocalOnly; the dashboard's two endpoints choose every path themselves.

## The starter pack

Seven abilities in `abilities/` at the repository's root (its README has the table), each with `notes.md` on its
licences, cost, keys, what leaves this computer and what a person installs; `go test ./internal/abilities/ -run
Starter` checks each as shipped and runs it against a fake of its service on this computer.

- `pexels-stock` — stock videos and photos from Pexels: a free key the code never sees (added only on api.pexels.com),
  the words leave; each file with the Pexels License and the photographer's credit.
- `open-media` — public-domain and openly licensed media from Wikimedia Commons and the Internet Archive: no key, the
  words leave, a safe step; only public domain, CC0, CC BY and CC BY-SA, each with its credit, carried into the
  delivery's notes.
- `freesound-effects` — sound effects from Freesound: a free key (Freesound's API terms: non-commercial use free),
  the words leave; CC0 and CC BY with the credit, NC and Sampling+ left out; the HQ previews (originals need OAuth2).
- `english-captions` — English captions for a clip in another language by whisper.cpp (`whisper-cli -tr`) on this
  computer: free, local, nothing leaves, a safe step; a translating model set up per computer, the captions
  kept with the clip.
- `comfyui-local` — pictures from the ComfyUI server on this computer (GPL, run beside MetaRush, never bundled):
  nothing leaves; an owner agrees to port 8188 at approval, a person starts each run.
- `content-credentials` — Content Credentials on a delivered video with c2patool: **not yet runnable** (a delivered
  video as input, a copy beside it, signing without the key in the sandbox); a pinned installer for c2patool.
- `davinci-resolve` — DaVinci Resolve Studio through its community MCP server: **not yet runnable** (an MCP server
  MetaRush hosts, its tools as people's steps, reaching a running app).

The rest of this page is how the sandbox works and what was verified about it.

## The sandbox and the network

`internal/sandbox` runs a program MetaRush didn't write. It is separate from the Remotion refreshes' rules
(`internal/remotion/isolate.go`), which run with the network on; the two share bubblewrap's probe and the plain
refusal (`sandbox.BwrapWorks`, `sandbox.CantRun`).

**What a run gets.** Its own folder (the room: the ability's files, read-only; its inputs; and the only folders it
writes, its output, home and temporary folders), read access to the system's folders and to the program with its own
installation (a Homebrew keg with the other kegs, their opt links, lib and Homebrew's TLS settings; a Python.framework
version; `/opt/<name>` with bin and lib), and nothing else. A program in a folder of programs (`~/bin`,
`/usr/local/bin`) gives just that file. MetaRush's home, cache, scratch and media folders and the person's home folder
are never visible: on a Mac they are denied after everything allowed, so they always win; a read that is or holds one
is refused; a program inside one is refused at setup. File names and sizes (metadata) are readable only on those places
and the folders above them. The program has no terminal (a session of its own), and whatever it leaves running is
killed when it ends or runs out of time (`tools.Spec.Contained`). The environment is built from nothing
(`sandbox.BaseEnv`): no variable of MetaRush's or of the person's comes along. The network is one of three:

| Mode | What the program can reach |
|---|---|
| off | nothing, not even this computer's loopback |
| MetaRush's network (`proxy`) | only MetaRush's proxy for this run, which forwards HTTP and HTTPS to the hosts the ability declared |
| open | any public host over HTTP(S), still through the run's proxy, never this computer or a local network, never listening, no keys; a separate approval only an owner gives |
| one local service | MetaRush's network, plus plain HTTP to the one port of this computer's loopback an owner agreed to, through the same proxy; nothing else on this computer |

A client's What may leave rules refuse a run's hosts, request by request, never its one local service: what
goes there stays on this computer, so a run may reach it whatever the rules say, and the receipt doesn't list it.

**Keys never enter the sandbox.** The program's environment holds a stand-in for each key (`mrkey-…`). The proxy opens
HTTPS with a certificate authority made for this run alone (valid two days, its private key never written to disk),
reads each request, and puts the real key in the one place the manifest names (its header, or its query parameter),
only on requests to the hosts that key is for, and only over HTTPS. The stand-in anywhere else in a request (another
header, the path, another parameter, a readable body), on its way to another host or over plain HTTP is refused, so
the code can't steer a key to where a host would repeat it. Every answer of a run with keys is opened (gzip and
deflate; other compressions are refused) and the key is taken out in each form a host might repeat it in — as it is,
escaped for an address, escaped for JSON, in base64 (`sandbox.KeyForms`); the run's log, its outputs (one holding a
key is left out) and `result.json` lose it the same way. Outputs are copied out of the room without following a
link, and only the copies are taken in. Every request is
recorded with its host, path, the names (never the values) of its query parameters, sizes, time, status and the names
of the keys put in. The run's proxy address carries a password of its own, so other programs on the computer can't
use it while the run lasts. It refuses this computer and local networks (also inside IPv6 forms), WebSockets (for now),
headers past 32 KB, and more than 10,000 requests or the run's size limit coming in. An ability that says it sends only
words has one outbound budget for the run — request lines, headers and bodies together, 256 KB — and at most 200
requests.

### How programs reach the network

Verified on macOS 27 (`go test ./internal/sandbox/`, all against local test servers; nothing leaves the computer):

| Client | Works through MetaRush | How |
|---|---|---|
| curl (the system's) | yes | reads `HTTPS_PROXY` and `CURL_CA_BUNDLE` |
| Python's urllib (python.org, Homebrew) | yes | reads `HTTPS_PROXY` and `SSL_CERT_FILE` |
| Apple's `/usr/bin/python3` | yes, with a helper | built with LibreSSL, it ignores `SSL_CERT_FILE`; MetaRush puts a `sitecustomize.py` on `PYTHONPATH` that adds the run's certificate to Python's default check. The shim in `/usr/bin` is replaced by the developer tools' own copy (`sandbox.RealProgram`). |
| Python requests, httpx | expected yes (the tests skip when they aren't installed) | read `HTTPS_PROXY`; requests reads `REQUESTS_CA_BUNDLE`, httpx `SSL_CERT_FILE` |
| Node.js `fetch` (Node 22) | yes, with a preload | **`fetch` ignores the proxy variables** before Node.js 24 (and from 24 only with `NODE_USE_ENV_PROXY=1`). MetaRush loads a preload first (`NODE_OPTIONS=--require …`, `sandbox.NodePreload`) that sends `fetch` to the proxy by address (`/__metarush/<scheme>/<host>/<path>`, redirects kept on the proxy) and replaces the `http`/`https` modules' global agents with CONNECT tunnels; `NODE_EXTRA_CA_CERTS` trusts the run's certificate. Setting undici's global dispatcher would need undici itself, which Node.js doesn't expose to user code before 24, so the preload rewrites addresses instead. |
| Node.js `https.get` | yes, with the preload | its global agent tunnels through the proxy |
| A plain program that reads the proxy variables and a certificate file | yes on Linux (Go's `net/http`, curl) | the variables are set under every usual name: `HTTPS_PROXY`, `https_proxy`, `ALL_PROXY`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO` |
| A Go program on a Mac | **no, it fails closed** | Go on macOS checks certificates with the system's own trust settings and ignores `SSL_CERT_FILE`, so it doesn't trust the run's certificate. Plain HTTP without a key works. Such abilities are scripts (Python, Node.js) or programs that read a certificate file. |
| Anything that opens its own connection (its own socket, its own agent, DNS) | **no, refused** | the sandbox lets it reach nothing but the proxy; nothing gets out around MetaRush |

**The GPU** (found with the starter pack): the sandbox doesn't open the Mac's GPU to abilities. whisper.cpp's
`whisper-cli` stops with an error at its Metal start-up there (`GGML_ASSERT(buffer) failed`) and runs fine on the CPU
with `-ng`; programs that need the GPU (ComfyUI, for instance) run beside MetaRush and are reached as the one local
service an owner allows.

What this means for ability authors: write abilities as Python or Node.js scripts, or as programs that honour the
usual proxy and certificate variables; never put a key in the code, read the stand-in from the environment variable
the manifest names; expect HTTP/1.1 from the proxy (it speaks HTTP/2 to the host when the host offers it).

### Linux (bubblewrap)

**Not yet measured on Linux.** The `sandbox-linux` job in `.github/workflows/ci.yml` runs the package there; until it
has, everything below is the design and what Ubuntu is known to do, not a measurement.

How it works: bubblewrap with every namespace its own (`--unshare-all`), only the system's folders mounted read-only,
the program's installation read-only, the room read-write, a fresh `/dev`, `/proc` and `/tmp`, and nothing of the
person's home. With the network off, its network namespace has only a loopback device. With MetaRush's network, the
proxy also listens on a socket file in the room, and MetaRush's own binary runs first inside the sandbox
(`metarush sandbox-forward`, handled before anything else starts): it listens on `127.0.0.1:<the proxy's port>`
inside the sandbox's own network, passes each connection to that socket, then runs the program. So the program sees the
same proxy address as on a Mac and has no other way out. An open network goes the same way (the proxy then takes any
public host); bubblewrap never shares the computer's network with an ability.

**Finding: Ubuntu's AppArmor limits user namespaces.** Since Ubuntu 23.10 (and in 24.04 LTS, which GitHub's
`ubuntu-latest` runners use) `kernel.apparmor_restrict_unprivileged_userns=1`: a program may make a user namespace
only if an AppArmor profile lets it, and bubblewrap run by a person (not set-uid) has none, so it fails before the
program starts. GitHub's runners need `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` for bubblewrap
to work (CI does that). Debian's older `kernel.unprivileged_userns_clone=0` and
`user.max_user_namespaces=0` stop it the same way.

What MetaRush says and does on such a computer: it **refuses to run the ability** (never unsandboxed). Before
anything runs, `sandbox.Here` tries bubblewrap once and, when it fails, reads those three settings and says, in plain
words, that the computer's security settings don't let bubblewrap make a sandbox and that an administrator can allow
it. Details carry what bubblewrap said and the fix:

- recommended, which allows only bubblewrap: save this profile (it is `internal/sandbox/bwrap.apparmor`) as
  `/etc/apparmor.d/bwrap`, then `sudo apparmor_parser -r /etc/apparmor.d/bwrap`:

  ```
  abi <abi/4.0>,
  include <tunables/global>

  profile bwrap /usr/bin/bwrap flags=(unconfined) {
    userns,

    include if exists <local/bwrap>
  }
  ```

- or lift the limit for every program: `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` (and a line
  in `/etc/sysctl.d/` to keep it). This is wider than needed.

Remotion refreshes say the same through the same helper. Whether the profile alone is enough on GitHub's runners is
one of the things the CI job records.

### Tests

`go test ./internal/abilities/`: the manifest's rules and schema, the archive, checks (a pass, problems, a failing
test, an output it doesn't declare, the network off), approvals and who may give them, setup and keys, plans, a local
run end to end, a run through MetaRush with a key (a local test server stands in for the service: it gets the key, the
program and the record never do; an undeclared host and a large upload are refused) and who decides a run. The
browser run is `scripts/abilities-e2e.sh` (port 5420).

`go test ./internal/sandbox/`: the clients above in the sandbox with the proxy, keys put in and taken out, a key
refused for another host and over plain HTTP, undeclared hosts, going around the proxy, a program without the proxy's
password, the network off (loopback included), the home folder hidden and writes kept in the room, a plain Go
program, bubblewrap's arguments (on any computer), the namespace limits, local addresses and host patterns. Without a
sandbox the tests that need one skip; `METARUSH_TEST_SANDBOX=require` makes them fail instead (CI's Linux job).
