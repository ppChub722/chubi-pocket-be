#!/usr/bin/env bash
# Deploy the committed BE (HEAD) to the ppForge VPS: build on this PC, docker load over ssh, swap the image,
# health check, roll back by itself if the check fails. The 1 GB box never builds.
#
#   ./scripts/deploy-ppforge.sh --dry-run     # print the plan, change nothing
#   ./scripts/deploy-ppforge.sh               # deploy; stops (exit 2) if there are new migrations
#   ./scripts/deploy-ppforge.sh --migrate     # same, and also backup + migrate (only after the owner said so)
#
# Run from Git Bash. Docker Desktop must be running. It never touches the app-update gate (MIN/LATEST_APP_BUILD)
# and never pushes; those are the owner's call. VPS_HOST (default ppforge) and HEALTH_URL override the target.
set -euo pipefail
export MSYS_NO_PATHCONV=1

HOST="${VPS_HOST:-ppforge}"
HEALTH_URL="${HEALTH_URL:-https://chubipocket-api.ppforge.dev}"
dry=false
migrate=false
for a in "$@"; do
  case "$a" in
    --dry-run) dry=true ;;
    --migrate) migrate=true ;;
    *) echo "unknown option: $a"; exit 64 ;;
  esac
done

cd "$(dirname "$0")/.."
step() { printf '\n== %s\n' "$*"; }

step "checks"
if ! git diff --quiet HEAD -- . ; then
  echo "tracked files have uncommitted changes. Deploy ships HEAD only: commit first."; exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker is not running. Start Docker Desktop and run again."; exit 1
fi
commit="$(git rev-parse --short HEAD)"
echo "HEAD $commit: $(git log -1 --format=%s | cut -c1-100)"
git status -sb | head -1

step "migrations"
local_m="$(ls migrations | sort)"
remote_m="$(ssh "$HOST" 'ls /srv/chubi/migrations' | sort)"
new_m="$(comm -23 <(printf '%s\n' "$local_m") <(printf '%s\n' "$remote_m") | sed '/^$/d')"
if [ -n "$new_m" ]; then
  echo "new migrations not on the server:"; printf '  %s\n' $new_m
else
  echo "none new"
fi

if $dry; then
  step "plan (dry run, nothing changed)"
  echo "build chubi-be:prod-new from git archive HEAD ($commit) -> docker save | ssh $HOST docker load"
  [ -n "$new_m" ] && echo "migrations: $($migrate && echo 'backup + migrate' || echo 'would STOP here; needs --migrate')"
  echo "swap chubi-be:prod -> prev, prod-new -> prod, compose up -d app, check $HEALTH_URL/health"
  exit 0
fi

if [ -n "$new_m" ] && ! $migrate; then
  echo; echo "STOP: new migrations need the owner's go-ahead. Run again with --migrate."; exit 2
fi

step "build"
git archive HEAD | docker build -q -t chubi-be:prod-new - >/dev/null
echo "built chubi-be:prod-new from $commit"

step "upload image"
docker save chubi-be:prod-new | gzip -1 | ssh "$HOST" 'gunzip | sudo docker load'

if [ -n "$new_m" ]; then
  step "backup + migrate"
  tar -czf - migrations | ssh "$HOST" 'rm -rf ~/upload && mkdir -p ~/upload && tar -xzf - -C ~/upload'
  ssh "$HOST" 'sudo bash /srv/backup/backup.sh'
  ssh "$HOST" 'sudo cp -r ~/upload/migrations/. /srv/chubi/migrations/ && cd /srv/chubi && sudo docker compose --profile tools run -T --rm migrate </dev/null'
fi

step "swap image"
ssh "$HOST" 'sudo docker tag chubi-be:prod chubi-be:prev && sudo docker tag chubi-be:prod-new chubi-be:prod && cd /srv/chubi && sudo docker compose up -d app 2>&1 | tail -2'

step "health"
ok=false
for _ in $(seq 1 15); do
  sleep 2
  if curl -fsS "$HEALTH_URL/health" >/dev/null 2>&1 && curl -fsS "$HEALTH_URL/api/v1/health" >/dev/null 2>&1; then ok=true; break; fi
done
if ! $ok; then
  echo "health check failed: rolling back to chubi-be:prev"
  ssh "$HOST" 'sudo docker tag chubi-be:prev chubi-be:prod && cd /srv/chubi && sudo docker compose up -d app 2>&1 | tail -2'
  echo "rolled back. A migration that already ran is NOT undone (backups: /srv/backup)."; exit 1
fi
echo "ok"
ssh "$HOST" 'sudo docker logs chubi_app --since 2m 2>&1' | grep -c -i '"level":"ERROR"' | sed 's/^/errors in the last 2 min of log: /' || true
printf '\ndeployed %s to %s. Old image kept as chubi-be:prev. Not pushed; the app-update gate is untouched.\n' "$commit" "$HOST"
