package engine

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

// ThreadItem is one row of GET /threads: a chat = the runs sharing a thread_id.
type ThreadItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`      // the first message's brief ("" when it has none)
	Status    string    `json:"status"`     // of the latest run
	CreatedAt time.Time `json:"created_at"` // first run
	UpdatedAt time.Time `json:"updated_at"` // latest run (the list order)
	Runs      int       `json:"runs"`
	// StyleguideID is the chat's style guide ("" = none).
	StyleguideID string `json:"styleguide_id"`
}

// ThreadParams page and search GET /threads.
type ThreadParams struct {
	Limit  int
	Offset int
	// Query matches the thread title (case-insensitive substring).
	Query string
}

// ListThreads lists the viewer's chat threads, most recently active first.
// more reports whether another page follows.
func (e *Engine) ListThreads(ctx context.Context, v Viewer, p ThreadParams) (items []ThreadItem, more bool, err error) {
	if p.Limit <= 0 {
		p.Limit = 30
	}
	p.Limit = min(p.Limit, 100)
	db := e.DB.WithContext(ctx)

	q := scoped(db, v).Model(&models.Run{})
	if needle := strings.ToLower(strings.TrimSpace(p.Query)); needle != "" {
		// Titles live in the JSON input, and SQLite's lower() only folds
		// ASCII — match the thread heads in Go instead.
		var heads []models.Run
		if err := scoped(db, v).Select("id", "input").Where("id = thread_id").Find(&heads).Error; err != nil {
			return nil, false, apperr.Internal(err)
		}
		ids := make([]string, 0)
		for i := range heads {
			if strings.Contains(strings.ToLower(threadTitle(heads[i].Input)), needle) {
				ids = append(ids, heads[i].ID)
			}
		}
		if len(ids) == 0 {
			return []ThreadItem{}, false, nil
		}
		q = q.Where("thread_id IN ?", ids)
	}

	var groups []struct {
		ThreadID string
		Runs     int
	}
	if err := q.Select("thread_id, COUNT(*) AS runs").Group("thread_id").
		Order("MAX(created_at) DESC, thread_id DESC").
		Limit(p.Limit + 1).Offset(max(p.Offset, 0)).Scan(&groups).Error; err != nil {
		return nil, false, apperr.Internal(err)
	}
	if len(groups) > p.Limit {
		groups, more = groups[:p.Limit], true
	}
	if len(groups) == 0 {
		return []ThreadItem{}, false, nil
	}

	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ThreadID
	}
	var runs []models.Run
	if err := scoped(db, v).Select("id", "thread_id", "status", "input", "created_at", "styleguide_id").
		Where("thread_id IN ?", ids).Order("created_at ASC, id ASC").Find(&runs).Error; err != nil {
		return nil, false, apperr.Internal(err)
	}
	type span struct{ first, last *models.Run }
	spans := make(map[string]*span, len(groups))
	for i := range runs {
		r := &runs[i]
		s := spans[r.ThreadID]
		if s == nil {
			s = &span{first: r}
			spans[r.ThreadID] = s
		}
		if r.ID == r.ThreadID {
			s.first = r
		}
		s.last = r
	}

	items = make([]ThreadItem, 0, len(groups))
	for _, g := range groups {
		s := spans[g.ThreadID]
		if s == nil {
			continue
		}
		items = append(items, ThreadItem{
			ID: g.ThreadID, Title: threadTitle(s.first.Input), Status: s.last.Status,
			CreatedAt: s.first.CreatedAt, UpdatedAt: s.last.CreatedAt, Runs: g.Runs,
			StyleguideID: s.first.StyleguideID,
		})
	}
	return items, more, nil
}

// threadTitle is the trimmed `brief` of a run input ("" when absent).
func threadTitle(input json.RawMessage) string {
	var in struct {
		Brief any `json:"brief"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return ""
	}
	s, _ := in.Brief.(string)
	return strings.TrimSpace(s)
}
