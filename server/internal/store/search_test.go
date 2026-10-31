package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/model"
)

func searchFor(t *testing.T, st *Store, p SearchParams) ([]model.SearchResult, bool) {
	t.Helper()
	res, more, err := st.Search(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return res, more
}

func ids(res []model.SearchResult) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Session.ID
	}
	return out
}

func snippetText(parts []model.SnippetPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Match {
			b.WriteString("[" + p.Text + "]")
		} else {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func TestSearchFindsItemsTitlesAndBranches(t *testing.T) {
	st := openTest(t)
	s1 := &model.Session{ID: "s1", ProjectID: "p1", Title: "Fix the login redirect", Workspace: model.Workspace{Branch: "ra/login-redirect"}}
	s2 := &model.Session{ID: "s2", ProjectID: "p1", Title: "Dark theme", Workspace: model.Workspace{Branch: "ra/dark-théme"}}
	write(t, st, func(w *W) error {
		for _, s := range []*model.Session{s1, s2} {
			if err := w.PutSession(s); err != nil {
				return err
			}
		}
		items := []*model.Item{
			{ID: "i1", SessionID: "s1", Order: 1, Kind: model.ItemUserMessage, Text: "The redirect after login drops the next= parameter."},
			{ID: "i2", SessionID: "s1", Order: 2, Kind: model.ItemReasoning, Text: "Thinking about cookies"},
			{ID: "i3", SessionID: "s1", Order: 3, Kind: model.ItemToolCall, Tool: &model.ToolCall{Name: "Bash", Kind: model.ToolExecute,
				Title: "Run the tests", Input: json.RawMessage(`{"command":"go test ./internal/auth -run TestRedirect","description":"Run the tests"}`),
				Output: "ok cookies"}},
			{ID: "i4", SessionID: "s1", Order: 4, Kind: model.ItemAssistantMessage, Text: "Fixed: `next` is kept.\n\n![Screenshot](rad-image:abc123.png)"},
			{ID: "i5", SessionID: "s2", Order: 1, Kind: model.ItemAssistantMessage, Text: "The settings page now follows the system's appearance."},
			{ID: "i6", SessionID: "s2", Order: 2, Kind: model.ItemAssistantMessage, ParentItemID: "i5", Text: "sub-agent cookies"},
			{ID: "i7", SessionID: "s2", Order: 3, Kind: model.ItemPlan, Plan: &model.Plan{Entries: []model.PlanEntry{{Content: "Add a palette"}}}},
		}
		for _, it := range items {
			if err := w.PutItem(it); err != nil {
				return err
			}
		}
		return nil
	})

	res, more := searchFor(t, st, SearchParams{Query: "redirect"})
	if more || len(res) != 1 || res[0].Session.ID != "s1" {
		t.Fatalf("redirect: %+v", res)
	}
	// The item shows rather than the title, which matches too.
	if r := res[0]; r.ItemID != "i1" || r.ItemKind != model.ItemUserMessage || r.Matches != 1 {
		t.Fatalf("redirect hit: %+v", r)
	}
	if got := snippetText(res[0].Snippet); got != "The [redirect] after login drops the next= parameter." {
		t.Fatalf("snippet: %q", got)
	}

	// A tool call's command, though its title is the description.
	if res, _ := searchFor(t, st, SearchParams{Query: "TestRedirect"}); len(res) != 1 || res[0].ItemID != "i3" {
		t.Fatalf("command: %+v", res)
	}
	// Reasoning, tool output and sub-agents' items aren't searched.
	if res, _ := searchFor(t, st, SearchParams{Query: "cookies"}); len(res) != 0 {
		t.Fatalf("cookies: %+v", res)
	}
	// Nor are image ids.
	if res, _ := searchFor(t, st, SearchParams{Query: "abc123"}); len(res) != 0 {
		t.Fatalf("image id: %+v", res)
	}
	// Prefixes, any case and accents; every word must match.
	if res, _ := searchFor(t, st, SearchParams{Query: "SETT appear"}); len(res) != 1 || res[0].ItemID != "i5" {
		t.Fatalf("prefix: %+v", res)
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "settings cookies"}); len(res) != 0 {
		t.Fatalf("every word: %+v", res)
	}
	// A branch matches without an item; accents don't matter.
	res, _ = searchFor(t, st, SearchParams{Query: "theme"})
	if len(res) != 1 || res[0].Session.ID != "s2" || res[0].ItemID != "" || res[0].Matches != 0 {
		t.Fatalf("title: %+v", res)
	}
	if got := snippetText(res[0].Snippet); got != "Dark [theme]" {
		t.Fatalf("title snippet: %q", got)
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "palette"}); len(res) != 1 || res[0].ItemKind != model.ItemPlan {
		t.Fatalf("plan: %+v", res)
	}
	// A quoted phrase must be there as it is.
	if res, _ := searchFor(t, st, SearchParams{Query: `"login drops"`}); len(res) != 1 {
		t.Fatalf("phrase: %+v", res)
	}
	if res, _ := searchFor(t, st, SearchParams{Query: `"drops login"`}); len(res) != 0 {
		t.Fatalf("phrase order: %+v", res)
	}
	// FTS5's syntax is taken as text.
	for _, q := range []string{`NOT`, `title:redirect`, `"`, `(`, `-`, `a*b`, `***`, `NEAR(a b)`, ``, `   `} {
		if _, _, err := st.Search(context.Background(), SearchParams{Query: q}); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "next="}); len(res) != 1 || res[0].Matches != 2 {
		t.Fatalf("punctuation: %+v", res)
	}
}

