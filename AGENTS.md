# AGENTS.md

## What this is

Single-package Go/Fyne desktop app: pre-flight connection tester for FACEIT
CS2 players. Pings HiperZ mirror/speedtest endpoints (not game servers) at
CS2-like rates (64pps, 600-byte payloads, 10s per server) via ICMP.

## Commands

```sh
go vet ./...
go test -short ./...    # -short skips network smoke tests (what CI runs)
go test ./...           # includes live pings to 1.1.1.1
go build
```

No linter/formatter beyond `gofmt` + `go vet`. CI (`.github/workflows/ci.yml`)
packages ubuntu/windows/macos with `fyne package -release` (needs
`fyne.io/fyne/v2/cmd/fyne@v2.8.2`) and uploads artifacts; Linux output is
`.tar.xz` (not `.tar.gz`), Windows `.exe`, macOS `.app`. Linux CI needs
`libgl1-mesa-dev xorg-dev` (Fyne cgo). Local Windows cross-compile needs
mingw (`CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1`), but CI builds natively so
this is rarely needed.

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
  Elevation check is build-tagged (`admin_windows.go`/`admin_other.go`);
  Test button is disabled with a message when not elevated. Linux tries
  unprivileged udp4 sockets, falls back to raw on permission errors
  (`isPermissionErr` matches several platform-specific strings).
- **Do not add per-server ICMP rate limits.** We tried: derived limits from
  a full probe run, but hivelocity experiments (identical runs giving
  0–3/8 replies, non-monotonic vs send rate, IPv6 clean vs IPv4 lossy)
  proved throttling is indistinguishable from path loss. Every server probes
  at fixed 64pps; bad endpoints just report their loss honestly.

## Architecture (all in package main)

- `servers.go` — embedded server tables (region → city → provider), trimmed
  to FACEIT-official locations with FACEIT city names (lax→San Jose,
  ny→Virginia). Transcribed from the official HiperZ test scripts.
  `servers.json` next to the exe replaces the lists entirely (city/provider/
  host JSON schema).
- `prefs.go` — pure, unit-tested state logic. Persistence is a *denylist*
  of unchecked server IDs (`disabled_servers`) plus an allowlist of
  expanded tree nodes (`expanded_nodes`). Only IDs present in the active
  server set apply on load; stale IDs (deleted overrides) are ignored and
  pruned on save. First launch (empty key) = everything checked.
- `pinger.go` — pro-bing wrapper: fixed pacing constants, jitter =
  mean |ΔRTT| (RFC 3550), platform fallback logic.
- `runner.go` — sequential 10s probes, context-cancelled.
- `main.go` — Fyne UI: region→city→provider tri-state tree (collapsed by
  default, expansion persisted), Test/Cancel toggle (green/red via
  Importance), CSV export button, results table with per-cell green/red
  grading against CS2 HUD thresholds (100ms ping / 10ms jitter / 2% loss)
  and bold per-city worst-case summary rows. All UI updates from the
  runner goroutine go through `fyne.Do`.
- `csv.go` — export with 3-decimal ms precision (UI table shows 1).
- `smoke_test.go` / tests — network tests skip under `-short`.

## Domain context

Endpoints are HiperZ mirrors/speedtest hosts used as *proxies* for FACEIT
locations — only round-trip loss is measurable (no directional up/down loss;
game traffic is UDP to different ports). Some FACEIT EU locations
(Kazakhstan, Novosibirsk, Yekaterinburg, Vladivostok) have no public test
endpoint and are intentionally absent. User is in FL, USA — useful context
when interpreting latency/loss data.
