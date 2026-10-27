#!/usr/bin/env bash
# Dev start (idempotent): local MongoDB → build → daemon on :4841 → Vite on :4840.
# The dev installation lives in .dev/home (separate from any installed copy).
# Usage: ./start.sh [--no-vite]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
BACK=4841; FRONT=4840
kill_port() { local p; p=$(lsof -ti tcp:"$1" -sTCP:LISTEN 2>/dev/null || true); [ -n "$p" ] && kill $p 2>/dev/null && sleep 0.5 || true; }

scripts/dev-mongo.sh start >/dev/null
DEV_HOME=.dev/home   # this checkout's development installation
if [ ! -f $DEV_HOME/config.yaml ]; then
  echo "No dev installation yet. Create one with:"
  echo "  METARUSH_MONGODB_URI=\"$(scripts/dev-mongo.sh uri)\" ./bin/metarush --home $DEV_HOME init --yes --secrets env --db-name metarush-dev --port $BACK --video-dir \"$ROOT/video\""
  echo "  (and put METARUSH_MONGODB_URI in $DEV_HOME/.env, chmod 600)"
  exit 1
fi
go build -ldflags "$(scripts/version.sh --ldflags)" -o bin/metarush ./cmd/metarush   # 0.2.0-dev.3 and its commit (D130)
# The dev instance renders from this checkout's live video/ package (the developer
# setting); installed copies render with their own version's renderer (D75).
if ! grep -q 'devRenderer: true' $DEV_HOME/config.yaml; then
  ./bin/metarush --home $DEV_HOME renderer dev "$ROOT/video" >/dev/null && echo "dev home: renders now use this checkout's video/ (metarush renderer dev)"
fi
kill_port $BACK
mkdir -p $DEV_HOME/logs
METARUSH_DEV_ORIGINS="localhost:$FRONT,127.0.0.1:$FRONT" nohup ./bin/metarush --home $DEV_HOME serve > $DEV_HOME/logs/daemon.log 2>&1 &
for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$BACK/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$BACK/api/health" >/dev/null || { echo "daemon failed to start — see $DEV_HOME/logs/daemon.log"; exit 1; }
echo "daemon: http://127.0.0.1:$BACK (logs $DEV_HOME/logs/daemon.log)"
if [ "${1:-}" != "--no-vite" ] && [ -f web/vite.config.ts ]; then
  kill_port $FRONT
  nohup npm run dev -w web > $DEV_HOME/logs/vite.log 2>&1 &
  for _ in $(seq 1 60); do curl -sf "http://localhost:$FRONT/" >/dev/null && break; sleep 0.25; done
  echo "dashboard (dev): $(./bin/metarush --home $DEV_HOME dashboard --print-link --dev)"
else
  echo "dashboard: $(./bin/metarush --home $DEV_HOME dashboard --print-link)"
fi
