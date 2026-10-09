// Package netinfo decides where rad listens and which URLs a phone should try.
package netinfo

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"

	"remote-agent/internal/config"
)

var tailscaleNet = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

type Addr struct {
	IP   net.IP
	Kind string // loopback | tailscale | lan
}

// Interfaces returns usable IPv4 addresses classified by kind.
func Interfaces() []Addr {
	var out []Addr
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip := ipn.IP.To4()
			switch {
			case ip.IsLoopback():
				out = append(out, Addr{ip, "loopback"})
			case tailscaleNet.Contains(ip):
				out = append(out, Addr{ip, "tailscale"})
			case ip.IsPrivate():
				out = append(out, Addr{ip, "lan"})
			}
		}
	}
	return out
}

// ListenAddrs returns host:port pairs to bind: loopback and Tailscale, plus LAN when enabled.
func ListenAddrs(c *config.Config) []string {
	if len(c.Listen) > 0 {
		return c.Listen
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range Interfaces() {
		if a.Kind == "lan" && !c.LAN {
			continue
		}
		hp := net.JoinHostPort(a.IP.String(), fmt.Sprint(c.Port))
		if !seen[hp] {
			seen[hp] = true
			out = append(out, hp)
		}
	}
	if len(out) == 0 {
		out = append(out, net.JoinHostPort("127.0.0.1", fmt.Sprint(c.Port)))
	}
	return out
}

// BaseURLs returns http base URLs in the order a phone should try them.
func BaseURLs(listen []string) []string {
	rank := func(host string) int {
		ip := net.ParseIP(host)
		switch {
		case ip == nil:
			return 1
		case tailscaleNet.Contains(ip):
			return 0
		case ip.IsLoopback():
			return 3
		case ip.IsUnspecified():
			return 4
		default:
			return 2
		}
	}
	var hosts []string
	for _, hp := range listen {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip != nil && ip.IsUnspecified() {
			for _, a := range Interfaces() {
				hosts = append(hosts, net.JoinHostPort(a.IP.String(), port))
			}
			continue
		}
		hosts = append(hosts, hp)
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		hi, _, _ := net.SplitHostPort(hosts[i])
		hj, _, _ := net.SplitHostPort(hosts[j])
		return rank(hi) < rank(hj)
	})
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, "http://"+h)
	}
	return out
}

// Hostname returns the machine's short host name.
func Hostname() string {
	h, _ := os.Hostname()
	if short, _, ok := strings.Cut(h, "."); ok && short != "" {
		return short
	}
	return h
}

// PairingLink builds the remoteagent:// URL encoded in the pairing QR code.
func PairingLink(name, code string, baseURLs []string) string {
	q := url.Values{}
	q.Set("v", "1")
	q.Set("name", name)
	q.Set("code", code)
	for _, u := range baseURLs {
		q.Add("url", u)
	}
	return "remoteagent://pair?" + q.Encode()
}
