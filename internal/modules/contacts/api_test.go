package contacts

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type cJSON struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Nickname     *string   `json:"nickname"`
	ShowNickname bool      `json:"show_nickname"`
	DisplayName  string    `json:"display_name"`
	Email        *string   `json:"email"`
	Status       string    `json:"status"`
	Avatar       struct {
		Kind     string `json:"kind"`
		IconCode *struct {
			Icon *string `json:"icon"`
		} `json:"icon_code"`
	} `json:"avatar"`
	LinkedUser *struct {
		ID          uuid.UUID `json:"id"`
		DisplayName string    `json:"display_name"`
		Email       *string   `json:"email"`
	} `json:"linked_user"`
}

type contactEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *Service
	h    *Handler
}

func newContactEnv(t *testing.T) *contactEnv {
	t.Helper()
	pool := testdb.Pool(t)
	svc := NewService(NewStore(pool))
	svc.WithNotificationService(notifications.NewService(notifications.NewStore(pool)))
	svc.WithSplitsAbsorber(func(_ context.Context, _, _ uuid.UUID, names []string) (int, error) { return len(names), nil })
	svc.WithSplitsRestorer(func(_ context.Context, _ pgx.Tx, _, _ uuid.UUID) (int, error) { return 3, nil })
	svc.WithUnlinkedNamesProvider(func(_ context.Context, _ uuid.UUID) ([]UnlinkedName, error) {
		return []UnlinkedName{{Name: "Somchai", Count: 2}}, nil
	})
	return &contactEnv{t: t, pool: pool, svc: svc, h: NewHandler(svc)}
}

func (e *contactEnv) as(uid uuid.UUID) *gin.Engine {
	h := e.h
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/contacts", h.List)
		g.POST("/contacts", h.Create)
		g.GET("/contacts/unlinked-names", h.UnlinkedNames)
		g.GET("/contacts/link-requests/:notification_id/sender-profile", h.SenderProfile)
		g.POST("/contacts/link-requests/:notification_id/accept", h.AcceptLinkRequest)
		g.POST("/contacts/link-requests/:notification_id/reject", h.RejectLinkRequest)
		g.POST("/contacts/link-requests/:notification_id/create-linked-contact", h.CreateLinkedContact)
		g.POST("/contacts/link-requests/:notification_id/link-existing-contact/:contact_id", h.LinkExistingContact)
		g.GET("/contacts/:id", h.Get)
		g.PUT("/contacts/:id", h.Update)
		g.DELETE("/contacts/:id", h.Delete)
		g.POST("/contacts/:id/archive", h.Archive)
		g.POST("/contacts/:id/restore", h.Restore)
		g.POST("/contacts/:id/request-link", h.RequestLink)
		g.POST("/contacts/:id/unlink", h.Unlink)
		g.POST("/contacts/:id/absorb", h.Absorb)
	})
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func (e *contactEnv) create(r *gin.Engine, body map[string]any) cJSON {
	e.t.Helper()
	status, raw := apitest.Do(e.t, r, "POST", "/api/v1/contacts", body)
	apitest.Conform(e.t, "createContact", status, raw)
	if status != http.StatusCreated {
		e.t.Fatalf("create %v: %d %s", body, status, raw)
	}
	return decode[struct {
		Contact cJSON `json:"contact"`
	}](e.t, raw).Contact
}

