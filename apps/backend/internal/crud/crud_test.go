package crud_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/crud"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/models"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
)

type note struct {
	models.Base
	Title   string `json:"title" crud:"create,update,filter,sort" validate:"required,max=50"`
	Count   int    `json:"count" crud:"create,update,filter,sort"`
	Done    bool   `json:"done" crud:"create,update,filter"`
	Secret  string `json:"secret" crud:"create"`
	OwnerID string `json:"owner_id"`
}

type other struct {
	models.Base
	Name string `json:"name" crud:"create"`
}

var dbSeq atomic.Int64

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := fmt.Sprintf("%s_%d", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()), dbSeq.Add(1))
	db, err := dbx.Open("file:"+name+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&note{}, &other{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// setup mounts a note resource with opts at /notes and returns the router.
func setup(t *testing.T, opts ...crud.Option) (http.Handler, *crud.Resource[note], *gorm.DB) {
	t.Helper()
	db := openDB(t)
	r := chi.NewRouter()
	reg := crud.NewRegistry(db, httpx.NewValidator(), slog.New(slog.DiscardHandler))
	res := crud.NewForType[note](reg.On(r), "/notes", opts...)
	return r, res, db
}

type resp struct {
	Code int
	Body []byte
}

func (r resp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
}

func (r resp) errFields(t *testing.T) map[string]string {
	t.Helper()
	var e struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	r.decode(t, &e)
	return e.Error.Fields
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr ...string) resp {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return resp{w.Code, w.Body.Bytes()}
}

func mustStatus(t *testing.T, r resp, want int) {
	t.Helper()
	if r.Code != want {
		t.Fatalf("status = %d, want %d; body %s", r.Code, want, r.Body)
	}
}

func create(t *testing.T, h http.Handler, body string, hdr ...string) note {
	t.Helper()
	r := do(t, h, http.MethodPost, "/notes", body, hdr...)
	mustStatus(t, r, http.StatusCreated)
	var n note
	r.decode(t, &n)
	return n
}

type page struct {
	Items      []note `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func list(t *testing.T, h http.Handler, query string, hdr ...string) page {
	t.Helper()
	r := do(t, h, http.MethodGet, "/notes?"+query, "", hdr...)
	mustStatus(t, r, http.StatusOK)
	var p page
	r.decode(t, &p)
	return p
}

func titles(p page) string {
	s := make([]string, len(p.Items))
	for i, n := range p.Items {
		s[i] = n.Title
	}
	return strings.Join(s, ",")
}

func TestCRUDRoundTrip(t *testing.T) {
	h, _, _ := setup(t)

	n := create(t, h, `{"title":"a","count":3,"done":true,"secret":"s"}`)
	if n.ID == "" || n.Title != "a" || n.Count != 3 || !n.Done || n.Secret != "s" || n.CreatedAt.IsZero() {
		t.Fatalf("created %+v", n)
	}

	r := do(t, h, http.MethodGet, "/notes/"+n.ID, "")
	mustStatus(t, r, http.StatusOK)
	var got note
	r.decode(t, &got)
	if got.ID != n.ID || got.Title != "a" {
		t.Fatalf("get %+v", got)
	}

	r = do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"title":"b"}`)
	mustStatus(t, r, http.StatusOK)
	r.decode(t, &got)
	if got.Title != "b" || got.Count != 3 || !got.Done || !got.UpdatedAt.After(n.UpdatedAt) {
		t.Fatalf("patched %+v (was %+v)", got, n)
	}

	p := list(t, h, "")
	if len(p.Items) != 1 || p.Items[0].Title != "b" || p.NextCursor != "" {
		t.Fatalf("list %+v", p)
	}

	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+n.ID, ""), http.StatusNoContent)
	mustStatus(t, do(t, h, http.MethodGet, "/notes/"+n.ID, ""), http.StatusNotFound)
	mustStatus(t, do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"title":"c"}`), http.StatusNotFound)
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+n.ID, ""), http.StatusNotFound)
	mustStatus(t, do(t, h, http.MethodGet, "/notes/nope", ""), http.StatusNotFound)
	if p := list(t, h, ""); len(p.Items) != 0 {
		t.Fatalf("list after delete %+v", p)
	}
}

func TestSoftDelete(t *testing.T) {
	h, _, db := setup(t)
	n := create(t, h, `{"title":"a"}`)
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+n.ID, ""), http.StatusNoContent)

	var row note
	if err := db.Unscoped().Where("id = ?", n.ID).Take(&row).Error; err != nil {
		t.Fatalf("row hard-deleted: %v", err)
	}
	if !row.DeletedAt.Valid {
		t.Fatal("deleted_at not set")
	}
}

func TestPatchZeroValues(t *testing.T) {
	h, _, _ := setup(t)
	n := create(t, h, `{"title":"a","count":5,"done":true}`)

	r := do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"count":0,"done":false}`)
	mustStatus(t, r, http.StatusOK)
	var got note
	r.decode(t, &got)
	if got.Count != 0 || got.Done || got.Title != "a" {
		t.Fatalf("patched %+v", got)
	}
	// Persisted, not just echoed.
	r = do(t, h, http.MethodGet, "/notes/"+n.ID, "")
	r.decode(t, &got)
	if got.Count != 0 || got.Done {
		t.Fatalf("stored %+v", got)
	}

	// Validation runs on the merged entity: clearing a required field fails and changes nothing.
	r = do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"title":""}`)
	mustStatus(t, r, http.StatusBadRequest)
	if f := r.errFields(t); f["title"] == "" {
		t.Fatalf("fields %v", f)
	}
}

func TestFieldRejection(t *testing.T) {
	h, _, _ := setup(t)

	r := do(t, h, http.MethodPost, "/notes", `{"title":"a","id":"x","owner_id":"o","created_at":"2020-01-01T00:00:00Z","bogus":1}`)
	mustStatus(t, r, http.StatusBadRequest)
	f := r.errFields(t)
	for _, k := range []string{"id", "owner_id", "created_at", "bogus"} {
		if f[k] == "" {
			t.Errorf("missing field error for %q: %v", k, f)
		}
	}
	if _, ok := f["title"]; ok {
		t.Errorf("title rejected: %v", f)
	}

	r = do(t, h, http.MethodPost, "/notes", `{"title":"a","count":"many"}`)
	mustStatus(t, r, http.StatusBadRequest)
	if f := r.errFields(t); f["count"] != "must be an integer" {
		t.Fatalf("fields %v", f)
	}

	r = do(t, h, http.MethodPost, "/notes", `{"count":1}`)
	mustStatus(t, r, http.StatusBadRequest)
	if f := r.errFields(t); f["title"] != "is required" {
		t.Fatalf("fields %v", f)
	}

	n := create(t, h, `{"title":"a","secret":"s"}`)
	// secret is create-only.
	r = do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"secret":"t","owner_id":"o","updated_at":"2020-01-01T00:00:00Z"}`)
	mustStatus(t, r, http.StatusBadRequest)
	f = r.errFields(t)
	if f["secret"] == "" || f["owner_id"] == "" || f["updated_at"] == "" {
		t.Fatalf("fields %v", f)
	}
}

