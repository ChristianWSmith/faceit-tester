package main

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// editRow is one server under edit. Numeric fields stay raw strings until
// Apply so half-typed values never break the edit session; the form
// validators (nonNegativeInt) keep bad input visible but harmless.
type editRow struct {
	City      string
	Provider  string
	Host      string
	MaxPPS    string
	ProbeSize string
	Scrubbed  bool
}

// toServer parses one row into the storage form.
func (r editRow) toServer() (Server, error) {
	maxPPS, err := parseOptInt(r.MaxPPS)
	if err != nil {
		return Server{}, fmt.Errorf("max pps: %w", err)
	}
	probeSize, err := parseOptInt(r.ProbeSize)
	if err != nil {
		return Server{}, fmt.Errorf("probe size: %w", err)
	}
	return Server{
		City:      r.City,
		Provider:  r.Provider,
		Host:      r.Host,
		MaxPPS:    maxPPS,
		ProbeSize: probeSize,
		Scrubbed:  r.Scrubbed,
	}, nil
}

// parseOptInt reads an optional non-negative integer ("" = 0 = default).
func parseOptInt(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q must be a whole number >= 0", raw)
	}
	return n, nil
}

// nonNegativeInt is the form validator for the optional numeric fields.
func nonNegativeInt(raw string) error {
	_, err := parseOptInt(raw)
	return err
}

// rowsFromTable converts a server table into the editor's row view.
func rowsFromTable(table map[string][]Server) map[string][]editRow {
	rows := make(map[string][]editRow, len(table))
	for region, list := range table {
		rs := make([]editRow, 0, len(list))
		for _, s := range list {
			r := editRow{City: s.City, Provider: s.Provider, Host: s.Host, Scrubbed: s.Scrubbed}
			if s.MaxPPS != 0 {
				r.MaxPPS = strconv.Itoa(s.MaxPPS)
			}
			if s.ProbeSize != 0 {
				r.ProbeSize = strconv.Itoa(s.ProbeSize)
			}
			rs = append(rs, r)
		}
		rows[region] = rs
	}
	return rows
}

// tableFromRows parses the editor's row view back into a storage table,
// rejecting incomplete or invalid input. Regions whose row list is empty
// are dropped — they carry no information and would render as empty tree
// nodes — but the list as a whole must keep at least one server.
func tableFromRows(rows map[string][]editRow) (map[string][]Server, error) {
	table := make(map[string][]Server, len(rows))
	total := 0
	for region, list := range rows {
		if len(list) == 0 {
			continue
		}
		out := make([]Server, 0, len(list))
		for i, r := range list {
			s, err := r.toServer()
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", region, i, err)
			}
			out = append(out, s)
		}
		table[region] = out
		total += len(list)
	}
	if total == 0 {
		return nil, fmt.Errorf("the list needs at least one server")
	}
	if err := validateServers(table); err != nil {
		return nil, err
	}
	return table, nil
}

// rowGroup is one city's rows within a region slice, by index.
type rowGroup struct {
	City    string
	Indexes []int
}

// groupRowsByCity groups rows by city preserving first-appearance order
// (the editRow twin of groupByCity).
func groupRowsByCity(rows []editRow) []rowGroup {
	index := make(map[string]int)
	var groups []rowGroup
	for i, r := range rows {
		if gi, ok := index[r.City]; ok {
			groups[gi].Indexes = append(groups[gi].Indexes, i)
			continue
		}
		index[r.City] = len(groups)
		groups = append(groups, rowGroup{City: r.City, Indexes: []int{i}})
	}
	return groups
}

// serverEditor is the custom-list dialog: a region → city → server list
// on the left, a per-server form on the right. Nothing touches prefs
// until Apply; Cancel discards the working copy wholesale. The working
// copy starts from the effective table — built-ins when no custom list
// exists — so the first Apply always stores a complete baseline plus
// the user's edits. Reset to Defaults reloads the built-ins into the
// working copy (still Apply to persist); destructive row actions
// confirm first because the only undo is Cancel-everything.
type serverEditor struct {
	u       *ui
	working map[string][]editRow
	region  string // selected row's region; index < 0 = nothing selected
	index   int

	d         *dialog.CustomDialog
	listBox   *fyne.Container
	errLabel  *widget.Label
	city      *widget.Entry
	provider  *widget.Entry
	host      *widget.Entry
	maxPPS    *widget.Entry
	probe     *widget.Entry
	scrubbed  *widget.Check
	deleteBtn *widget.Button

	// syncing guards write-through handlers while selectRow repopulates
	// the form (SetText/SetChecked fire callbacks).
	syncing bool
}

