package claude

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"remote-agent/internal/harness"
	"remote-agent/internal/images"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 3, 2)))
	return b.Bytes()
}

func TestToolResultImages(t *testing.T) {
	data := base64.StdEncoding.EncodeToString(testPNG(t))
	r := &runtime{images: images.New(t.TempDir())}
	text, refs := r.result([]byte(`[{"type":"text","text":"Took a screenshot"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]`))
	if text != "Took a screenshot" || len(refs) != 1 || refs[0].Width != 3 {
		t.Errorf("result = %q, %+v", text, refs)
	}

	// Without a store the image is a placeholder in the text.
	r.images = nil
	text, refs = r.result([]byte(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]`))
	if text != "[image]" || refs != nil {
		t.Errorf("result without a store = %q, %+v", text, refs)
	}
}

func TestPromptContent(t *testing.T) {
	if c, _ := promptContent(harness.Input{Text: "hi"}); c != "hi" {
		t.Errorf("text-only content = %#v", c)
	}
	path := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(path, testPNG(t), 0o644)
	c, err := promptContent(harness.Input{Text: "what's this?", Images: []harness.Image{{Path: path, MimeType: "image/png"}}})
	blocks, _ := c.([]map[string]any)
	if err != nil || len(blocks) != 2 || blocks[0]["type"] != "image" || blocks[1]["text"] != "what's this?" {
		t.Fatalf("content = %#v, %v", c, err)
	}
	src := blocks[0]["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != base64.StdEncoding.EncodeToString(testPNG(t)) {
		t.Errorf("image source = %v", src)
	}
	if _, err := promptContent(harness.Input{Images: []harness.Image{{Path: path + ".missing"}}}); err == nil {
		t.Error("missing image file accepted")
	}
}
