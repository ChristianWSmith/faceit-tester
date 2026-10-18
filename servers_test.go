package main

import (
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
	// Trimmed to FACEIT-official locations: 21 NA + 26 EU + 12 SA + 10 SEA
	// + 5 OCE = 74.
	if total != 74 {
		t.Errorf("total embedded servers = %d, want 74", total)
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
		{"Chicago", "PS", "108.181.62.197"},
		{"Dallas", "PS", "108.181.54.9"},
		{"Chicago", "cdn77", "185.93.1.65"},
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
		"MARS": {{"Phobos", "test", "10.0.0.1"}},
		"NA":   {{"Chicago", "PS", "108.181.62.197"}},
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
	s := Server{"San Jose", "hz", "167.160.91.2"}
	if s.ID() != "San Jose.hz" {
		t.Errorf("ID() = %q, want %q", s.ID(), "San Jose.hz")
	}
	if s.DisplayName() != "San Jose.hz" {
		t.Errorf("DisplayName() = %q", s.DisplayName())
	}
}
