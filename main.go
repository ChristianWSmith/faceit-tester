package main

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
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
	identifyBtn  *widget.Button
	statusLabel  *widget.Label
	table        *widget.Table
	headerRow    *fyne.Container // results column headers; col 0 width tracks content

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
	tree    *resultsTree    // hierarchical results model backing rows
	results []ServerOutcome // full outcomes for CSV export
	running bool
	cancel  context.CancelFunc

	nameColWidth float32 // current Server column width (0 = unset)
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
		tree:     newResultsTree(),
	}
	u.build()
	w.SetContent(u.content())
	// Persist selection + expansion once on exit: writing prefs on every
	// checkbox tap made the UI feel sluggish on slow storage.
	w.SetCloseIntercept(func() {
		u.saveAllPrefs()
		w.Close()
	})
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
	// Same widths the header row uses; col 0 is re-measured by
	// refreshTableLayout once rows exist.
	u.table.SetColumnWidth(0, minNameColWidth)
	for i := 1; i < 6; i++ {
		u.table.SetColumnWidth(i, metricColWidth)
	}
	// Tapping a region/city row toggles its children; leaves are no-ops.
	// Unselect immediately so no row stays highlighted.
	u.table.OnSelected = func(id widget.TableCellID) {
		if u.tree.toggle(id.Row) {
			u.rows = u.tree.flatten()
			u.refreshTableLayout()
		}
		u.table.UnselectAll()
	}

	u.testBtn = widget.NewButton("Test", u.onTestTap)
	u.testBtn.Importance = widget.SuccessImportance // green = start

	u.exportBtn = widget.NewButtonWithIcon("Export CSV", theme.DocumentSaveIcon(), u.onExportTap)
	u.exportBtn.Disable()

	u.identifyBtn = widget.NewButtonWithIcon("Identify", theme.SearchIcon(), u.onIdentifyTap)

	if !isElevated() {
		u.testBtn.Disable()
		u.statusLabel.SetText("Run this application as administrator to test (required on Windows).")
	}
}

func (u *ui) content() fyne.CanvasObject {
	// The table's findX inserts SizeNamePadding between columns, so the
	// header HBox (which pads between children with theme.Padding =
	// SizeNamePadding) matches column-for-column. No outer Padded wrapper:
	// the table body starts at x=0.
	u.headerRow = container.NewHBox()
	u.rebuildHeader(minNameColWidth)

	tableArea := container.NewBorder(
		u.headerRow, nil, nil, nil, u.table,
	)

	// Tree left, results right; draggable divider.
	split := container.NewHSplit(container.NewPadded(u.checksScroll), tableArea)
	split.Offset = 0.35

	// Status fills the left (room for long progress messages); actions
	// cluster right with the primary Test/Cancel toggle anchored last.
	bottom := container.NewBorder(nil, nil, nil,
		container.NewHBox(u.exportBtn, u.identifyBtn, u.testBtn), u.statusLabel)

	return container.NewBorder(nil, bottom, nil, nil, split)
}

const (
	// minNameColWidth is the floor for the Server column; it grows to fit
	// the widest name cell currently rendered (deep tree labels).
	minNameColWidth = 170
	// metricColWidth is the fixed width of the five metric columns; sized
	// for the widest header ("Ping (med)" ≈ 83px padded) with slack for
	// platform font metrics.
	metricColWidth = float32(90)
)

// rebuildHeader recreates the header labels, sizing the Server column to
// nameWidth so it stays aligned with the table's dynamic first column.
func (u *ui) rebuildHeader(nameWidth float32) {
	u.headerRow.RemoveAll()
	headers := []struct {
		text  string
		width float32
	}{
		// "Ping" is the gamer term for RTT and implies milliseconds; the
		// suffix says which statistic of the reply samples it is.
		{"Server", nameWidth}, {"Ping (med)", metricColWidth}, {"Ping (p1)", metricColWidth},
		{"Ping (p99)", metricColWidth}, {"Jitter", metricColWidth}, {"Loss", metricColWidth},
	}
	for _, h := range headers {
		label := widget.NewLabel(h.text)
		label.TextStyle = fyne.TextStyle{Bold: true}
		u.headerRow.Add(container.NewGridWrap(fyne.NewSize(h.width, label.MinSize().Height), label))
	}
}

// refreshTableLayout re-measures the name column against u.rows, updates
// the table and header widths when it changed, and repaints. Call after
// every rows rebuild.
func (u *ui) refreshTableLayout() {
	w := nameColumnWidth(u.rows)
	if w != u.nameColWidth {
		u.nameColWidth = w
		u.table.SetColumnWidth(0, w)
		u.rebuildHeader(w)
	}
	u.table.Refresh()
}

