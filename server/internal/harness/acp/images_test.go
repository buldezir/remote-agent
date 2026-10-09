package acp

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-agent/internal/harness"
	"remote-agent/internal/images"
	"remote-agent/internal/model"
)

func TestImages(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 7, 3)))
	path := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(path, b.Bytes(), 0o644)
	in := harness.Input{Text: "look", Images: []harness.Image{{Path: path, MimeType: "image/png"}}}

	r := &runtime{images: images.New(t.TempDir()), tools: map[string]*model.Item{}}
	// An agent without image prompts gets the paths in the text.
	blocks, err := r.promptBlocks(in)
	if err != nil || len(blocks) != 1 || !strings.HasSuffix(blocks[0]["text"].(string), "look\n\nAttached images (open them from disk):\n- "+path) {
		t.Errorf("blocks = %v, %v", blocks, err)
	}
	r.imagePrompts = true
	blocks, err = r.promptBlocks(in)
	if err != nil || len(blocks) != 2 || blocks[0]["type"] != "image" || blocks[0]["mimeType"] != "image/png" || blocks[1]["text"] != "look" {
		t.Errorf("blocks = %v, %v", blocks, err)
	}

	data := base64.StdEncoding.EncodeToString(b.Bytes())
	it := r.toolItem(update{ToolCallID: "t1", Content: []byte(`[{"type":"content","content":{"type":"image","data":"` + data + `","mimeType":"image/png"}},
		{"type":"content","content":{"type":"text","text":"Screenshot taken"}}]`)})
	if it.Tool.Output != "Screenshot taken" || len(it.Images) != 1 || it.Images[0].Width != 7 {
		t.Errorf("tool item = %+v, %+v", it, it.Tool)
	}
}
