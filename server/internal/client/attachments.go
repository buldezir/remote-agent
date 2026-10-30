package client

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// The long side of an image sent with a prompt.
const maxPixels = 2048

// Under 5 MB once base64-encoded (Claude's limit).
const maxImageBytes = 3_500_000

// ErrNotImage: the file isn't a PNG, JPEG, GIF or WebP image.
var ErrNotImage = errors.New("not an image ra can read (PNG, JPEG, GIF or WebP)")

// ErrImageTooLarge: the image doesn't fit the agents' limit even as a JPEG.
var ErrImageTooLarge = errors.New("the image is too large to send")

// PrepareImage readies an image for agents, whose APIs reject large ones:
// it scales it down to 2048 pixels on the long side and re-encodes it
// without metadata (no location). PNG when it was a PNG or has
// transparency, so screenshots stay sharp, else JPEG. As
// apple/Shared/ImageCoding.swift and web/src/store/attachments.ts do.
func PrepareImage(data []byte) ([]byte, error) {
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	img := scaled(src)
	if format == "png" || !opaque(img) {
		var b bytes.Buffer
		if png.Encode(&b, img) == nil && b.Len() <= maxImageBytes {
			return b.Bytes(), nil
		}
	}
	// JPEG has no transparency: put the image on white.
	flat := image.NewRGBA(img.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)
	for _, q := range []int{85, 60} {
		var b bytes.Buffer
		if jpeg.Encode(&b, flat, &jpeg.Options{Quality: q}) == nil && b.Len() <= maxImageBytes {
			return b.Bytes(), nil
		}
	}
	return nil, ErrImageTooLarge
}

func scaled(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := max(w, h)
	if long <= maxPixels {
		return src
	}
	scale := float64(maxPixels) / float64(long)
	nw, nh := max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}

func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}