func TestFilters(t *testing.T) {
	h, _, _ := setup(t)
	a := create(t, h, `{"title":"apple","count":1,"done":true}`)
	create(t, h, `{"title":"banana","count":2}`)
	c := create(t, h, `{"title":"cherry","count":3,"done":true}`)

	cases := []struct{ query, want string }{
		{"filter[title]=banana", "banana"},
		{"filter[title][eq]=banana", "banana"},
		{"filter[title][ne]=banana", "cherry,apple"},
		{"filter[count][gt]=1", "cherry,banana"},
		{"filter[count][gte]=2", "cherry,banana"},
		{"filter[count][lt]=2", "apple"},
		{"filter[count][lte]=2", "banana,apple"},
		{"filter[count][in]=1,3", "cherry,apple"},
		{"filter[title][like]=%25an%25", "banana"},
		{"filter[done]=true", "cherry,apple"},
		{"filter[done]=true&filter[count][gt]=1", "cherry"},
		{"filter[id]=" + a.ID, "apple"},
		{"filter[created_at][gte]=" + url.QueryEscape(c.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")), "cherry"},
		{"filter[created_at][lt]=" + url.QueryEscape(c.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")), "banana,apple"},
	}
	for _, tc := range cases {
		if got := titles(list(t, h, tc.query)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.query, got, tc.want)
		}
	}

	bad := []struct{ query, key string }{
		{"filter[secret]=x", "filter[secret]"},      // not filterable
		{"filter[nope]=x", "filter[nope]"},          // unknown
		{"filter[count][gt]=x", "filter[count]"},    // bad value
		{"filter[count][regex]=1", "filter[count]"}, // unknown op
		{"filter[count][like]=1", "filter[count]"},  // like on non-text
		{"filter=x", "filter"},                      // malformed
	}
	for _, tc := range bad {
		r := do(t, h, http.MethodGet, "/notes?"+tc.query, "")
		mustStatus(t, r, http.StatusBadRequest)
		if f := r.errFields(t); f[tc.key] == "" {
			t.Errorf("%s: fields %v", tc.query, f)
		}
	}
}

func TestSort(t *testing.T) {
	h, _, _ := setup(t)
	create(t, h, `{"title":"b","count":1}`)
	create(t, h, `{"title":"a","count":2}`)
	create(t, h, `{"title":"c","count":2}`)

	cases := []struct{ sort, want string }{
		{"", "c,a,b"}, // default -created_at
		{"created_at", "b,a,c"},
		{"title", "a,b,c"},
		{"-title", "c,b,a"},
		{"-count,title", "a,c,b"},
		{"id", "b,a,c"},
	}
	for _, tc := range cases {
		if got := titles(list(t, h, "sort="+tc.sort)); got != tc.want {
			t.Errorf("sort=%s: got %q, want %q", tc.sort, got, tc.want)
		}
	}

	for _, s := range []string{"done", "secret", "owner_id", "nope", "deleted_at"} {
		r := do(t, h, http.MethodGet, "/notes?sort="+s, "")
		mustStatus(t, r, http.StatusBadRequest)
		if f := r.errFields(t); f["sort"] == "" {
			t.Errorf("sort=%s: fields %v", s, f)
		}
	}

	h2, _, _ := setup(t, crud.DefaultSort("title"))
	create(t, h2, `{"title":"y"}`)
	create(t, h2, `{"title":"x"}`)
	if got := titles(list(t, h2, "")); got != "x,y" {
		t.Errorf("DefaultSort: got %q", got)
	}
}

func TestCursorPagination(t *testing.T) {
	h, _, _ := setup(t)
	for i := range 7 {
		create(t, h, fmt.Sprintf(`{"title":"n%d"}`, i))
	}
	for _, sort := range []string{"", "created_at", "-id", "id"} {
		all := titles(list(t, h, "limit=200&sort="+sort))
		var got []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 5 {
				t.Fatalf("sort=%q: too many pages", sort)
			}
			p := list(t, h, "limit=3&sort="+sort+"&cursor="+cursor)
			got = append(got, titles(p))
			if p.NextCursor == "" {
				if len(p.Items) != 1 {
					t.Fatalf("sort=%q: last page has %d items", sort, len(p.Items))
				}
				break
			}
			if len(p.Items) != 3 {
				t.Fatalf("sort=%q: page has %d items", sort, len(p.Items))
			}
			cursor = p.NextCursor
		}
		if strings.Join(got, ",") != all {
			t.Errorf("sort=%q: paged %q, want %q", sort, strings.Join(got, ","), all)
		}
	}
	if all := titles(list(t, h, "")); all != "n6,n5,n4,n3,n2,n1,n0" {
		t.Fatalf("default order %q", all)
	}

	// Exactly a full page: no dangling cursor.
	if p := list(t, h, "limit=7"); p.NextCursor != "" || len(p.Items) != 7 {
		t.Fatalf("full page %+v", p)
	}
	// A cursor stays valid after its row is deleted.
	p := list(t, h, "limit=2")
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+p.Items[1].ID, ""), http.StatusNoContent)
	if got := titles(list(t, h, "limit=2&cursor="+p.NextCursor)); got != "n4,n3" {
		t.Fatalf("after delete got %q", got)
	}

	for _, q := range []string{"cursor=!!!", "cursor=" + "bm9wZQ", "sort=title&cursor=" + p.NextCursor, "offset=1&cursor=" + p.NextCursor} {
		mustStatus(t, do(t, h, http.MethodGet, "/notes?"+q, ""), http.StatusBadRequest)
	}
}

