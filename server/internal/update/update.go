// Package update finds rad's releases on GitHub and installs them: the
// tarball for this platform, checked against the release's SHA256SUMS. It
// installs rad, or ra, the terminal client, from the same releases.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo is where rad's releases are published.
const Repo = "buldezir/remote-agent"

// Client finds and installs releases.
type Client struct {
	API          string // GitHub's API, e.g. https://api.github.com
	Repo         string // owner/name
	HTTP         *http.Client
	UserAgent    string
	GOOS, GOARCH string // the platform to install for
	// Name is the binary installed: "rad" (also when empty) or "ra".
	Name string
}

// Default is a client for rad's own releases on this platform.
func Default(version string) *Client {
	return For("rad", version)
}

// For is a client that installs name, rad or ra, at version, from rad's
// releases on this platform.
func For(name, version string) *Client {
	return &Client{
		API:       "https://api.github.com",
		Repo:      Repo,
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		UserAgent: name + "/" + version,
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		Name:      name,
	}
}

func (c *Client) binary() string {
	if c.Name == "" {
		return "rad"
	}
	return c.Name
}

// Release is a published release.
type Release struct {
	Version string // the tag without its "v"
	URL     string // the release's page
	assets  map[string]string
}

// Latest returns the newest release, leaving out drafts and prereleases.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	return c.release(ctx, "latest", fmt.Sprintf("%s has no published releases", c.Repo))
}

// Release returns the release of a version, such as the one this rad is.
func (c *Client) Release(ctx context.Context, version string) (*Release, error) {
	return c.release(ctx, "tags/v"+version, fmt.Sprintf("%s has no release %s", c.Repo, version))
}

func (c *Client) release(ctx context.Context, which, notFound string) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.get(ctx, c.API+"/repos/"+c.Repo+"/releases/"+which, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Message string `json:"message"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New(notFound)
	case resp.StatusCode != http.StatusOK:
		if body.Message != "" {
			return nil, fmt.Errorf("GitHub: %s: %s", resp.Status, body.Message)
		}
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	case !IsRelease(body.TagName):
		return nil, fmt.Errorf("the release's tag %q isn't a version", body.TagName)
	}
	rel := &Release{Version: strings.TrimPrefix(body.TagName, "v"), URL: body.HTMLURL, assets: map[string]string{}}
	for _, a := range body.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, nil
}

// Asset is the name of the release file that holds the binary for this
// platform, such as rad-linux-amd64.tar.gz.
func (c *Client) Asset() string { return c.binary() + "-" + c.GOOS + "-" + c.GOARCH + ".tar.gz" }

// maxSize caps downloads and the unpacked binary.
const maxSize = 256 << 20

// Install puts the release's binary at dst. It checks the download against
// the release's SHA256SUMS and runs it once (`rad version`, `ra version`)
// before it replaces dst, so dst is either the old file or a working new
// one.
func (c *Client) Install(ctx context.Context, rel *Release, dst string) (err error) {
	asset := c.Asset()
	url, ok := rel.assets[asset]
	if !ok {
		return fmt.Errorf("release %s has no %s", rel.Version, asset)
	}
	sumsURL, ok := rel.assets["SHA256SUMS"]
	if !ok {
		return fmt.Errorf("release %s has no SHA256SUMS", rel.Version)
	}
	// The new file goes next to dst so it can be renamed over it. Creating it
	// first fails fast where dst's folder can't be written.
	f, err := os.CreateTemp(filepath.Dir(dst), "."+c.binary()+"-update-*")
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()

	sums, err := c.download(ctx, sumsURL)
	if err != nil {
		return err
	}
	want, err := checksum(sums, asset)
	if err != nil {
		return err
	}
	archive, err := c.download(ctx, url)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s doesn't match its checksum in SHA256SUMS", asset)
	}
	if err := extract(archive, c.binary(), f); err != nil {
		return fmt.Errorf("%s: %w", asset, err)
	}
	if err := f.Chmod(0o755); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := verify(ctx, f.Name(), c.binary(), rel.Version); err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}

func (c *Client) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", accept)
	return c.HTTP.Do(req)
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	resp, err := c.get(ctx, url, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("download %s: larger than %d MB", url, maxSize>>20)
	}
	return b, nil
}

// checksum finds name's SHA-256 in a sha256sum listing.
func checksum(sums []byte, name string) (string, error) {
	for line := range strings.Lines(string(sums)) {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no line for %s", name)
}

// extract copies the file named name out of a .tar.gz.
func extract(archive []byte, name string, w io.Writer) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("no %s in the archive", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && strings.TrimPrefix(h.Name, "./") == name {
			if _, err := io.Copy(w, io.LimitReader(tr, maxSize)); err != nil {
				return err
			}
			return nil
		}
	}
}

// verify runs the new binary, which also catches one built for another
// platform, and checks that it is the version the release promised.
func verify(ctx context.Context, path, name, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return fmt.Errorf("the downloaded %s doesn't run: %w", name, err)
	}
	if got := strings.TrimSpace(string(out)); got != name+" "+version {
		return fmt.Errorf("the downloaded %s says %q, not \"%s %s\"", name, got, name, version)
	}
	return nil
}

// KeepCopy saves exe as old, with a hard link where it can, before an
// install replaces exe.
func KeepCopy(exe, old string) error {
	os.Remove(old)
	if os.Link(exe, old) == nil {
		return nil
	}
	in, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(old, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
