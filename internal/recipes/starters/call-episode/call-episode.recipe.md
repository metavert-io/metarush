---
recipe: 1
name: call-episode
title: A call becomes an episode with shorts
makes: one widescreen episode as long as the call needs
length: 60–10800 s
lands: a new project each time · named "{episode}"
---

> One widescreen episode as long as the call needs; each run makes a new project.
> Each run also makes up to 3 vertical shorts of 20–60 s from the footage “call”, each a new cut of the run's project.

## Inputs
- call: footage · items ast_320fe6bb1e9b0d9f0839105bc4 · required · count 1 · label "The call recording"
  > 1 clip · goes to the moments Claude chooses for “episode”
- episode: words · default "Episode 1: an example call" · required · up to 80 characters · label "The episode's title"
  > goes to the video's title and the title card's title

## Sections
1. episode: chosen · from call · count 1–12 · length 60–10800 s · purpose "the whole conversation in its order, cleaned up: false starts, crosstalk, long silences and stretches off the subject taken out"
   > Claude chooses 1–12 moments, 1–180 min in all from the call recording

## Pack
- shorts: shorts · count 1–3 · length 20–60 s · from footage call · shape vertical · purpose "a moment that stands on its own without the rest of the call: a clear claim, a question and its answer, a laugh" · named "{title}, short {k}"
  > Up to 3 vertical shorts of 20–60 s from the footage “call”, each a new cut of the run's project

## Bindings
- episode → title
  > the video's title
- episode → param:intro.title
  > the title card's title

## Sign-offs
- The delivery notes are written: a title, a description, chapters from 0:00 and show notes
- Names and spellings in the captions are checked

## Hints
- Keep the whole conversation in its order. Take out false starts, crosstalk, long silences and stretches off the subject through the cut's sheet, then the ums and the long pauses.
- Write the episode's delivery notes from its transcript: a title, a description, chapters starting at 0:00 and show notes.
- Keep or change the shorts' first picks: each must stand on its own without the rest of the call.
- Voices are made clearer in the cloud; a sample of one stretch comes first, and a person approves it before the rest.

## The cut

---
cut: 1
title: "Episode 1: an example call"
output:
  shape: landscape   # 1920 × 1080
  frame rate: 30
  platform: youtube
captions:
  on: true
  style: clean
  position: lower
  words per line: 6
  timing: transcript
mix:
  voices:
    "recording ast_320fe6bb1e9b0d9f0839105bc4": cloud
---

## Graphics

- intro: title.card at 0:00 for 4 s · as title card · with title "Episode 1: an example call"
  > starts after the video ends
