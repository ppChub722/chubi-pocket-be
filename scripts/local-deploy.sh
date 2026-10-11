#!/usr/bin/env bash
# Rebuild the local docker BE from the working tree (uncommitted changes
# included) before a QA run: build → migrate the local dev DB (:5433) →
# health check → one status line.
#
#   ./scripts/local-deploy.sh            # rebuild + migrate + health
#   ./scripts/local-deploy.sh --dry-run  # only print the current state
#   ./scripts/local-deploy.sh --web      # BE as above, then rebuild the web app
#                                        # from the app working tree and serve
#                                        # it at http://localhost:5181 (SPA
#                                        # fallback, background, restarted)
#
# Run from Git Bash (any cwd). The compose `migrate` service runs `up`
# before `app` starts, so new migrations apply on their own. It never drops
# or wipes the DB.
set -euo pipefail
# Git Bash rewrites /paths in docker arguments; keep them as written.
export MSYS_NO_PATHCONV=1

HEALTH_URL="http://localhost:8080/health"
WEB_PORT=5181
FLUTTER="${FLUTTER:-$HOME/fvm/versions/3.47.3/bin/flutter}"
DB_CONTAINER="chubi_pocket_db"
MIGRATE_CONTAINER="chubi_pocket_migrate"

dry_run=false
web=false
case "${1:-}" in
  --dry-run) dry_run=true ;;
  --web) web=true ;;
  -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
  "") ;;
  *) echo "unknown option: $1" >&2; exit 2 ;;
esac

BE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$BE_DIR"

commit="$(git rev-parse --short HEAD)"
dirty="$(git -c core.safecrlf=false status --porcelain --untracked-files=all | wc -l | tr -d ' ')"

migration() {
  docker exec "$DB_CONTAINER" psql -U chubadmin -d chubi_pocket_db -Atc \
    "SELECT version || CASE WHEN dirty THEN ' (DIRTY)' ELSE '' END FROM schema_migrations" \
    2>/dev/null || echo "?"
}
health() { curl -s -o /dev/null -m 3 -w '%{http_code}' "$HEALTH_URL" || true; }
newest_file() { ls -1 migrations/*.up.sql | tail -1 | sed 's|.*/||; s|_.*||; s|^0*||'; }

if $dry_run; then
  echo "dry-run: would run \`docker compose up -d --build\` in $BE_DIR"
  echo "local BE $commit +$dirty dirty · DB migration $(migration) (newest file $(newest_file)) · health $(health)"
  exit 0
fi

log="$(mktemp)"
if ! docker compose up -d --build >"$log" 2>&1; then
  tail -20 "$log" >&2
  echo "✗ docker compose up failed (full log: $log)" >&2
  exit 1
fi

# migrate is a one-shot container: it must have exited 0.
code="$(docker inspect "$MIGRATE_CONTAINER" --format '{{.State.ExitCode}}' 2>/dev/null || echo "?")"
if [[ "$code" != 0 ]]; then
  docker logs "$MIGRATE_CONTAINER" --tail 20 >&2 || true
  echo "✗ migrate exited $code" >&2
  exit 1
fi

status=""
for _ in $(seq 1 30); do
  status="$(health)"
  [[ "$status" == 200 ]] && break
  sleep 1
done

echo "local BE $commit +$dirty dirty · migration $(migration) · health $status"
[[ "$status" == 200 ]] || exit 1

$web || exit 0

# Web: build from the app working tree, serve build/web with an SPA fallback.
APP_DIR="$BE_DIR/../chubi-pocket-app"
cd "$APP_DIR"
app_commit="$(git rev-parse --short HEAD)"
app_dirty="$(git -c core.safecrlf=false status --porcelain --untracked-files=all | wc -l | tr -d ' ')"
wlog="$(mktemp)"
if ! "$FLUTTER" build web --dart-define=API_BASE_URL=http://localhost:8080/api/v1 >"$wlog" 2>&1; then
  tail -30 "$wlog" >&2
  echo "✗ flutter build web failed (full log: $wlog)" >&2
  exit 1
fi

# Replace any server already on the port.
old="$(netstat -ano | tr -d '' | awk -v p=":$WEB_PORT" '$2 ~ p"$" && $4=="LISTENING" {print $5}' | sort -u)"
for pid in $old; do taskkill //PID "$pid" //F >/dev/null 2>&1 || true; done
nohup node scripts/serve-web.js "$WEB_PORT" >"$APP_DIR/build/serve-web.log" 2>&1 &
disown
web_status=""
for _ in $(seq 1 10); do
  web_status="$(curl -s -o /dev/null -m 3 -w '%{http_code}' "http://localhost:$WEB_PORT/" || true)"
  [[ "$web_status" == 200 ]] && break
  sleep 1
done
echo "web :$WEB_PORT from app $app_commit +$app_dirty dirty · http $web_status"
[[ "$web_status" == 200 ]]
