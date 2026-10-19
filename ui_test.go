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
// VBox(header, HBox(spacer, providersBox)).
func providersBoxOf(t *testing.T, obj fyne.CanvasObject) *fyne.Container {
	t.Helper()
	vbox, ok := obj.(*fyne.Container)
	if !ok || len(vbox.Objects) != 2 {
		t.Fatalf("buildCity returned %T, want 2-object VBox", obj)
	}
	hbox, ok := vbox.Objects[1].(*fyne.Container)
	if !ok || len(hbox.Objects) != 2 {
		t.Fatalf("indent wrapper = %T, want HBox(spacer, providers)", vbox.Objects[1])
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
	if len(u.headerRow.Objects) != 6 {
		t.Fatalf("got %d header cells, want 6", len(u.headerRow.Objects))
	}
	total := nameW + 5*metricColWidth + 5*theme.Padding()
	u.headerRow.Resize(fyne.NewSize(total, 40))

	padding := theme.Padding()
	wantX := float32(0)
	widths := []float32{nameW, metricColWidth, metricColWidth, metricColWidth, metricColWidth, metricColWidth}
	for i, child := range u.headerRow.Objects {
		if child.Position().X != wantX {
			t.Errorf("header cell %d x = %v, want %v", i, child.Position().X, wantX)
		}
		if child.Size().Width != widths[i] {
			t.Errorf("header cell %d width = %v, want %v", i, child.Size().Width, widths[i])
		}
		wantX += widths[i] + padding
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
