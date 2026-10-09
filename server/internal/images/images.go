// Package images keeps the images in transcripts: the ones users attach to
// prompts and the ones agents return from tools (screenshots, say). Each is a
// file named by the SHA-256 of its bytes, so storing one twice is a no-op.
package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"remote-agent/internal/model"
)

// MaxSize is the largest image rad keeps.
const MaxSize = 20 << 20

var (
	ErrNotFound    = errors.New("image not found")
	ErrUnsupported = errors.New("not a PNG, JPEG, GIF or WebP image")
	ErrTooLarge    = fmt.Errorf("image is larger than %d MB", MaxSize>>20)
)

var extensions = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}

var validID = regexp.MustCompile(`^[0-9a-f]{64}\.(png|jpg|gif|webp)$`)

type Store struct{ dir string }

func New(dir string) *Store { return &Store{dir: dir} }

// Put stores data and returns its reference. The type comes from the bytes,
// not from what the sender claimed.
func (s *Store) Put(data []byte) (model.ImageRef, error) {
	if len(data) > MaxSize {
		return model.ImageRef{}, ErrTooLarge
	}
	mime := http.DetectContentType(data)
	ext, ok := extensions[mime]
	if !ok {
		return model.ImageRef{}, ErrUnsupported
	}
	sum := sha256.Sum256(data)
	ref := describe(hex.EncodeToString(sum[:])+"."+ext, mime, data)
	path := filepath.Join(s.dir, ref.ID)
	if _, err := os.Stat(path); err == nil {
		return ref, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return model.ImageRef{}, err
	}
	tmp, err := os.CreateTemp(s.dir, ".put-*")
	if err != nil {
		return model.ImageRef{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return model.ImageRef{}, err
	}
	if err := tmp.Close(); err != nil {
		return model.ImageRef{}, err
	}
	return ref, os.Rename(tmp.Name(), path)
}

// PutBase64 stores base64 image data, as agents send it, with or without a
// data: URL prefix.
func (s *Store) PutBase64(data string) (model.ImageRef, error) {
	if rest, ok := strings.CutPrefix(data, "data:"); ok {
		_, payload, found := strings.Cut(rest, ";base64,")
		if !found {
			return model.ImageRef{}, ErrUnsupported
		}
		data = payload
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return model.ImageRef{}, fmt.Errorf("image data: %w", err)
	}
	return s.Put(b)
}

// PutFile stores a copy of the image at path, e.g. one an agent viewed.
func (s *Store) PutFile(path string) (model.ImageRef, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return model.ImageRef{}, err
	}
	if fi.Size() > MaxSize {
		return model.ImageRef{}, ErrTooLarge
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return model.ImageRef{}, err
	}
	return s.Put(b)
}

// Get returns the reference for a stored image and the path of its file.
func (s *Store) Get(id string) (model.ImageRef, string, error) {
	if !validID.MatchString(id) {
		return model.ImageRef{}, "", ErrNotFound
	}
	path := filepath.Join(s.dir, id)
	f, err := os.Open(path)
	if err != nil {
		return model.ImageRef{}, "", ErrNotFound
	}
	defer f.Close()
	head := make([]byte, 64<<10) // enough for the dimensions
	n, _ := f.Read(head)
	ref := describe(id, "", head[:n])
	if fi, err := f.Stat(); err == nil {
		ref.Size = fi.Size()
	}
	return ref, path, nil
}

// describe builds the reference for an image's bytes (or their start).
func describe(id, mime string, data []byte) model.ImageRef {
	if mime == "" {
		for m, ext := range extensions {
			if strings.HasSuffix(id, "."+ext) {
				mime = m
			}
		}
	}
	ref := model.ImageRef{ID: id, MimeType: mime, Size: int64(len(data))}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		ref.Width, ref.Height = cfg.Width, cfg.Height
	}
	return ref
}
