package codex

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
	"remote-agent/internal/model"
)

func TestImageItems(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 5, 4)))
	cwd := t.TempDir()
	path := filepath.Join(cwd, "shot.png")
	os.WriteFile(path, b.Bytes(), 0o644)
	data := base64.StdEncoding.EncodeToString(b.Bytes())
	r := &runtime{cwd: cwd, images: images.New(t.TempDir()), items: map[string]*model.Item{}, events: make(chan harness.Event, 8)}
	next := func() model.Item {
		t.Helper()
		select {
		case ev := <-r.events:
			return ev.(harness.ItemEvent).Item
		default:
			t.Fatal("no item emitted")
		}
		return model.Item{}
	}

	r.onItem(threadItem{Type: "imageView", ID: "v1", Path: path}, true)
	it := next()
	if it.Tool.Title != "View shot.png" || it.Tool.Kind != model.ToolRead || len(it.Images) != 1 || it.Images[0].Width != 5 {
		t.Errorf("imageView item = %+v %+v", it, it.Tool)
	}

	r.onItem(threadItem{Type: "mcpToolCall", ID: "m1", Server: "browser", Tool: "screenshot", Status: "completed",
		Result: []byte(`{"content":[{"type":"text","text":"Captured"},{"type":"image","data":"` + data + `","mimeType":"image/png"}]}`)}, true)
	it = next()
	if it.Tool.Output != "Captured" || len(it.Images) != 1 {
		t.Errorf("mcp item output %q, images %+v", it.Tool.Output, it.Images)
	}

	r.onItem(threadItem{Type: "mcpToolCall", ID: "m2", Server: "db", Tool: "query", Status: "completed",
		Result: []byte(`{"content":[],"structuredContent":{"rows":1}}`)}, true)
	if it = next(); it.Tool.Output != `{"content":[],"structuredContent":{"rows":1}}` {
		t.Errorf("structured-only result = %q", it.Tool.Output)
	}
}
