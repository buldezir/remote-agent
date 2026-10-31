package client

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// As RAKit's TranscriptFindTests and SessionSearchTests, and
// web/src/store's transcriptFind.test.ts and sessionSearch.test.ts.

func TestFindMatchesTheSharedVectors(t *testing.T) {
	ev := fixture[notificationFrame[eventsParams]](t, "notification_events_session.json").Params
	var items []*model.Item
	for _, e := range ev.Events {
		if e.Item != nil {
			items = append(items, e.Item)
		}
	}
	for query, want := range map[string][]string{
		"TESTS":       {"i1", "i4", "i8"},
		"which":       {"i6"},
		"go test":     {"i3"},
		"  go test  ": {"i3"},
		"test output": nil, // reasoning isn't searched
		"sub-agent":   nil, // nor sub-agents' items
		"":            nil,
		"   ":         nil,
	} {
		if got := FindMatches(items, query); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", query, got, want)
		}
	}
}

// The searchText cases every client checks, on model.SearchText, which
// FindMatches uses.
func TestSearchTextOfToolCalls(t *testing.T) {
	tool := func(title, input string) *model.Item {
		return &model.Item{ID: "t", Kind: model.ItemToolCall, Tool: &model.ToolCall{Title: title, Input: json.RawMessage(input)}}
	}
	for _, c := range []struct {
		it   *model.Item
		want string
	}{
		{tool("Run", `{"command":["git","status"]}`), "Run\ngit status"},
		{tool("go test ./...", `{"command":"go test ./..."}`), "go test ./..."},
		{tool("Read", ""), "Read"},
	} {
		if got := model.SearchText(c.it); got != c.want {
			t.Errorf("%s: %q, want %q", c.it.Tool.Title, got, c.want)
		}
	}
}

func hit(server, session string) Hit {
	return Hit{ServerID: server, Result: model.SearchResult{Session: &model.Session{ID: session}}}
}

func hitIDs(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.ID())
	}
	return out
}

func TestMergeSearchResultsTakesTurns(t *testing.T) {
	got := hitIDs(MergeSearchResults([][]Hit{
		{hit("a", "1"), hit("a", "2"), hit("a", "3")},
		nil,
		{hit("b", "1")},
		{hit("c", "1"), hit("c", "2")},
	}))
	want := []string{"a/1", "b/1", "c/1", "a/2", "c/2", "a/3"}
	if !slices.Equal(got, want) {
		t.Errorf("merged %v, want %v", got, want)
	}
	if got := MergeSearchResults(nil); len(got) != 0 {
		t.Errorf("nothing merged into %v", got)
	}
	if !SearchActive(" ab ") || SearchActive(" a ") || !SearchActive("éé") || SearchActive("") {
		t.Error("SearchActive")
	}
}

// promptedSession makes a session with a prompt on the fake agent, and
// waits for its reply, which rad indexes as it writes it.
func (r *testRad) promptedSession(t *testing.T, c *ServerConnection, q *Queue, projectID, prompt string) model.Session {
	t.Helper()
	var s model.Session
	if err := run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: projectID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot}, Prompt: prompt})
		return
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the reply", func() bool {
		turns, err := r.st.Turns(context.Background(), s.ID)
		return err == nil && len(turns) == 1 && turns[0].Status == model.TurnCompleted
	})
	return s
}

func searchServer(id string, c *ServerConnection) SearchServer {
	return SearchServer{ID: id, Name: "rad " + id, Conn: c}
}

func TestRadSearchesSessions(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	if !c.Info.Search {
		t.Fatalf("info %+v", c.Info)
	}
	p := r.project(t, c, q)
	flaky := r.promptedSession(t, c, q, p.ID, "fix the flaky login test")
	theme := r.promptedSession(t, c, q, p.ID, "add a dark theme")
	if err := run(t, q, func() error { return c.Archive(context.Background(), theme.ID, false) }); err != nil {
		t.Fatal(err)
	}

	// The call itself.
	var res model.SearchResults
	if err := run(t, q, func() (err error) { res, err = c.Search(context.Background(), "flaky", false, 0); return }); err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].Session.ID != flaky.ID || res.Results[0].ItemID == "" || res.Results[0].Matches != 2 {
		t.Fatalf("flaky: %+v", res)
	}
	if err := run(t, q, func() (err error) { res, err = c.Search(context.Background(), "theme", false, 0); return }); err != nil || len(res.Results) != 0 {
		t.Fatalf("an archived session without archived: %+v %v", res, err)
	}

	s := NewSessionSearch(q, func() []SearchServer { return []SearchServer{searchServer("one", c)} })
	s.Debounce = 10 * time.Millisecond
	// A letter searches nothing.
	s.SetQuery("f")
	if s.IsSearching || s.Hits != nil {
		t.Fatalf("one letter: %+v", s)
	}
	s.SetQuery("theme")
	if !s.IsSearching {
		t.Fatal("not searching")
	}
	eventually(t, q, "the archived session", func() bool { return !s.IsSearching && len(s.Hits) == 1 })
	h := s.Hits[0]
	if h.ID() != "one/"+theme.ID || !h.Result.Session.Archived || h.Result.ItemKind == "" {
		t.Errorf("hit %+v", h)
	}
	// Without the archive, nothing.
	s.SetIncludeArchived(false)
	eventually(t, q, "no results", func() bool { return !s.IsSearching && len(s.Hits) == 0 })
	if s.Failures != nil || s.More || s.Indexing != nil || s.Unsupported != nil {
		t.Errorf("state %+v", s)
	}
	// Back under two characters, the results go.
	s.SetIncludeArchived(true)
	s.SetQuery("flaky login")
	eventually(t, q, "the flaky session", func() bool { return !s.IsSearching && len(s.Hits) == 1 })
	s.SetQuery("f ")
	if s.IsSearching || s.Hits != nil {
		t.Errorf("one letter again: %+v", s)
	}
}

