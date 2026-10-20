package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestComputeJitter(t *testing.T) {
	ms := func(vals ...int) []time.Duration {
		out := make([]time.Duration, len(vals))
		for i, v := range vals {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name string
		rtts []time.Duration
		want time.Duration
	}{
		{"empty", nil, 0},
		{"single", ms(10), 0},
		{"constant", ms(10, 10, 10, 10), 0},
		{"simple", ms(10, 20, 10, 20), 10 * time.Millisecond},
		{"ascending", ms(10, 20, 30), 10 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeJitter(tt.rtts); got != tt.want {
				t.Errorf("computeJitter(%v) = %v, want %v", tt.rtts, got, tt.want)
			}
		})
	}
}

func TestEmbeddedServersCoverAllRegions(t *testing.T) {
	wantRegions := []string{"NA", "EU", "SA", "SEA", "OCE"}
	if len(RegionOrder) != len(wantRegions) {
		t.Fatalf("RegionOrder = %v, want %v", RegionOrder, wantRegions)
	}
	for i, r := range wantRegions {
		if RegionOrder[i] != r {
			t.Errorf("RegionOrder[%d] = %q, want %q", i, RegionOrder[i], r)
		}
	}

	total := 0
	for _, region := range RegionOrder {
		servers, ok := embeddedServers[region]
		if !ok {
			t.Errorf("missing region %q", region)
			continue
		}
		if len(servers) == 0 {
			t.Errorf("region %q has no servers", region)
		}
		ids := make(map[string]bool)
		for _, s := range servers {
			if s.City == "" || s.Provider == "" || s.Host == "" {
				t.Errorf("region %q has incomplete entry: %+v", region, s)
			}
			if ids[s.ID()] {
				t.Errorf("region %q has duplicate server ID %q", region, s.ID())
			}
			ids[s.ID()] = true
		}
		total += len(servers)
	}
	// Trimmed to FACEIT-official locations: 21 NA + 23 EU + 11 SA + 7 SEA
	// + 4 OCE = 66. Dead HiperZ endpoints removed and Chicago.NFOservers
	// replaced per PACKET_LOSS_FINDINGS.md.
	if total != 66 {
		t.Errorf("total embedded servers = %d, want 66", total)
	}
}

func TestFaceitCityNames(t *testing.T) {
	// Cities renamed from hiperz labels to FACEIT client names.
	naCities := map[string]bool{}
	for _, s := range embeddedServers["NA"] {
		naCities[s.City] = true
	}
	for _, want := range []string{"San Jose", "Denver", "Dallas", "Chicago", "Virginia"} {
		if !naCities[want] {
			t.Errorf("NA missing FACEIT city %q", want)
		}
	}
	for _, gone := range []string{"lax", "ny", "Miami", "gcp"} {
		if naCities[gone] {
			t.Errorf("NA still contains non-FACEIT city %q", gone)
		}
	}
}

func TestGroupByCity(t *testing.T) {
	servers := []Server{
		srv("Chicago", "Psychz Networks", "108.181.62.197"),
		srv("Dallas", "Psychz Networks", "108.181.54.9"),
		srv("Chicago", "Datacamp", "185.93.1.65"),
	}
	groups := groupByCity(servers)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].City != "Chicago" || len(groups[0].Servers) != 2 {
		t.Errorf("first group = %+v, want Chicago with 2 servers", groups[0])
	}
	if groups[1].City != "Dallas" || len(groups[1].Servers) != 1 {
		t.Errorf("second group = %+v, want Dallas with 1 server", groups[1])
	}
}

