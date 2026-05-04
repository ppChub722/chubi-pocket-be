# Common Commands

Day-to-day commands for the `chubi-pocket-be` local dev environment.
Run from this directory (`chubi-pocket-be/`) unless noted.

PowerShell is the recommended shell on Windows — Git Bash mangles Docker volume paths.

---

## Docker Desktop

```powershell
# Start Docker Desktop (Windows)
Start-Process "C:\Program Files\Docker\Docker\Docker Desktop.exe"

# Verify the engine is up
docker info
docker version --format '{{.Server.Version}}'
```

---

## Bring up the whole stack

```powershell
# Build the app image, start db + run migrations + start app — all in one
docker compose up -d --build

# Tail app logs
docker logs -f chubi_pocket_app

# Verify health
curl http://localhost:8080/health
```

The compose flow runs in three ordered steps automatically:

1. `db` starts and waits for healthcheck
2. `migrate` runs once (`up`) and exits cleanly
3. `app` starts only after `migrate` exits successfully

---

## Just the database

```powershell
# Start only Postgres
docker compose up -d db

# Stop the DB (data persists in volume)
docker compose stop db

# Tail DB logs
docker logs -f chubi_pocket_db

# Health status
docker inspect -f '{{.State.Health.Status}}' chubi_pocket_db
```

**Connection string** (for TablePlus / DBeaver / pgAdmin):
```
postgres://chubadmin:admin1234@localhost:5432/chubi_pocket_db
```

---

## Migrations

The `migrate` compose service handles the default `up` flow on `docker compose up`.
For other commands, run the same image manually with overridden args.

```powershell
# Apply pending migrations (idempotent — safe to re-run)
docker compose run --rm migrate `
  -path=/migrations `
  -database "postgres://chubadmin:admin1234@db:5432/chubi_pocket_db?sslmode=disable" `
  up

# Rollback the last migration
docker compose run --rm migrate `
  -path=/migrations `
  -database "postgres://chubadmin:admin1234@db:5432/chubi_pocket_db?sslmode=disable" `
  down 1

# Show current version
docker compose run --rm migrate `
  -path=/migrations `
  -database "postgres://chubadmin:admin1234@db:5432/chubi_pocket_db?sslmode=disable" `
  version

# Force version (after a dirty/failed migration). Replace N with the version number.
docker compose run --rm migrate `
  -path=/migrations `
  -database "postgres://chubadmin:admin1234@db:5432/chubi_pocket_db?sslmode=disable" `
  force N

# Create a new migration pair (replace <name>)
docker run --rm -v "${PWD}\migrations:/migrations" migrate/migrate `
  create -ext sql -dir /migrations -seq <name>
```

---

## psql / inspecting the DB

```powershell
# Open an interactive psql shell
docker exec -it chubi_pocket_db psql -U chubadmin -d chubi_pocket_db

# Run a one-off query
docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -c "SELECT COUNT(*) FROM users;"

# List tables
docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -c "\dt"

# Describe a table
docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -c "\d users"

# Check applied migrations
docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -c "SELECT * FROM schema_migrations;"
```

---

## Running the API natively (without the `app` container)

Useful when iterating with Air hot-reload.

```powershell
# Start only the DB + apply migrations
docker compose up -d db migrate

# Run the API on host (uses .env, DB_HOST=localhost)
go run ./cmd/api

# Or with hot reload via Air (https://github.com/air-verse/air)
air
```

---

## End-to-end test scripts

```bash
# Phase 1a end-to-end smoke (register → accounts → tx → tag → summary → transfer edit)
bash scripts/smoke_phase1a.sh

# Concurrent balance-cache invariant check (50 workers × 20 transfers by default)
bash scripts/test_balance_invariant.sh
```

Full docs: [`scripts/README.md`](scripts/README.md). Both run against
the live BE container; `python` is required on PATH.

---

## Cleanup / fresh start

```powershell
# Stop and remove containers (volume + data persist)
docker compose down

# Stop and WIPE everything (containers + network + volume + data)
docker compose down -v

# Full fresh boot in one go
docker compose down -v
docker compose up -d --build
```
