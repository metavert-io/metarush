---
recipe: 1
name: release-video
title: A release tag becomes a release video
makes: one widescreen video, 45–90 s: the release's title, two code slides, a diagram and an end card, narrated in an AI voice
length: 45–90 s
lands: a new project each time · named "{release}: release video"
---

> One widescreen video, 45–90 s: the release's title, two code slides, a diagram and an end card, narrated in an AI voice; each run makes a new project.

## Inputs
- release: words · default "Widget 2.4" · up to 80 characters · label "The release, as the title slide shows it"
  > goes to the video's title and the title slide's title
- headline: words · default "Saves that never wait, and retries that back off" · up to 140 characters · label "What the release is about, in a line"
  > goes to the title slide's subtitle
- code: pictures · items res_be7e3f7ac0d5933db6db623717, res_c72c77a6cd34f0e6d42f9c3b4f · required · count 2 · label "The two code slides, made from the repository at the tag"
  > 2 pictures · goes to the picture “code-1” and the picture “code-2”
- change-1: words · default "Saves take the next free worker" · up to 80 characters · label "The first change's title"
  > goes to the code slide's title
- where-1: words · default "github.com/acme/widget@1a2b3c4d:queue/worker.go:18-26" · up to 140 characters · label "Where the first change is: repository@commit:file:lines"
  > goes to the code slide's caption
- change-2: words · default "Retries back off on their own" · up to 80 characters · label "The second change's title"
  > goes to the code slide's title
- where-2: words · default "github.com/acme/widget@1a2b3c4d:queue/retry.go:9-15" · up to 140 characters · label "Where the second change is: repository@commit:file:lines"
  > goes to the code slide's caption
- diagram: pictures · items res_673ae6541c1982ba551adee4cd · required · count 1 · label "The diagram of how the pieces fit"
  > 1 picture · goes to the picture “diagram”
- diagram-title: words · default "How a save travels" · up to 80 characters · label "The diagram's title"
  > goes to the architecture slide's title
- line-intro: words · default "Widget 2.4 is out. Here is what changed, and why your saves will feel faster from today, in under a minute." · up to 400 characters · label "The narration over the title"
  > goes to a narration line
- line-1: words · default "Saves no longer wait for the whole queue. Each job now takes the next free worker the moment it is ready, so a busy hour stays quick." · up to 400 characters · label "The narration over the first code slide"
  > goes to a narration line
- line-2: words · default "Retries back off on their own. When a service is slow, Widget waits a little longer each time instead of sending a flood of requests." · up to 400 characters · label "The narration over the second code slide"
  > goes to a narration line
- line-how: words · default "Here is how the pieces fit together: the queue hands each job to a worker, and every result lands in one store you can trust." · up to 400 characters · label "The narration over the diagram"
  > goes to a narration line
- line-end: words · default "That is Widget 2.4. The release notes have everything else, with thanks to everyone who tried the early builds." · up to 400 characters · label "The narration over the end card"
  > goes to a narration line
- voice: a voice · label "The AI voice (default: the narration voice in Content Settings)"
  > goes to the narration's voice
- cta: words · default "Read the release notes" · up to 80 characters · label "The end card's headline"
  > goes to the end card's headline
- link: words · default "example.com/releases" · up to 40 characters · label "Where to read them"
  > goes to the end card's handle

## Sections
1. intro: fixed · clips intro · purpose "the release's title"
2. change-1: fixed · clips change-1 · purpose "the first change, as code"
3. change-2: fixed · clips change-2 · purpose "the second change, as code"
4. how: fixed · clips how · purpose "how the pieces fit"
5. end: fixed · clips end · purpose "where to read more"

## Bindings
- release → title
  > the video's title
- release → param:title.title
  > the title slide's title
- headline → param:title.subtitle
  > the title slide's subtitle
- code → resource:code-1
  > the picture “code-1”
- code → resource:code-2
  > the picture “code-2”
- change-1 → param:code-1.title
  > the code slide's title
