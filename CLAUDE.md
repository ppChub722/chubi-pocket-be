# CLAUDE.md

## Running tests

Test output is expensive in tokens (a bare `flutter test` prints ~560 KB). Follow these rules:

1. **Do not run tests by default.** After making or changing something, do not run any tests, including `flutter test`, `go test` and `npm test`. Ask first: "Run tests? (changed files only / everything / skip)". Run them only when the user says so. If the user does not mention tests, skip them.
2. **Scope follows the answer.** "Changed files only" means just the test files or folders related to what was edited. "Everything" means the full suite.
3. **Never read the full output.** Redirect it to a file and read only the summary and the failures:
   - Flutter: `flutter test -r failures-only <paths> > $TEMP/t.log 2>&1; tail -n 1 $TEMP/t.log; grep '\[E\]' $TEMP/t.log | cut -c1-200 | head -n 20`
   - Go: `go test -count=1 <pkgs> > $TEMP/t.log 2>&1; grep -E '^(--- FAIL|FAIL|ok)' $TEMP/t.log | grep -v 'no test files' | cut -c1-200 | head -n 20`
4. **If everything passes**, reply "tests passed" and stop.
5. **If something fails, do not fix it.** Report which tests failed and why in one line each, and propose how to fix it. Wait for the user to approve before changing any code. Open the log file only for the failing tests, only when needed to explain the cause.
6. **Go DB tests** skip silently without `TEST_DATABASE_URL`. If a run is meant to be a full check, say whether the DB tests actually ran.
7. **Ask before every re-run**, including after an approved fix. Re-run only the test files that failed, with the same output rules.
8. **Fix loop only on explicit request.** If the user says "fix until it passes" (or similar), you may fix and re-run without asking, but stop after 3 fix-and-run loops at most. If it still fails after 3, report what is still failing and why, and wait for the user.

## Restarting / rebuilding the backend

Do not rebuild or restart the backend (`docker compose up -d --build`, `docker restart`, `go run`) on your own after a change. The owner will say when. After a BE change, just say it needs a rebuild and wait.
