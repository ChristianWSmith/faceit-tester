package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"net/http"
	"net/url"
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
	check     *widget.Check
	cities    map[string]*cityTree
	order     []string
	block     fyne.CanvasObject // whole region block in the root VBox (filter show/hide)
	box       *fyne.Container   // cities VBox (shown/hidden by expander)
	expanders []*widget.Button
	nodeKey   string
}

// cityTree holds the widgets for one city row and its providers.
type cityTree struct {
	check     *widget.Check
	providers map[string]*widget.Check // server ID → check
	ids       []string                 // server IDs in display order
	block     fyne.CanvasObject        // indented city block in the region's citiesBox
	box       *fyne.Container          // providers VBox
	expanders []*widget.Button
	nodeKey   string
}

type ui struct {
	window fyne.Window
	prefs  fyne.Preferences

	checksScroll  *container.Scroll
	treeHolder    *fyne.Container // wraps checksScroll so the tree can be rebuilt in place
	treePane      *fyne.Container // filter + bulk buttons + scroll; swapped by applyServerTable
	filterEntry   *widget.Entry
	countLabel    *widget.Label // "N of M selected" (+ match count while filtering)
	matchCount    int           // leaves shown by the current filter (UI-thread only)
	allBtn        *widget.Button
	noneBtn       *widget.Button
	invertBtn     *widget.Button
	testBtn       *widget.Button
	restartBtn    *widget.Button
	exportBtn     *widget.Button
	copyBtn       *widget.Button
	editBtn       *widget.Button
	identifyBtn   *widget.Button
	aboutBtn      *widget.Button
	drawerBtn     *widget.Button  // toggles the secondary-actions drawer
	drawerLayer   *fyne.Container // floating drawer: catcher + action column
	statusLabel   *widget.Label
	progress      *widget.ProgressBar // thin full-width strip under the bottom row
	expectedSends int                 // current server's planned probe count (UI-thread only)
	table         *widget.Table
	headerRow     *fyne.Container // results column headers; col 0 width tracks content
	split         *container.Split

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
	syncing   bool
	filtering bool // filter is active: expanders no-op, parent checks disable

	rows       [][]tableCell
	rowRefs    []rowRef        // parallel to rows: leaf outcomes / branch identity for taps
	tree       *resultsTree    // hierarchical results model backing rows
	results    []ServerOutcome // full outcomes for CSV export
	lastRun    time.Time       // start time of the most recent run (banner + CSV tested_at)
	running    bool
	cancelling bool
	cancel     context.CancelFunc

	// Run-progress state, written only inside fyne.Do closures.
	runStarted time.Time
	eta        time.Duration
	doneCount  int
	totalCount int
	activeID   string            // server ID currently being probed (tree marker)
	checkTexts map[string]string // original leaf check texts, restored when the marker moves

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
	w.Resize(savedWindowSize(prefs, fyne.NewSize(960, 600)))

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
		// selected servers' regions so the tri-state cities are visible.
		// Cities stay collapsed: players pick a location, not a provider.
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
		go u.checkForUpdate(false)
	}
	registerShortcuts(w, u)
	w.ShowAndRun()
}

// registerShortcuts wires the canvas keyboard shortcuts: Enter starts or
// cancels a test, Esc cancels a run or closes the drawer, Ctrl/Cmd+C
// copies the results summary.
//
// Fyne 2.8's driver dispatch model (glfw window.go) dictates the shape:
// bare keys are never turned into canvas shortcuts — they go to the
// focused widget's TypedKey and only fall through to canvas.SetOnTypedKey
// when nothing is focused. Modifier chords become concrete shortcut
// types (Ctrl/Cmd+C → fyne.ShortcutCopy, dispatched by ShortcutName via
// canvas.AddShortcut) but only when the focused widget is not
// Shortcutable — entries consume their own copy, which must keep working.
// Two guards make the bare-key path safe: an entry focus check (typing
// must not fire Test) and an overlay check (a dialog must not run Enter
// behind itself — dialog buttons Focus(nil) on tap, so focus alone can't
// tell). Callers keep focus nil where possible: buttons self-unfocus on
// tap, and the tree/table handlers below unfocus explicitly.
func registerShortcuts(w fyne.Window, u *ui) {
	sc := w.Canvas()
	dialogOpen := func() bool { return sc.Overlays().Top() != nil }
	entryFocused := func() bool {
		_, ok := sc.Focused().(*widget.Entry)
		return ok
	}
	sc.SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if entryFocused() || dialogOpen() {
			return
		}
		switch ev.Name {
		case fyne.KeyReturn, fyne.KeyEnter:
			if !isElevated() {
				return // the Test button is hidden unelevated; don't probe
			}
			u.onTestTap()
		case fyne.KeyEscape:
			switch {
			case u.running && u.cancel != nil:
				u.cancelRun()
			case u.drawerLayer.Visible():
				u.hideDrawer()
			}
		}
	})
	// The driver constructs &fyne.ShortcutCopy{} for Ctrl/Cmd+C; matching
	// by concrete type registers the same ShortcutName ("Copy"). Reached
	// only when the focused widget isn't Shortcutable (table, button,
	// nothing) — a focused Entry handles copy natively before this.
	sc.AddShortcut(&fyne.ShortcutCopy{}, func(fyne.Shortcut) {
		if entryFocused() {
			return
		}
		u.onCopySummaryTap()
	})
}

// unfocus clears canvas focus so subsequent bare keys reach the canvas
// SetOnTypedKey handlers. Button taps already do this (button.go:204);
// checks and the results table focus themselves on tap, so their
// handlers call this to compensate.
func (u *ui) unfocus() {
	if u.window == nil {
		return
	}
	u.window.Canvas().Focus(nil)
}

