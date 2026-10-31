package tui

import (
	"image/color"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

// Palette is Catppuccin, Frappé in dark mode and Latte in light mode, as
// the apps have it (apple/Shared/Views/Palette.swift).
type Palette struct {
	Dark bool

	Red, Peach, Yellow, Green, Blue, Mauve, Teal, Pink color.Color
	Accent, OnAccent                                   color.Color

	Text, Subtext, Overlay        color.Color
	Surface2, Surface1, Surface0  color.Color
	Base, Mantle, Crust           color.Color
	hexText, hexSubtext, hexMauve string
	hexBlue, hexOverlay, hexGreen string
	hexRed, hexPeach, hexYellow   string
	hexSurface0, hexTeal, hexPink string
}

func hex(s string) color.Color { return lipgloss.Color(s) }

func NewPalette(dark bool) Palette {
	c := func(latte, frappe string) string {
		if dark {
			return frappe
		}
		return latte
	}
	p := Palette{
		Dark:        dark,
		hexText:     c("#4c4f69", "#c6d0f5"),
		hexSubtext:  c("#6c6f85", "#a5adce"),
		hexOverlay:  c("#8c8fa1", "#838ba7"),
		hexMauve:    c("#8839ef", "#ca9ee6"),
		hexBlue:     c("#1e66f5", "#8caaee"),
		hexGreen:    c("#40a02b", "#a6d189"),
		hexRed:      c("#d20f39", "#e78284"),
		hexPeach:    c("#fe640b", "#ef9f76"),
		hexYellow:   c("#df8e1d", "#e5c890"),
		hexSurface0: c("#ccd0da", "#414559"),
		hexTeal:     c("#179299", "#81c8be"),
		hexPink:     c("#ea76cb", "#f4b8e4"),
	}
	p.Red, p.Peach, p.Yellow, p.Green = hex(p.hexRed), hex(p.hexPeach), hex(p.hexYellow), hex(p.hexGreen)
	p.Blue, p.Mauve, p.Teal, p.Pink = hex(p.hexBlue), hex(p.hexMauve), hex(p.hexTeal), hex(p.hexPink)
	p.Text, p.Subtext, p.Overlay = hex(p.hexText), hex(p.hexSubtext), hex(p.hexOverlay)
	p.Surface2 = hex(c("#acb0be", "#626880"))
	p.Surface1 = hex(c("#bcc0cc", "#51576d"))
	p.Surface0 = hex(p.hexSurface0)
	p.Base = hex(c("#eff1f5", "#303446"))
	p.Mantle = hex(c("#e6e9ef", "#292c3c"))
	p.Crust = hex(c("#dce0e8", "#232634"))
	p.Accent = p.Mauve
	p.OnAccent = p.Base
	return p
}

// Styles are the palette's styles for what ra draws.
type Styles struct {
	P Palette

	Text, Subtext, Faint, Bold      lipgloss.Style
	Accent, AccentBold              lipgloss.Style
	Red, Green, Yellow, Blue, Peach lipgloss.Style
	Header                          lipgloss.Style // a section header
	Rule                            lipgloss.Style
	Key                             lipgloss.Style // a key in a hint
	Chip, ChipOn                    lipgloss.Style
	Button, ButtonDeny, ButtonOn    lipgloss.Style
	Code                            lipgloss.Style
	Add, Del                        lipgloss.Style
	Match                           lipgloss.Style // a search's match in a snippet
	Panel                           lipgloss.Style // a docked panel's border
	Dialog                          lipgloss.Style
}

func NewStyles(p Palette) Styles {
	s := lipgloss.NewStyle
	return Styles{
		P:          p,
		Text:       s().Foreground(p.Text),
		Subtext:    s().Foreground(p.Subtext),
		Faint:      s().Foreground(p.Overlay),
		Bold:       s().Foreground(p.Text).Bold(true),
		Accent:     s().Foreground(p.Accent),
		AccentBold: s().Foreground(p.Accent).Bold(true),
		Red:        s().Foreground(p.Red),
		Green:      s().Foreground(p.Green),
		Yellow:     s().Foreground(p.Yellow),
		Blue:       s().Foreground(p.Blue),
		Peach:      s().Foreground(p.Peach),
		Header:     s().Foreground(p.Subtext).Bold(true),
		Rule:       s().Foreground(p.Surface1),
		Key:        s().Foreground(p.Accent).Bold(true),
		Chip:       s().Foreground(p.Text).Background(p.Surface0).Padding(0, 1),
		ChipOn:     s().Foreground(p.OnAccent).Background(p.Accent).Padding(0, 1),
		Button:     s().Foreground(p.Text).Background(p.Surface1).Padding(0, 1),
		ButtonDeny: s().Foreground(p.Red).Background(p.Surface0).Padding(0, 1),
		ButtonOn:   s().Foreground(p.OnAccent).Background(p.Accent).Bold(true).Padding(0, 1),
		Code:       s().Foreground(p.Text).Background(p.Surface0),
		Add:        s().Foreground(p.Green),
		Del:        s().Foreground(p.Red),
		Match:      s().Foreground(p.Text).Background(blend(p.Base, p.Yellow, 0.35)).Bold(true),
		Panel:      s().Border(lipgloss.RoundedBorder()).BorderForeground(p.Accent).Padding(0, 1),
		Dialog:     s().Border(lipgloss.RoundedBorder()).BorderForeground(p.Accent).Padding(0, 1),
	}
}

func ptr[T any](v T) *T { return &v }

// blend is fg over bg at opacity t: a colour the apps draw translucent,
// which a terminal can't, on the background.
func blend(bg, fg color.Color, t float64) color.Color {
	br, bgr, bb, _ := bg.RGBA()
	fr, fgr, fb, _ := fg.RGBA()
	mix := func(x, y uint32) uint8 {
		return uint8(float64(x>>8)*(1-t) + float64(y>>8)*t + 0.5)
	}
	return color.RGBA{R: mix(br, fr), G: mix(bgr, fgr), B: mix(bb, fb), A: 255}
}

var chromaMu sync.Mutex

// chromaTheme registers code's colours in the palette, as a Chroma style
// of their own for each theme, and returns its name. Glamour would
// register the colours it's given under one name, once for the process,
// so the first theme's would stay.
func chromaTheme(p Palette) string {
	name := "ra-latte"
	if p.Dark {
		name = "ra-frappe"
	}
	chromaMu.Lock()
	defer chromaMu.Unlock()
	if _, ok := styles.Registry[name]; ok {
		return name
	}
	styles.Register(chroma.MustNewStyle(name, chroma.StyleEntries{
		chroma.Text:                p.hexText,
		chroma.Comment:             "italic " + p.hexOverlay,
		chroma.Keyword:             p.hexMauve,
		chroma.KeywordType:         p.hexYellow,
		chroma.Operator:            p.hexTeal,
		chroma.Punctuation:         p.hexSubtext,
		chroma.Name:                p.hexText,
		chroma.NameBuiltin:         p.hexRed,
		chroma.NameFunction:        p.hexBlue,
		chroma.NameClass:           p.hexYellow,
		chroma.NameConstant:        p.hexPeach,
		chroma.NameTag:             p.hexMauve,
		chroma.NameAttribute:       p.hexYellow,
		chroma.LiteralNumber:       p.hexPeach,
		chroma.LiteralString:       p.hexGreen,
		chroma.LiteralStringEscape: p.hexPink,
		chroma.GenericDeleted:      p.hexRed,
		chroma.GenericInserted:     p.hexGreen,
		chroma.GenericEmph:         "italic",
		chroma.GenericStrong:       "bold",
		chroma.GenericSubheading:   p.hexOverlay,
	}))
	return name
}

// markdownStyle is Glamour's style for agents' Markdown, in the palette.
func markdownStyle(p Palette) ansi.StyleConfig {
	text := ptr(p.hexText)
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: text}},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: ptr(p.hexSubtext), Italic: ptr(true)},
			Indent:         ptr(uint(1)),
			IndentToken:    ptr("│ "),
		},
		Paragraph:      ansi.StyleBlock{},
		List:           ansi.StyleList{LevelIndent: 2},
		Heading:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: ptr(p.hexMauve), Bold: ptr(true), BlockSuffix: "\n"}},
		H1:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "# "}},
		H2:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "## "}},
		H3:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "### "}},
		H4:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "#### "}},
		H5:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "##### "}},
		H6:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "###### "}},
		Strikethrough:  ansi.StylePrimitive{CrossedOut: ptr(true)},
		Emph:           ansi.StylePrimitive{Italic: ptr(true)},
		Strong:         ansi.StylePrimitive{Bold: ptr(true)},
		HorizontalRule: ansi.StylePrimitive{Color: ptr(p.hexOverlay), Format: "\n────────\n"},
		Item:           ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration:    ansi.StylePrimitive{BlockPrefix: ". "},
		Task:           ansi.StyleTask{Ticked: "☑ ", Unticked: "☐ "},
		Link:           ansi.StylePrimitive{Color: ptr(p.hexBlue), Underline: ptr(true)},
		LinkText:       ansi.StylePrimitive{Color: ptr(p.hexBlue)},
		Image:          ansi.StylePrimitive{Color: ptr(p.hexBlue)},
		ImageText:      ansi.StylePrimitive{Color: ptr(p.hexSubtext), Format: "▣ {{.text}}"},
		Code:           ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: ptr(p.hexPeach), BackgroundColor: ptr(p.hexSurface0), Prefix: " ", Suffix: " "}},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: text}, Margin: ptr(uint(1))},
			Theme:      chromaTheme(p),
		},
		Table: ansi.StyleTable{
			StyleBlock:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{}},
			CenterSeparator: ptr("┼"),
			ColumnSeparator: ptr("│"),
			RowSeparator:    ptr("─"),
		},
		DefinitionDescription: ansi.StylePrimitive{BlockPrefix: "\n  "},
		HTMLBlock:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: text}},
		HTMLSpan:              ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: text}},
	}
}
