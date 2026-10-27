# Post a video on LinkedIn — not yet runnable

What it will do: post an approved version on LinkedIn as you — its final render with the words from its delivery
notes, to your connections unless you say public — and bring back a small document with the post's address.
A person starts every run (`changes-outside: yes`); Claude, Auto and AI apps never post, as with YouTube.

**Why it doesn't run yet**: `metarush ability check` refuses its manifest, because three things it needs
aren't in MetaRush yet:

1. **A person's own sign-in** (`signin:`): LinkedIn has no personal keys; posting as a member needs that member's
   OAuth sign-in (scope `w_member_social`). Abilities take keys only. The shape it needs is the one
   YouTube posting uses: the owner registers MetaRush with their own LinkedIn app once per computer; each person signs in in
   their browser on this computer; MetaRush keeps the lasting permission in the keychain under that person and puts
   a short-lived token into the run's requests to LinkedIn's hosts, never into the program.
2. **A version's final render as an input** (`kind: delivered`), as Content Credentials needs.
3. **The version's delivery notes as an input** (`kind: delivery-notes`).

- **LinkedIn's side:** an app in LinkedIn's developer portal with the "Share on LinkedIn" product (and "Sign In with
  LinkedIn using OpenID Connect" for the member's name). Videos go up in parts to the addresses LinkedIn gives, then
  the post is made with LinkedIn's posts API.
- **Cost:** free. **What leaves this computer:** the video and its words, to LinkedIn. **Who starts a run:** a
  person, every time.
