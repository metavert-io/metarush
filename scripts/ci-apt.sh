#!/usr/bin/env bash
# scripts/ci-apt.sh — Ubuntu packages in CI, from GitHub's cache when an earlier run kept them. The
# runners' Ubuntu mirror is sometimes very slow (FFmpeg's 94 MB once took 18 minutes at 88 kB/s, past
# half of a browser run's 45); GitHub's cache is quick. apt still chooses every version and checks
# every file against its index: a kept file is used only when it is the very file apt wants.
#
#   scripts/ci-apt.sh install <package…>   # apt-get install, with the files the cache put in ~/apt-debs
#   scripts/ci-apt.sh keep                 # the installed versions' files into ~/apt-debs; key=… for actions/cache/save
set -euo pipefail
KEEP="${METARUSH_CI_APT_DEBS:-$HOME/apt-debs}"
ARCHIVES=/var/cache/apt/archives

case "${1:-}" in
  install)
    shift
    [ "$#" -gt 0 ] || { echo "usage: $0 install <package…>" >&2; exit 2; }
    n=0
    if [ -d "$KEEP" ]; then
      n="$(find "$KEEP" -maxdepth 1 -name '*.deb' | wc -l | tr -d ' ')"
      if [ "$n" -gt 0 ]; then sudo cp "$KEEP"/*.deb "$ARCHIVES"/; fi
    fi
    echo "$n package file(s) from GitHub's cache"
    sudo apt-get update
    sudo apt-get install -y -o APT::Keep-Downloaded-Packages=true "$@"
    ;;
  keep)
    rm -rf "$KEEP"
    mkdir -p "$KEEP"
    for f in "$ARCHIVES"/*.deb; do
      [ -e "$f" ] || continue
      p="$(dpkg-deb -f "$f" Package)"
      v="$(dpkg-deb -f "$f" Version)"
      if [ "$(dpkg-query -W -f='${Version}' "$p" 2>/dev/null || true)" = "$v" ]; then cp "$f" "$KEEP"/; fi
    done
    sum="$(cd "$KEEP" && find . -maxdepth 1 -name '*.deb' | sort | sha256sum | cut -c1-16)"
    key="apt-debs-${ImageOS:-linux}-$(uname -m)-$sum"
    echo "$(find "$KEEP" -maxdepth 1 -name '*.deb' | wc -l | tr -d ' ') package file(s) kept ($(du -sh "$KEEP" | cut -f1)) as $key"
    echo "key=$key" >>"${GITHUB_OUTPUT:-/dev/null}"
    ;;
  *)
    echo "usage: $0 install <package…> | keep" >&2
    exit 2
    ;;
esac
