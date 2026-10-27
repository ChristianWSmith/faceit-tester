package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// TestPaneRendersFullRows pins the pane↔model contract: addOutcome
// flattens into 6-cell rows (resultRow's full column set), regions
// start expanded with cities collapsed, and error leaves render the
// short "error" cell in the loss column.
func TestPaneRendersFullRows(t *testing.T) {
	test.NewApp()
	p := newResultsPane(paneConfig{})
	ok := ServerOutcome{
		Server: Server{City: "San Jose", Provider: "Dedicated.com", Host: "203.0.113.7"},
		Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0.31,
			Median: 24100 * time.Microsecond, P1: 20000 * time.Microsecond,
			P99: 40200 * time.Microsecond, Jitter: 1200 * time.Microsecond,
		},
	}
	p.addOutcome("NA", ok)

	// Region + city rows only (cities start collapsed).
	if len(p.rows) != 2 {
		t.Fatalf("rows = %d, want 2 (region + collapsed city)", len(p.rows))
	}
	for i, row := range p.rows {
		if len(row) != 6 {
			t.Fatalf("row %d cells = %d, want 6", i, len(row))
		}
	}
	p.tree.regions[0].cities[0].expanded = true
	p.reflatten()
	if len(p.rows) != 3 {
		t.Fatalf("rows after expand = %d, want 3 (region + city + leaf)", len(p.rows))
	}
	leaf := p.rows[2]
	if !strings.Contains(leaf[0].text, "San Jose.Dedicated.com") {
		t.Errorf("leaf name = %q, want the display name", leaf[0].text)
	}
	if want := "20.0"; leaf[1].text != want {
		t.Errorf("p1 cell = %q, want %q", leaf[1].text, want)
	}
	if want := "24.1"; leaf[2].text != want {
		t.Errorf("med cell = %q, want %q", leaf[2].text, want)
	}
	if want := "40.2"; leaf[3].text != want {
		t.Errorf("p99 cell = %q, want %q", leaf[3].text, want)
	}
	if want := "1.2"; leaf[4].text != want {
		t.Errorf("jitter cell = %q, want %q", leaf[4].text, want)
	}
	if want := "0.31%"; leaf[5].text != want {
		t.Errorf("loss cell = %q, want %q", leaf[5].text, want)
	}

	// Error rows: resultRow's error branch is name + dashes + error.
	p.setTree(newResultsTree())
	p.addOutcome("NA", ServerOutcome{
		Server: Server{City: "Virginia", Provider: "NForce", Host: "198.51.100.4"},
		Result: PingResult{Err: errors.New("boom")},
	})
	p.tree.regions[0].cities[0].expanded = true
	p.reflatten()
	errLeaf := p.rows[2]
	if len(errLeaf) != 6 {
		t.Fatalf("error leaf cells = %d, want 6", len(errLeaf))
	}
	if errLeaf[5].text != "error" {
		t.Errorf("error cell = %q, want error in the loss column", errLeaf[5].text)
	}
	if errLeaf[1].text != "—" || errLeaf[2].text != "—" {
		t.Errorf("metric cells = (%q, %q), want (—, —)", errLeaf[1].text, errLeaf[2].text)
	}
}

// TestPaneNameWidthReserved pins the no-reflow contract: setTree
// reserves the Server column against a fully-expanded preview of the
// tree (and restores the original expansion state), so revealing a
// branch with a long name never re-flows the table underneath the
// player — the party summary's tree arrives collapsed but fully known.
func TestPaneNameWidthReserved(t *testing.T) {
	test.NewApp()
	p := newResultsPane(paneConfig{})
	tree := newResultsTree()
	tree.addOutcome("NA", ServerOutcome{
		Server: Server{City: "San Jose", Provider: "Verylongprovidername", Host: "203.0.113.7"},
		Result: PingResult{Sent: 640, Recv: 640, Median: 20 * time.Millisecond},
	})
	p.setTree(tree)

	if p.tree.regions[0].cities[0].expanded {
		t.Fatal("reservation leaked expansion state (city should start collapsed)")
	}
	if p.nameW <= minNameColWidth {
		t.Fatalf("reserved width = %v, want > floor %v (collapsed leaf unseen by refresh)", p.nameW, minNameColWidth)
	}
	w := p.nameW
	p.tree.regions[0].cities[0].expanded = true
	p.reflatten()
	if p.nameW != w {
		t.Errorf("nameW re-flowed on expand: reserved %v → %v", w, p.nameW)
	}
}

// TestPaneSetTreeSyncsTableColumn pins the header↔body contract: the
// Server-column width must reach the TABLE as well as the header. A
// "no change" short-circuit once left the table at its old width while
// the header reserved more — providers spilled past the column and
// every header cell drifted (the party summary's fresh pane reserved
// >floor immediately, which is how it surfaced). widget.Table exposes
// no getter, so the test peeks at its unexported columnWidths map.
func TestPaneSetTreeSyncsTableColumn(t *testing.T) {
	test.NewApp()
	p := newResultsPane(paneConfig{})
	if got, want := tableColWidth(t, p.table, 0), float32(minNameColWidth); got != want {
		t.Fatalf("initial table col0 = %v, want %v", got, want)
	}

	tree := newResultsTree()
	tree.addOutcome("NA", ServerOutcome{
		Server: Server{City: "San Jose", Provider: "Verylongprovidername", Host: "203.0.113.7"},
		Result: PingResult{Sent: 640, Recv: 640, Median: 20 * time.Millisecond},
	})
	p.setTree(tree)

	if p.nameW <= minNameColWidth {
		t.Fatalf("nameW = %v, want the reservation above the floor", p.nameW)
	}
	if got := tableColWidth(t, p.table, 0); got != p.nameW {
		t.Errorf("table col0 = %v, want nameW %v (first column synced to the reservation)", got, p.nameW)
	}
	// The header's first cell must claim exactly the same width.
	if got := p.header.Objects[0].Size().Width; got != p.nameW {
		t.Errorf("header cell width = %v, want nameW %v", got, p.nameW)
	}
	// Re-flattening stays in sync (no drift on expand).
	p.tree.regions[0].cities[0].expanded = true
	p.reflatten()
	if got := tableColWidth(t, p.table, 0); got != p.nameW {
		t.Errorf("after expand, table col0 = %v, want %v", got, p.nameW)
	}
}

// tableColWidth reads widget.Table's unexported per-column width map.
func tableColWidth(t *testing.T, tbl *widget.Table, col int) float32 {
	t.Helper()
	v := reflect.ValueOf(tbl).Elem().FieldByName("columnWidths")
	if !v.IsValid() {
		t.Fatal("widget.Table lost its columnWidths field — update this test")
	}
	m := v.MapIndex(reflect.ValueOf(col))
	if !m.IsValid() {
		t.Fatalf("column %d has no explicit width", col)
	}
	return float32(m.Float())
}
