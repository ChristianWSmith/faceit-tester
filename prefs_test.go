package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestAggregate(t *testing.T) {
	tests := []struct {
		name     string
		states   []bool
		wantAll  bool
		wantSome bool
	}{
		{"empty", nil, true, false},
		{"all true", []bool{true, true, true}, true, true},
		{"all false", []bool{false, false}, false, false},
		{"mixed", []bool{true, false, true}, false, true},
		{"single true", []bool{true}, true, true},
		{"single false", []bool{false}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all, some := aggregate(tt.states)
			if all != tt.wantAll || some != tt.wantSome {
				t.Errorf("aggregate(%v) = (%v, %v), want (%v, %v)",
					tt.states, all, some, tt.wantAll, tt.wantSome)
			}
		})
	}
}

func TestAggregateCities(t *testing.T) {
	tests := []struct {
		name     string
		cities   []CityState
		wantAll  bool
		wantSome bool
	}{
		{"empty", nil, true, false},
		{"all on", []CityState{{All: true}, {All: true}}, true, true},
		{"one partial", []CityState{{All: true}, {Some: true}}, false, true},
		{"one off rest on", []CityState{{All: true}, {}, {All: true}}, false, true},
		{"all off", []CityState{{}, {}}, false, false},
		{"partial and off", []CityState{{Some: true}, {}}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all, some := aggregateCities(tt.cities)
			if all != tt.wantAll || some != tt.wantSome {
				t.Errorf("aggregateCities(%v) = (%v, %v), want (%v, %v)",
					tt.cities, all, some, tt.wantAll, tt.wantSome)
			}
		})
	}
}

func TestStateFromDisabledDefaultsAllChecked(t *testing.T) {
	state := stateFromDisabled(embeddedServers, nil)
	if len(state) != 66 {
		t.Fatalf("state has %d entries, want 66", len(state))
	}
	for id, checked := range state {
		if !checked {
			t.Errorf("server %q default unchecked, want checked", id)
		}
	}
}

func TestStateFromDisabledIgnoresUnknownIDs(t *testing.T) {
	// Simulates: user had an override with Custom.foo, unchecked it, then
	// deleted the override. The stale ID must be ignored, not crash or
	// disable anything.
	state := stateFromDisabled(embeddedServers, []string{
		"Custom.foo",  // from deleted override
		"Gone.Region", // from an older version
		"Chicago.Psychz Networks",
	})
	if state["Custom.foo"] {
		t.Error("unknown ID should not appear in state")
	}
	if state["Chicago.Psychz Networks"] {
		t.Error("Chicago.Psychz Networks should be disabled")
	}
	// Everything else stays checked.
	checked := 0
	for _, v := range state {
		if v {
			checked++
		}
	}
	if checked != 65 {
		t.Errorf("checked = %d, want 65", checked)
	}
}

func TestDisabledFromStatePrunesStaleIDs(t *testing.T) {
	// Save-side: state map containing a stale key (e.g. carried in memory
	// across an override reload) must not leak into the persisted list.
	state := stateFromDisabled(embeddedServers, []string{"Chicago.Psychz Networks"})
	state["Custom.foo"] = false // stale, not in embeddedServers

	disabled := disabledFromState(embeddedServers, state)
	if !reflect.DeepEqual(disabled, []string{"Chicago.Psychz Networks"}) {
		t.Errorf("disabled = %v, want [Chicago.Psychz Networks]", disabled)
	}
}

func TestDisabledRoundTrip(t *testing.T) {
	state := stateFromDisabled(embeddedServers, []string{
		"Chicago.Psychz Networks", "San Jose.Dedicated.com", "Singapore.AWS",
	})
	disabled := disabledFromState(embeddedServers, state)
	restored := stateFromDisabled(embeddedServers, disabled)
	if !reflect.DeepEqual(state, restored) {
		t.Error("round-trip through disabledFromState/stateFromDisabled mismatch")
	}
}

func TestDisabledFromStateOrder(t *testing.T) {
	// Persisted list follows regionNames order for stability.
	state := stateFromDisabled(embeddedServers, []string{
		"Singapore.AWS", "Chicago.Psychz Networks", "France.OVH",
	})
	disabled := disabledFromState(embeddedServers, state)
	want := []string{"Chicago.Psychz Networks", "France.OVH", "Singapore.AWS"}
	if !reflect.DeepEqual(disabled, want) {
		sort.Strings(disabled)
		sort.Strings(want)
		if !reflect.DeepEqual(disabled, want) {
			t.Errorf("disabled = %v, want %v", disabled, want)
		}
	}
}

func TestStateFromDisabledWithOverrideSet(t *testing.T) {
	// Override active: prefs written against the override only apply to
	// override IDs; embedded-only IDs from an earlier session are ignored.
	override := map[string][]Server{
		"TEST": {srv("CityA", "prov1", "10.0.0.1"), srv("CityA", "prov2", "10.0.0.2")},
	}
	state := stateFromDisabled(override, []string{
		"CityA.prov1", // in override — applies
		"Chicago.PS",  // embedded-only — ignored
	})
	if state["CityA.prov1"] {
		t.Error("CityA.prov1 should be disabled")
	}
	if !state["CityA.prov2"] {
		t.Error("CityA.prov2 should stay checked")
	}
	if _, exists := state["Chicago.PS"]; exists {
		t.Error("embedded-only ID must not appear in override state")
	}
}

func TestExpandedFromListDefaultsCollapsed(t *testing.T) {
	expanded := expandedFromList(embeddedServers, nil)
	if len(expanded) != 0 {
		t.Errorf("empty list should mean all collapsed, got %v", expanded)
	}
}

func TestExpandedFromListKeepsValidKeys(t *testing.T) {
	expanded := expandedFromList(embeddedServers, []string{"NA", "NA/Chicago", "EU"})
	if !expanded["NA"] || !expanded["NA/Chicago"] || !expanded["EU"] {
		t.Errorf("valid keys dropped: %v", expanded)
	}
}

func TestExpandedFromListIgnoresStaleKeys(t *testing.T) {
	// Simulates: nodes persisted under an override, then the override was
	// deleted or the region renamed. Stale keys must be ignored.
	expanded := expandedFromList(embeddedServers, []string{
		"TEST",           // region not in embedded set
		"NA/GoneCity",    // city not in NA
		"OldRegion/City", // both gone
		"NA",             // still valid
	})
	if len(expanded) != 1 || !expanded["NA"] {
		t.Errorf("stale keys not filtered: %v", expanded)
	}
}

func TestExpandedRoundTrip(t *testing.T) {
	expanded := map[string]bool{
		"NA":            true,
		"NA/Chicago":    true,
		"EU":            true,
		"EU/Germany":    true,
		"SEA/Singapore": true,
	}
	list := expandedToList(embeddedServers, expanded)
	restored := expandedFromList(embeddedServers, list)
	if !reflect.DeepEqual(expanded, restored) {
		t.Errorf("round-trip mismatch: %v → %v → %v", expanded, list, restored)
	}
}

func TestExpandedToListOrder(t *testing.T) {
	// Stable region/city order regardless of map iteration.
	list := expandedToList(embeddedServers, map[string]bool{
		"SEA/Singapore": true,
		"NA":            true,
		"NA/Chicago":    true,
	})
	want := []string{"NA", "NA/Chicago", "SEA/Singapore"}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("expandedToList = %v, want %v", list, want)
	}
}
