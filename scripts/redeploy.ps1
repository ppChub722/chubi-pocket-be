# Edit → test redeploy loop for chubi-pocket-be on local Docker Desktop.
# Run from the repo root:  .\scripts\redeploy.ps1
#
# What it does:
#   1. `go build ./...` — fail fast on compile errors (saves ~30s of Docker rebuild).
#   2. `docker compose up -d --build app` — rebuilds the app image, leaves db running,
#      auto-applies any new migrations via the `migrate` service.
#   3. Health check + migration head + last 20 app log lines.
#
# DB volume is preserved. For a wipe-and-restart, use:
#   docker compose down -v; docker compose up -d --build

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot\..

# --- 1. Pre-flight Go build ----------------------------------------------------
Write-Host '== go build ./...' -ForegroundColor Cyan
go build ./...
if ($LASTEXITCODE -ne 0) {
    Write-Host 'Go build failed. Aborting deploy.' -ForegroundColor Red
    exit $LASTEXITCODE
}

# --- 2. Rebuild + recreate the app container ----------------------------------
Write-Host '== docker compose up -d --build app' -ForegroundColor Cyan
docker compose up -d --build app
if ($LASTEXITCODE -ne 0) {
    Write-Host 'Docker build/up failed.' -ForegroundColor Red
    exit $LASTEXITCODE
}

# --- 3. Verify -----------------------------------------------------------------
Write-Host ''
Write-Host '== verify' -ForegroundColor Cyan

# Give the app ~5s to bind the port. (HTTP server starts after migrate exits.)
$deadline = (Get-Date).AddSeconds(15)
$health = $null
while ((Get-Date) -lt $deadline) {
    try {
        $health = Invoke-RestMethod -Uri http://localhost:8080/health -TimeoutSec 2
        break
    } catch {
        Start-Sleep -Milliseconds 500
    }
}
if ($null -eq $health) {
    Write-Host 'health check failed — app did not respond on :8080 within 15s' -ForegroundColor Red
    docker logs --tail 30 chubi_pocket_app
    exit 1
}

$version = (docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -t -c 'SELECT version FROM schema_migrations;').Trim()

Write-Host "  health         = $($health.status)" -ForegroundColor Green
Write-Host "  migration head = $version" -ForegroundColor Green
Write-Host ''
Write-Host '== last 20 app log lines' -ForegroundColor Cyan
docker logs --tail 20 chubi_pocket_app
