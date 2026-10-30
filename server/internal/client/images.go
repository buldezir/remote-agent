package client

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ImageCache keeps rad's images on disk, by id. An id names its content,
// so a file never goes stale. Loads of the same id are shared.
type ImageCache struct {
	dir     string
	mu      sync.Mutex
	loading map[string]*imageLoad
}

type imageLoad struct {
	done chan struct{}
	data []byte
	err  error
}

func NewImageCache(dir string) *ImageCache {
	return &ImageCache{dir: dir, loading: map[string]*imageLoad{}}
}

// Path is where the image's file is once cached.
func (c *ImageCache) Path(id string) string {
	return filepath.Join(c.dir, safeID(id))
}

// safeID keeps an id to a file name, whatever rad sent.
func safeID(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(id)
}

// Load returns the image's bytes, from the disk or, once for any number of
// callers, from load.
func (c *ImageCache) Load(id string, load func() ([]byte, error)) ([]byte, error) {
	if data, err := os.ReadFile(c.Path(id)); err == nil {
		return data, nil
	}
	c.mu.Lock()
	l := c.loading[id]
	if l == nil {
		l = &imageLoad{done: make(chan struct{})}
		c.loading[id] = l
		go func() {
			l.data, l.err = load()
			if l.err == nil {
				c.Store(id, l.data)
			}
			c.mu.Lock()
			delete(c.loading, id)
			c.mu.Unlock()
			close(l.done)
		}()
	}
	c.mu.Unlock()
	<-l.done
	return l.data, l.err
}

// Store keeps bytes just uploaded, so showing them doesn't download them again.
func (c *ImageCache) Store(id string, data []byte) {
	if _, err := os.Stat(c.Path(id)); err == nil {
		return
	}
	writeFileAtomic(c.Path(id), data, 0o600)
}
