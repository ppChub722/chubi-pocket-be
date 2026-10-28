package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/contacts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/providers"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/recurring"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/users"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func testServices(pool *pgxpool.Pool) Services {
	categoriesService := categories.NewService(categories.NewStore(pool))
	tagsService := tags.NewService(tags.NewStore(pool))
	transactionsService := transactions.NewService(transactions.NewStore(pool), categoriesService)
	categoriesService.WithTransactionCounter(transactionsService.CountByCategory)
	accountsService := accounts.NewService(accounts.NewStore(pool), transactionsService, categoriesService)
	contactsService := contacts.NewService(contacts.NewStore(pool))
	debtsService := personal_debts.NewService(personal_debts.NewStore(pool), transactionsService)
	notificationsService := notifications.NewService(notifications.NewStore(pool))
	projectsService := projects.NewService(projects.NewStore(pool))
	pendingService := pending.NewService(pending.NewStore(pool), transactionsService, tagsService, debtsService, projectsService)
	return Services{
		Accounts:     accountsService,
		Categories:   categoriesService,
		Tags:         tagsService,
		Contacts:     contactsService,
		Debts:        debtsService,
		Projects:     projectsService,
		Recurring:    recurring.NewService(pool, transactionsService, pendingService, notificationsService).WithDebts(debtsService),
		Transactions: transactionsService,
		Providers:    providers.NewStore(pool),
		Users:        users.NewService(users.NewStore(pool)),
	}
}

func syncEngine(t *testing.T, pool *pgxpool.Pool, uid uuid.UUID) (*gin.Engine, *Handler) {
	t.Helper()
	h := NewHandler(pool, quietLog, testServices(pool))
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.POST("/sync", h.Sync)
	}), h
}

type syncReply struct {
	Versions map[string]string `json:"versions"`
	Data     map[string]struct {
		V    string          `json:"v"`
		Body json.RawMessage `json:"body"`
	} `json:"data"`
	Unchanged []string `json:"unchanged"`
	Errors    map[string]struct {
		Code string `json:"code"`
	} `json:"errors"`
}

func doSync(t *testing.T, r http.Handler, req any) (int, syncReply, []byte) {
	t.Helper()
	status, body := apitest.Do(t, r, "POST", "/api/v1/sync", req)
	var out syncReply
	_ = json.Unmarshal(body, &out)
	return status, out, body
}