// onEditTap opens the server-list editor. Disabled while a run is in
// flight (setRunning), so the table can't swap under a live test.
func (u *ui) onEditTap() {
	if u.running {
		return
	}
	e := &serverEditor{
		u:       u,
		working: rowsFromTable(u.servers),
		region:  "",
		index:   -1,
	}
	e.build()
	if region, idx, ok := e.firstRow(); ok {
		e.selectRow(region, idx)
	} else {
		e.setFormEnabled(false)
	}
	e.d.Show()
}

func (e *serverEditor) build() {
	e.listBox = container.NewVBox()
	scroll := container.NewVScroll(e.listBox)
	scroll.SetMinSize(fyne.NewSize(260, 0))

	e.city = widget.NewEntry()
	e.provider = widget.NewEntry()
	e.host = widget.NewEntry()
	e.maxPPS = widget.NewEntry()
	e.maxPPS.Validator = nonNegativeInt
	e.probe = widget.NewEntry()
	e.probe.Validator = nonNegativeInt
	e.scrubbed = widget.NewCheck("Scrubbed", nil)

	// Write-through: every keystroke lands in the working copy, so
	// navigating away never has to flush pending field state.
	e.city.OnChanged = func(v string) { e.write(func(r *editRow) { r.City = v }); e.rebuildList() }
	e.provider.OnChanged = func(v string) { e.write(func(r *editRow) { r.Provider = v }); e.rebuildList() }
	e.host.OnChanged = func(v string) { e.write(func(r *editRow) { r.Host = v }) }
	e.maxPPS.OnChanged = func(v string) { e.write(func(r *editRow) { r.MaxPPS = v }) }
	e.probe.OnChanged = func(v string) { e.write(func(r *editRow) { r.ProbeSize = v }) }
	e.scrubbed.OnChanged = func(v bool) { e.write(func(r *editRow) { r.Scrubbed = v }) }

	// FormItem.HintText renders under the field in placeholder styling —
	// Fyne has no tooltip API, and these fields' semantics (policer
	// quotas, size filters, scrubbers) would otherwise be undocumented.
	hostItem := widget.NewFormItem("Host", e.host)
	hostItem.HintText = "IPv4/hostname reachable over ICMP (this is what gets probed)"
	ppsItem := widget.NewFormItem("Max PPS", e.maxPPS)
	ppsItem.HintText = "Cap the probe send rate (packets/sec). 0 or empty = default 64. Set when a provider rate-limits ICMP."
	sizeItem := widget.NewFormItem("Probe size", e.probe)
	sizeItem.HintText = "ICMP payload bytes. 0 or empty = default 600. Lower (e.g. 56) when a provider filters large echo packets."
	scrubItem := widget.NewFormItem("Scrubbed", e.scrubbed)
	scrubItem.HintText = "Replies come from a DDoS scrubber, not the game path — loss/jitter report as n/a; RTT stays valid."
	form := widget.NewForm(
		widget.NewFormItem("City", e.city),
		widget.NewFormItem("Provider", e.provider),
		hostItem,
		ppsItem,
		sizeItem,
		scrubItem,
	)

	e.errLabel = widget.NewLabel("")
	e.errLabel.Importance = widget.DangerImportance
	e.errLabel.Wrapping = fyne.TextWrapWord
	e.errLabel.Hide()

	header := widget.NewLabel(e.headerText())
	header.Wrapping = fyne.TextWrapWord

	// Cancel/Apply own the bottom row (NewCustomWithoutButtons — see the
	// identify dialog); Drop and Reset sit with the other negative
	// actions on the left.
	cancelBtn := widget.NewButton("Cancel", func() { e.d.Hide() })
	applyBtn := widget.NewButton("Apply", e.apply)
	applyBtn.Importance = widget.HighImportance // blue primary; importance before first render
	resetBtn := widget.NewButton("Reset to Defaults", e.resetTapped)
	var btns []fyne.CanvasObject
	if overrideActive(e.u.prefs) {
		btns = append(btns, widget.NewButton("Drop Custom List", e.dropTapped))
	}
	btns = append(btns, resetBtn, layout.NewSpacer(), cancelBtn, applyBtn)
	buttons := container.NewHBox(btns...)

	center := container.NewVBox(form, e.errLabel)
	body := container.NewBorder(header, buttons, scroll, nil, center)
	e.d = dialog.NewCustomWithoutButtons("Edit servers", body, e.u.window)
	e.d.Resize(fyne.NewSize(720, 560))

	e.rebuildList()
}

