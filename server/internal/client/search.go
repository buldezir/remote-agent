package client

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"remote-agent/internal/model"
)

// Finding in a transcript, and searching sessions on the servers, as
// RAKit's TranscriptFind and SessionSearch have it (web/src/store's
// transcriptFind.ts and sessionSearch.ts, Android's TranscriptFind and
// SessionSearch).

// FindMatches are the ids of the items whose text holds the query,
// ignoring case, in the order given, the transcript's. An item's text is
// what rad's search indexes of it (model.SearchText). The query is trimmed
// and taken whole, spaces included; an empty one finds nothing.
func FindMatches(items []*model.Item, query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var ids []string
	for _, it := range items {
		if strings.Contains(strings.ToLower(model.SearchText(it)), q) {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// SearchMinLength is the shortest query, in characters, that SessionSearch
// searches for.
const SearchMinLength = 2

// SearchActive reports whether a query is long enough to search for;
// shorter, the lists show their usual rows.
func SearchActive(query string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(query)) >= SearchMinLength
}

// Hit is a session that a search found, on the server with id ServerID.
type Hit struct {
	ServerID string
	Result   model.SearchResult
}

// ID tells hits apart across servers: "<serverId>/<sessionId>".
func (h Hit) ID() string {
	id := ""
	if h.Result.Session != nil {
		id = h.Result.Session.ID
	}
	return h.ServerID + "/" + id
}

// MergeSearchResults interleaves the servers' results, given in the app's
// order of servers: every server's first, then every server's second, and
// so on, each server's own order kept. One server's scores mean nothing
// to another's, so round-robin is the fair merge.
func MergeSearchResults(lists [][]Hit) []Hit {
	var out []Hit
	for rank := 0; ; rank++ {
		took := false
		for _, l := range lists {
			if rank < len(l) {
				out = append(out, l[rank])
				took = true
			}
		}
		if !took {
			return out
		}
	}
}

// SearchServer is a server SessionSearch searches: its connection, with
// the id and name the app knows it by.
type SearchServer struct {
	ID   string
	Name string
	Conn *ServerConnection
}

// SearchFailure is a server whose search failed, and why.
type SearchFailure struct {
	Server  string
	Message string
}

// SessionSearch searches the sessions on the servers for Query as it's
// typed. Debounce after the last change to Query or IncludeArchived, it
// asks every connected server that searches (ServerInfo.Search) at once,
// and merges their results as they answer (MergeSearchResults). An answer
// to an older query is dropped. A query shorter than SearchMinLength
// searches nothing and clears the results.
//
// Its exported fields belong to the dispatcher, as its methods do; it
// posts the answers there.
type SessionSearch struct {
	Query string
	// IncludeArchived searches archived sessions too; on by default.
	IncludeArchived bool

	Hits []Hit
	// IsSearching: a search is due, or a server is still answering.
	IsSearching bool
	// Failures are the servers whose search failed, in the servers' order.
	Failures []SearchFailure
	// More: a server has more sessions that match than it returned.
	More bool
	// Indexing names the servers still indexing sessions an older rad
	// wrote, which may be missing from their results.
	Indexing []string
	// Unsupported names the connected servers that don't search, whose rad
	// needs updating.
	Unsupported []string
	// Version counts changes, for views that cache what they drew.
	Version uint64

	Debounce time.Duration
	Limit    int // sessions from each server

	d       Dispatcher
	servers func() []SearchServer
	// search is the call to a server, which tests replace.
	search func(ctx context.Context, c *ServerConnection, query string, archived bool, limit int) (model.SearchResults, error)
	gen    int
	timer  *time.Timer
	cancel context.CancelFunc
}

// NewSessionSearch makes a search over the servers servers returns, in the
// app's order, when a search starts.
func NewSessionSearch(d Dispatcher, servers func() []SearchServer) *SessionSearch {
	return &SessionSearch{
		IncludeArchived: true,
		Debounce:        250 * time.Millisecond,
		Limit:           30,
		d:               d,
		servers:         servers,
		search: func(ctx context.Context, c *ServerConnection, query string, archived bool, limit int) (model.SearchResults, error) {
			return c.Search(ctx, query, archived, limit)
		},
	}
}

// Active reports whether the query is long enough to search for.
func (s *SessionSearch) Active() bool { return SearchActive(s.Query) }

func (s *SessionSearch) SetQuery(q string) {
	if q == s.Query {
		return
	}
	// Spaces around the words change nothing.
	same := strings.TrimSpace(q) == strings.TrimSpace(s.Query)
	s.Query = q
	if !same {
		s.changed()
	}
}

func (s *SessionSearch) SetIncludeArchived(on bool) {
	if on == s.IncludeArchived {
		return
	}
	s.IncludeArchived = on
	s.changed()
}

// Refresh searches again for the query, as when the servers changed.
func (s *SessionSearch) Refresh() { s.changed() }

// Stop drops the search under way, if any.
func (s *SessionSearch) Stop() {
	s.gen++
	s.stop()
	s.IsSearching = false
	s.Version++
}

func (s *SessionSearch) stop() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

func (s *SessionSearch) changed() {
	s.gen++
	s.stop()
	s.Version++
	if !s.Active() {
		s.Hits, s.Failures, s.More, s.Indexing, s.Unsupported = nil, nil, false, nil, nil
		s.IsSearching = false
		return
	}
	// The results so far stay until the first answer replaces them.
	s.IsSearching = true
	gen := s.gen
	s.timer = time.AfterFunc(s.Debounce, func() {
		s.d.Post(func() {
			if gen == s.gen {
				s.run(gen)
			}
		})
	})
}

func (s *SessionSearch) run(gen int) {
	query, archived, limit := strings.TrimSpace(s.Query), s.IncludeArchived, s.Limit
	var targets []SearchServer
	var unsupported []string
	for _, srv := range s.servers() {
		c := srv.Conn
		if c == nil || !c.IsConnected() || c.Info == nil {
			continue
		}
		if !c.Info.Search {
			unsupported = append(unsupported, srv.Name)
			continue
		}
		targets = append(targets, srv)
	}
	s.Unsupported = unsupported
	s.Version++
	if len(targets) == 0 {
		s.Hits, s.Failures, s.More, s.Indexing = nil, nil, false, nil
		s.IsSearching = false
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	// What each server answered: changed only on the dispatcher.
	lists := make([][]Hit, len(targets))
	failed := make([]string, len(targets))
	indexing := make([]bool, len(targets))
	more := false
	pending := len(targets)
	for i, srv := range targets {
		go func() {
			res, err := s.search(ctx, srv.Conn, query, archived, limit)
			s.d.Post(func() {
				if gen != s.gen {
					return
				}
				pending--
				if err != nil {
					failed[i] = err.Error()
				} else {
					hits := make([]Hit, 0, len(res.Results))
					for _, r := range res.Results {
						if r.Session != nil {
							hits = append(hits, Hit{ServerID: srv.ID, Result: r})
						}
					}
					lists[i] = hits
					more = more || res.More
					indexing[i] = res.Indexing
				}
				s.Hits = MergeSearchResults(lists)
				s.Failures, s.Indexing = nil, nil
				for j, t := range targets {
					if failed[j] != "" {
						s.Failures = append(s.Failures, SearchFailure{Server: t.Name, Message: failed[j]})
					}
					if indexing[j] {
						s.Indexing = append(s.Indexing, t.Name)
					}
				}
				s.More = more
				s.IsSearching = pending > 0
				if pending == 0 {
					cancel()
					s.cancel = nil
				}
				s.Version++
			})
		}()
	}
}
