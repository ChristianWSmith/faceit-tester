package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// TestFilterDoesNotResizeWindow guards the regression where typing in
// the filter entry changed the window's minimum size (Fyne clamps the
// window UP to content MinSize and never back down, so any min bump
// while typing permanently resized the window and pushed the right
// edge — Test button, legend "?") out of place).
//
// Scenario mirrors the bug report: Virginia (and a wide city like
// Netherlands, for its long provider name) expanded, type "reli",
// then clear.
func TestFilterDoesNotResizeWindow(t *testing.T) {
	test.NewApp()
	u := &ui{
		prefs:    newTestPrefs(t),
		servers:  embeddedServers,
		regionBy: map[string]string{},
		state:    map[string]bool{},
		expanded: map[string]bool{},
		tree:     newResultsTree(),
	}
	for region, srvs := range embeddedServers {
		for _, s := range srvs {
			u.regionBy[s.ID()] = region
			u.state[s.ID()] = true
		}
	}
	// The repro: Virginia expanded + Netherlands expanded (wide label).
	u.expanded[cityNodeKey("NA", "Virginia")] = true
	u.expanded[cityNodeKey("EU", "Netherlands")] = true
	u.expanded[regionNodeKey("NA")] = true
	u.expanded[regionNodeKey("EU")] = true

	u.build()
	content := u.content()

	minEmpty := content.MinSize()

	u.filterEntry.SetText("reli") // fires OnChanged → applyFilter
	minFiltered := content.MinSize()

	u.filterEntry.SetText("") // clear → back to full tree
	minCleared := content.MinSize()

	t.Logf("empty:   %v", minEmpty)
	t.Logf("reli:    %v", minFiltered)
	t.Logf("cleared: %v", minCleared)

	if minFiltered.Width > minEmpty.Width || minFiltered.Height > minEmpty.Height {
		t.Errorf("typing grew the window min: %v -> %v", minEmpty, minFiltered)
	}
	if minCleared.Width > minEmpty.Width || minCleared.Height > minEmpty.Height {
		t.Errorf("clearing grew the window min: %v -> %v", minEmpty, minCleared)
	}
	if minFiltered.Width != minCleared.Width || minFiltered.Height != minCleared.Height {
		t.Errorf("type-then-clear did not return to baseline: %v vs %v", minFiltered, minCleared)
	}

	// A wider/other filter must not grow it either.
	u.filterEntry.SetText("google cloud")
	minOther := content.MinSize()
	t.Logf("google:  %v", minOther)
	if minOther.Width > minEmpty.Width || minOther.Height > minEmpty.Height {
		t.Errorf("filter 'google cloud' grew the window min: %v -> %v", minEmpty, minOther)
	}
}

// TestFilterFromCollapsedDoesNotGrowMin covers the fresh-launch state:
// everything collapsed (narrow tree content), then a filter force-expands
// a branch with a wide provider name — if the scroll's content width
// crosses the pane's floor, the split clamp shifts and the right pane
// overflows.
func TestFilterFromCollapsedDoesNotGrowMin(t *testing.T) {
	test.NewApp()
	u := &ui{
		prefs:    newTestPrefs(t),
		servers:  embeddedServers,
		regionBy: map[string]string{},
		state:    map[string]bool{},
		expanded: map[string]bool{}, // fresh launch: everything collapsed
		tree:     newResultsTree(),
	}
	for region, srvs := range embeddedServers {
		for _, s := range srvs {
			u.regionBy[s.ID()] = region
			u.state[s.ID()] = true
		}
	}
	u.build()
	content := u.content()
	minEmpty := content.MinSize()
	paneEmpty := u.treePane.MinSize()
	scrollEmpty := u.checksScroll.MinSize()

	// Longest provider name in the list, force-expands Netherlands.
	u.filterEntry.SetText("google cloud")
	minFilt := content.MinSize()
	paneFilt := u.treePane.MinSize()
	scrollFilt := u.checksScroll.MinSize()

	u.filterEntry.SetText("west") // different expansion set
	minWest := content.MinSize()
	paneWest := u.treePane.MinSize()

	u.filterEntry.SetText("")
	minBack := content.MinSize()

	t.Logf("empty:    content=%v pane=%v scroll=%v", minEmpty, paneEmpty, scrollEmpty)
	t.Logf("google:   content=%v pane=%v scroll=%v", minFilt, paneFilt, scrollFilt)
	t.Logf("west:     content=%v pane=%v", minWest, paneWest)
	t.Logf("back:     content=%v", minBack)

	if minFilt.Width > minEmpty.Width {
		t.Errorf("filter grew content min width: %v -> %v (pane %v -> %v, scroll %v -> %v)",
			minEmpty, minFilt, paneEmpty, paneFilt, scrollEmpty, scrollFilt)
	}
	if minWest.Width > minEmpty.Width {
		t.Errorf("'west' grew content min width: %v -> %v (pane %v -> %v)",
			minEmpty, minWest, paneEmpty, paneWest)
	}
	if minBack.Width > minEmpty.Width || minBack.Height > minEmpty.Height {
		t.Errorf("clearing overshot baseline: %v vs %v", minEmpty, minBack)
	}
}

