# Project motion graphics (code a person approves)

Use a vetted template whenever one fits (`metarush edit templates --json`). When none does — an animated route,
a number that counts up, a chart that builds, a logo sting in the show's style — a project can carry its own
Remotion component. MetaRush checks it, a **person** approves the exact version, and only then can a cut use it.

## The folder
```text
<project folder>/motion/<name>/      name: lowercase letters, digits, dashes (count-up, route-map)
  index.tsx      default export: the component; named exports schema and meta
  preview.json   sample values for every field (the test frames use them)
  *.ts / *.tsx / *.json   more of its own files, if it needs them
```
`metarush motion new <name> --json` writes a working example to start from. In the dashboard's Claude panel,
write the folder inside the proposals folder named in the message (`<proposals>/motion/<name>/`).
Only `.tsx`, `.ts` and `.json` files; no images, fonts, packages, `package.json` or `tsconfig.json`. Pictures come
from the project's resources by name instead (below).

## index.tsx
```tsx
import { AbsoluteFill, interpolate, useCurrentFrame } from 'remotion';
import { z } from 'zod';
import { makeOverlayClock, rgba, type MotionMeta, type MotionProps } from '@metarush/motion';

export const schema = z.object({
  target: z.number().int().min(1).max(10_000_000).describe('The number it counts up to'),
  label: z.string().min(1).max(40).describe('The words under the number'),
});

export const meta: MotionMeta = {
  summary: 'A number that counts up to a goal, with a label under it.', // one plain sentence for the person approving it
  region: 'center',                                                    // lower | center | full
  minDuration: 2,
  maxDuration: 6,
  checkAt: [1.5],                                                      // optional extra test frames (seconds)
};

export default function CountUp({ params, durationInFrames, brand, layout }: MotionProps<typeof schema>) {
  const frame = useCurrentFrame();
  const clock = makeOverlayClock(frame, durationInFrames, layout.fps, 1.4);
  const n = Math.round(params.target * clock.enter(0, 1.4));
  const { content, unit } = layout;
  return (
    <AbsoluteFill style={{ pointerEvents: 'none', opacity: clock.exit }}>
      <div style={{ position: 'absolute', left: content.left, top: content.top, width: content.width, height: content.height,
        display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', fontFamily: brand.fontFamily, color: brand.text }}>
        <div style={{ fontSize: 160 * unit, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>{n.toLocaleString('en-US')}</div>
        <div style={{ fontSize: 44 * unit, color: rgba(brand.text, 0.8) }}>{params.label}</div>
      </div>
    </AbsoluteFill>
  );
}
```
- **Props** (`MotionProps<typeof schema>`): `params` (already checked against `schema`), `durationInFrames`,
  `from`, `planDurationInFrames`, `brand` (`text`, `accent`, `background`, `primary`, `fontFamily`) and `layout`
  (`content` = the box text may use inside the platform's safe area, `unit` = 1 at 1080 px, `fps`, `orientation`).
  `useCurrentFrame()` is 0 when the graphic appears.
- **Imports allowed:** `remotion`, `react`, `zod`, `@remotion/zod-types`, `@remotion/shapes`, `@remotion/paths`,
  `@remotion/layout-utils`, `@remotion/transitions` (and its presentations), `@metarush/motion` (the helpers above,
  `fitWords`, `splitWords`, `pick`, `ramp`, `EASE_OUT`…) and files in the component's own folder. Anything else
  is refused, and so are `import()`, `require`, `eval`, `new Function`, Node built-ins and network APIs (`fetch`,
  WebSocket…). It runs only in the renderer's headless browser, which has no network.
- **Deterministic:** frames render in parallel and in any order. No `Math.random()` (use `random('seed')` from
  remotion), no clock (`Date.now()`); derive everything from `useCurrentFrame()` and the params.
- **Readable:** keep text inside `layout.content`, size it with `unit`, use the brand colors and font, enter and
  leave gently (`makeOverlayClock`), and leave room for captions in the lower band unless region is `lower`.
- **Pictures from the project's resources** (logos, photos, code slides, diagrams, page snapshots):
  ```tsx
  import { ResourceImage, fitInside, useResource } from '@metarush/motion';
  export const schema = z.object({ shot: z.string().meta({ format: 'metarush-resource' }) }); // a field that names one
  const logo = useResource('logo');          // a name in quotes: checked by motion check, pinned by every cut that uses it
  const shot = useResource(params.shot);     // or the field above (preview.json gives a sample name)
  const box = logo ? fitInside(logo, 400 * unit, 160 * unit) : null;
  return logo && box ? <ResourceImage pic={logo} style={{ width: box.width, height: box.height }} /> : null;
  ```
  Only a name in quotes or a resource field works (`useResource("deck#3")` is page 3 of a PDF, `"homepage#2"` a
  page snapshot on a phone); a name the project doesn't have fails the check. The pictures come by hash from the
  renderer itself; the component never sees an address and still has no network.
- **Fonts from the project's resources:** `useFont('inter', 'Inter', { weight: '400 800' })` (from
  `@metarush/motion`, called at the top of the component) draws the font resource `inter` as the CSS family
  `Inter`; then `fontFamily: 'Inter'`. The cut pins it like a picture.
