package tui

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
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

// inspectMsg runs f on the program's goroutine, where the model's state is.
type inspectMsg struct {
	f    func(*Model)
	done chan struct{}
}

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
			if in, ok := msg.(inspectMsg); ok {
				in.f(model.(*Model))
				close(in.done)
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

func (u *ui) inspect(f func(*Model)) {
	u.t.Helper()
	done := make(chan struct{})
	u.p.Send(inspectMsg{f, done})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		u.t.Fatal("the interface didn't answer")
	}
}

// menuTo moves the open menu's cursor down to the item labelled label.
func (u *ui) menuTo(label string) {
	u.t.Helper()
	for range 40 {
		on := ""
		u.inspect(func(m *Model) {
			if mn, ok := m.topOverlay().(*menu); ok && mn.selectable(mn.cursor) {
				on = mn.items[mn.cursor].label
			}
		})
		if on == label {
			return
		}
		u.key("down")
	}
	u.t.Fatalf("no %q in the menu:\n%s", label, u.screen())
}

// turnsEnded waits until the session has n turns, and all of them ended,
// on rad and, when it's open, in ra's store too, whose menus go by it.
func (u *ui) turnsEnded(sessionID string, n int) {
	u.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		turns, err := u.rad.Store.Turns(context.Background(), sessionID)
		ended := err == nil && len(turns) == n
		for _, t := range turns {
			ended = ended && t.Status != model.TurnRunning
		}
		if ended {
			u.inspect(func(m *Model) {
				if m.sess != nil && m.sess.store.ID == sessionID {
					ended = len(m.sess.store.SortedTurns()) == n && !m.sess.store.IsRunning()
					for _, t := range m.sess.store.SortedTurns() {
						ended = ended && t.Status != model.TurnRunning
					}
				}
			})
		}
		if ended {
			return
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("turns %+v, %v", turns, err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// forkOf is the session rad forked from sessionID.
func (u *ui) forkOf(sessionID string) *model.Session {
	u.t.Helper()
	ss, err := u.rad.Store.Sessions(context.Background())
	if err != nil {
		u.t.Fatal(err)
	}
	for _, s := range ss {
		if s.ForkedFrom != nil && s.ForkedFrom.SessionID == sessionID {
			return s
		}
	}
	u.t.Fatalf("no fork of %s", sessionID)
	return nil
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

// The last choice of a question is an answer in the user's own words.
func TestAnswersAQuestionInOwnWords(t *testing.T) {
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
	u.key("a question")
	u.key("enter")
	u.waitFor("Other…")
	// Past Red and Blue to Other, where keys type.
	for range 3 {
		u.key("right")
	}
	u.waitFor("your own answer")
	u.key("teal, not green")
	s := u.waitFor("Other: teal, not green")
	if strings.Contains(s, "space choose") {
		t.Errorf("the hints are the choices':\n%s", s)
	}
	// Enter takes it, and enter again answers.
	u.key("enter")
	u.key("enter")
	u.waitFor("Which color? → teal, not green")

	// A label replaces it again.
	u.key("another question")
	u.key("enter")
	u.waitFor("Other…")
	for range 3 {
		u.key("right")
	}
	u.key("pink")
	// ← moves through the text, then from its start back to Blue.
	for range 5 {
		u.key("left")
	}
	u.key("enter")
	u.key("enter")
	if s := u.waitFor("Which color? → Blue"); strings.Contains(s, "pink") {
		t.Errorf("the label didn't replace the own answer:\n%s", s)
	}
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

// A new session starts in the project ra was started in, the innermost
// if projects nest.
func TestProjectAtTheWorkingFolder(t *testing.T) {
	ps := map[string]*model.Project{
		"code": {ID: "code", Path: "/home/me/code"},
		"app":  {ID: "app", Path: "/home/me/code/app"},
		"web":  {ID: "web", Path: "/home/me/web/"},
	}
	for dir, want := range map[string]string{
		"/home/me/code":                "code",
		"/home/me/code/lib":            "code",
		"/home/me/code/app":            "app",
		"/home/me/code/app/.claude/wt": "app",
		"/home/me/code/application":    "code",
		"/home/me/web/src":             "web",
		"/home/me/website":             "",
		"/home/me":                     "",
		"":                             "",
	} {
		if got := projectAt(ps, dir); got != want {
			t.Errorf("projectAt(%q) = %q, want %q", dir, got, want)
		}
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

func TestSteersPromptsIntoTheTurn(t *testing.T) {
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
	u.waitFor("ctrl+t steers it into the turn")
	u.key("turn left")
	u.key("enter")
	u.waitFor("t steers it in")
	// The queued prompt is the last item.
	u.key("shift+tab")
	u.key("end")
	u.key("t")
	u.waitFor("Steered: turn left")
	// From the prompt, ctrl+t steers at once.
	u.key("tab")
	u.key("slow again, with a good many words in it, so that it streams for a while")
	u.key("enter")
	u.waitFor("Echo: slow again")
	u.key("and right")
	u.key("ctrl+t")
	u.waitFor("Steered: and right")
	time.Sleep(300 * time.Millisecond)
	if s := u.screen(); strings.Contains(s, "Echo: turn left") || strings.Contains(s, "Echo: and right") || strings.Contains(s, "Queued") {
		t.Errorf("a steered prompt waited for a turn of its own:\n%s", s)
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

// A session forks from its actions menu, after the turn chosen in the
// form, and the fork opens.
func TestForksASessionAfterATurn(t *testing.T) {
	var src string
	u := startUIWith(t, 120, 44, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		ctx := context.Background()
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		var s model.Session
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &s); err != nil {
			t.Fatal(err)
		}
		src = s.ID
		if err := saveJSON(filepath.Join(dir, "state.json"), State{LastSession: s.ID}); err != nil {
			t.Fatal(err)
		}
	})
	u.waitFor("No prompts yet")
	// Before a turn has ended there's nothing to fork from.
	u.key("ctrl+p")
	if s := u.waitFor("Delete…"); strings.Contains(s, "Fork…") {
		t.Errorf("Fork offered before a turn:\n%s", s)
	}
	u.key("esc")
	u.key("first thing")
	u.key("enter")
	u.waitFor("Echo: first thing")
	u.key("second thing")
	u.key("enter")
	u.waitFor("Echo: second thing")
	u.turnsEnded(src, 2)

	u.key("ctrl+p")
	u.menuTo("Fork…")
	u.key("enter")
	s := u.waitFor("Fork Session")
	for _, want := range []string{"After turn", "Turn 2  second thing", "Fake (scripted)", "enter fork", "the fork opens with the conversation"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	// Up to the turn, at the top, and the one before.
	for range 8 {
		u.key("up")
	}
	u.key("enter")
	u.waitFor("After Turn")
	u.key("down")
	u.key("enter")
	u.waitFor("Turn 1  first thing")
	// Another effort than the session's.
	u.key("down")
	u.key("down")
	u.key("down")
	u.key("right")
	u.waitFor("Low")
	for !strings.Contains(u.screen(), "enter fork") {
		u.key("tab")
	}
	u.key("third thing")
	u.key("enter")
	// The fork opens, with the first turn and its own prompt.
	if s = u.waitFor("Echo: third thing"); !strings.Contains(s, "after turn 1.") {
		t.Errorf("not the fork after turn 1:\n%s", s)
	}
	f := u.forkOf(src)
	if f.ForkedFrom.Turn != 1 || f.Harness != "fake" || f.Effort != "low" || f.Workspace.Kind != model.WorkspaceRoot {
		t.Errorf("fork %+v", f)
	}
	u.inspect(func(m *Model) {
		if m.sess == nil || m.sess.store.ID != f.ID || m.focus != focusComposer || len(m.overlays) > 0 {
			t.Errorf("the fork isn't open with its prompt focused")
		}
	})
	items, err := u.rad.Store.Items(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	copied := false
	for _, it := range items {
		if strings.Contains(it.Text, "second thing") {
			t.Errorf("the fork has turn 2: %+v", it)
		}
		// The agent copied its conversation as it stood after turn 1.
		copied = copied || strings.HasPrefix(it.Text, "Forked fake-"+src+" at fake-"+src+"#1.")
	}
	if !copied {
		t.Errorf("the agent didn't fork its conversation: %+v", items)
	}
}

// A worktree session's fork gets a worktree of its own, unless the switch
// is turned off.
func TestForksAWorktreeSession(t *testing.T) {
	var src string
	u := startUIWith(t, 120, 44, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		for _, args := range [][]string{
			{"init", "-q", "-b", "main"},
			{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = r.Root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Skipf("git %v: %v\n%s", args, err, out)
			}
		}
		ctx := context.Background()
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		var s model.Session
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake",
			"workspace": map[string]any{"kind": "worktree"}, "prompt": "hello"}, &s); err != nil {
			t.Fatal(err)
		}
		src = s.ID
		if err := saveJSON(filepath.Join(dir, "state.json"), State{LastSession: s.ID}); err != nil {
			t.Fatal(err)
		}
	})
	u.waitFor("Echo: hello")
	u.turnsEnded(src, 1)
	u.key("ctrl+p")
	u.menuTo("Fork…")
	u.key("enter")
	u.waitFor("Fork Session")
	// Up from the prompt to the branch, under the switch, which is on.
	u.key("up")
	s := u.waitFor("own branch, with the files as they were")
	if !strings.Contains(s, "◉ Isolated git worktree") {
		t.Errorf("the worktree switch is off:\n%s", s)
	}
	u.key("up")
	u.key("space")
	u.waitFor("in the project folder, as it is now")
	u.key("space")
	u.key("down")
	u.key("ra/forked")
	u.key("enter")
	u.waitFor("enter fork")
	u.key("enter")
	u.waitFor("after turn 1.")
	f := u.forkOf(src)
	if f.Workspace.Kind != model.WorkspaceWorktree || f.Workspace.Branch != "ra/forked" {
		t.Errorf("fork %+v", f)
	}
}

func TestCommitsPushesAndOpensAPullRequest(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.com", "GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.com"} {
		t.Setenv(k, v)
	}
	// gh answers as GitHub would; origin is a bare repository.
	bin := t.TempDir()
	gh := "#!/bin/sh\ncase \"$1 $2\" in\n\"pr list\") echo '[]' ;;\n\"pr create\") cat >/dev/null; echo https://github.com/test/repo/pull/12 ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	origin := filepath.Join(t.TempDir(), "origin.git")

	var src model.Session
	u := startUIWith(t, 120, 44, func(rpc *client.RPCClient, r *radtest.Rad, dir string) {
		for _, args := range [][]string{{"init", "-q", "-b", "main", r.Root}, {"-C", r.Root, "commit", "-q", "--allow-empty", "-m", "init"},
			{"init", "-q", "--bare", "-b", "main", origin}, {"-C", r.Root, "remote", "add", "origin", origin}, {"-C", r.Root, "push", "-q", "origin", "main"}} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Skipf("git %v: %v\n%s", args, err, out)
			}
		}
		ctx := context.Background()
		if err := rpc.Call(ctx, "server.setSettings", map[string]any{"systemAgent": map[string]string{"harness": "fake", "model": "echo"}}, nil); err != nil {
			t.Fatal(err)
		}
		var p model.Project
		if err := rpc.Call(ctx, "project.add", map[string]any{"path": r.Root}, &p); err != nil {
			t.Fatal(err)
		}
		if err := rpc.Call(ctx, "session.create", map[string]any{"commandId": "c1", "projectId": p.ID, "harness": "fake",
			"workspace": map[string]any{"kind": "worktree", "branch": "ra/notes"}, "prompt": "hello"}, &src); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src.Workspace.Path, "notes.txt"), []byte("hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := saveJSON(filepath.Join(dir, "state.json"), State{LastSession: src.ID}); err != nil {
			t.Fatal(err)
		}
	})
	u.waitFor("Echo: hello")
	u.turnsEnded(src.ID, 1)
	u.key("ctrl+p")
	u.menuTo("Commit…")
	u.key("enter")
	s := u.waitFor("CHANGES")
	for _, want := range []string{"⑂ ra/notes → origin", "A notes.txt", "+1 −0", "Change notes.txt", "Written by the fake system agent.",
		"Commit & push", "Commit & Push"} {
		if !strings.Contains(s, want) {
			t.Errorf("the form lacks %q:\n%s", want, s)
		}
	}
	// The message is the system agent's, to edit: a word more.
	u.key("end")
	u.key(" today")
	u.key("tab")
	u.key("right")
	s = u.waitFor("Pull request into main")
	if !strings.Contains(s, "Commit, Push & Open PR") {
		t.Errorf("the button:\n%s", s)
	}
	u.key("ctrl+s")
	s = u.waitFor("✓ Done.")
	for _, want := range []string{"✓ Commit", "✓ Push to origin", "ra/notes → origin", "✓ Open a pull request", "#12 Fake: Change notes.txt",
		"https://github.com/test/repo/pull/12", "$ git push --set-upstream origin ra/notes"} {
		if !strings.Contains(s, want) {
			t.Errorf("the progress lacks %q:\n%s", want, s)
		}
	}
	out, err := exec.Command("git", "-C", origin, "log", "-1", "--format=%B", "ra/notes").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Written by the fake system agent. today") {
		t.Errorf("origin's commit: %s %v", out, err)
	}
	u.key("y")
	select {
	case c := <-u.clip:
		if !strings.Contains(c, "https://github.com/test/repo/pull/12") {
			t.Errorf("copied %q", c)
		}
	case <-time.After(5 * time.Second):
		t.Error("nothing copied")
	}
	u.inspect(func(m *Model) {
		if m.state.LastCommit != "pullRequest" {
			t.Errorf("remembered %q", m.state.LastCommit)
		}
	})
	u.key("esc")
	u.waitFor("Committed ")

	// Again, with nothing to commit: the form starts at the last choice.
	u.key("ctrl+p")
	u.menuTo("Commit…")
	u.key("enter")
	s = u.waitFor("Nothing to commit.")
	if !strings.Contains(s, "Commit, push & open PR") || !strings.Contains(s, "Push & Open PR") || strings.Contains(s, "MESSAGE") {
		t.Errorf("the clean form:\n%s", s)
	}
	u.key("esc")
}

// The new session form picks up a session the agent ran in the terminal:
// the fake's two, the one written just now with a warning.
func TestContinuesATerminalSession(t *testing.T) {
	u := startUI(t, 120, 44)
	u.key("ctrl+n")
	u.waitFor("New Session")
	u.key("enter")
	u.key("enter")
	u.waitFor("▸ /")
	u.key("a")
	u.waitFor("Continue a terminal session…")
	for {
		var on bool
		u.inspect(func(m *Model) {
			ns, ok := m.topOverlay().(*newSession)
			on = ok && ns.field == fieldImport
		})
		if on {
			break
		}
		u.key("down")
	}
	u.key("enter")
	s := u.waitFor("Fix the login redirect")
	for _, want := range []string{"Terminal Sessions", "Add a dark theme to the settings page", "6 messages · just now",
		"⚠ Active in the last minute", "4 messages · 2h ago", "Picks up a session Fake (scripted) ran in this folder"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	u.key("down")
	u.key("enter")
	u.waitFor("Picked up from Fake (scripted)")
	u.key("carry on")
	u.key("enter")
	s = u.waitFor("Echo: carry on")
	if !strings.Contains(s, "Picked up terminal-1-") || !strings.Contains(s, "Done in the terminal: add a test for it.") {
		t.Errorf("no resumed reply or transcript in\n%s", s)
	}

	// Picked up, it's gone from the list.
	u.key("ctrl+n")
	u.waitFor("New Session")
	for {
		var on bool
		u.inspect(func(m *Model) {
			ns, ok := m.topOverlay().(*newSession)
			on = ok && ns.field == fieldImport
		})
		if on {
			break
		}
		u.key("up")
	}
	u.key("enter")
	u.waitFor("Add a dark theme")
	u.inspect(func(m *Model) {
		il, ok := m.topOverlay().(*importList)
		if !ok || len(il.sessions) != 1 || il.sessions[0].Title != "Add a dark theme to the settings page" {
			t.Errorf("listed after the import: %#v", m.topOverlay())
		}
	})
}
