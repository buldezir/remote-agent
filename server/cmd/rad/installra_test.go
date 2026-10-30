package main

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
	"runtime"
	"strings"
	"testing"

	"remote-agent/internal/update"
)

// raReleases serves one release, v0.2.0, as rad's own and the latest, with
// ra for this platform: a script that says its version.
func raReleases(t *testing.T) *update.Client {
	t.Helper()
	asset := "ra-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := raScript("0.2.0")
	tw.WriteHeader(&tar.Header{Name: "ra", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest", "/repos/o/r/releases/tags/v0.2.0":
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.0", "html_url": "https://example.com/v0.2.0",
				"assets": []map[string]string{
					{"name": asset, "browser_download_url": srv.URL + "/dl/ra"},
					{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/dl/sums"},
				},
			})
		case "/dl/ra":
			w.Write(archive)
		case "/dl/sums":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &update.Client{API: srv.URL, Repo: "o/r", HTTP: srv.Client(), UserAgent: "rad/test", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Name: "ra"}
}

func raScript(version string) string { return "#!/bin/sh\necho ra " + version + "\n" }

func installRaTest(t *testing.T, version, exe, dir string, force bool, ra string) (string, error) {
	t.Helper()
	if ra != "" {
		place := dir
		if place == "" {
			place = filepath.Dir(exe)
		}
		os.WriteFile(filepath.Join(place, "ra"), []byte(ra), 0o755)
	}
	var out bytes.Buffer
	err := installRaFor(context.Background(), raReleases(t), version, exe, dir, force, &out)
	return out.String(), err
}

func readRa(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestInstallRaNextToRad(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	out, err := installRaTest(t, "0.2.0", filepath.Join(bin, "rad"), "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := readRa(t, filepath.Join(bin, "ra")); got != raScript("0.2.0") {
		t.Errorf("ra is %q", got)
	}
	if !strings.Contains(out, "installed ra 0.2.0") || !strings.Contains(out, "run `ra`") {
		t.Error(out)
	}
}

func TestInstallRaElsewhere(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	dir := filepath.Join(t.TempDir(), "bin")
	app := "/Applications/Remote Agent Server.app/Contents/MacOS/rad"
	// Not into the app, which its signature covers.
	if _, err := installRaTest(t, "0.2.0", app, "", false, ""); err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Errorf("into the app: err = %v", err)
	}
	out, err := installRaTest(t, "0.2.0", app, dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := readRa(t, filepath.Join(dir, "ra")); got != raScript("0.2.0") || !strings.Contains(out, "isn't on your PATH") {
		t.Errorf("ra is %q, said %q", got, out)
	}
}

func TestInstallRaOverAnOlderOne(t *testing.T) {
	bin := t.TempDir()
	exe := filepath.Join(bin, "rad")
	out, err := installRaTest(t, "0.2.0", exe, "", false, raScript("0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if readRa(t, filepath.Join(bin, "ra")) != raScript("0.2.0") || readRa(t, filepath.Join(bin, "ra.old")) != raScript("0.1.0") {
		t.Errorf("ra and ra.old weren't replaced and kept: %s", out)
	}
}

func TestInstallRaLeavesWhatItShould(t *testing.T) {
	for _, c := range []struct {
		name, ra, said string
		fails          bool
	}{
		{"the same", raScript("0.2.0"), "already at", false},
		{"a newer one", raScript("0.3.0"), "is newer than", false},
		{"a development build", raScript("dev"), "development build of ra", true},
		{"another program", "#!/bin/sh\necho hello\n", "isn't ra", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := t.TempDir()
			out, err := installRaTest(t, "0.2.0", filepath.Join(bin, "rad"), "", false, c.ra)
			if c.fails != (err != nil) || !strings.Contains(out+errString(err), c.said) {
				t.Errorf("said %q, err = %v", out, err)
			}
			if got := readRa(t, filepath.Join(bin, "ra")); got != c.ra {
				t.Errorf("ra was replaced with %q", got)
			}
			// --force puts this rad's release there.
			if _, err := installRaTest(t, "0.2.0", filepath.Join(bin, "rad"), "", true, ""); err != nil {
				t.Fatal(err)
			}
			if got := readRa(t, filepath.Join(bin, "ra")); got != raScript("0.2.0") {
				t.Errorf("--force left %q", got)
			}
		})
	}
}

func TestInstallRaForADevelopmentBuild(t *testing.T) {
	bin := t.TempDir()
	out, err := installRaTest(t, "dev", filepath.Join(bin, "rad"), "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "latest release") || readRa(t, filepath.Join(bin, "ra")) != raScript("0.2.0") {
		t.Errorf("said %q", out)
	}
	// A release that isn't out, such as rad's own when it's ahead.
	if _, err := installRaTest(t, "0.9.0", filepath.Join(bin, "rad"), "", false, ""); err == nil || !strings.Contains(err.Error(), "no release 0.9.0") {
		t.Errorf("missing release: err = %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
