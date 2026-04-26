# chubi-pocket-be

Backend API for ChubiPocket. Phase 0 ships authentication and user profile management.

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

## Phase 0 endpoints

```
GET    /health
POST   /api/v1/auth/register
POST   /api/v1/auth/login
POST   /api/v1/auth/logout              (auth)
PUT    /api/v1/auth/password            (auth)
GET    /api/v1/users/me                 (auth)
PUT    /api/v1/users/me                 (auth)
PUT    /api/v1/users/me/password        (auth — alias of /auth/password)
POST   /api/v1/users/me/deactivate      (auth)
POST   /api/v1/users/me/reactivate      (auth, allowed while inactive)
```

Specs: [01-auth.md](../chubi-pocket-docs/design/spec/01-auth.md), [02-users.md](../chubi-pocket-docs/design/spec/02-users.md). Phase 0 plan: [phase0/be.md](../chubi-pocket-docs/product/phase0/be.md).

## Project layout

```
cmd/api/main.go           # entrypoint — wires modules + router
internal/
  modules/
    auth/                 # register, login, logout, change password, JWT middleware
    users/                # GET/PUT /me, deactivate, reactivate
  platform/
    config/               # env loader
    database/             # pgxpool factory
    logger/               # slog + Gin middleware
    response/             # uniform error/success envelope
migrations/               # golang-migrate SQL files (canonical schema lives in docs)
```

Each feature module has the same shape — `model.go` / `store.go` / `service.go` / `handler.go`. Phase 1+ modules copy this layout.

## Configuration

Copy `.env.example` to `.env` and adjust as needed:

```bash
cp .env.example .env
```

The `.env` is gitignored. Defaults work out-of-the-box for local dev.
