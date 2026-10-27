# Pictures for the fixture repository's art folder

`scripts/make-fixture-repo.sh <dest> art` puts these in `acme/widget` under `art/characters/` (pictures kept
from a repository's folders), with files that show what is left out and why; `art-second` changes `alice.png` (to
`alice-2.png`), removes `bob.jpg` and adds `frank.png`. Small (under 1 KB each) and committed as they are: they were
drawn once with Go's image encoders (checkerboards of two colours), so every machine fetches the same bytes.

| File | What it is |
|---|---|
| `alice.png`, `alice-2.png`, `frank.png` | PNG pictures (alice-2 is alice with another colour) |
| `bob.jpg` | a JPEG |
| `dot.gif` | a GIF with one frame |
| `blink.gif` | a GIF with two frames: it plays, so it is footage, not a picture |
| `carol.svg` | an SVG with a script and an onload handler, which cleaning takes out |