- **Shared libraries**: what several graphics share (a project's colours, fonts, components) can live in a
  library folder — `library.json` with `{"summary": "…"}` and an `index.tsx` that exports it — imported as
  `@metarush/lib/<name>` (`import { Pill, COLORS, useThemeFonts } from '@metarush/lib/brand-theme'`). It is checked
  and approved like a graphic, once per version for every graphic that uses it; a cut sheet line names its version
  after a `+` (`motion end-card@3fa9c2d1+brand-theme@7e3a91c0`) or gets the newest approved one. Graphics lifted from a
  Remotion project (`metarush remotion lift`, the `video-remotion` skill) come with their project's library.

## The loop
1. Write or edit the files.
2. `metarush motion check <folder> --json` (in a project folder, `metarush motion check <name>`). It reads the files
   into one version (`hash`), scans the imports, type-checks, bundles and draws test frames and a short preview.
   - `ok: false` → fix `problems` (each has `file`, `line`, `message`, `hint`) and check again.
   - `ok: true` → **Read `sheet`** (the contact sheet; the dashed amber line is the safe area) and judge it as a
     viewer would. Fix what looks wrong and check again; each change is a new version.
   - `problems` with severity `warning` are notes for the person (safe area, non-deterministic, slow) — fix them
     or say why not.
3. **Approval is for people.** Tell the person, in one sentence, that the graphic is ready for them to look at and
   approve under Motion graphics in the dashboard (it shows the frames, a preview and the source). Never run
   `motion approve` or `motion revoke` yourself; in the dashboard panel, asking with
   `metarush motion approve <name>@<version> --json` makes a card the person decides on (several graphics from one
   reply are one card they can approve together). In the panel the check itself runs by itself when the person's
   setting allows it, and you hear how it went in the next message. Talk about the graphic, not its code: what it
   looks like and does on screen ("a dark bar along the bottom with the speaker's first name"), never file names,
   lines or the version code; a problem the check found is said the same way ("it can stay on screen for at most a
   minute").
4. Once approved (`metarush motion show <name>@<version> --json` → `row.state: "approved"`), use that exact version
   in the cut — in the cut sheet's `## Graphics`:
   `- subs: motion count-up@3fa9c2d1 at 0:02 for 4 s · with target 1000, label "subscribers"`
   (the full version is pinned under `motion:` in the settings when MetaRush writes the sheet), or in a plan as an
   overlay with `"motion": {"name": "count-up", "hash": "<full hash>"}` instead of `"template"`.
   `cut check` / `edit check` validate the fields against its schema and refuse a version nobody approved.
5. Offer a review render. Renders of a version a person withdrew approval from are refused.

Changing any file — even preview.json — makes a new version that needs approval again. A version a person already
approved stays approved when it is checked again unchanged.
