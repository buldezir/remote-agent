package orchestrator

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/images"
	"remote-agent/internal/model"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLinkImages(t *testing.T) {
	t.Parallel()
	cwd, tmp := t.TempDir(), t.TempDir()
	imgs := images.New(t.TempDir())
	a := &actor{
		o:    &Orchestrator{opt: Options{Images: imgs}, log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sess: &model.Session{ID: "s", Workspace: model.Workspace{Path: cwd}},
	}
	abs := filepath.Join(tmp, "shot.png")
	writePNG(t, abs, 4, 3)
	writePNG(t, filepath.Join(cwd, "rel.png"), 5, 3)
	writePNG(t, filepath.Join(tmp, "my shot.png"), 6, 3)
	os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("not an image"), 0o644)

	text := strings.Join([]string{
		"Here it is:",
		"![Screenshot](" + abs + ")",
		"![Again](" + abs + ` "same file")`,
		"![Relative](rel.png) and ![Spaces](<" + filepath.Join(tmp, "my shot.png") + ">)",
		"![Encoded](file://" + strings.ReplaceAll(filepath.Join(tmp, "my shot.png"), " ", "%20") + ")",
		"![Missing](/nonexistent/x.png) ![Text](" + filepath.Join(tmp, "notes.txt") + ") ![Web](https://example.com/a.png)",
		"Inline code: `![Code](" + abs + ")`",
		"```",
		"![Fenced](" + abs + ")",
		"```",
	}, "\n")
	got, refs := a.linkImages(text)

	if len(refs) != 3 || refs[0].Width != 4 || refs[1].Width != 5 || refs[2].Width != 6 {
		t.Fatalf("refs = %+v", refs)
	}
	for _, want := range []string{
		"![Screenshot](rad-image:" + refs[0].ID + ")",
		"![Again](rad-image:" + refs[0].ID + ` "same file")`,
		"![Relative](rad-image:" + refs[1].ID + ") and ![Spaces](rad-image:" + refs[2].ID + ")",
		"![Encoded](rad-image:" + refs[2].ID + ")",
		"![Missing](/nonexistent/x.png) ![Text](" + filepath.Join(tmp, "notes.txt") + ") ![Web](https://example.com/a.png)",
		"`![Code](" + abs + ")`",
		"```\n![Fenced](" + abs + ")\n```",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
	for _, r := range refs {
		if _, _, err := imgs.Get(r.ID); err != nil {
			t.Errorf("%s not stored: %v", r.ID, err)
		}
	}

	// A file overwritten with the next screenshot is stored again.
	writePNG(t, abs, 7, 3)
	future := time.Now().Add(time.Minute)
	os.Chtimes(abs, future, future)
	if _, refs := a.linkImages("![Next](" + abs + ")"); len(refs) != 1 || refs[0].Width != 7 {
		t.Errorf("after overwriting: refs = %+v", refs)
	}

	if got, refs := a.linkImages("No images, but `![` and ![]()"); refs != nil || got != "No images, but `![` and ![]()" {
		t.Errorf("text without images = %q, %+v", got, refs)
	}
}

func TestCodeRanges(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ text, want string }{
		{"a `b` c", "`b`"},
		{"a ``b ` c`` d `e`", "``b ` c``|`e`"},
		{"a `` b `c` d", "`c`"},
		{"x\n```go\ncode `y`\n```\nz `w`", "```go\ncode `y`\n```\n|`w`"},
		{"x\n~~~~\n~~~\n~~~~\nz", "~~~~\n~~~\n~~~~\n"},
		{"open\n```\nstreaming", "```\nstreaming"},
	} {
		var got []string
		for _, r := range codeRanges(c.text) {
			got = append(got, c.text[r[0]:r[1]])
		}
		if strings.Join(got, "|") != c.want {
			t.Errorf("codeRanges(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
