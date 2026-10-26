package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

// providersBoxOf digs the providers VBox out of a buildCity result:
// HBox(spacer, VBox(header, HBox(spacer, providersBox))) — the city
// block is indented as a whole so the filter can show/hide it without
// leaving a gap.
func providersBoxOf(t *testing.T, obj fyne.CanvasObject) *fyne.Container {
	t.Helper()
	outer, ok := obj.(*fyne.Container) // indent HBox
	if !ok || len(outer.Objects) != 2 {
		t.Fatalf("buildCity returned %T, want indent HBox with 2 objects", obj)
	}
	vbox, ok := outer.Objects[1].(*fyne.Container)
	if !ok || len(vbox.Objects) != 2 {
		t.Fatalf("city block = %T, want VBox(header, providers-indent)", outer.Objects[1])
	}
	hbox, ok := vbox.Objects[1].(*fyne.Container)
	if !ok || len(hbox.Objects) != 2 {
		t.Fatalf("providers indent = %T, want HBox(spacer, providers)", vbox.Objects[1])
	}
	providers, ok := hbox.Objects[1].(*fyne.Container)
	if !ok {
		t.Fatalf("providers box = %T, want VBox", hbox.Objects[1])
	}
	return providers
}

// TestBuildCityRestoresExpandedState guards against a restored-expanded
// city rendering ▾ while its provider checks stay hidden (an unconditional
// providersBox.Hide() after newExpander's Show used to cause exactly that).
func TestBuildCityRestoresExpandedState(t *testing.T) {
	test.NewApp() // expander SetText needs a started app for its renderer
	server := srv("Chicago", "Psychz Networks", "1.2.3.4")
	group := CityGroup{City: "Chicago", Servers: []Server{server}}

	expanded := &ui{
		state:    map[string]bool{server.ID(): true},
		expanded: map[string]bool{cityNodeKey("NA", "Chicago"): true},
	}
	rt := &regionTree{cities: map[string]*cityTree{}}
	expanded.trees = map[string]*regionTree{"NA": rt}
	providers := providersBoxOf(t, expanded.buildCity("NA", group, rt))
	if !providers.Visible() {
		t.Error("restored-expanded city: providers box is hidden (shows ▾ with no children)")
	}

	collapsed := &ui{
		state:    map[string]bool{server.ID(): true},
		expanded: map[string]bool{},
	}
	rt2 := &regionTree{cities: map[string]*cityTree{}}
	collapsed.trees = map[string]*regionTree{"NA": rt2}
	providers2 := providersBoxOf(t, collapsed.buildCity("NA", group, rt2))
	if providers2.Visible() {
		t.Error("collapsed city: providers box should stay hidden")
	}
}

// TestInvertSelectionRefreshesParents guards the invert bug where city
// and region checkboxes kept their pre-invert state (refreshChecksFromState
// only recomputed regions, leaving city tri-states stale).
func TestInvertSelectionRefreshesParents(t *testing.T) {
	test.NewApp()
	srvA := srv("Virginia", "Datacamp", "1.2.3.4")
	srvB := srv("Virginia", "OVH", "5.6.7.8")
	u := &ui{
		servers:     map[string][]Server{"NA": {srvA, srvB}},
		state:       map[string]bool{srvA.ID(): true, srvB.ID(): true},
		expanded:    map[string]bool{},
		trees:       map[string]*regionTree{},
		regionOrder: []string{"NA"},
	}
	u.buildTree()

	u.invertSelection()

	rt := u.trees["NA"]
	ct := rt.cities["Virginia"]
	for _, id := range ct.ids {
		if u.state[id] {
			t.Errorf("%s should be unchecked after invert", id)
		}
		if ct.providers[id].Checked {
			t.Errorf("%s leaf check should be unchecked after invert", id)
		}
	}
	if ct.check.Checked || ct.check.Partial {
		t.Errorf("city check = (checked=%v, partial=%v), want both false",
			ct.check.Checked, ct.check.Partial)
	}
	if rt.check.Checked || rt.check.Partial {
		t.Errorf("region check = (checked=%v, partial=%v), want both false",
			rt.check.Checked, rt.check.Partial)
	}

	// Invert back: every parent flips to fully checked.
	u.invertSelection()
	if !ct.check.Checked || ct.check.Partial {
		t.Errorf("city check after re-invert = (checked=%v, partial=%v), want checked",
			ct.check.Checked, ct.check.Partial)
	}
	if !rt.check.Checked || rt.check.Partial {
		t.Errorf("region check after re-invert = (checked=%v, partial=%v), want checked",
			rt.check.Checked, rt.check.Partial)
	}
}

