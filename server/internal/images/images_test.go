package images

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPutAndGet(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	data := pngBytes(t, 30, 20)
	ref, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(ref.ID, ".png") || len(ref.ID) != 68 || ref.MimeType != "image/png" ||
		ref.Width != 30 || ref.Height != 20 || ref.Size != int64(len(data)) {
		t.Errorf("ref = %+v", ref)
	}

	// The same bytes again (here as base64 in a data: URL) are the same image.
	again, err := s.PutBase64("data:image/png;base64," + base64.StdEncoding.EncodeToString(data))
	if err != nil || again != ref {
		t.Errorf("again = %+v, %v; want %+v", again, err, ref)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files = %d, want 1", len(entries))
	}

	got, path, err := s.Get(ref.ID)
	if err != nil || got != ref || path != filepath.Join(dir, ref.ID) {
		t.Errorf("Get = %+v, %q, %v", got, path, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, data) {
		t.Error("stored bytes differ")
	}
}

func TestRejects(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put([]byte("hello, not an image")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("text: err = %v", err)
	}
	if _, err := s.Put(make([]byte, MaxSize+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("large: err = %v", err)
	}
	if _, err := s.PutBase64("not base64!"); err == nil {
		t.Error("bad base64 accepted")
	}
	for _, id := range []string{"", "../rad.db", strings.Repeat("a", 64) + ".png", strings.Repeat("a", 64) + ".svg"} {
		if _, _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q): err = %v", id, err)
		}
	}
}

func TestPutFile(t *testing.T) {
	s := New(t.TempDir())
	path := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(path, pngBytes(t, 4, 4), 0o644)
	ref, err := s.PutFile(path)
	if err != nil || ref.Width != 4 {
		t.Errorf("PutFile = %+v, %v", ref, err)
	}
	if _, err := s.PutFile(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}