// headerText says what Apply will do from the current starting point.
func (e *serverEditor) headerText() string {
	if overrideActive(e.u.prefs) {
		return "Editing your custom server list. Apply saves it; Drop Custom List restores the built-ins."
	}
	return "Editing the built-in servers — Apply creates a custom list. Cancel leaves everything unchanged."
}

// write applies fn to the selected row; no-ops while syncing or when
// nothing is selected.
func (e *serverEditor) write(fn func(*editRow)) {
	if e.syncing || e.index < 0 {
		return
	}
	fn(&e.working[e.region][e.index])
}

func (e *serverEditor) firstRow() (string, int, bool) {
	for _, region := range orderedRegions(e.working) {
		if len(e.working[region]) > 0 {
			return region, 0, true
		}
	}
	return "", 0, false
}

// selectRow points the form at one row. Repopulating the form fires
// write-through handlers, but syncing holds them off and the values are
// identical anyway.
func (e *serverEditor) selectRow(region string, index int) {
	e.region, e.index = region, index
	e.syncing = true
	r := e.working[region][index]
	e.city.SetText(r.City)
	e.provider.SetText(r.Provider)
	e.host.SetText(r.Host)
	e.maxPPS.SetText(r.MaxPPS)
	e.probe.SetText(r.ProbeSize)
	e.scrubbed.Checked = r.Scrubbed
	e.scrubbed.Refresh()
	e.syncing = false
	e.setFormEnabled(true)
	e.rebuildList()
}

// setFormEnabled toggles the per-server form; disabled when no row is
// selected (every server in the region deleted). Clearing fires
// write-through handlers, so hold syncing — the invariant shouldn't be
// load-bearing here.
func (e *serverEditor) setFormEnabled(enabled bool) {
	e.syncing = true
	entries := []*widget.Entry{e.city, e.provider, e.host, e.maxPPS, e.probe}
	for _, entry := range entries {
		if enabled {
			entry.Enable()
		} else {
			entry.SetText("")
			entry.Disable()
		}
	}
	if enabled {
		e.scrubbed.Enable()
	} else {
		e.scrubbed.Checked = false
		e.scrubbed.Refresh()
		e.scrubbed.Disable()
	}
	e.syncing = false
}

// rebuildList re-renders the left pane from the working copy; the
// selected row is highlighted. Cheap at ~90 rows, and City/Provider
// edits must re-render to stay in sync. Region rows carry "+ City" and
// a delete-region button; city rows carry "+ Provider" and a
// delete-city button — both pinned to the right edge of the STRETCHED
// row (indentBlock, not indent: an HBox-sized indent collapses the
// spacer and left-clumps the buttons). A top-level "+ Add region"
// creates whole regions.
func (e *serverEditor) rebuildList() {
	e.listBox.RemoveAll()
	addRegionBtn := widget.NewButton("+ Add region", e.addRegionPrompt)
	addRegionBtn.Importance = widget.LowImportance
	e.listBox.Add(addRegionBtn)
	for _, region := range orderedRegions(e.working) {
		regionLabel := widget.NewLabel(region)
		regionLabel.TextStyle = fyne.TextStyle{Bold: true}
		addCityBtn := widget.NewButton("+ City", func() { e.addCityPrompt(region) })
		addCityBtn.Importance = widget.LowImportance
		deleteRegionBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { e.deleteRegion(region) })
		deleteRegionBtn.Importance = widget.LowImportance
		e.listBox.Add(container.NewHBox(regionLabel, layout.NewSpacer(), addCityBtn, deleteRegionBtn))
		for _, g := range groupRowsByCity(e.working[region]) {
			cityLabel := widget.NewLabel(fmt.Sprintf("%s (%d)", g.City, len(g.Indexes)))
			addProviderBtn := widget.NewButton("+ Provider", func() { e.addServer(region, g.City) })
			addProviderBtn.Importance = widget.LowImportance
			deleteCityBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { e.deleteCity(region, g.City) })
			deleteCityBtn.Importance = widget.LowImportance
			header := container.NewBorder(nil, nil, cityLabel,
				container.NewHBox(addProviderBtn, deleteCityBtn))

			block := container.NewVBox(header)
			for _, idx := range g.Indexes {
				row := e.working[region][idx]
				i := idx
				btn := widget.NewButton(row.Provider, func() { e.selectRow(region, i) })
				if region == e.region && idx == e.index {
					btn.Importance = widget.HighImportance
				}
				// Per-row delete, right-pinned like the city/region
				// trashes — destructive affordances live in the list,
				// not the form pane.
				del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { e.deleteRow(region, i) })
				del.Importance = widget.LowImportance
				block.Add(indentBlock(container.NewBorder(nil, nil, btn, del)))
			}
			e.listBox.Add(indentBlock(block))
		}
	}
}

