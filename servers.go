package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Server is one testable endpoint. City roughly matches a FACEIT location;
// Provider is the hosting label from the official HiperZ test scripts.
// MaxPPS caps the probe send rate for providers with a known ICMP policer
// (0 = default 64 pps); ProbeSize overrides the ICMP payload in bytes for
// providers that filter large echo packets (0 = default 600). Scrubbed
// marks endpoints whose ICMP responder sits behind a DDoS scrubber: replies
// are capped at ~1/sec under load and low-rate probes are dropped at
// random, so loss and jitter are not measurable and are reported as n/a.
type Server struct {
	City      string `json:"city"`
	Provider  string `json:"provider"`
	Host      string `json:"host"`
	MaxPPS    int    `json:"max_pps,omitempty"`
	ProbeSize int    `json:"probe_size,omitempty"`
	Scrubbed  bool   `json:"scrubbed,omitempty"`
}

// srv is shorthand for a server without pacing/size overrides.
func srv(city, provider, host string) Server {
	return Server{City: city, Provider: provider, Host: host}
}

// ID is a stable identifier for UI state keyed per server.
func (s Server) ID() string {
	return s.City + "." + s.Provider
}

// DisplayName is shown in the results table.
func (s Server) DisplayName() string {
	return s.City + "." + s.Provider
}

// RegionOrder is the dropdown display order (NA first, matching the default
// selection).
var RegionOrder = []string{"NA", "EU", "SA", "SEA", "OCE"}

// embeddedServers is trimmed to locations FACEIT currently offers. City
// groups rename hiperz endpoints (lax→San Jose, ny→Virginia, de→Germany,
// etc.) to match FACEIT's client. Providers are the hosting companies that
// own the endpoints per RDAP registration (the HiperZ scripts used opaque
// codenames — O→OVH, PS→Psychz, m→MyLoc, a→Altus Host, etc.); hosts are
// transcribed from the official HiperZ test scripts, except Chicago.NFOservers
// which comes from NFOservers' own network page (the HiperZ IP was retired;
// see PACKET_LOSS_FINDINGS.md). MaxPPS values are measured ICMP policer
// quotas minus a ~10% margin; Scrubbed marks the one DDoS-scrubbed
// endpoint whose loss cannot be measured at all.
var embeddedServers = map[string][]Server{
	"NA": {
		// San Jose
		srv("San Jose", "Dedicated.com", "167.160.91.2"),
		srv("San Jose", "Datacamp", "185.152.67.2"),
		srv("San Jose", "WebNX", "mirrors-lax.webnx.com"),
		srv("San Jose", "HostPapa", "107.175.180.6"),
		// Denver
		srv("Denver", "Datacamp", "84.17.63.1"),
		srv("Denver", "Handy Networks", "173.248.162.1"),
		// Dallas
		srv("Dallas", "Psychz Networks", "108.181.54.9"),
		srv("Dallas", "Dedicated.com", "66.23.201.30"),
		// Hivelocity's ICMP responder is behind a DDoS scrubber: it answers
		// exactly ~1 echo reply/sec under load (any rate/size/payload) and
		// drops low-rate v4 probes at random, so loss/jitter are not
		// measurable — see PACKET_LOSS_FINDINGS.md. Probe at the default
		// rate for RTT samples; stats are marked Scrubbed.
		{City: "Dallas", Provider: "Hivelocity", Host: "speedtest.dal.hivelocity.net", Scrubbed: true},
		srv("Dallas", "Datacamp", "89.187.164.1"),
		srv("Dallas", "HostPapa", "192.3.237.150"),
		srv("Dallas", "Riot Games", "104.160.151.182"),
		// Chicago
		srv("Chicago", "Psychz Networks", "108.181.62.197"),
		srv("Chicago", "Datacamp", "185.93.1.65"),
		srv("Chicago", "End of Reality", "103.214.111.228"),
		srv("Chicago", "HostVenom", "104.128.48.3"),
		srv("Chicago", "HostPapa", "66.225.198.198"),
		// NFOservers filters ICMP payloads above ~56 bytes entirely
		// (600-byte probes drop 100% at any rate); small packets are clean
		// at 64pps.
		{City: "Chicago", Provider: "NFOservers", Host: "74.91.113.173", ProbeSize: 56},
		// Virginia
		srv("Virginia", "ReliableSite", "172.93.99.1"),
		srv("Virginia", "Datacamp", "185.59.223.8"),
		srv("Virginia", "Dedicated.com", "66.23.200.20"),
	},
	"EU": {
		// France
		{City: "France", Provider: "OVH", Host: "54.37.87.229", MaxPPS: 45},
		srv("France", "Datacamp", "185.93.2.193"),
		// UK
		// OVH UK's policer quota is exactly ~105 replies per 10s at any
		// send rate up to 64pps.
		{City: "UK", Provider: "OVH", Host: "198.244.202.178", MaxPPS: 9},
		srv("UK", "Hydra", "185.38.150.1"),
		srv("UK", "Datacamp", "185.59.221.51"),
		// Germany
		srv("Germany", "MyLoc", "85.114.146.1"),
		srv("Germany", "Hetzner", "fsn.icmp.hetzner.com"),
		{City: "Germany", Provider: "OVH", Host: "145.239.244.75", MaxPPS: 45},
		srv("Germany", "Datacamp", "185.102.219.93"),
		srv("Germany", "Aixit", "83.141.29.20"),
		// Netherlands
		srv("Netherlands", "NForce", "mirror.nforce.com"),
		srv("Netherlands", "WorldStream", "iperf.worldstream.nl"),
		srv("Netherlands", "Google Cloud (West4 Premium)", "34.91.82.186"),
		srv("Netherlands", "Datacamp", "185.102.218.1"),
		srv("Netherlands", "Leaseweb", "mirror.nl.leaseweb.net"),
		srv("Netherlands", "i3D.net", "mirror.i3d.net"),
		// Sweden
		srv("Sweden", "Altus Host", "79.142.78.1"),
		srv("Sweden", "Datacamp", "185.76.9.135"),
		srv("Sweden", "Fragnet", "185.62.206.1"),
		// Finland
		srv("Finland", "Hetzner", "hel.icmp.hetzner.com"),
		// Moscow
		{City: "Moscow", Provider: "NTT", Host: "140.174.177.1", MaxPPS: 27},
		{City: "Moscow", Provider: "Psychz Networks", Host: "108.181.60.1", MaxPPS: 20},
		srv("Moscow", "EdgeCenter", "92.38.138.1"),
	},
	"SA": {
		// Lima
		srv("Lima", "Datacamp", "79.127.252.129"),
		// São Paulo
		srv("São Paulo", "Datacamp", "138.199.4.1"),
		srv("São Paulo", "Latitude.sh SP1", "177.54.147.60"),
		srv("São Paulo", "Latitude.sh SP2", "103.14.27.97"),
		srv("São Paulo", "Google Cloud", "34.95.203.40"),
		srv("São Paulo", "i3D.net", "185.50.104.53"),
		srv("São Paulo", "G-Core", "5.188.238.1"),
		srv("São Paulo", "AWS", "54.207.127.254"),
		// Santiago
		srv("Santiago", "Latitude.sh", "45.250.252.51"),
		// Buenos Aires
		// Quota is exactly ~304 replies per 10s at 64 and 128pps.
		{City: "Buenos Aires", Provider: "Latitude.sh", Host: "103.106.58.59", MaxPPS: 27},
		srv("Buenos Aires", "Datacamp", "169.150.214.140"),
	},
	"SEA": {
		srv("Singapore", "Datacamp", "89.187.162.1"),
		{City: "Singapore", Provider: "OVH", Host: "sgp.lg.ovh.net", MaxPPS: 45},
		srv("Singapore", "Google Cloud (Premium)", "35.198.228.5"),
		srv("Singapore", "SGGS", "103.14.247.1"),
		srv("Singapore", "Leaseweb", "mirror.sg.leaseweb.net"),
		srv("Singapore", "G-Core", "92.223.85.8"),
		srv("Singapore", "AWS", "46.51.216.14"),
	},
	"OCE": {
		{City: "Sydney", Provider: "OVH", Host: "51.161.196.31", MaxPPS: 45},
		srv("Sydney", "Keystone Servers", "103.212.227.31"),
		srv("Sydney", "Datacamp", "143.244.63.144"),
		srv("Sydney", "Servers Australia", "27.50.68.245"),
	},
}

