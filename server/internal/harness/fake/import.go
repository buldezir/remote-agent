package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// The fake ran two sessions of its own in every folder, as if in a
// terminal, which rad can pick up (harness.Importer): "terminal-1-<folder>",
// named "Fix the login redirect", from two hours ago, and "terminal-2-<folder>",
// without a name, which it wrote to just now, as one that may still run.

func terminalID(dir string, n int) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return fmt.Sprintf("terminal-%d-%s", n, hex.EncodeToString(sum[:4]))
}

type terminalSession struct {
	title   string // its own name; "" for none
	prompts []string
	ago     time.Duration
}

var terminalSessions = []terminalSession{
	{title: "Fix the login redirect", prompts: []string{"Fix the login redirect", "Add a test for it"}, ago: 2 * time.Hour},
	{prompts: []string{"Add a dark theme to the settings page", "Use the system setting by default", "Run the tests"}},
}

func (Harness) ListNative(ctx context.Context, dir string, known map[string]bool) ([]model.ImportableSession, error) {
	out := []model.ImportableSession{}
	for i := len(terminalSessions) - 1; i >= 0; i-- {
		s := terminalSessions[i]
		id := terminalID(dir, i+1)
		if known[id] {
			continue
		}
		out = append(out, model.ImportableSession{ID: id, Title: cmpOr(s.title, s.prompts[0]), Messages: 2 * len(s.prompts),
			UpdatedAt: time.Now().Add(-s.ago).UTC()})
	}
	return out, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (Harness) ReadNative(ctx context.Context, dir, id string, images harness.ImageStore) (*harness.Transcript, error) {
	n := 0
	for i := range terminalSessions {
		if id == terminalID(dir, i+1) {
			n = i + 1
		}
	}
	if n == 0 {
		return nil, harness.ErrNotFound
	}
	s := terminalSessions[n-1]
	end := time.Now().Add(-s.ago).UTC()
	start := end.Add(-time.Duration(len(s.prompts)) * time.Minute)
	tr := &harness.Transcript{Title: s.title, Model: "echo", UpdatedAt: end}
	for k, p := range s.prompts {
		at := start.Add(time.Duration(k) * time.Minute)
		file := fmt.Sprintf("step-%d.txt", k+1)
		input, _ := json.Marshal(map[string]string{"file": file})
		tr.Turns = append(tr.Turns, harness.TranscriptTurn{
			Status: model.TurnCompleted, StartedAt: at, EndedAt: at.Add(30 * time.Second), ForkPoint: fmt.Sprintf("%s#%d", id, k+1),
			Items: []model.Item{
				{ID: fmt.Sprintf("u%d", k), Kind: model.ItemUserMessage, Status: model.ItemCompleted, Text: p, CreatedAt: at},
				{ID: fmt.Sprintf("t%d", k), Kind: model.ItemToolCall, Status: model.ItemCompleted, CreatedAt: at.Add(10 * time.Second),
					Tool: &model.ToolCall{Name: "write", Kind: model.ToolEdit, Title: "Write " + file, Input: input, Paths: []string{file}, Output: "ok"}},
				{ID: fmt.Sprintf("a%d", k), Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, CreatedAt: at.Add(20 * time.Second),
					Text: "Done in the terminal: " + strings.ToLower(p[:1]) + p[1:] + "."},
			},
		})
	}
	return tr, nil
}
