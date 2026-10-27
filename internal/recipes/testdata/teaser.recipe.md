---
recipe: 1
name: app-teaser
base: 3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9a   # version 2, current
title: App teaser
makes: one widescreen video, 7–11 s
length: 7–11 s
lands: new cuts in prj_01jabcdefghjkmnpqrstvwxyz0   # “App teaser”
settings: prj_01jabcdefghjkmnpqrstvwxyz0   # “App teaser”
from: project prj_01jabcdefghjkmnpqrstvwxyz0 · cut main · version 4 · revision rev_01jabcdefghjkmnpqrstvwxyz0 · approved   # “App teaser”
---

> One widescreen video, 7–11 s; each run makes a new cut in “App teaser”.
> Each run also makes up to 3 vertical shorts of 15–60 s from the conversation the video plays, each its own project.

## Inputs
- footage: footage · items ast_01jabcdefghjkmnpqrstvwxyz0, ast_01jabcdefghjkmnpqrstvwxyz3 · count at least 1 · label "The footage"
  > 2 clips: “interview.mp4” and “interview-2.mp4” · goes to the moments Claude chooses for “moments”
- headline: words · default "Read free" · up to 40 characters · label "The headline on the end card"
  > goes to the end card's headline
- tone: a choice · default "warm" · required · choices "warm", "bright · loud"
- hero: pictures · items res_01jabcdefghjkmnpqrstvwxyz0 · count 1 · from folder fld_01jabcdefghjkmnpqrstvwxyz0 · label "The pictures from “Character art”"
  > 1 picture: “hero” · a run takes the newest in the linked folder “Character art” · goes to the picture “hero”
- voice: a voice · default "Ada" · items voice-ada-0001 · label "The AI voice"
  > goes to the narration's voice

## Sections
1. brand: fixed · clips brand · purpose "Brand intro"
2. moments: chosen · from footage · count 1–3 · length 4–8 s · clips hook, answer · purpose "the hook; the answer"
   > Claude chooses 1–3 moments, 4–8 s in all from the footage

## Pack
- shorts: shorts · count 1–3 · length 15–60 s · shape vertical · captions on · purpose "a question and its answer" · named "{title} — short {k}"
  > Up to 3 vertical shorts of 15–60 s from the conversation the video plays, each its own project

## Bindings
- headline → param:end.headline
  > the end card's headline
- hero → resource:hero
  > the picture “hero”
- voice → voice
  > the narration's voice

## Rules
- Under 35 seconds · measured max-length:35
- "Captions at the top · always" · check

## Sign-offs
- Lena OKs the final version

## Hints
- Name the app in the first two seconds.
- "- a hint that looks like a list"

## The cut

---
cut: 1
title: App teaser
output:
  shape: landscape   # 1920 × 1080
  frame rate: 30
  platform: generic
captions:
  on: true
  style: clean
  position: top
  words per line: 6
  timing: transcript
footage:
  intro:     ast_01jabcdefghjkmnpqrstvwxyz1 sha256:0d1e0d1e0d1e0d1e   # intro.mov · 0:05
  interview: ast_01jabcdefghjkmnpqrstvwxyz0 sha256:9f2c9f2c9f2c9f2c   # interview.mp4 · 0:27.3
  bed:       ast_01jabcdefghjkmnpqrstvwxyz2 sha256:1111111111111111   # bed.m4a · 0:30
resources:
  hero: res_01jabcdefghjkmnpqrstvwxyz0 sha256:abababababababababababababababababababababababababababababababab
---

## Clips

1. brand: intro 0:00 → 0:03 · label "Brand intro"

2. hook: interview 0:01 → 0:04 · label "the hook"

3. answer: interview 0:06 → 0:09 · label "the answer"

## Voiceover

- line-1: "Meet the app." · 0.2 s after hook starts · voice elevenlabs "Ada" id voice-ada-0001

- line-2: "Read free tonight." · after the previous line

## Graphics

- title: title.card from hook to hook · with title "Stories for tonight" · {duration: 2}
  > over hook

- end: endcard.cta from answer to answer · with headline "Read free", cta "Start now", image "hero" · {duration: 3}
  > over answer

## Sound

- bed: bed 0:00 → 0:09 · music · starts at 0:00 · volume -18 dB