func TestNameColumnWidth(t *testing.T) {
	test.NewApp() // theme + font measurement
	// Empty results keep the floor width.
	if got := nameColumnWidth(nil); got != minNameColWidth {
		t.Errorf("empty rows width = %v, want floor %v", got, minNameColWidth)
	}
	rows := [][]tableCell{
		{{text: "▾ NA (12)", bold: true}},
		{{text: "        Chicago.Psychz Networks"}},
	}
	wide := nameColumnWidth(rows)
	if wide <= minNameColWidth {
		t.Errorf("long-name width = %v, want > floor %v", wide, minNameColWidth)
	}
	// Narrow content never shrinks below the floor.
	narrow := nameColumnWidth([][]tableCell{{{text: "x"}}})
	if narrow != minNameColWidth {
		t.Errorf("narrow width = %v, want floor %v", narrow, minNameColWidth)
	}
}

// TestHeaderCellsAlignToColumns guards the header/table alignment: the
// Table renderer inserts SizeNamePadding between columns (findX), so the
// header HBox must use the same inter-cell gaps and no leading offset,
// or every column drifts relative to the body.
func TestHeaderCellsAlignToColumns(t *testing.T) {
	test.NewApp()
	u := &ui{}
	u.headerRow = container.NewHBox()
	nameW := float32(210)
	u.rebuildHeader(nameW)
	// Six aligned column cells, then a spacer + the "?" legend button.
	if len(u.headerRow.Objects) != 8 {
		t.Fatalf("got %d header children, want 8 (6 cells + spacer + legend)", len(u.headerRow.Objects))
	}
	total := nameW + 5*metricColWidth + 5*theme.Padding()
	u.headerRow.Resize(fyne.NewSize(total, 40))

	padding := theme.Padding()
	wantX := float32(0)
	widths := []float32{nameW, metricColWidth, metricColWidth, metricColWidth, metricColWidth, metricColWidth}
	for i, child := range u.headerRow.Objects[:6] {
		if child.Position().X != wantX {
			t.Errorf("header cell %d x = %v, want %v", i, child.Position().X, wantX)
		}
		if child.Size().Width != widths[i] {
			t.Errorf("header cell %d width = %v, want %v", i, child.Size().Width, widths[i])
		}
		wantX += widths[i] + padding
	}

	// With slack in the row the spacer absorbs it all and the legend
	// pins to the right edge.
	wide := total + 150
	u.headerRow.Resize(fyne.NewSize(wide, 40))
	legend := u.headerRow.Objects[7]
	if got, want := legend.Position().X+legend.Size().Width, wide; got < want-1 || got > want+1 {
		t.Errorf("legend right edge = %v, want ~%v (pinned to row edge)", got, want)
	}
}

func TestIdentifyScrollHeight(t *testing.T) {
	// Floor covers the happy-path report (word wrap makes it taller than
	// the logical line count suggests).
	if got := identifyScrollHeight("3 short lines\nb\nc", 70); got != 160 {
		t.Errorf("short report height = %v, want floor 160", got)
	}
	if got := identifyScrollHeight(strings.Repeat("x\n", 9), 0); got != 220 {
		t.Errorf("10-line height = %v, want 220", got)
	}
	// Measured (wrapped) height wins when larger than the estimate.
	if got := identifyScrollHeight("a\nb\nc", 200); got != 200 {
		t.Errorf("measured override = %v, want 200", got)
	}
	if got := identifyScrollHeight(strings.Repeat("x\n", 200), 0); got != 320 {
		t.Errorf("tall height = %v, want cap 320", got)
	}
}
