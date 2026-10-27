# Motion-design rules (timing, layout, restraint)

Times are timeline seconds. `metarush revision plan <rev> --json` gives cut positions (`clips[].from`) and caption
lines (`captions.lines[].from/to`) in frames; divide by `fps`.

## Templates and their place on screen
| Template | Region | Reads best | Use for |
|---|---|---|---|
| `lower-third.clean` | lower | 3–5 s | name + role, interviews |
| `lower-third.cinematic` | lower | 3.5–6 s | documentary tone |
| `title.card` | center | 1.5–3 s | opening title over a softened frame |
| `quote.kinetic` | center | words ÷ 3 + 1 s | a pull quote the person actually said |
| `datacard.stat` | center | 3–5 s | one sourced number |
| `endcard.cta` | full frame | 2–4 s | headline, call to action, handle (covers everything under it) |
| `logo.corner` | corner | as long as wanted | the logo kept small in a corner (top-right by default) |

`title.card`, both lower thirds and `endcard.cta` take an optional `logo` (a resource's short name); `title.card`
and `endcard.cta` also an `image` behind them (a picture, or a PDF page as `"deck#3"`).

Template minimum/maximum durations come from `metarush edit templates --json`; `edit check` warns when shorter.

## Timing
- **Start on a beat**: at least ~0.3 s after a cut (never within ±4 frames of one) and not mid-word — start in a
  pause or at the start of a phrase. End before the next cut, or hold across it deliberately.
- **Lower thirds** appear when the person first speaks on screen (0.5–1.5 s into their first shot) and leave
  before the next speaker or cut.
- **Quotes** must match the transcript word for word; time them over or just after the line they quote.
- **End cards** start after the last spoken word (captions under a full-frame card are hidden) and run to the end.
- **One at a time**: no two graphics in the same region at once; in a 15–60 s short, one or two graphics total.
  "Intimate" or "not an ad" briefs usually want none beyond a name strap.

## Layout and collisions
- Captions `lower`/`bottom` share the lower band with lower thirds; the renderer lifts captions above a strap,
  which can push them over faces. Prefer a caption-free moment for the strap, or captions `top` while it shows.
- Center cards (quote, title, data) and `middle` captions compete; move captions to `lower` during the card.
- Vertical deliveries: set `output.platform` (`shorts`, `tiktok`, `reels`) so the safe area clears the app UI
  (bottom ~20–22 %, right ~10–14 %). `generic` margins are smaller.
- Look at a frame before covering part of the picture: graphics never sit on a face or on on-screen text.

## Brand
- `setBrand` tokens: `primary`, `accent`, `text`, `background` (`#RRGGBB`) and `font` (Inter, Fraunces,
  JetBrains Mono). Use only colors and fonts the user or the brief states; keep text/background contrast high.
- Brand rules belong in the project brief (`--constraint`); quote them back when you apply them.
- **The brand kit** (`metarush brand show --project <prj> --json`) is the logos, colours and font of the Content
  Settings the project uses (its own, another project's or the default's). New cuts start with
  it (its logo pinned as `logo`, its font file as `brandfont`); a person can put it on an existing cut
  (`metarush brand apply <prj>`, a checked change).

## Logos and pictures (resources)
- "Our logo" is a resource: `metarush resources list --project <prj> --json` (tags like `logo`, the brand kit's
  `logo`). Look at it before placing it (`metarush resources path <name> --json`, then Read the file). If there is
  none, ask the person to add it (drop the file on the project's Resources tab); never draw or invent a logo.
- On a dark picture use the light logo (`logoDark` in the brand kit), on a light one the dark logo.
- Corner logos stay small (`size: small` is a tenth of the frame's width), clear of the platform UI corner (right
  side on vertical apps: prefer `top-left` or `top-right` with `platform` set), out of the captions' band and never
  over a face. One corner logo per video; end it where an end card starts (the card shows the logo itself).
- Pictures behind a title or end card must be the person's own or ones they may use; MetaRush notes web pictures
  without a recorded licence in QA, for the person to check.

## Claims
- `datacard.stat` needs `params.source` (shown on screen) and an `evidence` entry (kind `user` or `analysis`)
  saying where the number came from. If the user has no source, don't make the card.

## When no template fits
A project can carry its own animated graphic (see [motion-code.md](motion-code.md)): you write it, MetaRush checks it
and draws test frames, and a person approves that exact version before a cut may use it. Reach for it only when the
templates can't say what the person asked for; the same timing, collision and claims rules apply.
