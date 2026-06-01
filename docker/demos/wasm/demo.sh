#!/bin/sh
set -e

CLI="./bin/wasm-cli"
URL="http://127.0.0.1:35997"
SALT="0100000000000000000000000000000000000000000000000000000000000000"
INDEX=1

pass=0
fail=0

assert() {
  local desc="$1"
  local got="$2"
  local want="$3"
  if [ "$got" = "$want" ]; then
    echo "  PASS: $desc"
    pass=$((pass + 1))
  else
    echo "  FAIL: $desc (got '$got', want '$want')"
    fail=$((fail + 1))
  fi
}

section() {
  echo ""
  echo "=== $1 ==="
}

rpc() {
  local method="$1"
  shift
  curl -s -X POST "$URL" \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$method\",\"params\":[$*]}"
}

wait_height() {
  local target="$1"
  while true; do
    H=$(rpc "ledger.getFrontierMomentum" | grep -o '"height":[0-9]*' | head -1 | cut -d: -f2)
    H=${H:-0}
    if [ "$H" -ge "$target" ] 2>/dev/null; then
      return
    fi
    sleep 2
  done
}

# --- Pre-flight ---

if [ ! -f "$CLI" ]; then
  echo "Building binaries..."
  make build
fi

# --- Wait for wasm-runtime enforcement (height >= 15) ---

section "Waiting for wasm-runtime spork"
HEIGHT=$(rpc "ledger.getFrontierMomentum" | grep -o '"height":[0-9]*' | head -1 | cut -d: -f2)
HEIGHT=${HEIGHT:-0}
echo "Chain height: $HEIGHT"
if [ "$HEIGHT" -lt 15 ] 2>/dev/null; then
  echo "Need >= 15 for wasm-runtime. Waiting..."
  wait_height 15
fi
echo "wasm-runtime active"

# --- Deploy ---

section "Deploying contract"
if [ ! -f "contract/demo.wasm" ]; then
  echo "No contract/demo.wasm found. Run 'make contract' first."
  exit 1
fi

DEPLOY_OUTPUT=$($CLI --index $INDEX --url "$URL" deploy --salt "$SALT" --upgradeable contract/demo.wasm 2>&1)
echo "$DEPLOY_OUTPUT"
CONTRACT=$(echo "$DEPLOY_OUTPUT" | grep "^Contract:" | awk '{print $2}')
if [ -z "$CONTRACT" ]; then
  echo "ERROR: could not extract contract address"
  exit 1
fi
echo "Contract address: $CONTRACT"

HEIGHT=$(rpc "ledger.getFrontierMomentum" | grep -o '"height":[0-9]*' | head -1 | cut -d: -f2)
wait_height $((HEIGHT + 4))

# --- Verify deploy ---

section "Verifying deployment"
INFO=$(rpc "embedded.wasm.getContract" "\"$CONTRACT\"")
DEPLOYED=$(echo "$INFO" | grep -o '"deployed":true')
assert "contract deployed" "$DEPLOYED" '"deployed":true'

# --- Fund the contract with QSR for state-storage deposits ---
# A deploy only locks the bytecode deposit; the contract has zero *spendable*
# QSR. state_write locks a per-key storage deposit out of spendable QSR, so
# without this funding step every write fails with "insufficient deposit" and
# no state ever persists. A plain (data-less) QSR transfer credits the
# contract's balance. 1 QSR covers thousands of small writes.

section "Funding contract with QSR (for state-storage deposits)"
$CLI --index $INDEX --url "$URL" fund "$CONTRACT" --qsr 100000000
HEIGHT=$(rpc "ledger.getFrontierMomentum" | grep -o '"height":[0-9]*' | head -1 | cut -d: -f2)
wait_height $((HEIGHT + 4))

# --- Execute twice ---

section "Executing contract (x2)"
echo "Execute 1..."
$CLI --index $INDEX --url "$URL" execute "$CONTRACT"
HEIGHT=$(rpc "ledger.getFrontierMomentum" | grep -o '"height":[0-9]*' | head -1 | cut -d: -f2)
wait_height $((HEIGHT + 4))

echo "Execute 2..."
$CLI --index $INDEX --url "$URL" execute "$CONTRACT"
HEIGHT=$(rpc "ledger.getFrontierMomentum" | grep -o '"height":[0-9]*' | head -1 | cut -d: -f2)
wait_height $((HEIGHT + 4))

# --- View ---

section "Reading state via view"
VIEW_OUTPUT=$($CLI --index $INDEX --url "$URL" view "$CONTRACT" --function view 2>&1)
echo "$VIEW_OUTPUT"
VIEW_VAL=$(echo "$VIEW_OUTPUT" | grep "Decoded u64:" | awk '{print $3}')
assert "view returns 2 after two executes" "$VIEW_VAL" "2"

# --- Info ---

section "Contract info"
$CLI --index $INDEX --url "$URL" info "$CONTRACT"

# --- Summary ---

echo ""
echo "=== Summary ==="
echo "PASS: $pass"
echo "FAIL: $fail"
echo ""
if [ "$fail" -eq 0 ]; then
  echo "WASM-CONTRACT devnet demo: PASS"
  exit 0
else
  echo "WASM-CONTRACT devnet demo: FAIL"
  exit 1
fi
