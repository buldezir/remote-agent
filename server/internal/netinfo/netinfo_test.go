package netinfo

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"remote-agent/internal/config"
)

func TestListenAddrs(t *testing.T) {
	c := &config.Config{Port: 7421}
	if got := ListenAddrs(c); !slices.Equal(got, []string{"0.0.0.0:7421"}) {
		t.Errorf("default = %v", got)
	}
	c.Listen = []string{"127.0.0.1:7499"}
	if got := ListenAddrs(c); !slices.Equal(got, c.Listen) {
		t.Errorf("explicit = %v", got)
	}
}

func TestBaseURLs(t *testing.T) {
	// Explicit addresses are all advertised, Tailscale first and loopback last.
	c := &config.Config{Listen: []string{"127.0.0.1:7421", "192.168.1.5:7421", "100.64.1.2:7421"}}
	want := []string{"http://100.64.1.2:7421", "http://192.168.1.5:7421", "http://127.0.0.1:7421"}
	if got := BaseURLs(c); !slices.Equal(got, want) {
		t.Errorf("explicit = %v", got)
	}

	// The wildcard stands for this machine's interfaces, LAN ones only with lan = true.
	kind := map[string]string{}
	for _, a := range Interfaces() {
		kind["http://"+a.IP.String()+":7421"] = a.Kind
	}
	for _, lan := range []bool{false, true} {
		got := BaseURLs(&config.Config{Port: 7421, LAN: lan})
		if !slices.Contains(got, "http://127.0.0.1:7421") {
			t.Errorf("lan=%v: no loopback in %v", lan, got)
		}
		for _, u := range got {
			if kind[u] == "lan" && !lan {
				t.Errorf("lan=false advertises %s", u)
			}
			if strings.Contains(u, "0.0.0.0") {
				t.Errorf("wildcard advertised: %v", got)
			}
		}
	}
}

func TestPairingLinkName(t *testing.T) {
	link := PairingLink("Studio Mac + iPad", "abc", []string{"http://127.0.0.1:7421"})
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.RawQuery != "code=abc&name=Studio%20Mac%20%2B%20iPad&url=http%3A%2F%2F127.0.0.1%3A7421&v=1" {
		t.Errorf("query = %s", u.RawQuery)
	}
	if got := u.Query().Get("name"); got != "Studio Mac + iPad" {
		t.Errorf("name = %q", got)
	}
}

func TestPairURLs(t *testing.T) {
	c := &config.Config{Listen: []string{"127.0.0.1:7421"}}
	if got := PairURLs(c); !slices.Equal(got, []string{"http://127.0.0.1:7421"}) {
		t.Errorf("detected = %v", got)
	}
	c.PairURLs = []string{"http://mac.ts.net:7421"}
	if got := PairURLs(c); !slices.Equal(got, c.PairURLs) {
		t.Errorf("configured = %v", got)
	}
}
