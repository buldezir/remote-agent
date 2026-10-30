package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// PairingLink is a parsed remoteagent://pair?… link from `rad pair`.
type PairingLink struct {
	Name string
	Code string
	URLs []string
}

var baseURL = regexp.MustCompile(`^https?://[^/]+$`)

// ParsePairingLink parses a link as `rad pair --print-url` prints it.
func ParsePairingLink(text string) (PairingLink, bool) {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "remoteagent://pair") {
		return PairingLink{}, false
	}
	_, query, ok := strings.Cut(s, "?")
	if !ok {
		return PairingLink{}, false
	}
	params, err := url.ParseQuery(query)
	if err != nil {
		return PairingLink{}, false
	}
	code := params.Get("code")
	if code == "" {
		return PairingLink{}, false
	}
	var urls []string
	for _, u := range params["url"] {
		u = strings.TrimRight(u, "/")
		if baseURL.MatchString(u) {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return PairingLink{}, false
	}
	name := params.Get("name")
	if name == "" {
		name = "Server"
	}
	return PairingLink{Name: name, Code: code, URLs: urls}, true
}

// LoopbackFirst orders base URLs with this machine's own first: ra talks
// to the rad beside it.
func LoopbackFirst(urls []string) []string {
	out := slices.Clone(urls)
	slices.SortStableFunc(out, func(a, b string) int {
		la, lb := isLoopback(a), isLoopback(b)
		switch {
		case la && !lb:
			return -1
		case lb && !la:
			return 1
		}
		return 0
	})
	return out
}

func isLoopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.")
}

// Pair redeems the link's code against each of its URLs in order, and
// returns the result and the URL that worked.
func Pair(ctx context.Context, link PairingLink, deviceName string) (PairResult, string, error) {
	var last error = &RPCError{"unreachable", "rad isn't reachable at " + strings.Join(link.URLs, " or ")}
	body, _ := json.Marshal(map[string]string{"code": link.Code, "deviceName": deviceName})
	for _, base := range link.URLs {
		rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		req, _ := http.NewRequestWithContext(rctx, "POST", base+"/v1/pair", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			continue
		}
		var res PairResult
		var e RPCError
		ok := resp.StatusCode == http.StatusOK
		if ok {
			err = json.NewDecoder(resp.Body).Decode(&res)
		} else {
			json.NewDecoder(resp.Body).Decode(&e)
		}
		resp.Body.Close()
		cancel()
		if ok {
			return res, base, err
		}
		// rad answered; other URLs won't do better.
		if e.Code == "unauthorized" {
			return res, "", &RPCError{"unauthorized", "The pairing code is wrong or has expired. Run `rad pair` again."}
		}
		if e.Code != "" {
			return res, "", &e
		}
		last = &RPCError{"http", fmt.Sprintf("rad answered HTTP %d", resp.StatusCode)}
	}
	return PairResult{}, "", last
}

// ErrNoRad: there's no rad on this machine to pair with.
var ErrNoRad = errors.New("rad isn't installed here: put it on your PATH, or set RAD to its path")

// serverAppRad is where the Remote Agent Server app keeps its rad.
const serverAppRad = "/Applications/Remote Agent Server.app/Contents/MacOS/rad"

// FindRad finds this machine's rad: $RAD, then rad on the PATH, then on a
// Mac the Server app's.
func FindRad() (string, error) {
	if p := os.Getenv("RAD"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("rad"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(serverAppRad); err == nil {
			return serverAppRad, nil
		}
	}
	return "", ErrNoRad
}

// LocalPairingLink asks this machine's rad for a pairing link
// (`rad pair --print-url`). rad gets ra's environment, so RAD_HOME
// chooses its config.
func LocalPairingLink(ctx context.Context) (PairingLink, error) {
	rad, err := FindRad()
	if err != nil {
		return PairingLink{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rad, "pair", "--print-url")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return PairingLink{}, fmt.Errorf("%s pair: %s", filepath.Base(rad), msg)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if link, ok := ParsePairingLink(line); ok {
			return link, nil
		}
	}
	return PairingLink{}, fmt.Errorf("%s pair --print-url printed no pairing link", rad)
}

// DeviceName is the name ra pairs as: "ra on <host>".
func DeviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "ra"
	}
	return "ra on " + strings.TrimSuffix(host, ".local")
}

// PairLocal pairs with this machine's rad, trying its loopback address
// first, and saves the server and token in store.
func PairLocal(ctx context.Context, store *ServerStore) (SavedServer, string, error) {
	link, err := LocalPairingLink(ctx)
	if err != nil {
		return SavedServer{}, "", err
	}
	return PairWith(ctx, store, link)
}

// PairWith pairs with the link's server, its loopback address first, and
// saves the server and token in store.
func PairWith(ctx context.Context, store *ServerStore, link PairingLink) (SavedServer, string, error) {
	link.URLs = LoopbackFirst(link.URLs)
	res, worked, err := Pair(ctx, link, DeviceName())
	if err != nil {
		return SavedServer{}, "", err
	}
	urls := []string{worked}
	for _, u := range link.URLs {
		if u != worked {
			urls = append(urls, u)
		}
	}
	saved := SavedServer{ID: res.ServerID, Name: res.Name, URLs: urls, DeviceID: res.DeviceID, AddedAt: time.Now().UTC()}
	if err := store.Save(saved, res.Token); err != nil {
		return SavedServer{}, "", err
	}
	return saved, res.Token, nil
}
