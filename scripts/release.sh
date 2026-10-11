#!/usr/bin/env bash
# One release, end to end, from a brief folder written by Lead
# (template + format: chubi-pocket-docs/ai/templates/release-brief/).
#
#   ./scripts/release.sh --dry-run <brief-dir>   # read-only: preflight + the plan
#   ./scripts/release.sh <brief-dir>             # do it
#
# Steps: preflight → checks → bump pubspec → commits → push → deploy BE →
# APK → prod .env → verify → offsite backup copy → docs entry + commit →
# tags → summary.
#
# Stops at the first problem. Safe to re-run after a failure: every step
# first looks at whether it is already done (release commit by subject,
# prod image label, LATEST_APP_BUILD, tags, the docs entry) and skips it.
# Facts it can't re-read (check results, APK upload, backup name) live in
# <brief-dir>/.state; logs in <brief-dir>/logs/; the final summary in
# <brief-dir>/summary.txt.
#
# Run from Git Bash on the dev machine (any cwd).
set -euo pipefail

VPS="Admin@160.238.13.137"
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

# ── args ────────────────────────────────────────────────────────────────
DRY=false
BRIEF=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY=true ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) BRIEF="$1" ;;
  esac
  shift
done
[[ -n "$BRIEF" && -d "$BRIEF" ]] || { echo "usage: release.sh [--dry-run] <brief-dir>" >&2; exit 2; }
BRIEF="$(cd "$BRIEF" && pwd)"
# State and logs are written into the brief folder: inside a repo they would
# end up in a release commit (`git add -A`).
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

dirty_count() { g "$1" status --porcelain --untracked-files=all | wc -l | tr -d ' '; }
short() { g "$1" rev-parse --short "$2"; }
# The commit whose subject is exactly $2 (newest 100), or nothing.
find_commit() {
  local h s
  while IFS=$'\t' read -r h s; do
    [[ "$s" == "$2" ]] && { echo "$h"; return 0; }
  done < <(g "$1" log -n 100 --format='%H%x09%s')
  return 0
}

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
VERSION="" EXPECT_MIGRATIONS="" MIN_APP_BUILD="" BE_PATHS="" APP_PATHS="" DOCS_PATHS="" QA=""
while IFS= read -r line || [[ -n "$line" ]]; do
  line="${line%$'\r'}"
  [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
  [[ "$line" =~ ^(VERSION|EXPECT_MIGRATIONS|MIN_APP_BUILD|BE_PATHS|APP_PATHS|DOCS_PATHS|QA)=(.*)$ ]] \
    || die "brief.env: unknown line: $line"
  val="${BASH_REMATCH[2]}"; val="${val%\"}"; val="${val#\"}"
  printf -v "${BASH_REMATCH[1]}" '%s' "$val"
done < "$BRIEF/brief.env"

[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9]+)?$ ]] || die "VERSION '$VERSION' is not like 0.3.1-8"
[[ "$EXPECT_MIGRATIONS" == none || "$EXPECT_MIGRATIONS" =~ ^[0-9]+( [0-9]+)*$ ]] \
  || die "EXPECT_MIGRATIONS must be 'none' or numbers like '53 54'"
[[ "$MIN_APP_BUILD" == unchanged || "$MIN_APP_BUILD" =~ ^[0-9]+$ ]] \
  || die "MIN_APP_BUILD must be 'unchanged' or a build number"
for k in BE_PATHS APP_PATHS DOCS_PATHS; do
  [[ "${!k}" == any || "${!k}" =~ ^[0-9]+$ ]] || die "$k must be a number or 'any'"
done
[[ "$QA" == y || "$QA" == n ]] || die "QA must be y or n"