func TestSearchFollowsChanges(t *testing.T) {
	st := openTest(t)
	s := &model.Session{ID: "s1", ProjectID: "p1", Title: "New session"}
	it := &model.Item{ID: "i1", SessionID: "s1", Order: 1, Kind: model.ItemAssistantMessage, Status: model.ItemInProgress}
	write(t, st, func(w *W) error { return w.PutSession(s) })
	// A streaming reply: each upsert replaces the indexed text.
	for _, text := range []string{"Looking", "Looking at the parser", "Looking at the tokenizer"} {
		it.Text = text
		write(t, st, func(w *W) error { return w.PutItem(it) })
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "parser"}); len(res) != 0 {
		t.Fatalf("old text: %+v", res)
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "tokenizer"}); len(res) != 1 {
		t.Fatalf("new text: %+v", res)
	}
	s.Title = "Tokenizer bug"
	write(t, st, func(w *W) error { return w.PutSession(s) })
	if res, _ := searchFor(t, st, SearchParams{Query: "bug"}); len(res) != 1 {
		t.Fatalf("renamed: %+v", res)
	}

	// Archived sessions only when asked for.
	s.Archived = true
	write(t, st, func(w *W) error { return w.PutSession(s) })
	if res, _ := searchFor(t, st, SearchParams{Query: "tokenizer"}); len(res) != 0 {
		t.Fatalf("archived: %+v", res)
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "tokenizer", Archived: true}); len(res) != 1 || !res[0].Session.Archived {
		t.Fatalf("archived asked for: %+v", res)
	}

	write(t, st, func(w *W) error { return w.DeleteSession("s1") })
	if res, _ := searchFor(t, st, SearchParams{Query: "tokenizer", Archived: true}); len(res) != 0 {
		t.Fatalf("deleted: %+v", res)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM search_docs`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d docs left", n)
	}
	// The index agrees with its rows.
	if _, err := st.db.Exec(`INSERT INTO search_fts(search_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRanksRecentGoodMatchesFirstAndLimits(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	put := func(id string, age time.Duration, text string) {
		s := &model.Session{ID: id, ProjectID: "p1", Title: "Session " + id}
		write(t, st, func(w *W) error {
			if err := w.PutSessionKeepingTime(s); err != nil {
				return err
			}
			return w.PutItem(&model.Item{ID: id + "-i", SessionID: id, Order: 1, Kind: model.ItemAssistantMessage, Text: text})
		})
		s.UpdatedAt = now.Add(-age)
		write(t, st, func(w *W) error { return w.PutSessionKeepingTime(s) })
	}
	put("old", 400*24*time.Hour, "migrate the database")
	put("new", time.Hour, "migrate the database")
	put("mid", 20*24*time.Hour, "migrate the database")
	put("other", time.Hour, "nothing to see")
	res, more := searchFor(t, st, SearchParams{Query: "migrate"})
	if more || strings.Join(ids(res), ",") != "new,mid,old" {
		t.Fatalf("order: %v more=%v", ids(res), more)
	}
	res, more = searchFor(t, st, SearchParams{Query: "migrate", Limit: 2})
	if !more || strings.Join(ids(res), ",") != "new,mid" {
		t.Fatalf("limit: %v more=%v", ids(res), more)
	}
	// A title counts for more than a mention, even an older one.
	put("titled", 10*24*time.Hour, "unrelated")
	s := &model.Session{ID: "titled", ProjectID: "p1", Title: "Migrate", UpdatedAt: now.Add(-10 * 24 * time.Hour)}
	write(t, st, func(w *W) error { return w.PutSessionKeepingTime(s) })
	if res, _ := searchFor(t, st, SearchParams{Query: "migrate"}); res[0].Session.ID != "titled" {
		t.Fatalf("title first: %v", ids(res))
	}
}

func TestSearchIndexesOlderDatabases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rad.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, st, func(w *W) error {
		if err := w.PutSession(&model.Session{ID: "s1", ProjectID: "p1", Title: "Fix the flaky test"}); err != nil {
			return err
		}
		return w.PutItem(&model.Item{ID: "i1", SessionID: "s1", Order: 1, Kind: model.ItemUserMessage, Text: "the websocket test hangs"})
	})
	// As a rad without search left it: no index, no version.
	if _, err := st.db.Exec(`DELETE FROM search_docs; DELETE FROM meta WHERE key='search_version'`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !st.Indexing() {
		t.Fatal("not indexing")
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "websocket"}); len(res) != 0 {
		t.Fatalf("before indexing: %+v", res)
	}
	if err := st.IndexForSearch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.Indexing() {
		t.Fatal("still indexing")
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "websocket"}); len(res) != 1 || res[0].ItemID != "i1" {
		t.Fatalf("item: %+v", res)
	}
	if res, _ := searchFor(t, st, SearchParams{Query: "flaky"}); len(res) != 1 {
		t.Fatalf("title: %+v", res)
	}
	// Once indexed, it stays so; a new database has nothing to index.
	st.Close()
	if st, err = Open(path); err != nil || st.Indexing() {
		t.Fatalf("reopened: indexing=%v %v", st.Indexing(), err)
	}
	if fresh := openTest(t); fresh.Indexing() {
		t.Fatal("a new database is indexing")
	}
}

