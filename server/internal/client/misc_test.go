package client

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"slices"
	"testing"

	"remote-agent/internal/model"
)

// As web/src/protocol/commandSuggestions.test.ts.
func TestCommandSuggestions(t *testing.T) {
	cmds := []model.Command{
		{Name: "compact", Description: "Summarize the conversation"},
		{Name: "skill:prototype", Description: "Prototype to answer a design question"},
		{Name: "review", Description: "Review a pull request"},
		{Name: "compact", Description: "a duplicate"},
	}
	names := func(draft string) []string {
		var out []string
		for _, c := range CommandSuggestions(draft, cmds) {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names("/"); !slices.Equal(got, []string{"compact", "skill:prototype", "review"}) {
		t.Errorf("/: %v", got)
	}
	if got := names("/proto"); !slices.Equal(got, []string{"skill:prototype"}) {
		t.Errorf("/proto: %v", got)
	}
	if got := names("/design"); !slices.Equal(got, []string{"skill:prototype"}) {
		t.Errorf("/design: %v", got)
	}
	// Arguments and prose stay as they are.
	if got := names("/compact now"); got != nil {
		t.Errorf("/compact now: %v", got)
	}
	if got := names("compact"); got != nil {
		t.Errorf("compact: %v", got)
	}
	// Exact and prefix names before parts of names and descriptions.
	ranked := []model.Command{{Name: "a-review"}, {Name: "x", Description: "review"}, {Name: "reviewer"}, {Name: "review"}}
	var got []string
	for _, c := range CommandSuggestions("/review", ranked) {
		got = append(got, c.Name)
	}
	if !slices.Equal(got, []string{"review", "reviewer", "a-review", "x"}) {
		t.Errorf("ranked: %v", got)
	}
}

// As web/src/protocol/pairing.test.ts.
func TestParsePairingLink(t *testing.T) {
	l, ok := ParsePairingLink(" remoteagent://pair?v=1&name=My%20Mac&code=abc&url=http://100.1.2.3:7421/&url=http://127.0.0.1:7421&url=http://bad/path \n")
	if !ok || l.Name != "My Mac" || l.Code != "abc" || !slices.Equal(l.URLs, []string{"http://100.1.2.3:7421", "http://127.0.0.1:7421"}) {
		t.Fatalf("%+v %v", l, ok)
	}
	for _, bad := range []string{"https://example.com", "remoteagent://pair", "remoteagent://pair?code=abc", "remoteagent://pair?url=http://h:1"} {
		if _, ok := ParsePairingLink(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if got := LoopbackFirst(l.URLs); !slices.Equal(got, []string{"http://127.0.0.1:7421", "http://100.1.2.3:7421"}) {
		t.Errorf("loopback first: %v", got)
	}
}

// As web/src/store/drafts.test.ts.
func TestDrafts(t *testing.T) {
	dir := t.TempDir()
	a := (&ServerStore{dir: dir}).Drafts()
	b := (&ServerStore{dir: dir}).Drafts()
	a.Set("s1", "hello")
	// Another ra sees it, and keeps it when it writes its own.
	if b.Get("s1") != "hello" {
		t.Fatal(b.Get("s1"))
	}
	b.Set("s2", "world")
	if a.Get("s1") != "hello" || a.Get("s2") != "world" {
		t.Fatal(a.load())
	}
	a.Keep(func(id string) bool { return id == "s2" })
	if b.Get("s1") != "" || b.Get("s2") != "world" {
		t.Fatal(b.load())
	}
	b.Set("s2", "")
	if _, err := filepath.Glob(filepath.Join(dir, "drafts.json")); err != nil || len(a.load()) != 0 {
		t.Fatal(a.load())
	}
	var none *Drafts
	none.Set("s", "x")
	if none.Get("s") != "" {
		t.Fatal("a nil Drafts kept a draft")
	}
}

func TestServerStore(t *testing.T) {
	s := NewServerStore(t.TempDir())
	if _, _, ok := s.Load(); ok {
		t.Fatal("loaded before pairing")
	}
	srv := SavedServer{ID: "srv", Name: "mac", URLs: []string{"http://127.0.0.1:7421", "http://100.1.2.3:7421"}, DeviceID: "d"}
	if err := s.Save(srv, "tok"); err != nil {
		t.Fatal(err)
	}
	s.Promote("http://100.1.2.3:7421")
	got, tok, ok := s.Load()
	if !ok || tok != "tok" || !slices.Equal(got.URLs, []string{"http://100.1.2.3:7421", "http://127.0.0.1:7421"}) {
		t.Fatalf("%+v %q %v", got, tok, ok)
	}
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Load(); ok {
		t.Fatal("loaded after forgetting")
	}
}

func TestPrepareImage(t *testing.T) {
	encode := func(img image.Image, asPNG bool) []byte {
		var b bytes.Buffer
		if asPNG {
			png.Encode(&b, img)
		} else {
			jpeg.Encode(&b, img, nil)
		}
		return b.Bytes()
	}
	big := image.NewRGBA(image.Rect(0, 0, 4096, 1024))
	for x := range 4096 {
		big.Set(x, 0, color.RGBA{uint8(x), 0, 0, 255})
	}
	out, err := PrepareImage(encode(big, true))
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "png" || img.Bounds().Dx() != 2048 || img.Bounds().Dy() != 512 {
		t.Fatalf("%v %s %v", err, format, img.Bounds())
	}
	// A photo stays a JPEG.
	out, err = PrepareImage(encode(image.NewRGBA(image.Rect(0, 0, 10, 10)), false))
	if _, format, _ := image.Decode(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Fatalf("%v %s", err, format)
	}
	if _, err := PrepareImage([]byte("not an image")); err != ErrNotImage {
		t.Fatal(err)
	}
}