msg_subject() { [[ -f "$BRIEF/$1" ]] && head -1 "$BRIEF/$1" | tr -d '\r' || true; }
# Commit messages without CRs (the brief may be written on Windows).
msg_file() { tr -d '\r' < "$BRIEF/$1" > "$LOGS/$1"; echo "$LOGS/$1"; }
BE_SUBJ="$(msg_subject be.msg)"
APP_SUBJ="$(msg_subject app.msg)"
DOCS_SUBJ="$(msg_subject docs.msg)"
[[ -n "$DOCS_SUBJ" ]] || die "docs.msg missing or empty"
[[ -f "$BRIEF/entry.md" ]] || die "entry.md missing"
# A repo with BE_PATHS / APP_PATHS = 0 gets no release commit, deploy or tag.
HAS_BE=true;  [[ "$BE_PATHS" == 0 ]] && HAS_BE=false
HAS_APP=true; [[ "$APP_PATHS" == 0 ]] && HAS_APP=false
$HAS_BE  && [[ -z "$BE_SUBJ" ]]  && die "be.msg missing or empty (BE_PATHS=$BE_PATHS)"
$HAS_APP && [[ -z "$APP_SUBJ" ]] && die "app.msg missing or empty (APP_PATHS=$APP_PATHS)"
ok "version $VERSION · migrations $EXPECT_MIGRATIONS · MIN $MIN_APP_BUILD · QA $QA"

# ── preflight ───────────────────────────────────────────────────────────
say "preflight"
for d in "$BE_DIR" "$APP_DIR" "$DOCS_DIR"; do
  name="$(basename "$d")"
  [[ "$(g "$d" rev-parse --abbrev-ref HEAD)" == main ]] || die "$name is not on main"
  g "$d" fetch -q origin
  behind="$(g "$d" rev-list --count HEAD..origin/main)"
  [[ "$behind" == 0 ]] || die "$name is $behind commit(s) behind origin/main"
done

BE_REL=""; APP_REL=""; DOCS_REL=""
$HAS_BE  && BE_REL="$(find_commit "$BE_DIR" "$BE_SUBJ")"
$HAS_APP && APP_REL="$(find_commit "$APP_DIR" "$APP_SUBJ")"
DOCS_REL="$(find_commit "$DOCS_DIR" "$DOCS_SUBJ")"

check_paths() { # repo-dir expected release-commit
  local name n; name="$(basename "$1")"
  if [[ -n "$3" ]]; then skip "$name: release commit $(short "$1" "$3") already made"; return; fi
  n="$(dirty_count "$1")"
  if [[ "$2" == any ]]; then ok "$name: $n changed paths (not checked)"
  elif [[ "$n" == "$2" ]]; then ok "$name: $n changed paths, as briefed"
  else die "$name has $n changed paths, the brief says $2 (git status --porcelain --untracked-files=all)"
  fi
}
$HAS_BE  && check_paths "$BE_DIR" "$BE_PATHS" "$BE_REL"
$HAS_APP && check_paths "$APP_DIR" "$APP_PATHS" "$APP_REL"
check_paths "$DOCS_DIR" "$DOCS_PATHS" "$DOCS_REL"

PROD="$(read_prod)" || die "cannot reach the VPS over ssh"
[[ "$(http_code "$API/health")" == 200 ]] || die "prod /health is not 200"
ok "prod: BE $(prod label) · migration $(prod migration) · LATEST $(prod LATEST_APP_BUILD) · MIN $(prod MIN_APP_BUILD) · health 200"
[[ "$(prod migration)" != *DIRTY* ]] || die "prod migration state is DIRTY"

# What the BE release ships: its release commit, or HEAD (made from the tree).
if $HAS_BE; then BE_TARGET="${BE_REL:-}"; else BE_TARGET="$(g "$BE_DIR" rev-parse HEAD)"; fi
BE_DEPLOYED=false
[[ -n "$BE_TARGET" && "$(prod label)" == "$(short "$BE_DIR" "$BE_TARGET")" ]] && BE_DEPLOYED=true

# New migrations = files in the BE tree that prod doesn't have yet.
tree_migs="$(ls -1 "$BE_DIR/migrations" | sed -n 's/^0*\([0-9][0-9]*\)_.*\.up\.sql$/\1/p')"
new_migs=""
for m in $tree_migs; do
  [[ " $(prod migration_files) " == *" $m "* ]] || new_migs="$new_migs $m"
