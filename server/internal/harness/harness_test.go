package harness

import (
	"context"
	"testing"

	"remote-agent/internal/model"
)

type stub struct{ info model.HarnessInfo }

func (s stub) ID() string                                         { return s.info.ID }
func (s stub) Probe(context.Context) model.HarnessInfo            { return s.info }
func (s stub) Open(context.Context, OpenOptions) (Runtime, error) { return nil, ErrUnsupported }

func TestInstalled(t *testing.T) {
	r := NewRegistry(
		stub{model.HarnessInfo{ID: "a", Installed: true, AuthOK: true}},
		stub{model.HarnessInfo{ID: "b"}},
		stub{model.HarnessInfo{ID: "c", Installed: true}},
	)
	var got []string
	for _, i := range r.Installed(context.Background(), false) {
		got = append(got, i.ID)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("installed = %v, want [a c]", got)
	}
}

func TestInputCommand(t *testing.T) {
	for _, c := range []struct {
		in   Input
		args string
		ok   bool
	}{
		{Input{Text: "/compact"}, "", true},
		{Input{Text: "  /compact\n"}, "", true},
		{Input{Text: "/compact keep the API notes"}, "keep the API notes", true},
		{Input{Text: "/compact\nkeep the API notes"}, "keep the API notes", true},
		{Input{Text: "/compacted"}, "", false},
		{Input{Text: "compact"}, "", false},
		{Input{Text: "please /compact"}, "", false},
		{Input{Text: "/compact", Images: []Image{{Path: "a.png"}}}, "", false},
	} {
		if args, ok := c.in.Command("compact"); args != c.args || ok != c.ok {
			t.Errorf("Command(%q) = %q, %v; want %q, %v", c.in.Text, args, ok, c.args, c.ok)
		}
	}
}
