# AGENTS.md

## What this is

Single-package Go/Fyne desktop app: pre-flight connection tester for FACEIT
CS2 players. Pings HiperZ mirror/speedtest endpoints (not game servers) at
CS2-like rates (64pps, 600-byte payloads, 10s per server) via ICMP.

## Commands

```sh
go vet ./...
go test -short ./...    # -short skips network smoke tests (what CI runs)
go test ./...           # includes live pings (1.1.1.1, paced OVH UK, NFO 56B)
go build
go build -ldflags "-H windowsgui"   # Windows: GUI subsystem, no console
```

Plain Windows `go build` produces a console-subsystem exe; the Restart
as Admin relaunch can't inherit the terminal, so a blank console window
appears — always test elevation flows with `-H windowsgui` (what
`fyne package` produces anyway). Windows needs a C compiler (mingw) for
Fyne's cgo.

No linter/formatter beyond `gofmt` + `go vet`. CI (`.github/workflows/ci.yml`)
packages ubuntu/windows/macos with `fyne package -release` (needs
`fyne.io/fyne/v2/cmd/fyne@v2.8.2`) and uploads artifacts; every platform
ships as a plain `.zip` (Linux is re-zipped from fyne's tar.xz so the
in-app updater needs only `archive/zip` — no xz dependency). Builds run
only via `workflow_dispatch` (the release skill passes the version to
stamp — the stamp step overwrites `version.go` before compiling; the
input is regex-validated `X.Y.Z`) and `pull_request`; pushes never
build. Unstamped builds keep `version = "dev"`, which never prompts for
updates. Linux CI needs
`libgl1-mesa-dev xorg-dev` (Fyne cgo). Local Windows cross-compile needs
mingw (`CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1`), but CI builds natively
so this is rarely needed.

App icon: `icon.png` is embedded at compile time (`//go:embed` +
`a.SetIcon`) for the window icon, and `FyneApp.toml` gives `fyne package`
the platform-native icon (Windows exe resource, macOS bundle, Linux
`.desktop` + pixmap). Rename the icon → update FyneApp.toml too.

## Non-obvious gotchas

- **Fyne widget refresh order:** `SetText` triggers a re-render; plain field
  assignments (e.g. `btn.Importance`) do not. Assign fields like Importance
  *before* SetText, or the visual update silently never happens.
- **Fyne closure scoping:** `chk := widget.NewCheck(..., func(){ chk... })`
  does not compile (scope of `chk` starts after the statement). Declare
  `var chk *widget.Check` first, then assign.
- **Fyne threading (2.6+):** all widget mutations from background goroutines
  must be wrapped in `fyne.Do(...)`. Button/entry callbacks already run on
  the UI thread.