done
new_migs="${new_migs# }"; [[ -n "$new_migs" ]] || new_migs=none
if $BE_DEPLOYED; then
  skip "migrations: BE already deployed (prod at $(prod migration))"
elif [[ "$new_migs" == "$EXPECT_MIGRATIONS" ]]; then
  ok "new migrations: $new_migs (as briefed)"
else
  die "new migrations in the tree: '$new_migs', the brief expects '$EXPECT_MIGRATIONS'"
fi
if ! $HAS_BE && ! $BE_DEPLOYED; then
  die "the brief has no BE changes, but prod runs $(prod label) and BE HEAD is $(short "$BE_DIR" HEAD)"
fi

# Build = app commit count of the release commit.
if $HAS_APP; then
  if [[ -n "$APP_REL" ]]; then BUILD="$(g "$APP_DIR" rev-list --count "$APP_REL")"
  else BUILD="$(( $(g "$APP_DIR" rev-list --count HEAD) + 1 ))"; fi
  ok "app build will be $BUILD (Firebase $((2000 + BUILD)))"
fi
WANT_MIN="$(prod MIN_APP_BUILD)"; [[ "$MIN_APP_BUILD" == unchanged ]] || WANT_MIN="$MIN_APP_BUILD"

# ── checks ──────────────────────────────────────────────────────────────
say "checks"
need_be_checks=false;  $HAS_BE  && [[ -z "$BE_REL" ]]  && need_be_checks=true
need_app_checks=false; $HAS_APP && [[ -z "$APP_REL" ]] && need_app_checks=true

be_checks() {
  cd "$BE_DIR"
  go vet ./... || { echo "CHECK go vet failed"; return 1; }
  local rc=0
  TEST_DATABASE_URL="$TEST_DB" go test -count=1 -v ./... || rc=$?
  return $rc
}
app_checks() {
  cd "$APP_DIR"
  "$FLUTTER" analyze || return 1
  "$FLUTTER" test
}

if ! $need_be_checks && ! $need_app_checks; then
  skip "release commits already made — checks ran before them"
  [[ -n "$(state_get checks_be)$(state_get checks_app)" ]] \
    && skip "BE $(state_get checks_be) · app $(state_get checks_app)"
else
  if $need_be_checks; then
    # DB tests SKIP (not fail) when the DB is down or behind, so check it first.
    newest="$(printf '%s\n' $tree_migs | sort -n | tail -1)"
    local_ver="$(MSYS_NO_PATHCONV=1 docker exec chubi_pocket_db psql -U chubadmin -d chubi_pocket_db -Atc \
      "SELECT version FROM schema_migrations WHERE NOT dirty" 2>/dev/null || true)"
    [[ "$local_ver" == "$newest" ]] \
      || die "local test DB (:5433) is at '${local_ver:-unreachable}', the tree's newest migration is $newest — run scripts/local-deploy.sh first"
    ok "local test DB at migration $local_ver"
  fi
  if $DRY; then
    $need_be_checks  && plan "run BE: go vet ./... + go test -count=1 -v ./... (TEST_DATABASE_URL :5433), require 0 failed / 0 skipped"
    $need_app_checks && plan "run app: flutter analyze + flutter test"
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
      ok "BE: $(state_get checks_be)"
    fi
    if $need_app_checks; then
      l="$LOGS/checks-app.log"
      [[ " $failed " != *" app "* ]] || { tail -15 "$l" >&2; die "app checks failed (see $l)"; }
      grep -aq 'No issues found!' "$l" || die "flutter analyze reported issues (see $l)"
      n="$(grep -ao '+[0-9]*[^:]*: All tests passed' "$l" | tail -1 | grep -o '[0-9]*' | head -1)"
      state_set checks_app "analyze clean, flutter test ${n:-?} passed"
      ok "app: $(state_get checks_app)"
    fi
  fi
