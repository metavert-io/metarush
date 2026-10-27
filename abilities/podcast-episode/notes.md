# Post an episode to your podcast host — not yet runnable

What it will do: post a version's sound as a podcast episode on your podcast host — Buzzsprout first, whose API
takes a personal key; other hosts with a key take the same shape — with the title, show notes and chapters from
the version's delivery notes, as a draft there unless you say publish now, and bring back a small document
with the episode's address. ffmpeg on this computer turns the render's sound into the episode's MP3. A person starts
every run (`changes-outside: yes`); Claude, Auto and AI apps never post, as with YouTube.

**Why it doesn't run yet**: `metarush ability check` refuses its manifest, because two things it needs
aren't in MetaRush yet:

1. **A version's final render as an input** (`kind: delivered`), as Content Credentials needs: the program
   takes its sound.
2. **The version's delivery notes as an input** (`kind: delivery-notes`): the episode's title, show notes and
   chapters (delivery notes already write chapters as "0:00 Intro" lines, which hosts read).

- **Its key:** Buzzsprout's API token (Buzzsprout → Settings → API Access), kept by MetaRush and put into requests
  to `www.buzzsprout.com` in the `Authorization: Token token=…` header the program writes with its stand-in.
- **Cost:** free (your host's plan limits how much you upload). **What leaves this computer:** the episode's sound
  and words, to the host. **Who starts a run:** a person, every time.