func TestOffsetAndLimits(t *testing.T) {
	h, _, _ := setup(t, crud.MaxLimit(4))
	for i := range 6 {
		create(t, h, fmt.Sprintf(`{"title":"n%d"}`, i))
	}
	if p := list(t, h, "sort=title&limit=2&offset=1"); titles(p) != "n1,n2" || p.NextCursor != "" {
		t.Fatalf("offset page %+v", p)
	}
	if p := list(t, h, "limit=2&offset=2"); titles(p) != "n3,n2" || p.NextCursor != "" {
		t.Fatalf("offset on default sort %+v", p)
	}
	if p := list(t, h, "limit=100"); len(p.Items) != 4 || p.NextCursor == "" {
		t.Fatalf("MaxLimit not applied: %d items", len(p.Items))
	}
	if p := list(t, h, "sort=title"); len(p.Items) != 4 {
		t.Fatalf("default limit clamped to MaxLimit: %d items", len(p.Items))
	}
	for _, q := range []string{"limit=0", "limit=x", "offset=-1"} {
		mustStatus(t, do(t, h, http.MethodGet, "/notes?"+q, ""), http.StatusBadRequest)
	}
}

func TestDefaultLimit(t *testing.T) {
	_, res, _ := setup(t)
	ctx := context.Background()
	for i := range crud.DefaultLimit + 1 {
		if _, err := res.Service.Create(ctx, map[string]json.RawMessage{"title": json.RawMessage(fmt.Sprintf(`"n%d"`, i))}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := res.Service.List(ctx, crud.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != crud.DefaultLimit || p.NextCursor == "" {
		t.Fatalf("got %d items, cursor %q", len(p.Items), p.NextCursor)
	}
}

type ownerKey struct{}

func owner(ctx context.Context) string { s, _ := ctx.Value(ownerKey{}).(string); return s }

func withOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey{}, r.Header.Get("X-Owner"))))
	})
}

