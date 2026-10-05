# chubi-pocket-be

The Go REST API for ChubiPocket, a personal finance and shared-expense app. It
backs the [Flutter app](https://github.com/ppChub722/chubi-pocket-app) and runs
in production on a VPS.

Around 126 endpoints across 14 feature modules — accounts, transactions,
budgets, saving goals, scheduled transactions, categories, tags, contacts,
personal debts, projects (shared costs), notifications, plus auth and users.

Part of a multi-repo project:

- **chubi-pocket-be** — this repo, the Go REST API
- [chubi-pocket-app](https://github.com/ppChub722/chubi-pocket-app) — Flutter client
- [chubi-pocket-web](https://github.com/ppChub722/chubi-pocket-web) — web client (in progress)
- [chubi-pocket-docs](https://github.com/ppChub722/chubi-pocket-docs) — specs and design docs

## Tech stack

| Component | Choice |
|---|---|
| Language | Go 1.25+ |
| Framework | Gin |
| Database | PostgreSQL 15+ |
| Driver | `pgx/v5` (connection pooling) |
| Auth | JWT (`golang-jwt/v5`), argon2id password hashing |
| Migrations | `golang-migrate` (run as a separate compose service) |
| Logging | `slog` (structured JSON in non-dev) |

## Quick start

```bash
# 1. Bring up Postgres + run migrations + start the API (one command)
docker compose up -d

# 2. Verify
curl http://localhost:8080/health
# → {"status":"ok"}
```

For more commands (psql, migration rollback, fresh-reset, etc.) see [COMMANDS.md](COMMANDS.md).

## API surface

All routes are under `/api/v1` and require a JWT except `register`, `login` and
`/health`.

| Module | What it covers |
|---|---|
| `auth` | Register, login, logout, change password |
| `users` | Profile, deactivate/reactivate, entitlement packs |
| `accounts` | Balances, members, transfers, ownership, adjustments |
| `transactions` | Income/expense/transfer, summary, tagging |
| `categories` | CRUD, reorder, soft-delete/restore |
| `tags` | CRUD, attach to transactions |
| `budgets` | Caps per category, overview, archive/restore |
| `saving_goals` | Targets and allocations |
| `scheduled_transactions` | Recurring entries, upcoming, run history |
| `contacts` | People, link requests, merge/absorb |
| `personal_debts` | Who owes whom, per-person views |
| `projects` | Shared costs, members, project transactions |
| `notifications` | Invites, link requests, reminders, settings |

Every module follows the same shape (see [Project layout](#project-layout)), so
the list grows without the codebase changing shape. Endpoint-level detail lives
in the [specs](../chubi-pocket-docs/design/spec/).

## Project layout

```
cmd/api/main.go           # entrypoint — wires modules + router
internal/
  modules/                # one folder per feature (14 in total)
    auth/  users/  accounts/  transactions/  categories/  tags/
    budgets/  saving_goals/  scheduled_transactions/  contacts/
    personal_debts/  projects/  notifications/  user_pack_permissions/
  platform/
    config/               # env loader
    database/             # pgxpool factory
    logger/               # slog + Gin middleware
    response/             # uniform error/success envelope
migrations/               # golang-migrate SQL files (canonical schema lives in docs)
```

Each feature module has the same shape — `model.go` / `store.go` / `service.go`
/ `handler.go` — so a change stays in one folder and a new module is a copy of
the pattern, not a new pattern.

## Configuration

Copy `.env.example` to `.env` and adjust as needed:

```bash
cp .env.example .env
```

The `.env` is gitignored. Defaults work out-of-the-box for local dev.

## Deployment

Runs on a VPS as one Docker Compose stack: the API, PostgreSQL, a one-shot
migration service, and [Caddy](https://caddyserver.com/) as a reverse proxy
with automatic HTTPS. `docker-compose.deploy.yml` is the production compose
file; `scripts/setup-vps.sh` provisions a fresh box.

The full runbook — SSH hardening, firewall, DNS, deploy, day-2 operations and
backups — is in [DEPLOY.md](DEPLOY.md).

```bash
# on the VPS, after setup
docker compose -f docker-compose.deploy.yml up -d
```

## Logging

Structured `slog` everywhere: pretty console output in dev, JSON on stdout in production. Every request gets an `X-Request-ID` (client-supplied or generated) that appears on every log line — testers paste it from error screens, we grep for the full trace. Bodies are redacted (passwords, tokens, card numbers, etc. → `[REDACTED]`) and capped at 4KB. Full plan: [logging-plan.md](../chubi-pocket-docs/engineering/logging-plan.md).

Two env dials — switching phase is an `.env` edit + restart, no code changes:

| Flag | Closed beta (now) | Wider beta | Production | Notes |
|---|---|---|---|---|
| `LOG_LEVEL` | `debug` | `info` | `warn` | Unset = `debug` in development, `info` otherwise |
| `LOG_BODIES` | `true` | `false` | `false` | Logs redacted request/response JSON bodies (4KB cap). Default `false` |

Closed-beta storage is stdout → Docker `json-file` rotation (20m × 5 files, configured in `docker-compose.yml`). Tail with `docker logs -f chubi_pocket_app`.