- where-1 → param:code-1.caption
  > the code slide's caption
- change-2 → param:code-2.title
  > the code slide's title
- where-2 → param:code-2.caption
  > the code slide's caption
- diagram → resource:diagram
  > the picture “diagram”
- diagram-title → param:arch.title
  > the architecture slide's title
- line-intro → line:l-intro
  > a narration line
- line-1 → line:l-1
  > a narration line
- line-2 → line:l-2
  > a narration line
- line-how → line:l-how
  > a narration line
- line-end → line:l-end
  > a narration line
- voice → voice
  > the narration's voice
- cta → param:end-card.headline
  > the end card's headline
- link → param:end-card.handle
  > the end card's handle

## Rules
- Under 90 seconds · measured max-length:90

## Sign-offs
- The code on the slides is the tag's: each slide says its repository, commit, file and lines

## Hints
- Fetch the repository at the release's tag, then make the two code slides from the changes that matter most and a diagram of how they fit; each keeps its repository, commit, file and lines.
- Write one narration line per slide, said the way a person would explain it, short enough to fit its slide.
- The AI voice is priced first, and a short sample line comes first; a person approves it before the rest.

## The cut

---
cut: 1
title: Widget 2.4
output:
  shape: landscape   # 1920 × 1080
  frame rate: 30
  platform: youtube
footage:
  footage-1: ast_a82f5057e37527ce232bd3d838
resources:
  code-1:  res_be7e3f7ac0d5933db6db623717 sha256:31e80b912019f88d8e87c8986bef72d87c1d469855fd070aca8677794005bc87
  code-2:  res_c72c77a6cd34f0e6d42f9c3b4f sha256:abfdea38cae5e7b8d2ab8bffbf9c564a05c7efc5f41d1b954ac2c3e07460609f
  diagram: res_673ae6541c1982ba551adee4cd sha256:372234596f08c598d918cc38474f1e6847c76232e06bbc3c2ec9ed730aa65011
---

## Clips

1. intro: footage-1 0:00 → 0:07 · muted

2. change-1: footage-1 0:00 → 0:11 · muted

3. change-2: footage-1 0:00 → 0:11 · muted

4. how: footage-1 0:00 → 0:10 · muted

5. end: footage-1 0:00 → 0:08 · muted

## Voiceover

- l-intro: "Widget 2.4 is out. Here is what changed, and why your saves will feel faster from today, in under a minute." · 0.6 s after intro starts · status draft

- l-1: "Saves no longer wait for the whole queue. Each job now takes the next free worker the moment it is ready, so a busy hour stays quick." · 0.6 s after change-1 starts · status draft

- l-2: "Retries back off on their own. When a service is slow, Widget waits a little longer each time instead of sending a flood of requests." · 0.6 s after change-2 starts · status draft

- l-how: "Here is how the pieces fit together: the queue hands each job to a worker, and every result lands in one store you can trust." · 0.6 s after how starts · status draft

- l-end: "That is Widget 2.4. The release notes have everything else, with thanks to everyone who tried the early builds." · 0.6 s after end starts · status draft

## Graphics

- title: slide.title from intro to intro · as title card · with kicker "What's new", subtitle "Saves that never wait, and retries that back off", title "Widget 2.4"
  > over intro

- code-1: slide.code from change-1 to change-1 · with caption "github.com/acme/widget@1a2b3c4d:queue/worker.go:18-26", code "code-1", title "Saves take the next free worker"
  > over change-1

- code-2: slide.code from change-2 to change-2 · with caption "github.com/acme/widget@1a2b3c4d:queue/retry.go:9-15", code "code-2", title "Retries back off on their own"
  > over change-2

- arch: slide.architecture from how to how · with diagram "diagram", title "How a save travels"
  > over how

- end-card: endcard.cta at 0:00 for 0 s · as end screen · with cta "Read more", handle "example.com/releases", headline "Read the release notes" · {span: {from: "end", to: "end"}}
  > over end