func TestScopeIsolation(t *testing.T) {
	h, _, _ := setup(t,
		crud.Use(withOwner),
		crud.Scope[note](func(ctx context.Context, q *gorm.DB) *gorm.DB { return q.Where("owner_id = ?", owner(ctx)) }),
		crud.WithHooks(crud.Hooks[note]{
			BeforeCreate: func(ctx context.Context, n *note) error { n.OwnerID = owner(ctx); return nil },
		}),
	)
	alice := []string{"X-Owner", "alice"}
	bob := []string{"X-Owner", "bob"}
	a := create(t, h, `{"title":"a"}`, alice...)
	create(t, h, `{"title":"b1"}`, bob...)
	create(t, h, `{"title":"b2"}`, bob...)
	if a.OwnerID != "alice" {
		t.Fatalf("owner not set: %+v", a)
	}

	if got := titles(list(t, h, "", alice...)); got != "a" {
		t.Fatalf("alice sees %q", got)
	}
	if got := titles(list(t, h, "filter[id]="+a.ID, bob...)); got != "" {
		t.Fatalf("bob filtered to %q", got)
	}
	mustStatus(t, do(t, h, http.MethodGet, "/notes/"+a.ID, "", bob...), http.StatusNotFound)
	mustStatus(t, do(t, h, http.MethodPatch, "/notes/"+a.ID, `{"title":"x"}`, bob...), http.StatusNotFound)
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+a.ID, "", bob...), http.StatusNotFound)
	mustStatus(t, do(t, h, http.MethodGet, "/notes/"+a.ID, "", alice...), http.StatusOK)

	// Bob's pages never include Alice's rows, and her ids are not usable as cursors.
	p := list(t, h, "limit=1", bob...)
	if got := titles(p) + "|" + titles(list(t, h, "cursor="+p.NextCursor, bob...)); got != "b2|b1" {
		t.Fatalf("bob pages %q", got)
	}
	foreign := base64.RawURLEncoding.EncodeToString([]byte(a.ID))
	mustStatus(t, do(t, h, http.MethodGet, "/notes?cursor="+foreign, "", bob...), http.StatusBadRequest)
	mustStatus(t, do(t, h, http.MethodGet, "/notes?cursor="+foreign, "", alice...), http.StatusOK)
}

