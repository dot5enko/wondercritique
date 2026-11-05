package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

func TestListThreadsPagesAndSearches(t *testing.T) {
	h := setup(t)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	add := func(owner, thread, brief, status string, at time.Time) string {
		t.Helper()
		in, _ := json.Marshal(map[string]string{"brief": brief})
		r := &models.Run{Base: models.Base{ID: models.NewID(), CreatedAt: at}, OwnerID: owner, ThreadID: thread, Input: in, Status: status}
		if r.ThreadID == "" {
			r.ThreadID = r.ID
		}
		if err := h.e.DB.Create(r).Error; err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	// user1: 5 threads, oldest first; thread 0 gets a late follow-up → newest.
	var ids []string
	for i := range 5 {
		ids = append(ids, add("user1", "", fmt.Sprintf("Pricing page %d", i), models.RunCompleted, base.Add(time.Duration(i)*time.Hour)))
	}
	add("user1", ids[0], "make it darker", models.RunRunning, base.Add(10*time.Hour))
	add("user1", "", "Привіт Login Form", models.RunFailed, base.Add(5*time.Hour))
	add("user2", "", "pricing of someone else", models.RunCompleted, base.Add(20*time.Hour))

	type page struct {
		Items      []ThreadItem `json:"items"`
		NextOffset *int         `json:"next_offset"`
	}
	get := func(path, token string) page {
		t.Helper()
		w := h.do("GET", path, "", token)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := get("/threads?limit=4", h.user)
	if len(p.Items) != 4 || p.NextOffset == nil || *p.NextOffset != 4 {
		t.Fatalf("page 1 = %+v", p)
	}
	first := p.Items[0]
	if first.ID != ids[0] || first.Title != "Pricing page 0" || first.Status != models.RunRunning || first.Runs != 2 ||
		!first.UpdatedAt.Equal(base.Add(10*time.Hour)) || !first.CreatedAt.Equal(base) {
		t.Errorf("latest thread = %+v", first)
	}
	if p.Items[1].Title != "Привіт Login Form" {
		t.Errorf("order: %+v", p.Items)
	}
	p = get("/threads?limit=4&offset=4", h.user)
	if len(p.Items) != 2 || p.NextOffset != nil || p.Items[1].Title != "Pricing page 1" {
		t.Fatalf("page 2 = %+v", p)
	}

	if p = get("/threads?q=PRICING", h.user); len(p.Items) != 5 {
		t.Errorf("search pricing = %d", len(p.Items))
	}
	if p = get("/threads?q=привіт", h.user); len(p.Items) != 1 || p.Items[0].Title != "Привіт Login Form" {
		t.Errorf("unicode search = %+v", p.Items)
	}
	if p = get("/threads?q=darker", h.user); len(p.Items) != 0 {
		t.Errorf("follow-up briefs are not titles: %+v", p.Items)
	}
	if p = get("/threads?q=pricing&limit=2", h.user); len(p.Items) != 2 || p.NextOffset == nil {
		t.Errorf("search paging = %+v", p)
	}
	if p = get("/threads", h.other); len(p.Items) != 1 {
		t.Errorf("other user = %+v", p.Items)
	}
	if p = get("/threads", h.admin); len(p.Items) != 7 {
		t.Errorf("admin = %d", len(p.Items))
	}
	if w := h.do("GET", "/threads?limit=x", "", h.user); w.Code != http.StatusBadRequest {
		t.Errorf("bad limit: %d", w.Code)
	}
}