fi

# ── commits ─────────────────────────────────────────────────────────────
say "commits"
if $HAS_APP; then
  if [[ -n "$APP_REL" ]]; then
    skip "app: $(short "$APP_DIR" "$APP_REL") $APP_SUBJ"
  else
    num="${VERSION##*-}"; [[ "$VERSION" == *-* ]] || num=0
    cur="$(grep -m1 '^version:' "$APP_DIR/pubspec.yaml" | tr -d '\r')"
    want="version: $VERSION+$num"
    if $DRY; then
      [[ "$cur" == "$want" ]] || plan "bump pubspec '$cur' → '$want'"
      plan "commit app ($(dirty_count "$APP_DIR") paths): $APP_SUBJ"
    else
      [[ "$cur" == "$want" ]] || sed -i "s/^version: .*/$want/" "$APP_DIR/pubspec.yaml"
      g "$APP_DIR" add -A
      g "$APP_DIR" commit -q -F "$(msg_file app.msg)"
      APP_REL="$(g "$APP_DIR" rev-parse HEAD)"
      ok "app: $(short "$APP_DIR" "$APP_REL") (build $(g "$APP_DIR" rev-list --count HEAD))"
    fi
  fi
fi
if $HAS_BE; then
  if [[ -n "$BE_REL" ]]; then
    skip "be: $(short "$BE_DIR" "$BE_REL") $BE_SUBJ"
  elif $DRY; then
    plan "commit be ($(dirty_count "$BE_DIR") paths): $BE_SUBJ"
  else
    g "$BE_DIR" add -A
    g "$BE_DIR" commit -q -F "$(msg_file be.msg)"
    BE_REL="$(g "$BE_DIR" rev-parse HEAD)"; BE_TARGET="$BE_REL"
    ok "be: $(short "$BE_DIR" "$BE_REL")"
  fi
fi

# ── push ────────────────────────────────────────────────────────────────
say "push"
push_repo() {
  local name ahead; name="$(basename "$1")"
  if $DRY && [[ -z "$2" ]]; then plan "push $name (after its commit)"; return; fi
  ahead="$(g "$1" rev-list --count origin/main..HEAD)"
  if [[ "$ahead" == 0 ]]; then skip "$name: in sync with origin"
  elif $DRY; then plan "push $name ($ahead commit(s))"
  else g "$1" push -q origin main; ok "$name: pushed $ahead commit(s)"; fi
}
$HAS_BE  && push_repo "$BE_DIR" "$BE_REL"
$HAS_APP && push_repo "$APP_DIR" "$APP_REL"

# ── deploy BE ───────────────────────────────────────────────────────────
say "deploy BE"
if $BE_DEPLOYED; then
  skip "prod already runs $(prod label)"
  [[ -n "$(state_get backup)" ]] || state_set backup "$(prod dump) (newest; deploy ran earlier)"
elif $DRY; then
  plan "run deploy.sh --skip-apk (backup → migrate → swap → health; new migrations: $new_migs)"
else
  [[ "$(g "$BE_DIR" rev-parse HEAD)" == "$BE_TARGET" ]] \
    || die "BE HEAD is not the release commit $(short "$BE_DIR" "$BE_TARGET") — deploy.sh would ship the wrong commit"
  "$BE_DIR/scripts/deploy.sh" --skip-apk 2>&1 | tee "$LOGS/deploy.log" \
    || die "deploy.sh failed (see $LOGS/deploy.log)"
  dump="$(sed -n 's/.*latest dump: //p' "$LOGS/deploy.log" | tail -1)"
  state_set backup "$dump"
  ok "BE live: $(short "$BE_DIR" "$BE_TARGET") · backup $dump"
fi

# ── APK ─────────────────────────────────────────────────────────────────
say "APK"
if ! $HAS_APP; then
  skip "no app changes in this release"
elif [[ "$(state_get apk)" == "$BUILD" ]]; then
  skip "build $BUILD already uploaded (Firebase $(state_get firebase))"
