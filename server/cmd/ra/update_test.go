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

// releases serves one release, v0.2.0, with ra for this platform: a
// script that says its version.
func releases(t *testing.T) *update.Client {
	t.Helper()
	asset := "ra-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho ra 0.2.0\n"
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
		case "/repos/o/r/releases/latest":
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
	return &update.Client{API: srv.URL, Repo: "o/r", HTTP: srv.Client(), UserAgent: "ra/test", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Name: "ra"}
}

func TestUpdateInstallsANewerRelease(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "ra")
	os.WriteFile(exe, []byte("#!/bin/sh\necho ra 0.1.0\n"), 0o755)
	var out bytes.Buffer
	if err := selfUpdate(context.Background(), releases(t), "0.1.0", exe, false, false, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); !strings.Contains(string(got), "ra 0.2.0") {
		t.Errorf("ra is %q", got)
	}
	if old, _ := os.ReadFile(exe + ".old"); !strings.Contains(string(old), "ra 0.1.0") {
		t.Errorf("ra.old is %q", old)
	}
	if !strings.Contains(out.String(), "installed ra 0.2.0") {
		t.Error(out.String())
	}
}

func TestUpdateLeavesAnUpToDateRa(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "ra")
	os.WriteFile(exe, []byte("current"), 0o755)
	var out bytes.Buffer
	if err := selfUpdate(context.Background(), releases(t), "0.2.0", exe, false, false, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "current" || !strings.Contains(out.String(), "up to date") {
		t.Errorf("ra is %q; %s", got, out.String())
	}
}

func TestUpdateRefusesADevelopmentBuildWithoutForce(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "ra")
	os.WriteFile(exe, []byte("dev"), 0o755)
	var out bytes.Buffer
	err := selfUpdate(context.Background(), releases(t), "dev", exe, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err %v", err)
	}
	if err := selfUpdate(context.Background(), releases(t), "dev", exe, false, true, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); !strings.Contains(string(got), "ra 0.2.0") {
		t.Errorf("ra is %q", got)
	}
}

func TestUpdateCheckOnlyReports(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "ra")
	os.WriteFile(exe, []byte("old"), 0o755)
	var out bytes.Buffer
	if err := selfUpdate(context.Background(), releases(t), "0.1.0", exe, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" || !strings.Contains(out.String(), "ra 0.2.0 is available (this is 0.1.0)") {
		t.Errorf("ra is %q; %s", got, out.String())
	}
}