// Two rads take turns in the results; one that fails is reported while
// the others' results show, and one too old to search is named.
func TestSessionSearchAcrossServers(t *testing.T) {
	r1, r2 := startRad(t), startRad(t)
	c1, q1, _ := r1.connect(t)
	c2, q2, _ := r2.connect(t)
	p1, p2 := r1.project(t, c1, q1), r2.project(t, c2, q2)
	a1 := r1.promptedSession(t, c1, q1, p1.ID, "deploy the docs")
	a2 := r1.promptedSession(t, c1, q1, p1.ID, "deploy the site")
	b1 := r2.promptedSession(t, c2, q2, p2.ID, "deploy the app")

	// A server that says it searches, whose socket is gone.
	q := NewQueue()
	broken := NewServerConnection(q, nil, "", nil)
	broken.State, broken.Info = ConnState{Kind: Connected}, &ServerInfo{Search: true}
	old := NewServerConnection(q, nil, "", nil)
	old.State, old.Info = ConnState{Kind: Connected}, &ServerInfo{}
	offline := NewServerConnection(q, nil, "", nil)
	offline.Info = &ServerInfo{Search: true}

	// Each connection's state is its own queue's; the search's queue is
	// another. Their state doesn't change while they search here.
	s := NewSessionSearch(q, func() []SearchServer {
		return []SearchServer{searchServer("one", c1), searchServer("broken", broken), searchServer("old", old), searchServer("off", offline), searchServer("two", c2)}
	})
	s.Debounce = 10 * time.Millisecond
	s.SetQuery("deploy")
	eventually(t, q, "the answers", func() bool { return !s.IsSearching && len(s.Hits) == 3 })
	ids := hitIDs(s.Hits)
	if ids[1] != "two/"+b1.ID || !slices.Contains(ids, "one/"+a1.ID) || !slices.Contains(ids, "one/"+a2.ID) || !strings.HasPrefix(ids[0], "one/") {
		t.Errorf("hits %v", ids)
	}
	if len(s.Failures) != 1 || s.Failures[0].Server != "rad broken" || s.Failures[0].Message != "Not connected to rad" {
		t.Errorf("failures %+v", s.Failures)
	}
	if !slices.Equal(s.Unsupported, []string{"rad old"}) {
		t.Errorf("unsupported %v", s.Unsupported)
	}
}

// An answer to an older query never shows, even when it comes last.
func TestSessionSearchDropsStaleAnswers(t *testing.T) {
	q := NewQueue()
	c := NewServerConnection(q, nil, "", nil)
	c.State, c.Info = ConnState{Kind: Connected}, &ServerInfo{Search: true}
	s := NewSessionSearch(q, func() []SearchServer { return []SearchServer{searchServer("one", c)} })
	s.Debounce = time.Millisecond
	release := map[string]chan struct{}{}
	for _, q := range []string{"first", "second", "third", "thirds"} {
		release[q] = make(chan struct{})
	}
	asked := make(chan string, 4)
	s.search = func(ctx context.Context, _ *ServerConnection, query string, archived bool, limit int) (model.SearchResults, error) {
		if !archived || limit != 30 {
			return model.SearchResults{}, errors.New("wrong params")
		}
		asked <- query
		<-release[query] // answers even when cancelled, as a slow rad would
		return model.SearchResults{Results: []model.SearchResult{{Session: &model.Session{ID: query}}}, Indexing: query == "first"}, nil
	}
	waitAsked := func(want string) {
		t.Helper()
		eventually(t, q, "the call for "+want, func() bool {
			select {
			case got := <-asked:
				if got != want {
					t.Fatalf("asked %q, want %q", got, want)
				}
				return true
			default:
				return false
			}
		})
	}
	s.SetQuery("first")
	waitAsked("first")
	s.SetQuery("second")
	waitAsked("second")
	close(release["second"])
	eventually(t, q, "the second answer", func() bool { return !s.IsSearching && len(s.Hits) == 1 })
	close(release["first"])
	time.Sleep(50 * time.Millisecond)
	q.Drain()
	if ids := hitIDs(s.Hits); !slices.Equal(ids, []string{"one/second"}) || s.Indexing != nil {
		t.Errorf("hits %v, indexing %v", ids, s.Indexing)
	}
	// Typing again before the debounce ends asks once, for the last.
	s.Debounce = 30 * time.Millisecond
	close(release["thirds"])
	s.SetQuery("third")
	s.SetQuery("thirds")
	waitAsked("thirds")
	eventually(t, q, "the last answer", func() bool { return !s.IsSearching && hitIDs(s.Hits)[0] == "one/thirds" })
	select {
	case got := <-asked:
		t.Errorf("also asked %q", got)
	default:
	}
}