func TestHooksOrderAndVeto(t *testing.T) {
	var events []string
	rec := func(name string) func(context.Context, *note) error {
		return func(ctx context.Context, n *note) error {
			if !dbx.InTx(ctx) {
				t.Errorf("%s outside tx", name)
			}
			events = append(events, name)
			return nil
		}
	}
	veto := errors.New("vetoed")
	h, _, db := setup(t,
		crud.WithHooks(crud.Hooks[note]{
			BeforeCreate: func(ctx context.Context, n *note) error {
				events = append(events, "BeforeCreate1")
				n.Title = strings.TrimSpace(n.Title) // runs before validation
				return nil
			},
			AfterCreate: rec("AfterCreate1"),
			BeforeUpdate: func(ctx context.Context, existing *note, patch map[string]any) error {
				events = append(events, "BeforeUpdate:"+existing.Title)
				if patch["title"] == "veto" {
					return apperr.Forbidden("no")
				}
				patch["owner_id"] = "hooked" // server-side field
				return nil
			},
			AfterUpdate:  rec("AfterUpdate"),
			BeforeDelete: rec("BeforeDelete"),
			AfterDelete: func(ctx context.Context, n *note) error {
				events = append(events, "AfterDelete")
				if n.Title == "keep" {
					return veto // rolls back
				}
				return nil
			},
		}),
		crud.WithHooks(crud.Hooks[note]{BeforeCreate: rec("BeforeCreate2")}),
		crud.Authorize[note](func(ctx context.Context, op crud.Op, n *note) error {
			events = append(events, "Authorize:"+string(op))
			if op == crud.OpCreate && n.Title == "forbidden" {
				return apperr.Forbidden("nope")
			}
			return nil
		}),
	)

	r := do(t, h, http.MethodPost, "/notes", `{"title":"   "}`)
	mustStatus(t, r, http.StatusBadRequest) // trimmed to "" by the hook, then validated
	if got := strings.Join(events, ","); got != "BeforeCreate1,BeforeCreate2" {
		t.Fatalf("events %s", got)
	}

	events = nil
	n := create(t, h, `{"title":" a "}`)
	if n.Title != "a" {
		t.Fatalf("title %q", n.Title)
	}
	if got := strings.Join(events, ","); got != "BeforeCreate1,BeforeCreate2,Authorize:create,AfterCreate1" {
		t.Fatalf("create events %s", got)
	}

	mustStatus(t, do(t, h, http.MethodPost, "/notes", `{"title":"forbidden"}`), http.StatusForbidden)

	events = nil
	r = do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"title":"b"}`)
	mustStatus(t, r, http.StatusOK)
	var got note
	r.decode(t, &got)
	if got.Title != "b" || got.OwnerID != "hooked" {
		t.Fatalf("patched %+v", got)
	}
	if e := strings.Join(events, ","); e != "Authorize:update,BeforeUpdate:a,AfterUpdate" {
		t.Fatalf("update events %s", e)
	}
	mustStatus(t, do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"title":"veto"}`), http.StatusForbidden)

	events = nil
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+n.ID, ""), http.StatusNoContent)
	if e := strings.Join(events, ","); e != "Authorize:delete,BeforeDelete,AfterDelete" {
		t.Fatalf("delete events %s", e)
	}

	// An After hook error rolls the operation back.
	k := create(t, h, `{"title":"keep"}`)
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+k.ID, ""), http.StatusInternalServerError)
	var cnt int64
	db.Model(&note{}).Where("id = ?", k.ID).Count(&cnt)
	if cnt != 1 {
		t.Fatal("delete not rolled back")
	}
}

