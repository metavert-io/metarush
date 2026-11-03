# Accounts and sign-in

MetaRush is self-hosted on a computer its owner controls. Out of the box it is **just me on this computer**: no
sign-in, nobody else gets in. When the owner wants others in, they choose a sign-in method in **Settings → People** (the Users part),
giving their own sign-in at the same step, and then invite people. Tailscale (or any HTTPS proxy) is how other
computers *reach* MetaRush; it is not required, and its identity is only used when "Tailscale accounts" is the chosen
sign-in method (or kept beside the organization's account).

## Sign-in methods

The method is stored on the workspace (`workspace.auth.method`), so every computer of the workspace agrees.

| Method | Who gets in | How |
|---|---|---|
| `single` (default, "Just me on this computer") | The owner, on this computer | `metarush dashboard` opens a one-time sign-in link, as before. Other computers see "Sign-in isn't set up here". |
| `password` ("Email and password") | Everyone with an account | Email and password on the sign-in page, on this computer or another (another needs HTTPS). |
| `tailscale` ("Tailscale accounts") | People whose Tailscale login is linked to their account | Signed in by Tailscale's identity on the public address. |
| `sso` ("Your organization's account") | People of the workspace, by the account they have at Google, Microsoft or another OpenID Connect provider | "Sign in with Google" on the sign-in page; [below](#signing-in-with-your-organizations-account). Passwords and Tailscale accounts can stay beside it. |

In every method the owner can sign in at the host with `metarush dashboard` (a person at the host's terminal can read
the installation's files anyway): that is the recovery path. One-time links work only on this computer's own address;
they no longer sign another computer in as the owner.

Choosing a method is a guided step in Settings → People (or `metarush auth method`): for email and password the owner
gives their email and a password in the same step; for Tailscale, their Tailscale login; for the organization's
account, the test sign-in they just did on this computer. Inviting people is off until a method is chosen. Going back
to "Just me" signs everyone else out and keeps their records.

## Accounts

Accounts are the workspace's `users` (`usr_…`): `name`, `email` (and `emailKey`, lower-cased and trimmed, unique per
workspace), `role` (`owner` | `member`), `state` (`active` | `invited` | `disabled` | `earlier`), `sessionsValidAfter`,
the Tailscale `logins` linked to them, and `sso`: the accounts at the organization's provider that are them (a stable
key per account — Google's account ID, Microsoft's directory and person IDs, another provider's issuer and subject —
unique in the workspace, with the email it vouched for; never a token). Roles are minimal: owners manage people and the sign-in method and change
this computer's settings (below); members do everything else, approving included.

## What only owners change

Everyone signed in can **see** every setting; only an owner **changes** this computer's settings and the workspace's
limits. The server enforces it (`ops.Flags.OwnerOnly`, or `ops.RequireOwner` inside an op where only one path is the
owners'); the dashboard disables the controls and says "Only an owner can change this. Owner: …".

| Area | Owner-only | Stays open to everyone |
|---|---|---|
| People and sign-in | `auth.method.set`, `auth.sso.set\|secret\|test`, `users.invite\|resetLink\|revoke\|disable\|enable\|remove\|role` | your own account (`account.*`), reading how people sign in (`auth.status`, `auth.sso.status`) |
| Remote access | `remote.enable\|disable\|allow\|signout` | `remote.status` |
| Storage and the Trash | `storage.migrate`, `/api/storage/choose\|plan\|move`, `trash.configure`, `trash.empty`, `worker.configure` (battery) | moving clips to the Trash and back, `storage.status`, `trash.list` |
| Keys and the team's limits | `provider.set\|remove`; `generate.configure` for the model, quality and daily limit | the narration voice (Content Settings: `provider.settings` with or without `--project`), a project's budget (`provider.settings --project`, `generate.configure --project`), looks |
| Claude Code | `assistant.settings.set` (model, effort, limits, the switch for steps that run by themselves) | each person's Ask first / Auto (`assistant.mode.set`), conversations |
| Footage coming in | `watch.set\|disable\|mkdir`, `/api/watch/choose`, `ingest.url.configure\|update`; `footage.describe.configure` for the workspace | importing, `watch.import`, a project's scene descriptions |
| Browser extension | removing someone else's browser, or everyone's (`extension.revoke` with another person's browser or `everyone`) | pairing your own browser (`extension.pair`), removing yours (`extension.revoke`), `extension.status` (yours; an owner sees everyone's), downloading and installing it |
| Projects | clearing archived projects out for good (`project.purge`) | deleting a project (`project.delete`: it moves to Archived projects), restoring one (`project.restore`) |
| Log and improvements | `log.configure\|purge\|hooks.enable\|hooks.disable`, `improve.schedule.set` | reading the log, improvement ideas |
| Machines | `machine.invite\|revoke\|rekey`; handing this computer's role to another computer and taking over from one that is gone (`machine.handover.plan\|export\|receive\|abandon\|clean`, `machine.takeover`, people only) | `machine.list`, `machine.handover.status` (people) |
| Abilities | approving a version or turning it down, taking an approval back (`ability.approve\|revoke`, people only), which also agrees to the one service on this computer it may reach; an ability's keys and the files it reads (`ability.setup` with keys or files); agreeing to the program on its local service's port on another computer (`ability.setup`) | checking one (`ability.check`), setting one up on a computer (programs), planning and running approved ones |
| This computer's folders and programs | `watch.browse` (listing the host's folders), `resources.link\|folder` and `/api/resources/folders/choose` (linking a host folder), `sound.setup` (installing studio quality), `ai.local.setup` (downloading an AI model that runs on this computer), `components.add\|remove` (adding or removing a program or model on this computer), `ingest.url` with `browserSignIn` (lending this computer's browser sign-in) | adding files by dropping them, importing links without the browser's sign-in |
| Automatic work and deleting for good | `render.follow.set` (review copies that follow the latest version), `asset.delete` (like emptying the Trash) | asking for a review copy, moving clips to the Trash |
| Review links for people outside the workspace | turning them off, letting only owners send them, the longest a link lasts (`share.settings.set`, people only) | sending a version for review (`share.create`, people only, unless owners keep it to themselves), sending a link again, taking one back, reading what came back |
| Exporting the workspace | making an archive of the whole workspace (`workspace.export`, people only) and seeing the exports made on this computer (`workspace.exports`); restoring one is a step at a terminal or in setup, never in the dashboard; saying a restored copy is now the workspace, which lifts its pause on deleting by itself (`workspace.adopt`, people only) | — |
| Estimates of what outside AI services cost | asking everyone for estimates (`account.preferences.set` `requireEstimates`) | your own estimates on or off (`account.preferences.set` `estimates`) |

**Browser extension pairings are a person's.** Anyone signed in pairs their own browser (Settings → Browser
extension, or `metarush extension pair` in the host's terminal, which pairs for the owner); what that browser sends is
credited to them like anything else they add. A pairing works only while its person's account is on: turned off,
removed or not yet joined, or any account but the owner's with "Just me" sign-in, and every `/ext` request is refused
at once (it works again when the account is back). Pairings from before belong to the person the installation was set
up for.

**Who counts as an owner.** A signed-in person whose account is an owner, on this computer's own address or from
another computer alike — a member signed in with a password at the host is refused too. With "Just me" sign-in the
person is the owner, and a one-time link's session (`metarush dashboard`) is the owner at the host. This computer's own
command line, MCP server and daemon count as the owner (a person at the host's terminal can read the installation's
files anyway). Every signed-in request carries its person (`ops.OverHTTP`), so an endpoint that calls an op itself, or a
Claude Code card a member approves, is held to the same rule. A refusal says "Only an owner of this MetaRush can change
that." with the owners' names in `details.owners`; the session (`/api/session` `owner`, `owners`) and `auth.status`
(`owners`) say who they are.

**Earlier people.** People who did things before accounts existed (a Tailscale sign-in, or set up MetaRush on a
computer) keep their records as `earlier`: their ID stays on everything they did. Inviting their email (or choosing
them in the invite) makes the account *that same record*, so their history is theirs again with nothing rewritten.

## Passwords (NIST SP 800-63B)

- **Stored** with argon2id (64 MiB, 3 passes, 2 lanes, 16-byte random salt, 32-byte key) as a PHC string
  (`$argon2id$v=19$m=65536,t=3,p=2$salt$hash`), in `user_credentials` (never in `users`, never in an answer, a log or
  the audit). A hash with older settings is replaced at the next successful sign-in.
- **Rules**: 12 to 128 characters after Unicode NFKC normalization, any characters (spaces and emoji too); no
  composition rules and no expiry. Refused: one of the ~30,000 most common passwords of that length (embedded,
  from SecLists' top million, MIT licence — no network), fewer than 5 different characters, or one containing the
  person's email name or name.
- **Sign-in** answers "That email and password don't match" for a wrong email and a wrong password alike, and takes
  the same time either way (an unknown email verifies against a dummy hash).
- **Throttling** (`auth_failures`, shared by every computer): after 5 wrong passwords for an account each try waits
  longer (2^n seconds, up to 15 minutes); after 10 the account locks for 15 minutes; the owner is notified and the
  audit records it. A success clears it.

## Sessions

The installation's session file (`<home>/state/sessions.json`) as before, with the method that made each session
(`link` | `password` | `tailscale` | `sso`, with the provider an `sso` session came through) and when it was last used.
An `sso` session is like a password session (30 days at most, 7 unused) and ends when people stop signing in with that
provider. A sign-in always makes a new session (no fixation);
password sessions last at most 30 days and end after 7 days unused; changing or resetting a password, disabling or
removing a person, or "Sign out everywhere" sets `sessionsValidAfter`, which every computer checks. Cookies stay
`HttpOnly`, `SameSite=Strict`, and `Secure` with the `__Host-` prefix over HTTPS. Changing one's email or password
or the sign-in method asks for the current password again.

## Invites and password resets

One-time links (`auth_tokens`): 32 random bytes, stored only as their SHA-256; an invite lasts 7 days, a reset 1 hour;
both can be revoked and work once. Nothing is emailed: the owner copies the link (shown once) and sends it however
they like. Links use the public address when remote access is on. An invite asks for a display name and a password
and signs the person in; a reset asks for a new password, signs them in and signs them out everywhere else. Owners
make reset links from Settings → People or `metarush users reset-password <email>`.

## Surfaces

- Endpoints outside the ops registry (they work without a session): `GET /api/auth/state`, `POST /api/auth/login`,
  `POST /api/auth/logout`, `GET|POST /api/auth/link/{token}` (an invite or a reset), `POST /api/auth/sso/start`,
  `GET /auth/sso/begin` and `GET /auth/sso/callback` (the organization's account). Same Host, CSRF-header and
  cross-site checks as every endpoint.
- `GET /api/session` says whether the signed-in person is an owner (`owner`) and names the owners (`owners`).
- Ops: `auth.status`, `auth.method.set` (owner, people only), `auth.sso.status`, `auth.sso.set|secret|test` (owners,
  people only, on this computer), `users.list`, `users.invite`, `users.revoke`,
  `users.resetLink`, `users.disable`, `users.enable`, `users.remove`, `users.role`, `account.password`,
  `account.update`, `account.signoutEverywhere`, `account.avatar`, `account.preferences` and
  `account.preferences.set` (people only).
- CLI: `metarush auth status|method|reset`, `metarush auth sso status|set|secret|test`,
  `metarush users list|invite|revoke|reset-password|disable|enable|remove|role`,
  `metarush account password|update|signout-everywhere|avatar|preferences`.
- `metarush auth reset` (a person at the host's terminal): the method back to "Just me", the Tailscale login list
  cleared (logins stay on the people), every browser signed out on every computer, people without a sign-in (no
  password and no organization account) marked `earlier`. The organization sign-in's setup stays, waiting; going back
  to it takes a new test sign-in. Nobody's work changes.

## Signing in with your organization's account

People sign in with the account they already have: **Google** (Google Workspace or a personal Google account),
**Microsoft** (Entra ID: work or school accounts) or **any provider that supports OpenID Connect** (Okta, Keycloak,
Authentik… — MetaRush reads its settings from its discovery document). MetaRush is a confidential web app at the
provider: the authorization code flow with PKCE (S256), a state and a nonce, the code traded for the ID token on this
computer with the app's client secret.

**Setting it up** (an owner, in Settings → People → Sign-in with your organization's account, or `metarush auth sso
set`, on the computer that runs MetaRush):

1. Register an app at the provider. **Google**: Google Cloud → APIs & Services → Credentials → an OAuth client of type
   *Web application*. **Microsoft**: Microsoft Entra → App registrations → a new registration with a *Web* redirect
   address and a client secret (Certificates & secrets); note its Application (client) ID and Directory (tenant) ID.
   **Another provider**: a confidential (web) app with the authorization code flow.
2. Give it the **addresses MetaRush shows** as its redirect addresses: `http://localhost:<port>/auth/sso/callback` for
   this computer's own browser (Google and Microsoft both take an http address on localhost; Microsoft's portal doesn't
   take 127.0.0.1 over http), and with remote access on, `https://<computer>.<tailnet>.ts.net/auth/sso/callback` for
   other computers. Each computer that runs MetaRush for the workspace has its own addresses and registers them too.
3. In MetaRush, give the provider (and for Microsoft the directory ID; for another provider its address, the
   *issuer*), the app's client ID and its **client secret**. The secret is this computer's own, kept like a key (the
   keychain, or `<home>/.env` as `METARUSH_SSO_CLIENT_API_KEY` in env mode), never in the database, `config.yaml`, a
   log or an answer; the form sends it once and never shows it again, and the terminal reads it hidden or from a pipe
   (`--secret-stdin`), never from a flag. MetaRush reads the provider's settings right away and keeps nothing it can't
   reach.
4. **Test it**: Test sign-in (Settings) or `metarush auth sso test` opens the provider in a browser on this computer;
   sign in with your own account. The result says who signed in and that the answer passed every check.
5. **Switch** (Switch to Google, or `metarush auth method sso`): only after a test that passed on this computer, by the
   owner switching, within 30 minutes. The account you tested with is linked to you, so you keep getting in.

`metarush auth method sso --provider google --client-id … [--domain example.com]` does all of it in one go: it asks for
the secret, saves the setup, opens the test sign-in and switches only when it worked. A new provider or app always
waits for its own test; changes to who may join and to the other sign-ins of the setup in use apply at once.

**Who may sign in.** Never anyone just because they have an account at the provider:

- the person a provider account is **linked** to (it stays theirs when its email changes);
- else, by an **email the provider answers for**, a person of the workspace or an invite — linked at that first
  sign-in. Google answers for Gmail addresses and for Google Workspace accounts (`hd`); for an outside address it once
  checked, the email may have changed hands since, so it doesn't count. Microsoft's email counts only when the app asks
  for the optional claims `email` and `xms_edov` and Microsoft says the domain's owner is verified (its documentation:
  never authorize by email alone). Another provider's counts when it says `email_verified`;
- else, with an **allowed domain** the owner set, a new member: for Google a Google Workspace domain (the answer's
  `hd`, never just the email's ending), for Microsoft a directory (its ID: the answer's `tid`), for another provider a
  checked email's domain (exactly, not its subdomains). The owners are told when someone joins this way.

An **invite link** joins only when the provider gives the invited email (and doesn't say it is unchecked), and links
that account.
A different account showing a linked person's email is refused (another directory's look-alike can't take an account
over); an owner removes the person and invites them again to link another. Turned-off and removed people are refused.
Owners stay owners: roles and the owner checks don't change.

**Passwords and Tailscale beside it.** An owner can keep **email and password** working (people who have one still use
it; invite links can choose one; reset links work) and **Tailscale accounts** (the listed accounts keep signing in by
themselves on the public address; an account not on the list signs in with the organization's account). Kept, they are
another way in, as strong as a password or the tailnet's admin; turned off, their sessions end at once.

**What is checked**, every time: the answer comes back to the browser that started the sign-in (its state in an
HttpOnly, SameSite=Lax cookie, `__Host-` over HTTPS), once, within ten minutes, on the address it began at; the code is
traded with the PKCE verifier; the ID token's signature against the provider's published keys (kept for their
`max-age`, read again for a key MetaRush hasn't seen — at most every 30 seconds — so key changes are picked up; RS, PS,
ES and EdDSA; never `none` or a shared secret), `iss` exactly (Google's two spellings, Microsoft's `{tenantid}` filled
in with the token's `tid`), `aud` exactly this app, `exp`, `iat` and `nbf` within a minute of this computer's clock, the
nonce, a subject. The return address is a path on this site only. Nothing from the provider is kept but who signed in
(the account's key and the email it vouched for); the code, the tokens, the state, the nonce and the verifier are
hidden from the activity log and never written down.

**If the provider stops working** (an outage, an expired client secret, a wrong change at the provider): `metarush
dashboard` at the computer that runs MetaRush still opens it as the owner with a one-time link, whatever the method;
there, Settings → People fixes the setup, or `metarush auth reset` goes back to just you on that computer (everyone's
accounts and work stay). Sessions already made keep working until they end.

## Preferences

Settings → Preferences (`/settings/preferences`, after People) holds each person's own choices:

- **Estimate costs of outside AI services before edits** (on by default; `users.preferences.estimatesOff`).
  On: generated shots, the AI voice and clearer voices in the cloud show their estimate — "about $0.40", opening to
  the service, its model, the clip, script line or picture, the units and each part's price — and wait for the
  person's OK. Off: paid steps run within the person's budgets, daily limits and project spending limits without the
  estimate step (a generated shot needs no agreed price, the dashboard and a terminal don't ask, and under Auto
  Claude's changes that cost money run by themselves). Consent, approvals, looks and their test shots, setup and
  people-only steps don't change. **An owner can ask everyone for estimates** ("Ask everyone for estimates",
  `workspaces.estimates.required`): it wins over each person's off, says who asked and since when, and a person's own
  choice counts again when they stop. Everything that spends money asks one hook, `prefs.EstimatesFirst(ctx, a,
  userID)`; when the database doesn't answer within 3 s it reads as on.
- **Ask first or Auto**: the same choice as the switch by the message box (`assistant.mode.set`).
- **Light or dark**: kept in each browser (moved here from General; `/settings/general#appearance`,
  `/settings#preferences` and `/preferences` redirect).

`metarush account preferences` shows them (`--json`); `--estimates on|off` changes yours and `--require-estimates
on|off` everyone's (owners), both at a terminal (people only).

## Pictures

Each person can add a picture in Settings → People (Your account) or with `metarush account avatar <file>` (`--remove`
takes it away). PNG, JPEG or WebP up to 5 MB and at least 32 pixels; MetaRush crops the middle square, scales it to
256 pixels and stores it as a PNG in the media store by its SHA-256 (`users.avatar`). It is served at
`/media/avatars/{user}` only to signed-in browsers, and shown in the account menu, the people list and conversations.
Nothing is fetched from an outside service.