// nameColumnWidth returns the width needed for the widest name cell in
// rows (bold parents measured bold), floored at minNameColWidth.
func nameColumnWidth(rows [][]tableCell) float32 {
	w := float32(minNameColWidth)
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		tw := fyne.MeasureText(row[0].text, theme.TextSize(), fyne.TextStyle{Bold: row[0].bold})
		// Label padding on both sides plus a hair of slack.
		if cw := tw.Width + 2*theme.Padding() + 2; cw > w {
			w = cw
		}
	}
	return w
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
		})
		chk.Checked = u.state[server.ID()]
		providersBox.Add(chk)
		ct.providers[server.ID()] = chk
		ct.ids = append(ct.ids, server.ID())
		u.allChecks = append(u.allChecks, chk)
	}
	// Visibility is owned by newExpander: a restored-expanded city was
	// Show()n there, so do not force-hide the box after adding children.

	// Initial tri-state from restored selection.
	all, some := u.cityStates(region, group.City)
	setTriState(cityCheck, all, some)

	u.allChecks = append(u.allChecks, cityCheck)
	header := container.NewHBox(cityCheck, cityExpander)
	return container.NewVBox(header, indent(providersBox))
}

// newExpander builds a ▸/▾ toggle that shows/hides obj, restoring its
// expanded state under nodeKey. The updated state map is persisted on
// exit (see main), not per tap.
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

// saveAllPrefs writes the selection denylist and expansion allowlist.
// Called once from the window close intercept; the in-memory maps are
// the live source of truth while the app runs.
func (u *ui) saveAllPrefs() {
	u.prefs.SetStringList(prefsDisabledKey, disabledFromState(u.servers, u.state))
	u.prefs.SetStringList(prefsExpandedKey, expandedToList(u.servers, u.expanded))
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

	u.tree = newResultsTree()
	u.rows = nil
	u.results = nil
	u.refreshTableLayout()
	u.setRunning(true)
	u.statusLabel.SetText(fmt.Sprintf("Testing (0/%d)...", len(selected)))

	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel

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
					u.tree.addOutcome(u.regionBy[outcome.Server.ID()], outcome)
					u.rows = u.tree.flatten()
					u.refreshTableLayout()
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
	return resultRow(outcome.Server.DisplayName(), outcome.Result, outcome.Server.Scrubbed)
}

