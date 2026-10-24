package main

import (
	"reflect"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// newTestPrefs returns a fresh in-memory Preferences store (test.NewApp
// has no unique ID, so nothing is ever written to the real config dir).
func newTestPrefs(t *testing.T) fyne.Preferences {
	t.Helper()
	return test.NewApp().Preferences()
}

// customList returns a deep copy of the built-ins with one host changed —
// a table that clearly differs from embeddedServers.
func customList(t *testing.T) map[string][]Server {
	t.Helper()
	custom := cloneServers(embeddedServers)
	custom["NA"][0].Host = "10.0.0.9"
	return custom
}

func TestLoadServersBuiltinsWithoutCustomList(t *testing.T) {
	servers, err := loadServers(newTestPrefs(t))
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	if !reflect.DeepEqual(servers, embeddedServers) {
		t.Fatal("want embedded built-ins with no stored custom list")
	}
	if overrideActive(newTestPrefs(t)) {
		t.Fatal("overrideActive with no stored custom list")
	}
}

func TestCustomListRoundTrip(t *testing.T) {
	prefs := newTestPrefs(t)
	custom := customList(t)
	if err := saveOverride(prefs, custom); err != nil {
		t.Fatalf("saveOverride: %v", err)
	}
	if !overrideActive(prefs) {
		t.Fatal("custom list not active after save")
	}
	got, err := loadServers(prefs)
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	if !reflect.DeepEqual(got, custom) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, custom)
	}
}

func TestSaveOverrideDropsBuiltinsMatch(t *testing.T) {
	prefs := newTestPrefs(t)
	if err := saveOverride(prefs, customList(t)); err != nil {
		t.Fatalf("saveOverride(custom): %v", err)
	}
	// Editing back to a table that equals the built-ins must remove the
	// key, not store a byte-identical copy.
	if err := saveOverride(prefs, cloneServers(embeddedServers)); err != nil {
		t.Fatalf("saveOverride(builtins): %v", err)
	}
	if overrideActive(prefs) {
		t.Fatal("custom list still active after saving a built-ins-equal table")
	}
	got, err := loadServers(prefs)
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	if !reflect.DeepEqual(got, embeddedServers) {
		t.Fatal("want built-ins after dropping")
	}
}

func TestLoadServersMalformedKeepsBuiltins(t *testing.T) {
	prefs := newTestPrefs(t)
	prefs.SetString(prefsServersOverrideKey, "{not json")
	servers, err := loadServers(prefs)
	if err == nil {
		t.Fatal("want error for malformed stored list")
	}
	if !reflect.DeepEqual(servers, embeddedServers) {
		t.Fatal("malformed stored list must fall back to built-ins")
	}
	// The broken key must stay "active" so the editor can offer Drop.
	if !overrideActive(prefs) {
		t.Fatal("broken stored list must remain droppable")
	}
}

func TestDropOverride(t *testing.T) {
	prefs := newTestPrefs(t)
	if err := saveOverride(prefs, customList(t)); err != nil {
		t.Fatalf("saveOverride: %v", err)
	}
	dropOverride(prefs)
	if overrideActive(prefs) {
		t.Fatal("custom list still active after drop")
	}
	got, err := loadServers(prefs)
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	if !reflect.DeepEqual(got, embeddedServers) {
		t.Fatal("want built-ins after drop")
	}
}

func TestCloneServersIndependence(t *testing.T) {
	origLen := len(embeddedServers["NA"])
	clone := cloneServers(embeddedServers)
	clone["NA"][0].Host = "mutated"
	clone["EU"] = append(clone["EU"], srv("Testville", "TestHost", "192.0.2.1"))
	if embeddedServers["NA"][0].Host == "mutated" {
		t.Fatal("clone shares the built-in slice")
	}
	if len(embeddedServers["NA"]) != origLen {
		t.Fatal("clone append grew the built-in slice")
	}
	for _, s := range embeddedServers["EU"] {
		if s.City == "Testville" {
			t.Fatal("clone leaked a new server into the built-ins")
		}
	}
}

func TestValidateServersEmbeddedPasses(t *testing.T) {
	if err := validateServers(embeddedServers); err != nil {
		t.Fatalf("embedded servers must validate: %v", err)
	}
}

func TestValidateServersRejects(t *testing.T) {
	cases := []struct {
		name string
		srvs map[string][]Server
		want string
	}{
		{"duplicate City.Provider", map[string][]Server{
			"NA": {srv("X", "Y", "192.0.2.1"), srv("X", "Y", "192.0.2.2")},
		}, "duplicate"},
		{"slash in city", map[string][]Server{
			"NA": {srv("A/B", "Y", "192.0.2.1")},
		}, "must not contain"},
		{"slash in region", map[string][]Server{
			"A/B": {srv("A", "Y", "192.0.2.1")},
		}, "must not contain"},
		{"empty region", map[string][]Server{
			"": {srv("A", "Y", "192.0.2.1")},
		}, "region name"},
		{"missing host", map[string][]Server{
			"NA": {srv("A", "Y", "")},
		}, "missing city"},
		{"negative max pps", map[string][]Server{
			"NA": {{City: "A", Provider: "Y", Host: "h", MaxPPS: -1}},
		}, "max_pps"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateServers(c.srvs)
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}
