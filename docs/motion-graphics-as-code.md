# Motion graphics as code

A project can carry its own motion graphics: small Remotion (React) components in the project's `motion/` folder,
for the one-off motion the vetted templates don't cover — a logo sting, an animated map of a route, a chart that
builds bar by bar, a title in a show's own style. Claude Code writes them, MetaRush checks them, a person approves
each version, and renders draw them in the same pipeline as everything else.

The vetted templates (lower thirds, kinetic quotes, title, data and end cards) stay the default and the safe path:
their code ships with MetaRush and Claude only fills in checked parameters. A project's own components are code,
so they run only behind four guards: each version is identified by the hash of its sources, its imports must be on
an allowlist, it runs only inside a network-jailed headless browser, and a person approves each hash before any
render uses it ([Safety](#safety)).

## What it looks like

A project folder (see [cut-sheet.md](cut-sheet.md#project-folders-and-git)) has a `motion/` directory:

```text
builder-story/
  cut.md
  footage.md
  motion/
    route-map/
      index.tsx        default export: the component; named exports: schema (zod), meta
      preview.json     sample parameters for test frames
  .metarush/lock.json  also records each component's name, hash and approval
```

`index.tsx` is an ordinary Remotion component. `schema` (zod; `@remotion/zod-types` for colors and the like)
describes its parameters; the check converts it to JSON Schema with zod's own converter, inside the render page, and
stores it with the version, so Go validates parameters exactly as it does for vetted templates. `meta` gives a
one-sentence `summary` for the person approving it, the duration range (`minDuration`, `maxDuration`), the screen
region (`lower`, `center`, `full`) used by collision checks, captions and QA, and optional `checkAt` moments for
extra test frames. Components receive the same props as templates (`params` already checked, `durationInFrames`,
`from`, `planDurationInFrames`, `brand`, `layout`); `@metarush/motion` exports their types (`MotionProps`,
`MotionMeta`) and the theme and layout helpers the templates use. `metarush motion new <name>` writes a working
starting point; the authoring guide for Claude is `skills/video-motion-design/reference/motion-code.md`.

The cut sheet references a component by name and version in `## Graphics`, and the settings pin the full version
(like `footage:`), so the sheet stays lossless:

```markdown
---
motion:
  route-map: sha256:3fa9c2d1…   # approved by Ada · 18 Oct 2026
---
## Graphics
- route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from "Lisbon", to "Porto"
```

In the spec this is an overlay with `motion: {name, hash}` instead of `template` (exactly one of the two). See
[cut-sheet.md](cut-sheet.md#graphics-sound-and-captions) for the parsing rules.

## Making one

1. Claude Code writes or edits `motion/<name>/index.tsx` in the project folder (`motion new <name>` scaffolds
   one; in the dashboard's Claude panel, in that conversation's proposals folder).
2. `metarush motion check <name|folder>`: archive and hash, the import scan, a type check with MetaRush's
   TypeScript settings, the bundle, then test frames (first, start, middle, end, last and any `meta.checkAt`
   moments), a determinism check (the middle frame drawn twice) and a short preview video, headless with the
   network off. It prints the version and, with `--json`, the contact sheet and frames for Claude to Read, plus
   notes (outside the safe area, region mismatch, non-deterministic, slow frames). A passing version is recorded
   (archive, sheet, frames and preview in the media store) and waits for a person; a failing check records
   nothing. Claude Code may run a check on its own in a terminal; in the Claude panel, a check is a card the person
   runs.
3. A person reviews it in the dashboard (Motion graphics → the version: source, imports, frames, preview), or
   previews it in Remotion Studio on their own machine (`metarush motion studio <name>`, dev port 4842).
4. A person approves the version (dashboard, or `metarush motion approve` in their own terminal).
5. The cut sheet references `motion <name>@<version>`; `cut check` validates the parameters against the component's
   schema and reports an unapproved version as an error that names the approval step. `project push` makes sure
   the media store holds every version the cut uses; checkout and pull write the sources of versions the folder
   lacks.

## What a version and an approval cover

- **A version is its sources**, `preview.json` included. The renderer's version is recorded per check and per render
  as for any template (`templateVersions` also lists `motion:<name>` → version). Editing one character makes a new
  version.
- **An approval is per workspace**: a version approved once can be used in every project of the workspace (the
  workspace is the trust boundary, one team per database). Because approval belongs to the hash, a recipe that
  runs again with the same components needs no new approval; changing a component does.
- **Withdrawing** a version keeps its files and existing renders; new renders and QA are refused, `cut check`
  reports it, and a person can approve it again.
- **Parameters** are checked with the JSON Schema the check stored from the component's zod schema; Go refuses
  external `$ref`s.
- **Pictures** come from the resources library by name ([below](#pictures-from-the-library)). They are never part
  of the version, so a new picture version needs no new approval (the cut moves to it with `cut update`).
- **Determinism**: the check draws the middle frame twice and warns when the two differ (not a failure:
  antialiasing differences stay below the threshold); the scan warns about `Math.random` and the clock.
- **Ownership**: component code written inside a project belongs to the project like its cut sheet; MetaRush adds
  no license terms.

## Pictures from the library

A component draws the project's resources ([resources.md](resources.md): logos, pictures, SVGs, PDF pages, web page
snapshots, code slides and diagrams) by name, never by address or file:

```tsx
import { ResourceImage, fitInside, useResource, type MotionProps } from '@metarush/motion';

export const schema = z.object({ shot: z.string().meta({ format: 'metarush-resource' }) });

export default function CardReveal({ params, layout }: MotionProps<typeof schema>) {
  const card = useResource('acme-widget-card'); // a name in quotes ("deck#3": page 3; "homepage#2": the phone picture)
  const shot = useResource(params.shot);        // a parameter marked as a resource
  const box = card ? fitInside(card, layout.content.width * 0.9, layout.content.height * 0.6) : null;
  return <>{shot && <ResourceImage pic={shot} fit="cover" />}{card && box && <ResourceImage pic={card} style={box} />}</>;
}
```

`useResource` returns the picture (its size and, for a page, its address) or null when the cut has none by that
name; `<ResourceImage>` draws it and holds the frame until it has loaded; `fitInside` sizes it into a box. The rules:

- The import scan accepts `useResource` only with a name in quotes or `params.<field>` (also `props.params.<field>`),
  called directly: a computed name, a variable, or passing the function around is refused with its line
  (`resource_name`). No new imports are allowed: pictures never enter the archive, and `.png` imports stay refused.
- `motion check` scans first, then looks every quoted name — and each sample parameter in `preview.json` that
  names a resource — up as the project sees them (its own resources, linked projects', the workspace library): a
  name the project lacks, or one that isn't a picture, fails the check. A `params.<field>` whose schema doesn't say
  `format: 'metarush-resource'` fails too (`resource_param`): a cut couldn't pin the picture it names. The test
  frames are drawn with exactly those files, served by hash from the check's own loopback server inside the jail.
- The version records where its code draws resources (`MotionComponent.resources`). A cut that uses it must pin
  the quoted names in `spec.resources` (`cut check` reports `resource_unpinned`; a cut sheet pins them when it is
  checked), and the marked parameters' values like any template's. Renders serve them the same way as for vetted
  templates, by hash from the worker's loopback media server: the network jail and the CSP are unchanged.

## Graphics that stay up for a section or the whole video

`meta.maxDuration` is at most 60 seconds, which suits a sting or a title but not a bar that has to stay on screen
through a whole section. A component made to stay up says so: `meta.persistent: true` (then `maxDuration` may be left
out, or be any length). A cut places it with a **span** (the whole video, or from one clip to another) instead of a
time and a length, and can change its fields while it stays on screen (**changes**: "from this moment in that clip,
the name is Lena"); Go resolves both into frames and hands the component `segments` (`[{from, params}]`) and
`segmentFrom` (where the current segment started) beside `params` (the current segment's fields), so it can animate a
change instead of starting over ([call-recordings.md](call-recordings.md#graphics-that-last-a-section-or-the-whole-video)).
Its check draws the test frames over 20 seconds, and the preview's first and middle frames with a sample change when
`preview.json` has `"changes": [{"at": 2, "params": {…}}]` (seconds from the start). A component without `persistent`
keeps the one-minute limit, and the vetted `name.bar` template covers the common case (a name that follows who is on
screen, over the names a call app printed) without any code.

## Test frames in the cut's shape

The test frames and the preview are drawn in the shape of the cuts the graphic is for, or in several shapes when it is
used in several: the cuts of the project (every branch's latest version) that use a graphic of that name, else the
project's cuts, else the shapes of the brief's deliverables ([keeping.md](keeping.md#the-brief-and-deliverables)),
else vertical. A dashboard card always knows the conversation's project, so a widescreen cut's graphic is checked
widescreen. Each shape gets the full set of test frames (labelled "widescreen", "vertical"…) and its own short preview,
at most three shapes; the notes (safe area, region, determinism) are worked out per shape. The version records every
shape (`MotionComponent.Shapes`), and the dashboard and the card show the frames grouped by shape. `--shape` still
draws exactly one. With no brand on the cut, the frames use the project's brand kit, else the workspace's.

## Stills in the live preview

The dashboard's live preview (the Edit tab and a version's page) shows a checked graphic as the still its check drew,
in its place: the preview plan (`revision.plan`, `edit.preview`, `mix.preview`) carries `motionStills` by hash — the
version's test frames in the plan's shape, else the closest shape, earliest first — filled by the caller like the
footage's `Sources` and never present in a render's plan, so renders are unchanged. `MotionOverlay` picks the frame
nearest the moment: the check drew the graphic over its own length, so the way in and the way out are placed at their
own pace and the rest in proportion, and the very first and last frames (often still blank) are used only when there
is nothing else. A still drawn in the cut's own shape covers the frame exactly as the graphic will; one from another
shape fits the graphic's region. It is labelled "still — moves in renders". `/media/motion/{hash}/still-{n}` serves
test frame n with the check's backdrop (`motion.TestBackground`) keyed out (`motion.Still`: soft edges, the graphic's
colours restored), to signed-in browsers only, same-origin, kept in memory by the frame's SHA-256. A graphic never
drawn keeps the dashed placeholder. The real motion is in the review copy, which follows the cut's latest version.

## Shared libraries and graphics lifted from a Remotion project

A folder with a `library.json` (`{"summary": "…"}`) is a **shared library**: the colours, fonts, components and words
several graphics have in common. A graphic imports it as `@metarush/lib/<name>` (only through its `index.tsx`):

```tsx
import { Pill, COLORS, useThemeFonts } from '@metarush/lib/brand-theme';
```

- It is checked, versioned and approved like a graphic (the same scan, types, bundle and jail; a library can't import
  another library). Its check draws the graphics that use it with the new version, so the person approving it sees
  what changes; **one approval of a library version covers every graphic that uses it**, and approving it moves the
  latest cuts' graphics to it (a new version of each cut).
- A graphic is checked with the newest approved version of each library it imports (else the newest checked).
  Cuts pin the versions: `motion: {name, hash, libs: {brand-theme: <hash>}}`; cut sheets write
  `motion end-card@3fa9c2d1+brand-theme@7e3a91c0` and fill in the newest approved library version when a line names
  none. Renders and QA refuse a library version nobody approved, like a graphic.
- **Fonts**: `useFont('inter', 'Inter', { weight: '400 800' })` (from `@metarush/motion`) draws a font resource by its
  short name under a CSS family name; the cut pins it like a picture.
- **TypeScript settings**: a graphic's `motion.json` or a library's `library.json` may declare
  `{"typescript": {"noUncheckedIndexedAccess": false}}` — the one relaxation allowed, for code lifted from a project
  that was written without it.

**Lifting** (`metarush remotion lift <source> <Composition> [--scene N]`, [remotion-sources.md](remotion-sources.md#lifting-a-scene-into-a-motion-graphic-shared-libraries))
makes both from a Remotion project: the scene's own file plus an `index.tsx` that draws it at its own size fitted to
the frame (its props become the parameters), and `<source>-theme` from every other file it imports, with the
project's Google Fonts as workspace font resources.

## Safety

1. **Content addressing.** A component is its source files, normalized (sorted paths, LF line endings, UTF-8
   without BOM; only `.tsx`, `.ts` and `.json`; no links, packages, `tsconfig.json`, images or fonts) into one
   canonical archive (`metarush.motion/1`, `internal/motion`) whose SHA-256 is its identity. The archive is
   stored in the media store like any object (`objects/<sha>`), so every machine renders exactly the reviewed
   bytes; the renderer checks the hash again before using it.
2. **Import allowlist, enforced before bundling.** The scan parses every `.ts`/`.tsx` file with the TypeScript
   compiler (`video/render/motion/scan.mjs`) and walks it: only `remotion`, `react`, `zod`, `@remotion/zod-types`,
   `@remotion/shapes`, `@remotion/paths`, `@remotion/layout-utils`, `@remotion/transitions` (and its
   presentations), `@metarush/motion` and relative imports inside the component's own folder are allowed. Dynamic
   `import()`, `require` (and `import x = require`), `eval`, `new Function`, `.constructor(…)`, string timers,
   Node built-ins, URLs, `/// <reference>` directives, network APIs (`fetch`, `XMLHttpRequest`, WebSocket,
   WebRTC, `sendBeacon`), workers and WebAssembly are refused with plain, line-numbered problems. Pictures come
   only through `useResource` with a name in quotes or a marked parameter ([above](#pictures-from-the-library)),
   never as imports or URLs. Non-deterministic sources (`Math.random`, the clock) are flagged. Packages come from
   MetaRush's own pinned versions; a component cannot add npm dependencies. The bundle is built by MetaRush's
   esbuild configuration for a browser target (`bundle.mjs`): the component cannot supply loaders or plugins,
   React, Remotion and `@metarush/motion` come from the render page (so it shares the renderer's instances), the
   curated packages are bundled from MetaRush's copies, and the bundler's resolver applies the allowlist a second
   time.
3. **Rendering only inside headless Chromium.** Component code runs only in the browser page Remotion renders,
   never in Node: the Node side parses and bundles the source as text and serves the bundle as an opaque file
   from loopback. Every render page (vetted templates too) is network-jailed: Chrome starts with a refusing
   proxy that only the render's own loopback servers bypass (its footage, Remotion's frame server, the bundle),
   host-resolver rules that make every other name unresolvable, WebRTC limited to that proxy, and a
   Content-Security-Policy in the page; Node refuses to download anything the page names outside the same
   allowlist. Limits: a per-frame time limit, a total time limit (checks 4 minutes; renders 10 minutes plus 20×
   the cut's length), a 2 MB bundle limit. Chrome is killed with its renderer. A check whose component tries to
   reach the network fails and says what it tried.
4. **Human approval per hash.** `motion approve <name>@<hash>` (and `--reject`, and `motion revoke`) is a
   people-only operation (`HumanOnly`: the dashboard, or an interactive terminal that asks for a typed
   confirmation), recorded with who, when and an optional note, after reviewing the source (with the changes since
   the last approved version), the imports, the test frames and the preview. Workers refuse to render, and QA
   refuses to check, any revision that references an unapproved, turned-down or withdrawn version, with a plain
   error that names the approval step; `edit check` and `cut check` report it as `motion_not_approved`. The
   dashboard assistant and headless Claude Code can never approve (the human-only flag plus the assistant
   channel's refusal of human-only operations; a non-interactive terminal is refused too).
5. **No live code in the dashboard.** The dashboard shows a component only as its test frames, contact sheet and
   preview video (pictures and video served with a sandboxing Content-Security-Policy) and its source as text;
   the cut preview shows the still its check drew nearest the moment ([above](#stills-in-the-live-preview)), or a
   labeled placeholder for a version never drawn. Component code never runs in the dashboard, before or after
   approval.

Threats considered: a buggy or malicious component written by a model or a collaborator trying to read local files
(browser only, no `file://`, no Node built-ins), send data out (network jailed; also through Remotion's own
downloads), spend the machine's time (limits), or act as the person in the dashboard (its code never runs there).
Residual risks: a component that is approved but misbehaves visually (QA and review copies catch that the same
way as for any cut); a deliberately obfuscated component that computes a forbidden name at run time gets past the
static scan, but not the jail; Remotion Studio (`motion studio`) runs a component in the author's own browser,
outside the jail, and is meant for components they wrote or read.

## Project components and vetted templates

- **Vetted templates stay the default** and the only kind unattended flows introduce on their own; the skills
  reach for a project component only when no template fits. A component that proves itself across projects can
  be promoted into a vetted template: code review, tests and a MetaRush release, after which it is just another
  template ID.
- **Recipes** ([recipes.md](recipes.md)) can pin motion components by hash, so a run with the same components needs
  no new approval.