// resultRow renders one results-table row, coloring numeric cells green
// (SuccessImportance) at-or-below the CS2 HUD thresholds and red
// (DangerImportance) above them. With no received replies there is nothing
// to grade, so RTT cells stay uncolored; loss still grades (0-of-640 is
// a meaningful 100%). Scrubbed endpoints cannot report loss or jitter
// (their ICMP responder is a DDoS scrubber, not the path), so those cells
// show n/a uncolored instead of a misleading number.
func resultRow(name string, r PingResult, scrubbed bool) []tableCell {
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
	jitterCell := tableCell{formatMS(r.Jitter), gradeMS(r.Jitter, jitterThreshold, rttOK), false}
	lossCell := tableCell{fmt.Sprintf("%.2f%%", r.LossPercent), gradePct(r.LossPercent), false}
	if scrubbed {
		jitterCell = tableCell{"n/a", widget.MediumImportance, false}
		lossCell = tableCell{"n/a", widget.MediumImportance, false}
	}
	return []tableCell{
		nameCell,
		{formatMS(r.Median), gradeMS(r.Median, pingThreshold, rttOK), false},
		{formatMS(r.P1), gradeMS(r.P1, pingThreshold, rttOK), false},
		{formatMS(r.P99), gradeMS(r.P99, pingThreshold, rttOK), false},
		jitterCell,
		lossCell,
	}
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

// onIdentifyTap opens the connect-string lookup dialog: paste a CS2
// connect command (or any text containing the server address), look it
// up via RDAP, and see which hosting provider — and which of our tested
// locations — it maps to. The dialog opens compact and only grows to fit
// a returned report.
func (u *ui) onIdentifyTap() {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("connect 198.244.202.178:27015")
	result := widget.NewLabel("Paste a connect string (or any text containing the server address), then Look up.")
	result.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(result)
	scroll.SetMinSize(fyne.NewSize(440, 56))

	pasteBtn := widget.NewButtonWithIcon("Paste", theme.ContentPasteIcon(), func() {
		if c := u.window.Clipboard(); c != nil {
			entry.SetText(c.Content())
		}
	})
	top := container.NewBorder(nil, nil, nil, pasteBtn, entry)

	// Declare before NewButton: the closures reference lookupBtn/d, whose
	// scope starts after the := statement (same gotcha as the check tree).
	var (
		d         *dialog.CustomDialog
		lookupBtn *widget.Button
	)
	lookupBtn = widget.NewButton("Look up", func() {
		raw := entry.Text
		if strings.TrimSpace(raw) == "" {
			result.SetText("Nothing to look up — paste a connect string first.")
			return
		}
		lookupBtn.Disable()
		result.SetText("Looking up…")
		go func() {
			text := u.identifyText(context.Background(), raw)
			fyne.Do(func() {
				result.SetText(text)
				lookupBtn.Enable()
				fitIdentifyDialog(d, scroll, result, text)
			})
		}()
	})
	lookupBtn.Importance = widget.HighImportance
	closeBtn := widget.NewButton("Close", func() { d.Hide() })

	// Own the bottom row (WithoutButtons — NewCustom always renders a
	// dismiss button, even with empty text): Close bottom-left, Look up
	// bottom-right, neither stretched.
	buttons := container.NewHBox(closeBtn, layout.NewSpacer(), lookupBtn)
	body := container.NewBorder(nil, buttons, nil, nil, container.NewVBox(top, scroll))
	d = dialog.NewCustomWithoutButtons("Identify server", body, u.window)
	d.Show()
}

// fitIdentifyDialog grows the report area (and the dialog with it) to
// the lookup result; hints and short errors keep the dialog compact.
func fitIdentifyDialog(d *dialog.CustomDialog, scroll *container.Scroll, result *widget.Label, text string) {
	h := identifyScrollHeight(text, result.MinSize().Height)
	scroll.SetMinSize(fyne.NewSize(440, h))
	if d != nil {
		// entry + scroll + button row + paddings.
		d.Resize(fyne.NewSize(520, h+140))
	}
}

// identifyScrollHeight is the report-area height for a result. Word
// wrapping adds visual lines the logical line count misses, so use the
// label's measured height when it is larger; floor high enough that the
// common happy-path report fits without scrolling, cap with scroll
// taking over for long city lists.
func identifyScrollHeight(text string, measured float32) float32 {
	h := float32(strings.Count(text, "\n")+1) * 22
	if measured > h {
		h = measured
	}
	if h < 160 {
		return 160
	}
	if h > 320 {
		return 320
	}
	return h
}

// identifyText runs one lookup and renders it for the dialog. Lookup
// errors are shown inline rather than failing the whole report.
func (u *ui) identifyText(ctx context.Context, raw string) string {
	res := identifyTarget(ctx, raw, u.servers)
	var b strings.Builder
	fmt.Fprintf(&b, "Target:   %s\n", res.Display())
	if res.IP != "" && res.IP != res.Host {
		fmt.Fprintf(&b, "Address:  %s\n", res.IP)
	}
	if res.Err != nil {
		fmt.Fprintf(&b, "Error:    %v", res.Err)
		return b.String()
	}
	if res.Exact != nil {
		fmt.Fprintf(&b, "Known speedtest host in our list.\nProvider: %s · %s · %s",
			u.regionBy[res.Exact.ID()], res.Exact.City, res.Exact.Provider)
		return b.String()
	}
	fmt.Fprintf(&b, "RDAP:     %s\n", orDash(res.Registrant))
	if res.Netname != "" {
		fmt.Fprintf(&b, "Netname:  %s\n", res.Netname)
	}
	if res.Country != "" {
		fmt.Fprintf(&b, "Country:  %s\n", res.Country)
	}
	if len(res.PTR) > 0 {
		fmt.Fprintf(&b, "PTR:      %s\n", res.PTR[0])
	}
	if res.Provider == "" {
		b.WriteString("Provider: (unrecognized — see RDAP above)")
		return b.String()
	}
	fmt.Fprintf(&b, "Provider: %s\n\nTested locations with this provider:\n", res.Provider)
	matched := citiesWithProvider(u.servers, res.Provider)
	if len(matched) == 0 {
		b.WriteString("  (none in the server list)")
		return b.String()
	}
	for _, m := range matched {
		var outs []ServerOutcome
		for _, o := range u.results {
			if o.Server.Provider == res.Provider && o.Server.City == m.City &&
				u.regionBy[o.Server.ID()] == m.Region {
				outs = append(outs, o)
			}
		}
		if len(outs) == 0 {
			fmt.Fprintf(&b, "  %s · %s — not tested\n", m.Region, m.City)
			continue
		}
		row := worstCase("", outs)
		fmt.Fprintf(&b, "  %s · %s — med %s, p99 %s, jitter %s, loss %s\n",
			m.Region, m.City, row[1].text, row[3].text, row[4].text, row[5].text)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
