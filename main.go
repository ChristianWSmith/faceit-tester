package main

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

//go:embed icon.png
var iconData []byte

const appID = "com.christianws.faceit-tester"

// CS2 telemetry HUD default thresholds. Values strictly above these render
// red; at-or-below render green.
const (
	pingThreshold   = 100 * time.Millisecond
	jitterThreshold = 10 * time.Millisecond
	lossThreshold   = 2.0 // percent
)

// tableCell is one results-table cell: display text plus the label
// importance used for green/red threshold coloring.
type tableCell struct {
	text string
	imp  widget.Importance
	bold bool
}

// regionTree holds the widgets for one region row and its cities.
type regionTree struct {
	check  *widget.Check
	cities map[string]*cityTree
	order  []string
}

// cityTree holds the widgets for one city row and its providers.
type cityTree struct {
	check     *widget.Check
	providers map[string]*widget.Check // server ID → check
	ids       []string                 // server IDs in display order
}

type ui struct {
	window fyne.Window
	prefs  fyne.Preferences

	checksScroll *container.Scroll
	testBtn      *widget.Button
	exportBtn    *widget.Button
	statusLabel  *widget.Label
	table        *widget.Table

	servers  map[string][]Server
	state    map[string]bool   // server ID → checked
	expanded map[string]bool   // tree node key → expanded
	regionBy map[string]string // server ID → region name

	trees        map[string]*regionTree
	regionOrder  []string
	allChecks    []*widget.Check
	allExpanders []*widget.Button

	// syncing guards tri-state updates: bulk SetChecked calls fire
	// OnChanged, which must not re-enter the cascade.
	syncing bool

	rows    [][]tableCell
	results []ServerOutcome // full outcomes for CSV export
	running bool
	cancel  context.CancelFunc
}

func main() {
	a := app.NewWithID(appID)
	a.SetIcon(fyne.NewStaticResource("icon.png", iconData))
	w := a.NewWindow("FACEIT Connection Tester")
	w.Resize(fyne.NewSize(960, 600))

	servers := loadServers()
	prefs := a.Preferences()
	regionBy := map[string]string{}
	for region, srvs := range servers {
		for _, s := range srvs {
			regionBy[s.ID()] = region
		}
	}
	u := &ui{
		window:   w,
		prefs:    prefs,
		servers:  servers,
		regionBy: regionBy,
		state:    stateFromDisabled(servers, prefs.StringList(prefsDisabledKey)),
		expanded: expandedFromList(servers, prefs.StringList(prefsExpandedKey)),
	}
	u.build()
	w.SetContent(u.content())
	w.ShowAndRun()
}

func (u *ui) build() {
	u.checksScroll = container.NewVScroll(u.buildTree())
	u.checksScroll.SetMinSize(fyne.NewSize(260, 0))

	u.statusLabel = widget.NewLabel("Select servers, then press Test.")
	u.statusLabel.Wrapping = fyne.TextWrapWord

	u.table = widget.NewTable(
		func() (int, int) { return len(u.rows), 6 },
		func() fyne.CanvasObject { return widget.NewLabel("0000.0") },
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			label := cell.(*widget.Label)
			if id.Row < len(u.rows) && id.Col < len(u.rows[id.Row]) {
				c := u.rows[id.Row][id.Col]
				label.Importance = c.imp
				label.TextStyle = fyne.TextStyle{Bold: c.bold}
				label.SetText(c.text)
			} else {
				label.Importance = widget.MediumImportance
				label.TextStyle = fyne.TextStyle{}
				label.SetText("")
			}
		},
	)
	colWidths := []float32{170, 70, 70, 70, 70, 70}
	for i, w := range colWidths {
		u.table.SetColumnWidth(i, w)
	}

	u.testBtn = widget.NewButton("Test", u.onTestTap)
	u.testBtn.Importance = widget.SuccessImportance // green = start

	u.exportBtn = widget.NewButtonWithIcon("Export CSV", theme.DocumentSaveIcon(), u.onExportTap)
	u.exportBtn.Disable()

	if !isElevated() {
		u.testBtn.Disable()
		u.statusLabel.SetText("Run this application as administrator to test (required on Windows).")
	}
}