func TestOnlyAndUseFor(t *testing.T) {
	deny := func(next http.Handler) http.Handler {
		return httpx.Handle(func(http.ResponseWriter, *http.Request) error { return apperr.Unauthorized("no") })
	}
	h, res, _ := setup(t,
		crud.Only(crud.OpList, crud.OpGet),
		crud.Only(crud.OpCreate),
		crud.UseFor(crud.OpGet, deny),
	)
	extraCalled := false
	res.Route(func(r chi.Router) {
		r.Post("/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
			extraCalled = true
			w.WriteHeader(http.StatusAccepted)
		})
	})

	n := create(t, h, `{"title":"a"}`)
	mustStatus(t, do(t, h, http.MethodGet, "/notes", ""), http.StatusOK)
	mustStatus(t, do(t, h, http.MethodGet, "/notes/"+n.ID, ""), http.StatusUnauthorized)
	mustStatus(t, do(t, h, http.MethodPatch, "/notes/"+n.ID, `{"title":"b"}`), http.StatusMethodNotAllowed)
	mustStatus(t, do(t, h, http.MethodDelete, "/notes/"+n.ID, ""), http.StatusMethodNotAllowed)
	mustStatus(t, do(t, h, http.MethodPost, "/notes/"+n.ID+"/archive", ""), http.StatusAccepted)
	if !extraCalled {
		t.Fatal("extra route not called")
	}
}

func TestUseAppliesToExtraRoutes(t *testing.T) {
	calls := 0
	count := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; next.ServeHTTP(w, r) })
	}
	h, res, _ := setup(t, crud.Use(count))
	res.Route(func(r chi.Router) { r.Get("/x/extra", func(http.ResponseWriter, *http.Request) {}) })
	do(t, h, http.MethodGet, "/notes", "")
	do(t, h, http.MethodGet, "/notes/x/extra", "")
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestMismatchedTypedOptionPanics(t *testing.T) {
	db := openDB(t)
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: no panic", name)
			}
		}()
		fn()
	}
	mustPanic("Scope", func() {
		crud.NewService[note](db, nil, crud.Scope[other](func(_ context.Context, q *gorm.DB) *gorm.DB { return q }))
	})
	mustPanic("WithHooks", func() {
		crud.NewForType[note](crud.NewRegistry(db, nil, nil).On(chi.NewRouter()), "/n", crud.WithHooks(crud.Hooks[other]{}))
	})
	mustPanic("Authorize", func() {
		crud.NewService[note](db, nil, crud.Authorize[other](func(context.Context, crud.Op, *other) error { return nil }))
	})
	mustPanic("DefaultSort", func() { crud.NewService[note](db, nil, crud.DefaultSort("secret")) })
	mustPanic("MaxLimit", func() { crud.NewService[note](db, nil, crud.MaxLimit(0)) })
}

func TestServiceWithoutHTTP(t *testing.T) {
	db := openDB(t)
	svc := crud.NewService[note](db, nil, crud.Only(crud.OpList)) // HTTP-only options are ignored
	ctx := context.Background()
	n, err := svc.Create(ctx, map[string]json.RawMessage{"title": json.RawMessage(`"a"`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, n.ID, map[string]json.RawMessage{"count": json.RawMessage(`7`)}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, n.ID)
	if err != nil || got.Count != 7 {
		t.Fatalf("get %+v %v", got, err)
	}
	var cnt int64
	if err := svc.Query(ctx).Count(&cnt).Error; err != nil || cnt != 1 {
		t.Fatalf("query count %d %v", cnt, err)
	}
	if err := svc.Delete(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, n.ID); !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Fatalf("err = %v", err)
	}
}
