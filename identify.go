package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// connectRe finds the "connect" keyword and the token after it (CS2
// console command form, but the rest of the string is not constrained).
var connectRe = regexp.MustCompile(`(?i)\bconnect\s+(\S+)`)

// ipv4Re finds candidate dotted quads embedded in arbitrary text;
// net.ParseIP does the real validation.
var ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// bracketIPv6Re finds bracketed IPv6 literals ("[2001:db8::1]").
var bracketIPv6Re = regexp.MustCompile(`\[([0-9A-Fa-f:.]+)\]`)

// hostnameRe is a permissive hostname check (letters, digits, dots,
// hyphens; no leading/trailing hyphen or dot).
var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// parseConnect pulls a host and optional port out of pasted text. It is
// deliberately loose: the "connect" keyword, port, and semicolon are all
// optional, and when no keyword is present it scans for any address-like
// token (IPv4, bracketed IPv6, host:port). Hostnames are accepted when
// they follow "connect" or when the whole input is a single token.
func parseConnect(s string) (host, port string, ok bool) {
	s = strings.TrimSpace(s)
	// Preferred: the token right after "connect", but only if it is
	// address-like (an IP or a dotted name) — "connect to 1.2.3.4" must
	// not latch onto "to".
	if m := connectRe.FindStringSubmatch(s); m != nil {
		if h, p, valid := cleanHostPort(m[1]); valid &&
			(net.ParseIP(h) != nil || strings.Contains(h, ".")) {
			return h, p, true
		}
	}
	if m := bracketIPv6Re.FindStringSubmatch(s); m != nil {
		if ip := net.ParseIP(m[1]); ip != nil {
			return ip.String(), "", true
		}
	}
	// Any standalone token that is an IP (optionally with a port).
	for _, tok := range strings.Fields(s) {
		if h, p, valid := cleanHostPort(tok); valid && net.ParseIP(h) != nil {
			return h, p, true
		}
	}
	// IPv4 embedded in surrounding punctuation/prose.
	if m := ipv4Re.FindString(s); m != "" {
		if ip := net.ParseIP(m); ip != nil {
			return ip.String(), "", true
		}
	}
	// Bare hostname only when it is the entire input (and not the lone
	// "connect" keyword).
	if !strings.ContainsAny(s, " \t\n") {
		if h, p, valid := cleanHostPort(s); valid && !strings.EqualFold(h, "connect") {
			return h, p, true
		}
	}
	return "", "", false
}

// cleanHostPort structurally splits one token into host and optional
// port, handling brackets and stray quotes/semicolons. It validates the
// host is an IP or hostname-shaped; ok is false otherwise.
func cleanHostPort(tok string) (host, port string, ok bool) {
	tok = strings.Trim(tok, `";,`)
	if tok == "" {
		return "", "", false
	}
	switch {
	case strings.HasPrefix(tok, "["):
		end := strings.Index(tok, "]")
		if end < 0 {
			return "", "", false
		}
		host = tok[1:end]
		if rest := tok[end+1:]; strings.HasPrefix(rest, ":") {
			port = strings.Trim(rest[1:], `";,`)
		}
	case strings.Count(tok, ":") == 1:
		parts := strings.SplitN(tok, ":", 2)
		host = parts[0]
		port = strings.Trim(parts[1], `";,`)
	default:
		host = tok // bare IPv4, multi-colon IPv6, or hostname
	}
	host = strings.Trim(host, `";,`)
	if host == "" {
		return "", "", false
	}
	if net.ParseIP(host) != nil || hostnameRe.MatchString(host) {
		return host, port, true
	}
	return "", "", false
}

// rdapBase is the RDAP bootstrap mirror; it 302-redirects to whichever
// RIR holds the address.
const rdapBase = "https://rdap.org"

type rdapNetwork struct {
	Handle   string       `json:"handle"`
	Name     string       `json:"name"`
	Country  string       `json:"country"`
	Entities []rdapEntity `json:"entities"`
}

type rdapEntity struct {
	Handle     string   `json:"handle"`
	Roles      []string `json:"roles"`
	VcardArray []any    `json:"vcardArray"`
}

type rdapEntityDetail struct {
	Handle     string `json:"handle"`
	VcardArray []any  `json:"vcardArray"`
}

// rdapClient talks to an RDAP bootstrap base URL (overridable in tests).
type rdapClient struct {
	base string
	http *http.Client
}

