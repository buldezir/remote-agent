// Package native has what the adapters' importers (harness.Importer) share:
// reading an agent's own session files, and making a transcript of them.
package native

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// Lines calls f with each line of the file at path, until f returns false.
// Lines can be long, such as one with an image in it, so they come whole.
func Lines(path string, f func(line []byte) bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	r := bufio.NewReaderSize(file, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 && !f(line) {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// File is a session file, with when it was last written.
type File struct {
	Path string
	Info fs.FileInfo
}

// Newest stats the files and sorts them newest first, leaving out those
// that are gone.
func Newest(paths []string) []File {
	files := make([]File, 0, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			files = append(files, File{p, fi})
		}
	}
	slices.SortStableFunc(files, func(a, b File) int { return b.Info.ModTime().Compare(a.Info.ModTime()) })
	return files
}

// Cache keeps what was read from each file while the file stays as it was,
// so that listing a folder's sessions again reads only those that changed.
type Cache[T any] struct {
	mu sync.Mutex
	m  map[string]cached[T]
}

type cached[T any] struct {
	size int64
	mod  time.Time
	v    T
}

// Get returns what read returned for f, reading it again if f changed.
func (c *Cache[T]) Get(f File, read func() (T, error)) (T, error) {
	c.mu.Lock()
	e, ok := c.m[f.Path]
	c.mu.Unlock()
	if ok && e.size == f.Info.Size() && e.mod.Equal(f.Info.ModTime()) {
		return e.v, nil
	}
	v, err := read()
	if err != nil {
		return v, err
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]cached[T]{}
	}
	c.m[f.Path] = cached[T]{f.Info.Size(), f.Info.ModTime(), v}
	c.mu.Unlock()
	return v, nil
}

// Dirs is dir as given, cleaned, and with its symbolic links resolved, as
// agents record the folder they ran in either way (/tmp is /private/tmp on
// macOS).
func Dirs(dir string) []string {
	dirs := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dirs[0] {
		dirs = append(dirs, real)
	}
	return dirs
}

// SameDir reports whether path is one of dirs.
func SameDir(dirs []string, path string) bool {
	return path != "" && slices.Contains(dirs, filepath.Clean(path))
}

// Title is a prompt's first line, as a session's title.
func Title(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > 80 {
		line = string([]rune(line)[:79]) + "…"
	}
	return line
}

// Time parses an RFC 3339 timestamp, or returns the zero time.
func Time(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Summary is what a listing shows of a session file, besides when it was
// written.
type Summary struct {
	ID        string
	Title     string // the agent's or the user's name for it
	Prompt    string // the first prompt
	Prompts   int
	Replies   int
	FromRad   bool // rad started it
	Subagent  bool
	OtherDir  bool // it ran in another folder
	StartedAt time.Time
}

// Session is the listing's entry for s, or false for one to leave out.
func (s Summary) Session(f File, known map[string]bool) (model.ImportableSession, bool) {
	if s.ID == "" || known[s.ID] || s.FromRad || s.Subagent || s.OtherDir || s.Prompts == 0 || s.Prompts+s.Replies < 2 {
		return model.ImportableSession{}, false
	}
	title := s.Title
	if title == "" {
		title = Title(s.Prompt)
	}
	return model.ImportableSession{ID: s.ID, Title: title, Messages: s.Prompts + s.Replies, UpdatedAt: f.Info.ModTime().UTC()}, true
}

// Builder makes a Transcript, turn by turn, from a session's entries.
type Builder struct {
	t     harness.Transcript
	turn  *harness.TranscriptTurn
	where map[string][2]int // item key -> its turn and its place there
	n     int
}

// Prompt starts a turn with the user's prompt.
func (b *Builder) Prompt(text string, images []model.ImageRef, at time.Time) {
	b.StartTurn(at)
	b.Add(model.Item{Kind: model.ItemUserMessage, Status: model.ItemCompleted, Text: text, Images: images}, at)
}

// StartTurn starts a turn without a prompt, such as one the agent started
// itself.
func (b *Builder) StartTurn(at time.Time) {
	b.close()
	b.t.Turns = append(b.t.Turns, harness.TranscriptTurn{Status: model.TurnCompleted, StartedAt: at, EndedAt: at})
	b.turn = &b.t.Turns[len(b.t.Turns)-1]
}

// InTurn reports whether a turn has started.
func (b *Builder) InTurn() bool { return b.turn != nil }

// Add adds it to the turn, starting one if none has. An item without an ID
// gets one.
func (b *Builder) Add(it model.Item, at time.Time) {
	if b.turn == nil {
		b.StartTurn(at)
	}
	b.n++
	if it.ID == "" {
		it.ID = fmt.Sprintf("n%d", b.n)
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = at
	}
	if b.where == nil {
		b.where = map[string][2]int{}
	}
	b.where[it.ID] = [2]int{len(b.t.Turns) - 1, len(b.turn.Items)}
	b.turn.Items = append(b.turn.Items, it)
	b.Touch(at)
}

// Update changes the item with key, wherever it is, and reports whether
// there is one.
func (b *Builder) Update(key string, f func(it *model.Item)) bool {
	w, ok := b.where[key]
	if ok {
		f(&b.t.Turns[w[0]].Items[w[1]])
	}
	return ok
}

// Touch notes that the turn went on until at.
func (b *Builder) Touch(at time.Time) {
	if b.turn != nil && at.After(b.turn.EndedAt) {
		b.turn.EndedAt = at
	}
}

// End ends the turn with status (and error), at at if it's known.
func (b *Builder) End(status model.TurnStatus, err string, at time.Time) {
	if b.turn == nil {
		return
	}
	b.turn.Status = status
	if err != "" {
		b.turn.Error = err
	}
	b.Touch(at)
	b.close()
}

// Turn is the current turn, or nil.
func (b *Builder) Turn() *harness.TranscriptTurn { return b.turn }

func (b *Builder) close() {
	if b.turn == nil {
		return
	}
	// Tool calls that never got their result failed, as rad ends them when
	// a turn is interrupted.
	for i, it := range b.turn.Items {
		if it.Status == model.ItemInProgress || it.Status == model.ItemPending {
			b.turn.Items[i].Status = model.ItemCompleted
			if it.Kind == model.ItemToolCall {
				b.turn.Items[i].Status = model.ItemFailed
			}
		}
	}
	b.turn = nil
}

// Transcript ends the last turn and returns the transcript, with its turns
// that have something in them.
func (b *Builder) Transcript(title, modelID string) *harness.Transcript {
	b.close()
	t := b.t
	t.Title, t.Model = title, modelID
	t.Turns = slices.DeleteFunc(t.Turns, func(t harness.TranscriptTurn) bool { return len(t.Items) == 0 })
	return &t
}