// CityGroup is a city and its servers, in first-appearance order.
type CityGroup struct {
	City    string
	Servers []Server
}

// groupByCity groups servers by city, preserving first-appearance order.
func groupByCity(servers []Server) []CityGroup {
	index := make(map[string]int)
	var groups []CityGroup
	for _, s := range servers {
		if i, ok := index[s.City]; ok {
			groups[i].Servers = append(groups[i].Servers, s)
			continue
		}
		index[s.City] = len(groups)
		groups = append(groups, CityGroup{City: s.City, Servers: []Server{s}})
	}
	return groups
}

// loadServers returns the embedded server lists, overridden by servers.json
// next to the executable (or cwd) if present and valid. A JSON file replaces
// the embedded lists entirely.
func loadServers() map[string][]Server {
	if override, err := loadServersJSON(); err == nil && override != nil {
		return override
	}
	return embeddedServers
}

func loadServersJSON() (map[string][]Server, error) {
	candidates := []string{"servers.json"}
	if exe, err := os.Executable(); err == nil {
		candidates = append([]string{filepath.Join(filepath.Dir(exe), "servers.json")}, candidates...)
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		servers, err := parseServersJSON(data)
		if err != nil {
			return nil, err
		}
		if servers != nil {
			return servers, nil
		}
	}
	return nil, nil
}

// parseServersJSON decodes an override file. It has full parity with the
// embedded table: entries carry city/provider/host plus optional max_pps,
// probe_size, and scrubbed fields. It returns (nil, nil) for an empty
// document (treated as no override) and an error for incomplete or
// out-of-range entries.
func parseServersJSON(data []byte) (map[string][]Server, error) {
	var servers map[string][]Server
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, nil
	}
	for region, list := range servers {
		for i, s := range list {
			if s.City == "" || s.Provider == "" || s.Host == "" {
				return nil, fmt.Errorf("%s[%d]: entry missing city, provider, or host", region, i)
			}
			if s.MaxPPS < 0 {
				return nil, fmt.Errorf("%s/%s: max_pps must be >= 0", s.City, s.Provider)
			}
			if s.ProbeSize < 0 {
				return nil, fmt.Errorf("%s/%s: probe_size must be >= 0", s.City, s.Provider)
			}
		}
	}
	return servers, nil
}

// regionNames returns regions to show: RegionOrder first, then any extra
// regions found only in an override file (sorted for stability).
func regionNames(servers map[string][]Server) []string {
	seen := make(map[string]bool, len(RegionOrder))
	var names []string
	for _, r := range RegionOrder {
		if _, ok := servers[r]; ok {
			names = append(names, r)
			seen[r] = true
		}
	}
	var extra []string
	for r := range servers {
		if !seen[r] {
			extra = append(extra, r)
		}
	}
	sort.Strings(extra)
	return append(names, extra...)
}