func TestRegionNamesOrder(t *testing.T) {
	got := regionNames(embeddedServers)
	want := []string{"NA", "EU", "SA", "SEA", "OCE"}
	if len(got) != len(want) {
		t.Fatalf("regionNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("regionNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegionNamesIncludesOverrideOnlyRegions(t *testing.T) {
	servers := map[string][]Server{
		"MARS": {srv("Phobos", "test", "10.0.0.1")},
		"NA":   {srv("Chicago", "Psychz Networks", "108.181.62.197")},
	}
	got := regionNames(servers)
	if got[0] != "NA" {
		t.Errorf("default region should come first, got %v", got)
	}
	if len(got) != 2 || got[1] != "MARS" {
		t.Errorf("override-only region missing, got %v", got)
	}
}

func TestServerIDAndDisplayName(t *testing.T) {
	s := srv("San Jose", "Dedicated.com", "167.160.91.2")
	if s.ID() != "San Jose.Dedicated.com" {
		t.Errorf("ID() = %q, want %q", s.ID(), "San Jose.Dedicated.com")
	}
	if s.DisplayName() != "San Jose.Dedicated.com" {
		t.Errorf("DisplayName() = %q", s.DisplayName())
	}
}

// TestServerListMatchesFindings pins the embedded list to the packet-loss
// investigation in PACKET_LOSS_FINDINGS.md: the NFO replacement host with
// its small-payload override, the nine ICMP policer caps, and the retired
// endpoints that must stay gone.
func TestServerListMatchesFindings(t *testing.T) {
	byID := map[string]Server{}
	for _, region := range RegionOrder {
		for _, s := range embeddedServers[region] {
			byID[s.ID()] = s
		}
	}

	// Replacement: NFOservers moved off the retired 216.52.148.47; the new
	// IP filters large ICMP payloads, so it must probe at 56 bytes.
	nfo, ok := byID["Chicago.NFOservers"]
	if !ok {
		t.Fatal("Chicago.NFOservers missing")
	}
	if nfo.Host != "74.91.113.173" {
		t.Errorf("Chicago.NFOservers host = %q, want 74.91.113.173", nfo.Host)
	}
	if nfo.ProbeSize != 56 {
		t.Errorf("Chicago.NFOservers ProbeSize = %d, want 56", nfo.ProbeSize)
	}
	if nfo.MaxPPS != 0 {
		t.Errorf("Chicago.NFOservers MaxPPS = %d, want 0 (no rate cap)", nfo.MaxPPS)
	}

	// Measured ICMP policer quotas (see findings doc for rate curves).
	wantPPS := map[string]int{
		"UK.OVH":                   9,
		"France.OVH":               45,
		"Germany.OVH":              45,
		"Singapore.OVH":            45,
		"Sydney.OVH":               45,
		"Buenos Aires.Latitude.sh": 27,
		"Moscow.NTT":               27,
		"Moscow.Psychz Networks":   20,
	}
	for id, want := range wantPPS {
		s, ok := byID[id]
		if !ok {
			t.Errorf("%s missing from embedded servers", id)
			continue
		}
		if s.MaxPPS != want {
			t.Errorf("%s MaxPPS = %d, want %d", id, s.MaxPPS, want)
		}
	}

	// Hivelocity sits behind a DDoS scrubber (not a plain policer): loss
	// is unmeasurable, so it must be Scrubbed at the default probe rate.
	hv, ok := byID["Dallas.Hivelocity"]
	if !ok {
		t.Fatal("Dallas.Hivelocity missing")
	}
	if !hv.Scrubbed {
		t.Error("Dallas.Hivelocity should be Scrubbed")
	}
	if hv.MaxPPS != 0 || hv.ProbeSize != 0 {
		t.Errorf("Dallas.Hivelocity overrides = (MaxPPS %d, ProbeSize %d), want (0, 0)", hv.MaxPPS, hv.ProbeSize)
	}

	// Retired endpoints: dead hosts with no replacement. IDs use the
	// pre-rename HiperZ codenames as documented in PACKET_LOSS_FINDINGS.md.
	for _, id := range []string{
		"Sweden.m", "Sweden.excellent", "Moscow.rsmedia",
		"Buenos Aires.inet", "Singapore.PS", "Singapore.streamline",
		"Singapore.softlayer", "Sydney.softlayer",
	} {
		if _, ok := byID[id]; ok {
			t.Errorf("retired endpoint %s still present", id)
		}
	}
}

func TestParseServersJSONParity(t *testing.T) {
	// Override files carry the same fields as the embedded table.
	data := []byte(`{
		"EU": [
			{"city": "UK", "provider": "OVH", "host": "198.244.202.178", "max_pps": 9},
			{"city": "Chicago2", "provider": "NFOservers", "host": "74.91.113.173", "probe_size": 56},
			{"city": "Plain", "provider": "p", "host": "10.0.0.1"},
			{"city": "Scrub", "provider": "s", "host": "10.0.0.2", "scrubbed": true}
		]
	}`)
	servers, err := parseServersJSON(data)
	if err != nil {
		t.Fatalf("parseServersJSON: %v", err)
	}
	list := servers["EU"]
	if len(list) != 4 {
		t.Fatalf("got %d entries, want 4", len(list))
	}
	if list[0].MaxPPS != 9 || list[0].ProbeSize != 0 {
		t.Errorf("paced entry = %+v, want MaxPPS 9", list[0])
	}
	if list[1].ProbeSize != 56 || list[1].MaxPPS != 0 {
		t.Errorf("sized entry = %+v, want ProbeSize 56", list[1])
	}
	if list[2].MaxPPS != 0 || list[2].ProbeSize != 0 || list[2].Scrubbed {
		t.Errorf("plain entry = %+v, want zero overrides", list[2])
	}
	if !list[3].Scrubbed {
		t.Errorf("scrubbed entry = %+v, want Scrubbed true", list[3])
	}

	// Empty document means "no override", not an empty server set.
	if got, err := parseServersJSON([]byte(`{}`)); err != nil || got != nil {
		t.Errorf("empty doc = (%v, %v), want (nil, nil)", got, err)
	}

	// Incomplete and out-of-range entries are rejected.
	bad := []string{
		`{"EU": [{"city": "", "provider": "p", "host": "10.0.0.1"}]}`,
		`{"EU": [{"city": "C", "provider": "", "host": "10.0.0.1"}]}`,
		`{"EU": [{"city": "C", "provider": "p", "host": ""}]}`,
		`{"EU": [{"city": "C", "provider": "p", "host": "10.0.0.1", "max_pps": -1}]}`,
		`{"EU": [{"city": "C", "provider": "p", "host": "10.0.0.1", "probe_size": -5}]}`,
	}
	for _, doc := range bad {
		if _, err := parseServersJSON([]byte(doc)); err == nil {
			t.Errorf("parseServersJSON(%s) succeeded, want error", doc)
		}
	}
}

func TestDumpServersJSONRoundTrip(t *testing.T) {
	// The dump must be loadable by parseServersJSON (full override parity)
	// and losslessly reproduce the source table.
	var buf bytes.Buffer
	if err := dumpServersJSON(&buf, embeddedServers); err != nil {
		t.Fatalf("dumpServersJSON: %v", err)
	}
	got, err := parseServersJSON(buf.Bytes())
	if err != nil {
		t.Fatalf("parseServersJSON(dumped): %v\n%s", err, buf.String())
	}
	if len(got) != len(embeddedServers) {
		t.Fatalf("got %d regions, want %d", len(got), len(embeddedServers))
	}
	for region, want := range embeddedServers {
		if !reflect.DeepEqual(got[region], want) {
			t.Errorf("region %s mismatch:\n got %+v\nwant %+v", region, got[region], want)
		}
	}

	// Region order: RegionOrder first (NA before EU in the raw text).
	text := buf.String()
	if strings.Index(text, `"NA"`) > strings.Index(text, `"EU"`) {
		t.Error("dumped region order should list NA before EU")
	}

	// A dumped file with one surgical edit still loads (the primary
	// dump-and-edit workflow).
	edited := strings.Replace(text, "167.160.91.2", "10.0.0.9", 1)
	servers, err := parseServersJSON([]byte(edited))
	if err != nil {
		t.Fatalf("parseServersJSON(edited): %v", err)
	}
	var found bool
	for _, s := range servers["NA"] {
		if s.City == "San Jose" && s.Provider == "Dedicated.com" {
			found = s.Host == "10.0.0.9"
		}
	}
	if !found {
		t.Error("surgical host edit not reflected after reload")
	}
}
