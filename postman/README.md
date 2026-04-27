# Postman collection

Local-dev API collection for `chubi-pocket-be`.

## Files

- [`chubi-pocket-be.postman_collection.json`](chubi-pocket-be.postman_collection.json) — request collection (Postman v2.1 schema)
- [`chubi-pocket-be.postman_environment.json`](chubi-pocket-be.postman_environment.json) — local-dev environment (`baseUrl`, `token`, `userId`, `categoryId`, `tagId`)

## Import

In Postman: **File → Import** → drop both JSON files in. The environment shows up in the top-right environment selector — pick **ChubiPocket BE — local**.

## Use

1. **Health → GET /health** to verify the backend is up
2. **Auth → Register** (or **Login** if user exists) — the test script auto-populates `{{token}}` so every other request just works
3. **Categories → Create** populates `{{categoryId}}`; **Tags → Create** populates `{{tagId}}` — the per-id GET/PUT/DELETE requests pick those up automatically

The collection uses bearer auth at the collection level — every request inherits `Authorization: Bearer {{token}}` except the few that opt out (Health, Register, Login).

## Phase coverage

- ✅ Phase 0: auth + users
- ✅ Phase 1a.1: categories + tags (standalone)
- ✅ Phase 1a.2: accounts (CRUD, archive, adjust-balance, summary)
- ✅ Phase 1a.3: transactions (CRUD, list, summary, transfer pairing) + tag attach/detach
- 🚧 Phase 1b: contacts, projects, splits, personal_debts, notifications

## Suggested smoke flow

1. **Health → GET /health**
2. **Auth → Register**
3. **Categories → List** (verify ~33 starter + 6 system rows seeded)
4. **Accounts → Create (cash, no opening)** + **Create (bank, opening 15000)**
5. **Accounts → Get by id** on the bank account → balance is 15000 (opening transaction created it)
6. **Transactions → List** → 1 row (the opening balance) for the bank account
7. **Transactions → Create expense** (250 from bank) → bank balance now 14750
8. **Transactions → Create transfer** (1000 bank → cash) → bank 13750, cash 1000
9. **Accounts → Adjust balance** (set bank to 18000) → adjustment transaction recorded
10. **Tags → Create** + **Transactions → Attach tags** + **Detach tag**
11. **Transactions → Summary** with `group_by=category`