func TestFTSQuery(t *testing.T) {
	for q, want := range map[string]string{
		"login redirect":      `"login"* "redirect"*`,
		`"login redirect" ok`: `"login redirect" "ok"`,
		`say "hi`:             `"say"* "hi"`,
		`a"b`:                 `"a" "b"`,
		`- && ||`:             ``,
		`src/ui/App.tsx`:      `"src/ui/App.tsx"*`,
	} {
		if got := ftsQuery(q); got != want {
			t.Errorf("ftsQuery(%q) = %q, want %q", q, got, want)
		}
	}
}

// BenchmarkSearchIndex measures indexing a database an older rad wrote, on
// the first start with search, and searching it. RAD_SEARCH_SESSIONS sets
// its size (default 200 sessions of 150 items).
func BenchmarkSearchIndex(b *testing.B) {
	sessions := 200
	if n, err := strconv.Atoi(os.Getenv("RAD_SEARCH_SESSIONS")); err == nil {
		sessions = n
	}
	path := filepath.Join(b.TempDir(), "rad.db")
	st, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	words := strings.Fields("the a parser token websocket session server client build test fix login redirect theme database migrate index query cache image prompt reply agent worktree branch commit push")
	sentence := func(i, n int) string {
		var sb strings.Builder
		for j := 0; j < n; j++ {
			sb.WriteString(words[(i*7+j*13)%len(words)])
			sb.WriteByte(' ')
		}
		return sb.String()
	}
	output := strings.Repeat("ok  \tremote-agent/internal/store\t0.123s\n", 100) // 4 KB of tool output
	for s := 0; s < sessions; s++ {
		id := fmt.Sprintf("s%05d", s)
		_, err := st.Write(context.Background(), func(w *W) error {
			if err := w.PutSession(&model.Session{ID: id, ProjectID: "p1", Title: sentence(s, 5)}); err != nil {
				return err
			}
			for i := 0; i < 150; i++ {
				it := &model.Item{ID: fmt.Sprintf("%s-%03d", id, i), SessionID: id, Order: int64(i)}
				switch i % 3 {
				case 0:
					it.Kind, it.Text = model.ItemAssistantMessage, sentence(s+i, 200)
				case 1:
					it.Kind, it.Tool = model.ItemToolCall, &model.ToolCall{Name: "Bash", Kind: model.ToolExecute, Title: sentence(i, 6),
						Input: json.RawMessage(`{"command":"go test ./..."}`), Output: output}
				default:
					it.Kind, it.Text = model.ItemUserMessage, sentence(s*i, 30)
				}
				if err := w.PutItem(it); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	st.db.Exec(`DELETE FROM meta WHERE key='search_version'`)
	st.Close()
	if fi, err := os.Stat(path); err == nil {
		b.Logf("%d sessions, %d items: %d MB", sessions, sessions*150, fi.Size()>>20)
	}
	b.ResetTimer()
	for b.Loop() {
		st, err := Open(path)
		if err != nil {
			b.Fatal(err)
		}
		start := time.Now()
		if err := st.IndexForSearch(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.Logf("indexed in %v", time.Since(start))
		for _, q := range []string{"websocket", "lo", "parser token", `"login redirect"`} {
			start = time.Now()
			res, _, err := st.Search(context.Background(), SearchParams{Query: q, Archived: true})
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("%q: %d results in %v", q, len(res), time.Since(start))
		}
		st.db.Exec(`DELETE FROM meta WHERE key='search_version'`)
		st.Close()
	}
}
