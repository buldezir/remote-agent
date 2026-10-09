package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"my-mac.tail1234.ts.net":         "http://my-mac.tail1234.ts.net:7421",
		" 192.168.1.20 ":                 "http://192.168.1.20:7421",
		"192.168.1.20:8000":              "http://192.168.1.20:8000",
		"fd7a::1":                        "http://[fd7a::1]:7421",
		"[fd7a::1]:8000":                 "http://[fd7a::1]:8000",
		"http://10.0.0.5:7421/":          "http://10.0.0.5:7421",
		"https://my-mac.tail1234.ts.net": "https://my-mac.tail1234.ts.net",
	} {
		if got, err := BaseURL(in, 7421); err != nil || got != want {
			t.Errorf("BaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ftp://host", "http://", "http://host/path", "http://host?x=1", "http://u:p@host"} {
		if got, err := BaseURL(in, 7421); err == nil {
			t.Errorf("BaseURL(%q) = %q, want an error", in, got)
		}
	}
}

func TestLoadPairURLs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RAD_HOME", home)
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("port = 8000\npair_urls = [\"mac.ts.net\", \"https://mac.ts.net\"]\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"http://mac.ts.net:8000", "https://mac.ts.net"}; !slices.Equal(c.PairURLs, want) {
		t.Errorf("PairURLs = %v", c.PairURLs)
	}
	write("pair_urls = [\"http://mac.ts.net/v1\"]\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "pair_urls") {
		t.Errorf("err = %v", err)
	}
}