// addServer appends a placeholder under city and selects it; the user
// fills host/provider before Apply (validation blocks an incomplete row).
func (e *serverEditor) addServer(region, city string) {
	e.working[region] = append(e.working[region], editRow{
		City:     city,
		Provider: "New Provider",
	})
	e.selectRow(region, len(e.working[region])-1)
}

// addRegionPrompt asks for a new region name and seeds it with one
// placeholder server (a zero-row region would be invisible in the list
// and stripped on Apply).
func (e *serverEditor) addRegionPrompt() {
	nameEntry := widget.NewEntry()
	nameEntry.Validator = func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("region name must not be empty")
		}
		if strings.Contains(s, "/") {
			return fmt.Errorf(`region must not contain "/"`)
		}
		if _, exists := e.working[s]; exists {
			return fmt.Errorf("region %q already exists", s)
		}
		return nil
	}
	e.promptName("Add region", "Region name", nameEntry, func(name string) {
		e.working[name] = []editRow{{City: "New City", Provider: "New Provider"}}
		e.selectRow(name, 0)
	})
}

// addCityPrompt asks for a new city name within region and seeds it
// with one placeholder server.
func (e *serverEditor) addCityPrompt(region string) {
	nameEntry := widget.NewEntry()
	nameEntry.Validator = func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("city name must not be empty")
		}
		if strings.Contains(s, "/") {
			return fmt.Errorf(`city must not contain "/"`)
		}
		for _, r := range e.working[region] {
			if r.City == s {
				return fmt.Errorf("city %q already exists in %s", s, region)
			}
		}
		return nil
	}
	e.promptName("Add city in "+region, "City name", nameEntry, func(name string) {
		e.working[region] = append(e.working[region], editRow{
			City:     name,
			Provider: "New Provider",
		})
		e.selectRow(region, len(e.working[region])-1)
	})
}

// promptName shows a one-field Form dialog and calls onOK with the
// validated (trimmed) name. The Form's Validator keeps OK disabled
// until the entry is valid.
func (e *serverEditor) promptName(title, label string, entry *widget.Entry, onOK func(string)) {
	item := widget.NewFormItem(label, entry)
	d := dialog.NewForm(title, "Add", "Cancel", []*widget.FormItem{item},
		func(ok bool) {
			if !ok {
				return
			}
			onOK(strings.TrimSpace(entry.Text))
		}, e.u.window)
	d.Resize(fyne.NewSize(360, 0))
	d.Show()
}

// deleteRow confirms, then removes the row the list's trash button
// named. The only undo is Cancel (discards the whole session), so
// destructive actions always confirm.
func (e *serverEditor) deleteRow(region string, index int) {
	if index < 0 || index >= len(e.working[region]) {
		return
	}
	victim := e.working[region][index]
	d := dialog.NewConfirm("Delete server",
		fmt.Sprintf("Delete %s.%s from %s?\n\nApply saves the deletion; Cancel discards everything.",
			victim.City, victim.Provider, region),
		func(ok bool) {
			if !ok {
				return
			}
			rows := e.working[region]
			e.working[region] = append(rows[:index], rows[index+1:]...)
			e.reselectAfterRemoval()
		}, e.u.window)
	d.Show()
}