- **Bare keys never reach `canvas.AddShortcut`.** Fyne 2.8's glfw driver
  only builds shortcut objects for *modifier chords* (Ctrl+C → concrete
  `fyne.ShortcutCopy`, dispatched by ShortcutName — entries consume their
  own copy first because the driver routes chords to the focused
  `Shortcutable` before the canvas). Bare Return/Esc go to the focused
  widget's TypedKey and fall through to `canvas.SetOnTypedKey` **only
  when nothing is focused**. Focus is won by check taps
  (`focusIfNotMobile`) and table taps, cleared by button taps
  (`Button.Tapped` → `Focus(nil)`) — so `registerShortcuts` uses
  SetOnTypedKey for Enter/Esc, registers `&fyne.ShortcutCopy{}` for
  Ctrl/Cmd+C, gates on `Overlays().Top() != nil` (dialog buttons unfocus
  on tap, so focus alone can't detect an open dialog), and the
  check/table handlers call `unfocus()` to keep the bare-key path alive.
  A focused Entry must never be treated as "safe to fire Test" — the
  gate exists because shortcuts otherwise preempt Return in text fields.
- **Results header alignment:** `Table.findX` inserts `SizeNamePadding`
  *between* columns, so the hand-built header row must be a plain `HBox`
  (same padding between children) with no outer `NewPadded` wrapper —
  zero-gap or padded-wrong headers drift progressively against the body.
  Table and header column widths must always come from the same constants.
- **VBox sizes children to their MinSize — it never stretches them**
  (layout/boxlayout.go; only vertical spacers absorb slack). Wrapping a
  `VScroll` in a VBox collapses it to min height and squashes the pane's
  content against the top; use `container.NewBorder(top, nil, nil, nil,
  scroll)` when the controls pin top and the scroll must fill the rest.
- **A window never shrinks below its content's MinSize, and grows to
  meet it** — not just at startup: **every frame** `repaintWindow` calls
  `canvas.EnsureMinSize()` (driver/common/canvas.go), which resizes the
  canvas and queues `fitContent()` → `view.SetSize()` whenever content
  min exceeds the current size. Any widget whose min varies with
  content — a label whose text changes, a tree that reveals nodes —
  resizes the *whole window* when it crosses the current size. Lock
  variable text into a fixed `container.NewGridWrap` cell (Fyne 2.8 has
  no SetMinSize) so typing in a filter or a growing count can't jerk
  the window around.
- **`NewVScroll` folds content *width* into its MinSize** (the
  `Scroll.MinSize` switch's `ScrollVerticalOnly` case), while
  **`NewScroll` (ScrollBoth) hits no case and ignores content in both
  dimensions**. The tree pane must use `NewScroll`: with VScroll a
  filter force-expanding a wide branch grew the pane min, the split's
  min-clamp stole width from the results pane, and EnsureMinSize grew
  the OS window per keystroke (long names get a horizontal scrollbar
  instead). `TestScrollMinNeverFollowsContent` pins this.
- **`dialog.NewCustom` always renders its dismiss button**, even with an
  empty text (invisible stub). Use `NewCustomWithoutButtons` when the
  content owns its own bottom button row.
- **pro-bing `Timeout` is a whole-run timer**, not per-packet. Never set it
  short — pacing is controlled by `Count` + context deadline. After Count
  sends finish, the run keeps receiving until the context expires; the
  runner's `probeWindow+replyGrace` budget makes that last part
  receive-only so tail replies aren't clipped (an earlier design lost ~1
  packet/run to this).
- **Tri-state checkboxes:** `Check.Partial` renders the dash icon; user taps
  on Partial fire `OnChanged(true)`. To set state programmatically without
  callbacks, assign `Checked`/`Partial` fields + `Refresh()` directly —
  `SetChecked` clears Partial and fires OnChanged. Bulk updates use a
  `syncing` bool guard so cascades don't re-enter.
- **Windows requires admin** (raw ICMP via pro-bing `SetPrivileged(true)`).
  Elevation check is build-tagged (`admin_windows.go`/`admin_other.go`) and
  is the *first* thing `main()` does, before any Fyne init: every launch
  goes straight to the UAC prompt (`ShellExecuteW` `runas` via
  `elevateAndRestart`, which forwards `os.Args[1:]` quoted by
  `joinWinArgs` so flags survive the round-trip). The unelevated GUI
  (Test hidden + yellow **Restart as Admin** explaining and retrying) is
  only the fallback after a dismissed prompt (`errElevationCancelled`
  sentinel compared via `errors.Is` from cross-platform code); the manual
  restart saves prefs first because `SetCloseIntercept` timing can't be
  relied on. The exe must stay `asInvoker` — a `requireAdministrator`
  manifest would force UAC at every launch. Linux tries
  unprivileged udp4 sockets, falls back to raw on permission errors
  (`isPermissionErr` matches several platform-specific strings).
- **`--test` auto-start** (`autotest.go`): `flag.Parse` runs *before* the
  elevation check so `--help` never triggers UAC, and the spec is
  validated *before* the UAC prompt so a bad spec fails fast on the
  terminal — errors print to stderr *and* show in the status label (the
  packaged exe is windowsgui: stderr writes are silent no-ops there).
  The spec replaces `u.state` and expands the selected servers' *regions*
  (not cities — providers are a power-user detail; the city checkbox's
  tri-state shows the selection) via `expansionForSelection`, *before*
  `build()`. Auto-start is a goroutine launched *before*
  `ShowAndRun` that sleeps ~100ms then calls `fyne.Do(onTestTap)` —
  `fyne.Do` must not run before the event loop exists, and there is no
  after-first-render hook in Fyne to use instead. Skipped when unelevated
  or the spec is invalid; the selection persists on close like any manual
  change.
- **`app.NewWithID` runs before the elevation check, prefs reads before
  `ShowAndRun`.** `--test` validation needs the server list, which now
  lives in preferences, so `main()` creates the app + reads
  `servers_override` pre-UAC (harmless — creating the app opens no
  window). Preference *writes* before `ShowAndRun` would block on
  `fyne.DoAndWait`, so the only pre-Run interaction is reads; every write
  (editor Apply/Drop, close-intercept saves) happens inside the event
  loop. Don't move `loadServers` back after elevation without also moving
  spec validation, and don't write prefs from startup code.
- **Per-server ICMP policers exist; measure them with rate curves.** Some
  providers (OVH fleet, Moscow PS, BA Latitude.sh) answer ICMP
  at a fixed token quota regardless of send rate — a *limiter* keeps the
  absolute recv count constant across rates (e.g. OVH UK: exactly ~105
  replies at 16/32/64pps) and delivers 100% at low rates, while real path
  loss keeps the *loss percentage* constant. `Server.MaxPPS` probes under
  the measured quota (~10% margin) so loss reflects the path, not the
  policer. Values and evidence: `PACKET_LOSS_FINDINGS.md`. Do not copy
  MaxPPS without a rate curve — the old Hivelocity 8-packet experiments
  that "proved" throttling was indistinguishable from loss were too short
  to see the quota.
- **Some endpoints are DDoS-scrubbed, not just rate-limited.**
  Hivelocity (Dallas) answers exactly 1 echo reply/sec under load and
  drops 1pps IPv4 probes at random (payload/interval/idle all irrelevant;
  v6 slightly better) — loss is unmeasurable, so it carries
  `Server.Scrubbed: true`: probed at the default rate for RTT only,
  loss/jitter render as uncolored "n/a" (UI) or blank + `icmp-scrubbed`
  note (CSV), and are excluded from city worst-loss summaries. Never
  "fix" a scrubbed host with MaxPPS — 1pps lands in the stochastic
  regime and makes the report noisier, not better.
- **Some providers filter large ICMP payloads entirely** (NFO: 600-byte
  echo drops 100% at any rate; 56-byte clean at 64pps).
  `Server.ProbeSize` overrides the payload per server (0 = 600 default).
- **Party ranking treats the ping target as the base reality.** Result
  codes join servers by lowercased `Host` and carry hosts plus metrics
  — nothing else — because `City.Provider` IDs are override-relative.
  Display identity comes from the party leader's OWN active table
  (`resolverFromTable` at Summarize time), so the tree shows the naming they
  run; targets their list lacks land in the `Unknown` region named by
  the host (DisplayName drops the dot for empty providers). Only
  servers present in EVERY pasted run are ranked (intersection), with
  the excluded count surfaced in the dialog.
- **Result codes are versionless by design** — no version byte, one
  `errNotResultCode` sentinel for every failure mode (base64, magic,
  flate, truncation, trailing bytes). Changing the format breaks old
  codes on purpose: update encode, decode and tests in one commit and
  expect re-shares, not compatibility shims — users are expected to run
  the latest app. A full embedded-list run must stay under 2000 chars
  (Discord message limit; ~630 measured, pinned by
  `TestResultCodeWithinDiscordBudget`); payload size is
  value-independent (fixed-width metrics), so the number only moves
  when the server list does.
- **The party view is the same `resultsPane` on `mainColumns`** — codes
  carry every metric now, so there is no column trim; decoded runs are
  resolved into `partyEntry` identity *before* `buildParty`, which
  assumes every run is already named (leaf naming is first-run-wins
  and consistent because all runs resolve through the same table).
- **Auto-update replaces the running binary via rename-first.** All three
  platforms allow renaming a running executable/bundle but not overwriting
  it, so `replaceAndRelaunch` (build-tagged per OS) renames old → `.old`,
  writes the new file at the original path, spawns/execs it, and the new
  process's `cleanupOlds()` (first thing in `main`, before elevation)
  deletes `*.old` / `*.app.old` siblings. Linux uses `syscall.Exec` (no
  orphan parent); Windows children inherit elevation (no second UAC);
  macOS swaps the whole `.app` bundle (walk up from the executable).
  Failures keep the old version running and show a dialog with the manual
  download URL. Version comparison is semver-only: `updateAvailable`
  treats semver as unconditionally newer than any legacy date tag in both
  directions, and `version == "dev"` (unstamped branch/local builds)
  never prompts. `--force-update-check` bypasses the dev/never/--test
  gates for UI review (still needs a platform asset to exist). The `--test`
  flag suppresses the prompt entirely — automation must never block on a
  modal. Download + extraction is stdlib-only (`archive/zip` with zip-slip
  rejection); CI re-packs Linux's tar.xz as a zip so no xz library is
   needed anywhere. When CI's packaging or asset names change, update
  `updateAssetName` + `stagePayload` in `update.go` and their tests in the
  same commit; the per-platform zip layouts (windows = bare
  `faceit-tester.exe`, linux = `faceit-tester/` tree, darwin = `.app`)
  are pinned by `TestStagePayloadPublishedLayouts`. Note: 0.2.0–1.0.0
  Windows builds can never self-update — staging looked for the
  extensionless `faceit-tester` instead of `faceit-tester.exe` (fixed in
  1.0.1), so those installs need a one-time manual download.
- **Releases are dispatch-built** (`.opencode/skills/release/SKILL.md`):
  commit → plain push of main (pushes no longer build) → `gh workflow
  run` with the version input → the dispatch run stamps `version.go` and
  builds → only after it's green, tag `X.Y.Z` at the same SHA and create
  the release from *that* run's artifacts (unwrapped from the artifact
  zips and attached byte-for-byte; `gh release create --target` pins the
  tag). A failed run means fix-forward and re-dispatch — the same
  version is still free because no tag exists until the build is green.
  The date tag format (`MM-DD-YYYY`) is retired; same-day double
  releases are just a patch bump.

