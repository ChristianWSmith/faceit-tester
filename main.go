package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
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
// red; at-or-above the warn thresholds render yellow (WarningImportance);
// below the warn thresholds render green.
const (
	pingThreshold   = 100 * time.Millisecond
	jitterThreshold = 10 * time.Millisecond
	lossThreshold   = 2.0 // percent

	pingWarnThreshold   = 70 * time.Millisecond
	jitterWarnThreshold = 5 * time.Millisecond
	lossWarnThreshold   = 1.0 // percent
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
	treeHolder   *fyne.Container // wraps checksScroll so the tree can be rebuilt in place
	testBtn      *widget.Button
	restartBtn   *widget.Button
	exportBtn    *widget.Button
	editBtn      *widget.Button
	identifyBtn  *widget.Button
	drawerBtn    *widget.Button  // toggles the secondary-actions drawer
	drawerLayer  *fyne.Container // floating drawer: catcher + action column
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
	// Help output goes to stdout so console sessions can read it; the
	// packaged windowsgui exe has no console, so --help exits silently
	// there (README carries the same reference). Parsed before the
	// elevation check so --help never triggers a UAC prompt.
	flag.CommandLine.SetOutput(os.Stdout)
	testSpec := flag.String("test", "", `auto-start a test: JSON spec of regions/cities/providers, e.g. {"NA":"all"}`)
	forceUpdate := flag.Bool("force-update-check", false, "show the update dialog even when no newer release exists (dev/UI-review)")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "FACEIT Connection Tester — pre-flight ICMP connection tester for FACEIT CS2 locations.\n\n")
		fmt.Fprintf(out, "Usage:\n  faceit-tester [flags]\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(out, `
--test selects servers with a JSON object of region → city → provider:
  {"NA":"all"}                               entire NA region
  {"NA":{"Virginia":"all"}}                  one city
  {"NA":{"Virginia":"OVH"}}                  one provider
  {"NA":{"Virginia":["OVH","Datacamp"]}}     provider list
  {"NA":"all","EU":{"Germany":"all"}}        multiple regions
  {"all":"all"}                              every server in every region

Names match case-insensitively; "all" is reserved at every level. The
selection replaces the checkbox state and is saved on exit. On Windows
the app requests administrator rights on launch; a --test flag survives
the elevated relaunch and starts automatically once the window is up.
--force-update-check bypasses the update-check gates (dev build, "Never"
pref, --test) so the update dialog can be reviewed without a real
pending release.
`)
	}
	flag.Parse()

	// Sweep "<exe>.old" / "*.app.old" trash left by a previous self-update
	// before anything else touches the install directory. Best-effort.
	cleanupOlds()

	// The app object must exist before the elevation check: --test is
	// validated against the server list, and the custom list now lives in
	// app preferences. Creating the app and reading prefs needs no event
	// loop and writes nothing (a Set* before ShowAndRun would block on
	// fyne.DoAndWait), so this is safe pre-UAC and pre-Run.
	a := app.NewWithID(appID)

	// Validate --test before the elevation prompt so a bad spec fails
	// fast on the terminal instead of after a pointless UAC round-trip.
	// Errors also reach the status label once the window is up (stderr
	// writes are silent no-ops in the windowsgui build, which has no
	// console handles).
	prefs := a.Preferences()
	servers, overrideErr := loadServers(prefs)
	var (
		specErr error
		sel     map[string]bool
	)
	if *testSpec != "" {
		var err error
		if sel, err = parseTestSpec(servers, *testSpec); err != nil {
			specErr = err
			fmt.Fprintln(os.Stderr, "Invalid --test spec:", err)
		}
	}

	// Request elevation before any window exists: a launch goes straight
	// to the UAC prompt, and the relaunched process re-parses the
	// forwarded argv so --test runs elevated automatically. A dismissed
	// prompt falls through to the unelevated GUI, where Restart as Admin
	// explains and retries.
	if !isElevated() {
		switch err := elevateAndRestart(); {
		case err == nil:
			os.Exit(0)
		case errors.Is(err, errElevationCancelled):
			fmt.Fprintln(os.Stderr, "Elevation cancelled — running unelevated.")
		default:
			fmt.Fprintln(os.Stderr, "Elevation failed:", err)
		}
	}

	a.SetIcon(fyne.NewStaticResource("icon.png", iconData))
	w := a.NewWindow("FACEIT Connection Tester")
	w.Resize(fyne.NewSize(960, 600))

	regionBy := map[string]string{}
	for region, srvs := range servers {
		for _, s := range srvs {
			regionBy[s.ID()] = region
		}
	}
	state := stateFromDisabled(servers, prefs.StringList(prefsDisabledKey))
	expanded := expandedFromList(servers, prefs.StringList(prefsExpandedKey))
	if specErr == nil && sel != nil {
		// A valid --test spec replaces the restored selection entirely
		// (persisted on exit like any manual change) and expands the
		// ancestors of the selected servers so the tree shows what runs.
		state = sel
		for key := range expansionForSelection(servers, sel) {
			expanded[key] = true
		}
	}
	u := &ui{
		window:   w,
		prefs:    prefs,
		servers:  servers,
		regionBy: regionBy,
		state:    state,
		expanded: expanded,
		tree:     newResultsTree(),
	}
	u.build()
	if *testSpec != "" {
		switch {
		case specErr != nil:
			u.statusLabel.SetText("Invalid --test spec: " + specErr.Error())
		case !isElevated():
			// build() already hid Test; say the queued spec will run
			// after the user restarts elevated.
			u.statusLabel.SetText("Testing needs administrator rights — Restart as Admin will run the queued test automatically.")
		}
	}
	// Custom-list status, in priority order behind an invalid --test
	// spec: a broken stored list beats the queued-test/elevation notes
	// (it silently changed what would be tested), while a merely active
	// custom list is only worth a note when testing is available.
	if specErr == nil && overrideErr != nil {
		u.statusLabel.SetText("Custom server list is invalid — using built-ins: " + overrideErr.Error() +
			"  (Edit Servers in the actions drawer to fix or drop it.)")
	} else if specErr == nil && isElevated() && overrideActive(prefs) {
		u.statusLabel.SetText("Custom server list active — Edit Servers in the actions drawer to change or drop it.")
	}
	w.SetContent(u.content())
	// Persist selection + expansion once on exit: writing prefs on every
	// checkbox tap made the UI feel sluggish on slow storage.
	w.SetCloseIntercept(func() {
		u.saveAllPrefs()
		w.Close()
	})
	// Auto-start once the event loop is running: fyne.Do queues onto the
	// UI thread, and the short delay lets the first frame render. Skipped
	// when unelevated (ICMP would just fail) or the spec was invalid.
	if *testSpec != "" && specErr == nil && isElevated() {
		go func() {
			time.Sleep(100 * time.Millisecond)
			fyne.Do(func() { u.onTestTap() })
		}()
	}
	// Startup update check: same once-the-loop-is-running pattern as the
	// --test autostart. --force-update-check bypasses the gates so the
	// dialog can be reviewed on dev builds and after "Never".
	if shouldCheckUpdate(version, *testSpec, prefs.Bool(prefsUpdatesDisabledKey), *forceUpdate, updateAssetName()) {
		go u.checkForUpdate(*forceUpdate)
	}
	w.ShowAndRun()
}

// updateHTTPClient is shared by the release lookup and the asset download.
func updateHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// checkForUpdate fetches the latest release and, when an update applies
// (or force is set for UI review), shows the three-button dialog. Runs on
// its own goroutine; every UI touch goes through fyne.Do. Network errors
// fail silent — being offline is not an error condition at startup.
func (u *ui) checkForUpdate(force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := fetchLatestRelease(ctx, updateHTTPClient())
	if err != nil {
		return
	}
	if !force && !updateAvailable(version, rel.TagName) {
		return
	}
	fyne.Do(func() { u.showUpdateDialog(rel, force) })
}

// showUpdateDialog offers Update / Later / Never for the pending release.
// Later dismisses and re-offers next launch; Never persists the opt-out
// immediately (a crash must not resurrect the prompt); Update saves the
// server selection first (the relaunch restores it) and runs the
// download/replace flow with a progress dialog.
func (u *ui) showUpdateDialog(rel releaseInfo, force bool) {
	assetName := updateAssetName()
	if _, ok := assetDownloadURL(rel, assetName); !ok && !force {
		return // vanished between check and dialog; don't offer a dead update
	}
	youHave := version
	if youHave == "" || youHave == "dev" {
		youHave = "a dev build"
	}
	body := widget.NewLabel(fmt.Sprintf("Version %s is available (you have %s).\n\n"+
		"Update downloads the new version, replaces this one, and restarts.",
		rel.TagName, youHave))
	body.Wrapping = fyne.TextWrapWord

	var d *dialog.CustomDialog
	laterBtn := widget.NewButton("Later", func() { d.Hide() })
	neverBtn := widget.NewButton("Never", func() {
		// Written immediately, not via saveAllPrefs-on-close: the whole
		// point of Never is that it sticks.
		u.prefs.SetBool(prefsUpdatesDisabledKey, true)
		d.Hide()
	})
	updateBtn := widget.NewButton("Update", func() {
		d.Hide()
		u.onUpdateConfirm(rel)
	})
	updateBtn.Importance = widget.SuccessImportance

	buttons := container.NewHBox(laterBtn, neverBtn, layout.NewSpacer(), updateBtn)
	content := container.NewBorder(nil, buttons, nil, nil, body)
	d = dialog.NewCustomWithoutButtons("Update available", content, u.window)
	d.Resize(fyne.NewSize(420, 0))
	d.Show()
}

// onUpdateConfirm saves prefs, shows a progress dialog, and runs
// applyUpdate on a goroutine. Any failure keeps the current version
// running and surfaces the error with a manual-download link.
func (u *ui) onUpdateConfirm(rel releaseInfo) {
	u.saveAllPrefs()

	prog := widget.NewProgressBar()
	status := widget.NewLabel("Downloading " + rel.TagName + "…")
	status.Wrapping = fyne.TextWrapWord
	cancelled := false
	closeBtn := widget.NewButton("Cancel", func() { cancelled = true })
	buttons := container.NewHBox(layout.NewSpacer(), closeBtn)
	p := dialog.NewCustomWithoutButtons("Updating", container.NewBorder(nil, buttons, nil, nil,
		container.NewVBox(status, prog)), u.window)
	p.Resize(fyne.NewSize(420, 0))
	p.Show()

	client := updateHTTPClient()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Poll the cancel flag; applyUpdate only aborts at the next ctx
		// check (download read or GitHub round-trip).
		for !cancelled {
			time.Sleep(100 * time.Millisecond)
		}
		cancel()
	}()
	go func() {
		err := applyUpdate(ctx, client, rel, func(frac float64) {
			fyne.Do(func() {
				if frac >= 0 {
					prog.SetValue(frac)
				}
			})
		})
		fyne.Do(func() {
			p.Hide()
			cancel()
			if err == nil {
				// New process is starting (windows/darwin) or exec'd
				// in place (linux); close this one.
				u.window.Close()
				return
			}
			if ctx.Err() != nil && cancelled {
				u.statusLabel.SetText("Update cancelled — still running " + version + ".")
				return
			}
			dialog.ShowError(fmt.Errorf(
				"couldn't install the update: %v\n\n"+
					"Your current version keeps running. Download the new release manually:\n%s",
				err, updateManualURL), u.window)
		})
	}()
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

	// Opens the custom-list editor (editor.go): per-server form on the
	// left-pane selection, Apply persists to prefs, Drop restores the
	// built-ins. Disabled while a run is in flight (setRunning).
	u.editBtn = widget.NewButtonWithIcon("Edit Servers", theme.DocumentCreateIcon(), u.onEditTap)

	u.identifyBtn = widget.NewButtonWithIcon("Identify", theme.SearchIcon(), u.onIdentifyTap)

	// Secondary actions live in a floating drawer above the arrow button
	// (see onDrawerTap); the arrow flips to point down while it is open.
	u.drawerBtn = widget.NewButtonWithIcon("", theme.MoveUpIcon(), u.onDrawerTap)

	// Restart as Admin relaunches this exe elevated via the UAC runas verb
	// (see onRestartTap); only shown when testing is blocked by a missing
	// elevation token, so the button collapses when elevated.
	u.restartBtn = widget.NewButton("Restart as Admin", u.onRestartTap)
	u.restartBtn.Importance = widget.WarningImportance // yellow = needs attention

	if !isElevated() {
		// Test is pointless without elevation — Restart as Admin takes
		// its place in the button row (hidden buttons collapse in HBox).
		u.testBtn.Hide()
		u.statusLabel.SetText("Testing needs administrator rights on Windows — use Restart as Admin.")
	} else {
		u.restartBtn.Hide()
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

	// Tree left, results right; draggable divider. The tree sits in a
	// stack holder so applyServerTable can swap in a rebuilt tree without
	// recreating the whole window content (which would reset the split
	// offset and drawer state).
	u.treeHolder = container.NewStack(container.NewPadded(u.checksScroll))
	split := container.NewHSplit(u.treeHolder, tableArea)
	split.Offset = 0.35

	// Status fills the center (room for long progress messages); actions
	// sit in the right slot so label and buttons share one vertical band.
	// Secondary actions (export/edit/identify) float in a drawer above
	// the arrow button rather than taking inline space — the bottom-right
	// corner was getting crowded.
	bottom := container.NewBorder(nil, nil, nil,
		container.NewHBox(u.drawerBtn, u.restartBtn, u.testBtn), u.statusLabel)
	main := container.NewBorder(nil, bottom, nil, nil, split)

	// Floating drawer: a transparent catcher behind the action column
	// dismisses the drawer on any tap outside the buttons (the tap search
	// skips non-tappable subtrees, so the column's empty areas fall
	// through to the catcher). The column stacks vertically, bottom-aligned
	// and right-aligned, raised above the control row by a gap sized to it.
	// The buttons sit on an overlay-colored backdrop (the same fill theme
	// uses for popups) so the drawer reads as a container, not loose
	// floating controls.
	catcher := newTapCatcher(u.hideDrawer)
	actions := container.NewPadded(container.NewVBox(u.exportBtn, u.editBtn, u.identifyBtn))
	backdrop := canvas.NewRectangle(theme.Color(theme.ColorNameOverlayBackground))
	backdrop.CornerRadius = theme.Size(theme.SizeNamePopupRadius)
	panel := container.NewStack(backdrop, actions)
	rightCol := container.NewBorder(nil, panel, nil, nil, nil)
	gap := container.NewGridWrap(
		fyne.NewSize(1, u.drawerBtn.MinSize().Height+2*theme.Padding()),
		widget.NewLabel(""))
	cluster := container.NewBorder(nil, gap, nil, rightCol, nil)
	u.drawerLayer = container.NewStack(catcher, cluster)
	u.drawerLayer.Hide()

	return container.NewStack(main, u.drawerLayer)
}

// onDrawerTap toggles the secondary-actions drawer. The arrow icon points
// the way the drawer will move: up to open, down while open. Dismissal
// always goes through hideDrawer (the catcher taps it directly), so the
// icon can never get out of sync with the drawer's actual visibility.
func (u *ui) onDrawerTap() {
	if u.drawerLayer.Visible() {
		u.hideDrawer()
		return
	}
	u.drawerLayer.Show()
	u.drawerBtn.SetIcon(theme.MoveDownIcon())
}

func (u *ui) hideDrawer() {
	u.drawerLayer.Hide()
	u.drawerBtn.SetIcon(theme.MoveUpIcon())
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
		{"Server", nameWidth}, {"Ping (p1)", metricColWidth}, {"Ping (med)", metricColWidth},
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

// applyServerTable swaps in a new server table (Apply or Drop in the
// editor): rebuilds the selection tree, re-derives selection and
// expansion against the new ID set, and clears results — old rows would
// reference servers that no longer exist. Callers must already have
// persisted the table; this only updates the live session.
func (u *ui) applyServerTable(servers map[string][]Server, status string) {
	u.servers = servers
	u.regionBy = map[string]string{}
	for region, srvs := range servers {
		for _, s := range srvs {
			u.regionBy[s.ID()] = region
		}
	}
	// Both persisted lists are active-table-relative: unknown IDs/keys
	// are ignored on load and pruned on save, so surviving checkboxes and
	// expansions carry over and new servers default to checked/collapsed.
	u.state = stateFromDisabled(u.servers, u.prefs.StringList(prefsDisabledKey))
	u.expanded = expandedFromList(u.servers, expandedToList(u.servers, u.expanded))

	u.allChecks = nil
	u.allExpanders = nil
	u.checksScroll = container.NewVScroll(u.buildTree())
	u.treeHolder.RemoveAll()
	u.treeHolder.Add(container.NewPadded(u.checksScroll))

	u.tree = newResultsTree()
	u.rows = nil
	u.results = nil
	u.refreshTableLayout()
	u.exportBtn.Disable()
	u.statusLabel.SetText(status)
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

// onRestartTap confirms, then relaunches this exe elevated via the UAC
// runas verb and closes the unelevated instance. Cancel keeps the old
// instance running — everything except Test works without elevation.
func (u *ui) onRestartTap() {
	d := dialog.NewConfirm("Restart as administrator",
		"Testing sends raw ICMP sockets, which require admin on Windows.\n\n"+
			"Windows will ask you to approve the restart. Your current selections "+
			"are saved first.",
		func(ok bool) {
			if !ok {
				return
			}
			// Prefs are otherwise only saved from SetCloseIntercept; save
			// explicitly so the elevated relaunch restores current state.
			u.saveAllPrefs()
			if err := elevateAndRestart(); err != nil {
				if errors.Is(err, errElevationCancelled) {
					u.statusLabel.SetText("Elevation cancelled — Restart as Admin to enable testing.")
				} else {
					u.statusLabel.SetText("Restart failed: " + err.Error())
				}
				return
			}
			// New elevated instance is starting; close this one. The
			// SetCloseIntercept runs saveAllPrefs again (harmless).
			u.window.Close()
		}, u.window)
	d.Show()
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
		u.editBtn.Disable() // the table can't swap under a live run
	} else {
		u.testBtn.Importance = widget.SuccessImportance // green = start
		u.testBtn.SetText("Test")
		if isElevated() {
			u.testBtn.Enable()
		}
		if len(u.results) > 0 {
			u.exportBtn.Enable()
		}
		u.editBtn.Enable()
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
// (SuccessImportance) below the warn thresholds, yellow (WarningImportance)
// at-or-above warn / at-or-below the CS2 HUD thresholds, and red
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
	jitterCell := tableCell{formatMS(r.Jitter), gradeMS(r.Jitter, jitterThreshold, jitterWarnThreshold, rttOK), false}
	lossCell := tableCell{fmt.Sprintf("%.2f%%", r.LossPercent), gradePct(r.LossPercent), false}
	if scrubbed {
		jitterCell = tableCell{"n/a", widget.MediumImportance, false}
		lossCell = tableCell{"n/a", widget.MediumImportance, false}
	}
	return []tableCell{
		nameCell,
		{formatMS(r.P1), gradeMS(r.P1, pingThreshold, pingWarnThreshold, rttOK), false},
		{formatMS(r.Median), gradeMS(r.Median, pingThreshold, pingWarnThreshold, rttOK), false},
		{formatMS(r.P99), gradeMS(r.P99, pingThreshold, pingWarnThreshold, rttOK), false},
		jitterCell,
		lossCell,
	}
}

// gradeMS returns DangerImportance strictly above threshold, WarningImportance
// at-or-above warn; ungraded (Medium) when hasSamples is false.
func gradeMS(d, threshold, warn time.Duration, hasSamples bool) widget.Importance {
	if !hasSamples {
		return widget.MediumImportance
	}
	if d > threshold {
		return widget.DangerImportance
	}
	if d >= warn {
		return widget.WarningImportance
	}
	return widget.SuccessImportance
}

func gradePct(p float64) widget.Importance {
	if p > lossThreshold {
		return widget.DangerImportance
	}
	if p >= lossWarnThreshold {
		return widget.WarningImportance
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

// tapCatcher is a transparent widget that runs onTap when tapped. It backs
// the floating action drawer: the tap search skips non-tappable subtrees,
// so any tap that misses the drawer's buttons lands here and dismisses it.
type tapCatcher struct {
	widget.BaseWidget
	onTap func()
}

func newTapCatcher(onTap func()) *tapCatcher {
	t := &tapCatcher{onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapCatcher) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

func (t *tapCatcher) Tapped(*fyne.PointEvent) { t.onTap() }

func (t *tapCatcher) TappedSecondary(*fyne.PointEvent) { t.onTap() }
