# Content Credentials

Content Credentials are a signed record inside a video file, in the open C2PA format (the Coalition for Content
Provenance and Authenticity). When they are on, every final video MetaRush delivers carries one, signed with the
certificate of the computer that delivers it, saying how the video was made:

- that it was made with MetaRush (and which version);
- which footage, sound and pictures it uses, each by its fingerprint (SHA-256), never by its name;
- which parts an AI model made or changed, in the words C2PA uses for them;
- who signed it: the organisation the signing certificate names.

Apps and sites that read Content Credentials show this, and anyone can check that the video wasn't changed after it
was signed. They never carry the names of people, projects or clients, a prompt, a consent, or a picture of the
video.

MetaRush builds them into delivery itself. No ability ever holds a signing key: the key stays in this computer's
keychain and inside MetaRush's own process.

## Turning them on

They are off until an owner turns them on.

| Where | Choices | Who |
|---|---|---|
| The workspace (Settings → Delivery, `metarush credentials on\|required\|off`) | off, on, required | owners; asking less (required → on, on → off) is a person's |
| A client's Content Settings (the project's Settings tab, `metarush content credentials <mode> --project <p>`) | on, required, off, or the workspace's | owners; asking less is a person's |

- **On**: every final video MetaRush delivers is signed. A video that can't be signed (no certificate on this
  computer, c2patool missing, a check after signing that fails) goes out without them, and its record, its notes file
  and what delivery says tell you why.
- **Required**: the same, but a video that can't be signed doesn't go out. A version this computer can't sign waits
  ([the ship gate](ship-gate.md): "Its videos must carry Content Credentials, and this computer can't sign them: …",
  with Set them up), and a signing that fails during the delivery stops it — nothing is written — with the gate off
  too. A person can still send it out without them, with a note (`metarush deliver override`, Deliver anyway…), which
  is kept and shows on the receipt. Moving a project to settings that don't require them is an owner's, as a person.
- **Review copies** are never signed: only final videos are.
- **Timelines** for other editing apps carry none, and a version isn't held for them there. **Posts to YouTube** send
  the render as it is: they carry none yet, and the post's plan says so.

## A certificate on each computer that delivers

Signing needs a certificate and its private key on the computer that delivers. An owner sets it up there, as a
person: in Settings → Delivery, or at their own terminal (Claude, Auto and other AI apps never do).

- **A test certificate** (`metarush credentials setup --test [--organisation "Example Studio"]`, or **Make a test
  certificate…**): made on this computer, valid for a year, issued by a throwaway authority whose own key is forgotten
  at once. Videos signed with it carry Content Credentials that check out, but verifiers show the signer as unknown:
  it is for trying them, never for a client. It names an organisation only if you type one; otherwise it says "MetaRush
  test certificate" and no name goes in.
- **Your own certificate** (`metarush credentials setup --cert chain.pem --key key.pem`, or **Use my own
  certificate…**): the chain your certificate authority issued (the signing certificate first, then the ones that
  issued it) and its private key, PEM, without a password. MetaRush reads the files once; the key goes into this
  computer's keychain and is never shown again (you can delete the key file afterwards). Verifiers show the
  organisation the certificate names as the one who signed — that is how a video says who published it. A certificate
  on the C2PA trust list shows as trusted in verifiers that use it.

MetaRush checks a certificate the way C2PA does before it keeps it, and says plainly what is wrong: not yet valid or
run out; signed by itself (use a test certificate instead); a certificate authority's own certificate; not marked for
digital signatures; no extended key usage, "any" usage, or one C2PA doesn't take (it takes email protection, document
signing, C2PA claim signing and Microsoft's C2PA signing); no authority key identifier; issued with SHA-1; the chain out
of order; a key that doesn't belong to it, or one protected with a password. Keys: ECDSA on P-256, P-384 or P-521 (signed
ES256, ES384, ES512), RSA of 2048 bits or more (PS256), Ed25519.

**Where it is kept**: the private key in this computer's keychain (an installation that keeps its secrets in its
secrets file keeps it there, never put into the environment of anything MetaRush runs); the certificate chain, which is
public, in the home's private state (`state/private/content-credentials.json`, 0600). Never the database,
`config.yaml`, a log, a command line, an error or an ability. Each computer needs its own; `metarush credentials
remove` takes this computer's away.