func TestContactAPI(t *testing.T) {
	e := newContactEnv(t)
	ctx := context.Background()
	uid := testdb.User(t, e.pool)
	friend := testdb.User(t, e.pool)
	if _, err := e.pool.Exec(ctx, `UPDATE users SET display_name = 'Friend Profile',
		icon_code = '{"icon":"star","iconColors":[],"bgColors":[],"borderColors":[]}'::jsonb WHERE id = $1`, friend); err != nil {
		t.Fatal(err)
	}
	r := e.as(uid)

	ann := e.create(r, map[string]any{"display_name": "Ann Real", "nickname": "  Annie ", "email": "ann@example.com"})
	if ann.Name != "Annie" || !ann.ShowNickname || ann.DisplayName != "Ann Real" || ann.Nickname == nil || *ann.Nickname != "Annie" {
		t.Errorf("created = %+v, want name Annie (trimmed), show_nickname true", ann)
	}
	bob := e.create(r, map[string]any{"display_name": "bob", "nickname": "", "show_nickname": false})
	if bob.Name != "bob" || bob.Nickname != nil || bob.ShowNickname {
		t.Errorf("bob = %+v, want empty nickname stored as null, show false, name bob", bob)
	}
	cat := e.create(r, map[string]any{"display_name": "Cat", "nickname": "Zed", "show_nickname": false, "absorb_names": []string{"Cat", "Kitty"}})
	_, raw := apitest.Do(t, r, "POST", "/api/v1/contacts", map[string]any{"display_name": "Dan", "absorb_names": []string{"Dan"}})
	if got := decode[struct {
		Absorbed int `json:"absorbed_count"`
	}](t, raw); got.Absorbed != 1 {
		t.Errorf("absorbed_count = %d, want 1", got.Absorbed)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/contacts", map[string]any{"display_name": "Long", "nickname": strings.Repeat("n", 101)}); status != http.StatusBadRequest {
		t.Errorf("nickname > 100 status = %d, want 400", status)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/contacts", map[string]any{"display_name": ""}); status != http.StatusBadRequest {
		t.Errorf("empty display_name status = %d, want 400", status)
	}

	// The delete preview answers (it once queried a dropped column and 500'd).
	if status, raw := apitest.Do(t, r, "GET", "/api/v1/contacts/"+ann.ID.String()+"?include=delete_preview", nil); status != http.StatusOK {
		t.Errorf("delete preview status = %d, want 200: %s", status, raw)
	} else if !strings.Contains(string(raw), `"delete_preview"`) {
		t.Errorf("delete preview missing: %s", raw)
	}

	put := func(id uuid.UUID, body map[string]any) cJSON {
		t.Helper()
		status, raw := apitest.Do(t, r, "PUT", "/api/v1/contacts/"+id.String(), body)
		apitest.Conform(t, "updateContact", status, raw)
		if status != http.StatusOK {
			t.Fatalf("update %v: %d %s", body, status, raw)
		}
		return decode[cJSON](t, raw)
	}
	if got := put(ann.ID, map[string]any{"show_nickname": false}); got.Name != "Ann Real" || got.Nickname == nil || *got.Nickname != "Annie" {
		t.Errorf("nickname off = %+v, want name Ann Real, nickname kept", got)
	}
	if got := put(ann.ID, map[string]any{"phone": "0812345678"}); got.ShowNickname || got.Name != "Ann Real" {
		t.Errorf("show_nickname absent on update changed: %+v", got)
	}
	if got := put(ann.ID, map[string]any{"show_nickname": true}); got.Name != "Annie" {
		t.Errorf("nickname back on = %+v, want Annie", got)
	}
	if got := put(ann.ID, map[string]any{"nickname": nil}); got.Nickname != nil || got.Name != "Ann Real" {
		t.Errorf("nickname null = %+v, want cleared", got)
	}
	if got := put(ann.ID, map[string]any{"nickname": "Annie2"}); got.Name != "Annie2" {
		t.Errorf("nickname set again = %+v", got)
	}
	if status, _ := apitest.Do(t, r, "PUT", "/api/v1/contacts/"+ann.ID.String(), map[string]any{"nickname": strings.Repeat("n", 101)}); status != http.StatusBadRequest {
		t.Errorf("update nickname > 100 = %d, want 400", status)
	}
	if status, _ := apitest.Do(t, r, "PUT", "/api/v1/contacts/"+uuid.NewString(), map[string]any{"phone": "1"}); status != http.StatusNotFound {
		t.Errorf("update unknown = %d, want 404", status)
	}

	if _, err := e.pool.Exec(ctx, `UPDATE contacts SET linked_user_id = $1, email = 'stored@example.com' WHERE id = $2`, friend, bob.ID); err != nil {
		t.Fatal(err)
	}
	status, raw := apitest.Do(t, r, "GET", "/api/v1/contacts/"+bob.ID.String(), nil)
	apitest.Conform(t, "getContact", status, raw)
	linked := decode[cJSON](t, raw)
	if linked.Name != "Friend Profile" || linked.LinkedUser == nil || linked.LinkedUser.ID != friend ||
		linked.Avatar.Kind != "icon" || linked.Avatar.IconCode == nil || linked.Avatar.IconCode.Icon == nil ||
		*linked.Avatar.IconCode.Icon != "star" ||
		linked.Email == nil || *linked.Email == "stored@example.com" {
		t.Errorf("linked = %+v, want profile name, linked user's icon + email", linked)
	}
	if got := put(bob.ID, map[string]any{"nickname": "Bobby", "show_nickname": true}); got.Name != "Bobby" || got.LinkedUser == nil {
		t.Errorf("linked + nickname = %+v, want Bobby", got)
	}
	if got := put(bob.ID, map[string]any{"show_nickname": false}); got.Name != "Friend Profile" {
		t.Errorf("linked, nickname off = %+v, want Friend Profile", got)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET display_name = 'Renamed Profile' WHERE id = $1`, friend); err != nil {
		t.Fatal(err)
	}
	if status, raw = apitest.Do(t, r, "GET", "/api/v1/contacts/"+bob.ID.String(), nil); decode[cJSON](t, raw).Name != "Renamed Profile" {
		t.Errorf("profile rename not live: %s", raw)
	}

	// getContact include: unlinked_names null unless asked.
	var det struct {
		UnlinkedNames *[]UnlinkedName `json:"unlinked_names"`
	}
	status, raw = apitest.Do(t, r, "GET", "/api/v1/contacts/"+ann.ID.String(), nil)
	apitest.Conform(t, "getContact", status, raw)
	_ = json.Unmarshal(raw, &det)
	if det.UnlinkedNames != nil {
		t.Errorf("unlinked_names without include = %v, want null", *det.UnlinkedNames)
	}
	status, raw = apitest.Do(t, r, "GET", "/api/v1/contacts/"+ann.ID.String()+"?include=unlinked_names", nil)
	apitest.Conform(t, "getContact", status, raw)
	det.UnlinkedNames = nil
	_ = json.Unmarshal(raw, &det)
	if det.UnlinkedNames == nil || len(*det.UnlinkedNames) != 1 || (*det.UnlinkedNames)[0].Name != "Somchai" {
		t.Errorf("unlinked_names with include = %v", det.UnlinkedNames)
	}
	if status, _ := apitest.Do(t, r, "GET", "/api/v1/contacts/"+uuid.NewString(), nil); status != http.StatusNotFound {
		t.Errorf("get unknown = %d, want 404", status)
	}

	if status, raw = apitest.Do(t, r, "POST", "/api/v1/contacts/"+cat.ID.String()+"/archive", nil); status != http.StatusOK {
		t.Fatalf("archive: %d %s", status, raw)
	}
	apitest.Conform(t, "archiveContact", status, raw)
	if got := decode[cJSON](t, raw); got.Status != "archived" {
		t.Errorf("archived = %+v", got)
	}
	if status, _ = apitest.Do(t, r, "POST", "/api/v1/contacts/"+cat.ID.String()+"/archive", nil); status != http.StatusBadRequest {
		t.Errorf("archive twice = %d, want 400", status)
	}
	names := func(q string) []string {
		t.Helper()
		status, raw := apitest.Do(t, r, "GET", "/api/v1/contacts"+q, nil)
		apitest.Conform(t, "listContacts", status, raw)
		var out []string
		for _, c := range decode[struct {
			Data []cJSON `json:"data"`
		}](t, raw).Data {
			out = append(out, c.Name)
		}
		return out
	}
	if got := strings.Join(names("?status=all"), ","); got != "Annie2,Dan,Renamed Profile,Cat" {
		t.Errorf("order (status=all) = %s, want Annie2,Dan,Renamed Profile,Cat (active first, then by name)", got)
	}
	if got := strings.Join(names(""), ","); got != "Annie2,Dan,Renamed Profile" {
		t.Errorf("default list = %s", got)
	}
	for q, want := range map[string]string{
		"?search=annie2":   "Annie2", // nickname
		"?search=ann%20re": "Annie2", // display_name
		"?search=ann@exam": "Annie2", // email
		"?search=renamed":  "Renamed Profile",
		"?linked=true":     "Renamed Profile",
		"?status=archived": "Cat",
	} {
		if got := strings.Join(names(q), ","); got != want {
			t.Errorf("list %s = %q, want %q", q, got, want)
		}
	}
	var friendEmail string
	_ = e.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, friend).Scan(&friendEmail)
	if got := names("?search=" + friendEmail[:12]); len(got) != 1 || got[0] != "Renamed Profile" {
		t.Errorf("search by linked email = %v", got)
	}
	if got := names("?status=archived&search=cat"); len(got) != 1 {
		t.Errorf("search display_name behind hidden nickname = %v", got)
	}

	status, raw = apitest.Do(t, r, "POST", "/api/v1/contacts/"+cat.ID.String()+"/restore", nil)
	apitest.Conform(t, "restoreContact", status, raw)
	if got := decode[cJSON](t, raw); got.Status != "active" {
		t.Errorf("restored = %+v", got)
	}
	status, raw = apitest.Do(t, r, "POST", "/api/v1/contacts/"+bob.ID.String()+"/unlink", nil)
	apitest.Conform(t, "unlinkContact", status, raw)
	if got := decode[cJSON](t, raw); got.LinkedUser != nil || got.Name != "bob" {
		t.Errorf("unlinked = %+v, want stored name bob and no linked_user", got)
	}
	if status, _ = apitest.Do(t, r, "POST", "/api/v1/contacts/"+bob.ID.String()+"/unlink", nil); status != http.StatusBadRequest {
		t.Errorf("unlink twice = %d, want 400", status)
	}
	status, raw = apitest.Do(t, r, "POST", "/api/v1/contacts/"+ann.ID.String()+"/absorb", map[string]any{"names": []string{"A", "B"}})
	apitest.Conform(t, "absorbContactNames", status, raw)
	if got := decode[struct {
		Contact  cJSON `json:"contact"`
		Absorbed int   `json:"absorbed_count"`
	}](t, raw); got.Absorbed != 2 || got.Contact.ID != ann.ID {
		t.Errorf("absorb = %+v", got)
	}
	if status, _ = apitest.Do(t, r, "POST", "/api/v1/contacts/"+ann.ID.String()+"/absorb", map[string]any{"names": []string{}}); status != http.StatusBadRequest {
		t.Errorf("absorb no names = %d, want 400", status)
	}
	status, raw = apitest.Do(t, r, "GET", "/api/v1/contacts/unlinked-names", nil)
	apitest.Conform(t, "listUnlinkedNames", status, raw)

	status, raw = apitest.Do(t, r, "DELETE", "/api/v1/contacts/"+cat.ID.String(), nil)
	apitest.Conform(t, "deleteContact", status, raw)
	if got := decode[struct {
		ID       uuid.UUID `json:"id"`
		Restored int       `json:"splits_restored"`
	}](t, raw); got.ID != cat.ID || got.Restored != 3 {
		t.Errorf("delete = %+v", got)
	}
	if status, _ = apitest.Do(t, r, "DELETE", "/api/v1/contacts/"+cat.ID.String(), nil); status != http.StatusNotFound {
		t.Errorf("delete twice = %d, want 404", status)
	}
}

func (e *contactEnv) linkRequest(sender, recipient uuid.UUID) (contactID, notifID uuid.UUID) {
	e.t.Helper()
	ctx := context.Background()
	var email string
	if err := e.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, recipient).Scan(&email); err != nil {
		e.t.Fatal(err)
	}
	c := e.create(e.as(sender), map[string]any{"display_name": "Typed Name", "email": email})
	status, raw := apitest.Do(e.t, e.as(sender), "POST", "/api/v1/contacts/"+c.ID.String()+"/request-link", nil)
	apitest.Conform(e.t, "requestContactLink", status, raw)
	if status != http.StatusOK {
		e.t.Fatalf("request-link: %d %s", status, raw)
	}
	if err := e.pool.QueryRow(ctx, `SELECT id FROM notifications WHERE recipient_user_id = $1
		AND type = 'contact_link_request' ORDER BY created_at DESC LIMIT 1`, recipient).Scan(&notifID); err != nil {
		e.t.Fatalf("notification: %v", err)
	}
	return c.ID, notifID
}

type linkResult struct {
	Contact      *cJSON `json:"contact"`
	Notification struct {
		ID    uuid.UUID `json:"id"`
		State string    `json:"state"`
	} `json:"notification"`
	Badges struct {
		Unread int `json:"unread_notifications"`
	} `json:"badges"`
}

func TestContactLinkRequests(t *testing.T) {
	e := newContactEnv(t)
	ctx := context.Background()
	a, b, c, d, f := testdb.User(t, e.pool), testdb.User(t, e.pool), testdb.User(t, e.pool), testdb.User(t, e.pool), testdb.User(t, e.pool)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = ANY($1)`, []uuid.UUID{a, b, c, d, f})
	})
	for id, name := range map[uuid.UUID]string{a: "Alice Profile", b: "Beam Profile", d: "Dee Profile", f: "Fon Profile"} {
		if _, err := e.pool.Exec(ctx, `UPDATE users SET display_name = $2 WHERE id = $1`, id, name); err != nil {
			t.Fatal(err)
		}
	}

	contactA, notif := e.linkRequest(a, b)
	rb := e.as(b)
	status, raw := apitest.Do(t, rb, "GET", "/api/v1/contacts/link-requests/"+notif.String()+"/sender-profile", nil)
	apitest.Conform(t, "getLinkRequestSender", status, raw)
	sp := decode[struct {
		ID        uuid.UUID  `json:"id"`
		Name      string     `json:"display_name"`
		ContactID *uuid.UUID `json:"contact_id"`
	}](t, raw)
	if sp.ID != a || sp.Name != "Alice Profile" || sp.ContactID != nil {
		t.Errorf("sender profile = %+v, want Alice, no contact yet", sp)
	}

	status, raw = apitest.Do(t, rb, "POST", "/api/v1/contacts/link-requests/"+notif.String()+"/accept", nil)
	apitest.Conform(t, "acceptContactLinkRequest", status, raw)
	acc := decode[linkResult](t, raw)
	if acc.Notification.State != "actioned" || acc.Contact != nil {
		t.Errorf("accept = %+v, want actioned notification, no contact (it is the sender's)", acc)
	}
	var linkedTo *uuid.UUID
	_ = e.pool.QueryRow(ctx, `SELECT linked_user_id FROM contacts WHERE id = $1`, contactA).Scan(&linkedTo)
	if linkedTo == nil || *linkedTo != b {
		t.Errorf("sender's contact linked_user_id = %v, want b", linkedTo)
	}
	// Accepted but not rejectable twice etc.; a stranger cannot see it.
	if status, _ = apitest.Do(t, e.as(c), "POST", "/api/v1/contacts/link-requests/"+notif.String()+"/accept", nil); status != http.StatusNotFound {
		t.Errorf("accept by a stranger = %d, want 404", status)
	}
	if status, _ = apitest.Do(t, rb, "POST", "/api/v1/contacts/link-requests/"+uuid.NewString()+"/accept", nil); status != http.StatusNotFound {
		t.Errorf("accept unknown = %d, want 404", status)
	}

	status, raw = apitest.Do(t, rb, "POST", "/api/v1/contacts/link-requests/"+notif.String()+"/create-linked-contact", nil)
	apitest.Conform(t, "createLinkedContactFromRequest", status, raw)
	made := decode[linkResult](t, raw)
	if made.Contact == nil || made.Contact.Name != "Alice Profile" || made.Contact.LinkedUser == nil || made.Contact.LinkedUser.ID != a {
		t.Errorf("created linked contact = %+v, want Alice Profile linked to a", made.Contact)
	}
	status, raw = apitest.Do(t, rb, "GET", "/api/v1/contacts/link-requests/"+notif.String()+"/sender-profile", nil)
	apitest.Conform(t, "getLinkRequestSender", status, raw)
	if sp := decode[struct {
		ContactID *uuid.UUID `json:"contact_id"`
	}](t, raw); made.Contact == nil || sp.ContactID == nil || *sp.ContactID != made.Contact.ID {
		t.Errorf("sender profile contact_id = %v, want the new contact", sp.ContactID)
	}

	contactA2, notif2 := e.linkRequest(a, c)
	status, raw = apitest.Do(t, e.as(c), "POST", "/api/v1/contacts/link-requests/"+notif2.String()+"/reject", nil)
	apitest.Conform(t, "rejectContactLinkRequest", status, raw)
	rej := decode[linkResult](t, raw)
	if rej.Notification.State != "dismissed" || rej.Contact != nil {
		t.Errorf("reject = %+v, want dismissed, no contact", rej)
	}
	_ = e.pool.QueryRow(ctx, `SELECT linked_user_id FROM contacts WHERE id = $1`, contactA2).Scan(&linkedTo)
	if linkedTo != nil {
		t.Errorf("rejected request linked the contact to %v", *linkedTo)
	}

	_, notif3 := e.linkRequest(f, d)
	rd := e.as(d)
	if status, _ = apitest.Do(t, rd, "POST", "/api/v1/contacts/link-requests/"+notif3.String()+"/link-existing-contact/"+uuid.NewString(), nil); status != http.StatusConflict && status != http.StatusNotFound {
		t.Errorf("link-existing before accept = %d, want a 4xx (not accepted yet)", status)
	}
	if status, raw = apitest.Do(t, rd, "POST", "/api/v1/contacts/link-requests/"+notif3.String()+"/accept", nil); status != http.StatusOK {
		t.Fatalf("accept: %d %s", status, raw)
	}
	mine := e.create(rd, map[string]any{"display_name": "My Fon", "nickname": "Fonny"})
	status, raw = apitest.Do(t, rd, "POST", "/api/v1/contacts/link-requests/"+notif3.String()+"/link-existing-contact/"+mine.ID.String(), map[string]any{"note": "met at work"})
	apitest.Conform(t, "linkExistingContactFromRequest", status, raw)
	ex := decode[linkResult](t, raw)
	if ex.Contact == nil || ex.Contact.ID != mine.ID || ex.Contact.Name != "Fonny" || ex.Contact.LinkedUser == nil || ex.Contact.LinkedUser.ID != f {
		t.Errorf("link-existing = %+v, want my contact linked to f, nickname Fonny shown", ex.Contact)
	}
	if status, _ = apitest.Do(t, rd, "POST", "/api/v1/contacts/link-requests/"+notif3.String()+"/link-existing-contact/"+uuid.NewString(), nil); status != http.StatusNotFound {
		t.Errorf("link-existing unknown contact = %d, want 404", status)
	}
}
