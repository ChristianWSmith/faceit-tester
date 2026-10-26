package main

import (
	"strings"
	"testing"
)

func idsFor(servers map[string][]Server, region string) []string {
	var ids []string
	for _, s := range servers[region] {
		ids = append(ids, s.ID())
	}
	return ids
}

func countChecked(state map[string]bool) int {
	n := 0
	for _, v := range state {
		if v {
			n++
		}
	}
	return n
}

func allServerCount(servers map[string][]Server) int {
	n := 0
	for _, srvs := range servers {
		n += len(srvs)
	}
	return n
}

func TestParseTestSpecRegionAll(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"NA":"all"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := countChecked(state), len(embeddedServers["NA"]); got != want {
		t.Fatalf("checked = %d, want %d", got, want)
	}
	if len(state) != allServerCount(embeddedServers) {
		t.Fatalf("state has %d entries, want every server (%d)", len(state), allServerCount(embeddedServers))
	}
	for _, id := range idsFor(embeddedServers, "EU") {
		if state[id] {
			t.Errorf("%s should be unchecked", id)
		}
	}
}

func TestParseTestSpecAllRegions(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"all":"all"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := countChecked(state), allServerCount(embeddedServers); got != want {
		t.Fatalf("checked = %d, want %d", got, want)
	}
}

