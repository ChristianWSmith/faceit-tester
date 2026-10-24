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
- **Results header alignment:** `Table.findX` inserts `SizeNamePadding`
  *between* columns, so the hand-built header row must be a plain `HBox`
  (same padding between children) with no outer `NewPadded` wrapper —
  zero-gap or padded-wrong headers drift progressively against the body.
  Table and header column widths must always come from the same constants.
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
  The spec replaces `u.state` and adds ancestors to `u.expanded`
  *before* `build()`. Auto-start is a goroutine launched *before*
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
  Cancel (or the dialog's close) discards everything — it's also the
  undo for deletes. `applyServerTable` in main.go swaps the live table:
  rebuilds the tree into `treeHolder`, re-derives state/expansion
  against the new IDs, and clears results.
- `prefs.go` — pure, unit-tested state logic. Persistence is a *denylist*
  of unchecked server IDs (`disabled_servers`) plus an allowlist of
  expanded tree nodes (`expanded_nodes`). Only IDs present in the active
  server set apply on load; stale IDs (deleted overrides) are ignored and
  pruned on save. First launch (empty key) = everything checked. Both
  lists are saved once from the window `SetCloseIntercept` handler (not
  per checkbox tap — per-toggle prefs writes made the UI sluggish on
  slow storage; a crash loses unsaved toggles, accepted tradeoff).
- `pinger.go` — pro-bing wrapper: per-server pacing (`probePacing`, default
  64pps/600B, `MaxPPS`/`ProbeSize` overrides), jitter = mean |ΔRTT|
  (RFC 3550), RTT stats are nearest-rank median/p1/p99 percentiles of the
  reply samples (avg/min/max proved less informative — one delayed reply
  skewed a whole row), platform fallback logic.
- `runner.go` — sequential 10s probes, context-cancelled (flat window even
  for paced hosts).
- `results.go` — hierarchical results model (region → city → provider)
  behind the results table: `addOutcome` appends leaves, `flatten`
  recomputes worst-case aggregates for region/city rows on every render
  (bold, ▸/▾ glyph, child count), `worstCase` is the shared column-wise
  worst merge (error-skip, scrubbed-RTT-only). Tapping a parent row
  toggles expansion (in-memory only; new runs start with regions
  expanded and cities collapsed).
- `main.go` — Fyne UI: region→city→provider tri-state selection tree
  (collapsed by default, expansion persisted), Test/Cancel toggle
  (green/red via Importance), results table driven by
  `resultsTree.flatten()` with per-cell green/yellow/red grading: below
  the warn thresholds green, at-or-above warn / at-or-below the CS2 HUD
  thresholds yellow (WarningImportance), strictly above red (ping
  70/100ms, jitter 5/10ms, loss 1/2%; RTT columns are p1/median/p99);
  region/city rows are bold worst-case aggregates (scrubbed providers
  contribute RTT only — jitter/loss show n/a). The Server column
  auto-sizes to the widest name cell (`nameColumnWidth` +
  `rebuildHeader`). Tap a parent row to collapse/expand it. All UI
  updates from the runner goroutine go through `fyne.Do`.
- Secondary actions (Export CSV, Edit Servers, Identify) live in a
  floating drawer above the button row: `widget.NewStack(main, drawerLayer)`
  where the drawer is a tap-catcher + right-aligned vertical button column
  on an overlay-colored backdrop panel. The arrow button toggles it and
  flips icon direction; every close path goes through `hideDrawer` so the
  icon can't desync from visibility. Built in `content()`, not `build()`.
- `csv.go` — export with 3-decimal ms precision (UI table shows 1) for the
  median/p1/p99/jitter columns plus `max_pps`/`probe_size` (0 = default)
  and a `notes` column (`icmp-scrubbed` for scrubbed rows, whose
  loss/jitter cells are blank).
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
  derives tree-node keys to expand so the selection is visible;
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
  `--force-update-check` bypasses), the Update/Later/Never dialog,
  download with Content-Length verification + throttled progress,
  stdlib zip extraction with slip rejection, per-OS
  `replaceAndRelaunch`, and startup `cleanupOlds`. Dialog wiring and the
  `--force-update-check` flag live in `main.go`; the `updates_disabled`
  pref key is in `prefs.go`.
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
