# Layout packs

A client's videos should look like theirs every time: the same title card, the same lower thirds, the same end
screen, quote cards and chapter cards, and call recordings laid out the same way — in the client's colours, font
and logos. A **layout pack** says which graphic plays each of those parts. It is part of a project's
[Content Settings](resources.md#content-settings), so it follows, copies and shows in the
**Settings** tab like the brand, the house style and the rest: every project using a client's settings draws with
the client's pack.

## What a pack holds

| Part (role) | Drawn by (vetted templates) | |
|---|---|---|
| `title-card` | `title.card`, `slide.title` | opens a video or a part of it |
| `lower-third` | `lower-third.clean`, `lower-third.cinematic` | who is speaking |
| `end-screen` | `endcard.cta`, `end.screen`, `slide.title` | the end: a call to action, or room for the next videos |
| `quote-card` | `slide.quote`, `quote.kinetic` | a quote with who said it |
| `chapter-card` | `slide.title`, `title.card` | starts a chapter (`kicker` "Part 2") |
| `picture-in-picture` | — | a call recording: one person full frame, another in a corner |
| `side-by-side` | — | a call recording: people next to each other (stacked too) |

Any graphic part may instead be drawn by a **project motion graphic** a person approved. A part may have
variants (`lower-third:guest`, `lower-third:host`). For each graphic the pack keeps:

- **fields** — its choices for the graphic. Fields that choose among fixed options, numbers and yes/no (alignment,
  motion, an end screen's slots) are **how it looks**: always the pack's. Text fields are **words**: the pack's
  words are used only where a cut gives none (an end screen's call to action and handle).
- **seconds** — how long it stays when a cut doesn't say.
- **logo** — show the brand's logo; graphics on the brand's background get the logo made for it.

Call layouts: picture in picture's **corner**, **size**, **shape** (rounded, square or a circle — a round
camera bubble, its name tag centred at its foot inside the circle) and **frame** (a line in the brand's accent, primary or text colour); side by side's **gap** (none,
thin, wide with a margin), **frame**, **backdrop** (black or the brand's background) and **rounded** corners.

Until a client has a pack, cuts use **MetaRush's own** — the vetted templates in the house style: the house's
name-title look for lower thirds, its intro and end card templates for the title card and end screen (the end card's
words for an end screen that has none), call layouts as they always looked. It also fills any part a client's pack
leaves out. A pack of your own starts as a copy of it.

### The end screen template

`end.screen` (5–20 s, full frame) is the one template packs added: a headline, a line under it and the logo
on one side, and empty room for what a video platform places over the last seconds of a video — two videos and
the subscribe button (`slots: two-videos`, the default), one video and the button (`one-video`) or two videos
(`videos-only`). `outline: true` draws a dashed guide where each goes while you place them in the platform's editor.
On a vertical or square frame the room sits below the words.

## Using it in a cut

A graphic names its part, and MetaRush draws it with the pack when the change is checked:

```markdown
## Graphics
- intro: title card at 0:00 for 3 s · with title "Spring launch"
- ada: lower third at 0:04 for 3 s · with name "Ada Lovelace", title "Engineer"
- guest: lower third: guest at 0:20 for 3 s · with name "Grace Hopper"
- end: end screen at 0:52 for 5 s · with headline "Thanks for watching"
```

In a plan it is `"role": "title-card"` with no `template`. Checking fills each one: the pack's template or motion
graphic, your words carried over (a title becomes an end card's headline when the template calls it that), the
pack's look, the logo, its seconds when you gave none, a length the graphic allows. MetaRush writes the sheet back
with both — `- intro: title.card at 0:00 for 3 s · as title card · with …` — because the cut keeps what it was drawn
with: **saved versions never change** when the pack does. A brand-new cut follows the pack in every graphic that
names a part, and the house style's intro and end card name theirs.

After the pack changes, the latest cuts that don't follow it are offered **Use the layout pack on the latest cut**
(`content.pack.apply`, a checked change: locked graphics stay; Settings → Layout pack shows the offers;
`metarush content pack apply --project <p>`). Claude's context names the pack, so it names parts too.

## Packs Claude proposes

Code reaches a pack only through a person:

1. Claude writes the client's custom pieces as project motion graphics in the conversation's proposals folder
   (`motion/<name>/`), and each is checked (a card or a safe step) like any motion graphic.
2. It writes the pack as JSON — the shape of `metarush content pack show --json`'s `pack`:

   ```json
   {"name": "Acme spring", "pieces": {
     "title-card": {"motion": {"name": "acme-title", "hash": "<its version>"}, "fields": {"wipe": "left"}, "seconds": 4},
     "lower-third": {"template": "lower-third.cinematic", "fields": {"align": "center"}},
     "end-screen": {"template": "end.screen", "fields": {"slots": "two-videos"}, "logo": true}},
    "layouts": {"pip": {"shape": "circle", "frame": "accent"}}}
   ```

3. It asks `metarush content pack propose --project <p> --file <proposals>/pack.json --note "…"`. In the panel that is
   refused for Claude and becomes a card **only a person decides** — never a safe step, never Auto. It lists what
   draws each part and which graphics still wait for approval.
4. A person approves each motion graphic (Library → Motion), then accepts the pack: on the card, under Settings →
   Layout pack (**Use this pack**), or `metarush content pack accept` in their own terminal (people only). Accepting
   before a graphic is approved keeps the pack as the settings' **proposed pack** and says what to approve first.
   A graphic that imports a shared library gets the version the pack already uses, else the library's newest,
   pinned when it is proposed, and the library is approved like the graphic; a pack uses one version of each library.
   When a newer version of a library is approved, the latest cuts move to it and the pack is offered it (**Use it in the
   pack** in Settings, `content pack set --library name@version`), which a person takes. A pack proposed before a later change to the pack can't be accepted as
   it is (it would undo that change), and accepting is always of the proposed pack the person saw.

From a terminal, Claude Code (or anyone) proposes the same way; `content pack set` changes a pack with vetted
templates directly, and puts a motion graphic in only for a person (it asks for a typed confirmation).

## Seeing it

Settings → Layout pack previews each part in the brand and house style: graphics play in the player with the
vetted templates — a motion graphic shows the stills its check drew, never its code — in the latest cut's shape or
one you pick; call layouts show where Go puts the pictures. Choose a template or an approved motion graphic per part,
show the logo, set call layouts, name the pack, or go back to MetaRush's own. `metarush content pack preview --json`
returns the same plans.

The quality check's `house_style` notes a graphic that names a part the pack doesn't have (`pack_role`: MetaRush's
own drew it) or one drawn with another graphic or look than the pack's (`pack_override`), each with the change that
follows the pack. They are notes: a cut may choose its own.

## Commands

| | |
|---|---|
| `metarush content pack show [--project <p>]` | the pack in use, what draws each part, a proposed pack |
| `metarush content pack set [--project <p>] --role <part> --template <id> [--fields '{…}'] [--seconds s] [--logo]` | change a part (a pack of the settings' own; without `--project`, the default's) |
| `… set --motion name@version` | a motion graphic a person approved, by a person (asks to confirm) |
| `… set --pip-corner/--pip-size/--pip-shape/--pip-frame …`, `--gap/--frame/--backdrop/--rounded` | call layouts |
| `… set --name …`, `--remove <part>`, `--reset` | the pack's name; a part back to MetaRush's own; the whole pack back to MetaRush's own |
| `metarush content pack propose --file pack.json [--project <p>] [--note …] [--dry-run]` | a whole pack, as a draft |
| `metarush content pack accept` / `decline` | make the proposed pack current (people only) / set it aside |
| `metarush content pack apply --project <p> [--dry-run]` | the latest cut follows the pack (a checked change) |
| `metarush content pack preview [--project <p>] [--role <part>] [--shape vertical] [--draft] --json` | previews |

Operations: `content.pack.show|set|propose|accept|decline|apply|preview`. Browser run: `scripts/layout-packs-e2e.sh`
(ports 5490–5499, a fake `claude`, nothing paid). The render self-test's `layout-pack` variant checks the call
layouts' looks and the end screen in real renders.