func newRDAPClient() *rdapClient {
	return &rdapClient{
		base: rdapBase,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *rdapClient) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", "faceit-tester/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no RDAP registration for this address")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("RDAP returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// lookupIP returns the registrant org name, netname, and country for an
// IP. RIPE-style networks often carry only an ORG- handle on the network
// entity, so that handle is resolved via a second entity lookup.
func (c *rdapClient) lookupIP(ctx context.Context, ip string) (registrant, netname, country string, err error) {
	var network rdapNetwork
	if err := c.get(ctx, c.base+"/ip/"+ip, &network); err != nil {
		return "", "", "", err
	}
	registrant = networkRegistrant(ctx, c, &network)
	return registrant, network.Name, network.Country, nil
}

func networkRegistrant(ctx context.Context, c *rdapClient, n *rdapNetwork) string {
	pick := func(e rdapEntity) string {
		if s := vcardField(e.VcardArray, "org"); s != "" {
			return s
		}
		return vcardField(e.VcardArray, "fn")
	}
	for _, e := range n.Entities {
		if hasRole(e.Roles, "registrant") || hasRole(e.Roles, "administrative") {
			if name := pick(e); name != "" {
				return name
			}
		}
	}
	for _, e := range n.Entities {
		if !strings.HasPrefix(strings.ToUpper(e.Handle), "ORG-") {
			continue
		}
		var detail rdapEntityDetail
		if err := c.get(ctx, c.base+"/entity/"+e.Handle, &detail); err != nil {
			continue
		}
		if name := pick(rdapEntity{VcardArray: detail.VcardArray}); name != "" {
			return name
		}
	}
	return ""
}

func vcardField(vcardArray []any, field string) string {
	if len(vcardArray) < 2 {
		return ""
	}
	list, ok := vcardArray[1].([]any)
	if !ok {
		return ""
	}
	for _, raw := range list {
		entry, ok := raw.([]any)
		if !ok || len(entry) < 4 {
			continue
		}
		if name, _ := entry[0].(string); name == field {
			if v, ok := entry[3].(string); ok {
				return v
			}
		}
	}
	return ""
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if strings.EqualFold(r, want) {
			return true
		}
	}
	return false
}

// providerKeys maps lowercase substrings of RDAP registrant/netname text
// to the Provider labels used in our server table. Evaluated
// longest-needle-first so specific netnames win over generic ones
// ("dedicatednodes" must claim i3D.net before bare "dedicated" could
// match Dedicated.com).
var providerKeys = buildProviderKeys(map[string]string{
	"reliablesite":     "ReliableSite",
	"hivelocity":       "Hivelocity",
	"serversaustralia": "Servers Australia",
	"worldstream":      "WorldStream",
	"endofreality":     "End of Reality",
	"keystone":         "Keystone Servers",
	"hostvenom":        "HostVenom",
	"dedicatednodes":   "i3D.net", // i3D announces as "DedicatedNodes-…"
	"latitude.sh":      "Latitude.sh",
	"latitude":         "Latitude.sh",
	"cloudserver":      "Latitude.sh", // their CUST-CLOUDSERVER-* netnames
	"hetzner":          "Hetzner",
	"leaseweb":         "Leaseweb",
	"hostpapa":         "HostPapa",
	"colocrossing":     "HostPapa",
	"datacamp":         "Datacamp",
	"cdn77":            "Datacamp",
	"webnx":            "WebNX",
	"nforce":           "NForce",
	"handy networks":   "Handy Networks",
	"handy":            "Handy Networks",
	"myloc":            "MyLoc",
	"wiit":             "MyLoc", // MyLoc is operated by WIIT AG
	"aixit":            "Aixit",
	"altus host":       "Altus Host",
	"altus":            "Altus Host",
	"fragnet":          "Fragnet",
	"edgecenter":       "EdgeCenter",
	"g-core":           "G-Core",
	"gcore":            "G-Core",
	"nfoservers":       "NFOservers",
	"psychz networks":  "Psychz Networks",
	"psychz":           "Psychz Networks",
	"hydra":            "Hydra",
	"ovh":              "OVH",
	"riot games":       "Riot Games",
	"riot":             "Riot Games",
	"i3d.net":          "i3D.net",
	"i3d":              "i3D.net",
	"amazon":           "AWS",
	"google cloud":     "Google Cloud",
	"googlecloud":      "Google Cloud",
	"sggs":             "SGGS",
	"dedicated.com":    "Dedicated.com",
})