`metarush credentials` (Settings → Delivery) says whether they are on, whose choice that is, and whether this computer
can sign: the certificate (whom it names, who issued it, until when, its fingerprint), where the key is kept, and
c2patool's version. `metarush credentials test` (**Try it on a sample**) signs MetaRush's own one-second sample with it
and reads it back.

## c2patool

The signing itself is done by **c2patool**, the C2PA project's own tool, under the MIT or Apache-2.0 licence (your
choice). It is an optional component (Settings → Components, `metarush components add content-credentials`):
MetaRush downloads c2patool 0.28.3 from the project's releases, checks it against the SHA-256 GitHub states for it
(Macs: one universal program; Linux on x86-64), and keeps it in the home's tools folder. It is never bundled. Elsewhere,
build it from its source (`cargo install c2patool --version 0.28.3`). MetaRush needs 0.28 or newer.

## How a video is signed

1. Delivery checks everything first, as always (approval, the ship gate, What may leave, names), then signs every final
   video before writing anything.
2. c2patool writes a signed copy of the render next to where the video goes (a hidden file), with the manifest MetaRush
   wrote. **It never sees the key**: its signer is MetaRush itself. c2patool runs MetaRush as its signing program;
   that program asks the MetaRush that started c2patool, over a socket in a private folder only this signing knows, to
   sign — and only C2PA signatures, at most twice. c2patool runs with an environment of its own (none of the C2PA
   variables or settings of yours reach it, so its built-in test key can never be used), MetaRush's own settings file
   (no thumbnail, no remote manifests, the purposes a certificate may name), and a signing MetaRush didn't sign is
   thrown away.
3. MetaRush reads the copy back with c2patool and checks it: well formed, the signature and the video's bytes match, and
   signed with this computer's certificate. An unknown signer is not a failure (MetaRush gives c2patool no trust list;
   verifiers decide whom they trust). It also hashes the manifest store itself.
4. Only a copy that checks out is placed, with the same rule as every delivery: never over an existing file. A copy
   that doesn't check out is never delivered: the render goes out without Content Credentials instead (or, where they
   are required, nothing goes out), and delivery says so plainly.

A timestamp service is optional (Settings → Delivery → Add one, `metarush credentials on --timestamp-url <url>`): it
vouches for when each video was signed, so its Content Credentials still check out after the certificate runs out. It
receives a fingerprint of what was signed, never the video. Without one, nothing leaves this computer when a video is
signed.

## What the manifest says

| | What MetaRush writes |
|---|---|
| Title | "Video" — never the file's name (c2patool would use it, and a delivered file is named after its project) |
| Claim generator | MetaRush and its version (c2patool adds its library's) |
| Actions (`c2pa.actions.v2`) | `c2pa.created` first, with the video's digital source type; `c2pa.placed` for each part; `c2pa.edited`; all actions included. MetaRush vouches for them (created assertions) |
| Ingredients (`c2pa.ingredient.v3`) | each part used, `componentOf`, titled by kind and number ("Footage 1", "AI-generated video 1"), identified by `sha256:<its fingerprint>`, with its media type; an AI part also says what made it ("Made with Google Gemini, veo-3.1") |
| Thumbnail | none |
| Signature | the computer's certificate chain; with a timestamp service, its timestamp |

The parts are what the delivery's record lists: the footage and sound the cut plays, the cuts it nests and what they
play, the narration's recordings, the pictures it shows (fonts aren't parts), and every AI-generated part — so the
manifest discloses exactly what the record, the quality check and the receipt disclose. At most 200 parts are listed.

