#!/usr/bin/env bash
# One-shot deploy: backend to the VPS, then the APK to Firebase.
#
#   ./scripts/deploy.sh                 # BE + APK
#   ./scripts/deploy.sh --skip-apk      # BE only
#   ./scripts/deploy.sh --skip-be       # APK only
#   ./scripts/deploy.sh -m "notes"      # release notes for the APK
#   ./scripts/deploy.sh --dir <path>    # ship that checkout's HEAD (a worktree of
#                                       # a release tag) instead of this repo
#
# Run from Git Bash on the dev machine (any cwd). Deploys what is COMMITTED
# (git archive HEAD of the repo, or of --dir) — uncommitted changes are not
# shipped; the script warns.
#
# BE steps (see chubi-pocket-docs/ops/ops-runbook.md):
#   1. build image chubi-be:prod-new HERE (Docker Desktop must run): the 1 GB
#      box must never build — then docker save | ssh … docker load
#   2. on the VPS: pg_dump backup (/srv/backup/backup.sh)
#   3. copy migrations → /srv/chubi/migrations, run `migrate up`
#   4. keep the running image as chubi-be:prev, swap in the new one, restart
#   5. health check /health and /api/v1/health — on failure the previous image
#      is put back automatically (migrations are NOT rolled back; restore from
#      the backup taken in step 2 if ever needed).
set -euo pipefail

VPS="${VPS_HOST:-Admin@143.14.147.249}"   # ppForge (ssh alias ppforge); the old Happy-Host box is gone
KEY="$HOME/.ssh/ppforge_vps"
HEALTH_URL="https://chubipocket-api.ppforge.dev/health"

BE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="$(cd "$BE_DIR/../chubi-pocket-app" 2>/dev/null && pwd || true)"

skip_be=false
skip_apk=false
src_dir=""
notes=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-be) skip_be=true ;;
    --skip-apk) skip_apk=true ;;
    -m|--notes) notes="${2:-}"; shift ;;
    --dir) src_dir="${2:-}"; shift ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '\n\033[1;36m▶ %s\033[0m\n' "$*"; }
# The host drops a connection now and then: retry the connect, and notice a
# dead link instead of hanging.
SSH_OPTS=(-i "$KEY" -o BatchMode=yes -o ConnectTimeout=15 -o ConnectionAttempts=3
  -o ServerAliveInterval=15 -o ServerAliveCountMax=4)
ssh_vps() { ssh "${SSH_OPTS[@]}" "$VPS" "$@"; }

deploy_be() {
  cd "${src_dir:-$BE_DIR}"
  local commit
  commit="$(git rev-parse --short HEAD)"
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "⚠ chubi-pocket-be has uncommitted changes — deploying commit $commit only."
  fi
  if [[ -n "$(git log '@{u}..HEAD' --oneline 2>/dev/null || true)" ]]; then
    echo "⚠ commit $commit is not pushed yet (deploying it anyway)."
  fi

  say "BE: building image of commit $commit (here, not on the VPS)"
  docker info >/dev/null 2>&1 || { echo "✗ Docker is not running on this machine" >&2; exit 1; }
  local src
  src="$(mktemp -d)"
  git archive HEAD | tar -x -C "$src"
  docker build -q -t chubi-be:prod-new --label "commit=$commit" "$src" >/dev/null

  say "BE: shipping the image + migrations"
  docker save chubi-be:prod-new | gzip -1 | ssh_vps 'gunzip | sudo docker load' >/dev/null
  tar -czf - -C "$src" migrations | ssh_vps 'rm -rf ~/deploy/migrations && mkdir -p ~/deploy && tar -xzf - -C ~/deploy'
  rm -rf "$src"

  say "BE: backup → migrate → swap → health check (on the VPS)"
  ssh_vps "COMMIT=$commit HEALTH_URL=$HEALTH_URL bash -s" <<'REMOTE'
set -euo pipefail
cd ~/deploy

echo "· backup"
sudo bash /srv/backup/backup.sh >/dev/null
echo "  latest dump: $(ls -t /srv/backup/dumps | grep '\.dump\.gz$' | head -1)"

echo "· migrations"
sudo cp -r migrations/. /srv/chubi/migrations/
cd /srv/chubi
# -T + </dev/null: compose must not read stdin — this script itself arrives
# on stdin (ssh ... bash -s), and `run` would swallow the rest of it.
sudo docker compose --profile tools run -T --rm migrate </dev/null 2>&1 | tail -5

echo "· swapping image"
if sudo docker image inspect chubi-be:prod >/dev/null 2>&1; then
  sudo docker tag chubi-be:prod chubi-be:prev
fi
sudo docker tag chubi-be:prod-new chubi-be:prod
sudo docker compose up -d app </dev/null >/dev/null 2>&1

echo "· health check"
for i in $(seq 1 30); do
  if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1 &&
     curl -fsS --max-time 3 "${HEALTH_URL%/health}/api/v1/health" >/dev/null 2>&1; then
    echo "  healthy after ${i}s"
    sudo docker image prune -f >/dev/null
    exit 0
  fi
  sleep 1
done

echo "✗ health check failed — rolling back to the previous image"
sudo docker logs chubi_app --tail 30 || true
if sudo docker image inspect chubi-be:prev >/dev/null 2>&1; then
  sudo docker tag chubi-be:prev chubi-be:prod
  sudo docker compose up -d app </dev/null >/dev/null 2>&1
fi
exit 1
REMOTE
  say "BE: live — commit $commit"
}

deploy_apk() {
  if [[ -z "$APP_DIR" || ! -x "$APP_DIR/scripts/release-apk.sh" ]]; then
    echo "✗ chubi-pocket-app/scripts/release-apk.sh not found next to the BE repo" >&2
    exit 1
  fi
  say "APK: build + Firebase"
  "$APP_DIR/scripts/release-apk.sh" ${notes:+-m "$notes"}
}

$skip_be || deploy_be
$skip_apk || deploy_apk
say "done ✓"
