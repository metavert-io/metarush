#!/bin/sh
# A stand-in for the metarush program, for MetaRush.app's tests (the Swift tests and
# internal/macapp's update test). It writes each command line to calls.log beside it,
# runs <command>.sh beside it if there is one (a test's hook: change what the next
# answer says), prints <command>.json and ends with <command>.status (default 0).
# The answers are the Go-written fixtures (internal/cli TestMacAppFixtures). Never the
# real program: nothing here touches a home, a service or the network.
D="$(cd "$(dirname "$0")" && pwd)"
printf '%s\n' "$*" >>"$D/calls.log"
args=" $* "
case "$args" in
*" setup questions "*) k=setup-questions ;;
*" setup "*) k=setup ;;
*" service status "*) k=service-status ;;
*" service restart "*) k=service-restart ;;
*" service install "*) k=service-install ;;
*" skills install "*"--dry-run"*) k=skills-dry ;;
*" skills install "*) k=skills-install ;;
*" renderer install "*) k=renderer-install ;;
*" migrate "*) k=migrate ;;
*" version "*) k=version ;;
*" doctor "*) k=doctor ;;
*" dashboard "*) k=dashboard ;;
*" stop "*) k=stop ;;
*) k=unknown ;;
esac
if [ -f "$D/$k.sh" ]; then . "$D/$k.sh"; fi
if [ -f "$D/$k.json" ]; then
  cat "$D/$k.json"
else
  printf '{"ok": false, "error": {"code": "E_FAKE", "message": "The stand-in has no answer for %s."}}\n' "$k"
fi
s=0
if [ -f "$D/$k.status" ]; then s="$(cat "$D/$k.status")"; fi
exit "$s"