func TestParseTestSpecCity(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"NA":{"Virginia":"all"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, s := range embeddedServers["NA"] {
		if s.City == "Virginia" {
			want++
			if !state[s.ID()] {
				t.Errorf("%s should be checked", s.ID())
			}
		}
	}
	if got := countChecked(state); got != want {
		t.Fatalf("checked = %d, want %d", got, want)
	}
}

func TestParseTestSpecSingleProvider(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"NA":{"Virginia":"Datacamp"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := countChecked(state); got != 1 {
		t.Fatalf("checked = %d, want 1", got)
	}
	if !state["Virginia.Datacamp"] {
		t.Error("Virginia.Datacamp should be checked")
	}
}

func TestParseTestSpecProviderListRejectsUnknown(t *testing.T) {
	// Unknown names in a list are errors, not silent skips — the spec
	// should not partially apply without the user knowing.
	_, err := parseTestSpec(embeddedServers, `{"NA":{"Virginia":["OVH","Datacamp"]}}`)
	if err == nil {
		t.Fatal("expected error for OVH not present in Virginia")
	}
	if !strings.Contains(err.Error(), `unknown provider "OVH"`) {
		t.Fatalf("error %q does not mention unknown OVH", err.Error())
	}
}

func TestParseTestSpecProviderListAllValid(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"NA":{"Chicago":["Psychz Networks","Datacamp","NFOservers"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"Chicago.Psychz Networks", "Chicago.Datacamp", "Chicago.NFOservers"} {
		if !state[id] {
			t.Errorf("%s should be checked", id)
		}
	}
	if got := countChecked(state); got != 3 {
		t.Fatalf("checked = %d, want 3", got)
	}
}

func TestParseTestSpecCaseInsensitive(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"na":{"virginia":"datacamp"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !state["Virginia.Datacamp"] {
		t.Error("Virginia.Datacamp should be checked")
	}
	if got := countChecked(state); got != 1 {
		t.Fatalf("checked = %d, want 1", got)
	}
}

func TestParseTestSpecMultiRegion(t *testing.T) {
	state, err := parseTestSpec(embeddedServers, `{"NA":"all","EU":{"Germany":"all"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := len(embeddedServers["NA"])
	for _, s := range embeddedServers["EU"] {
		if s.City == "Germany" {
			want++
		}
	}
	if got := countChecked(state); got != want {
		t.Fatalf("checked = %d, want %d", got, want)
	}
}

func TestParseTestSpecCityAllAcrossCities(t *testing.T) {
	// "all" as a city key within one region selects every city there.
	state, err := parseTestSpec(embeddedServers, `{"OCE":{"all":"OVH"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !state["Sydney.OVH"] {
		t.Error("Sydney.OVH should be checked")
	}
	if got := countChecked(state); got != 1 {
		t.Fatalf("checked = %d, want 1", got)
	}
}

func TestParseTestSpecErrors(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string // substring of the error message
	}{
		{"malformed JSON", `{NA:all}`, "invalid JSON"},
		{"top-level string", `"all"`, "must be a JSON object"},
		{"top-level array", `["NA"]`, "must be a JSON object"},
		{"empty object", `{}`, "selects no servers"},
		{"unknown region", `{"XX":"all"}`, `unknown region "XX"`},
		{"unknown city", `{"NA":{"Atlantis":"all"}}`, `unknown city "Atlantis"`},
		{"unknown provider", `{"NA":{"Virginia":"Nope"}}`, `unknown provider "Nope"`},
		{"unknown provider in list", `{"NA":{"Virginia":["Datacamp","Nope"]}}`, `unknown provider "Nope"`},
		{"region string not all", `{"NA":"everything"}`, "must be \"all\" or a city object"},
		{"all key with city object", `{"all":{"NA":"all"}}`, `"all" region key only takes the value "all"`},
		{"city value wrong type", `{"NA":{"Virginia":true}}`, "must be \"all\", a provider name, or a list"},
		{"region value wrong type", `{"NA":5}`, "must be \"all\" or a city object"},
		{"empty provider list", `{"NA":{"Virginia":[]}}`, "provider list is empty"},
		{"non-string list entry", `{"NA":{"Virginia":[1]}}`, "entries must be strings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTestSpec(embeddedServers, tc.spec)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestExpansionForSelection(t *testing.T) {
	// Regions expand so the selection's tri-state city rows are visible;
	// cities stay collapsed — providers are a power-user detail (players
	// pick a location, not a provider).
	state, err := parseTestSpec(embeddedServers, `{"NA":{"Virginia":"Datacamp"}}`)
	if err != nil {
		t.Fatal(err)
	}
	keys := expansionForSelection(embeddedServers, state)
	if !keys["NA"] {
		t.Error("region NA should be expanded")
	}
	if keys["NA/Virginia"] {
		t.Error("city NA/Virginia should stay collapsed")
	}
	if len(keys) != 1 {
		t.Errorf("keys = %v, want exactly NA", keys)
	}
}

func TestQuoteWinArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{`has"quote`, `"has\"quote"`},
		{"", `""`},
		{"two words", `"two words"`},
		{`trailing\`, `trailing\`}, // no quoting needed: lone backslash is fine unquoted
		{`C:\path with space\`, `"C:\path with space\\"`},
		{`say "hi"`, `"say \"hi\""`},
		{`a\\`, `a\\`}, // unquoted: backslashes are literal when no quotes follow
	}
	for _, tc := range cases {
		if got := quoteWinArg(tc.in); got != tc.want {
			t.Errorf("quoteWinArg(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestJoinWinArgs(t *testing.T) {
	got := joinWinArgs([]string{`{"NA":"all"}`, "plain"})
	want := `"{\"NA\":\"all\"}" plain`
	if got != want {
		t.Errorf("joinWinArgs = %s, want %s", got, want)
	}
	if got := joinWinArgs(nil); got != "" {
		t.Errorf("joinWinArgs(nil) = %q, want empty", got)
	}
}

// TestParseTestSpecOverrideShape sanity-checks that parsing works against a
// non-embedded table shape (override files can add regions).
func TestParseTestSpecOverrideShape(t *testing.T) {
	override := map[string][]Server{
		"XX": {srv("Testville", "TestHost", "10.0.0.1")},
	}
	state, err := parseTestSpec(override, `{"xx":{"testville":"testhost"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !state["Testville.TestHost"] {
		t.Error("Testville.TestHost should be checked")
	}
}
