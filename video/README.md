# @metarush/video

MetaRush's Remotion package. It renders a **RenderPlan**, the frame-accurate edit that the Go side resolves (`internal/editspec.Resolve`), **verbatim**. All timing math lives in Go. This package only maps plan frames to pixels and samples, plus easing inside each template's own duration.

The same component, `MetaRushEdit`, is used in two places:

| Where | How | Media |
|---|---|---|
| Dashboard preview | `@remotion/player` via `@metarush/video/lib` | proxy URLs in `plan.sources` |
| Final render | `node render/render.mjs` (spawned by the Go worker) | originals served from an ephemeral `http://127.0.0.1:<port>/…` server |

## Layout

```
src/
  index.ts              registerRoot (bundle entry for Studio and render.mjs)
  Root.tsx              composition "MetaRushEdit" (metadata from props.plan) + Studio demos
  lib.ts                browser-safe exports for the dashboard
  MetaRushEdit.tsx      layer stack: background, dip mattes, clips, lower/center overlays, captions, full-frame overlays
  metadata.ts           planMetadata() / calculateMetadata
  clips/                OffthreadVideo per clip: framing, cut/fade/crossfade/dip-to-black
  audio/                Html5Audio layers: gainDb, fades, dialogue ducking
  captions/             clean | emphasis | boxed captions, fitted inside the safe area
  templates/            one file per vetted template; ids, versions and regions come from the generated catalog (schema/templates.json)
  theme/                brand tokens, bundled fonts
  core/                 layout (safe area, orientation), motion (easing), text fitting
render/
  render.mjs            final-render CLI (JSON lines on stdout)
  still.mjs             single-frame PNG CLI (QA)
  selftest.mjs          end-to-end test with synthetic media
  shared.mjs            bundle cache, validation, stdout discipline
scripts/gen-fontfaces.mjs  regenerates src/theme/fontFaces.ts from the font packages
```

## Scripts

| Command | What it does |
|---|---|
| `npm run studio -w video` | Remotion Studio on **http://localhost:4842** (`MetaRushEdit` plus `Demos/*` per aspect ratio, media-free demo plans). Add `-- --no-open` to skip opening a browser tab. |
| `npm run typecheck -w video` | `tsc --noEmit` |
| `npm run render:selftest -w video` | End-to-end check, about 2 minutes. Generates synthetic media and serves it over a Range HTTP server. Renders vertical and landscape and verifies the JSON-lines contract. Uses ffprobe to check size, codec, frame count (±1), duration (±1 frame) and the AAC track, measures the music duck (~12 dB), renders square stills, and checks that invalid props fail cleanly. Review frames go to `.selftest/frames/`. Add `-- --only vertical` to limit. |
| `npm run browser:ensure -w video` | `remotion browser ensure`: downloads Chrome Headless Shell once |

### Chrome Headless Shell

Remotion renders with its own Chrome Headless Shell. It is stored under `video/node_modules/.remotion/chrome-headless-shell/`: Remotion resolves the location from the nearest `package.json` to the process cwd, so `render.mjs` / `still.mjs` `chdir` into `video/` before rendering. Install it ahead of time (about 180 MB download, done once per machine):

```sh
cd video && npx remotion browser ensure     # or: npm run browser:ensure -w video
```

`render.mjs` also calls `ensureBrowser()` on every run. That is a no-op when the browser is present, and otherwise downloads it, logging progress to stderr.

## Dashboard usage

```tsx
import { Player } from '@remotion/player'; // must be exactly 4.0.536, same as this package
import { MetaRushEdit, planMetadata, TEMPLATE_MANIFEST } from '@metarush/video/lib';

<Player component={MetaRushEdit} inputProps={{ plan }} {...planMetadata(plan)} controls acknowledgeRemotionLicense />
```

`@metarush/video/lib` is TypeScript source (like `@metarush/schema`) and needs a bundler that handles `.ts` and `.woff2` imports (Vite does by default). Nothing reachable from it imports Node-only modules. Keep a single copy of `react`, `react-dom` and `remotion` in the tree: `web` should depend on `remotion`/`@remotion/player` **4.0.536** exactly and React `^19.2`.

## Render CLI contract

```
node render/render.mjs --props <file.json> --out <file.mp4>
     [--scale 1] [--crf 18] [--concurrency N] [--bundle-cache <dir>] [--hwaccel if-possible|disable]
```

