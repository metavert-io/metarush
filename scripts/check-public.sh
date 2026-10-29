#!/usr/bin/env bash
# Pre-publication check: is anything personal or private about to be published from a git repository?
#
#   check-public.sh [-r REPO] [-a] [DENYLIST ...]
#
# Scans the repository (default: the one containing the current folder):
#   - every TRACKED file's contents and path, for real-looking email addresses (anything not on a placeholder
#     domain), phone numbers, home-directory paths, Fly.io machine ids, and every deny-list term;
#   - every commit message reachable from HEAD (with -a: from every local branch and tag), for the same;
#   - every author and committer email in those commits, which must be a placeholder or on an allow list;
#   - the history for secrets, with gitleaks when it's installed.
#
# Deny-lists hold one case-insensitive term per line; `term | allow: glob,glob` exempts matching files. With no
# DENYLIST arguments, both of these are used when they exist (neither is ever committed):
#   local/publish-denylist.txt                          this repository's own terms (keep `local/` gitignored)
#   ${OPEN_SOURCE_PREP_DIR:-~/.config/open-source-prep}/denylist.txt   terms for every repository you publish
# Allowed email addresses (e.g. your public commit email), one per line, come from scripts/check-public.allow or
# check-public.allow in the repository and from allow.txt in the same config folder.
# Paths to skip (globs, one per line) come from scripts/check-public.skip or check-public.skip. Lockfiles, certificates
# and minified/map files are always skipped (their encoded text matches short terms by accident).
# Exit status 1 when anything is found.
set -uo pipefail
repo=. all_refs=
while [ $# -gt 0 ]; do
  case $1 in
    -r) repo=${2:?-r needs a folder}; shift 2 ;;
    -a) all_refs=1; shift ;;
    *) break ;;
  esac
done
top=$(git -C "$repo" rev-parse --show-toplevel 2>/dev/null) || { echo "not a git repository: $repo" >&2; exit 2; }
cd "$top" || exit 2
conf=${OPEN_SOURCE_PREP_DIR:-$HOME/.config/open-source-prep}
status=0

if command -v gitleaks >/dev/null 2>&1; then
  if ! gitleaks git . --no-banner --redact >/dev/null 2>&1; then
    echo "gitleaks: possible secrets — run: gitleaks git . --no-banner --redact"
    status=1
  fi
else
  echo "(gitleaks not installed — secret scan skipped)"
fi

CONF="$conf" ALL_REFS="$all_refs" python3 - "$@" <<'PY' || status=1
import fnmatch, os, re, subprocess, sys

conf = os.environ["CONF"]
def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout
def lines_of(path):
    if not os.path.isfile(path):
        return []
    return [l.strip() for l in open(path, encoding="utf-8") if l.strip() and not l.lstrip().startswith("#")]

deny_paths = sys.argv[1:] or [p for p in ("local/publish-denylist.txt", os.path.join(conf, "denylist.txt")) if os.path.isfile(p)]
if not deny_paths:
    print(f"(no deny-list found — only the generic checks ran; see the header of this script)")
deny = []
for p in deny_paths:
    for raw in lines_of(p):
        term, _, allow = raw.partition("|")
        globs = [g.strip() for g in allow.replace("allow:", "").split(",") if g.strip()]
        deny.append((term.strip().lower(), globs))
allowed = set()
for p in ("scripts/check-public.allow", "check-public.allow", os.path.join(conf, "allow.txt")):
    allowed |= {l.lower() for l in lines_of(p)}
skip = ["*.lock", "*-lock.json", "*-lock.yaml", "go.sum", "*.crt", "*.cer", "*.min.js", "*.min.css", "*.map"]
for p in ("scripts/check-public.skip", "check-public.skip"):
    skip += lines_of(p)

# Placeholder domains (RFC 2606 names, one-letter domains like x.com) and automated senders are not people.
placeholder = re.compile(r"@(?:[a-z0-9-]+\.)*(example(\.(com|org|net))?|test|invalid|localhost|"
                         r"users\.noreply\.github\.com|anthropic\.com|"
                         r"(?:resource|group)\.calendar\.google\.com)$|@[a-z]\.[a-z]{2,3}$", re.I)
