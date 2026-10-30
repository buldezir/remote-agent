package tui

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"remote-agent/internal/client"
	"remote-agent/internal/client/radtest"
	"remote-agent/internal/model"
)

// ui runs the interface against a rad in the test, with no terminal: keys
// go in as messages, and the screen comes out as text.
type ui struct {
	t    *testing.T
	p    *tea.Program
	rad  *radtest.Rad
	conn *client.ServerConnection
	// clip gets what the program puts on the clipboard.
	clip chan string
}

type probeMsg struct{ ch chan string }

func startUI(t *testing.T, width, height int) *ui {
	return startUIWith(t, width, height, nil)
}

// startUIWith runs setup, with a socket of its own to rad and ra's
// settings folder, before the interface starts.
func startUIWith(t *testing.T, width, height int, setup func(rpc *client.RPCClient, r *radtest.Rad, dir string)) *ui {
	t.Helper()
	r := radtest.Start(t)
	ss := client.NewServerStore(t.TempDir())
	saved, token, err := client.PairWith(context.Background(), ss, client.PairingLink{Name: "test", Code: r.Code(t), URLs: []string{r.URL}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if setup != nil {
		rpc := client.NewRPCClient(token)
		if _, err := rpc.Connect(context.Background(), saved.URLs); err != nil {
			t.Fatal(err)
		}
		setup(rpc, r, dir)
		rpc.Disconnect("", false)
	}
	q := client.NewQueue()
	conn := client.NewServerConnection(q, saved.URLs, token, ss.Drafts())
	conn.Images = client.NewImageCache(t.TempDir())
	m := New(Options{Queue: q, Conn: conn, Store: ss, Dir: dir, Theme: "dark"})
	clip := make(chan string, 8)
	p := tea.NewProgram(m,
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(width, height), tea.WithoutSignalHandler(),
		tea.WithFilter(func(model tea.Model, msg tea.Msg) tea.Msg {
			if pr, ok := msg.(probeMsg); ok {
				pr.ch <- ansi.Strip(model.(*Model).render())
				return nil
			}
			if reflect.TypeOf(msg).String() == "tea.setClipboardMsg" {
				select {
				case clip <- reflect.ValueOf(msg).String():
				default:
				}
			}
			return msg
		}))
	done := make(chan struct{})
	go func() {
		p.Run()
		close(done)
	}()
	t.Cleanup(func() {
		p.Quit()
		<-done
		conn.Stop()
		q.Drain()
	})
	p.Send(tea.WindowSizeMsg{Width: width, Height: height})
	u := &ui{t: t, p: p, rad: r, conn: conn, clip: clip}
	// The status bar's key hints, whatever has focus.
	u.waitFor(" · ")
	return u
}

func (u *ui) screen() string {
	ch := make(chan string, 1)
	u.p.Send(probeMsg{ch})
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		u.t.Fatal("the interface didn't answer")
	}
	return ""
}

func (u *ui) waitFor(text string) string {
	u.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := u.screen()
		if strings.Contains(s, text) {
			return s
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("timed out waiting for %q on\n%s", text, s)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

var keyCodes = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
	"left": tea.KeyLeft, "right": tea.KeyRight, "space": tea.KeySpace, "backspace": tea.KeyBackspace,
	"home": tea.KeyHome, "end": tea.KeyEnd,
}

// key presses a named key, a ctrl+ key, or types text.
func (u *ui) key(k string) {
	if c, ok := keyCodes[k]; ok {
		u.p.Send(tea.KeyPressMsg{Code: c})
	} else if c, ok := strings.CutPrefix(k, "ctrl+"); ok && len(c) == 1 {
		u.p.Send(tea.KeyPressMsg{Code: rune(c[0]), Mod: tea.ModCtrl})
	} else if k == "shift+tab" {
		u.p.Send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	} else {
		for _, r := range k {
			u.p.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	// Let the program take it before the next.
	u.screen()
}

func TestStartsASessionAndAnswersItsQuestions(t *testing.T) {
	u := startUI(t, 140, 40)
	u.waitFor("No sessions yet")
	u.key("ctrl+n")
	u.waitFor("New Session")
	// No project yet: add the root's folder.
	u.key("enter")
	u.waitFor("Add a Folder…")
	u.key("enter")
	u.waitFor("▸ /")
	u.key("a")
	u.waitFor("Fake (scripted)")
	for !strings.Contains(u.screen(), "save as draft") {
		u.key("tab")
	}
	u.key("write a.txt then a question")
	u.key("enter")
	u.waitFor("Needs your answer")
	// An arrow picks the first answer, Allow; enter sends it.
	u.key("right")
	u.key("enter")
	u.waitFor("A question for you")
	// The second color, Blue, then the answer.
	u.key("right")
	u.key("right")
	u.key("enter")
	u.key("enter")
	u.waitFor("Echo: write a.txt then a question")
	s := u.waitFor("› Prompt")
	for _, want := range []string{"✓ Allow: Write a.txt", "Which color? → Blue"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}

	// A follow-up, from the composer.
	u.key("markdown please")
	u.key("enter")
	s = u.waitFor("A heading")
	if !strings.Contains(s, "☑ A task done") {
		t.Errorf("no task box in\n%s", s)
	}

	// Slash commands are suggested and run.
	u.key("/comp")
	u.waitFor("/compact")
	u.key("tab")
	u.key("enter")
	u.waitFor("Context compacted")

	// The switcher finds the session.
	u.key("ctrl+k")
	u.waitFor("Jump to a session")
	u.key("write")
	u.key("esc")
}

// ra opens the session it showed last; what's typed goes into its prompt.
func TestTypesIntoTheSessionItReopens(t *testing.T) {
	u := startUIWith(t, 120, 36, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		ctx := context.Background()
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		var s model.Session
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &s); err != nil {
			t.Fatal(err)
		}
		if err := saveJSON(filepath.Join(dir, "state.json"), State{LastSession: s.ID}); err != nil {
			t.Fatal(err)
		}
	})
	u.waitFor("No prompts yet")
	u.key("hello there")
	u.key("enter")
	u.waitFor("Echo: hello there")
	// ↑ in the empty prompt brings it back.
	u.key("up")
	u.waitFor("› hello there")
	// ctrl+h opens the help from the prompt, and closes it.
	u.key("ctrl+h")
	u.waitFor("these keys")
	u.key("ctrl+h")
	if s := u.waitFor("› hello there"); strings.Contains(s, "these keys") {
		t.Errorf("the help stayed open:\n%s", s)
	}
}

func TestStopsATurnThatWaits(t *testing.T) {
	u := startUI(t, 120, 36)
	u.key("ctrl+n")
	u.waitFor("New Session")
	u.key("enter")
	u.key("enter")
	u.waitFor("▸ /")
	u.key("a")
	u.waitFor("Fake (scripted)")
	for !strings.Contains(u.screen(), "save as draft") {
		u.key("tab")
	}
	u.key("write b.txt")
	u.key("enter")
	u.waitFor("Needs your answer")
	// What's typed as the approval appears doesn't answer it.
	u.key("2 files")
	u.key("enter")
	if s := u.waitFor("pick an answer"); strings.Contains(s, "Allow:") {
		t.Errorf("typing answered the approval:\n%s", s)
	}
	// Esc goes back to the list and stops nothing; ctrl+x stops.
	u.key("esc")
	u.waitFor("enter open")
	if s := u.screen(); strings.Contains(s, "Interrupted") || !strings.Contains(s, "Needs your answer") {
		t.Errorf("esc stopped the turn:\n%s", s)
	}
	u.key("ctrl+x")
	s := u.waitFor("— Interrupted")
	if !strings.Contains(s, "Cancelled: Write b.txt") {
		t.Errorf("approval not cancelled in\n%s", s)
	}
}

// Esc closes the new session's form, keeping its prompt as a draft.
func TestClosingTheFormKeepsADraft(t *testing.T) {
	u := startUI(t, 120, 36)
	u.key("ctrl+n")
	u.waitFor("New Session")
	u.key("enter")
	u.key("enter")
	u.waitFor("▸ /")
	u.key("a")
	u.waitFor("Fake (scripted)")
	for !strings.Contains(u.screen(), "save as draft") {
		u.key("tab")
	}
	u.key("write d.txt later")
	u.waitFor("keeping the prompt")
	u.key("esc")
	u.waitFor("as a draft")
	if s := u.waitFor("enter open"); !strings.Contains(s, "write d.txt later") {
		t.Errorf("no draft in the list:\n%s", s)
	}
}

// Text typed into the list by mistake doesn't delete the session: Delete
// starts on Cancel, and a space doesn't choose.
func TestTypingInTheListDeletesNothing(t *testing.T) {
	var id string
	u := startUIWith(t, 120, 36, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		ctx := context.Background()
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		var s model.Session
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &s); err != nil {
			t.Fatal(err)
		}
		id = s.ID
	})
	u.waitFor("enter open")
	u.key("down")
	u.key("D")
	u.waitFor("for good")
	u.key("o it")
	u.waitFor("for good")
	u.key("enter")
	u.waitFor("enter open")
	// A deletion would be on its way by now; let it land.
	time.Sleep(200 * time.Millisecond)
	if s, err := u.rad.Store.Session(context.Background(), id); err != nil || s == nil {
		t.Fatalf("the session went: %v", err)
	}
}

