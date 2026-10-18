package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Server is one testable endpoint. City roughly matches a FACEIT location;
// Provider is the hosting label from the official HiperZ test scripts.
type Server struct {
	City     string `json:"city"`
	Provider string `json:"provider"`
	Host     string `json:"host"`
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
// etc.) to match FACEIT's client. Providers and hosts are transcribed from
// the official HiperZ test scripts.
var embeddedServers = map[string][]Server{
	"NA": {
		// San Jose
		{"San Jose", "hz", "167.160.91.2"},
		{"San Jose", "cdn77", "185.152.67.2"},
		{"San Jose", "webnx", "mirrors-lax.webnx.com"},
		{"San Jose", "colocrossing", "107.175.180.6"},
		// Denver
		{"Denver", "cdn77", "84.17.63.1"},
		{"Denver", "handy", "173.248.162.1"},
		// Dallas
		{"Dallas", "PS", "108.181.54.9"},
		{"Dallas", "GSL", "66.23.201.30"},
		{"Dallas", "hivelocity", "speedtest.dal.hivelocity.net"},
		{"Dallas", "cdn77", "89.187.164.1"},
		{"Dallas", "colocrossing", "192.3.237.150"},
		{"Dallas", "RIOT", "104.160.151.182"},
		// Chicago
		{"Chicago", "PS", "108.181.62.197"},
		{"Chicago", "cdn77", "185.93.1.65"},
		{"Chicago", "GSL", "103.214.111.228"},
		{"Chicago", "v", "104.128.48.3"},
		{"Chicago", "colocrossing", "66.225.198.198"},
		{"Chicago", "nfo", "216.52.148.47"},
		// Virginia
		{"Virginia", "hz", "172.93.99.1"},
		{"Virginia", "cdn77", "185.59.223.8"},
		{"Virginia", "gsl", "66.23.200.20"},
	},
	"EU": {
		// France
		{"France", "O", "54.37.87.229"},
		{"France", "cdn77", "185.93.2.193"},
		// UK
		{"UK", "O", "198.244.202.178"},
		{"UK", "hydra", "185.38.150.1"},
		{"UK", "cdn77", "185.59.221.51"},
		// Germany
		{"Germany", "m", "85.114.146.1"},
		{"Germany", "h", "fsn.icmp.hetzner.com"},
		{"Germany", "O", "145.239.244.75"},
		{"Germany", "cdn77", "185.102.219.93"},
		{"Germany", "ckras", "83.141.29.20"},
		// Netherlands
		{"Netherlands", "hz", "mirror.nforce.com"},
		{"Netherlands", "ws", "iperf.worldstream.nl"},
		{"Netherlands", "gcp.west4.premium", "34.91.82.186"},
		{"Netherlands", "cdn77", "185.102.218.1"},
		{"Netherlands", "leaseweb", "mirror.nl.leaseweb.net"},
		{"Netherlands", "i3d", "mirror.i3d.net"},
		// Sweden
		{"Sweden", "a", "79.142.78.1"},
		{"Sweden", "m", "speed.se.m247.ro"},
		{"Sweden", "cdn77", "185.76.9.135"},
		{"Sweden", "frag", "185.62.206.1"},
		{"Sweden", "excellent", "178.236.67.35"},
		// Finland
		{"Finland", "hetzner", "hel.icmp.hetzner.com"},
		// Moscow
		{"Moscow", "ps1", "140.174.177.1"},
		{"Moscow", "ps2", "108.181.60.1"},
		{"Moscow", "edge", "92.38.138.1"},
		{"Moscow", "rsmedia", "62.122.214.254"},
	},
	"SA": {
		// Lima
		{"Lima", "cdn77", "79.127.252.129"},
		// São Paulo
		{"São Paulo", "cdn77", "138.199.4.1"},
		{"São Paulo", "maxiSP1", "177.54.147.60"},
		{"São Paulo", "maxiSP2", "103.14.27.97"},
		{"São Paulo", "gcp", "34.95.203.40"},
		{"São Paulo", "i3d", "185.50.104.53"},
		{"São Paulo", "gcore", "5.188.238.1"},
		{"São Paulo", "aws", "54.207.127.254"},
		// Santiago
		{"Santiago", "maxi", "45.250.252.51"},
		// Buenos Aires
		{"Buenos Aires", "maxi", "103.106.58.59"},
		{"Buenos Aires", "inet", "186.148.224.119"},
		{"Buenos Aires", "cdn77", "169.150.214.140"},
	},
	"SEA": {
		{"Singapore", "cdn77", "89.187.162.1"},
		{"Singapore", "O", "sgp.lg.ovh.net"},
		{"Singapore", "GCP.premium", "35.198.228.5"},
		{"Singapore", "sggs", "103.14.247.1"},
		{"Singapore", "LW", "mirror.sg.leaseweb.net"},
		{"Singapore", "PS", "103.126.138.20"},
		{"Singapore", "gcore", "92.223.85.8"},
		{"Singapore", "streamline", "103.62.48.1"},
		{"Singapore", "softlayer", "speedtest.sng01.softlayer.com"},
		{"Singapore", "aws", "46.51.216.14"},
	},
	"OCE": {
		{"Sydney", "O", "51.161.196.31"},
		{"Sydney", "GSL", "103.212.227.31"},
		{"Sydney", "cdn77", "143.244.63.144"},
		{"Sydney", "sau", "27.50.68.245"},
		{"Sydney", "softlayer", "speedtest.syd01.softlayer.com"},
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
		var servers map[string][]Server
		if err := json.Unmarshal(data, &servers); err != nil {
			return nil, err
		}
		if len(servers) == 0 {
			continue
		}
		return servers, nil
	}
	return nil, nil
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
