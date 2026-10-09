package pi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"remote-agent/internal/images"
)

func TestToolResultImages(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 6, 2)))
	raw := `{"content":[{"type":"text","text":"Read image file [image/png]"},{"type":"image","data":"` +
		base64.StdEncoding.EncodeToString(b.Bytes()) + `","mimeType":"image/png"}]}`
	var res toolResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	r := &runtime{images: images.New(t.TempDir())}
	refs := r.keepImages(&res)
	if len(refs) != 1 || refs[0].Width != 6 || res.text() != "Read image file [image/png]" {
		t.Errorf("images = %+v, text %q", refs, res.text())
	}
	r.images = nil
	if refs := r.keepImages(&res); refs != nil {
		t.Errorf("kept images without a store: %+v", refs)
	}
}
