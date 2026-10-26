#!/usr/bin/env bash
# Deploy one release from its git tags (Lead commits, tags and pushes;
# DevOps never does). Given a brief folder (template + format:
# chubi-pocket-docs/team/templates/release-brief/):
#
#   ./scripts/release.sh --dry-run <brief-dir>   # read-only: preflight + the plan
#   ./scripts/release.sh <brief-dir>             # do it
#   options: --skip-checks     redeploy / rollback of an already-checked tag
#            --keep-worktrees  leave the tag checkouts in place afterwards
#
# Steps: resolve tag v<version> in be/app/docs → check the be and app tags out
# into clean worktrees (the team's main trees are never touched) → checks →
# backup → migrate → deploy BE from the tag → APK from the tag → prod .env →
# verify → offsite backup copy → release entry in the MAIN docs tree
# (uncommitted: Lead commits it) → summary.
#
# Redeploy / rollback = the same command with an older VERSION. Migrations are
# never rolled back: a tag older than prod's migration level is refused.
#
# Stops at the first problem. Safe to re-run after a failure: every step first
# looks at whether it is already done (prod image label, LATEST_APP_BUILD,
# the entry file) and skips it. Facts it can't re-read (check results, APK
# upload, backup name) live in <brief-dir>/.state; logs in <brief-dir>/logs/;
# the final summary in <brief-dir>/summary.txt.
#
# Run from Git Bash on the dev machine (any cwd).
set -euo pipefail

VPS="${VPS_HOST:-Admin@143.14.147.249}"   # ppForge (ssh alias ppforge); the old Happy-Host box is gone
KEY="$HOME/.ssh/ppforge_vps"
API="https://chubipocket-api.ppforge.dev"
SSH_OPTS=(-i "$KEY" -o BatchMode=yes -o ConnectTimeout=15 -o ConnectionAttempts=3
  -o ServerAliveInterval=15 -o ServerAliveCountMax=4)
TEST_DB="postgres://chubadmin:admin1234@localhost:5433/chubi_pocket_db?sslmode=disable"

BE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "$BE_DIR/.." && pwd)"
APP_DIR="$ROOT/chubi-pocket-app"
DOCS_DIR="$ROOT/chubi-pocket-docs"
INFRA_DIR="$(cd "$ROOT/../ppforge-infra" && pwd)"
FLUTTER="$HOME/fvm/versions/3.47.3/bin/flutter"
[[ -x "$FLUTTER" ]] || FLUTTER="$APP_DIR/.fvm/flutter_sdk/bin/flutter"
# Short path on purpose: Gradle on Windows chokes on long ones.
WORK_ROOT="${RELEASE_WORK:-/c/chubi-rel}"

# ── args ────────────────────────────────────────────────────────────────
DRY=false
SKIP_CHECKS=false
KEEP=false
BRIEF=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY=true ;;
    --skip-checks) SKIP_CHECKS=true ;;
    --keep-worktrees) KEEP=true ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) BRIEF="$1" ;;
  esac
  shift
done
[[ -n "$BRIEF" && -d "$BRIEF" ]] || { echo "usage: release.sh [--dry-run] [--skip-checks] [--keep-worktrees] <brief-dir>" >&2; exit 2; }
BRIEF="$(cd "$BRIEF" && pwd)"
# State and logs are written into the brief folder: keep it out of the repos.
for d in "$BE_DIR" "$APP_DIR" "$DOCS_DIR"; do
  [[ "$BRIEF/" != "$d/"* ]] || { echo "brief folder is inside $(basename "$d") — copy it outside the repos" >&2; exit 2; }
done
STATE="$BRIEF/.state"
LOGS="$BRIEF/logs"
mkdir -p "$LOGS"

