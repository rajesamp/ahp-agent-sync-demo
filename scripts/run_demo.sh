#!/usr/bin/env bash
#
# run_demo.sh — build and run the full AHP multi-client sync demo.
#
# Starts the host, then a passive viewer subscribed to the root channel,
# then the agent. The agent creates a session + chat and streams a
# scripted turn; the host sequences every action and fans it out; the
# viewer prints each synchronized change as it arrives. All three are
# shut down cleanly at the end.
#
# Usage: scripts/run_demo.sh
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"
BIN="$ROOT/bin"
ADDR="${AHP_HOST_ADDR:-:12345}"
URL="${AHP_HOST_URL:-ws://127.0.0.1:12345}"
AUDIT="${AHP_AUDIT_LOG:-$ROOT/audit.log}"

export PATH="$PATH:/usr/local/go/bin"

echo "==> Building binaries"
mkdir -p "$BIN"
go build -o "$BIN/host"   ./host
go build -o "$BIN/viewer" ./clients/viewer
go build -o "$BIN/agent"  ./clients/agent

HOST_PID="" ; VIEWER_PID=""
cleanup() {
  [ -n "$VIEWER_PID" ] && kill "$VIEWER_PID" 2>/dev/null || true
  [ -n "$HOST_PID" ]   && kill "$HOST_PID"   2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "==> Starting host on ws://${ADDR#:} (audit: $AUDIT)"
AHP_HOST_ADDR="$ADDR" AHP_AUDIT_LOG="$AUDIT" "$BIN/host" &
HOST_PID=$!

# Wait for the host's WebSocket port to accept connections.
echo "==> Waiting for host to be ready"
for _ in $(seq 1 50); do
  if (exec 3<>"/dev/tcp/127.0.0.1/${ADDR#:}") 2>/dev/null; then
    exec 3>&- 3<&- ; break
  fi
  sleep 0.1
done

echo ""
echo "================ BEFORE: viewer is idle, no session exists ================"
AHP_HOST_URL="$URL" "$BIN/viewer" &
VIEWER_PID=$!
sleep 1   # let the viewer subscribe to the root channel

echo ""
echo "================ AGENT STARTS: creates session + streams a turn ==========="
AHP_HOST_URL="$URL" "$BIN/agent"

# Allow the final synchronized frames to reach the viewer.
sleep 1
echo ""
echo "================ AFTER: every agent action above appeared in the viewer ==="
echo "==> Demo complete. Audit trail written to: $AUDIT"