// updateHTTPClient is shared by the release lookup and the asset download.
func updateHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// checkForUpdate fetches the latest release and, when an update applies
// (or manual is set from the About dialog), shows the three-button
// dialog. Runs on its own goroutine; every UI touch goes through fyne.Do.
// Startup network errors fail silent — being offline is not an error
// condition at launch — but a manual check reports them: the user asked.
func (u *ui) checkForUpdate(manual bool) {
	if manual {
		fyne.Do(func() { u.statusLabel.SetText("Checking for updates…") })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := fetchLatestRelease(ctx, updateHTTPClient())
	if err != nil {
		if manual {
			fyne.Do(func() { u.statusLabel.SetText("Update check failed: " + err.Error()) })
		}
		return
	}
	if !manual && !updateAvailable(version, rel.TagName) {
		return
	}
	if manual && !updateAvailable(version, rel.TagName) {
		fyne.Do(func() { u.statusLabel.SetText("You're on the latest release (" + versionDisplay() + ").") })
		return
	}
	fyne.Do(func() { u.showUpdateDialog(rel, manual) })
}

// versionDisplay renders the version for UI text; unstamped dev builds
// read as "dev build" rather than the raw sentinel.
func versionDisplay() string {
	if version == "" || version == "dev" {
		return "dev build"
	}
	return version
}

// showUpdateDialog offers Update / Later / Never for the pending release.
// Later dismisses and re-offers next launch; Never persists the opt-out
// immediately (a crash must not resurrect the prompt); Update saves the
// server selection first (the relaunch restores it) and runs the
// download/replace flow with a progress dialog. When the release carries
// body text, a truncated preview plus a full-notes hyperlink shows what
// changed.
func (u *ui) showUpdateDialog(rel releaseInfo, force bool) {
	assetName := updateAssetName()
	if _, ok := assetDownloadURL(rel, assetName); !ok && !force {
		return // vanished between check and dialog; don't offer a dead update
	}
	youHave := versionDisplay()
	bodyLabel := widget.NewLabel(fmt.Sprintf("Version %s is available (you have %s).\n\n"+
		"Update downloads the new version, replaces this one, and restarts.",
		rel.TagName, youHave))
	bodyLabel.Wrapping = fyne.TextWrapWord

	var content fyne.CanvasObject = bodyLabel
	if notes, notesURL := releaseNotesPreview(rel); notes != "" || notesURL != nil {
		kids := []fyne.CanvasObject{bodyLabel}
		if notes != "" {
			preview := widget.NewLabel(notes)
			preview.Wrapping = fyne.TextWrapWord
			scroll := container.NewVScroll(preview)
			scroll.SetMinSize(fyne.NewSize(380, 110))
			kids = append(kids, widget.NewLabel(""), scroll)
		}
		if notesURL != nil {
			kids = append(kids, widget.NewHyperlink("View full release notes", notesURL))
		}
		content = container.NewVBox(kids...)
	}

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
	wrapped := container.NewBorder(nil, buttons, nil, nil, content)
	d = dialog.NewCustomWithoutButtons("Update available", wrapped, u.window)
	d.Resize(fyne.NewSize(420, 0))
	d.Show()
}

// releaseNotesPreview extracts a truncated preview of the release body
// and the full-notes URL for the update dialog. Both may be empty/nil
// for minimal releases.
func releaseNotesPreview(rel releaseInfo) (string, *url.URL) {
	preview := truncateMarkdown(rel.Body, 600)
	var notesURL *url.URL
	if rel.HTMLURL != "" {
		if u, err := url.Parse(rel.HTMLURL); err == nil {
			notesURL = u
		}
	}
	return preview, notesURL
}

// truncateMarkdown cuts s at roughly n runes on a word boundary with a
// trailing ellipsis — best-effort preview; the full notes are a click away.
func truncateMarkdown(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := n
	for cut > 0 && runes[cut] != ' ' && runes[cut] != '\n' {
		cut--
	}
	if cut < n/2 {
		cut = n // no sensible word boundary; hard cut
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
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
		err := applyUpdate(ctx, client, rel, func(up updateProgress) {
			fyne.Do(func() {
				switch up.Phase {
				case "extract":
					status.SetText("Extracting…")
					prog.SetValue(1)
				case "install":
					status.SetText("Installing — the app will restart when ready…")
					prog.SetValue(1)
				default: // download
					if up.Total > 0 {
						status.SetText(fmt.Sprintf("Downloading %s — %.1f / %.1f MB", rel.TagName,
							float64(up.Written)/1e6, float64(up.Total)/1e6))
						prog.SetValue(float64(up.Written) / float64(up.Total))
					} else {
						status.SetText(fmt.Sprintf("Downloading %s — %.1f MB", rel.TagName,
							float64(up.Written)/1e6))
						prog.Hide() // unknown length: byte counter only
					}
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
	u.treePane = u.buildTreePane()

	u.statusLabel = widget.NewLabel("Select servers, then press Test. Export, Edit and Identify live under Actions.")
	u.statusLabel.Wrapping = fyne.TextWrapWord

	u.progress = widget.NewProgressBar()
	u.progress.Hide()

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
	// Tapping a region/city row toggles its children; tapping a leaf
	// copies the row or shows its full error (onResultRowTap). Unselect
	// immediately so no row stays highlighted, and unfocus: Table.Tapped
	// focuses the table, which would swallow canvas bare-key shortcuts.
	u.table.OnSelected = func(id widget.TableCellID) {
		u.onResultRowTap(id)
		u.table.UnselectAll()
		u.unfocus()
	}

	u.testBtn = widget.NewButton("Test", u.onTestTap)
	u.testBtn.Importance = widget.SuccessImportance // green = start

	u.exportBtn = widget.NewButtonWithIcon("Export CSV", theme.DocumentSaveIcon(), u.onExportTap)
	u.exportBtn.Disable()

	// Copy Summary puts the whole run on the clipboard as an aligned
	// text table — the paste-into-Discord companion to Export CSV.
	u.copyBtn = widget.NewButtonWithIcon("Copy Summary", theme.ContentCopyIcon(), u.onCopySummaryTap)
	u.copyBtn.Disable()

	// Opens the custom-list editor (editor.go): per-server form on the
	// left-pane selection, Apply persists to prefs, Drop restores the
	// built-ins. Disabled while a run is in flight (setRunning).
	u.editBtn = widget.NewButtonWithIcon("Edit Servers", theme.DocumentCreateIcon(), u.onEditTap)

	u.identifyBtn = widget.NewButtonWithIcon("Identify", theme.SearchIcon(), u.onIdentifyTap)

	// About shows the version and offers a manual update check (see
	// onAboutTap) — the in-app recovery path after choosing "Never".
	u.aboutBtn = widget.NewButtonWithIcon("About", theme.InfoIcon(), u.onAboutTap)

	// Secondary actions live in a floating drawer above the arrow button
	// (see onDrawerTap); the arrow flips to point down while it is open.
	// The "Actions" label is the drawer's discoverability affordance —
	// an icon-only button in the corner went unnoticed.
	u.drawerBtn = widget.NewButtonWithIcon("Actions", theme.MoveUpIcon(), u.onDrawerTap)

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

	tableArea := container.NewBorder(u.headerRow, nil, nil, nil, u.table)

	// Tree left, results right; draggable divider. The tree sits in a
	// stack holder so applyServerTable can swap in a rebuilt tree without
	// recreating the whole window content (which would reset the split
	// offset and drawer state).
	u.treeHolder = container.NewStack(container.NewPadded(u.treePane))
	u.split = container.NewHSplit(u.treeHolder, tableArea)
	u.split.Offset = 0.35
	if permille := u.prefs.Int(prefsSplitOffsetKey); permille > 0 {
		u.split.Offset = clampSplitOffset(permille)
	}

	// Status fills the center (room for long progress messages); actions
	// sit in the right slot so label and buttons share one vertical band.
	// Secondary actions (export/copy/edit/identify/about) float in a
	// drawer above the arrow button rather than taking inline space —
	// the bottom-right corner was getting crowded. The progress bar is a
	// thin full-width strip *below* the row: stacking it beside the
	// buttons stretched their band vertically.
	row := container.NewBorder(nil, nil, nil,
		container.NewHBox(u.drawerBtn, u.restartBtn, u.testBtn), u.statusLabel)
	bottom := container.NewVBox(row, u.progress)
	main := container.NewBorder(nil, bottom, nil, nil, u.split)

	// Floating drawer: a transparent catcher behind the action column
	// dismisses the drawer on any tap outside the buttons (the tap search
	// skips non-tappable subtrees, so the column's empty areas fall
	// through to the catcher). The column stacks vertically, bottom-aligned
	// and right-aligned, raised above the control row by a gap sized to it.
	// The buttons sit on an overlay-colored backdrop (the same fill theme
	// uses for popups) so the drawer reads as a container, not loose
	// floating controls.
	catcher := newTapCatcher(u.hideDrawer)
	actions := container.NewPadded(container.NewVBox(u.exportBtn, u.copyBtn, u.editBtn, u.identifyBtn, u.aboutBtn))
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
// the way the drawer will move: up to open, down while open. The button
// text stays "Actions" in both states — swapping to "Hide" changed the
// button's width and made the whole bottom row jump. Dismissal always
// goes through hideDrawer (the catcher taps it directly), so the icon
// can never get out of sync with the drawer's actual visibility.
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

// headerSortMode maps each results-table column to the sortMode its
// header cell toggles (0 = no sort for that column).
var headerSortMode = map[int]sortMode{
	0: sortName,
	1: sortWorstP1,
	2: sortWorstMedian,
	3: sortWorstP99,
	4: sortWorstJitter,
	5: sortWorstLoss,
}

// rebuildHeader recreates the header labels as sort-toggle buttons,
// sizing the Server column to nameWidth so it stays aligned with the
// table's dynamic first column. An active sort shows its direction (▾
// descending/worst-first, ▴ ascending/best-first). A trailing "?" button
// opens the color legend; it sits after the six aligned cells and does
// not affect column spacing.
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
	for i, h := range headers {
		col := i
		label := h.text
		if u.tree != nil && u.tree.mode == headerSortMode[col] {
			if u.tree.desc {
				label += " ▾"
			} else {
				label += " ▴"
			}
		}
		btn := widget.NewButton(label, func() { u.toggleSort(col) })
		btn.Importance = widget.LowImportance
		btn.Alignment = widget.ButtonAlignLeading
		u.headerRow.Add(container.NewGridWrap(fyne.NewSize(h.width, btn.MinSize().Height), btn))
	}
	// Spacer pins the legend to the row's right edge when the pane is
	// wider than the columns. It contributes nothing to MinSize (spacers
	// are skipped there), so the pane/window min is exactly the six
	// cells + the button — no resize-on-layout risk.
	u.headerRow.Add(layout.NewSpacer())
	legendBtn := widget.NewButtonWithIcon("", theme.QuestionIcon(), u.showLegend)
	legendBtn.Importance = widget.LowImportance
	u.headerRow.Add(legendBtn)
}

// toggleSort cycles one column through three states: inactive → first
// direction → opposite direction → inactive. First direction is
// descending (worst-first) for metric columns — the app's whole point
// is surfacing problem locations — and ascending for Server (A→Z).
// The sort ranks a region's cities by their worst-case aggregate;
// providers keep probe order and regions keep RegionOrder.
func (u *ui) toggleSort(col int) {
	if u.tree == nil {
		return
	}
	mode := headerSortMode[col]
	firstDesc := mode != sortName
	switch {
	case u.tree.mode != mode:
		u.tree.setSort(mode, firstDesc)
	case u.tree.desc == firstDesc:
		u.tree.setSort(mode, !firstDesc)
	default:
		u.tree.setSort(sortProbeOrder, false)
	}
	u.rows, u.rowRefs = u.tree.flattenWithRefs()
	u.rebuildHeader(u.nameColWidth)
	u.refreshTableLayout()
}

// legendContent builds the legend dialog body: color thresholds, then
// a compact glossary. Section gaps are 6px GridWrap rows — full-height
// empty labels made the spacing read as double line breaks and pushed
// the content past the scroll viewport (a scrollbar on open).
func legendContent() fyne.CanvasObject {
	line := func(text string, imp widget.Importance) *widget.Label {
		l := widget.NewLabel(text)
		l.Importance = imp
		return l
	}
	gap := func() fyne.CanvasObject {
		return container.NewGridWrap(fyne.NewSize(1, 6), widget.NewLabel(""))
	}
	return container.NewVBox(
		widget.NewLabel("Color thresholds (CS2 HUD)"),
		line(fmt.Sprintf("  green   ping < %dms · jitter < %dms · loss < %g%%",
			int(pingWarnThreshold/time.Millisecond), int(jitterWarnThreshold/time.Millisecond),
			lossWarnThreshold), widget.SuccessImportance),
		line(fmt.Sprintf("  yellow  ping ≤ %dms · jitter ≤ %dms · loss ≤ %g%%",
			int(pingThreshold/time.Millisecond), int(jitterThreshold/time.Millisecond),
			lossThreshold), widget.WarningImportance),
		line("  red     above those", widget.DangerImportance),
		gap(),
		widget.NewLabel("Ping columns are p1 / median / p99 percentiles of reply RTTs.\n"+
			"Bold region/city rows are worst-case aggregates."),
		gap(),
		widget.NewLabel("  n/a     DDoS-scrubbed endpoint — RTT is valid; loss and\n"+
			"          jitter are not measurable."),
		widget.NewLabel("  error   probe failed — tap the row for the full error\n"+
			"          (Export CSV carries it too)."),
	)
}

// showLegend explains the green/yellow/red grading and the n/a scrubbed
// convention — the thresholds live in code comments and AGENTS.md, which
// end users never see. Color rows use the same Importance mechanism the
// table uses, so the legend is the palette. Loss thresholds are already
// in percent units (lossWarnThreshold == 1.0 means 1%), so they print
// directly — never ×100. The scroll viewport is sized to the body's
// natural height so the dialog opens without a scrollbar; the scroll
// itself remains only as a safety net on tiny screens.
func (u *ui) showLegend() {
	body := legendContent()
	scroll := container.NewVScroll(body)
	scroll.SetMinSize(fyne.NewSize(400, body.MinSize().Height))
	closeBtn := widget.NewButton("Close", nil)
	buttons := container.NewHBox(layout.NewSpacer(), closeBtn)
	d := dialog.NewCustomWithoutButtons("Legend", container.NewBorder(nil, buttons, nil, nil, scroll), u.window)
	closeBtn.OnTapped = func() { d.Hide() }
	d.Resize(fyne.NewSize(440, 0))
	d.Show()
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

// buildTreePane assembles the left pane: filter entry, bulk-select
// buttons + selected count, then the scrollable check tree. The pane is
// rebuilt wholesale by applyServerTable; the widgets are recreated so
// stale closures never outlive a table swap.
func (u *ui) buildTreePane() *fyne.Container {
	u.filterEntry = widget.NewEntry()
	u.filterEntry.SetPlaceHolder("Filter servers…")
	u.filterEntry.OnChanged = func(string) { u.applyFilter() }

	u.allBtn = widget.NewButton("All", func() { u.setAllSelected(true) })
	u.noneBtn = widget.NewButton("None", func() { u.setAllSelected(false) })
	u.invertBtn = widget.NewButton("Invert", u.invertSelection)
	for _, b := range []*widget.Button{u.allBtn, u.noneBtn, u.invertBtn} {
		b.Importance = widget.LowImportance
	}
	u.countLabel = widget.NewLabel("")
	u.countLabel.Importance = widget.MediumImportance
	// Lock the label's cell to a realistic widest text (GridWrap forces
	// a fixed size; Fyne 2.8 has no SetMinSize). A label that grew with
	// match numbers changed the pane's min width and made the whole
	// window resize on every keystroke.
	sample := "99 match · 99 of 99 selected"
	cellW := fyne.MeasureText(sample, theme.TextSize(), fyne.TextStyle{}).Width
	cellH := fyne.MeasureText("X", theme.TextSize(), fyne.TextStyle{}).Height
	countCell := container.NewGridWrap(fyne.NewSize(cellW+4, cellH), u.countLabel)
	u.updateSelectedCount()

	bulkRow := container.NewHBox(u.allBtn, u.noneBtn, u.invertBtn,
		layout.NewSpacer(), countCell)
	// NewScroll (ScrollBoth), NOT NewVScroll: a vertical-only scroll
	// folds its content's *width* into its MinSize (scroller.go's
	// MinSize switch), and the window driver's EnsureMinSize expands the
	// OS window whenever content min grows — so a filter that
	// force-expanded a wide branch resized the whole window per
	// keystroke. ScrollBoth's min case ignores content entirely (both
	// dimensions), keeping the pane min fixed; long provider names get a
	// horizontal scrollbar instead of moving the window.
	u.checksScroll = container.NewScroll(u.buildTree())
	u.checksScroll.SetMinSize(fyne.NewSize(260, 0))
	// Border, not VBox: only Border stretches its center child to fill
	// the pane — the controls pin to the top and the check tree fills
	// all remaining height (a VBox sized the scroll to its min height,
	// squashing the tree against the top).
	top := container.NewVBox(u.filterEntry, bulkRow)
	return container.NewBorder(top, nil, nil, nil, u.checksScroll)
}

// setAllSelected checks or unchecks every server and refreshes the
// tri-state tree. syncing holds off the per-check OnChanged cascades.
func (u *ui) setAllSelected(v bool) {
	u.syncing = true
	for id := range u.state {
		u.state[id] = v
	}
	u.refreshChecksFromState()
	u.syncing = false
	u.updateSelectedCount()
}

// invertSelection flips every server's checked state.
func (u *ui) invertSelection() {
	u.syncing = true
	for id := range u.state {
		u.state[id] = !u.state[id]
	}
	u.refreshChecksFromState()
	u.syncing = false
	u.updateSelectedCount()
}

// refreshChecksFromState re-applies u.state to every leaf check and
// recomputes both the city and region parent tri-states (recomputeRegion
// alone skips the city checks, leaving them stale after bulk flips).
// Callers hold syncing.
func (u *ui) refreshChecksFromState() {
	for _, rt := range u.trees {
		for _, ct := range rt.cities {
			for _, id := range ct.ids {
				ct.providers[id].SetChecked(u.state[id])
			}
		}
	}
	for _, region := range u.regionOrder {
		for _, city := range u.trees[region].order {
			u.recomputeCity(region, city)
		}
		u.recomputeRegion(region)
	}
}

// updateSelectedCount refreshes the "N of M selected" label so the
// user knows the run's scope before committing 10s × N. While a filter
// is active it also shows the match count. The label's min width is
// locked (buildTreePane) so the varying text can't resize the window.
func (u *ui) updateSelectedCount() {
	if u.countLabel == nil {
		return
	}
	total, checked := 0, 0
	for _, v := range u.state {
		total++
		if v {
			checked++
		}
	}
	if u.filtering {
		if u.matchCount == 0 {
			u.countLabel.SetText("no matches")
		} else {
			u.countLabel.SetText(fmt.Sprintf("%d match · %d of %d selected", u.matchCount, checked, total))
		}
		return
	}
	u.countLabel.SetText(fmt.Sprintf("%d of %d selected", checked, total))
}

// applyFilter hides tree nodes that don't match the filter entry and
// force-expands visible branches; clearing the filter restores the
// persisted expansion. Whole city/region blocks show/hide (hiding only
// the header left an indent-wrapper gap per filtered-out city). Parent
// checks disable while filtering so a cascade can't select unseen
// servers. The match count folds into the existing "N of M selected"
// label — a separate label changed its min size and resized the window.
func (u *ui) applyFilter() {
	filter := strings.TrimSpace(u.filterEntry.Text)
	u.filtering = filter != ""
	u.matchCount = 0
	for region, rt := range u.trees {
		regionVisible := false
		for city, ct := range rt.cities {
			cityVisible := false
			for _, id := range ct.ids {
				show := filter == "" || u.matchesFilter(id, region, filter)
				chk := ct.providers[id]
				if show {
					chk.Show()
					cityVisible = true
					u.matchCount++
				} else {
					chk.Hide()
				}
			}
			if cityVisible || filter == "" {
				ct.block.Show()
				if u.filtering {
					// Recompute the tri-state without firing OnChanged
					// (setTriState assigns fields directly); a SetChecked
					// here would cascade over hidden servers.
					u.recomputeCity(region, city)
					ct.check.Disable()
					ct.box.Show()
					for _, exp := range ct.expanders {
						exp.Disable()
					}
				} else {
					ct.check.Enable()
					u.syncExpander(ct.nodeKey, ct.box, ct.expanders)
				}
				regionVisible = true
			} else {
				ct.block.Hide()
			}
		}
		if regionVisible || filter == "" {
			rt.block.Show()
			if u.filtering {
				u.recomputeRegion(region)
				rt.check.Disable()
				rt.box.Show()
				for _, exp := range rt.expanders {
					exp.Disable()
				}
			} else {
				rt.check.Enable()
				u.syncExpander(rt.nodeKey, rt.box, rt.expanders)
			}
		} else {
			rt.block.Hide()
		}
	}
	u.updateSelectedCount()
}

// matchesFilter reports whether the server with this ID matches the
// filter against its region, city, provider, or host (case-insensitive
// substring).
func (u *ui) matchesFilter(id, region, filter string) bool {
	f := strings.ToLower(filter)
	if strings.Contains(strings.ToLower(region), f) {
		return true
	}
	for _, srvs := range u.servers {
		for _, s := range srvs {
			if s.ID() != id {
				continue
			}
			return strings.Contains(strings.ToLower(s.City), f) ||
				strings.Contains(strings.ToLower(s.Provider), f) ||
				strings.Contains(strings.ToLower(s.Host), f)
		}
	}
	return false
}

func (u *ui) buildRegion(region string) fyne.CanvasObject {
	servers := u.servers[region]
	groups := groupByCity(servers)

	rt := &regionTree{cities: map[string]*cityTree{}, order: make([]string, 0, len(groups)),
		nodeKey: regionNodeKey(region)}
	u.trees[region] = rt

	regionCheck := widget.NewCheck(fmt.Sprintf("%s (%d)", region, len(servers)), func(checked bool) {
		if u.syncing {
			return
		}
		u.unfocus() // Check.Tapped focuses itself; keep canvas bare keys alive
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
		u.updateSelectedCount()
	})
	rt.check = regionCheck

	citiesBox := container.NewVBox()
	rt.box = citiesBox
	regionExpander := u.newExpander(regionNodeKey(region), citiesBox)
	rt.expanders = append(rt.expanders, regionExpander.btn)

	for _, group := range groups {
		// buildCity returns the city block already indented (ct.block) —
		// wrapping it again would leave the outer indent's spacer visible
		// when the filter hides the block, ~5px of gap per hidden city.
		citiesBox.Add(u.buildCity(region, group, rt))
	}

	// Initial tri-state from restored selection (cities registered above).
	u.recomputeRegion(region)

	u.allChecks = append(u.allChecks, regionCheck)
	// Plain HBox header: only the ▸/▾ button toggles expansion — a
	// whole-row tap catcher highlighted like a button and was
	// inconsistent with the results table, which toggles on any cell.
	block := container.NewVBox(container.NewHBox(regionCheck, regionExpander.btn), indent(citiesBox))
	rt.block = block
	return block
}

func (u *ui) buildCity(region string, group CityGroup, rt *regionTree) fyne.CanvasObject {
	ct := &cityTree{providers: map[string]*widget.Check{}, ids: make([]string, 0, len(group.Servers)),
		nodeKey: cityNodeKey(region, group.City)}
	rt.cities[group.City] = ct
	rt.order = append(rt.order, group.City)

	var cityCheck *widget.Check
	cityCheck = widget.NewCheck(fmt.Sprintf("%s (%d)", group.City, len(group.Servers)), func(checked bool) {
		if u.syncing {
			return
		}
		u.unfocus()
		u.syncing = true
		for _, id := range ct.ids {
			u.state[id] = checked
			ct.providers[id].SetChecked(checked)
		}
		u.syncing = false
		setTriState(cityCheck, checked, false)
		u.recomputeRegion(region)
		u.updateSelectedCount()
	})
	ct.check = cityCheck

	providersBox := container.NewVBox()
	ct.box = providersBox
	cityExpander := u.newExpander(cityNodeKey(region, group.City), providersBox)
	ct.expanders = append(ct.expanders, cityExpander.btn)

	for _, s := range group.Servers {
		server := s
		chk := widget.NewCheck(server.Provider, func(checked bool) {
			if u.syncing {
				return
			}
			u.unfocus()
			u.state[server.ID()] = checked
			u.recomputeCity(region, server.City)
			u.recomputeRegion(region)
			u.updateSelectedCount()
		})
		chk.Checked = u.state[server.ID()]
		if u.checkTexts == nil {
			u.checkTexts = map[string]string{}
		}
		u.checkTexts[server.ID()] = server.Provider
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
	vbox := container.NewVBox(container.NewHBox(cityCheck, cityExpander.btn), indent(providersBox))
	// The outer indent is what the filter shows/hides: hiding the whole
	// block leaves no remnant gap between the remaining siblings (a
	// hidden header with visible-empty padding inside a still-shown
	// indent wrapper stacked ~5px per filtered-out city).
	ct.block = indent(vbox)
	return ct.block
}

// syncExpander restores one node's expander buttons + box visibility
// from the live u.expanded map (the source of truth the toggle closures
// write to) — used when a filter clears. Glyphs, buttons, and box state
// all re-derive from the map so nothing drifts.
func (u *ui) syncExpander(nodeKey string, box *fyne.Container, expanders []*widget.Button) {
	expanded := u.expanded[nodeKey]
	glyph := "▸"
	if expanded {
		glyph = "▾"
	}
	for _, btn := range expanders {
		btn.Enable()
		btn.SetText(glyph)
	}
	if expanded {
		box.Show()
	} else {
		box.Hide()
	}
}

// expander is a ▸/▾ toggle: the button widgets plus the toggle func
// shared by the button and the whole-row tap catcher.
type expander struct {
	btn    *widget.Button
	toggle func()
}

// newExpander builds a ▸/▾ toggle that shows/hides obj, restoring its
// expanded state under nodeKey. The updated state map is persisted on
// exit (see main), not per tap. The returned toggle is also wired to a
// tap-catcher over the whole header row (plan 13).
func (u *ui) newExpander(nodeKey string, obj fyne.CanvasObject) *expander {
	expanded := u.expanded[nodeKey]
	var btn *widget.Button
	e := &expander{}
	e.toggle = func() {
		if u.running || u.filtering {
			return
		}
		expanded = !expanded
		u.expanded[nodeKey] = expanded
		if expanded {
			btn.SetText("▾")
			obj.Show()
		} else {
			btn.SetText("▸")
			obj.Hide()
		}
	}
	btn = widget.NewButton("▸", func() { e.toggle() })
	btn.Importance = widget.LowImportance
	if expanded {
		btn.SetText("▾")
		obj.Show()
	} else {
		obj.Hide()
	}
	e.btn = btn
	u.allExpanders = append(u.allExpanders, btn)
	return e
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

// saveAllPrefs writes the selection denylist, expansion allowlist, and
// window geometry. Called once from the window close intercept; the
// in-memory maps are the live source of truth while the app runs.
func (u *ui) saveAllPrefs() {
	u.prefs.SetStringList(prefsDisabledKey, disabledFromState(u.servers, u.state))
	u.prefs.SetStringList(prefsExpandedKey, expandedToList(u.servers, u.expanded))
	if u.split != nil {
		u.prefs.SetInt(prefsSplitOffsetKey, int(u.split.Offset*1000))
	}
	if size := u.window.Canvas().Size(); size.Width > 0 && size.Height > 0 {
		u.prefs.SetInt(prefsWindowWidthKey, int(size.Width))
		u.prefs.SetInt(prefsWindowHeightKey, int(size.Height))
	}
}

// savedWindowSize returns the persisted content size when it is within
// sane bounds, else def (a corrupt or out-of-range pref — e.g. from a
// monitor change — must not create an unreachable window).
func savedWindowSize(prefs fyne.Preferences, def fyne.Size) fyne.Size {
	w, h := prefs.Int(prefsWindowWidthKey), prefs.Int(prefsWindowHeightKey)
	if w < 720 || w > 3840 || h < 480 || h > 2160 {
		return def
	}
	return fyne.NewSize(float32(w), float32(h))
}

// clampSplitOffset converts a persisted permille value to a Split.Offset
// fraction, clamped to (0,1) so the divider can never hide a pane entirely.
func clampSplitOffset(permille int) float64 {
	f := float64(permille) / 1000
	if f < 0.05 {
		return 0.05
	}
	if f > 0.95 {
		return 0.95
	}
	return f
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
	u.checkTexts = nil
	u.activeID = ""
	u.treePane = u.buildTreePane()
	u.treeHolder.RemoveAll()
	u.treeHolder.Add(container.NewPadded(u.treePane))

	u.tree = newResultsTree()
	u.rows = nil
	u.rowRefs = nil
	u.results = nil
	u.lastRun = time.Time{}
	u.refreshTableLayout()
	u.exportBtn.Disable()
	u.copyBtn.Disable()
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
		u.cancelRun()
		return
	}

	selected := u.selectedServers()
	if len(selected) == 0 {
		u.statusLabel.SetText("No servers selected.")
		return
	}

	u.tree = newResultsTree()
	u.rows = nil
	u.rowRefs = nil
	u.results = nil
	u.lastRun = time.Now()
	u.runStarted = u.lastRun
	u.doneCount = 0
	u.totalCount = len(selected)
	u.expectedSends = 0
	u.eta = 0
	u.refreshTableLayout()
	u.setRunning(true)
	u.statusLabel.SetText(fmt.Sprintf("Testing (0/%d)...", u.totalCount))

	ctx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel

	var (
		mu           sync.Mutex
		statusPrefix string
		lastPaint    time.Time
	)
	go func() {
		runTests(ctx, selected,
			func(s Server, index, total int) {
				name := s.DisplayName()
				mu.Lock()
				statusPrefix = fmt.Sprintf("Testing %s (%d/%d)", name, index, total)
				mu.Unlock()
				fyne.Do(func() {
					u.setActiveServer(s.ID())
					// Planned probe count for this server — onTick blends
					// sent/expected into the bar so it moves within a
					// server, not just at the ~1.4%-per-server steps.
					pps, _ := probePacing(s.MaxPPS)
					u.expectedSends = pps * int(probeWindow/time.Second)
					u.progress.SetValue(float64(index-1) / float64(total))
					eta := u.eta
					if eta > 0 {
						u.statusLabel.SetText(fmt.Sprintf("%s — about %s left", statusPrefix, formatETA(eta)))
					} else {
						u.statusLabel.SetText(statusPrefix + "...")
					}
				})
			},
			func(sent, recv int) {
				// Throttle to ~4 paints/sec: the pinger ticks per packet
				// (64/s), and a label re-render per tick floods the UI.
				// Guarded by mu: OnSend/OnRecv fire from different
				// pro-bing goroutines.
				mu.Lock()
				prefix := statusPrefix
				if time.Since(lastPaint) < 250*time.Millisecond {
					mu.Unlock()
					return
				}
				lastPaint = time.Now()
				mu.Unlock()
				loss, showLoss := runningLoss(sent, recv)
				fyne.Do(func() {
					if showLoss {
						u.statusLabel.SetText(fmt.Sprintf("%s — %d sent, %s loss", prefix, sent, loss))
					} else {
						u.statusLabel.SetText(fmt.Sprintf("%s — %d sent", prefix, sent))
					}
					// Blend the current server's send fraction into the
					// overall progress (u fields are UI-thread-only; the
					// fyne.Do closure is what makes this read safe from
					// the pinger goroutine).
					if u.running && u.expectedSends > 0 && u.totalCount > 0 {
						frac := float64(sent) / float64(u.expectedSends)
						if frac > 1 {
							frac = 1
						}
						p := (float64(u.doneCount) + frac) / float64(u.totalCount)
						if p > 0 && p < 1 {
							u.progress.SetValue(p)
						}
					}
				})
			},
			func(outcome ServerOutcome) {
				fyne.Do(func() {
					u.results = append(u.results, outcome)
					u.tree.addOutcome(u.regionBy[outcome.Server.ID()], outcome)
					u.doneCount++
					// Average per-server duration so far drives the ETA;
					// early failures make it shrink faster than the naive
					// (total-done)*10.25s estimate.
					if u.doneCount > 0 {
						elapsed := time.Since(u.runStarted)
						u.eta = time.Duration(float64(elapsed) / float64(u.doneCount) * float64(u.totalCount-u.doneCount))
					}
					u.progress.SetValue(float64(u.doneCount) / float64(u.totalCount))
					u.rows, u.rowRefs = u.tree.flattenWithRefs()
					u.refreshTableLayout()
				})
			},
		)
		fyne.Do(func() {
			u.setRunning(false)
			u.clearActiveServer()
			u.progress.SetValue(1)
			if ctx.Err() != nil {
				u.statusLabel.SetText(fmt.Sprintf("Cancelled — %d of %d servers tested.", len(u.results), len(selected)))
			} else {
				u.statusLabel.SetText(fmt.Sprintf("Done — %d servers tested.", len(u.results)))
			}
			u.cancel = nil
		})
	}()
}

// cancelRun acknowledges a Cancel tap immediately: the button disables
// and reads "Cancelling…" until the runner goroutine unwinds and
// setRunning(false) restores it. The probe itself aborts as soon as the
// context dies; this only closes the feedback gap.
func (u *ui) cancelRun() {
	if u.cancel == nil {
		return
	}
	u.cancelling = true
	u.testBtn.Disable()
	u.testBtn.SetText("Cancelling…")
	u.statusLabel.SetText("Cancelling — finishing the current probe…")
	u.cancel()
}

// setActiveServer marks the leaf check of the server being probed with a
// ▶ prefix so the selection tree shows where the run is. The original
// text is remembered so the marker can move on and clear cleanly.
func (u *ui) setActiveServer(id string) {
	if u.activeID != "" && u.activeID != id {
		if text, ok := u.checkTexts[u.activeID]; ok {
			if chk := u.leafCheck(u.activeID); chk != nil {
				chk.SetText(text)
			}
		}
	}
	if text, ok := u.checkTexts[id]; ok {
		if chk := u.leafCheck(id); chk != nil {
			chk.SetText("▶ " + text)
		}
	}
	u.activeID = id
}

// clearActiveServer removes the run marker from the tree.
func (u *ui) clearActiveServer() {
	if u.activeID == "" {
		return
	}
	if text, ok := u.checkTexts[u.activeID]; ok {
		if chk := u.leafCheck(u.activeID); chk != nil {
			chk.SetText(text)
		}
	}
	u.activeID = ""
}

// leafCheck finds a provider checkbox by server ID in the live tree.
func (u *ui) leafCheck(id string) *widget.Check {
	for _, rt := range u.trees {
		for _, ct := range rt.cities {
			if chk, ok := ct.providers[id]; ok {
				return chk
			}
		}
	}
	return nil
}

// runningLoss formats the running packet-loss percentage. The bool is
// false while the figure would be pure noise (no replies yet in the
// first half-second of probes).
func runningLoss(sent, recv int) (string, bool) {
	if sent < 32 {
		return "", false
	}
	loss := float64(sent-recv) / float64(sent) * 100
	return fmt.Sprintf("%.1f%%", loss), true
}

// formatETA renders a remaining-time estimate compactly ("45s", "9m",
// "1h 12m"). Minutes round up so the estimate never promises less time
// than remains.
func formatETA(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds() + 0.5)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := (seconds + 59) / 60 // ceiling
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
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
		writeErr := writeCSV(closer, func(s Server) string { return u.regionBy[s.ID()] }, u.lastRun, u.results)
		if writeErr != nil {
			u.statusLabel.SetText("Export failed: " + writeErr.Error())
			return
		}
		path := ""
		if closer.URI() != nil {
			path = closer.URI().Path()
		}
		if path != "" {
			u.statusLabel.SetText(fmt.Sprintf("Exported %d results to %s.", len(u.results), path))
		} else {
			u.statusLabel.SetText(fmt.Sprintf("Exported %d results to CSV.", len(u.results)))
		}
	}, u.window)
	d.SetFileName("faceit-tester-results.csv")
	d.Show()
}

// onResultRowTap dispatches a results-table row tap: parent rows toggle
// expansion, error rows show the full error, healthy leaf rows copy the
// row line to the clipboard. The dispatcher keeps the semantics in one
// place as the tap behaviors grow.
func (u *ui) onResultRowTap(id widget.TableCellID) {
	if id.Row < 0 || id.Row >= len(u.rowRefs) {
		return
	}
	ref := u.rowRefs[id.Row]
	if !ref.leaf {
		if u.tree.toggleNode(ref) {
			u.rows, u.rowRefs = u.tree.flattenWithRefs()
			u.refreshTableLayout()
		}
		return
	}
	outcome := ref.outcome
	if outcome.Result.Err != nil {
		u.showErrorDetail(outcome)
		return
	}
	text := formatOutcomeLine(outcome, u.regionBy[outcome.Server.ID()])
	if c := fyne.CurrentApp().Clipboard(); c != nil {
		c.SetContent(text)
		u.statusLabel.SetText("Copied " + outcome.Server.DisplayName() + " row.")
	}
}

// showErrorDetail shows a failed probe's full error with a copy button —
// the Loss-column cell only fits the word "error".
func (u *ui) showErrorDetail(outcome ServerOutcome) {
	text := widget.NewLabel(outcome.Server.DisplayName() + "\n\n" + outcome.Result.Err.Error())
	text.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(text)
	scroll.SetMinSize(fyne.NewSize(420, 100))
	copyBtn := widget.NewButton("Copy", func() {
		if c := fyne.CurrentApp().Clipboard(); c != nil {
			c.SetContent(outcome.Server.DisplayName() + ": " + outcome.Result.Err.Error())
		}
	})
	closeBtn := widget.NewButton("Close", nil) // replaced below — declared for the row
	buttons := container.NewHBox(copyBtn, layout.NewSpacer(), closeBtn)
	body := container.NewBorder(nil, buttons, nil, nil, scroll)
	d := dialog.NewCustomWithoutButtons("Probe failed", body, u.window)
	closeBtn.OnTapped = func() { d.Hide() }
	d.Resize(fyne.NewSize(460, 0))
	d.Show()
}

// onCopySummaryTap puts the whole run on the clipboard as an aligned
// text table — paste-into-Discord companion to Export CSV.
func (u *ui) onCopySummaryTap() {
	if len(u.results) == 0 {
		u.statusLabel.SetText("No results to copy yet.")
		return
	}
	text := buildSummaryText(u.lastRun, u.results, func(s Server) string { return u.regionBy[s.ID()] })
	if c := fyne.CurrentApp().Clipboard(); c != nil {
		c.SetContent(text)
		u.statusLabel.SetText(fmt.Sprintf("Copied %d results to clipboard.", len(u.results)))
	}
}

// onAboutTap shows the version dialog with a manual update check — the
// in-app recovery path after choosing "Never" (which otherwise only the
// --force-update-check flag bypasses).
func (u *ui) onAboutTap() {
	bodyText := widget.NewLabel("FACEIT Connection Tester\nVersion " + versionDisplay() + "\n\n" +
		"Pre-flight ICMP tester for FACEIT CS2 locations.")
	bodyText.Wrapping = fyne.TextWrapWord

	checkBtn := widget.NewButton("Check for updates now", func() {
		u.checkForUpdate(true)
	})
	reEnableBtn := widget.NewButton("Re-enable update checks", func() {
		u.prefs.SetBool(prefsUpdatesDisabledKey, false)
		u.statusLabel.SetText("Update checks re-enabled.")
	})
	if !u.prefs.Bool(prefsUpdatesDisabledKey) {
		reEnableBtn.Hide()
	}
	closeBtn := widget.NewButton("Close", nil)
	buttons := container.NewHBox(layout.NewSpacer(), reEnableBtn, checkBtn, closeBtn)
	body := container.NewBorder(nil, buttons, nil, nil, bodyText)
	d := dialog.NewCustomWithoutButtons("About", body, u.window)
	closeBtn.OnTapped = func() { d.Hide() }
	checkBtn.OnTapped = func() { d.Hide(); u.checkForUpdate(true) }
	d.Resize(fyne.NewSize(420, 0))
	d.Show()
}

// formatOutcomeLine renders one result as a single clipboard-friendly
// line: identity, host, and the graded metrics.
func formatOutcomeLine(o ServerOutcome, region string) string {
	s := o.Server
	r := o.Result
	head := fmt.Sprintf("%s.%s  %s  %s", region, s.City, s.Provider, s.Host)
	if r.Err != nil {
		return head + "  error: " + r.Err.Error()
	}
	if s.Scrubbed {
		return head + fmt.Sprintf("  med %s  p99 %s  jitter n/a  loss n/a (icmp-scrubbed)",
			formatMS(r.Median), formatMS(r.P99))
	}
	return head + fmt.Sprintf("  med %s  p99 %s  jitter %s  loss %.2f%%",
		formatMS(r.Median), formatMS(r.P99), formatMS(r.Jitter), r.LossPercent)
}

// buildSummaryText renders the whole run as an aligned text table with a
// timestamp header — one line per leaf outcome, no aggregates.
func buildSummaryText(now time.Time, outcomes []ServerOutcome, regionOf func(Server) string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FACEIT tester — %s\n\n", now.Format("Jan 2, 15:04"))
	fmt.Fprintf(&b, "%-36s %8s %8s %8s %7s\n", "Server", "med", "p99", "jitter", "loss")
	for _, o := range outcomes {
		r := o.Result
		name := o.Server.DisplayName()
		if len(name) > 36 {
			name = name[:35] + "…"
		}
		if r.Err != nil {
			fmt.Fprintf(&b, "%-36s %s\n", name, "error: "+r.Err.Error())
			continue
		}
		if o.Server.Scrubbed {
			fmt.Fprintf(&b, "%-36s %8s %8s %8s %7s\n", name,
				formatMS(r.Median), formatMS(r.P99), "n/a", "n/a")
			continue
		}
		fmt.Fprintf(&b, "%-36s %7sms %7sms %7sms %6.2f%%\n", name,
			formatMS(r.Median), formatMS(r.P99), formatMS(r.Jitter), r.LossPercent)
	}
	return b.String()
}

func (u *ui) setRunning(running bool) {
	u.running = running
	if running {
		u.cancelling = false
		// Importance before SetText: SetText triggers the re-render, so
		// the color must already be updated when it fires.
		u.testBtn.Importance = widget.DangerImportance // red = stop
		u.testBtn.SetText("Cancel")
		u.testBtn.Enable()
		u.progress.SetValue(0)
		u.progress.Show()
		u.exportBtn.Disable()
		u.copyBtn.Disable()
		u.editBtn.Disable() // the table can't swap under a live run
	} else {
		u.cancelling = false
		u.testBtn.Importance = widget.SuccessImportance // green = start
		u.testBtn.SetText("Test")
		if isElevated() {
			u.testBtn.Enable()
		}
		u.progress.Hide()
		if len(u.results) > 0 {
			u.exportBtn.Enable()
			u.copyBtn.Enable()
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
	// Filtering mid-run would hide the active-server marker and reflow
	// the tree under a live probe — lock it with the checks.
	if u.filterEntry != nil {
		if running {
			u.filterEntry.Disable()
		} else {
			u.filterEntry.Enable()
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
// show n/a uncolored instead of a misleading number. Errored rows keep the
// cell short ("error") — the full error shows on tap (showErrorDetail).
func resultRow(name string, r PingResult, scrubbed bool) []tableCell {
	nameCell := tableCell{text: name}
	if r.Err != nil {
		return []tableCell{
			nameCell,
			{"—", widget.MediumImportance, false},
			{"—", widget.MediumImportance, false},
			{"—", widget.MediumImportance, false},
			{"—", widget.MediumImportance, false},
			{"error", widget.DangerImportance, false},
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
// a returned report. Lookups are cancellable; the report is copyable.
func (u *ui) onIdentifyTap() {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("connect 198.244.202.178:27015")
	result := widget.NewLabel("Paste a connect string (or any text containing the server address), then Look up.")
	result.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(result)
	scroll.SetMinSize(fyne.NewSize(440, 56))

	pasteBtn := widget.NewButtonWithIcon("Paste", theme.ContentPasteIcon(), func() {
		if c := fyne.CurrentApp().Clipboard(); c != nil {
			entry.SetText(c.Content())
		}
	})
	top := container.NewBorder(nil, nil, nil, pasteBtn, entry)

	// Declare before NewButton: the closures reference lookupBtn/d, whose
	// scope starts after the := statement (same gotcha as the check tree).
	var (
		d            *dialog.CustomDialog
		lookupBtn    *widget.Button
		cancelBtn    *widget.Button
		copyBtn      *widget.Button
		lookupCancel context.CancelFunc
		lookupGen    int
	)
	lookupBtn = widget.NewButton("Look up", func() {
		raw := entry.Text
		if strings.TrimSpace(raw) == "" {
			result.SetText("Nothing to look up — paste a connect string first.")
			return
		}
		lookupGen++
		gen := lookupGen
		lookupBtn.Disable()
		cancelBtn.Enable()
		copyBtn.Disable()
		result.SetText("Looking up…")
		ctx, cancel := context.WithCancel(context.Background())
		lookupCancel = cancel
		go func() {
			text := u.identifyText(ctx, raw)
			fyne.Do(func() {
				if gen != lookupGen {
					return // cancelled or superseded; a newer lookup owns the label
				}
				result.SetText(text)
				lookupBtn.Enable()
				cancelBtn.Disable()
				copyBtn.Enable()
				fitIdentifyDialog(d, scroll, result, text)
			})
		}()
	})
	lookupBtn.Importance = widget.HighImportance
	cancelBtn = widget.NewButton("Cancel", func() {
		lookupGen++ // invalidate the in-flight completion
		if lookupCancel != nil {
			lookupCancel()
			lookupCancel = nil
		}
		result.SetText("Lookup cancelled.")
		lookupBtn.Enable()
		cancelBtn.Disable()
		fitIdentifyDialog(d, scroll, result, result.Text)
	})
	cancelBtn.Disable()
	copyBtn = widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		if c := fyne.CurrentApp().Clipboard(); c != nil {
			c.SetContent(result.Text)
		}
		copyBtn.SetText("Copied")
		go func() {
			time.Sleep(1500 * time.Millisecond)
			fyne.Do(func() { copyBtn.SetText("Copy") })
		}()
	})
	copyBtn.Disable()
	closeBtn := widget.NewButton("Close", func() {
		if lookupCancel != nil {
			lookupCancel()
		}
		d.Hide()
	})

	// Own the bottom row (WithoutButtons — NewCustom always renders a
	// dismiss button, even with empty text): Close + Copy bottom-left,
	// Cancel + Look up bottom-right, neither stretched.
	buttons := container.NewHBox(closeBtn, copyBtn, layout.NewSpacer(), cancelBtn, lookupBtn)
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