// Opening an archived session shows the Archived fold, which still folds.
func TestFoldsTheArchiveAnArchivedSessionOpened(t *testing.T) {
	u := startUIWith(t, 120, 36, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		ctx := context.Background()
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		var s model.Session
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &s); err != nil {
			t.Fatal(err)
		}
		if err := rpc.Call(ctx, "session.archive", map[string]any{"sessionId": s.ID}, nil); err != nil {
			t.Fatal(err)
		}
		if err := saveJSON(filepath.Join(dir, "state.json"), State{LastSession: s.ID}); err != nil {
			t.Fatal(err)
		}
	})
	u.waitFor("▾ Archived")
	u.key("esc")
	u.waitFor("enter open")
	u.key("up")
	u.key("left")
	u.waitFor("▸ Archived")
	u.key("down")
	if s := u.screen(); !strings.Contains(s, "▸ Archived") {
		t.Errorf("the fold opened again:\n%s", s)
	}
	u.key("enter")
	u.waitFor("▾ Archived")
	u.key("space")
	u.waitFor("▸ Archived")
}

func TestCancelsAQueuedPrompt(t *testing.T) {
	u := startUI(t, 120, 36)
	u.key("ctrl+n")
	u.waitFor("New Session")
	u.key("enter")
	u.key("enter")
	u.waitFor("▸ /")
	u.key("a")
	u.waitFor("Fake (scripted)")
	for !strings.Contains(u.screen(), "save as draft") {
		u.key("tab")
	}
	u.key("slow reply with a good many words in it, so that it streams for a while yet")
	u.key("enter")
	u.waitFor("Echo: slow")
	u.key("never mind")
	u.key("enter")
	u.waitFor("x cancels it")
	// The queued prompt is the last item.
	u.key("shift+tab")
	u.key("end")
	u.key("x")
	u.waitFor("Not sent")
	// The reply ends; nothing starts after it.
	u.waitFor("while yet")
	time.Sleep(300 * time.Millisecond)
	if s := u.screen(); strings.Contains(s, "Echo: never mind") || !strings.Contains(s, "Not sent") {
		t.Errorf("the cancelled prompt ran:\n%s", s)
	}
}

