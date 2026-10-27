# Content Credentials on a delivered video — not yet runnable

What it will do: add Content Credentials (C2PA) to a video MetaRush delivered — that it was made with MetaRush,
and which parts are AI-generated (the delivery's record already knows) — signed with your organisation's
certificate, as a copy next to the delivered file (`<name>.c2pa.mp4`). Platforms and viewers that read C2PA then
show where the video came from. It runs c2patool, the C2PA project's command-line tool.

**Why it doesn't run yet**: `metarush ability check` refuses its manifest, because three things it needs
aren't in MetaRush yet:

1. **A delivered video as an input** (`kind: delivered`): today an ability takes clips, pictures and sound from the
   library, not what delivery wrote.
2. **A copy next to the delivered file** (`outputs: [delivered-copy]`): today what a run makes comes into the
   library. Writing beside a delivered file changes something outside MetaRush, so a person would start each run.
3. **Signing without the key in the sandbox** (`signs:`): signing needs a private key. Abilities never hold keys,
   so the proposed shape has MetaRush sign for the run: c2patool's `--signer-path` runs `signer.py`, which
   asks the run's proxy (`signer.metarush.invalid`) for the certificate chain and a signature; the key stays in
   this computer's keychain, added by an owner, and each signature is recorded. Copying the key into the run's room
   (as `needs.files` copies a model) was rejected: a run could write it into the video it delivers.

These may instead arrive as Content Credentials built into delivery itself, which would not need an ability.

What the manifest will say: the video is an edit of captured footage (IPTC's `compositeCapture`; with AI-generated
parts, `compositeWithTrainedAlgorithmicMedia`), never `digitalCapture`, which is for footage straight from a camera.

- **c2patool's licence:** MIT or Apache-2.0 (dual). It runs beside MetaRush and is never bundled.
- **Installing it, pinned:** `sh install-c2patool.sh [--prefix /opt/c2patool]` downloads c2patool 0.28.3 from
  its project's releases on GitHub, refuses a download whose SHA-256 isn't the pinned one (Mac: the universal zip,
  `12db3a9b…2787`; Linux x86-64: `74f9d5c5…9600`), and puts it in a folder outside your home folder (abilities never
  run programs in a home folder). Then `metarush ability setup content-credentials --program c2patool=<path>`.
  Homebrew's `c2patool` works too but is an older version (0.27).
- **The certificate:** C2PA viewers trust signatures from certificates on the C2PA trust list; c2patool's built-in
  test certificate is for trying it only. A timestamp server (c2patool's `ta_url`) keeps a signature valid after the
  certificate expires but needs the network.
- **Cost:** free (a signing certificate may cost money). **Key:** a signing key, kept by MetaRush, never by the
  code. **What leaves this computer:** nothing (no timestamp server). **Who starts a run:** a person, each time.
