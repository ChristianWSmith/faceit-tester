package main

import (
	"reflect"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

func TestEditRowRoundTrip(t *testing.T) {
	rows := rowsFromTable(embeddedServers)
	table, err := tableFromRows(rows)
	if err != nil {
		t.Fatalf("tableFromRows: %v", err)
	}
	if !reflect.DeepEqual(table, embeddedServers) {
		t.Fatal("rowsFromTable → tableFromRows must reproduce the source table")
	}
}

func TestEditRowKeepsNumericStrings(t *testing.T) {
	s := Server{City: "A", Provider: "B", Host: "h", MaxPPS: 45, ProbeSize: 56}
	rows := rowsFromTable(map[string][]Server{"NA": {s}})
	row := rows["NA"][0]
	if row.MaxPPS != "45" || row.ProbeSize != "56" {
		t.Fatalf("numeric fields = %q/%q, want 45/56", row.MaxPPS, row.ProbeSize)
	}
	back, err := row.toServer()
	if err != nil {
		t.Fatalf("toServer: %v", err)
	}
	if !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip: got %#v want %#v", back, s)
	}
}

func TestTableFromRowsErrors(t *testing.T) {
	cases := []struct {
		name string
		rows map[string][]editRow
		want string
	}{
		{"half-typed int", map[string][]editRow{
			"NA": {{City: "A", Provider: "B", Host: "h", MaxPPS: "45x"}},
		}, "max pps"},
		{"negative probe size", map[string][]editRow{
			"NA": {{City: "A", Provider: "B", Host: "h", ProbeSize: "-1"}},
		}, "probe size"},
		{"no servers left", map[string][]editRow{
			"NA": {}, "EU": {},
		}, "at least one server"},
		{"missing host", map[string][]editRow{
			"NA": {{City: "A", Provider: "B"}},
		}, "missing city"},
		{"duplicate after edit", map[string][]editRow{
			"NA": {{City: "A", Provider: "B", Host: "h1"}, {City: "A", Provider: "B", Host: "h2"}},
		}, "duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tableFromRows(c.rows)
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

func TestGroupRowsByCity(t *testing.T) {
	rows := []editRow{{City: "A"}, {City: "B"}, {City: "A"}, {City: "C"}}
	groups := groupRowsByCity(rows)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if groups[0].City != "A" || groups[1].City != "B" || groups[2].City != "C" {
		t.Fatalf("group order = %v, want first-appearance A,B,C", []string{groups[0].City, groups[1].City, groups[2].City})
	}
	if !reflect.DeepEqual(groups[0].Indexes, []int{0, 2}) {
		t.Fatalf("A indexes = %v, want [0 2]", groups[0].Indexes)
	}
}

func TestNonNegativeInt(t *testing.T) {
	if err := nonNegativeInt(""); err != nil {
		t.Fatalf("empty should be valid (default): %v", err)
	}
	if err := nonNegativeInt(" 64 "); err != nil {
		t.Fatalf("padded number should parse: %v", err)
	}
	if err := nonNegativeInt("abc"); err == nil {
		t.Fatal("want error for junk")
	}
	if err := nonNegativeInt("-1"); err == nil {
		t.Fatal("want error for negative")
	}
}

func TestRemoveCityRows(t *testing.T) {
	rows := []editRow{{City: "A", Provider: "P1"}, {City: "B", Provider: "P2"}, {City: "A", Provider: "P3"}}
	out := removeCityRows(rows, "A")
	if len(out) != 1 || out[0].City != "B" || out[0].Provider != "P2" {
		t.Fatalf("removeCityRows(A) = %+v, want just the B row", out)
	}
	if got := removeCityRows(rows, "Missing"); len(got) != 3 {
		t.Fatalf("unknown city kept %d rows, want 3", len(got))
	}
}

// TestEditorDialogOpens is a construction smoke test: the rebuilt list
// (stretched city blocks, + Provider, delete-city/region buttons,
// Reset to Defaults, no Undo) must wire without a nil deref or Fyne
// misuse. Editing logic lives in the pure-function tests above.
func TestEditorDialogOpens(t *testing.T) {
	test.NewApp()
	u := &ui{
		prefs:    newTestPrefs(t),
		servers:  embeddedServers,
		regionBy: map[string]string{},
		state:    map[string]bool{},
		expanded: map[string]bool{},
	}
	for region, srvs := range embeddedServers {
		for _, s := range srvs {
			u.regionBy[s.ID()] = region
			u.state[s.ID()] = true
		}
	}
	u.build()
	u.window = test.NewWindow(u.content())
	u.onEditTap() // must not panic
}
