package main

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The screen as a picture, drawn with the Mac's fonts: Menlo, then Apple
// Symbols and Arial Unicode for what Menlo lacks.

var fontFiles = []string{
	"/System/Library/Fonts/Menlo.ttc",
	"/System/Library/Fonts/Apple Symbols.ttf",
	"/Library/Fonts/Arial Unicode.ttf",
	"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
}

type faceSet struct {
	fonts  []*sfnt.Font
	faces  []font.Face
	bold   font.Face
	cw     int
	ch     int
	ascent int
}

var (
	facesOnce sync.Once
	faces     *faceSet
	facesErr  error
)

const fontSize = 26

func loadFaces() (*faceSet, error) {
	fs := &faceSet{}
	opts := &opentype.FaceOptions{Size: fontSize, DPI: 72, Hinting: font.HintingFull}
	for i, path := range fontFiles {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var fnt *sfnt.Font
		if c, err := opentype.ParseCollection(b); err == nil {
			fnt, _ = c.Font(0)
			if i == 0 && c.NumFonts() > 1 {
				if bold, err := c.Font(1); err == nil {
					fs.bold, _ = opentype.NewFace(bold, opts)
				}
			}
		} else if f, err := opentype.Parse(b); err == nil {
			fnt = f
		}
		if fnt == nil {
			continue
		}
		face, err := opentype.NewFace(fnt, opts)
		if err != nil {
			continue
		}
		fs.fonts = append(fs.fonts, fnt)
		fs.faces = append(fs.faces, face)
	}
	if len(fs.faces) == 0 {
		return nil, errors.New("no fonts to draw with")
	}
	m := fs.faces[0].Metrics()
	adv, _ := fs.faces[0].GlyphAdvance('M')
	fs.cw = int(math.Ceil(float64(adv) / 64))
	fs.ascent = m.Ascent.Ceil()
	fs.ch = (m.Ascent + m.Descent).Ceil() + 4
	return fs, nil
}

func (fs *faceSet) face(r rune, bold bool) font.Face {
	var buf sfnt.Buffer
	for i, f := range fs.fonts {
		if idx, err := f.GlyphIndex(&buf, r); err == nil && idx != 0 {
			if i == 0 && bold && fs.bold != nil {
				return fs.bold
			}
			return fs.faces[i]
		}
	}
	return fs.faces[0]
}

func (t *term) png(path string) error {
	facesOnce.Do(func() { faces, facesErr = loadFaces() })
	if facesErr != nil {
		return facesErr
	}
	fs := faces
	t.mu.Lock()
	defer t.mu.Unlock()
	w, h := t.emu.Width(), t.emu.Height()
	defBg, defFg := t.emu.BackgroundColor(), t.emu.ForegroundColor()
	if defBg == nil {
		defBg = color.Black
	}
	if defFg == nil {
		defFg = color.White
	}
	img := image.NewRGBA(image.Rect(0, 0, w*fs.cw, h*fs.ch))
	draw.Draw(img, img.Bounds(), image.NewUniform(defBg), image.Point{}, draw.Src)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := t.emu.CellAt(x, y)
			if c == nil {
				continue
			}
			fg, bg := c.Style.Fg, c.Style.Bg
			if fg == nil {
				fg = defFg
			}
			if bg == nil {
				bg = defBg
			}
			if c.Style.Attrs&uv.AttrReverse != 0 {
				fg, bg = bg, fg
			}
			if c.Style.Attrs&uv.AttrFaint != 0 {
				fg = blend(fg, bg, 0.6)
			}
			cw := fs.cw * max(1, c.Width)
			rect := image.Rect(x*fs.cw, y*fs.ch, x*fs.cw+cw, (y+1)*fs.ch)
			draw.Draw(img, rect, image.NewUniform(bg), image.Point{}, draw.Src)
			switch c.Content {
			case "", " ":
			case "▀":
				draw.Draw(img, image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+fs.ch/2), image.NewUniform(fg), image.Point{}, draw.Src)
			case "█":
				draw.Draw(img, rect, image.NewUniform(fg), image.Point{}, draw.Src)
			case "│", "─", "╭", "╮", "╰", "╯", "┃", "▌", "▎":
				boxGlyph(img, rect, c.Content, fg)
			default:
				r := []rune(c.Content)[0]
				d := font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: fs.face(r, c.Style.Attrs&uv.AttrBold != 0),
					Dot: fixed.P(rect.Min.X, rect.Min.Y+fs.ascent+2)}
				d.DrawString(c.Content)
			}
			if c.Style.Underline != 0 {
				draw.Draw(img, image.Rect(rect.Min.X, rect.Max.Y-3, rect.Max.X, rect.Max.Y-1), image.NewUniform(fg), image.Point{}, draw.Src)
			}
			if c.Width > 1 {
				x += c.Width - 1
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// boxGlyph draws lines that join from cell to cell, as fonts' don't.
func boxGlyph(img *image.RGBA, r image.Rectangle, g string, c color.Color) {
	u := image.NewUniform(c)
	mx, my := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	hline := func(x0, x1 int) { draw.Draw(img, image.Rect(x0, my-1, x1, my+1), u, image.Point{}, draw.Src) }
	vline := func(y0, y1 int) { draw.Draw(img, image.Rect(mx-1, y0, mx+1, y1), u, image.Point{}, draw.Src) }
	switch g {
	case "│", "┃":
		vline(r.Min.Y, r.Max.Y)
	case "─":
		hline(r.Min.X, r.Max.X)
	case "╭":
		hline(mx, r.Max.X)
		vline(my, r.Max.Y)
	case "╮":
		hline(r.Min.X, mx)
		vline(my, r.Max.Y)
	case "╰":
		hline(mx, r.Max.X)
		vline(r.Min.Y, my)
	case "╯":
		hline(r.Min.X, mx)
		vline(r.Min.Y, my)
	case "▌":
		draw.Draw(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+r.Dx()/2, r.Max.Y), u, image.Point{}, draw.Src)
	case "▎":
		draw.Draw(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+r.Dx()/4, r.Max.Y), u, image.Point{}, draw.Src)
	}
}

func blend(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 { return uint8((float64(x)*t + float64(y)*(1-t)) / 257) }
	return color.RGBA{mix(ar, br), mix(ag, bg), mix(ab, bb), 255}
}
