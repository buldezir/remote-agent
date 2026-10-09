// Package store persists rad's state in SQLite.
//
// Entities live in typed tables (projects, sessions, turns). Every mutation also
// upserts a row in the per-stream change feed (`changes`), which keeps only the
// latest event per entity. A stream's seq is gap-free for live delivery, and
// replaying a stream from seq 0 yields a complete snapshot.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"remote-agent/internal/model"
)

//go:embed schema.sql
var schema string

var ErrNotFound = errors.New("not found")

type Store struct {
	db      *sql.DB
	mu      sync.Mutex // serializes writes so events are published in seq order
	publish func([]model.Event)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, publish: func([]model.Event) {}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// OnCommit registers the function that receives events after each committed write.
// It is called with the write lock held, so events arrive in seq order.
func (s *Store) OnCommit(f func([]model.Event)) { s.publish = f }

func NewID() string { return uuid.Must(uuid.NewV7()).String() }

func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ServerID returns a stable identifier for this rad installation.
func (s *Store) ServerID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='server_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = NewID()
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO meta(key, value) VALUES('server_id', ?)`, id)
	if err != nil {
		return "", err
	}
	return s.ServerID(ctx)
}

// W is a write transaction. Methods that change entities also emit events.
type W struct {
	ctx    context.Context
	tx     *sql.Tx
	now    time.Time
	events []model.Event
}

// Write runs f in a transaction and publishes the emitted events after commit.
func (s *Store) Write(ctx context.Context, f func(w *W) error) ([]model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	w := &W{ctx: ctx, tx: tx, now: time.Now().UTC()}
	if err := f(w); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if len(w.events) > 0 {
		s.publish(w.events)
	}
	return w.events, nil
}

func (w *W) Now() time.Time { return w.now }

// Emit appends ev to its stream's change feed, replacing the previous event for
// the same entity, and assigns the next seq.
func (w *W) Emit(ev model.Event) error {
	key := ev.EntityKey()
	if key == "" {
		return fmt.Errorf("event %s has no entity", ev.Type)
	}
	var seq int64
	err := w.tx.QueryRowContext(w.ctx,
		`INSERT INTO streams(stream, last_seq) VALUES(?, 1)
		 ON CONFLICT(stream) DO UPDATE SET last_seq = last_seq + 1
		 RETURNING last_seq`, ev.Stream).Scan(&seq)
	if err != nil {
		return err
	}
	ev.Seq, ev.TS = seq, w.now
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = w.tx.ExecContext(w.ctx,
		`INSERT INTO changes(stream, entity, seq, type, data) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(stream, entity) DO UPDATE SET seq=excluded.seq, type=excluded.type, data=excluded.data`,
		ev.Stream, key, seq, ev.Type, data)
	if err != nil {
		return err
	}
	w.events = append(w.events, ev)
	return nil
}

func (w *W) PutProject(p *model.Project) error {
	data, _ := json.Marshal(p)
	_, err := w.tx.ExecContext(w.ctx,
		`INSERT INTO projects(id, path, data) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET path=excluded.path, data=excluded.data`, p.ID, p.Path, data)
	if err != nil {
		return err
	}
	cp := *p
	return w.Emit(model.Event{Stream: model.IndexStream, Type: model.EvProjectUpserted, Project: &cp})
}

func (w *W) DeleteProject(id string) error {
	if _, err := w.tx.ExecContext(w.ctx, `DELETE FROM projects WHERE id=?`, id); err != nil {
		return err
	}
	return w.Emit(model.Event{Stream: model.IndexStream, Type: model.EvProjectRemoved, ID: id})
}

// PutSession saves s and emits it on both the session stream and the index.
func (w *W) PutSession(s *model.Session) error {
	s.UpdatedAt = w.now
	data, _ := json.Marshal(s)
	_, err := w.tx.ExecContext(w.ctx,
		`INSERT INTO sessions(id, project_id, data) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET data=excluded.data`,
		s.ID, s.ProjectID, data)
	if err != nil {
		return err
	}
	a, b := *s, *s
	if err := w.Emit(model.Event{Stream: model.SessionStream(s.ID), Type: model.EvSessionUpserted, Session: &a}); err != nil {
		return err
	}
	return w.Emit(model.Event{Stream: model.IndexStream, Type: model.EvSessionUpserted, Session: &b})
}

// SetItemCount records how many items a session has (the next item's order).
func (w *W) SetItemCount(sessionID string, n int64) error {
	_, err := w.tx.ExecContext(w.ctx, `UPDATE sessions SET item_count=? WHERE id=?`, n, sessionID)
	return err
}

func (w *W) PutTurn(t *model.Turn) error {
	data, _ := json.Marshal(t)
	_, err := w.tx.ExecContext(w.ctx,
		`INSERT INTO turns(id, session_id, n, data) VALUES(?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET data=excluded.data`, t.ID, t.SessionID, t.N, data)
	if err != nil {
		return err
	}
	cp := *t
	return w.Emit(model.Event{Stream: model.SessionStream(t.SessionID), Type: model.EvTurnUpserted, Turn: &cp})
}

// PutItem upserts a transcript item. Items live only in the change feed.
func (w *W) PutItem(it *model.Item) error {
	if it.CreatedAt.IsZero() {
		it.CreatedAt = w.now
	}
	it.UpdatedAt = w.now
	cp := *it
	return w.Emit(model.Event{Stream: model.SessionStream(it.SessionID), Type: model.EvItemUpserted, Item: &cp})
}

// Receipt returns the stored result of an already-applied command.
func (w *W) Receipt(commandID string) (json.RawMessage, bool, error) {
	if commandID == "" {
		return nil, false, nil
	}
	var res []byte
	err := w.tx.QueryRowContext(w.ctx, `SELECT result FROM receipts WHERE command_id=?`, commandID).Scan(&res)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return res, err == nil, err
}

func (w *W) PutReceipt(commandID string, result any) error {
	if commandID == "" {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = w.tx.ExecContext(w.ctx, `INSERT INTO receipts(command_id, result, created_at) VALUES(?, ?, ?)`,
		commandID, data, w.now.Unix())
	return err
}

// Reads.

func (s *Store) Projects(ctx context.Context) ([]*model.Project, error) {
	return queryJSON[model.Project](ctx, s.db, `SELECT data FROM projects ORDER BY json_extract(data, '$.name')`)
}

func (s *Store) Project(ctx context.Context, id string) (*model.Project, error) {
	return one(queryJSON[model.Project](ctx, s.db, `SELECT data FROM projects WHERE id=?`, id))
}

func (s *Store) ProjectByPath(ctx context.Context, path string) (*model.Project, error) {
	return one(queryJSON[model.Project](ctx, s.db, `SELECT data FROM projects WHERE path=?`, path))
}

func (s *Store) Sessions(ctx context.Context) ([]*model.Session, error) {
	return querySessions(ctx, s.db, `SELECT data, item_count FROM sessions ORDER BY json_extract(data, '$.updatedAt') DESC`)
}

func (s *Store) Session(ctx context.Context, id string) (*model.Session, error) {
	return one(querySessions(ctx, s.db, `SELECT data, item_count FROM sessions WHERE id=?`, id))
}

func (s *Store) Turns(ctx context.Context, sessionID string) ([]*model.Turn, error) {
	return queryJSON[model.Turn](ctx, s.db, `SELECT data FROM turns WHERE session_id=? ORDER BY n`, sessionID)
}

func (s *Store) Turn(ctx context.Context, id string) (*model.Turn, error) {
	return one(queryJSON[model.Turn](ctx, s.db, `SELECT data FROM turns WHERE id=?`, id))
}

// Items returns the latest state of every item in a session, in transcript order.
func (s *Store) Items(ctx context.Context, sessionID string) ([]*model.Item, error) {
	evs, err := queryJSON[model.Event](ctx, s.db,
		`SELECT data FROM changes WHERE stream=? AND type=? ORDER BY json_extract(data, '$.item.order')`,
		model.SessionStream(sessionID), model.EvItemUpserted)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Item, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Item)
	}
	return out, nil
}

// ItemsWithStatus finds items with a status (and kind, if non-empty) across
// all sessions. Used for restart recovery.
func (s *Store) ItemsWithStatus(ctx context.Context, kind model.ItemKind, status model.ItemStatus) ([]*model.Item, error) {
	evs, err := queryJSON[model.Event](ctx, s.db,
		`SELECT data FROM changes WHERE type=? AND json_extract(data, '$.item.status')=?
		 AND (?='' OR json_extract(data, '$.item.kind')=?)`,
		model.EvItemUpserted, string(status), string(kind), string(kind))
	if err != nil {
		return nil, err
	}
	out := make([]*model.Item, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Item)
	}
	return out, nil
}

// Changes returns the events of stream with seq > after, plus the stream's
// current last seq, read in one consistent snapshot.
func (s *Store) Changes(ctx context.Context, stream string, after int64) ([]model.Event, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var last int64
	err = tx.QueryRowContext(ctx, `SELECT last_seq FROM streams WHERE stream=?`, stream).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT data FROM changes WHERE stream=? AND seq>? ORDER BY seq`, stream, after)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, 0, err
		}
		var ev model.Event
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil, 0, err
		}
		out = append(out, ev)
	}
	return out, last, rows.Err()
}

func querySessions(ctx context.Context, db *sql.DB, q string, args ...any) ([]*model.Session, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Session
	for rows.Next() {
		var data []byte
		var n int64
		if err := rows.Scan(&data, &n); err != nil {
			return nil, err
		}
		v := new(model.Session)
		if err := json.Unmarshal(data, v); err != nil {
			return nil, err
		}
		v.ItemCount = n
		out = append(out, v)
	}
	return out, rows.Err()
}

func queryJSON[T any](ctx context.Context, db *sql.DB, q string, args ...any) ([]*T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*T
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		v := new(T)
		if err := json.Unmarshal(data, v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func one[T any](xs []*T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	if len(xs) == 0 {
		return nil, ErrNotFound
	}
	return xs[0], nil
}