func TestNarrowWindowShowsOnePane(t *testing.T) {
	u := startUI(t, 80, 30)
	s := u.waitFor("No sessions yet")
	if strings.Contains(s, "No session open") {
		t.Errorf("a narrow window shows the session pane:\n%s", s)
	}
}

func TestMarkdownKeepsHTMLAndSplitsImages(t *testing.T) {
	src := "Some <b>bold</b> text.\n\n<div>\nraw block\n</div>\n\n![shot](rad-image:abc.png)\n\n- [ ] todo\n- [x] done\n\n`<code>`"
	blocks := renderMarkdown(src, 60, NewPalette(true))
	var text []string
	var images []string
	for _, b := range blocks {
		if b.imageID != "" {
			images = append(images, b.imageID)
			continue
		}
		for _, l := range b.lines {
			text = append(text, ansi.Strip(l))
		}
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{"<b>bold</b>", "<div>", "raw block", "☐ todo", "☑ done", "<code>"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in\n%s", want, all)
		}
	}
	if !slices.Equal(images, []string{"abc.png"}) {
		t.Errorf("images %v", images)
	}
	// An image inside a paragraph stays in it.
	if parts := splitImages("see ![x](rad-image:a.png) here"); len(parts) != 1 || parts[0].imageID != "" {
		t.Errorf("split an inline image: %+v", parts)
	}
}

// Code takes each theme's colours: Glamour keeps the first it was given
// under its own name.
func TestCodeColoursFollowTheTheme(t *testing.T) {
	code := "```go\nfunc main() {}\n```"
	dark := strings.Join(markdownLines(code, 40, NewPalette(true)), "\n")
	light := strings.Join(markdownLines(code, 40, NewPalette(false)), "\n")
	// The keyword in Frappé's mauve, then in Latte's.
	if !strings.Contains(dark, "202;158;230") || !strings.Contains(light, "136;57;239") {
		t.Errorf("dark %q\nlight %q", dark, light)
	}
}