- `--props`: JSON file `{"plan": RenderPlan}`.
- `--out`: output MP4 (H.264 + AAC, yuv420p). Parent dirs are created.
- `--scale` (default 1): output scale factor (0 < s ≤ 4).
- `--crf` (default 18): x264 CRF 1..51.
- `--concurrency` (default: Remotion's default): parallel browser tabs.
- `--bundle-cache` (default `video/.bundle-cache`): webpack bundle cache. Bundles are keyed by a SHA-256 of `src/**`, `package.json`, `tsconfig.json`, the schema types and the Remotion version. The first bundle takes ~6 s; after that it is reused instantly. Builds are written to a temp dir and renamed atomically, so concurrent renders are safe; old bundles are pruned (newest three kept).
- `--hwaccel` (default `disable`): `if-possible` asks Remotion for hardware H.264 (VideoToolbox on macOS, verified). Hardware encoders cannot honor CRF, so with an explicit `--crf` Remotion encodes in software anyway (logged to stderr). Without `--crf`, a bitrate scaled to the frame size is used (≈0.18 bits/pixel/frame, about 11 Mb/s at 1080p30).
- Environment: `METARUSH_REMOTION_GL` overrides Chrome's GL backend (default `angle` on macOS, GPU-accelerated and Remotion v5's default; `swangle` on Linux, where most hosts that render have no GPU). Set `angle` on a Linux host with a GPU. Review copies use the hardware encoder (VideoToolbox) on macOS only; `METARUSH_REMOTION_HWACCEL=1` lets a Linux host with an NVIDIA GPU use NVENC.

**stdout is JSON lines only**. Everything else (Remotion logs, browser console errors) goes to stderr. In order:

```json
{"type":"versions","remotion":"4.0.536","templates":{"lower-third.clean":"1","lower-third.cinematic":"1","quote.kinetic":"1","endcard.cta":"1","datacard.stat":"1","title.card":"1"}}
{"type":"progress","progress":0,"renderedFrames":0,"encodedFrames":0,"stage":"browser"}
{"type":"progress","progress":0,"renderedFrames":0,"encodedFrames":0,"stage":"bundling","stageProgress":0.42}
{"type":"progress","progress":0,"renderedFrames":0,"encodedFrames":0,"stage":"preparing"}
{"type":"progress","progress":0.513,"renderedFrames":352,"encodedFrames":340,"stage":"rendering"}
{"type":"progress","progress":1,"renderedFrames":675,"encodedFrames":675,"stage":"muxing"}
{"type":"done","output":"/abs/path/out.mp4","durationInFrames":675}
```

- `versions` is always the first line, even when the arguments are invalid.
- `progress` is throttled to about 4 lines per second. `progress` is Remotion's overall render progress (0..1, monotonic). `stage` is one of `browser`, `bundling` (only when the bundle cache misses; it carries `stageProgress`), `preparing`, `rendering`, `encoding` or `muxing`.
- On failure: `{"type":"error","message":"..."}`, then exit code **1**. Validation failures (missing plan, non-positive or odd dimensions, missing or non-http(s) `sources` for any clip or audio asset) are reported before any browser work.
- SIGINT/SIGTERM cancel the render cleanly: `{"type":"error","message":"renderMedia() got cancelled"}`, exit 1.
- The cwd does not matter: paths are resolved first, then the process `chdir`s into `video/`.

`render/still.mjs --props <file.json> --frame <n> --out <file.png> [--scale 1] [--bundle-cache <dir>]` emits `versions`, then `{"type":"done","output":"...","frame":n}` or `error`.

## Plan semantics implemented

- **Composition metadata**: `width/height/fps/durationInFrames` come from `props.plan` via `calculateMetadata`. `render.mjs` refuses to render if the selected composition disagrees with the plan.
- **Clips**: `<OffthreadVideo>` inside `<Sequence from durationInFrames>` with `trimBefore` (source offset in output frames; Remotion maps media time = (trimBefore + f·playbackRate)/fps), `playbackRate`, linear `volume`, and `muted || !hasAudio`.
  - Framing: `fill` = cover, `fit` = contain, `zoom ≥ 1` scales around the focal point. `focusX/focusY` is centered when possible and otherwise kept in frame. The crop is clamped so frame edges are never revealed on a covering axis, and a letterboxed axis is centered.
  - Transitions:
    - `cut`: no ramp.
    - `fade`: opacity ramps in from `plan.background`.
    - `crossfade`: Go already overlapped the clips. The incoming clip sits above and ramps 0 to 1 while the outgoing one keeps playing beneath. Audio uses an equal-power crossfade: incoming up, and the outgoing clip ramps down over the same frames.
    - `dip-to-black`: the previous clip fades out over the `fadeOutFrames` Go set (half the dip). The incoming clip fades in over the remaining `durationInFrames - floor(durationInFrames/2)` frames, over a black matte, so the dip is black even when `plan.background` is not.
  - `fadeOutFrames` fades picture and sound.
