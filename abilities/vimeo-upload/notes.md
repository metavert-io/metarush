# Post to Vimeo — not yet runnable

What it will do: post an approved version to your Vimeo account as you — its final render, with the title and
description from its delivery notes, private unless you say otherwise — and bring back a small document
with the video's address, for the version's record. A person starts every run (`changes-outside: yes`): posting
is public and can't be taken back quietly, so Claude, Auto and AI apps never do (the same rule as for YouTube posts).

**Why it doesn't run yet**: `metarush ability check` refuses its manifest, because two things it needs
aren't in MetaRush yet:

1. **A version's final render as an input** (`kind: delivered`): today an ability takes clips, pictures and sound
   from the Library, not a render. Content Credentials needs the same.
2. **The version's delivery notes as an input** (`kind: delivery-notes`): its title, description and chapters, as
   words the run reads; today the person would type them again.

What it needs from you, once it runs:

- **A Vimeo personal access token** with the scopes `upload`, `edit` and `video_files` (Vimeo's developer site →
  your app → Generate an access token). It is a key like any other (`metarush ability setup vimeo-upload --key
  vimeo`): kept by MetaRush, put into requests to Vimeo's hosts only, never seen by the program. Vimeo's
  sign-in for other people's accounts (OAuth) isn't needed for your own.
- **Its upload:** Vimeo's resumable upload (tus) to the address Vimeo gives, under `*.cloud.vimeo.com`.
- **Cost:** free (Vimeo's plans limit how much you upload a week). **What leaves this computer:** the video and its
  words, to Vimeo. **Who starts a run:** a person, every time.
