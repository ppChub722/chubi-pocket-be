# Implementing the API spec v2 (0.3.1-9)

The spec `internal/apidoc/openapi.yaml` is the source of truth. The code conforms to it, not the other way round. Owner rules (2026-10-12): breaking changes are fine (0.3.1-9 forces an update), clean shapes, thin FE.

## Rules for every module
- **Bare bodies.** A single resource is the object itself; a list is `{data, pagination}` (or whatever its schema says). NO `{success, message, data}` envelope on success. Errors keep `response.Fail(...)` → `{success:false, error:{code,message,details}}`.
- **Every key of the schema is always present** (null = none, arrays are `[]`, never null). A key the spec does not list must NOT be sent (the conformance check fails on it).
- **Writes return the final state** (the changed resource with every embedded ref resolved). A write that moves money returns `account_balances` (post-write balances of ALL wallets it touched; `shared.LoadWalletBalances` after commit).
- **Shared building blocks** (use them, do not duplicate): `internal/shared/refs.go` (AccountRef / CategoryRef / EventRef / TagRef / UserRef / PersonRef / MemberRef / WalletBalance / Badges / Page, batch loaders `LoadAccountRefs` … `LoadPersons` + `ResolvePerson`, `PersonNameSQL` / `PersonIconSQL`), `shared/writes.go` (`LoadWalletBalances`, `LoadBadges`), `shared/notification_view.go` (`NotificationView`, `LoadNotificationViews` — the display-ready notification everyone returns). One query per ref kind per page (batch), never per row.
- **Person name rule** (one place: `shared.PersonNameSQL`): nickname when set AND show_nickname → else the linked user's live profile name → else contact.display_name; the stored snapshot (`person_name`) only when there is no contact. A split / debt / draft with a contact always shows the contact-resolved name; a sent `person_name` on a row that has a contact is ignored.
- **Currency**: user-level (`users.currency`), set at registration (default THB), LOCKED. Wallet create/update take no currency: the server sets `accounts.currency` = the user's currency. Do not change other currency handling.
- **No migration** (053 nickname is already applied locally). **No commit, no push, no deploy.** English only in code/comments. `gofmt` only files you touched (the repo is `core.autocrlf=true`; CRLF/LF noise is not a real diff).
- **Do not edit `cmd/api/main.go`** and do not edit packages that are not yours (list in your brief): the lead wires routes. Export your handler methods and report the exact route registrations (method + path + handler).
- **Do not change exported Go signatures other packages call** unless your brief says so; add new functions instead. JSON shape changes happen by changing struct fields / tags; keep a Go field with `json:"-"` when other packages still read it.

## Tests (mandatory, DB-backed)
- `TEST_DATABASE_URL=postgres://chubadmin:admin1234@localhost:5433/chubi_pocket_db?sslmode=disable` (unset = DB tests SKIP silently — always set it). Use `internal/platform/testdb` (Pool, User, Account, …).
- For EVERY operation of your brief: a handler test that builds the real service + handler, drives it with `internal/apidoc/apitest` (`Engine`, `Do`, `Conform(t, "<operationId>", status, body)`), covers the happy path and the main error codes. `Conform` fails on any difference from the spec.
- Also assert behaviour, not just shape (balances, resolved names, order, state changes).
- Run `go vet` and `go test -count=1` on YOUR packages only (other packages may be mid-change by other agents). Never run the whole repo's tests.

## If the spec is wrong or ambiguous
Do not silently deviate. Make the smallest reasonable choice that keeps the spec true, and list it under "Spec questions" in your final report. If a spec change is really needed, say exactly which operationId / schema and what to change; the lead edits the yaml (do not edit `openapi.yaml` yourself).

## Final report (keep it short)
Files changed; operations done (operationIds) with their route registration lines for main.go; tests added; `go vet` / `go test` result for your packages; anything not done; spec questions; doc notes (behaviour the docs in `chubi-pocket-docs/design/` must mention).