// deleteCity confirms, then removes every row in the city.
func (e *serverEditor) deleteCity(region, city string) {
	d := dialog.NewConfirm("Delete city",
		fmt.Sprintf("Delete %s and all its servers from %s?", city, region),
		func(ok bool) {
			if !ok {
				return
			}
			e.working[region] = removeCityRows(e.working[region], city)
			e.reselectAfterRemoval()
		}, e.u.window)
	d.Show()
}

// deleteRegion confirms, then removes the whole region.
func (e *serverEditor) deleteRegion(region string) {
	d := dialog.NewConfirm("Delete region",
		fmt.Sprintf("Delete %s and all its cities and servers?", region),
		func(ok bool) {
			if !ok {
				return
			}
			delete(e.working, region)
			e.reselectAfterRemoval()
		}, e.u.window)
	d.Show()
}

// resetTapped confirms, then reloads the built-ins into the working
// copy — an in-editor reset, not a prefs change (Apply persists it,
// Cancel throws it away).
func (e *serverEditor) resetTapped() {
	d := dialog.NewConfirm("Reset to defaults",
		"Reload the built-in server list into the editor?\n\nYour current edits are discarded; Apply saves the defaults as your list.",
		func(ok bool) {
			if !ok {
				return
			}
			e.working = rowsFromTable(embeddedServers)
			e.reselectAfterRemoval()
		}, e.u.window)
	d.Show()
}

// reselectAfterRemoval points the form at the first surviving row (or
// disables it when nothing is left) and re-renders the list.
func (e *serverEditor) reselectAfterRemoval() {
	if region, idx, ok := e.firstRow(); ok {
		e.selectRow(region, idx)
		return
	}
	e.region, e.index = "", -1
	e.setFormEnabled(false)
	e.rebuildList()
}

// removeCityRows drops every row belonging to city, preserving order.
func removeCityRows(rows []editRow, city string) []editRow {
	out := make([]editRow, 0, len(rows))
	for _, r := range rows {
		if r.City != city {
			out = append(out, r)
		}
	}
	return out
}

// apply validates the working copy, persists it as the custom list, and
// swaps the live table. A table matching the built-ins drops the custom
// list instead of storing it (see saveOverride).
func (e *serverEditor) apply() {
	if err := e.maxPPS.Validate(); err != nil {
		e.showError(fmt.Errorf("max pps: %w", err))
		return
	}
	if err := e.probe.Validate(); err != nil {
		e.showError(fmt.Errorf("probe size: %w", err))
		return
	}
	table, err := tableFromRows(e.working)
	if err != nil {
		e.showError(err)
		return
	}
	wasActive := overrideActive(e.u.prefs)
	if err := saveOverride(e.u.prefs, table); err != nil {
		e.showError(err)
		return
	}
	nowActive := overrideActive(e.u.prefs)

	// Same table as live: nothing to rebuild — but a stale/broken key
	// may still have been cleaned up by saveOverride above.
	if reflect.DeepEqual(table, e.u.servers) {
		if wasActive && !nowActive {
			e.u.statusLabel.SetText("Custom list cleared — the table matches the built-ins.")
		} else {
			e.u.statusLabel.SetText("No changes to apply.")
		}
		e.d.Hide()
		return
	}

	var status string
	if nowActive {
		status = "Custom server list saved. Previous results cleared."
	} else {
		status = "Applied — the list matches the built-ins, so the custom list was cleared. Previous results cleared."
	}
	e.u.applyServerTable(table, status)
	e.d.Hide()
}

// dropTapped confirms, then deletes the custom list and restores the
// built-ins.
func (e *serverEditor) dropTapped() {
	d := dialog.NewConfirm("Drop custom server list",
		"Restore the built-in server list?\n\nYour custom edits will be deleted.",
		func(ok bool) {
			if !ok {
				return
			}
			dropOverride(e.u.prefs)
			e.u.applyServerTable(embeddedServers,
				"Custom list dropped — using the built-in servers. Previous results cleared.")
			e.d.Hide()
		}, e.u.window)
	d.Show()
}

func (e *serverEditor) showError(err error) {
	e.errLabel.SetText(err.Error())
	e.errLabel.Show()
}
