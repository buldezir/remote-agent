package tui

import (
	"slices"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Agents' Markdown, as AgentMarkdown draws it in the apps: an image alone
// in its paragraph, as rad links it (rad-image:<id>), sits between the
// texts; raw HTML stays text; long lines wrap, code too.

// mdBlock is a part of a reply: text lines, or an image.
type mdBlock struct {
	lines   []string
	imageID string
	alt     string
}

var gm = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderers are Glamour's, by theme and width; making one is slow.
var renderers sync.Map

type rendererKey struct {
	dark  bool
	width int
}

func renderer(p Palette, width int) *glamour.TermRenderer {
	key := rendererKey{p.Dark, width}
	if r, ok := renderers.Load(key); ok {
		return r.(*glamour.TermRenderer)
	}
	r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(p)), glamour.WithWordWrap(width), glamour.WithChromaFormatter("terminal16m"))
	if err != nil {
		return nil
	}
	renderers.Store(key, r)
	return r
}

// renderMarkdown renders Markdown to lines width wide at most, with the
// images that stand alone in a paragraph as blocks of their own.
func renderMarkdown(src string, width int, p Palette) []mdBlock {
	var blocks []mdBlock
	for _, part := range splitImages(src) {
		if part.imageID != "" {
			blocks = append(blocks, part)
			continue
		}
		if lines := markdownLines(part.lines[0], width, p); len(lines) > 0 {
			blocks = append(blocks, mdBlock{lines: lines})
		}
	}
	return blocks
}

func markdownLines(src string, width int, p Palette) []string {
	src = escapeHTML(src)
	if strings.TrimSpace(src) == "" {
		return nil
	}
	width = max(width, 10)
	r := renderer(p, width)
	out := src
	if r != nil {
		if s, err := r.Render(src); err == nil {
			out = s
		}
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, " ")
		// Glamour can overrun the width (a long word, a table); wrap it
		// rather than let the terminal.
		if ansi.StringWidth(l) > width {
			lines = append(lines, strings.Split(ansi.Hardwrap(l, width, true), "\n")...)
		} else {
			lines = append(lines, l)
		}
	}
	// Glamour frames the document with blank lines.
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// splitImages cuts the source at paragraphs that are only a rad-image
// link. Text parts come back with their source in lines[0].
func splitImages(src string) []mdBlock {
	source := []byte(src)
	doc := gm.Parser().Parse(text.NewReader(source))
	var parts []mdBlock
	last := 0
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		p, ok := n.(*ast.Paragraph)
		if !ok || p.Lines().Len() == 0 {
			continue
		}
		img := soleImage(p, source)
		if img == nil {
			continue
		}
		id, ok := strings.CutPrefix(string(img.Destination), "rad-image:")
		if !ok || id == "" {
			continue
		}
		start := p.Lines().At(0).Start
		stop := p.Lines().At(p.Lines().Len() - 1).Stop
		if start > last {
			parts = append(parts, mdBlock{lines: []string{src[last:start]}})
		}
		parts = append(parts, mdBlock{imageID: id, alt: string(img.Text(source))})
		last = stop
	}
	if last < len(src) {
		parts = append(parts, mdBlock{lines: []string{src[last:]}})
	}
	return parts
}

// soleImage is the paragraph's image, if it holds nothing else but space.
func soleImage(p *ast.Paragraph, source []byte) *ast.Image {
	var img *ast.Image
	for c := p.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Image:
			if img != nil {
				return nil
			}
			img = c
		case *ast.Text:
			if strings.TrimSpace(string(c.Segment.Value(source))) != "" {
				return nil
			}
		default:
			return nil
		}
	}
	return img
}

// escapeHTML puts a backslash before each < of raw HTML, so it shows as
// the agent wrote it: Glamour would sanitize it away.
func escapeHTML(src string) string {
	source := []byte(src)
	doc := gm.Parser().Parse(text.NewReader(source))
	var at []int
	escape := func(start, stop int) {
		for i := start; i < stop && i < len(source); i++ {
			if source[i] == '<' {
				at = append(at, i)
			}
		}
	}
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.HTMLBlock:
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				escape(seg.Start, seg.Stop)
			}
			if n.HasClosure() {
				escape(n.ClosureLine.Start, n.ClosureLine.Stop)
			}
		case *ast.RawHTML:
			for i := 0; i < n.Segments.Len(); i++ {
				seg := n.Segments.At(i)
				escape(seg.Start, seg.Stop)
			}
		}
		return ast.WalkContinue, nil
	})
	if len(at) == 0 {
		return src
	}
	slices.Sort(at)
	at = slices.Compact(at)
	var b strings.Builder
	last := 0
	for _, i := range at {
		b.WriteString(src[last:i])
		b.WriteString(`\`)
		last = i
	}
	b.WriteString(src[last:])
	return b.String()
}
