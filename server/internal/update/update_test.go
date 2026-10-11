package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"0.1.3": true, "v0.1.3": true, "1.20.300": true, "0.2.0-rc.1": true,
		"dev": false, "0.1": false, "": false, "0.1.2-6-gd609b51": false, "v0.1.2-6-gd609b51": false,
		"0.1.3 ": false, "0.1.x": false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.3", "0.1.3", 0},
		{"v0.1.3", "0.1.3", 0},
		{"0.1.4", "0.1.3", 1},
		{"0.1.10", "0.1.9", 1},
		{"0.2.0", "0.1.99", 1},
		{"1.0.0", "0.9.9", 1},
		{"0.2.0-rc.1", "0.2.0", -1},
		{"0.2.0-rc.1", "0.1.9", 1},
		{"0.2.0-rc.10", "0.2.0-rc.9", 1},
		{"0.2.0-rc.1", "0.2.0-rc.1.1", -1},
		{"0.2.0-1", "0.2.0-beta", -1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

// fakeGitHub serves one release, v0.2.0, with a tarball for linux/amd64.
type fakeGitHub struct {
	*httptest.Server
	status  int
	archive []byte
	sums    string
	agent   string
}

func newFakeGitHub(t *testing.T, rad string) *fakeGitHub {
	g := &fakeGitHub{status: http.StatusOK, archive: tarball(t, map[string]string{"rad": rad})}
	g.sums = sumLine(g.archive, "rad-linux-amd64.tar.gz") + sumLine([]byte("x"), "rad-darwin-arm64.tar.gz")
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			g.agent = r.Header.Get("User-Agent")
			w.WriteHeader(g.status)
			if g.status != http.StatusOK {
				json.NewEncoder(w).Encode(map[string]string{"message": "API rate limit exceeded"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.0",
				"html_url": "https://github.com/o/r/releases/tag/v0.2.0",
				"assets": []map[string]string{
					{"name": "rad-linux-amd64.tar.gz", "browser_download_url": g.URL + "/dl/rad-linux-amd64.tar.gz"},
					{"name": "SHA256SUMS", "browser_download_url": g.URL + "/dl/SHA256SUMS"},
				},
			})
		case "/dl/rad-linux-amd64.tar.gz":
			w.Write(g.archive)
		case "/dl/SHA256SUMS":
			w.Write([]byte(g.sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitHub) client() *Client {
	return &Client{API: g.URL, Repo: "o/r", HTTP: g.Client(), UserAgent: "rad/0.1.0", GOOS: "linux", GOARCH: "amd64"}
}

func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sumLine(b []byte, name string) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// script is a stand-in rad that reports version.
func script(version string) string { return "#!/bin/sh\necho rad " + version + "\n" }

func TestLatest(t *testing.T) {
	g := newFakeGitHub(t, script("0.2.0"))
	rel, err := g.client().Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.2.0" || rel.URL != "https://github.com/o/r/releases/tag/v0.2.0" || len(rel.assets) != 2 {
		t.Errorf("release = %+v", rel)
	}
	if g.agent != "rad/0.1.0" {
		t.Errorf("User-Agent = %q", g.agent)
	}

	g.status = http.StatusForbidden
	if _, err := g.client().Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("forbidden: err = %v", err)
	}
	g.status = http.StatusNotFound
	if _, err := g.client().Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no published releases") {
		t.Errorf("not found: err = %v", err)
	}
}

func TestInstall(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(g *fakeGitHub, c *Client)
		err   string
	}{
		{name: "ok"},
		{name: "checksum", err: "doesn't match", setup: func(g *fakeGitHub, c *Client) {
			g.sums = strings.Replace(g.sums, g.sums[:8], "00000000", 1)
		}},
		{name: "no sum", err: "no line for", setup: func(g *fakeGitHub, c *Client) { g.sums = "" }},
		{name: "no asset", err: "has no rad-linux-mips.tar.gz", setup: func(g *fakeGitHub, c *Client) { c.GOARCH = "mips" }},
		{name: "no rad", err: "no rad in the archive", setup: func(g *fakeGitHub, c *Client) {
			g.archive = tarball(t, map[string]string{"README": "hi"})
			g.sums = sumLine(g.archive, "rad-linux-amd64.tar.gz")
		}},
		{name: "wrong version", err: `says "rad 0.1.9"`, setup: func(g *fakeGitHub, c *Client) {
			g.archive = tarball(t, map[string]string{"rad": script("0.1.9")})
			g.sums = sumLine(g.archive, "rad-linux-amd64.tar.gz")
		}},
		{name: "doesn't run", err: "doesn't run", setup: func(g *fakeGitHub, c *Client) {
			g.archive = tarball(t, map[string]string{"rad": "#!/bin/sh\nexit 3\n"})
			g.sums = sumLine(g.archive, "rad-linux-amd64.tar.gz")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newFakeGitHub(t, script("0.2.0"))
			client := g.client()
			if c.setup != nil {
				c.setup(g, client)
			}
			dir := t.TempDir()
			dst := filepath.Join(dir, "rad")
			os.WriteFile(dst, []byte("old"), 0o755)
			rel, err := client.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = client.Install(context.Background(), rel, dst)
			got, _ := os.ReadFile(dst)
			if c.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				if fi, _ := os.Stat(dst); string(got) != script("0.2.0") || fi.Mode().Perm() != 0o755 {
					t.Errorf("dst = %q, mode %v", got, fi.Mode())
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				if string(got) != "old" {
					t.Errorf("dst was replaced: %q", got)
				}
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("left behind: %v", entries)
			}
		})
	}
}