func TestPastedImagePaths(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a shot.png")
	f, _ := os.Create(p)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	f.Close()
	txt := filepath.Join(dir, "notes.txt")
	os.WriteFile(txt, []byte("x"), 0o600)
	escaped := strings.ReplaceAll(p, " ", `\ `)
	for _, in := range []string{escaped, "'" + p + "'", `"` + p + `"`, "file://" + strings.ReplaceAll(p, " ", "%20"), escaped + " " + escaped} {
		if got := imagePaths(in); len(got) == 0 || got[0] != p {
			t.Errorf("%q: %v", in, got)
		}
	}
	for _, in := range []string{"hello there", txt, escaped + " " + txt, dir} {
		if got := imagePaths(in); got != nil {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestNotificationSequences(t *testing.T) {
	cases := map[notifier]string{
		{osc: "9"}:             "\x1b]9;t: b\a",
		{osc: "777"}:           "\x1b]777;notify;t;b\a",
		{osc: "9", tmux: true}: "\x1bPtmux;\x1b\x1b]9;t: b\a\x1b\\",
		{osc: "99"}:            "\x1b]99;i=1:d=0;t\x1b\\\x1b]99;i=1:d=1:p=body;b\x1b\\",
	}
	for n, want := range cases {
		if got := n.sequence("t", "b"); got != want {
			t.Errorf("%+v: %q, want %q", n, got, want)
		}
	}
	if clean("a;b\x1bc") != "a b c" {
		t.Error(clean("a;b\x1bc"))
	}
}

func TestHighlightSurvivesResets(t *testing.T) {
	st := NewStyles(NewPalette(true))
	line := st.Red.Render("red") + " plain " + st.Green.Render("green")
	out := highlight(line, 30, color.RGBA{1, 2, 3, 255})
	bg := "\x1b[48;2;1;2;3m"
	if !strings.HasPrefix(out, bg) || strings.Count(out, bg) < 3 {
		t.Errorf("background lost: %q", out)
	}
	if w := ansi.StringWidth(out); w != 30 {
		t.Errorf("width %d", w)
	}
}

func TestHalfBlocks(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 50))
	lines := halfBlocks(img, 20, 10, color.Black)
	if len(lines) != 5 || ansi.StringWidth(lines[0]) != 20 {
		t.Fatalf("%d lines of %d", len(lines), ansi.StringWidth(lines[0]))
	}
}

// at is where text first shows on the screen: its column and row.
func at(screen, text string) (int, int, bool) {
	for y, line := range strings.Split(screen, "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y, true
		}
	}
	return 0, 0, false
}

// A drag over the messages selects their text alone, not the sidebar's
// beside it, and copies it.
func TestSelectingCopiesTheMessagesAlone(t *testing.T) {
	u := startUIWith(t, 120, 36, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		ctx := context.Background()
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		var s model.Session
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &s); err != nil {
			t.Fatal(err)
		}
		if err := saveJSON(filepath.Join(dir, "state.json"), State{LastSession: s.ID}); err != nil {
			t.Fatal(err)
		}
	})
	u.waitFor("No prompts yet")
	u.key("hello there")
	u.key("enter")
	s := u.waitFor("Echo: hello there")
	x0, y0, ok0 := at(s, "▎ hello there")
	x1, y1, ok1 := at(s, "Echo: hello there")
	if !ok0 || !ok1 {
		t.Fatalf("no prompt or reply on\n%s", s)
	}
	// From the prompt's text to the end of the reply.
	left := tea.MouseLeft
	u.p.Send(tea.MouseClickMsg{X: x0 + 2, Y: y0, Button: left})
	u.p.Send(tea.MouseMotionMsg{X: x1 + 5, Y: y1 - 1, Button: left})
	u.p.Send(tea.MouseMotionMsg{X: x1 + len("Echo: hello there") - 1, Y: y1, Button: left})
	u.p.Send(tea.MouseReleaseMsg{X: x1 + len("Echo: hello there") - 1, Y: y1, Button: left})
	select {
	case got := <-u.clip:
		if !strings.HasPrefix(got, "hello there\n") || !strings.HasSuffix(got, "\nEcho: hello there") || strings.Contains(got, "│") {
			t.Errorf("copied %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was copied")
	}
	u.waitFor("Copied.")
}
