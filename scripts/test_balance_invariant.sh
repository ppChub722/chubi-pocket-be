#!/usr/bin/env bash
# Balance-cache invariant test (Phase 1a exit criterion).
#
# Spec §3.6 (accounts) + §2.4 (transactions): `accounts.balance` is a
# CACHE of the sum of that account's transactions, atomically updated
# inside each write tx. The invariant under concurrent load is:
#
#     accounts.balance == SUM(signed transaction amounts) per account
#
# This test fires N parallel workers, each generating M transfers in
# random directions across a small pool of accounts, then verifies the
# invariant via SQL. Failure → ship-blocker for 1a.
#
# Defaults: 50 workers × 20 transfers = 1000 writes spread across 4
# accounts. Override with WORKERS / TRANSFERS_PER_WORKER env vars.

set -euo pipefail

BASE="${BASE:-http://localhost:8080/api/v1}"
WORKERS="${WORKERS:-50}"
TRANSFERS_PER_WORKER="${TRANSFERS_PER_WORKER:-20}"
USERNAME="${USERNAME:-balanceinv$RANDOM}"
PASSWORD="Password123!"

echo "=== balance-cache invariant test ==="
echo "user=$USERNAME workers=$WORKERS transfers/worker=$TRANSFERS_PER_WORKER"

# Helper: extract a JSON field with python (more reliable than `jq`
# being installed on Windows Git Bash).
json() { python -c "import sys,json;print(json.load(sys.stdin)$1)"; }

TOKEN=$(curl -sf -X POST "$BASE/auth/register" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\",\"display_name\":\"BI\"}" \
  | json "['token']['access_token']")
H="Authorization: Bearer $TOKEN"
echo "registered, token len=${#TOKEN}"

# Create 4 accounts with non-trivial opening balances. Random opening
# values keep the invariant check honest (Opening Balance transactions
# must be in the sum too).
ACCT_IDS=()
for i in 1 2 3 4; do
  bal=$((1000 * i))
  id=$(curl -sf -X POST "$BASE/accounts" -H "$H" -H "Content-Type: application/json" \
    -d "{\"name\":\"A$i\",\"type\":\"cash\",\"balance\":$bal,\"currency\":\"THB\"}" \
    | json "['id']")
  ACCT_IDS+=("$id")
done
echo "created ${#ACCT_IDS[@]} accounts"

# Worker function: spawned in background N times. Each picks a random
# (src, dst) pair, posts a transfer, sleeps a tiny jitter to spread
# scheduler pressure. Failures abort the worker (set -e) but other
# workers keep going — final invariant check reports the truth.
worker() {
  local wid="$1"
  for ((j=0; j<TRANSFERS_PER_WORKER; j++)); do
    local src_idx=$((RANDOM % ${#ACCT_IDS[@]}))
    local dst_idx
    while true; do
      dst_idx=$((RANDOM % ${#ACCT_IDS[@]}))
      [ "$dst_idx" != "$src_idx" ] && break
    done
    local src="${ACCT_IDS[$src_idx]}"
    local dst="${ACCT_IDS[$dst_idx]}"
    local amt=$((1 + RANDOM % 50))
    curl -sf -X POST "$BASE/transactions" -H "$H" \
      -H "Content-Type: application/json" \
      -d "{\"type\":\"transfer\",\"account_id\":\"$src\",\"transfer_to_account_id\":\"$dst\",\"amount\":$amt,\"date\":\"2026-04-29\"}" \
      > /dev/null || echo "worker $wid request $j failed" >&2
  done
}

echo "firing $WORKERS workers..."
START=$(date +%s)
for ((w=0; w<WORKERS; w++)); do
  worker "$w" &
done
wait
END=$(date +%s)
echo "all workers done in $((END - START))s"

# Verify the invariant via SQL. For each account: cached balance must
# equal SUM(signed amount). Signed amount per spec §2.1:
#   income     → +amount
#   expense    → -amount
#   transfer + Transfer IN cat   → +amount
#   transfer + Transfer OUT cat  → -amount
#
# In SQL: classify by t.type and the account's role in the pair via
# the system category id. Easiest is the per-row `signed_amount`
# expression below.

echo "running invariant query..."
USER_ID_HEX=$(docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -tAc \
  "SELECT id FROM users WHERE username = '$USERNAME'")
echo "user_id=$USER_ID_HEX"

# Find this user's Transfer IN / Transfer OUT system category ids.
TRANSFER_IN=$(docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -tAc \
  "SELECT id FROM categories WHERE user_id='$USER_ID_HEX' AND system_kind='TRANSFER_IN'")
TRANSFER_OUT=$(docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -tAc \
  "SELECT id FROM categories WHERE user_id='$USER_ID_HEX' AND system_kind='TRANSFER_OUT'")

VIOLATIONS=$(docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -tAc \
"WITH expected AS (
   SELECT a.id, a.balance AS cached, COALESCE(SUM(
     CASE
       WHEN t.type = 'income'   THEN  t.amount
       WHEN t.type = 'expense'  THEN -t.amount
       WHEN t.type = 'transfer' AND t.category_id = '$TRANSFER_IN'  THEN  t.amount
       WHEN t.type = 'transfer' AND t.category_id = '$TRANSFER_OUT' THEN -t.amount
       ELSE 0
     END
   ), 0) AS computed
   FROM accounts a
   LEFT JOIN transactions t ON t.account_id = a.id
   WHERE a.user_id = '$USER_ID_HEX'
   GROUP BY a.id, a.balance
 )
 SELECT id, cached, computed FROM expected
 WHERE ABS(cached - computed) > 0.001;")

if [ -z "$VIOLATIONS" ]; then
  echo "PASS — invariant holds for all ${#ACCT_IDS[@]} accounts after $((WORKERS * TRANSFERS_PER_WORKER)) writes"
  exit 0
fi

echo "FAIL — invariant broken:"
echo "$VIOLATIONS"
exit 1
