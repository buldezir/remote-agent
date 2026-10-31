package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"remote-agent/internal/model"
)

// Search indexes each item's model.SearchText, and each session's title and
// branch, in search_docs, which the FTS5 table search_fts indexes (see
// schema.sql). Writes keep it in step; IndexForSearch adds what an older rad
// wrote.

// searchVersion names what the index holds. IndexForSearch indexes
// everything again when the stored one differs, as after a change to what it
// indexes.
const searchVersion = "1"

// maxSearchText caps an item's indexed text, such as a prompt with a log
// pasted into it.
const maxSearchText = 64 << 10

const (
	markOpen  = "\x02" // around matches in snippet(); stripped from indexed text
	markClose = "\x03"
)

// imageLink matches the target of a Markdown image rad stores, whose id
// would only add noise to the index.
var imageLink = regexp.MustCompile(`\]\(rad-image:[^)\s]*\)`)

func searchDocText(s string) string {
	if strings.Contains(s, "rad-image:") {
		s = imageLink.ReplaceAllString(s, "]")
	}
	s = strings.NewReplacer(markOpen, " ", markClose, " ").Replace(s)
	if len(s) > maxSearchText {
		s = s[:maxSearchText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func sessionDocKey(id string) string { return "session:" + id }

// indexItem keeps an item's row in step with its text; an unchanged text
// leaves the index alone, as for a status change.
func (w *W) indexItem(it *model.Item) error {
	text := searchDocText(model.SearchText(it))
	if strings.TrimSpace(text) == "" {
		_, err := w.tx.ExecContext(w.ctx, `DELETE FROM search_docs WHERE key=?`, it.ID)
		return err
	}
	_, err := w.tx.ExecContext(w.ctx,
		`INSERT INTO search_docs(key, session_id, item_id, kind, body) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET body=excluded.body, kind=excluded.kind WHERE body IS NOT excluded.body`,
		it.ID, it.SessionID, it.ID, string(it.Kind), text)
	return err
}

func (w *W) indexSession(s *model.Session) error {
	_, err := w.tx.ExecContext(w.ctx,
		`INSERT INTO search_docs(key, session_id, kind, title, body) VALUES(?, ?, 'session', ?, ?)
		 ON CONFLICT(key) DO UPDATE SET title=excluded.title, body=excluded.body
		 WHERE title IS NOT excluded.title OR body IS NOT excluded.body`,
		sessionDocKey(s.ID), s.ID, searchDocText(s.Title), searchDocText(s.Workspace.Branch))
	return err
}

// prepareSearch notes whether the index lacks sessions an older rad wrote,
// for IndexForSearch to add. A database without sessions has nothing to add.
func (s *Store) prepareSearch(ctx context.Context) error {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='search_version'`).Scan(&v)
	if err == nil && v == searchVersion {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var has bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sessions)`).Scan(&has); err != nil {
		return err
	}
	if !has {
		return s.setSearchVersion(ctx)
	}
	s.indexing.Store(true)
	return nil
}

func (s *Store) setSearchVersion(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('search_version', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, searchVersion)
	return err
}

// Indexing says whether IndexForSearch has sessions left to add, so that
// search may miss older ones.
func (s *Store) Indexing() bool { return s.indexing.Load() }

// IndexForSearch adds the sessions an older rad wrote to the index, on the
// first start of a rad with search (or of one that indexes more). It takes
// the newest first, a few sessions to a transaction, so that writes go on
// meanwhile and search soon finds the recent ones. It reads only the fields
// search needs, leaving tool output in SQLite. rad serve runs it in the
// background; other commands open the store without it.
func (s *Store) IndexForSearch(ctx context.Context) error {
	if !s.indexing.Load() {
		return nil
	}
	cursor := int64(math.MaxInt64)
	for {
		var n int
		_, err := s.Write(ctx, func(w *W) error {
			var err error
			n, cursor, err = w.indexSessionsBefore(cursor, searchBatch)
			return err
		})
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	if err := s.setSearchVersion(ctx); err != nil {
		return err
	}
	s.indexing.Store(false)
	return nil
}

// indexSessionsBefore indexes again the n sessions before rowid cursor,
// newest first (the rowid only grows), replacing what the index has of them.
func (w *W) indexSessionsBefore(cursor int64, n int) (int, int64, error) {
	rows, err := w.tx.QueryContext(w.ctx,
		`SELECT rowid, id, json_extract(data, '$.title'), coalesce(json_extract(data, '$.workspace.branch'), '')
		 FROM sessions WHERE rowid < ? ORDER BY rowid DESC LIMIT ?`, cursor, n)
	if err != nil {
		return 0, cursor, err
	}
	var sessions []*model.Session
	for rows.Next() {
		s := new(model.Session)
		if err := rows.Scan(&cursor, &s.ID, &s.Title, &s.Workspace.Branch); err != nil {
			rows.Close()
			return 0, cursor, err
		}
		sessions = append(sessions, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, cursor, err
	}
	for _, s := range sessions {
		if _, err := w.tx.ExecContext(w.ctx, `DELETE FROM search_docs WHERE session_id=?`, s.ID); err != nil {
			return 0, cursor, err
		}
		if err := w.indexSession(s); err != nil {
			return 0, cursor, err
		}
		items, err := w.tx.QueryContext(w.ctx,
			`SELECT json_remove(json_extract(data, '$.item'), '$.tool.output', '$.images', '$.approval.input', '$.approval.options')
			 FROM changes WHERE stream=? AND +type=?`, model.SessionStream(s.ID), model.EvItemUpserted)
		if err != nil {
			return 0, cursor, err
		}
		var list []*model.Item
		for items.Next() {
			var data []byte
			if err := items.Scan(&data); err != nil {
				items.Close()
				return 0, cursor, err
			}
			it := new(model.Item)
			if err := json.Unmarshal(data, it); err != nil {
				items.Close()
				return 0, cursor, err
			}
			list = append(list, it)
		}
		items.Close()
		if err := items.Err(); err != nil {
			return 0, cursor, err
		}
		for _, it := range list {
			if err := w.indexItem(it); err != nil {
				return 0, cursor, err
			}
		}
	}
	return len(sessions), cursor, nil
}

// SearchParams are session.search's.
type SearchParams struct {
	Query    string `json:"query"`
	Archived bool   `json:"archived,omitempty"` // archived sessions too
	Limit    int    `json:"limit,omitempty"`    // sessions at most: default 30, at most 100
}

// searchBatch is how many sessions IndexForSearch indexes in a
// transaction: some 50 ms of holding the store's writes.
const searchBatch = 4

const (
	defaultSearchLimit = 30
	maxSearchLimit     = 100
	snippetTokens      = 16
)

// Search finds the sessions whose items, title or branch hold every word of
// the query, the last word, or each, as the start of one. Each comes once,
// with the item that matches best (else its title or branch) and how many of
// its items match. Sessions rank by how well they match (BM25, a title
// counting for more), damped by their age so that among good matches recent
// ones come first: a session idle for 30 days counts half as much. more says
// whether sessions past the limit matched too.
func (s *Store) Search(ctx context.Context, p SearchParams) (results []model.SearchResult, more bool, err error) {
	query := ftsQuery(p.Query)
	results = []model.SearchResult{}
	if query == "" {
		return results, false, nil
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	limit = min(limit, maxSearchLimit)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// One pass over the matches: each session's best item (a title or
	// branch only when no item matches), how many items match, and whether
	// its title or branch does, which doubles its score. Scores are BM25's,
	// negative, the best the lowest.
	rows, err := tx.QueryContext(ctx, `
		SELECT s.data, s.item_count, h.matches, h.id, coalesce(h.item_id, ''), h.kind
		FROM (
		  SELECT session_id, id, item_id, kind, min(score) AS score, count(item_id) AS matches, total(item_id IS NULL) AS titled
		  FROM (
		    SELECT d.session_id, d.id, d.item_id, d.kind, bm25(search_fts, 4.0, 1.0) + (d.item_id IS NULL) * 1e9 AS score
		    FROM search_fts JOIN search_docs d ON d.id = search_fts.rowid
		    WHERE search_fts MATCH ?
		    LIMIT -1 -- keeps bm25() in the query that matches
		  )
		  GROUP BY session_id
		) h
		JOIN sessions s ON s.id = h.session_id
		WHERE ? OR coalesce(json_extract(s.data, '$.archived'), 0) = 0
		ORDER BY (CASE WHEN h.score >= 1e8 THEN h.score - 1e9 ELSE h.score END) * (1 + h.titled)
		           / (1 + max(0, julianday('now') - julianday(json_extract(s.data, '$.updatedAt'))) / 30.0),
		         json_extract(s.data, '$.updatedAt') DESC
		LIMIT ?`, query, p.Archived, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search: %w", err)
	}
	var docs []int64
	for rows.Next() {
		var data []byte
		var r model.SearchResult
		var n, doc int64
		var kind string
		if err := rows.Scan(&data, &n, &r.Matches, &doc, &r.ItemID, &kind); err != nil {
			rows.Close()
			return nil, false, err
		}
		r.Session = new(model.Session)
		if err := json.Unmarshal(data, r.Session); err != nil {
			rows.Close()
			return nil, false, err
		}
		r.Session.ItemCount = n
		if r.ItemID != "" {
			r.ItemKind = model.ItemKind(kind)
		}
		results = append(results, r)
		docs = append(docs, doc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(results) > limit {
		results, docs, more = results[:limit], docs[:limit], true
	}
	if len(docs) == 0 {
		return results, more, nil
	}
	// Snippets, for the rows shown only: snippet() works in a query that
	// matches.
	args := []any{markOpen, markClose, snippetTokens, query}
	marks := make([]string, len(docs))
	for i, d := range docs {
		marks[i] = "?"
		args = append(args, d)
	}
	srows, err := tx.QueryContext(ctx,
		`SELECT rowid, snippet(search_fts, -1, ?, ?, '…', ?) FROM search_fts WHERE search_fts MATCH ? AND rowid IN (`+strings.Join(marks, ",")+`)`,
		args...)
	if err != nil {
		return nil, false, fmt.Errorf("search: %w", err)
	}
	defer srows.Close()
	snippets := map[int64]string{}
	for srows.Next() {
		var id int64
		var snip string
		if err := srows.Scan(&id, &snip); err != nil {
			return nil, false, err
		}
		snippets[id] = snip
	}
	if err := srows.Err(); err != nil {
		return nil, false, err
	}
	for i := range results {
		results[i].Snippet = snippetParts(snippets[docs[i]])
	}
	return results, more, nil
}

// ftsQuery makes an FTS5 query of what the user typed: every word must be
// there, as the start of a word, so that results come as they type, or as a
// whole word when it's shorter than three letters (a prefix of one or two
// letters matches most of the index, which is slow). A "quoted phrase" must
// be there as it is. Each is quoted, so that nothing they type is FTS5's
// syntax.
func ftsQuery(q string) string {
	if r := []rune(q); len(r) > 256 {
		q = string(r[:256])
	}
	var terms []string
	add := func(t string, prefix bool) {
		if len(terms) >= 16 || !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			return
		}
		term := `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		if prefix && utf8.RuneCountInString(t) >= 3 {
			term += "*"
		}
		terms = append(terms, term)
	}
	for {
		before, phrase, found := strings.Cut(q, `"`)
		for _, f := range strings.Fields(before) {
			add(f, true)
		}
		if !found {
			break
		}
		phrase, rest, closed := strings.Cut(phrase, `"`)
		if !closed {
			for _, f := range strings.Fields(phrase) {
				add(f, true)
			}
			break
		}
		add(phrase, false)
		q = rest
	}
	return strings.Join(terms, " ")
}

// snippetParts splits a snippet at its marks, with its whitespace
// collapsed, as clients show it on a line or two.
func snippetParts(s string) []model.SnippetPart {
	s = strings.Join(strings.Fields(s), " ")
	parts := []model.SnippetPart{}
	for s != "" {
		before, rest, found := strings.Cut(s, markOpen)
		if before != "" {
			parts = append(parts, model.SnippetPart{Text: before})
		}
		if !found {
			break
		}
		match, after, _ := strings.Cut(rest, markClose)
		if match != "" {
			parts = append(parts, model.SnippetPart{Text: match, Match: true})
		}
		s = after
	}
	return parts
}
