#!/usr/bin/env bash
# Phase 1a end-to-end smoke test.
#
# Exercises the full single-user workflow against a running BE:
#   register → create accounts → create category-tagged transactions →
#   transfer → adjust-balance → tags on transaction → global summary.
#
# Asserts at each step that responses look right (HTTP 200/201) and
# that totals in the global summary respect the include_in_report
# filter (transfers / opening balance / adjustments excluded).
#
# Usage:
#   bash scripts/smoke_phase1a.sh
# Optional env:
#   BASE      — API base URL (default http://localhost:8080/api/v1)
#   USERNAME  — username to register (default smoke<random>)
#
# Exits 0 on full pass, non-zero with a failing line on any error.

set -euo pipefail

BASE="${BASE:-http://localhost:8080/api/v1}"
USERNAME="${USERNAME:-smoke$RANDOM}"
PASSWORD="Password123!"

json() { python -c "import sys,json;print(json.load(sys.stdin)$1)"; }
say()  { printf '\n--- %s ---\n' "$*"; }
ok()   { printf '   ok: %s\n' "$*"; }

say "register $USERNAME"
TOKEN=$(curl -sf -X POST "$BASE/auth/register" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\",\"display_name\":\"Smoke\"}" \
  | json "['token']['access_token']")
H="Authorization: Bearer $TOKEN"
ok "token len=${#TOKEN}"

say "create two accounts"
A1=$(curl -sf -X POST "$BASE/accounts" -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"Cash","type":"cash","balance":1000,"currency":"THB"}' | json "['id']")
A2=$(curl -sf -X POST "$BASE/accounts" -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"Bank","type":"bank","balance":5000,"currency":"THB"}' | json "['id']")
ok "A1=$A1  A2=$A2"

say "create a tag"
TAG=$(curl -sf -X POST "$BASE/tags" -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"work","color":"#64B5F6","icon":"work_outline"}' | json "['id']")
ok "tag=$TAG"

say "log expense + attach tag"
ECAT=$(curl -sf -H "$H" "$BASE/categories?type=expense" \
  | python -c "import sys,json;d=json.load(sys.stdin);print([c for c in d['data'] if not c['is_system']][0]['id'])")
TX=$(curl -sf -X POST "$BASE/transactions" -H "$H" -H "Content-Type: application/json" \
  -d "{\"type\":\"expense\",\"account_id\":\"$A1\",\"amount\":250,\"category_id\":\"$ECAT\",\"date\":\"2026-04-29\",\"note\":\"Coffee\"}" \
  | json "['id']")
TAG_COUNT=$(curl -sf -X POST "$BASE/transactions/$TX/tags" -H "$H" -H "Content-Type: application/json" \
  -d "{\"tag_ids\":[\"$TAG\"]}" | json "[\"data\"]" | python -c "import sys,ast;print(len(ast.literal_eval(sys.stdin.read())))")
[ "$TAG_COUNT" -eq 1 ] || { echo "FAIL: tag attach returned $TAG_COUNT tags"; exit 1; }
ok "tx=$TX with 1 tag"

say "verify GET /transactions/:id surfaces the tag"
GOT=$(curl -sf -H "$H" "$BASE/transactions/$TX" | python -c "
import sys,json
d=json.load(sys.stdin)
print(len(d.get('tags',[])))")
[ "$GOT" -eq 1 ] || { echo "FAIL: detail tags=$GOT (expected 1)"; exit 1; }
ok "detail tags=1"

say "income + transfer"
ICAT=$(curl -sf -H "$H" "$BASE/categories?type=income" \
  | python -c "import sys,json;d=json.load(sys.stdin);print([c for c in d['data'] if not c['is_system']][0]['id'])")
curl -sf -X POST "$BASE/transactions" -H "$H" -H "Content-Type: application/json" \
  -d "{\"type\":\"income\",\"account_id\":\"$A2\",\"amount\":7000,\"category_id\":\"$ICAT\",\"date\":\"2026-04-29\"}" >/dev/null
curl -sf -X POST "$BASE/transactions" -H "$H" -H "Content-Type: application/json" \
  -d "{\"type\":\"transfer\",\"account_id\":\"$A2\",\"transfer_to_account_id\":\"$A1\",\"amount\":1000,\"date\":\"2026-04-29\"}" >/dev/null
ok "income + transfer logged"

say "adjust A1 balance to 5000"
ADJ_TX=$(curl -sf -X POST "$BASE/accounts/$A1/adjust-balance" -H "$H" -H "Content-Type: application/json" \
  -d '{"new_balance":5000}' | json "['adjustment_transaction_id']")
ok "adjustment_transaction_id=$ADJ_TX"

say "global /transactions/summary excludes transfers + opening + adjust"
SUMMARY=$(curl -sf -H "$H" "$BASE/transactions/summary?from=2026-01-01&to=2026-12-31")
INCOME=$(echo "$SUMMARY" | json "['total_income']")
EXPENSE=$(echo "$SUMMARY" | json "['total_expense']")
NET=$(echo "$SUMMARY" | json "['net']")
COUNT=$(echo "$SUMMARY" | json "['transaction_count']")
echo "   income=$INCOME expense=$EXPENSE net=$NET count=$COUNT"
echo "   expected: income=7000 expense=250 net=6750 count=2"
[ "$INCOME" = "7000" ] || { echo "FAIL: income=$INCOME"; exit 1; }
[ "$EXPENSE" = "250" ]  || { echo "FAIL: expense=$EXPENSE"; exit 1; }
[ "$COUNT"  = "2" ]    || { echo "FAIL: count=$COUNT"; exit 1; }
ok "global summary respects include_in_report"

say "GET /transactions list returns tags array on rows"
LIST=$(curl -sf -H "$H" "$BASE/transactions?per_page=10")
TAGGED=$(echo "$LIST" | python -c "
import sys,json
d=json.load(sys.stdin)
n=sum(1 for r in d['data'] if r.get('tags'))
print(n)")
[ "$TAGGED" -ge 1 ] || { echo "FAIL: no tagged rows in list response"; exit 1; }
ok "$TAGGED tagged rows in list"

say "edit transfer (cascade) — find OUT row, change amount 1000 → 1500"
OUT_TX=$(echo "$LIST" | python -c "
import sys,json
d=json.load(sys.stdin)
out=next(r for r in d['data'] if r['type']=='transfer' and r['account_id']=='$A2')
print(out['id'])")
EDITED=$(curl -sf -X PUT "$BASE/transactions/$OUT_TX" -H "$H" -H "Content-Type: application/json" \
  -d '{"amount":1500}')
ROWS=$(echo "$EDITED" | json "['rows']" | python -c "import sys,ast;print(len(ast.literal_eval(sys.stdin.read())))")
[ "$ROWS" -eq 2 ] || { echo "FAIL: edit transfer returned $ROWS rows (expected 2 — envelope shape)"; exit 1; }
ok "PUT transfer returned envelope with 2 rows"

echo
echo "PASS — Phase 1a smoke complete (user=$USERNAME)"