func (u *ui) content() fyne.CanvasObject {
	colHeaders := []struct {
		text  string
		width float32
	}{
		{"Server", 170}, {"Avg ms", 70}, {"Min", 70}, {"Max", 70}, {"Jitter", 70}, {"Loss", 70},
	}
	headerRow := container.NewHBox()
	for _, h := range colHeaders {
		label := widget.NewLabel(h.text)
		label.TextStyle = fyne.TextStyle{Bold: true}
		wrapper := container.NewGridWrap(fyne.NewSize(h.width, label.MinSize().Height), label)
		headerRow.Add(wrapper)
	}

	tableArea := container.NewBorder(
		container.NewPadded(headerRow), nil, nil, nil, u.table,
	)

	// Tree left, results right; draggable divider.
	split := container.NewHSplit(container.NewPadded(u.checksScroll), tableArea)
	split.Offset = 0.35

	// Export + Test buttons pinned left, status fills the rest along the bottom.
	bottom := container.NewBorder(nil, nil,
		container.NewHBox(u.exportBtn, u.testBtn), nil, u.statusLabel)

	return container.NewBorder(nil, bottom, nil, nil, split)
}

// buildTree constructs the region → city → provider tree, all collapsed.
func (u *ui) buildTree() *fyne.Container {
	root := container.NewVBox()
	u.trees = map[string]*regionTree{}
	u.regionOrder = regionNames(u.servers)
	for _, region := range u.regionOrder {
		root.Add(u.buildRegion(region))
	}
	return root
}

func (u *ui) buildRegion(region string) fyne.CanvasObject {
	servers := u.servers[region]
	groups := groupByCity(servers)

	rt := &regionTree{cities: map[string]*cityTree{}, order: make([]string, 0, len(groups))}
	u.trees[region] = rt

	regionCheck := widget.NewCheck(fmt.Sprintf("%s (%d)", region, len(servers)), func(checked bool) {
		if u.syncing {
			return
		}
		u.syncing = true
		for _, cityName := range rt.order {
			ct := rt.cities[cityName]
			setTriState(ct.check, checked, false)
			for _, id := range ct.ids {
				u.state[id] = checked
				ct.providers[id].SetChecked(checked)
			}
		}
		u.syncing = false
		u.savePrefs()
	})
	rt.check = regionCheck

	citiesBox := container.NewVBox()
	regionExpander := u.newExpander(regionNodeKey(region), citiesBox)

	for _, group := range groups {
		citiesBox.Add(indent(u.buildCity(region, group, rt)))
	}

	// Initial tri-state from restored selection (cities registered above).
	u.recomputeRegion(region)

	u.allChecks = append(u.allChecks, regionCheck)
	header := container.NewHBox(regionCheck, regionExpander)
	return container.NewVBox(header, indent(citiesBox))
}