elif (( $(prod LATEST_APP_BUILD) >= BUILD )); then
  # LATEST is only raised after the upload, so the APK went out earlier.
  skip "prod LATEST_APP_BUILD=$(prod LATEST_APP_BUILD) ≥ $BUILD — APK uploaded in an earlier run"
  [[ -n "$(state_get firebase)" ]] || state_set firebase "$((2000 + BUILD))"
elif $DRY; then
  plan "run release-apk.sh → build $BUILD to firsttester (Firebase $((2000 + BUILD)))"
else
  [[ "$(g "$APP_DIR" rev-parse HEAD)" == "$APP_REL" ]] \
    || die "app HEAD is not the release commit — release-apk.sh would build the wrong tree"
  "$APP_DIR/scripts/release-apk.sh" 2>&1 | tee "$LOGS/apk.log" \
    || die "release-apk.sh failed (see $LOGS/apk.log)"
  fb="$(grep -ao 'uploaded new release [^ ]* ([0-9]*)' "$LOGS/apk.log" | grep -o '([0-9]*)' | tr -d '()' | tail -1)"
  state_set apk "$BUILD"
  state_set firebase "${fb:-?}"
  ok "APK build $BUILD uploaded (Firebase ${fb:-?})"
fi

# ── prod .env ───────────────────────────────────────────────────────────
say "prod .env"
WANT_LATEST="$(prod LATEST_APP_BUILD)"; $HAS_APP && WANT_LATEST="$BUILD"
if [[ "$(prod LATEST_APP_BUILD)" == "$WANT_LATEST" && "$(prod MIN_APP_BUILD)" == "$WANT_MIN" ]]; then
  skip "already LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN"
elif $DRY; then
  plan "back up .env → .env.bak-$VERSION; set LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN; recreate app"
else
  if ! ssh_vps "V='$VERSION' L='$WANT_LATEST' M='$WANT_MIN' bash -s" <<'REMOTE' 2>&1 | tee "$LOGS/env.log"; then
set -euo pipefail
cd /srv/chubi
grep -q '^LATEST_APP_BUILD=' .env && grep -q '^MIN_APP_BUILD=' .env || { echo "keys missing in .env"; exit 1; }
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
  plan "check /health 200 · app/version min $WANT_MIN latest $WANT_LATEST · unauthenticated /transactions 401 · app log errors 0 · prod label = BE release"
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
  [[ "$(prod label)" == "$(short "$BE_DIR" "$BE_TARGET")" ]] || problems+=" label=$(prod label)"
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

# ── docs entry + commit ─────────────────────────────────────────────────
say "docs"
REL_ENTRY="$DOCS_DIR/ai/releases/$VERSION.md"   # one file per release; CURRENT-WORK.md is frozen
make_entry() {
  local be_s app_s
  be_s="$( [[ -n "$BE_REL" ]] && short "$BE_DIR" "$BE_REL" || echo '<be>')"
  app_s="$( [[ -n "$APP_REL" ]] && short "$APP_DIR" "$APP_REL" || echo '<app>')"
  echo "# $VERSION — released $(date +%F) (DevOps, release.sh)"
  echo
  echo "> QA pass: $([[ "$QA" == y ]] && echo yes || echo no) · new migrations: $new_migs · \`MIN_APP_BUILD\` $MIN_APP_BUILD"
  echo "> Pre-checks: BE $(state_get checks_be) · app $(state_get checks_app)"
  echo
  if $HAS_BE; then
    echo "- **be** \`$be_s\` $BE_SUBJ"
    echo "  - live · backup \`$(state_get backup)\`"
  fi
  if $HAS_APP; then
    echo "- **app** \`$app_s\` $APP_SUBJ"
    echo "  - build **$BUILD** (Firebase $(state_get firebase)) sent to \`firsttester\`"
  fi
  echo "- **prod \`.env\`:** \`LATEST_APP_BUILD=$WANT_LATEST\` · \`MIN_APP_BUILD=$WANT_MIN\` · backup copy \`.env.bak-$VERSION\`"
  echo "- **verify:** ${VERIFY}"
  printf -- '- tag `v%s`:' "$VERSION"
  $HAS_APP && printf ' app `%s` ·' "$app_s"
  $HAS_BE && printf ' be `%s` ·' "$be_s"
  echo ' docs (this commit)'
  echo
  tr -d '\r' < "$BRIEF/entry.md"
}
if [[ -n "$DOCS_REL" ]]; then
  skip "docs: $(short "$DOCS_DIR" "$DOCS_REL") $DOCS_SUBJ"
