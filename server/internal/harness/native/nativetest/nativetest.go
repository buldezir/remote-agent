// Package nativetest helps the importers' tests.
package nativetest

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CopyTree copies the folder src to a new temporary folder for a test,
// giving each file the age that ages names by its base name (others are a
// day old), as listings go by when a file was written.
func CopyTree(t testing.TB, src string, ages map[string]time.Duration) string {
	t.Helper()
	dst := t.TempDir()
	now := time.Now()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		age, ok := ages[d.Name()]
		if !ok {
			age = 24 * time.Hour
		}
		return os.Chtimes(target, now.Add(-age), now.Add(-age))
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
