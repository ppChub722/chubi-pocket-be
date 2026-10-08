#!/usr/bin/env bash
# One-shot deploy: backend to the VPS, then the APK to Firebase.
#
#   ./scripts/deploy.sh                 # BE + APK
#   ./scripts/deploy.sh --skip-apk      # BE only
#   ./scripts/deploy.sh --skip-be       # APK only
#   ./scripts/deploy.sh -m "notes"      # release notes for the APK
#
# Run from Git Bash on the dev machine (any cwd). Deploys what is COMMITTED
# (git archive HEAD) — uncommitted changes are not shipped; the script warns.
#
# BE steps (all on the VPS, see chubi-pocket-docs/engineering/ops-runbook.md):
#   1. upload the source, build image chubi-be:prod-new on the server
#   2. pg_dump backup (/srv/backup/backup.sh)
#   3. copy migrations → /srv/chubi/migrations, run `migrate up`
#   4. keep the running image as chubi-be:prev, swap in the new one, restart
#   5. health check https://chubipocket-api.ppforge.dev/health — on failure the
#      previous image is put back automatically (migrations are NOT rolled
#      back; restore from the backup taken in step 2 if ever needed).
set -euo pipefail

VPS="Admin@160.238.13.137"
KEY="$HOME/.ssh/ppforge_vps"
HEALTH_URL="https://chubipocket-api.ppforge.dev/health"

BE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="$(cd "$BE_DIR/../chubi-pocket-app" 2>/dev/null && pwd || true)"

skip_be=false
skip_apk=false
notes=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-be) skip_be=true ;;
    --skip-apk) skip_apk=true ;;
    -m|--notes) notes="${2:-}"; shift ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '\n\033[1;36m▶ %s\033[0m\n' "$*"; }
ssh_vps() { ssh -i "$KEY" -o BatchMode=yes -o ConnectTimeout=15 "$VPS" "$@"; }

deploy_be() {
  cd "$BE_DIR"
  local commit
  commit="$(git rev-parse --short HEAD)"
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "⚠ chubi-pocket-be has uncommitted changes — deploying commit $commit only."
  fi
  if [[ -n "$(git log '@{u}..HEAD' --oneline 2>/dev/null || true)" ]]; then
    echo "⚠ commit $commit is not pushed yet (deploying it anyway)."
  fi

  say "BE: packing commit $commit"
  local tgz
  tgz="$(mktemp -d)/chubi-be-src.tgz"
  git archive --format=tar.gz -o "$tgz" HEAD

  say "BE: uploading"
  ssh_vps 'mkdir -p ~/deploy'
  scp -i "$KEY" -q "$tgz" "$VPS:~/deploy/chubi-be-src.tgz"

  say "BE: build → backup → migrate → swap → health check (on the VPS)"
  ssh_vps "COMMIT=$commit HEALTH_URL=$HEALTH_URL bash -s" <<'REMOTE'
set -euo pipefail
cd ~/deploy
rm -rf src && mkdir src && tar -xzf chubi-be-src.tgz -C src

echo "· building image (commit $COMMIT)"
sudo docker build -q -t chubi-be:prod-new --label "commit=$COMMIT" src </dev/null >/dev/null

echo "· backup"
sudo bash /srv/backup/backup.sh >/dev/null
echo "  latest dump: $(ls -t /srv/backup/dumps | head -1)"

echo "· migrations"
sudo cp -r src/migrations/. /srv/chubi/migrations/
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
  if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then
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
