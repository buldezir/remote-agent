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
