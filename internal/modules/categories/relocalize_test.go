package categories

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func seedLang(t *testing.T, pool *pgxpool.Pool, uid uuid.UUID, lang string) *Store {
	t.Helper()
	ctx := context.Background()
	st := NewStore(pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SeedForUserLangTx(ctx, tx, uid, lang); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func nameOf(t *testing.T, pool *pgxpool.Pool, uid uuid.UUID, key string) (name string, desc *string) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT name, description FROM categories WHERE user_id = $1 AND seed_key = $2`, uid, key).Scan(&name, &desc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return name, desc
}

func TestCatalogKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range seedEntries() {
		if e.Key == "" || seen[e.Key] {
			t.Errorf("key %q is empty or repeated", e.Key)
		}
		seen[e.Key] = true
		for _, lang := range Langs {
			if e.Name[lang] == "" {
				t.Errorf("%s has no %s name", e.Key, lang)
			}
			if e.Description != nil && e.Description[lang] == "" {
				t.Errorf("%s has no %s description", e.Key, lang)
			}
		}
	}
}

func TestRelocalizeFollowsLanguageButKeepsEdits(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	st := seedLang(t, pool, uid, LangEN)

	if n, _ := nameOf(t, pool, uid, "food_drinks"); n != "Food & Drinks" {
		t.Fatalf("seeded name = %q", n)
	}
	// The user renames one starter and rewrites another's description.
	if _, err := pool.Exec(ctx, `UPDATE categories SET name = 'My eats' WHERE user_id = $1 AND seed_key = 'food_drinks'`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE categories SET description = 'mine' WHERE user_id = $1 AND seed_key = 'food_drinks.buffet'`, uid); err != nil {
		t.Fatal(err)
	}

	if err := st.Relocalize(ctx, uid, "th-TH"); err != nil {
		t.Fatalf("relocalize th: %v", err)
	}
	if n, _ := nameOf(t, pool, uid, "food_drinks"); n != "My eats" {
		t.Errorf("edited name = %q, want kept", n)
	}
	n, d := nameOf(t, pool, uid, "food_drinks.buffet")
	if n != "บุฟเฟต์" {
		t.Errorf("buffet name = %q, want Thai", n)
	}
	if d == nil || *d != "mine" {
		t.Errorf("edited description = %v, want kept", d)
	}
	if n, d := nameOf(t, pool, uid, "income.salary"); n != "เงินเดือน" || d == nil || *d != "เงินเดือนจากงานประจำ หรือรายรับหลัก" {
		t.Errorf("salary = %q / %v, want Thai", n, d)
	}

	if err := st.Relocalize(ctx, uid, "en"); err != nil {
		t.Fatalf("relocalize en: %v", err)
	}
	if n, _ := nameOf(t, pool, uid, "income.salary"); n != "Salary" {
		t.Errorf("salary back = %q, want Salary", n)
	}
	if n, _ := nameOf(t, pool, uid, "food_drinks"); n != "My eats" {
		t.Errorf("edited name after round trip = %q, want kept", n)
	}

	// System rows are never keyed, so never renamed.
	var sys int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE user_id = $1 AND is_system AND seed_key IS NOT NULL`, uid).Scan(&sys)
	if sys != 0 {
		t.Errorf("%d system rows carry a seed_key", sys)
	}
}

func TestRelocalizeSkipsSiblingNameClash(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	st := seedLang(t, pool, uid, LangEN)

	// The user already has a root called "เงินเดือน" (the Thai Salary name) beside the starter.
	if _, err := pool.Exec(ctx, `
		INSERT INTO categories (id, user_id, name, type, is_system) VALUES ($1, $2, 'เงินเดือน', 'income', false)`,
		uuid.New(), uid); err != nil {
		t.Fatal(err)
	}
	if err := st.Relocalize(ctx, uid, LangTH); err != nil {
		t.Fatalf("relocalize must not fail on a clash: %v", err)
	}
	if n, _ := nameOf(t, pool, uid, "income.salary"); n != "Salary" {
		t.Errorf("clashing starter = %q, want left as Salary", n)
	}
}

func TestBackfillSeedKeys(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	st := seedLang(t, pool, uid, LangTH)

	// Simulate rows seeded before the column existed, plus one the user renamed.
	if _, err := pool.Exec(ctx, `UPDATE categories SET seed_key = NULL WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE categories SET name = 'Mine' WHERE user_id = $1 AND name = 'เงินเดือน'`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackfillSeedKeys(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var keyed, salary int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE user_id = $1 AND seed_key IS NOT NULL`, uid).Scan(&keyed)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM categories WHERE user_id = $1 AND seed_key = 'income.salary'`, uid).Scan(&salary)
	if want := len(seedEntries()) - 1; keyed != want || salary != 0 {
		t.Errorf("keyed %d (want %d), renamed starter keyed %d (want 0)", keyed, want, salary)
	}
	if n, err := st.BackfillSeedKeys(ctx); err != nil || n != 0 {
		t.Errorf("second run = %d, %v; want 0 (idempotent)", n, err)
	}
	if n, _ := nameOf(t, pool, uid, "food_drinks.groceries"); n != "ของสดและวัตถุดิบ" {
		t.Errorf("child keyed under its parent: name = %q", n)
	}
}