**Digital source types** (IPTC's, as C2PA uses them):

| Part | Source type |
|---|---|
| The video, when an AI model made or edited any part of it | `compositeSynthetic` (composite including generative AI elements) |
| The video otherwise | `composite` |
| Generated video, AI pictures, AI voices (narration), new words in someone's own voice copy, presenters, AI music, AI sound effects, AI dubs | `trainedAlgorithmicMedia` (created using generative AI) |
| A mouth matched to new words (lip sync), a voice changed by AI | `compositeWithTrainedAlgorithmicMedia` (edited using generative AI) |
| A picture's background taken away, a picture made larger, a voice made clearer, a narration line cleaned up | `algorithmicallyEnhanced` (changed by an algorithm, its main content kept) |
| Footage, sound, music and pictures nothing AI touched | none (MetaRush doesn't claim how they were captured) |

## What delivery writes

Each video's record (the `.json` next to it) has `contentCredentials`:

```json
"contentCredentials": {
  "signed": true,
  "signer": "Example Studio",
  "verified": true,
  "manifestHash": "a0adab88…",
  "issuer": "Example Authority",
  "fingerprint": "…",
  "sha256": "…",
  "size": 48213377,
  "parts": 12,
  "aiParts": 2,
  "sourceType": "http://cv.iptc.org/newscodes/digitalsourcetype/compositeSynthetic",
  "says": "This video carries Content Credentials, signed by “Example Studio” and checked: …"
}
```

`signed` and `verified` are false, with `problem` and `says`, when it carries none; `testCertificate` marks a test
certificate; `required` says the settings ask for them. `sha256` and `size` are the delivered file's (its credentials
included); the render's own stay under `render`. `manifestHash` is the SHA-256 of the manifest store in the file.

The `.notes.md` file says it in a few plain sentences ("This video carries Content Credentials, …", or why it went out
without them), with the advice that matters: upload the file as it is — editing or converting it takes them out. The
version keeps a line per signed file (who signed, checked, the manifest's hash), and the [receipt](receipt.md) lists
them ("Version 3 went out with Content Credentials on 3 Nov 2026, signed by “Example Studio”: 2 files, checked after
signing").

`metarush credentials check <video>` reads any video's Content Credentials: whether it carries them, whether they
check out, who signed them and whether that is this computer's certificate.

## The dashboard

- **Settings → Delivery → Content Credentials**: off, on or required; this computer's certificate (make a test one, use
  your own, try it on a sample, remove it); a timestamp service. Everyone can read it; owners change it.
- **A project's Settings tab → Content Credentials**: a client's own word.
- **The version page**: Ready to deliver has **Deliver…**, which delivers the final video into a new folder in
  MetaRush exports (in Movies on a Mac, Videos elsewhere; Show in Finder, or from another computer download it),
  saying what Content Credentials each video carries. Held by "its videos must carry Content Credentials" has Set them up.

## Commands

| Command | What it does |
|---|---|
| `metarush credentials [status] [--project <p>]` | whether delivered videos carry them (and whose choice), and whether this computer can sign |
| `metarush credentials setup --test [--organisation <name>] [--replace]` | an owner, at their own terminal: a test certificate on this computer |
| `metarush credentials setup --cert <chain.pem> --key <key.pem> [--replace]` | an owner, at their own terminal: your own certificate, the key into the keychain |
| `metarush credentials test` | sign a one-second sample and check it |
| `metarush credentials check <video>` | what a video's Content Credentials say |
| `metarush credentials remove` | an owner, at their own terminal: remove this computer's certificate and key |
| `metarush credentials on\|required\|off [--timestamp-url <url> \| --no-timestamp]` | owners: the workspace's choice (asking less is a person's) |
| `metarush content credentials <on\|required\|off\|workspace> [--project <p>]` | owners: a client's Content Settings |

Every command takes `--json`.

## Testing

- `go test ./internal/c2pa/ ./internal/ops/ -run 'Credentials|Manifest|ShipGateCanRequire|DeliveryWithout'` uses a
  c2patool stand-in (`internal/testutil/fakec2patool`) that speaks the same signing protocol — it asks MetaRush's
  signer for the chain and the signature and checks them against the certificate — writes the Content Credentials box
  where c2patool does, and can be told to fail. Certificates and keys are made while the tests run; none is ever kept
  in the repository. The real c2patool is never run under `go test` unless a test names it.
- Once on a computer that has the real one:
  `METARUSH_REAL_C2PATOOL=/path/to/c2patool go test -tags c2pareal -run TestRealC2patool -v ./internal/c2pa/` signs
  a short video ffmpeg makes with a test certificate and with brought certificates of every key kind, reads each back,
  checks every assertion is a created one, and searches the signed file for names that must never be in it.
- The browser run `scripts/content-credentials-e2e.sh` (ports 5780–5789) turns them on in Settings → Delivery with a
  test certificate, delivers from the version page, and reads the delivered file's credentials back
  (`C2PATOOL=/path/to/c2patool` uses the real one).

## Not yet

- Posts to YouTube and review links send the render without Content Credentials.
- No trust list is loaded: MetaRush checks a signature, not whom verifiers trust.
- C2PA's newer AI disclosure assertion and the CAWG identity assertion aren't written.
- The key lives in the keychain: hardware keys and cloud key services would need a signer of their own.