func fingerprintsOf(t *testing.T, pool *pgxpool.Pool, uid uuid.UUID) map[string]string {
	t.Helper()
	fp, err := Fingerprints(context.Background(), pool, uid, "")
	if err != nil {
		t.Fatalf("fingerprints: %v", err)
	}
	return fp
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func TestSyncSendsEveryDatasetWithoutHave(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	testdb.Account(t, pool, uid)
	r, _ := syncEngine(t, pool, uid)

	status, got, body := doSync(t, r, nil)
	apitest.Conform(t, "postSync", status, body)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	for _, name := range DatasetNames {
		d, ok := got.Data[name]
		if !ok {
			t.Errorf("dataset %s missing from data", name)
			continue
		}
		if d.V != got.Versions[name] {
			t.Errorf("%s: v %q != versions %q", name, d.V, got.Versions[name])
		}
	}
	if len(got.Unchanged) != 0 || len(got.Errors) != 0 {
		t.Errorf("unchanged=%v errors=%v, want none", got.Unchanged, got.Errors)
	}
	for _, key := range keyNames {
		if got.Versions[key] == "" {
			t.Errorf("versions[%s] is empty", key)
		}
	}
}

func TestSyncMatchingHaveIsUnchanged(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r, _ := syncEngine(t, pool, uid)

	_, first, _ := doSync(t, r, nil)
	have := map[string]string{}
	for _, name := range DatasetNames {
		have[name] = first.Data[name].V
	}
	status, second, body := doSync(t, r, map[string]any{"have": have})
	apitest.Conform(t, "postSync", status, body)
	if len(second.Data) != 0 || len(second.Unchanged) != len(DatasetNames) {
		t.Errorf("data=%d unchanged=%v, want every dataset unchanged", len(second.Data), second.Unchanged)
	}

	exec(t, pool, `INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'sync-test')`, uuid.New(), uid)
	_, third, _ := doSync(t, r, map[string]any{"want": []string{"tags", "contacts"}, "have": have})
	if _, ok := third.Data["tags"]; !ok {
		t.Errorf("tags changed but was not sent: %+v", third.Unchanged)
	}
	if len(third.Unchanged) != 1 || third.Unchanged[0] != "contacts" {
		t.Errorf("unchanged = %v, want [contacts]", third.Unchanged)
	}
	if _, ok := third.Data["accounts"]; ok {
		t.Errorf("accounts was not asked for")
	}
}

func TestSyncRejectsUnknownAndDuplicateWant(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r, _ := syncEngine(t, pool, uid)
	for _, want := range [][]string{{"nope"}, {"tags", "tags"}, {"transactions"}} {
		status, _, body := doSync(t, r, map[string]any{"want": want})
		if status != http.StatusBadRequest || !strings.Contains(string(body), "VALIDATION_ERROR") {
			t.Errorf("want %v: status %d body %s, want 400 VALIDATION_ERROR", want, status, body)
		}
	}
}

func TestSyncOneFailingDatasetDoesNotFailTheRequest(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r, h := syncEngine(t, pool, uid)
	h.datasets[Tags] = dataset{load: func(context.Context, uuid.UUID, string) (any, error) {
		return nil, errors.New("boom")
	}}
	h.datasets[Contacts] = dataset{load: func(ctx context.Context, _ uuid.UUID, _ string) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	old := datasetTimeout
	datasetTimeout = 200 * time.Millisecond
	defer func() { datasetTimeout = old }()

	status, got, body := doSync(t, r, nil)
	apitest.Conform(t, "postSync", status, body)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	for _, name := range []string{Tags, Contacts} {
		if got.Errors[name].Code != "INTERNAL_ERROR" {
			t.Errorf("errors[%s] = %+v, want INTERNAL_ERROR", name, got.Errors[name])
		}
		if _, ok := got.Data[name]; ok {
			t.Errorf("%s sent despite failing", name)
		}
	}
	if _, ok := got.Data[Accounts]; !ok {
		t.Errorf("accounts missing: one failing dataset must not stop the rest")
	}
}

// Every change a screen can make to a dataset must move that dataset's fingerprint.
func TestFingerprintsMoveOnEveryChange(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	other := testdb.User(t, pool)

	moved := func(step string, before map[string]string, keys ...string) map[string]string {
		t.Helper()
		after := fingerprintsOf(t, pool, uid)
		for _, k := range keys {
			if before[k] == after[k] {
				t.Errorf("%s: fingerprint of %s did not change (%s)", step, k, after[k])
			}
		}
		return after
	}
	still := func(step string, before, after map[string]string, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if before[k] != after[k] {
				t.Errorf("%s: fingerprint of %s changed without cause", step, k)
			}
		}
	}

	fp := fingerprintsOf(t, pool, uid)
	if again := fingerprintsOf(t, pool, uid); fp["accounts"] != again["accounts"] {
		t.Fatalf("fingerprint is not stable between two calls")
	}

	tagID := uuid.New()
	exec(t, pool, `INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'a')`, tagID, uid)
	next := moved("tag insert", fp, Tags, Recurring, Projects, Transactions)
	still("tag insert", fp, next, Contacts, Providers)
	fp = next
	exec(t, pool, `UPDATE tags SET name = 'b' WHERE id = $1`, tagID)
	fp = moved("tag update", fp, Tags)
	exec(t, pool, `DELETE FROM tags WHERE id = $1`, tagID)
	fp = moved("tag delete", fp, Tags)

	acc := testdb.Account(t, pool, uid)
	fp = moved("wallet insert", fp, Accounts, FormDefaults, Categories)

	// another member of a shared wallet edits it
	exec(t, pool, `INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'member', 'own', NOW())`, uuid.New(), acc, other)
	fp = moved("member joins", fp, Accounts)
	exec(t, pool, `UPDATE accounts SET name = 'renamed by member' WHERE id = $1`, acc)
	fp = moved("member edits the shared wallet", fp, Accounts)
	exec(t, pool, `UPDATE users SET display_name = 'New Name' WHERE id = $1`, other)
	fp = moved("member profile renamed", fp, Accounts)

	exec(t, pool, `INSERT INTO transactions (id, user_id, account_id, type, amount, date)
		VALUES ($1, $2, $3, 'expense', 10, CURRENT_DATE)`, uuid.New(), uid, acc)
	fp = moved("transaction insert", fp, Accounts, Transactions, Categories, FormDefaults)
	exec(t, pool, `UPDATE transactions SET amount = 11 WHERE user_id = $1`, uid)
	fp = moved("transaction update", fp, Accounts, Transactions)
	exec(t, pool, `DELETE FROM transactions WHERE user_id = $1`, uid)
	fp = moved("transaction delete", fp, Accounts, Transactions)

	// a contact linked to another user shows that user's live profile name
	exec(t, pool, `INSERT INTO contacts (id, user_id, display_name, linked_user_id) VALUES ($1, $2, 'Friend', $3)`,
		uuid.New(), uid, other)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM contacts WHERE linked_user_id = $1`, other)
	})
	fp = moved("contact insert", fp, Contacts, DebtPeople)
	exec(t, pool, `UPDATE users SET display_name = 'Renamed Again' WHERE id = $1`, other)
	fp = moved("linked user renamed", fp, Contacts, DebtPeople)

	exec(t, pool, `INSERT INTO pending_transactions (id, user_id, source)
		VALUES ($1, $2, 'manual')`, uuid.New(), uid)
	fp = moved("draft insert", fp, Pending)

	exec(t, pool, `UPDATE payment_providers SET short_name = COALESCE(short_name, '') || '' WHERE id = (SELECT id FROM payment_providers LIMIT 1)`)
	_ = moved("provider update", fp, Providers)

	// the fingerprints of other people do not move ours
	stranger := testdb.User(t, pool)
	before := fingerprintsOf(t, pool, uid)
	exec(t, pool, `INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'not mine')`, uuid.New(), stranger)
	still("someone else's tag", before, fingerprintsOf(t, pool, uid), Tags, Accounts, Transactions)
}

func TestMiddlewareStampsOnlySuccessfulWrites(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("user_id", uid); c.Next() })
	g.Use(Middleware(pool, quietLog))
	g.GET("/thing", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	g.POST("/thing", func(c *gin.Context) { c.JSON(http.StatusCreated, gin.H{"ok": true}) })
	g.DELETE("/thing", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	g.PUT("/thing", func(c *gin.Context) { c.JSON(http.StatusConflict, gin.H{"ok": false}) })
	g.POST("/scan-slip", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}

	if w := call("GET", "/api/v1/thing"); w.Header().Get(VersionsHeader) != "" || w.Code != 200 {
		t.Errorf("GET: header %q status %d, want none / 200", w.Header().Get(VersionsHeader), w.Code)
	}
	w := call("POST", "/api/v1/thing")
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("POST: status %d body %s, want 201 with the handler's body", w.Code, w.Body.String())
	}
	h := w.Header().Get(VersionsHeader)
	for _, key := range keyNames {
		if !strings.Contains(h, key+"=") {
			t.Errorf("header lacks %s: %s", key, h)
		}
	}
	if w := call("DELETE", "/api/v1/thing"); w.Code != http.StatusNoContent || w.Header().Get(VersionsHeader) == "" {
		t.Errorf("DELETE: status %d header %q, want 204 with a header", w.Code, w.Header().Get(VersionsHeader))
	}
	if w := call("PUT", "/api/v1/thing"); w.Code != http.StatusConflict || w.Header().Get(VersionsHeader) != "" {
		t.Errorf("failed PUT: status %d header %q, want 409 without a header", w.Code, w.Header().Get(VersionsHeader))
	}
}

func TestMiddlewareHeaderSeesTheWrite(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("user_id", uid); c.Next() })
	g.Use(Middleware(pool, quietLog))
	g.POST("/tag", func(c *gin.Context) {
		if _, err := pool.Exec(c.Request.Context(), `INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'via-handler')`, uuid.New(), uid); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusCreated, gin.H{})
	})
	before := fingerprintsOf(t, pool, uid)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/tag", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	want := "tags=" + fingerprintsOf(t, pool, uid)["tags"]
	if !strings.Contains(w.Header().Get(VersionsHeader), want) || strings.Contains(w.Header().Get(VersionsHeader), "tags="+before["tags"]+";") {
		t.Errorf("header %q does not carry the post-write tags fingerprint %q", w.Header().Get(VersionsHeader), want)
	}
}