func (u *ui) buildCity(region string, group CityGroup, rt *regionTree) fyne.CanvasObject {
	ct := &cityTree{providers: map[string]*widget.Check{}, ids: make([]string, 0, len(group.Servers))}
	rt.cities[group.City] = ct
	rt.order = append(rt.order, group.City)

	var cityCheck *widget.Check
	cityCheck = widget.NewCheck(fmt.Sprintf("%s (%d)", group.City, len(group.Servers)), func(checked bool) {
		if u.syncing {
			return
		}
		u.syncing = true
		for _, id := range ct.ids {
			u.state[id] = checked
			ct.providers[id].SetChecked(checked)
		}
		u.syncing = false
		setTriState(cityCheck, checked, false)
		u.recomputeRegion(region)
		u.savePrefs()
	})
	ct.check = cityCheck

	providersBox := container.NewVBox()
	cityExpander := u.newExpander(cityNodeKey(region, group.City), providersBox)

	for _, s := range group.Servers {
		server := s
		chk := widget.NewCheck(server.Provider, func(checked bool) {
			if u.syncing {
				return
			}
			u.state[server.ID()] = checked
			u.recomputeCity(region, server.City)
			u.recomputeRegion(region)
			u.savePrefs()
		})
		chk.Checked = u.state[server.ID()]
		providersBox.Add(chk)
		ct.providers[server.ID()] = chk
		ct.ids = append(ct.ids, server.ID())
		u.allChecks = append(u.allChecks, chk)
	}
	providersBox.Hide()

	// Initial tri-state from restored selection.
	all, some := u.cityStates(region, group.City)
	setTriState(cityCheck, all, some)

	u.allChecks = append(u.allChecks, cityCheck)
	header := container.NewHBox(cityCheck, cityExpander)
	return container.NewVBox(header, indent(providersBox))
}

// newExpander builds a ▸/▾ toggle that shows/hides obj, restoring and
// persisting its expanded state under nodeKey.
func (u *ui) newExpander(nodeKey string, obj fyne.CanvasObject) *widget.Button {
	expanded := u.expanded[nodeKey]
	var btn *widget.Button
	btn = widget.NewButton("▸", func() {
		expanded = !expanded
		u.expanded[nodeKey] = expanded
		if expanded {
			btn.SetText("▾")
			obj.Show()
		} else {
			btn.SetText("▸")
			obj.Hide()
		}
		u.saveExpanded()
	})
	btn.Importance = widget.LowImportance
	if expanded {
		btn.SetText("▾")
		obj.Show()
	} else {
		obj.Hide()
	}
	u.allExpanders = append(u.allExpanders, btn)
	return btn
}

func (u *ui) saveExpanded() {
	u.prefs.SetStringList(prefsExpandedKey, expandedToList(u.servers, u.expanded))
}

func indent(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewHBox(
		container.NewGridWrap(fyne.NewSize(28, 1), widget.NewLabel("")),
		obj,
	)
}

// setTriState applies an (all, some) aggregate to a parent checkbox without
// firing OnChanged. Tapping a partial check later fires OnChanged(true),
// which the parent handler treats as select-all.
func setTriState(chk *widget.Check, all, some bool) {
	wantChecked := all
	wantPartial := !all && some
	if chk.Checked == wantChecked && chk.Partial == wantPartial {
		return
	}
	chk.Checked = wantChecked
	chk.Partial = wantPartial
	chk.Refresh()
}

// cityStates aggregates a city's provider checkboxes into (all, some).
func (u *ui) cityStates(region, city string) (all, some bool) {
	ct := u.trees[region].cities[city]
	states := make([]bool, len(ct.ids))
	for i, id := range ct.ids {
		states[i] = u.state[id]
	}
	return aggregate(states)
}

func (u *ui) recomputeCity(region, city string) {
	ct := u.trees[region].cities[city]
	all, some := u.cityStates(region, city)
	setTriState(ct.check, all, some)
}

func (u *ui) recomputeRegion(region string) {
	rt := u.trees[region]
	cityStates := make([]CityState, 0, len(rt.order))
	for _, cityName := range rt.order {
		all, some := u.cityStates(region, cityName)
		cityStates = append(cityStates, CityState{All: all, Some: some})
	}
	all, some := aggregateCities(cityStates)
	setTriState(rt.check, all, some)
}

func (u *ui) savePrefs() {
	u.prefs.SetStringList(prefsDisabledKey, disabledFromState(u.servers, u.state))
}

// selectedServers returns checked servers across all regions in
// RegionOrder (then override-extra) order.
func (u *ui) selectedServers() []Server {
	var selected []Server
	for _, region := range u.regionOrder {
		for _, s := range u.servers[region] {
			if u.state[s.ID()] {
				selected = append(selected, s)
			}
		}
	}
	return selected
}