type providerKey struct {
	needle   string
	provider string
}

func buildProviderKeys(m map[string]string) []providerKey {
	keys := make([]providerKey, 0, len(m))
	for needle, provider := range m {
		keys = append(keys, providerKey{needle, provider})
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i].needle) != len(keys[j].needle) {
			return len(keys[i].needle) > len(keys[j].needle)
		}
		return keys[i].needle < keys[j].needle
	})
	return keys
}

// providerFromRDAP guesses the hosting provider from RDAP text. Returns
// "" when nothing matches; callers show the raw evidence either way.
func providerFromRDAP(registrant, netname string) string {
	hay := strings.ToLower(registrant + " " + netname)
	for _, k := range providerKeys {
		if strings.Contains(hay, k.needle) {
			return k.provider
		}
	}
	return ""
}

// Resolver hooks, stubbable in tests.
var (
	lookupHostIPs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return net.DefaultResolver.LookupIPAddr(ctx, host)
	}
	lookupPTR = net.DefaultResolver.LookupAddr
)

// identifyResult is one connect-string lookup. Exact is set when the
// target IP is one of our embedded speedtest hosts (definitive, no RDAP
// needed). Provider is the normalized guess ("" = unrecognized).
type identifyResult struct {
	Host       string
	Port       string
	IP         string
	Exact      *Server
	Registrant string
	Netname    string
	Country    string
	PTR        []string
	Provider   string
	Err        error
}

func (r identifyResult) Display() string {
	if r.Port != "" {
		return r.Host + ":" + r.Port
	}
	return r.Host
}

// identifyTarget resolves raw (a pasted connect string or bare address),
// matches it against the known server list first, then falls back to
// RDAP + best-effort PTR for provider identification.
func identifyTarget(ctx context.Context, raw string, servers map[string][]Server) identifyResult {
	return identifyTargetWith(ctx, raw, servers, newRDAPClient())
}

func identifyTargetWith(ctx context.Context, raw string, servers map[string][]Server, rdap *rdapClient) identifyResult {
	var res identifyResult
	host, port, ok := parseConnect(raw)
	if !ok {
		res.Err = fmt.Errorf("could not parse a host from %q", raw)
		return res
	}
	res.Host, res.Port = host, port

	ip := net.ParseIP(host)
	if ip == nil {
		addrs, err := lookupHostIPs(ctx, host)
		if err != nil {
			res.Err = fmt.Errorf("cannot resolve %s: %w", host, err)
			return res
		}
		ip = pickIP(addrs)
		if ip == nil {
			res.Err = fmt.Errorf("no usable address for %s", host)
			return res
		}
	}
	res.IP = ip.String()

	if s := findServerByHost(servers, res.IP); s != nil {
		res.Exact = s
		res.Provider = s.Provider
		return res
	}

	registrant, netname, country, err := rdap.lookupIP(ctx, res.IP)
	if err != nil {
		res.Err = err
		return res
	}
	res.Registrant, res.Netname, res.Country = registrant, netname, country
	res.Provider = providerFromRDAP(registrant, netname)
	// PTR is cosmetic and usually absent on game hosts; ignore failures.
	res.PTR, _ = lookupPTR(ctx, res.IP)
	return res
}

func pickIP(addrs []net.IPAddr) net.IP {
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return a.IP
		}
	}
	if len(addrs) > 0 {
		return addrs[0].IP
	}
	return nil
}

func findServerByHost(servers map[string][]Server, host string) *Server {
	for _, list := range servers {
		for i := range list {
			if strings.EqualFold(list[i].Host, host) {
				s := list[i]
				return &s
			}
		}
	}
	return nil
}

// serverCity is one (region, city) group from the server table.
type serverCity struct {
	Region  string
	City    string
	Servers []Server
}

// citiesWithProvider returns every (region, city) containing a server
// with the given provider label.
func citiesWithProvider(servers map[string][]Server, provider string) []serverCity {
	var out []serverCity
	for _, region := range regionNames(servers) {
		var regionServers []Server
		for _, s := range servers[region] {
			if s.Provider == provider {
				regionServers = append(regionServers, s)
			}
		}
		for _, g := range groupByCity(regionServers) {
			out = append(out, serverCity{Region: region, City: g.City, Servers: g.Servers})
		}
	}
	return out
}
