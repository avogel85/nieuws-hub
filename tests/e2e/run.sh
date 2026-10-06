#!/usr/bin/env bash
# Browser tests (Playwright + axe) against real dashboard servers.
#
#   tests/e2e/run.sh                 all suites
#   tests/e2e/run.sh v119 swipe      only these suites
#
# Needs Go, Node 22 and the Playwright Chromium (npm ci && npx playwright install chromium in
# tests/e2e). The servers fetch the real feeds and APIs, so internet access is required; a suite
# that fails is retried once, because a live source can hiccup.
#
# Servers (127.0.0.1): 8090 default config · 8091 mock KNMI warnings · 8092 hostile feed ·
# 8093 push on · 8094 accent colour + three sports · 8099 test fixtures.
# Screenshots go to $E2E_OUT (default /tmp/ndb-e2e).
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
WORK=${E2E_WORK:-$(mktemp -d)}
export E2E_OUT=${E2E_OUT:-/tmp/ndb-e2e}
mkdir -p "$E2E_OUT" "$WORK/logs"

ALL=(v115 v114 swipe nlbadge disc v110 v19 v18 v17 v16 i18n breaches alarms check features threats rain race
  hardening recent14 newpanels weather v116 v118 v119 outages117 trains117 v121 v123 v126 v127 v129 v131)
if [ $# -gt 0 ]; then SUITES=("$@"); else SUITES=("${ALL[@]}"); fi

pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

BIN=${NDB_BIN:-}
if [ -z "$BIN" ]; then
  echo "building the dashboard…"
  (cd "$ROOT" && CGO_ENABLED=0 go build -buildvcs=false -o "$WORK/ndb" .) # no git stamp: the checkout may belong to another user
  BIN="$WORK/ndb"
fi

node "$HERE/make-configs.mjs" "$WORK/cfg" 8099
VAPID=$("$BIN" -gen-vapid | sed -n 's/^NDB_VAPID_PRIVATE_KEY=//p') # a throwaway key for this run only

node "$HERE/serve-fixtures.mjs" 8099 > "$WORK/logs/fixtures.log" 2>&1 & pids+=($!)
start() { # name port config [extra env]
  env NDB_LISTEN="127.0.0.1:$2" NDB_SNAPSHOT_PATH= NDB_LOG_LEVEL=warn ${4:-} "$BIN" -config "$WORK/cfg/$3" > "$WORK/logs/$1.log" 2>&1 & pids+=($!)
}
start main 8090 main.yaml
start mock 8091 mock.yaml
start evil 8092 evil.yaml
start push 8093 push.yaml "NDB_VAPID_PRIVATE_KEY=$VAPID"
start accent 8094 accent.yaml "NDB_VAPID_PRIVATE_KEY=$VAPID"

# Wait until every server answers and has news (the first fetch of all feeds takes a while).
echo "waiting for the servers to fetch their feeds…"
for port in 8090 8091 8092 8093 8094; do
  for i in $(seq 1 90); do
    n=$(node -e 'fetch(process.argv[1]).then(r => r.json()).then(j => console.log(j.items.length), () => console.log(0))' "http://127.0.0.1:$port/api/news?limit=100")
    [ "${n:-0}" -ge 20 ] && break
    sleep 2
  done
  echo "  :$port ready (${n:-0} items)"
done
# The satellite image: EUMETSAT can take a minute to render a new one.
for i in $(seq 1 60); do
  node -e 'fetch(process.argv[1]).then(r => r.json()).then(j => process.exit(j.image ? 0 : 1), () => process.exit(1))' http://127.0.0.1:8090/api/satellite && break
  sleep 3
done
sleep 20 # let the panels' first background fetches finish too

base_for() {
  case "$1" in
    v110) echo http://127.0.0.1:8093/ ;;
    v114) echo http://127.0.0.1:8094/ ;;
    weather) echo http://127.0.0.1:8091/ ;;
    *) echo http://127.0.0.1:8090/ ;;
  esac
}

failed=(); total_pass=0
for s in "${SUITES[@]}"; do
  log="$WORK/logs/suite-$s.log"
  for attempt in 1 2; do
    if (cd "$HERE/suites" && BASE=$(base_for "$s") timeout 900 node "$s.mjs") > "$log" 2>&1; then ok=1; else ok=0; fi
    [ $ok = 1 ] && break
    [ $attempt = 1 ] && { echo "  $s failed, retrying once…"; cp "$log" "$WORK/logs/suite-$s.try1.log"; }
  done
  p=$(grep -c '^PASS' "$log" || true); f=$(grep -c '^FAIL' "$log" || true)
  total_pass=$((total_pass + p))
  if [ $ok = 1 ]; then printf '  %-11s %3s passed\n' "$s" "$p"
  else printf '  %-11s %3s passed, FAILED\n' "$s" "$p"; failed+=("$s"); grep -m 8 '^FAIL\|Error' "$log" | sed 's/^/      /' | cut -c1-220; fi
done

echo "$total_pass checks passed in ${#SUITES[@]} suites; logs in $WORK/logs, screenshots in $E2E_OUT"
if [ ${#failed[@]} -gt 0 ]; then echo "FAILED: ${failed[*]}"; exit 1; fi
echo "ALL PASSED"