- **Audio layers**: `<Html5Audio>` with `trimBefore` and gain `10^(gainDb/20)`. `fadeInFrames`/`fadeOutFrames` are smoothstep ramps. When `duckUnderDialogue` is set, the layer ducks by `|duckDb|` dB during `plan.dialogueRanges`, with ~0.2 s (6 frames at 30 fps) smooth ramps that start *before* speech, interpolated in dB.
- **Captions**: one line at a time from `plan.captions.lines` (timeline frames).
  - Styles:
    - `clean`: soft-edged shade under the line (a blurred flat shape, so it has no visible edge) plus a tight glyph halo. Verified legible over near-white footage.
    - `emphasis`: the same, and the currently spoken word turns accent (only when `precision === "word"`).
    - `boxed`: a per-line translucent box.
  - `emphasis: true` words are always accented. `uppercase` is supported.
  - `position`: `top` | `middle` | `lower` (80% down the safe box) | `bottom`.
  - Lines are measured with the bundled font and wrapped to at most two lines, and a one-letter word never ends a line. Long lines shrink (down to 62%) instead of overflowing, and the block is clamped inside `plan.safeArea` plus a 16px@1080 gutter.
- **Layering and collisions** follow Go's region model (`internal/editspec` validate: `overlay_collision`, `caption_collision`):
  - Bottom to top: background, dip mattes, clips, `lower`/`center` templates, captions, `full` templates. End cards cover captions, as the Go warning text assumes.
  - `center` templates (quote, datacard, title) that share screen time with caption lines are laid out outside the caption band (two lines at full size, plus a gap), so a stat card never sits on the words being spoken.
  - When a lower third is on screen during a caption line at `lower`/`bottom`, that line is placed about one line-height above the strap. This is decided once per line, so a caption never moves mid-line. Go still warns about the collision; the render stays clean.
- **Overlays**: `template` + `params` inside `<Sequence from durationInFrames>`. Unknown template ids render nothing and log one console warning. A template whose params are invalid, or that throws, is skipped by an error boundary instead of failing the preview or render. A `templateVersion` other than the shipped one logs a warning.

## Templates (v1)

Every template enters and exits inside its own duration. The exit takes the last ~0.42 s (13 frames at 30 fps). If an overlay is too short for the full entrance, the entrance compresses proportionally. Layouts adapt to vertical (1080x1920), landscape (1920x1080) and square (1080x1080). Sizes are authored at a 1080 px short edge and scale with the frame. Templates use `plan.brand` (`primary, accent, text, background, font`) and `plan.safeArea`. Easing is one family: `bezier(0.16, 1, 0.3, 1)` for entrances, ease-in for exits. There is no overshoot and no blur on text.

| id | Design |
|---|---|
| `lower-third.clean` | Name + title beside an accent hairline that draws down while lines rise into place. Soft corner scrim. `align` left/center/right. |
| `lower-third.cinematic` | Letter-spaced Fraunces small caps (always serif, by design). A slow soft-edged wipe while tracking settles inward, then a drawn hairline and a tracked-out title. Low gradient. |
| `quote.kinetic` | Large left-aligned pull quote on a dimmed frame, with an accent opening mark. Words surface in sequence (fade + short rise). `emphasis` words or phrases get the accent. `motion: "none"` is a single fade. Attribution follows an accent rule (no em dash). |
| `endcard.cta` | Full brand-background card: headline lines rise, then subline, a pill CTA whose label color is contrast-checked against the accent (WCAG 4.5:1) and never wraps, and the handle anchored to the bottom safe edge. If the card ends with the video, the background holds through the exit. |
| `datacard.stat` | One statistic on a quiet panel. Numeric values (`73%`, `$1.2M`, `3,400`) count up in tabular figures with their final width reserved, so nothing shifts. Optional trend mark and context. The **source line is part of the panel from its first frame** and is never truncated. |
| `title.card` | Opening title over a softened frame (dim + vignette + light defocus). Lines rise from their baselines, an accent rule draws, the subtitle settles. A title at frame 0 starts already softened. |

## Fonts

All typefaces are OFL-1.1 variable fonts bundled from `@fontsource-variable/inter`, `@fontsource-variable/fraunces` and `@fontsource-variable/jetbrains-mono`. Every subset (latin, latin-ext, cyrillic, greek, vietnamese, about 620 KB total) is imported as a local asset by `src/theme/fontFaces.ts` (generated by `scripts/gen-fontfaces.mjs`). The faces are registered through the `FontFace` API under private names (`MR Inter`, `MR Fraunces`, `MR JetBrains Mono`), so they never collide with fonts the dashboard loads.

There are **no network font fetches**. `useBundledFonts()` holds the frame with `delayRender` until every face has loaded. Overlays and captions mount only after that, so text measurement never sees a fallback font. In a final render, a font failure cancels the render. In the Player it degrades to the fallback stack with a warning. `brand.font` (`Inter` | `Fraunces` | `JetBrains Mono`) selects the family for all template and caption text except the cinematic lower third's serif name.

## Remotion license

Remotion is **not** MIT-licensed. It is free for individuals and for organizations of up to 3 people. Larger companies need a **Remotion Company License**: <https://www.remotion.dev/docs/license>. MetaRush does not bundle a Remotion license. **Each organization that installs MetaRush is responsible for obtaining its own Remotion license** if it needs one.
