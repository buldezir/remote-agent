// Package fsbrowse lists directories confined to a set of allowed roots.
package fsbrowse

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsGitRepo bool   `json:"isGitRepo"`
}

type Listing struct {
	Path    string  `json:"path"` // "" for the list of roots
	Parent  string  `json:"parent,omitempty"`
	Entries []Entry `json:"entries"`
}

var ErrOutsideRoots = errors.New("path is outside the configured roots")

type Browser struct{ roots []string }

func New(roots []string) *Browser {
	var rs []string
	for _, r := range roots {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			rs = append(rs, real)
		}
	}
	return &Browser{roots: rs}
}

func (b *Browser) Roots() []string { return b.roots }

// Resolve returns the symlink-resolved absolute path if it lies within a root.
func (b *Browser) Resolve(p string) (string, error) {
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	for _, r := range b.roots {
		if real == r || strings.HasPrefix(real, r+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", ErrOutsideRoots
}

func (b *Browser) rootOf(p string) string {
	for _, r := range b.roots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return r
		}
	}
	return ""
}

// List returns the subdirectories of path, or the roots when path is empty.
func (b *Browser) List(path string) (*Listing, error) {
	if path == "" {
		l := &Listing{Entries: []Entry{}}
		for _, r := range b.roots {
			l.Entries = append(l.Entries, Entry{Name: r, Path: r, IsGitRepo: isGitRepo(r)})
		}
		return l, nil
	}
	real, err := b.Resolve(path)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(real)
	if err != nil {
		return nil, err
	}
	l := &Listing{Path: real, Entries: []Entry{}}
	if real != b.rootOf(real) {
		l.Parent = filepath.Dir(real)
	}
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(real, name)
		if !de.IsDir() {
			if de.Type()&os.ModeSymlink == 0 {
				continue
			}
			if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
				continue
			}
		}
		l.Entries = append(l.Entries, Entry{Name: name, Path: full, IsGitRepo: isGitRepo(full)})
	}
	sort.Slice(l.Entries, func(i, j int) bool {
		return strings.ToLower(l.Entries[i].Name) < strings.ToLower(l.Entries[j].Name)
	})
	return l, nil
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