func (u *ui) onTestTap() {
	if u.running {
		if u.cancel != nil {
			u.cancel()
		}
		return
	}

	selected := u.selectedServers()
	if len(selected) == 0 {
		u.statusLabel.SetText("No servers selected.")
		return
	}

	u.rows = nil
	u.results = nil
	u.table.Refresh()
	u.setRunning(true)
	u.statusLabel.SetText(fmt.Sprintf("Testing (0/%d)...", len(selected)))

	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel

	// Track per-city completion so a worst-case summary row can be emitted
	// once every selected provider of a city has finished.
	cityTotal := map[string]int{}
	cityName := map[string]string{}
	for _, s := range selected {
		key := cityKey(u.regionBy[s.ID()], s.City)
		cityTotal[key]++
		cityName[key] = s.City
	}
	cityDone := map[string]int{}

	var (
		mu            sync.Mutex
		currentServer string
	)
	go func() {
		runTests(ctx, selected,
			func(name string, index, total int) {
				mu.Lock()
				currentServer = name
				mu.Unlock()
				fyne.Do(func() {
					u.statusLabel.SetText(fmt.Sprintf("Testing %s (%d/%d)...", name, index, total))
				})
			},
			func(sent, recv int) {
				mu.Lock()
				name := currentServer
				mu.Unlock()
				fyne.Do(func() {
					u.statusLabel.SetText(fmt.Sprintf("Testing %s — sent %d, received %d", name, sent, recv))
				})
			},
			func(outcome ServerOutcome) {
				fyne.Do(func() {
					u.results = append(u.results, outcome)
					u.rows = append(u.rows, outcomeRow(outcome))

					key := cityKey(u.regionBy[outcome.Server.ID()], outcome.Server.City)
					cityDone[key]++
					if cityDone[key] == cityTotal[key] {
						var group []ServerOutcome
						for _, o := range u.results {
							if cityKey(u.regionBy[o.Server.ID()], o.Server.City) == key {
								group = append(group, o)
							}
						}
						u.rows = append(u.rows, cityWorstCase(cityName[key], group))
					}
					u.table.Refresh()
				})
			},
		)
		fyne.Do(func() {
			u.setRunning(false)
			if ctx.Err() != nil {
				u.statusLabel.SetText(fmt.Sprintf("Cancelled — %d of %d servers tested.", len(u.results), len(selected)))
			} else {
				u.statusLabel.SetText(fmt.Sprintf("Done — %d servers tested.", len(u.results)))
			}
			u.cancel = nil
		})
	}()
}

func (u *ui) onExportTap() {
	if len(u.results) == 0 {
		u.statusLabel.SetText("No results to export yet.")
		return
	}
	d := dialog.NewFileSave(func(closer fyne.URIWriteCloser, err error) {
		if err != nil {
			u.statusLabel.SetText("Export failed: " + err.Error())
			return
		}
		if closer == nil {
			return // user cancelled
		}
		defer closer.Close()
		writeErr := writeCSV(closer, func(s Server) string { return u.regionBy[s.ID()] }, u.results)
		if writeErr != nil {
			u.statusLabel.SetText("Export failed: " + writeErr.Error())
			return
		}
		u.statusLabel.SetText(fmt.Sprintf("Exported %d results to CSV.", len(u.results)))
	}, u.window)
	d.SetFileName("faceit-tester-results.csv")
	d.Show()
}

func (u *ui) setRunning(running bool) {
	u.running = running
	if running {
		// Importance before SetText: SetText triggers the re-render, so
		// the color must already be updated when it fires.
		u.testBtn.Importance = widget.DangerImportance // red = stop
		u.testBtn.SetText("Cancel")
		u.testBtn.Enable()
		u.exportBtn.Disable()
	} else {
		u.testBtn.Importance = widget.SuccessImportance // green = start
		u.testBtn.SetText("Test")
		if isElevated() {
			u.testBtn.Enable()
		}
		if len(u.results) > 0 {
			u.exportBtn.Enable()
		}
	}
	for _, chk := range u.allChecks {
		if running {
			chk.Disable()
		} else {
			chk.Enable()
		}
	}
	for _, exp := range u.allExpanders {
		if running {
			exp.Disable()
		} else {
			exp.Enable()
		}
	}
}