system_sender = re.compile(r"^(no-?reply|notifications?|orders?|order-status|reservations?|"
                           r"support|billing|mailer-daemon|postmaster|reply\+[^@]*)@", re.I)
# Home folders that belong to no one: placeholders in docs, the account GitHub's CI runners use on Linux and
# macOS, Homebrew on Linux's own folder, and the default users of cloud and container images.
not_people = ["you", "me", "user", "username", "yourname", "your-name", "your_name", "someone", "somebody", "example",
              "runner", "linuxbrew", "ubuntu", "vscode", "node"]
rules = [
    ("email", re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b")),
    ("phone", re.compile(r"(?<![\w.])(?:\+?1[ .-]?)?\(?[2-9]\d{2}\)?[ .-][2-9]\d{2}[ .-]\d{4}(?![\w])|\b1[2-9]\d{9}\.\d{11}\b")),
    ("home-path", re.compile(r"/(?:Users|home)/(?!(?:" + "|".join(not_people) + r")(?![a-z0-9_])|<)[a-z][a-z0-9_-]+")),
    ("fly-machine-id", re.compile(r"(?<![0-9a-f])[0-9a-f]{14}(?![0-9a-f])")),
]
def ok_email(e):
    return bool(placeholder.search(e) or system_sender.search(e) or e.lower() in allowed)

def hits_in(text, where):
    hits = []
    for name, rx in rules:
        for m in rx.finditer(text):
            if name == "email" and ok_email(m.group(0)):
                continue
            if name == "phone" and re.search(r"555[ .-]?01\d\d|555[ .-]\d{4}", m.group(0)):
                continue
            # Three numbers among others, separated by spaces only ("330 660 990 1320"), are a list, not a phone number.
            if name == "phone" and re.fullmatch(r"\d{3} \d{3} \d{4}", m.group(0)) and (
                    re.search(r"\d[ \t]+$", text[:m.start()]) or re.match(r"[ \t]+\d", text[m.end():])):
                continue
            # A Fly.io machine id has letters; fourteen digits alone are a number (a timestamp, a long decimal).
            if name == "fly-machine-id" and m.group(0).isdigit():
                continue
            hits.append(f"{name}: {m.group(0)}")
    low = text.lower()
    for term, globs in deny:
        if term in low and not any(fnmatch.fnmatch(where, g) for g in globs):
            hits.append(f"deny-list: {term}")
    return hits

found = 0
def report(where, hits, line):
    global found
    for h in hits:
        found += 1
        print(f"{where}: [{h}] {line.strip()[:110]}")

files = [f for f in git("ls-files").split("\n") if f]
for f in files:
    if any(fnmatch.fnmatch(f, g) or fnmatch.fnmatch(os.path.basename(f), g) for g in skip):
        continue
    report(f"{f} (path)", [h for h in hits_in(f, f) if h.startswith("deny-list")], f)
    if not os.path.isfile(f):
        continue
    try:
        text = open(f, encoding="utf-8").read()
    except UnicodeDecodeError:
        continue
    for n, line in enumerate(text.split("\n"), 1):
        report(f"{f}:{n}", hits_in(line, f), line)

refs = ["--branches", "--tags"] if os.environ.get("ALL_REFS") else ["HEAD"]
for rec in git("log", *refs, "--format=%H%x1f%ae%x1f%ce%x1f%B%x1e").split("\x1e"):
    if not rec.strip():
        continue
    sha, ae, ce, body = (rec.strip("\n").split("\x1f") + ["", "", "", ""])[:4]
    for who, e in (("author", ae), ("committer", ce)):
        if e and not ok_email(e):
            report(f"commit {sha[:7]}", [f"{who} email: {e}"], body.split("\n")[0])
    for line in body.split("\n"):
        report(f"commit {sha[:7]} message", hits_in(line, ""), line)

print(f"\n{found} finding(s) in {len(files)} tracked files and their commit history.")
sys.exit(1 if found else 0)
PY
exit $status
