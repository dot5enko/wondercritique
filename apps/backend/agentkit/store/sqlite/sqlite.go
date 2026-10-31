// Package sqlite implements store.Store on top of any database/sql driver
// speaking SQLite (modernc.org/sqlite, glebarez/sqlite, mattn/go-sqlite3).
//
// The store is safe for concurrent use: all mutating operations are serialized
// through a mutex, which also keeps per-session Seq values contiguous.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
)

// Option configures the store.
type Option func(*Store)

// WithTablePrefix changes the table name prefix (default "agent_").
func WithTablePrefix(p string) Option { return func(s *Store) { s.table = p } }

// Store persists agent state in SQLite.
type Store struct {
	db    *sql.DB
	table string

	mu sync.Mutex // serializes writes so Seq stays contiguous
}

// New opens the store on db and creates its tables if needed. The schema is
// created idempotently; foreign keys are declared but deletes never rely on
// them (the pragma may be off on the caller's connection).
func New(db *sql.DB, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("agentkit/store/sqlite: nil *sql.DB")
	}
	s := &Store{db: db, table: "agent_"}
	for _, o := range opts {
		o(s)
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("agentkit/store/sqlite: migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ` + s.table + `sessions (
			id TEXT PRIMARY KEY,
			agent TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + s.table + `sessions_agent ON ` + s.table + `sessions (agent, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS ` + s.table + `messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			run_id TEXT NOT NULL DEFAULT '',
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + s.table + `messages_seq ON ` + s.table + `messages (session_id, seq)`,
		`CREATE TABLE IF NOT EXISTS ` + s.table + `runs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			agent TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL,
			finished_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS ` + s.table + `runs_session ON ` + s.table + `runs (session_id, started_at)`,
		`CREATE TABLE IF NOT EXISTS ` + s.table + `tool_calls (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			call_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			input TEXT NOT NULL DEFAULT '{}',
			output TEXT NOT NULL DEFAULT '',
			is_error INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + s.table + `tool_calls_run ON ` + s.table + `tool_calls (run_id)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func nanos(t time.Time) int64 { return t.UTC().UnixNano() }

func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateSession(ctx context.Context, sess *store.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.ID == "" {
		sess.ID = store.NewID()
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now()
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = sess.CreatedAt
	}
	meta, err := json.Marshal(sess.Metadata) // nil -> null
	if err != nil {
		return err
	}
	if string(meta) == "null" {
		meta = []byte("{}")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+s.table+`sessions (id, agent, title, metadata, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		sess.ID, sess.Agent, sess.Title, string(meta), nanos(sess.CreatedAt), nanos(sess.UpdatedAt))
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (*store.Session, error) {
	var (
		sess             store.Session
		meta             string
		created, updated int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, agent, title, metadata, created_at, updated_at FROM `+s.table+`sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.Agent, &sess.Title, &meta, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt = time.Unix(0, created).UTC()
	sess.UpdatedAt = time.Unix(0, updated).UTC()
	if err := json.Unmarshal([]byte(meta), &sess.Metadata); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) ListSessions(ctx context.Context, opts store.ListOptions) ([]store.Session, error) {
	q := `SELECT id, agent, title, metadata, created_at, updated_at FROM ` + s.table + `sessions`
	var args []any
	var where []string
	if opts.Agent != "" {
		where = append(where, "agent = ?")
		args = append(args, opts.Agent)
	}
	for k, v := range opts.Metadata { // map iteration order is fine: conditions are ANDed
		where = append(where, fmt.Sprintf("json_extract(metadata, '$.%s') = ?", k))
		args = append(args, sqlArg(v))
	}
	if len(where) > 0 {
		q += " WHERE " + joinAnd(where)
	}
	q += " ORDER BY created_at DESC, id DESC"
	if opts.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", opts.Limit, opts.Offset)
	} else if opts.Offset > 0 {
		q += " LIMIT -1 OFFSET " + fmt.Sprint(opts.Offset) // SQLite has no bare OFFSET
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Session
	for rows.Next() {
		var (
			sess             store.Session
			meta             string
			created, updated int64
		)
		if err := rows.Scan(&sess.ID, &sess.Agent, &sess.Title, &meta, &created, &updated); err != nil {
			return nil, err
		}
		sess.CreatedAt = time.Unix(0, created).UTC()
		sess.UpdatedAt = time.Unix(0, updated).UTC()
		if err := json.Unmarshal([]byte(meta), &sess.Metadata); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+s.table+`sessions WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		for _, q := range []string{
			`DELETE FROM ` + s.table + `messages WHERE session_id = ?`,
			`DELETE FROM ` + s.table + `runs WHERE session_id = ?`,
			`DELETE FROM ` + s.table + `tool_calls WHERE session_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) AppendMessages(ctx context.Context, sessionID, runID string, msgs ...llm.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one bool
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM `+s.table+`sessions WHERE id = ?`, sessionID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return err
		}
		var next int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM `+s.table+`messages WHERE session_id = ?`, sessionID,
		).Scan(&next); err != nil {
			return err
		}
		now := time.Now().UTC()
		for i, m := range msgs {
			blocks, err := json.Marshal(m)
			if err != nil {
				return err
			}
			next++
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO `+s.table+`messages (id, session_id, run_id, seq, role, message, created_at) VALUES (?,?,?,?,?,?,?)`,
				store.NewID(), sessionID, runID, next, string(m.Role), string(blocks), nanos(now.Add(time.Duration(i)))); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE `+s.table+`sessions SET updated_at = ? WHERE id = ?`, nanos(now), sessionID)
		return err
	})
}

func (s *Store) History(ctx context.Context, sessionID string) ([]store.StoredMessage, error) {
	var one bool
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM `+s.table+`sessions WHERE id = ?`, sessionID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, run_id, seq, message, created_at FROM `+s.table+`messages WHERE session_id = ? ORDER BY seq ASC`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.StoredMessage
	for rows.Next() {
		var (
			sm      store.StoredMessage
			raw     string
			created int64
		)
		if err := rows.Scan(&sm.ID, &sm.SessionID, &sm.RunID, &sm.Seq, &raw, &created); err != nil {
			return nil, err
		}
		sm.CreatedAt = time.Unix(0, created).UTC()
		if err := json.Unmarshal([]byte(raw), &sm.Message); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

func (s *Store) SaveRun(ctx context.Context, r *store.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = store.NewID()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	var finished any
	if !r.FinishedAt.IsZero() {
		finished = nanos(r.FinishedAt)
	}
	// Update first so a replacement keeps its insertion position.
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+s.table+`runs SET session_id=?, agent=?, model=?, status=?, error=?,
		 input_tokens=?, cached_input_tokens=?, output_tokens=?, reasoning_tokens=?,
		 started_at=?, finished_at=? WHERE id = ?`,
		r.SessionID, r.Agent, r.Model, string(r.Status), r.Error,
		r.Usage.InputTokens, r.Usage.CachedInputTokens, r.Usage.OutputTokens, r.Usage.ReasoningTokens,
		nanos(r.StartedAt), finished, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+s.table+`runs (id, session_id, agent, model, status, error,
		 input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, started_at, finished_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.SessionID, r.Agent, r.Model, string(r.Status), r.Error,
		r.Usage.InputTokens, r.Usage.CachedInputTokens, r.Usage.OutputTokens, r.Usage.ReasoningTokens,
		nanos(r.StartedAt), finished)
	return err
}

func runScan(row interface{ Scan(...any) error }) (*store.Run, error) {
	var (
		r                                store.Run
		status                           string
		input, cached, output, reasoning int64
		started                          int64
		finished                         sql.NullInt64
	)
	if err := row.Scan(&r.ID, &r.SessionID, &r.Agent, &r.Model, &status, &r.Error,
		&input, &cached, &output, &reasoning, &started, &finished); err != nil {
		return nil, err
	}
	r.Status = store.RunStatus(status)
	r.Usage = llm.Usage{InputTokens: int(input), CachedInputTokens: int(cached), OutputTokens: int(output), ReasoningTokens: int(reasoning)}
	r.StartedAt = time.Unix(0, started).UTC()
	if finished.Valid {
		r.FinishedAt = time.Unix(0, finished.Int64).UTC()
	}
	return &r, nil
}

const runCols = `id, session_id, agent, model, status, error,
	 input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, started_at, finished_at`

func (s *Store) GetRun(ctx context.Context, id string) (*store.Run, error) {
	r, err := runScan(s.db.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM `+s.table+`runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return r, err
}

func (s *Store) ListRuns(ctx context.Context, sessionID string) ([]store.Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM `+s.table+`runs WHERE session_id = ? ORDER BY started_at ASC, rowid ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Run
	for rows.Next() {
		r, err := runScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) SaveToolCall(ctx context.Context, rec *store.ToolCallRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.ID == "" {
		rec.ID = store.NewID()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	input := "{}"
	if len(rec.Input) > 0 {
		input = string(rec.Input)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+s.table+`tool_calls (id, run_id, session_id, call_id, name, input, output, is_error, duration_ms, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.RunID, rec.SessionID, rec.CallID, rec.Name, input, rec.Output, boolInt(rec.IsError), rec.Duration.Milliseconds(), nanos(rec.CreatedAt))
	return err
}

func (s *Store) ListToolCalls(ctx context.Context, runID string) ([]store.ToolCallRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, session_id, call_id, name, input, output, is_error, duration_ms, created_at
		 FROM `+s.table+`tool_calls WHERE run_id = ? ORDER BY rowid ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ToolCallRecord
	for rows.Next() {
		var (
			rec      store.ToolCallRecord
			input    string
			isErr    int
			duration int64
			created  int64
		)
		if err := rows.Scan(&rec.ID, &rec.RunID, &rec.SessionID, &rec.CallID, &rec.Name, &input, &rec.Output, &isErr, &duration, &created); err != nil {
			return nil, err
		}
		rec.Input = json.RawMessage(input)
		rec.IsError = isErr != 0
		rec.Duration = time.Duration(duration) * time.Millisecond
		rec.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) RecoverInterrupted(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+s.table+`runs SET status = ?, finished_at = ? WHERE status = ?`,
		string(store.RunInterrupted), nanos(time.Now().UTC()), string(store.RunRunning))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sqlArg adapts a scalar metadata value for an `=` comparison against
// json_extract. Non-scalar values are compared by their JSON encoding.
func sqlArg(v any) any {
	switch v.(type) {
	case string, int64, int, int32, float64, bool:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