## Architecture (all in package main)

- `servers.go` — embedded server tables (region → city → provider), trimmed
  to FACEIT-official locations with FACEIT city names (lax→San Jose,
  ny→Virginia). Providers are RDAP-resolved hosting company names (the
  HiperZ scripts used codenames: O→OVH, PS→Psychz Networks, hz→split across
  Dedicated.com/ReliableSite/NForce, maxi→Latitude.sh, etc.). Eight dead
  endpoints were removed and Chicago.NFOservers moved to NFO's current
  example IP (74.91.113.173, ProbeSize 56 — HiperZ's IP was retired) per
  `PACKET_LOSS_FINDINGS.md`. The custom list (schema: same
  city/provider/host plus optional `max_pps`/`probe_size`/`scrubbed`)
  lives in app preferences under `servers_override` — there is no
  servers.json-next-to-the-exe anymore. `parseServersJSON` +
  `validateServers` gate the stored blob (non-empty fields, no `/` in
  region/city — prefs tree keys — no duplicate City.Provider, no
  negative probe overrides); `dumpServersJSON` serializes a table back
  into that schema, NA-first region order, and `loadServers` returns the
  stored list or the built-ins (always a usable table; a malformed blob
  comes back as an error *and* the built-ins so the UI can surface it).
  Round-trip tested by `TestDumpServersJSONRoundTrip`.
- `override.go` — the custom-list persistence surface: `loadServers`,
  `saveOverride` (a table equal to `embeddedServers` *removes* the key so
  "custom list active" stays exact), `dropOverride`, `overrideActive`,
  `cloneServers` (editors must clone — the returned table may alias the
  built-ins). `prefs.go` holds the `servers_override` key constant.
- `editor.go` — the Edit Servers dialog (per-server form + region → city
  → server list). Works on an `editRow` copy (numeric fields stay raw
  strings until Apply; `nonNegativeInt` validators mark bad input),
  write-through on every keystroke (no pending-flush on navigation), and
  nothing touches prefs until Apply. First open clones the *effective*
  table, so the first Apply always stores a complete baseline + edits;
  Cancel (or the dialog's close) discards everything. Destructive
  actions (per-row/per-city/per-region trash buttons in the list, Reset
  to Defaults) all confirm — there is no per-row undo; Cancel is the
  only escape; destructive affordances live in the list, not the form
  pane. Reset reloads the built-ins into the working copy (Apply
  persists it as the list). `+ Add region` / `+ City` / `+ Provider`
  create whole new regions/cities/servers via a one-field Form popup
  (validated: non-empty, no "/", no duplicates) or a placeholder row.
  City blocks stretch under `indentBlock` (Border-center), NOT
  `indent` — indent's HBox sizes to min, which collapses interior
  spacers and left-clumps the right-pinned add/delete buttons. Form
  fields carry
  `HintText` explaining Max PPS / Probe size / Host / Scrubbed (Fyne has
  no tooltip API). `tableFromRows` strips zero-row regions (they would
  render as empty tree nodes). `applyServerTable` in main.go swaps the
  live table: rebuilds the tree into `treeHolder`, re-derives
  state/expansion against the new IDs, and clears results.
- `prefs.go` — pure, unit-tested state logic. Persistence is a *denylist*
  of unchecked server IDs (`disabled_servers`) plus an allowlist of
  expanded tree nodes (`expanded_nodes`). Only IDs present in the active
  server set apply on load; stale IDs (deleted overrides) are ignored and
  pruned on save. First launch (empty key) = everything checked. Both
  lists are saved once from the window `SetCloseIntercept` handler (not
  per checkbox tap — per-toggle prefs writes made the UI sluggish on
  slow storage; a crash loses unsaved toggles, accepted tradeoff). The
  same close handler also persists window geometry: content width/height
  in pixels (`window_width`/`window_height`, clamped 720–3840 ×
  480–2160 on restore via `savedWindowSize`) and the HSplit ratio in
  permille (`split_offset`, clamped 5–95% by `clampSplitOffset`). Fyne
  exposes no window-*position* API — size only.
- `pinger.go` — pro-bing wrapper: per-server pacing (`probePacing`, default
  64pps/600B, `MaxPPS`/`ProbeSize` overrides), jitter = mean |ΔRTT|
  (RFC 3550), RTT stats are nearest-rank median/p1/p99 percentiles of the
  reply samples (avg/min/max proved less informative — one delayed reply
  skewed a whole row), platform fallback logic.
- `runner.go` — sequential 10s probes, context-cancelled (flat window even
  for paced hosts). `onStatus` receives the `Server` itself (not just
  the display name) so callers can key tree-marker state by ID.
- `results.go` — hierarchical results model (region → city → provider)
  behind the results table: `addOutcome` appends leaves,
  `flattenWithRefs` recomputes worst-case aggregates for region/city rows
  on every render (bold, ▸/▾ glyph, child count) *plus* a parallel
  `[]rowRef` slice so tap handlers resolve rows without re-deriving
  indexes from tree order. City order honors `sortMode` + `desc`
  (probe order by default; metric modes rank a region's cities by
  their worst-case aggregate in either direction with errors/n-a last
  in both — `sortedCityIndexes`/`rowCellLess`; providers always keep
  probe order because players choose locations, not providers, and
  regions keep RegionOrder). `mergeOutcomes` is the shared
  column-wise worst merge (error-skip, scrubbed-loss/jitter-skip) that
  `worstCase` renders as a bold row; the party summary calls it directly
  to merge one server's outcomes across players. Tapping a
  parent row toggles expansion via `toggleNode(rowRef)` (in-memory only;
  new runs start with regions expanded and cities collapsed).
- `resultsview.go` — the reusable results table (`resultsPane`): header
  of sort-toggle buttons over a `widget.Table`, both driven by a
  `resultsTree`. Parameterized by `paneConfig`: `columns` (label +
  sortMode per column — both views use `mainColumns`, the full six
  metrics), `onLeaf` (main copies the row/shows the error; party opens
  the per-player breakdown), `onLegend`, `unfocus`. `setTree` reserves
  the Server column against a fully-expanded preview of the tree
  (`reservedNameWidth` flips every branch open for one flatten, then
  restores the expansion state) so revealing a branch never re-flows
  the table. `refreshLayout` pushes the Server width to the TABLE
  unconditionally — a "no change" short-circuit once left the body at
  its old width while the header reserved more, so providers spilled
  past the column and every header cell drifted (party summary fresh
  panes reserved >floor immediately, which is how it surfaced).
  Owns the
  header/table/column-width plumbing that used to live in main.go
  (`rebuildHeader`, `toggleSort`, `refreshLayout`); the header HBox
  alignment rules (SizeNamePadding between cells, no outer Padded)
  apply per pane.
- `main.go` — Fyne UI: region→city→provider tri-state selection tree
  (collapsed by default, expansion persisted) with a **filter entry**
  (substring match on region/city/provider/host; whole city/region
  blocks show/hide — hiding only the header left an indent-wrapper gap
  per filtered-out city — and visible branches force-expand, parent
  checks disable so a cascade can't select hidden servers) and
  **All/None/Invert** bulk buttons plus a live "N of M selected" count
  (folded with the filter's match count; its cell is width-locked via
  GridWrap so typing can't resize the window). Expansion toggles only
  via the ▸/▾ button — a whole-row tap catcher highlighted like a
  button and was inconsistent with the results table. Test/Cancel
  toggle (green/red via
  Importanance); Cancel flips to "Cancelling…" immediately and disables
  the button. While running: a determinate progress bar + ETA ("about
  9m left") — the bar is a thin full-width strip *below* the status/
  button row (never stacked beside the buttons, which stretched their
  band vertically) and blends the current server's send fraction
  (`expectedSends` from `probePacing`) into the overall value, because
  per-server-only steps (~1.4% of a 70-server run) are invisible; also
  a ▶ marker on the active leaf check,
   and a throttled (~4/s) running-loss status line. The results table
   itself is the reusable `resultsPane` (resultsview.go); main.go wires
   it with `mainColumns` + copy/error leaf taps; sortable
   headers (tap a column to cycle inactive → worst-first/asc → opposite →
   off; sorts a region's cities by their aggregate, never the providers);
   tap a leaf
   to copy its row or show the full error; tap a parent to toggle; no
   caption/placeholder chrome above or inside the table — the status
   label is the only prose; "?" legend
   dialog explaining thresholds and n/a. Secondary actions live in a
   labeled "Actions" drawer (constant text both states — only the icon
   flips; "Hide" changed the button width and made the row jump; its
   bottom gap is re-measured on every open by `sizeDrawerGap` so the
   progress bar strip can't steal the drawer's clearance and drop it
   over the row):
   Export CSV, Copy Summary, Party,
   Edit Servers, Identify, About. **Party** (`onPartyTap`) is the
   group summary flow in two dialogs: an input dialog with five
   teammate slots (one spare) + include-my-run checkbox (default on)
   whose text persists for the session only (`u.partySlots`, never
   disk; **Clear** wipes it) and whose **Copy Code** button (hidden
   with no run; `ContentCopyIcon`) shares the local run via
   `encodeResultCode`, whose Summarize ingests `parsePartySlots` (resolved through
   `resolverFromTable(u.servers)` — the party leader's own naming) →
   `buildParty`, closes itself, and opens `showPartySummary`: a
   full-view dialog (coverage + slot notes on top, a `mainColumns`
   pane, leaf taps open the per-player breakdown with
   p1/med/p99/jitter/loss per run + the raw host, "?" shows
   `partyLegendContent`; Close sits alone right). The primary action
   (Summarize) and Copy Code sit right; Close and Clear sit left —
   the dialog convention: negative actions left, positive right (also
   applied to Identify's Close/Look up, About, error-detail and
   breakdown rows). Keyboard shortcuts (canvas-level, Entry-gated):
   Enter = Test, Esc = cancel/close drawer, Ctrl/Cmd+C = copy summary.
   About shows the version + manual update check (the recovery path after
   "Never"). All runner-goroutine UI updates go through `fyne.Do`.
- `csv.go` — export with 3-decimal ms precision (UI table shows 1) for the
  median/p1/p99/jitter columns plus `tested_at` (RFC 3339 run start,
  same value on every row of one export), `max_pps`/`probe_size`
  (0 = default), and a `notes` column (`icmp-scrubbed` for scrubbed
  rows, whose loss/jitter cells are blank).
- `code.go` — the shareable *result code* codec: one finished run as
  one pasteable string (`base64url( 'G' | dictHash | flate(payload, dict) )`
  with `payload = uvarint n | n × (host | 9-byte bit-packed metrics)`).
  Codes carry **ping targets plus every metric** (loss 10b @0.01% —
  1023 is the ≥10.23% "dead" sentinel, decodes as 100; p1/med/p99 16b
  @0.1ms; jitter 8b @0.5ms; scrubbed/errored 2b) and nothing else: the
  host is the join key, and the decoder's own table names servers at
  Summarize time. The preset dictionary holds the embedded list's hosts
  (deterministic order, built at init); the fnv32a hash prefix picks
  the decoder's matching dictionary, and a mismatch just inflates
  without one — server-list updates shrink codes, never break them.
  A full embedded-list run encodes to ~630 chars (pinned by
  `TestResultCodeWithinDiscordBudget` < 2000 for Discord); payload size
  is value-independent (fixed-width metrics), so the number only moves
  when the server list does. Versionless by design: every failure maps
  to the single `errNotResultCode` sentinel — a code from an
  incompatible build is simply re-shared, never negotiated. If the
  format ever changes, update encode+decode+tests in one commit.
- `party.go` — the group-summary model (Fyne-free): `parsePartySlots`
  validates the teammate slots (blank skip, duplicates noted and
  dropped, invalid reported by slot number; `resultCode` extracts a
  code from messy pastes — whitespace-stripped fast path, then a
  rejoin of the base64url tokens ≥16 chars plus a short-tail candidate
  for arbitrary wrap widths, so a Copy Summary dump or chat preamble
  glued to the code doesn't kill the paste), `resolverFromTable` +
  `resolvePartyEntries` name decoded hosts from the party leader's OWN
  active table (unknown targets land in the `Unknown` region named by
  the host — DisplayName drops the dot for empty providers),
  `partyRunFromOutcomes` wraps the local run without a code round-trip,
  and `buildParty` intersects runs by lowercased host (only servers
  present in EVERY run are ranked; `partyStats` counts the exclusions
  the dialog warns about), merges each shared host with `mergeOutcomes`
  (column-wise worst across players; leaf `Scrubbed = !measurable` keeps
  the worstCase render contract), and inserts leaves in the first run's
  order (my run first = RegionOrder-first probe order). `members`
  (host → per-player outcomes) backs the breakdown dialog;
  `partyCoverageLine` renders the runs/ranked/excluded summary.
- `identify.go` — "Identify" dialog backend: `parseConnect` is
  deliberately loose (scans pasted text for `connect <addr>`, any IP
  token, or a whole-input hostname — port/semicolon/keyword all
  optional), then exact-IP match against the embedded list (definitive,
  no network), else RDAP via `rdap.org` (registrant vcard, with a
  follow-up `entity/ORG-…` lookup for RIPE-style handles) + best-effort
  PTR. `providerFromRDAP` substring-matches registrant/netname against
  our Provider labels, longest-needle-first so `dedicatednodes` claims
  i3D.net before `dedicated` could hit Dedicated.com; unmatched shows
  raw RDAP evidence. No city mapping (netname stays visible), no ICMP to
  game hosts (they filter it — see the Hivelocity scrubber note).
- `autotest.go` — CLI backend, no Fyne imports (unit-testable):
  `parseTestSpec` resolves the `--test` JSON grammar (region → city →
  provider, `all` reserved at every level, case-insensitive name matching,
  errors on unknown names or empty selections) into the same
  server-ID → checked map `stateFromDisabled` produces; `expansionForSelection`
  derives tree-node keys to expand so the selection is visible —
  **regions only, cities stay collapsed** (players select a location,
  not a provider; the tri-state city row shows the selection);
  `quoteWinArg`/`joinWinArgs` render Windows parameter strings for
  `elevateAndRestart`'s arg forwarding (CommandLineToArgvW quoting rules).
- `version.go` — **stamp-only**: `var version = "dev"`, overwritten
  wholesale by CI's tag-build step — never add anything else to this
  file (the first cut of the updater put `parseSemver` here and every
  tag build failed with `undefined: updateAvailable`).
  `semver.go` holds `parseSemver` (strict X.Y.Z, round-trip-rejects
  junk) and `updateAvailable` (semver beats legacy date tags both
  directions; dev never prompts).
- `update.go` + `update_windows.go`/`update_linux.go`/`update_darwin.go`/
  `update_other.go` — startup update check (GitHub latest-release API,
  gated by `shouldCheckUpdate`: dev/pref/`--test`/missing-asset all skip;
  `--force-update-check` bypasses), the Update/Later/Never dialog
  (release-notes preview + hyperlink when `body`/`html_url` are present),
  download with Content-Length verification + throttled byte-count
  progress (`updateProgress` struct: phase + written/total — unknown
  length shows a byte counter with the bar hidden), stdlib zip extraction
  with slip rejection, per-OS `replaceAndRelaunch`, and startup
  `cleanupOlds`. Dialog wiring and the `--force-update-check` flag live
  in `main.go`; the `updates_disabled` pref key is in `prefs.go`. The
  About dialog's "Check for updates now" runs `checkForUpdate(manual:
  true)`, which bypasses the dev/never gates and reports errors/"latest"
  in the status label (startup stays silent).
- `smoke_test.go` / tests — network tests skip under `-short`.

## Updating the server list

`.opencode/skills/update-servers/SKILL.md` documents the full workflow:
the user supplies the official HiperZ connection-test zips
(`hiperz_test_*.zip`, gitignored — downloaded manually from
hiperz.com, which 403s scripted fetches) plus the current FACEIT
region/city list; the skill parses the `.bat` scripts, maps HiperZ city
labels to FACEIT names, RDAP-resolves providers, rate/size-matrix-probes
new and changed hosts (dead / policer / size-filter / scrubbed), and
updates `embeddedServers` plus the tests that pin it
(`TestServerListMatchesFindings`, total-count in
`TestEmbeddedServersCoverAllRegions`). Codenames in the scripts are
unreliable (`GSL` = 3 companies; `hz` = 3 companies) — RDAP decides
Provider labels via `providerKeys` in `identify.go`.

## Domain context

Endpoints are HiperZ mirrors/speedtest hosts used as *proxies* for FACEIT
locations — only round-trip loss is measurable (no directional up/down loss;
game traffic is UDP to different ports). Some FACEIT EU locations
(Kazakhstan, Novosibirsk, Yekaterinburg, Vladivostok) have no public test
endpoint and are intentionally absent. User is in FL, USA — useful context
when interpreting latency/loss data.