// outcomeRow renders one result row from a finished server test.
func outcomeRow(outcome ServerOutcome) []tableCell {
	return resultRow(outcome.Server.DisplayName(), outcome.Result)
}

// resultRow renders one results-table row, coloring numeric cells green
// (SuccessImportance) at-or-below the CS2 HUD thresholds and red
// (DangerImportance) above them. With no received replies there is nothing
// to grade, so RTT cells stay uncolored; loss still grades (0-of-640 is
// a meaningful 100%).
func resultRow(name string, r PingResult) []tableCell {
	nameCell := tableCell{text: name}
	if r.Err != nil {
		return []tableCell{
			nameCell,
			{"—", widget.MediumImportance, false},
			{"—", widget.MediumImportance, false},
			{"—", widget.MediumImportance, false},
			{"—", widget.MediumImportance, false},
			{"error: " + r.Err.Error(), widget.DangerImportance, false},
		}
	}
	rttOK := r.Recv > 0
	return []tableCell{
		nameCell,
		{formatMS(r.Avg), gradeMS(r.Avg, pingThreshold, rttOK), false},
		{formatMS(r.Min), gradeMS(r.Min, pingThreshold, rttOK), false},
		{formatMS(r.Max), gradeMS(r.Max, pingThreshold, rttOK), false},
		{formatMS(r.Jitter), gradeMS(r.Jitter, jitterThreshold, rttOK), false},
		{fmt.Sprintf("%.2f%%", r.LossPercent), gradePct(r.LossPercent), false},
	}
}

// cityWorstCase builds the summary row shown after every selected provider
// of a city has been tested: column-by-column worst values across the
// city's outcomes (players cannot choose their provider, so this is what
// they might actually get). Errored providers are skipped for the numeric
// columns; if every provider errored, the first error is shown. The name
// cell is bold.
func cityWorstCase(city string, outcomes []ServerOutcome) []tableCell {
	var worst PingResult
	have := false
	var firstErr error
	for _, o := range outcomes {
		r := o.Result
		if r.Err != nil {
			if firstErr == nil {
				firstErr = r.Err
			}
			continue
		}
		if !have {
			worst = r
			have = true
			continue
		}
		if r.Recv > worst.Recv {
			worst.Recv = r.Recv
		}
		if r.Avg > worst.Avg {
			worst.Avg = r.Avg
		}
		if r.Min > worst.Min {
			worst.Min = r.Min
		}
		if r.Max > worst.Max {
			worst.Max = r.Max
		}
		if r.Jitter > worst.Jitter {
			worst.Jitter = r.Jitter
		}
		if r.LossPercent > worst.LossPercent {
			worst.LossPercent = r.LossPercent
		}
	}
	name := city + " (worst)"
	if !have {
		row := resultRow(name, PingResult{Err: firstErr})
		row[0].bold = true
		return row
	}
	row := resultRow(name, worst)
	row[0].bold = true
	return row
}

// cityKey identifies a city uniquely across regions for grouping.
func cityKey(region, city string) string {
	return region + "/" + city
}

// gradeMS returns SuccessImportance at-or-below threshold and
// DangerImportance above; ungraded (Medium) when hasSamples is false.
func gradeMS(d, threshold time.Duration, hasSamples bool) widget.Importance {
	if !hasSamples {
		return widget.MediumImportance
	}
	if d > threshold {
		return widget.DangerImportance
	}
	return widget.SuccessImportance
}

func gradePct(p float64) widget.Importance {
	if p > lossThreshold {
		return widget.DangerImportance
	}
	return widget.SuccessImportance
}

func formatMS(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000.0)
}
