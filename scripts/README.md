# Backend test scripts

End-to-end shell scripts that exercise the running BE container via
its real HTTP surface. They're intentionally **not** Go unit tests —
the tradeoff is "no test framework setup, runs against the real
Docker stack". Pragmatic for Phase 1a sanity checks; consider
upgrading to `go test ./...` integration tests once the API surface
stabilises.

Both scripts assume:

- `docker compose up -d` is running (BE listening on `localhost:8080`).
- Migrations have been applied (currently up to `000010`).
- `python` is available on PATH (used for JSON parsing — more reliable
  than relying on `jq` on Windows Git Bash).

Run them from any directory; paths are absolute.

---

## `smoke_phase1a.sh` — Phase 1a end-to-end smoke

Walks the full single-user flow against the API:

1. Register a fresh user
2. Create two accounts (Cash + Bank)
3. Create a tag
4. Log an expense + attach the tag → verify list/detail responses
   carry the tag
5. Log income + a transfer → verify the transfer envelope shape
6. Adjust-balance → verify the synthetic Adjustment transaction is
   created and the response carries `adjustment_transaction_id`
7. Hit `GET /v1/transactions/summary` → assert the global summary
   respects `include_in_report` (transfers / opening balance /
   adjustment all excluded; income/expense/count match expectations)
8. Edit a transfer's amount → assert the PUT returns the
   `{transfer_group_id, rows[2]}` envelope (cascade)

Runs in 5–10 seconds against the local stack.

```bash
bash scripts/smoke_phase1a.sh
# Optional override:
USERNAME=alice bash scripts/smoke_phase1a.sh
BASE=http://localhost:8080/api/v1 bash scripts/smoke_phase1a.sh
```

Exit codes: `0` on full pass; non-zero with a `FAIL: ...` line on the
first assertion failure.

---

## `test_balance_invariant.sh` — concurrent balance-cache stress

Phase 1a exit criterion (per
[`product/phase1a/overview.md`](../product/phase1a/overview.md#phase-1a-bedb-exit-criteria)):
under concurrent transfer writes, `accounts.balance` must equal
`SUM(signed transaction amounts)` for every account. This script
fires the workload and asserts the invariant via SQL.

Defaults: 50 parallel `curl` workers × 20 transfers = 1000 random
transfers across 4 accounts. Tunable:

```bash
bash scripts/test_balance_invariant.sh
# Smaller smoke:
WORKERS=10 TRANSFERS_PER_WORKER=5 bash scripts/test_balance_invariant.sh
# Bigger soak:
WORKERS=100 TRANSFERS_PER_WORKER=50 bash scripts/test_balance_invariant.sh
```

The script registers a fresh user each run and creates 4 fresh accounts,
so it's safe to run repeatedly without polluting other test data.

After the workload completes it shells into the DB container to compute
the expected sum vs. cached balance, and reports `PASS` or prints the
violating rows.

**When to run:** after touching anything in the transactions write path
(`service.Create`, `service.Update`, `service.Delete`, the balance
helper, the lock-ordering rule). Also good to run on bare main once
before declaring 1a done.

---

## Both scripts produce no persistent state to clean up

They register short-lived test users; nothing else is touched.
DB rows accumulate but don't conflict with real users (random
usernames). Wipe with `docker compose down -v` if the test users
clutter your local DB.
