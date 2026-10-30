package tui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"remote-agent/internal/model"
)

// Images in the transcript are drawn with half blocks, two pixels to a
// cell; o opens one in the computer's viewer, or full screen over SSH.

type imageStore struct {
	entries map[string]*imageEntry
	// version counts images loaded, for the transcript's cache.
	version int
}

type imageEntry struct {
	img     image.Image
	err     error
	loading bool
	wanted  bool
	thumbs  map[[3]int][]string
}

type imageLoadedMsg struct {
	id  string
	img image.Image
	err error
}

func newImageStore() *imageStore {
	return &imageStore{entries: map[string]*imageEntry{}}
}

func (s *imageStore) entry(id string) *imageEntry {
	e := s.entries[id]
	if e == nil {
		e = &imageEntry{thumbs: map[[3]int][]string{}}
		s.entries[id] = e
	}
	return e
}

func (s *imageStore) loaded(msg imageLoadedMsg) {
	e := s.entry(msg.id)
	e.loading = false
	e.img, e.err = msg.img, msg.err
	s.version++
}

// fetch loads the images drawn and not loaded yet.
func (s *imageStore) fetch(m *Model) tea.Cmd {
	if !m.conn.IsConnected() {
		return nil
	}
	var cmds []tea.Cmd
	for id, e := range s.entries {
		if e.wanted && !e.loading && e.img == nil && e.err == nil {
			e.loading = true
			cmds = append(cmds, loadImage(m, id))
		}
	}
	return tea.Batch(cmds...)
}

func loadImage(m *Model, id string) tea.Cmd {
	conn := m.conn
	return func() tea.Msg {
		data, err := conn.Image(context.Background(), id)
		if err != nil {
			return imageLoadedMsg{id: id, err: err}
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		return imageLoadedMsg{id: id, img: img, err: err}
	}
}

func imageLabel(ref model.ImageRef) string {
	kind := strings.TrimPrefix(ref.MimeType, "image/")
	if kind == "" {
		kind = "image"
	}
	if ref.Width > 0 && ref.Height > 0 {
		return fmt.Sprintf("▣ %s %d×%d", kind, ref.Width, ref.Height)
	}
	return "▣ " + kind
}

// thumb draws an image at most w cells wide and h high, once loaded.
func (s *imageStore) thumb(m *Model, ref model.ImageRef, w, h int) []string {
	e := s.entry(ref.ID)
	e.wanted = true
	switch {
	case e.err != nil:
		return []string{m.st.Faint.Render(imageLabel(ref) + " (couldn't load)")}
	case e.img == nil:
		return []string{m.st.Faint.Render(imageLabel(ref) + " " + m.spinner())}
	}
	dark := 0
	if m.pal.Dark {
		dark = 1
	}
	key := [3]int{w, h, dark}
	if t, ok := e.thumbs[key]; ok {
		return t
	}
	t := halfBlocks(e.img, w, h, m.pal.Base)
	e.thumbs[key] = t
	return t
}

// halfBlocks draws img in cells of ▀, the top pixel in the foreground and
// the bottom one in the background, on bg where it's transparent.
func halfBlocks(img image.Image, maxW, maxH int, bg color.Color) []string {
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 || maxW < 1 || maxH < 1 {
		return nil
	}
	scale := min(float64(maxW)/float64(b.Dx()), float64(maxH*2)/float64(b.Dy()), 1)
	cw := max(1, int(float64(b.Dx())*scale+0.5))
	ph := max(2, int(float64(b.Dy())*scale+0.5))
	ph += ph % 2
	dst := image.NewRGBA(image.Rect(0, 0, cw, ph))
	xdraw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, xdraw.Src)
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	lines := make([]string, 0, ph/2)
	var sb strings.Builder
	for y := 0; y < ph; y += 2 {
		sb.Reset()
		for x := 0; x < cw; x++ {
			t := dst.RGBAAt(x, y)
			u := dst.RGBAAt(x, y+1)
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", t.R, t.G, t.B, u.R, u.G, u.B)
		}
		sb.WriteString("\x1b[0m")
		lines = append(lines, sb.String())
	}
	return lines
}

// openImages shows images: in the computer's own viewer when ra runs at
// it, else full screen in the terminal.
func (m *Model) openImages(refs []model.ImageRef) tea.Cmd {
	if len(refs) == 0 {
		return nil
	}
	if m.sshSession || m.conn.Images == nil {
		m.push(&imageViewer{refs: refs})
		return nil
	}
	conn := m.conn
	return m.do(func(ctx context.Context) error {
		for _, ref := range refs {
			if _, err := conn.Image(ctx, ref.ID); err != nil {
				return err
			}
			opener := "xdg-open"
			if runtime.GOOS == "darwin" {
				opener = "open"
			}
			if err := exec.Command(opener, conn.Images.Path(ref.ID)).Start(); err != nil {
				return fmt.Errorf("%s: %w", opener, err)
			}
		}
		return nil
	}, nil)
}

// imageViewer shows images full screen, ← and → paging through them.
type imageViewer struct {
	refs []model.ImageRef
	i    int
}

func (iv *imageViewer) fullscreen() bool { return true }

func (iv *imageViewer) hints() [][2]string {
	return [][2]string{{"←→", "page"}, {"esc", "close"}}
}

func (iv *imageViewer) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "left", "h":
		iv.i = max(0, iv.i-1)
	case "right", "l", "space":
		iv.i = min(len(iv.refs)-1, iv.i+1)
	default:
		m.pop()
	}
	return nil
}

func (iv *imageViewer) view(m *Model, width, height int) string {
	ref := iv.refs[iv.i]
	caption := imageLabel(ref)
	if len(iv.refs) > 1 {
		caption += fmt.Sprintf("  %d of %d", iv.i+1, len(iv.refs))
	}
	lines := []string{centered(m.st.Subtext.Render(caption), width)}
	for _, l := range m.images.thumb(m, ref, width-2, height-3) {
		lines = append(lines, centered(l, width))
	}
	return fill(lines, width, height-1)
}
