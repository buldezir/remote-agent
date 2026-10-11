package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"remote-agent/internal/config"
)

// healthInfo is what /v1/health reports.
type healthInfo struct {
	ServerID string `json:"serverId"`
	Version  string `json:"version"`
}

// health asks this install's server for /v1/health at each address it
// listens on, and returns the first that answers. With serverID set, a
// server of another install (another RAD_HOME) on the same port doesn't count.
func health(cfg *config.Config, serverID string) (addr string, info healthInfo) {
	var hosts []string
	if len(cfg.Listen) > 0 {
		hosts = cfg.Listen
	} else {
		hosts = []string{net.JoinHostPort("127.0.0.1", fmt.Sprint(cfg.Port))}
	}
	client := http.Client{Timeout: 2 * time.Second}
	for _, hp := range hosts {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			hp = net.JoinHostPort("127.0.0.1", port)
		}
		resp, err := client.Get("http://" + hp + "/v1/health")
		if err != nil {
			continue
		}
		var info healthInfo
		json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		if serverID == "" || info.ServerID == serverID {
			return "http://" + hp, info
		}
	}
	return "", healthInfo{}
}
