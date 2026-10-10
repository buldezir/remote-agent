package orchestrator

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"remote-agent/internal/model"
)

// instructions go into every agent's system prompt (harness.OpenOptions).
// Agents otherwise take the user to be at this machine: they open images in
// Preview, or read them to describe them, which costs context. A Markdown
// image in a reply costs nothing; linkImages hands it to the apps.
const instructions = `You are running under Remote Agent. The user follows this session in the Remote Agent app, often on a phone or another computer, and can't see this machine's screen. Opening a file or an app here (open, Preview, a browser) doesn't show it to them.

To show the user an image, such as a screenshot or a chart, save it as a PNG or JPEG file and put it in your reply as a Markdown image with the file's absolute path, on a line of its own:

![Screenshot](/tmp/screenshot.png)

The app shows the image to the user. You don't need to open or read an image to show it; read it only when the task needs you to see it yourself.`

// ImageScheme is the scheme of the links to rad's images in agents' replies:
// rad-image:<id>, the id of GET /v1/images/<id>.
const ImageScheme = "rad-image"

// linkedImage is an image file a reply links to, as rad stored it.
type linkedImage struct {
	size int64
	mod  time.Time
	ref  model.ImageRef
}

// The destination is a path, or anything in angle brackets; a title may follow.
var markdownImage = regexp.MustCompile(`!\[[^\]\n]*\]\([ \t]*(<[^>\n]+>|[^\s)]+)(?:[ \t]+"[^"\n]*")?[ \t]*\)`)

// linkImages stores the image files an agent's reply shows as Markdown
// images, such as a screenshot it took, and points the links at rad's
// copies. It returns the new text and the images, in order. A link that
// isn't to a readable PNG, JPEG, GIF or WebP file, and any in code, stays.
//
// It runs on every update of a streaming reply, so it keeps the files it
// stored, and stores one again only when it changed (an agent may overwrite
// /tmp/screenshot.png for the next one).
func (a *actor) linkImages(text string) (string, []model.ImageRef) {
	if a.o.opt.Images == nil || !strings.Contains(text, "![") {
		return text, nil
	}
	code := codeRanges(text)
	var b strings.Builder
	var refs []model.ImageRef
	last := 0
	for _, m := range markdownImage.FindAllStringSubmatchIndex(text, -1) {
		if inRanges(code, m[0]) {
			continue
		}
		ref, ok := a.linkedImage(text[m[2]:m[3]])
		if !ok {
			continue
		}
		b.WriteString(text[last:m[2]])
		b.WriteString(ImageScheme + ":" + ref.ID)
		last = m[3]
		if !containsRef(refs, ref.ID) {
			refs = append(refs, ref)
		}
	}
	if refs == nil {
		return text, nil
	}
	b.WriteString(text[last:])
	return b.String(), refs
}

// linkedImage stores the image file a Markdown image's destination names.
func (a *actor) linkedImage(dest string) (model.ImageRef, bool) {
	path, ok := localPath(dest, a.sess.Workspace.Path)
	if !ok {
		return model.ImageRef{}, false
	}
	fi, err := os.Stat(path)
	if err != nil && strings.Contains(path, "%") {
		if p, uerr := url.PathUnescape(path); uerr == nil {
			path = p
			fi, err = os.Stat(path)
		}
	}
	if err != nil || !fi.Mode().IsRegular() {
		return model.ImageRef{}, false
	}
	if l, ok := a.linked[path]; ok && l.size == fi.Size() && l.mod.Equal(fi.ModTime()) {
		return l.ref, true
	}
	ref, err := a.o.opt.Images.PutFile(path)
	if err != nil {
		a.o.log.Info("not showing a linked image", "session", a.sess.ID, "path", path, "err", err)
		return model.ImageRef{}, false
	}
	if a.linked == nil {
		a.linked = map[string]linkedImage{}
	}
	a.linked[path] = linkedImage{fi.Size(), fi.ModTime(), ref}
	return ref, true
}

// localPath is the file a link destination names: an absolute path, one
// from the home directory (~/) or the working directory, or a file: URL.
func localPath(dest, cwd string) (string, bool) {
	dest = strings.TrimSuffix(strings.TrimPrefix(dest, "<"), ">")
	if rest, ok := strings.CutPrefix(dest, "file://"); ok {
		p, err := url.PathUnescape(rest)
		if err != nil || !strings.HasPrefix(p, "/") {
			return "", false
		}
		return filepath.Clean(p), true
	}
	if i := strings.Index(dest, ":"); i > 0 && !strings.ContainsAny(dest[:i], "/.~") {
		return "", false // another scheme: https:, data:, rad-image:
	}
	switch {
	case strings.HasPrefix(dest, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, dest[2:]), true
	case filepath.IsAbs(dest):
		return filepath.Clean(dest), true
	case cwd != "":
		return filepath.Join(cwd, dest), true
	}
	return "", false
}

// codeRanges are the byte ranges of a Markdown text's fenced code blocks and
// code spans, where an image's syntax is only text.
func codeRanges(text string) [][2]int {
	var out [][2]int
	var fence string // the opening fence while in a block
	start, prose := 0, 0
	for pos := 0; pos < len(text); {
		end := strings.IndexByte(text[pos:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += pos + 1
		}
		line := strings.TrimLeft(text[pos:end], " ")
		indent := end - pos - len(line)
		switch {
		case fence == "" && indent < 4 && (strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")):
			out = append(out, codeSpans(text, prose, pos)...)
			n := len(line) - len(strings.TrimLeft(line, line[:1]))
			fence, start = line[:n], pos
		case fence != "" && indent < 4 && strings.HasPrefix(line, fence) &&
			strings.TrimSpace(strings.TrimLeft(line, fence[:1])) == "":
			out = append(out, [2]int{start, end})
			fence, prose = "", end
		}
		pos = end
	}
	if fence != "" {
		return append(out, [2]int{start, len(text)}) // unclosed, as while streaming
	}
	return append(out, codeSpans(text, prose, len(text))...)
}

// codeSpans are the code spans in text[from:to]: a run of backticks up to the
// next run of the same length. A run with none is only backticks.
func codeSpans(text string, from, to int) [][2]int {
	var runs [][2]int
	for i := from; i < to; i++ {
		if text[i] == '`' {
			j := i
			for j < to && text[j] == '`' {
				j++
			}
			runs = append(runs, [2]int{i, j})
			i = j - 1
		}
	}
	var out [][2]int
	for i := 0; i < len(runs); i++ {
		n := runs[i][1] - runs[i][0]
		for k := i + 1; k < len(runs); k++ {
			if runs[k][1]-runs[k][0] == n {
				out = append(out, [2]int{runs[i][0], runs[k][1]})
				i = k
				break
			}
		}
	}
	return out
}

func inRanges(ranges [][2]int, pos int) bool {
	for _, r := range ranges {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}

func containsRef(refs []model.ImageRef, id string) bool {
	for _, r := range refs {
		if r.ID == id {
			return true
		}
	}
	return false
}
