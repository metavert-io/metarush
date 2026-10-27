# status.sh — a script that `set -u` stops fails, on macOS's bash 3.2 too.
#
# macOS's /bin/bash 3.2 runs a script's EXIT trap with $? = 0, and then exits 0, when `set -eu`
# stops the script on an unset variable: a script that broke half-way looks as if it passed (a
# browser run that never got to its checks passed scripts/check.sh). No ERR trap fires either. So
# a script with an EXIT trap sources this right after `set -euo pipefail` and ends with `exit 0`;
# every exit then goes through `exit`, and an EXIT trap reached without one means the shell
# stopped by itself: exit_status says 1.
#
#   . "$(dirname "${BASH_SOURCE[0]}")/lib/status.sh"
#   trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT
#   …
#   exit 0
#
# The browser runs get it from scripts/lib/e2e.sh, whose exit handler does this for them.

_status_exited=""

# exit [status]: the builtin, noting that the script chose to end here.
exit() {
  local rc=$?
  _status_exited=1
  builtin exit "${1:-$rc}"
}

# exit_status <$? in an EXIT trap>: that status, or 1 when the script ended without `exit`
# (stopped by an unset variable, or a script missing its final `exit 0`).
exit_status() {
  if [ "${1:-0}" = 0 ] && [ -z "$_status_exited" ]; then
    echo 1
  else
    echo "${1:-0}"
  fi
}
