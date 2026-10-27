# The publish check

Before anything of MetaRush is published, two scripts say whether it may go. They never change, move or
publish anything.

- **`scripts/check-public.sh`** is the open-source-prep standard's generic check, committed as it is published there
  (never edited here: a change goes to the standard, then is copied in again).
- **`scripts/publish-check.sh`** runs it, then what only MetaRush checks. Use this one.

```bash
scripts/publish-check.sh                 # this repository: its tracked files and the history of HEAD
scripts/publish-check.sh -a              # the history of every local branch and tag too
scripts/publish-check.sh -r ../public    # another repository (the prepared public one)
scripts/publish-check.sh <denylist>…     # these deny-lists instead of the usual ones
```

Each finding is a line — where (`path:line`, `path (path)` or `commit <sha>`), `[what]`, and the line — and each
part ends with its count. Exit status 1 when either part found anything, 2 on a usage error.

## What the generic check looks at (`check-public.sh`)

Every **tracked** file's path and contents, every commit message reachable from HEAD (with `-a`, from every local
branch and tag), and every author and committer email in those commits:

- **deny-listed terms**, anywhere in a line, ignoring case;
- **email addresses** other than placeholders (`example.com`/`.org`/`.net`, `.test`, `.invalid`, `.localhost`, a
  one-letter domain, GitHub's noreply addresses, automated senders such as `noreply@…`) and the allow list;
- **phone numbers** (except 555-01xx), **home folders** (`/Users/<name>`, `/home/<name>`, except you, me, user,
  username) and **Fly.io machine ids**;
- **authors and committers** other than placeholders and the allow list;
- **secrets in the history**, with gitleaks when it's installed (otherwise it says it skipped them).

Lockfiles, `go.sum`, certificates and minified or map files are always skipped.

## What MetaRush adds (`publish-check.sh`)

Over the same repository's tracked files (leaving out what `scripts/check-public.skip` names):

| Kind | What | Why |
|---|---|---|
| `work-note` | `pm/`, anything in `local/`, `CLAUDE.local.md`, a deny-list file, and every file whose name `local/publish-worknotes.txt` lists (the decision log, plans, research, handovers: this computer's to name) | they stay on disk, out of the repository, indexed from a git-ignored `CLAUDE.local.md` |
| `work-note` | a `.gitignore` line naming one of those notes | its name would be public: list it in `.git/info/exclude` instead |
| `tailnet-host` | `<host>.<tailnet>.ts.net`, except example ones (`example.ts.net`, `*.example.ts.net`), names with `tailnet` in them and the placeholders `tail0000`, `tail1234`, `tailXXXX` | a real tailnet's name |
| `secret` | MetaRush's own secret patterns (`scripts/secret-scan.sh`, with `scripts/secret-scan.allow` and its `secret-scan: allow` marker), never shown | remove it, and rotate it if it was ever pushed |

## The files it reads

| File | What | Committed? |
|---|---|---|
| `local/publish-denylist.txt` | this repository's own terms: the organisations and people it must never name, machines, the tailnet | **never** (`local/` is git-ignored); `local.example/publish-denylist.txt` shows the shape with invented entries |
| `local/publish-worknotes.txt` | the file names of this repository's work notes, one per line | **never**; `local.example/publish-worknotes.txt` shows the shape with invented names |
| `~/.config/open-source-prep/denylist.txt` | terms for every repository you publish | never (this computer's) |
| `scripts/check-public.allow` | email addresses that may appear: the public commit email (jon@metavert.io), and made-up addresses in tests that aren't people (a password in a database URI, a service account of an invented project, GitHub's SSH user) | yes |
| `~/.config/open-source-prep/allow.txt` | more allowed addresses, for every repository | never |
| `scripts/check-public.skip` | paths to leave out, as globs: files that must keep text the generic check misreads (generated numbers, Go's pseudo-versions, a list of clip lengths, Homebrew's folder on Linux), each with its reason | yes |

Deny-list entries take `term | allow: glob,glob` to exempt files; `OPEN_SOURCE_PREP_DIR` moves the config folder. In a
git worktree, `local/` is the worktree's own: link the main checkout's file into it. Fix a false positive with an
invented value first, then with the allow or skip file — never by changing either script.

## Tests and where it runs

`go test ./internal/secretpath -run TestPublishCheckScript` runs `publish-check.sh` on invented repositories: a clean
one passes both parts; one with a tracked note, a note in `local/`, `.gitignore` lines naming notes, a real-looking
tailnet host, a planted key and a deny-listed term reports each (and not the placeholders, the allowed key or the
ignored `/local/` line); a skipped path is left out; a folder that isn't a repository is a usage error. Every name,
host and key in it is made up, and the ones it must catch are put together at run time.

It is a step of `scripts/check.sh --release` (after the full run, before the release dry run): a release goes out only
when the tree and its history pass. Its false positives in the tree (generated numbers, made-up addresses in tests)
have narrow entries in `scripts/check-public.skip` and `scripts/check-public.allow`, each with its reason. A history
that still holds old author emails or messages fails until it is replaced by a fresh one; a repository prepared for
publishing must pass `scripts/publish-check.sh -a -r <it>` before its first push.