else
  if [[ -f "$REL_ENTRY" ]]; then
    skip "ai/releases/$VERSION.md already exists"
  elif $DRY; then
    plan "write ai/releases/$VERSION.md:"
    make_entry | sed 's/^/      │ /'
  else
    mkdir -p "$(dirname "$REL_ENTRY")"
    make_entry > "$REL_ENTRY"
    ok "entry written to ai/releases/$VERSION.md"
  fi
  if $DRY; then
    plan "commit docs ($(dirty_count "$DOCS_DIR") paths + the entry): $DOCS_SUBJ"
  else
    g "$DOCS_DIR" add -A
    g "$DOCS_DIR" commit -q -F "$(msg_file docs.msg)"
    DOCS_REL="$(g "$DOCS_DIR" rev-parse HEAD)"
    ok "docs: $(short "$DOCS_DIR" "$DOCS_REL")"
  fi
fi
push_repo "$DOCS_DIR" "$DOCS_REL"

# ── tags ────────────────────────────────────────────────────────────────
say "tags v$VERSION"
tag_repo() { # dir release-commit
  local name t; name="$(basename "$1")"; t="v$VERSION"
  if [[ -z "$2" ]]; then $DRY && plan "tag $name (after its commit)"; return; fi
  if g "$1" rev-parse -q --verify "refs/tags/$t" >/dev/null; then
    [[ "$(g "$1" rev-parse "$t^{}")" == "$2" ]] \
      || die "$name: tag $t exists but points at $(short "$1" "$t^{}"), not the release commit $(short "$1" "$2")"
    skip "$name: $t → $(short "$1" "$2")"
  elif $DRY; then plan "tag $name $t → $(short "$1" "$2")"; return
  else g "$1" tag -a "$t" -m "$VERSION" "$2"; ok "$name: $t → $(short "$1" "$2")"; fi
  if [[ -n "$(g "$1" ls-remote --tags origin "refs/tags/$t")" ]]; then skip "$name: $t on origin"
  elif $DRY; then plan "push tag $t ($name)"
  else g "$1" push -q origin "$t"; ok "$name: pushed $t"; fi
}
$HAS_APP && tag_repo "$APP_DIR" "$APP_REL"
$HAS_BE  && tag_repo "$BE_DIR" "$BE_REL"
tag_repo "$DOCS_DIR" "$DOCS_REL"

# ── summary ─────────────────────────────────────────────────────────────
if $DRY; then
  say "dry-run done — nothing was changed"
  exit 0
fi
{
  echo "$VERSION DONE"
  $HAS_BE  && echo "be   $(short "$BE_DIR" "$BE_REL")  $BE_SUBJ"
  $HAS_APP && echo "app  $(short "$APP_DIR" "$APP_REL")  build $BUILD · Firebase $(state_get firebase)"
  echo "docs $(short "$DOCS_DIR" "$DOCS_REL")  tags v$VERSION pushed"
  echo "checks: BE $(state_get checks_be) · app $(state_get checks_app)"
  echo "backup: $(state_get backup) · migrations new: $new_migs"
  echo "env: LATEST_APP_BUILD=$WANT_LATEST · MIN_APP_BUILD=$WANT_MIN (.env.bak-$VERSION)"
  echo "verify: $(state_get verify)"
  echo "offsite: $(tail -1 "$LOGS/offsite.log" 2>/dev/null)"
} | tee "$BRIEF/summary.txt"