// TestScrollMinNeverFollowsContent pins the root cause of the
// window-resizes-while-typing bug: with NewVScroll the tree's content
// width fed the scroll's MinSize (260 → 337 when "google cloud"
// force-expanded the Netherlands row), and the driver's EnsureMinSize
// then grew the OS window. NewScroll (ScrollBoth) must ignore content
// in both dimensions.
func TestScrollMinNeverFollowsContent(t *testing.T) {
	test.NewApp()
	u := &ui{
		prefs:    newTestPrefs(t),
		servers:  embeddedServers,
		regionBy: map[string]string{},
		state:    map[string]bool{},
		expanded: map[string]bool{},
		tree:     newResultsTree(),
	}
	for region, srvs := range embeddedServers {
		for _, s := range srvs {
			u.regionBy[s.ID()] = region
			u.state[s.ID()] = true
		}
	}
	u.build()
	base := u.checksScroll.MinSize()
	if base.Width != 260 {
		t.Fatalf("baseline scroll min width = %v, want 260", base.Width)
	}
	for _, filter := range []string{"google cloud", "reli", "west", "psychz", ""} {
		u.filterEntry.SetText(filter)
		if got := u.checksScroll.MinSize().Width; got != 260 {
			t.Errorf("filter %q: scroll min width = %v, want 260 (content fed the min)",
				filter, got)
		}
	}
}

// TestFilterHidesWholeBlocks guards the gap bug: a filtered-out city's
// entire block (including its indent wrapper) must be hidden — hiding
// only the header left the indent's spacer visible, stacking a few px of
// gap per hidden city between the region and the next visible one.
func TestFilterHidesWholeBlocks(t *testing.T) {
	test.NewApp()
	u := &ui{
		prefs:    newTestPrefs(t),
		servers:  embeddedServers,
		regionBy: map[string]string{},
		state:    map[string]bool{},
		expanded: map[string]bool{},
		tree:     newResultsTree(),
	}
	for region, srvs := range embeddedServers {
		for _, s := range srvs {
			u.regionBy[s.ID()] = region
			u.state[s.ID()] = true
		}
	}
	u.expanded[regionNodeKey("NA")] = true
	u.expanded[cityNodeKey("NA", "Virginia")] = true
	u.build()

	u.filterEntry.SetText("reli") // matches only Virginia.ReliableSite in NA

	na := u.trees["NA"]
	visibleCities := 0
	for city, ct := range na.cities {
		if city == "Virginia" {
			if !ct.block.Visible() {
				t.Errorf("Virginia block should be visible")
			}
			visibleCities++
			continue
		}
		if ct.block.Visible() {
			t.Errorf("%s block should be fully hidden, not just its header", city)
		}
	}
	if visibleCities != 1 {
		t.Errorf("visible NA cities = %d, want 1", visibleCities)
	}
	// Only visible children count toward the box min — a fully hidden
	// block contributes nothing (boxLayout skips !Visible objects).
	for _, region := range u.regionOrder {
		rt := u.trees[region]
		if region == "NA" {
			continue
		}
		if rt.block.Visible() {
			t.Errorf("region %s should be hidden by the filter", region)
		}
	}
}