# ── helpers ─────────────────────────────────────────────────────────────
say()  { printf '\n\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '  ✓ %s\n' "$*"; }
skip() { printf '  · %s\n' "$*"; }
plan() { printf '  → would %s\n' "$*"; }
warn() { printf '  \033[1;33m⚠ %s\033[0m\n' "$*"; }
die()  { printf '\n\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

ssh_vps() { ssh "${SSH_OPTS[@]}" "$VPS" "$@"; }
# -c core.safecrlf=false: no "LF will be replaced by CRLF" line per file.
g() { local d="$1"; shift; git -c core.safecrlf=false -C "$d" "$@"; }

state_get() { [[ -f "$STATE" ]] && grep -m1 "^$1=" "$STATE" | cut -d= -f2- || true; }
state_set() {
  $DRY && return 0
  { [[ -f "$STATE" ]] && grep -v "^$1=" "$STATE" || true; } > "$STATE.tmp"
  printf '%s=%s\n' "$1" "$2" >> "$STATE.tmp"
  mv "$STATE.tmp" "$STATE"
}

short() { g "$1" rev-parse --short "$2"; }

# key=value lines about prod, in one ssh round trip.
read_prod() {
  ssh_vps 'bash -s' <<'REMOTE'
echo "label=$(sudo docker image inspect chubi-be:prod --format '{{index .Config.Labels "commit"}}' 2>/dev/null)"
sudo grep -E '^(LATEST|MIN)_APP_BUILD=' /srv/chubi/.env
echo "migration=$(sudo docker exec shared_postgres psql -U postgres -d chubi_pocket -Atc "SELECT version || CASE WHEN dirty THEN ' DIRTY' ELSE '' END FROM schema_migrations")"
echo "migration_files=$(ls -1 /srv/chubi/migrations | sed -n 's/^0*\([0-9][0-9]*\)_.*\.up\.sql$/\1/p' | tr '\n' ' ')"
echo "dump=$(ls -1t /srv/backup/dumps | grep '\.dump\.gz$' | head -1)"
echo "errors=$(sudo docker logs chubi_app 2>&1 | grep -ciE '"level":"ERROR"|panic|fatal' || true)"
echo "envbak=$(ls /srv/chubi/.env.bak-* 2>/dev/null | sed 's|.*/||' | tr '\n' ' ')"
REMOTE
}
PROD=""
prod() { printf '%s\n' "$PROD" | grep -m1 "^$1=" | cut -d= -f2- || true; }

http_code() { curl -s -o /dev/null -m 10 -w '%{http_code}' "$1" || true; }

# ── brief ───────────────────────────────────────────────────────────────
say "brief: $BRIEF"
[[ -f "$BRIEF/brief.env" ]] || die "brief.env missing"
VERSION="" EXPECT_MIGRATIONS="" MIN_APP_BUILD="" QA="" SKIP_BE="n" SKIP_APP="n"
while IFS= read -r line || [[ -n "$line" ]]; do
  line="${line%$'\r'}"
  [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
  [[ "$line" =~ ^(VERSION|EXPECT_MIGRATIONS|MIN_APP_BUILD|QA|SKIP_BE|SKIP_APP)=(.*)$ ]] \
    || die "brief.env: unknown line: $line"
  val="${BASH_REMATCH[2]}"; val="${val%%#*}"; val="${val%"${val##*[![:space:]]}"}"
  val="${val%\"}"; val="${val#\"}"
  printf -v "${BASH_REMATCH[1]}" '%s' "$val"
done < "$BRIEF/brief.env"

[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9]+)?$ ]] || die "VERSION '$VERSION' is not like 0.3.1-9"
[[ "$EXPECT_MIGRATIONS" == none || "$EXPECT_MIGRATIONS" =~ ^[0-9]+( [0-9]+)*$ ]] \
  || die "EXPECT_MIGRATIONS must be 'none' or numbers like '53 54'"
[[ "$MIN_APP_BUILD" == unchanged || "$MIN_APP_BUILD" =~ ^[0-9]+$ ]] \
  || die "MIN_APP_BUILD must be 'unchanged' or a build number"
[[ "$QA" == y || "$QA" == n ]] || die "QA must be y or n"
[[ "$SKIP_BE" == y || "$SKIP_BE" == n ]] || die "SKIP_BE must be y or n"
[[ "$SKIP_APP" == y || "$SKIP_APP" == n ]] || die "SKIP_APP must be y or n"
[[ -f "$BRIEF/entry.md" ]] || die "entry.md missing"
HAS_BE=true;  [[ "$SKIP_BE" == y ]] && HAS_BE=false
HAS_APP=true; [[ "$SKIP_APP" == y ]] && HAS_APP=false
TAG="v$VERSION"
ok "version $VERSION (tag $TAG) · migrations $EXPECT_MIGRATIONS · MIN $MIN_APP_BUILD · QA $QA$($HAS_BE || echo ' · BE skipped')$($HAS_APP || echo ' · app skipped')"
$SKIP_CHECKS && warn "--skip-checks: the tags are deployed without running the checks"

# ── preflight: the tags ─────────────────────────────────────────────────
say "preflight"
resolve_tag() { # repo-dir → commit (full) of the pushed tag, or die
  local d="$1" name c; name="$(basename "$1")"
  g "$d" fetch -q origin --tags
  c="$(g "$d" rev-parse -q --verify "refs/tags/$TAG^{commit}" || true)"
  [[ -n "$c" ]] || die "$name: tag $TAG not found (Lead creates and pushes it)"
  [[ -n "$(g "$d" ls-remote --tags origin "refs/tags/$TAG")" ]] || die "$name: tag $TAG is not pushed to origin"
  echo "$c"
}
BE_COMMIT="$(resolve_tag "$BE_DIR")"
APP_COMMIT="$(resolve_tag "$APP_DIR")"
DOCS_COMMIT="$(resolve_tag "$DOCS_DIR")"
BE_SHORT="$(short "$BE_DIR" "$BE_COMMIT")"
APP_SHORT="$(short "$APP_DIR" "$APP_COMMIT")"
ok "$TAG: be $BE_SHORT · app $APP_SHORT · docs $(short "$DOCS_DIR" "$DOCS_COMMIT")"
BE_SUBJ="$(g "$BE_DIR" log -1 --format=%s "$BE_COMMIT")"
APP_SUBJ="$(g "$APP_DIR" log -1 --format=%s "$APP_COMMIT")"

if $HAS_APP; then
  tag_ver="$(g "$APP_DIR" show "$APP_COMMIT:pubspec.yaml" | tr -d '\r' | grep -m1 '^version:' || true)"
  [[ "$tag_ver" == "version: $VERSION"* ]] \
    || die "app tag $TAG has '$tag_ver' in pubspec.yaml, expected 'version: $VERSION+…' (Lead bumps it before tagging)"
  BUILD="$(g "$APP_DIR" rev-list --count "$APP_COMMIT")"
  ok "app build will be $BUILD (Firebase $((2000 + BUILD)))"
fi

PROD="$(read_prod)" || die "cannot reach the VPS over ssh"
[[ "$(http_code "$API/health")" == 200 ]] || die "prod /health is not 200"
ok "prod: BE $(prod label) · migration $(prod migration) · LATEST $(prod LATEST_APP_BUILD) · MIN $(prod MIN_APP_BUILD) · health 200"
[[ "$(prod migration)" != *DIRTY* ]] || die "prod migration state is DIRTY"
CUR_LATEST="$(prod LATEST_APP_BUILD)"

BE_DEPLOYED=false
new_migs=none
tag_newest=""
if $HAS_BE; then
  [[ "$(prod label)" == "$BE_SHORT" ]] && BE_DEPLOYED=true
  tag_migs="$(g "$BE_DIR" ls-tree --name-only "$BE_COMMIT:migrations" | sed -n 's/^0*\([0-9][0-9]*\)_.*\.up\.sql$/\1/p')"
  tag_newest="$(printf '%s\n' $tag_migs | sort -n | tail -1)"
  prod_mig="$(prod migration)"; prod_mig="${prod_mig%% *}"
  if ! $BE_DEPLOYED && [[ "$prod_mig" =~ ^[0-9]+$ ]] && (( prod_mig > tag_newest )); then
    die "prod migration $prod_mig is newer than the tag's newest ($tag_newest): migrations are not rolled back — restore from a dump, or pick a newer tag"
  fi
  # New migrations = files in the tag that prod doesn't have yet.
  new_migs=""
  for m in $tag_migs; do
    [[ " $(prod migration_files) " == *" $m "* ]] || new_migs="$new_migs $m"
  done
  new_migs="${new_migs# }"; [[ -n "$new_migs" ]] || new_migs=none
  if $BE_DEPLOYED; then
    skip "migrations: BE already deployed (prod at $(prod migration))"
  elif [[ "$new_migs" == "$EXPECT_MIGRATIONS" ]]; then
    ok "new migrations: $new_migs (as briefed)"
  else
    die "new migrations in the tag: '$new_migs', the brief expects '$EXPECT_MIGRATIONS'"
  fi
fi

# LATEST only ever goes up: a redeploy / rollback to an older tag keeps it.
WANT_LATEST="$CUR_LATEST"
if $HAS_APP && (( BUILD > CUR_LATEST )); then WANT_LATEST="$BUILD"; fi
WANT_MIN="$(prod MIN_APP_BUILD)"; [[ "$MIN_APP_BUILD" == unchanged ]] || WANT_MIN="$MIN_APP_BUILD"

# ── worktrees ───────────────────────────────────────────────────────────
say "worktrees (clean checkouts of the tags)"
WT="$WORK_ROOT/$VERSION"
# Named like the repos: app tests read ../chubi-pocket-be/... (the BE tag's spec).
BE_WT="$WT/chubi-pocket-be"; APP_WT="$WT/chubi-pocket-app"
add_worktree() { # repo-dir worktree-dir commit
  local d="$2"
  if [[ -d "$d" ]]; then
    if [[ "$(g "$d" rev-parse HEAD 2>/dev/null || true)" == "$3" && -z "$(g "$d" status --porcelain 2>/dev/null | head -1)" ]]; then
      skip "$(basename "$1"): $d already at $TAG"; return
    fi
    g "$1" worktree remove --force "$d" >/dev/null 2>&1 || rm -rf "$d"
  fi
  g "$1" worktree prune
  mkdir -p "$(dirname "$d")"
  g "$1" worktree add -q --detach "$d" "$3"
  ok "$(basename "$1"): $TAG → $d"
}
if $DRY; then
  $HAS_BE  && plan "check out be $TAG into $BE_WT"
  $HAS_APP && plan "check out app $TAG into $APP_WT"
else
  $HAS_BE  && add_worktree "$BE_DIR" "$BE_WT" "$BE_COMMIT"
  $HAS_APP && add_worktree "$APP_DIR" "$APP_WT" "$APP_COMMIT"
fi

# ── checks ──────────────────────────────────────────────────────────────
say "checks"
be_checks() { # in the worktree
  cd "$BE_WT"
  go vet ./... || { echo "CHECK go vet failed"; return 1; }
  local rc=0
  TEST_DATABASE_URL="$TEST_DB" go test -count=1 -v ./... || rc=$?
  return $rc
}
app_checks() {
  cd "$APP_WT"
  "$FLUTTER" analyze || return 1
  "$FLUTTER" test
}
need_be_checks=false;  $HAS_BE  && [[ "$(state_get checks_be_for)"  != "$BE_COMMIT"  ]] && need_be_checks=true
need_app_checks=false; $HAS_APP && [[ "$(state_get checks_app_for)" != "$APP_COMMIT" ]] && need_app_checks=true
if $SKIP_CHECKS; then
  warn "checks skipped (--skip-checks)"
  state_set checks_be "skipped"; state_set checks_app "skipped"
elif ! $need_be_checks && ! $need_app_checks; then
  skip "checks already ran on these tags"
  [[ -n "$(state_get checks_be)$(state_get checks_app)" ]] \
    && skip "BE $(state_get checks_be) · app $(state_get checks_app)"
else
  if $need_be_checks; then
    # DB tests SKIP (not fail) when the DB is down or behind, so check it first.
    local_ver="$(MSYS_NO_PATHCONV=1 docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -Atc \
      "SELECT version FROM schema_migrations WHERE NOT dirty" 2>/dev/null || true)"
    [[ "$local_ver" == "$tag_newest" ]] \
      || die "local test DB (:5433) is at '${local_ver:-unreachable}', the tag's newest migration is $tag_newest — migrate the local DB first (Lead: ask for a local deploy), or use --skip-checks for a rollback"
    ok "local test DB at migration $local_ver"
  fi
  if $DRY; then
    $need_be_checks  && plan "run BE in the worktree: go vet ./... + go test -count=1 -v ./... (TEST_DATABASE_URL :5433), require 0 failed / 0 skipped"
    $need_app_checks && plan "run app in the worktree: flutter analyze + flutter test"
  else
    pids=()
    $need_be_checks  && { be_checks  > "$LOGS/checks-be.log"  2>&1 & pids+=("be:$!"); }
    $need_app_checks && { app_checks > "$LOGS/checks-app.log" 2>&1 & pids+=("app:$!"); }
    failed=""
    for p in "${pids[@]}"; do wait "${p#*:}" || failed="$failed ${p%%:*}"; done
    if $need_be_checks; then
      l="$LOGS/checks-be.log"
      pass="$(grep -c -- '--- PASS' "$l" || true)"; fail="$(grep -c -- '--- FAIL' "$l" || true)"
      skp="$(grep -c -- '--- SKIP' "$l" || true)"
      [[ "$fail" == 0 && "$skp" == 0 && " $failed " != *" be "* ]] \
        || die "BE checks: $pass passed / $fail failed / $skp skipped (see $l)"
      state_set checks_be "go vet clean, go test $pass passed / 0 failed / 0 skipped"
      state_set checks_be_for "$BE_COMMIT"
      ok "BE: $(state_get checks_be)"
    fi
    if $need_app_checks; then
      l="$LOGS/checks-app.log"
      [[ " $failed " != *" app "* ]] || { tail -15 "$l" >&2; die "app checks failed (see $l)"; }
      grep -aq 'No issues found!' "$l" || die "flutter analyze reported issues (see $l)"
      n="$(grep -ao '+[0-9]*[^:]*: All tests passed' "$l" | tail -1 | grep -o '[0-9]*' | head -1)"
      state_set checks_app "analyze clean, flutter test ${n:-?} passed"
      state_set checks_app_for "$APP_COMMIT"
      ok "app: $(state_get checks_app)"
    fi
  fi
fi

# ── deploy BE ───────────────────────────────────────────────────────────
say "deploy BE"
if ! $HAS_BE; then
  skip "BE skipped (SKIP_BE=y)"
elif $BE_DEPLOYED; then
  skip "prod already runs $(prod label)"
  $DRY || [[ -n "$(state_get backup)" ]] || state_set backup "$(prod dump) (newest; deploy ran earlier)"
elif $DRY; then
  plan "run deploy.sh --skip-apk --dir $BE_WT (backup → migrate → swap → health; new migrations: $new_migs)"
else
  "$BE_DIR/scripts/deploy.sh" --skip-apk --dir "$BE_WT" 2>&1 | tee "$LOGS/deploy.log" \
    || die "deploy.sh failed (see $LOGS/deploy.log)"
  dump="$(sed -n 's/.*latest dump: //p' "$LOGS/deploy.log" | tail -1)"
  state_set backup "$dump"
  ok "BE live: $BE_SHORT · backup $dump"
fi

# ── APK ─────────────────────────────────────────────────────────────────
say "APK"
if ! $HAS_APP; then
  skip "app skipped (SKIP_APP=y)"
elif [[ "$(state_get apk)" == "$BUILD" ]]; then
  skip "build $BUILD already uploaded (Firebase $(state_get firebase))"
elif (( CUR_LATEST >= BUILD )); then
  # LATEST is only raised after the upload, so the APK went out earlier
  # (or this tag is older than what testers already have: redeploy / rollback).
  skip "prod LATEST_APP_BUILD=$CUR_LATEST ≥ $BUILD — APK already out (or older); LATEST is never lowered"
  $DRY || [[ -n "$(state_get firebase)" ]] || state_set firebase "$((2000 + BUILD))"
elif $DRY; then
  plan "run release-apk.sh --dir $APP_WT → build $BUILD to firsttester (Firebase $((2000 + BUILD)))"
else
  "$APP_DIR/scripts/release-apk.sh" --dir "$APP_WT" -m "$VERSION — $APP_SUBJ ($APP_SHORT)" 2>&1 | tee "$LOGS/apk.log" \
    || die "release-apk.sh failed (see $LOGS/apk.log)"
  fb="$(grep -ao 'uploaded new release [^ ]* ([0-9]*)' "$LOGS/apk.log" | grep -o '([0-9]*)' | tr -d '()' | tail -1)"
  state_set apk "$BUILD"
  state_set firebase "${fb:-?}"
  ok "APK build $BUILD uploaded (Firebase ${fb:-?})"
fi

# ── prod .env ───────────────────────────────────────────────────────────
say "prod .env"
if [[ "$(prod LATEST_APP_BUILD)" == "$WANT_LATEST" && "$(prod MIN_APP_BUILD)" == "$WANT_MIN" ]]; then
  skip "already LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN"
elif $DRY; then
  plan "back up .env → .env.bak-$VERSION; set LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN; recreate app"
else
  if ! ssh_vps "V='$VERSION' L='$WANT_LATEST' M='$WANT_MIN' bash -s" <<'REMOTE' 2>&1 | tee "$LOGS/env.log"; then
set -euo pipefail
cd /srv/chubi
sudo grep -q '^LATEST_APP_BUILD=' .env && sudo grep -q '^MIN_APP_BUILD=' .env || { echo "keys missing in .env"; exit 1; }
[[ -e ".env.bak-$V" ]] || sudo cp -p .env ".env.bak-$V"
sudo sed -i -e "s/^LATEST_APP_BUILD=.*/LATEST_APP_BUILD=$L/" -e "s/^MIN_APP_BUILD=.*/MIN_APP_BUILD=$M/" .env
sudo grep -E '^(LATEST|MIN)_APP_BUILD=' .env
sudo docker compose up -d --force-recreate app </dev/null >/dev/null 2>&1
REMOTE
    die "env update failed (see $LOGS/env.log)"
  fi
  for _ in $(seq 1 30); do [[ "$(http_code "$API/health")" == 200 ]] && break; sleep 1; done
  ok "LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN · app recreated"
fi

# ── verify ──────────────────────────────────────────────────────────────
say "verify"
if $DRY; then
  plan "check /health 200 · app/version min $WANT_MIN latest $WANT_LATEST · unauthenticated /transactions 401 · app log errors 0 · prod label = $BE_SHORT"
  VERIFY="(dry-run)"
else
  PROD="$(read_prod)"
  h="$(http_code "$API/health")"
  ver="$(curl -s -m 10 "$API/api/v1/app/version" || true)"
  u="$(http_code "$API/api/v1/transactions")"
  vmin="$(grep -o '"min_build":[0-9]*' <<<"$ver" | cut -d: -f2)"
  vlat="$(grep -o '"latest_build":[0-9]*' <<<"$ver" | cut -d: -f2)"
  problems=""
  [[ "$h" == 200 ]] || problems+=" health=$h"
  [[ "$vmin" == "$WANT_MIN" && "$vlat" == "$WANT_LATEST" ]] || problems+=" app/version=$vmin/$vlat"
  [[ "$u" == 401 ]] || problems+=" unauth=$u"
  [[ "$(prod errors)" == 0 ]] || problems+=" log-errors=$(prod errors)"
  if $HAS_BE; then [[ "$(prod label)" == "$BE_SHORT" ]] || problems+=" label=$(prod label)"; fi
  [[ "$(prod migration)" != *DIRTY* ]] || problems+=" migration=$(prod migration)"
  [[ -z "$problems" ]] || die "verify failed:$problems"
  VERIFY="/health 200 · app/version min $vmin latest $vlat · unauthenticated /transactions 401 · app log errors 0 · migration $(prod migration)"
  state_set verify "$VERIFY"
  ok "$VERIFY"
fi

# ── offsite copy ────────────────────────────────────────────────────────
say "offsite copy (dev PC, no restore)"
if $DRY; then
  plan "run NO_RESTORE=1 ppforge-infra/backup/pull-to-dev.sh --no-dump"
else
  NO_RESTORE=1 "$INFRA_DIR/backup/pull-to-dev.sh" --no-dump > "$LOGS/offsite.log" 2>&1 \
    || die "offsite copy failed (see $LOGS/offsite.log)"
  ok "$(tail -1 "$LOGS/offsite.log")"
fi

# ── release entry (main docs tree, NOT committed: Lead commits it) ──────
say "release entry"
REL_ENTRY="$DOCS_DIR/team/releases/$VERSION.md"   # one file per release
make_entry() {
  echo "# $VERSION — released $(date +%F) (DevOps, release.sh from tag $TAG)"
  echo
  echo "> QA pass: $([[ "$QA" == y ]] && echo yes || echo no) · new migrations: $new_migs · \`MIN_APP_BUILD\` $MIN_APP_BUILD"
  echo "> Pre-checks: BE $(state_get checks_be) · app $(state_get checks_app)"
  echo
  if $HAS_BE; then
    echo "- **be** \`$BE_SHORT\` $BE_SUBJ"
    echo "  - live · backup \`$(state_get backup)\`"
  fi
  if $HAS_APP; then
    echo "- **app** \`$APP_SHORT\` $APP_SUBJ"
    echo "  - build **$BUILD** (Firebase $(state_get firebase)) sent to \`firsttester\`"
  fi
  echo "- **prod \`.env\`:** \`LATEST_APP_BUILD=$WANT_LATEST\` · \`MIN_APP_BUILD=$WANT_MIN\` · backup copy \`.env.bak-$VERSION\`"
  echo "- **verify:** ${VERIFY}"
  echo "- tag \`$TAG\`: be \`$BE_SHORT\` · app \`$APP_SHORT\` · docs \`$(short "$DOCS_DIR" "$DOCS_COMMIT")\`"
  echo
  tr -d '\r' < "$BRIEF/entry.md"
}
if [[ -f "$REL_ENTRY" ]]; then
  skip "team/releases/$VERSION.md already exists (delete it to regenerate)"
elif $DRY; then
  plan "write team/releases/$VERSION.md in the main docs tree (uncommitted):"
  make_entry | sed 's/^/      │ /'
else
  mkdir -p "$(dirname "$REL_ENTRY")"
  make_entry > "$REL_ENTRY"
  ok "entry written to team/releases/$VERSION.md (uncommitted: Lead commits it)"
fi

# ── cleanup + summary ───────────────────────────────────────────────────
if $DRY; then
  say "dry-run done — nothing was changed"
  exit 0
fi
if ! $KEEP; then
  say "cleanup"
  if $HAS_BE;  then g "$BE_DIR"  worktree remove --force "$BE_WT"  >/dev/null 2>&1 || true; fi
  if $HAS_APP; then g "$APP_DIR" worktree remove --force "$APP_WT" >/dev/null 2>&1 || true; fi
  rmdir "$WT" 2>/dev/null || true
  ok "worktrees removed"
fi
{
  echo "$VERSION DONE (from tag $TAG)"
  $HAS_BE  && echo "be   $BE_SHORT  $BE_SUBJ"
  $HAS_APP && echo "app  $APP_SHORT  build $BUILD · Firebase $(state_get firebase)"
  echo "docs $(short "$DOCS_DIR" "$DOCS_COMMIT")  entry team/releases/$VERSION.md (uncommitted)"
  echo "checks: BE $(state_get checks_be) · app $(state_get checks_app)"
  echo "backup: $(state_get backup) · migrations new: $new_migs"
  echo "env: LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN (.env.bak-$VERSION)"
  echo "verify: $(state_get verify)"
  echo "offsite: $(tail -1 "$LOGS/offsite.log" 2>/dev/null)"
} | tee "$BRIEF/summary.txt"
