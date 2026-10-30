package client

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// SavedServer is the rad ra is paired with.
type SavedServer struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	URLs     []string  `json:"urls"` // the one that worked last first
	DeviceID string    `json:"deviceId"`
	AddedAt  time.Time `json:"addedAt"`
}

// ServerStore keeps the server ra is paired with in a folder: server.json,
// and the token in a file only its owner reads. As the apps' ServerStore,
// for the one server on this machine.
type ServerStore struct {
	dir string
	mu  sync.Mutex
}

func NewServerStore(dir string) *ServerStore {
	return &ServerStore{dir: dir}
}

func (s *ServerStore) Dir() string { return s.dir }

func (s *ServerStore) serverPath() string { return filepath.Join(s.dir, "server.json") }
func (s *ServerStore) tokenPath() string  { return filepath.Join(s.dir, "token") }

// Drafts are the prompts written and not sent, by session.
func (s *ServerStore) Drafts() *Drafts {
	return &Drafts{path: filepath.Join(s.dir, "drafts.json")}
}

// Load returns the saved server and its token; ok is false before pairing.
func (s *ServerStore) Load() (srv SavedServer, token string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.serverPath())
	if err != nil || json.Unmarshal(b, &srv) != nil || srv.ID == "" || len(srv.URLs) == 0 {
		return SavedServer{}, "", false
	}
	t, err := os.ReadFile(s.tokenPath())
	if err != nil {
		return SavedServer{}, "", false
	}
	token = strings.TrimSpace(string(t))
	return srv, token, token != ""
}

// Save keeps a newly paired server and its token, in place of the last.
func (s *ServerStore) Save(srv SavedServer, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(s.tokenPath(), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return s.saveServer(srv)
}

func (s *ServerStore) saveServer(srv SavedServer) error {
	b, err := json.MarshalIndent(srv, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.serverPath(), append(b, '\n'), 0o600)
}

// Promote moves the URL that worked to the front, to try it first next time.
func (s *ServerStore) Promote(url string) {
	srv, _, ok := s.Load()
	if !ok || (len(srv.URLs) > 0 && srv.URLs[0] == url) || !slices.Contains(srv.URLs, url) {
		return
	}
	srv.URLs = append([]string{url}, slices.DeleteFunc(srv.URLs, func(u string) bool { return u == url })...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveServer(srv)
}

// Forget removes the server, its token and the drafts written for it.
func (s *ServerStore) Forget() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, p := range []string{s.serverPath(), s.tokenPath(), filepath.Join(s.dir, "drafts.json")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// writeFileAtomic writes through a temporary file, so a reader never sees
// half a file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), perm)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
