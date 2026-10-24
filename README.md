# faceit-tester

Quick pre-flight connection check for FACEIT CS2 players. Pick a region,
select the servers you care about, and measure ping and packet loss against
the official HiperZ test endpoints before you queue.

## What it measures

For each selected server the app sends ICMP echo requests for 10 seconds at
~64 packets/sec with 600-byte payloads — approximating CS2's UDP traffic
shape (~64 pps each way, 400–800 byte payloads). Results per server:

- **Ping (p1 / med / p99)** — round-trip time percentiles in milliseconds
- **Jitter** — mean variation between consecutive RTT samples (RFC 3550 style)
- **Loss** — round-trip packet loss percentage

Numbers are colored against CS2's telemetry HUD thresholds with a warning
band below them: green under 70ms ping / 5ms jitter / 1% loss, yellow up
to 100ms / 10ms / 2%, red above.

The results pane is a tree that mirrors the selection pane: regions start
expanded, cities start collapsed — tap a row to expand it. Region and city
rows are bold worst-case aggregates (column-by-column maximums across
their finished providers). Players cannot choose their provider within a
city, so the city row is what they might actually get.

A few providers answer ICMP at a fixed token quota regardless of send rate
(OVH, Psychz, Latitude.sh, …); those probe at a measured per-server rate so
loss reflects the path rather than the policer. Hivelocity sits behind a
DDoS scrubber whose loss cannot be measured at all — it reports ping only,
with loss/jitter shown as n/a.

These endpoints are HiperZ mirrors/speedtest hosts used as proxies for
FACEIT locations, not live game servers, so only round-trip loss is
measurable (directional up/down loss would require a cooperating endpoint).
Treat results as a rough signal of what to expect.

## Identify a match server

FACEIT's match page can copy a console connect command
(`connect <ip>:<port>;`). Paste it (or any text containing the server
address) into the **Identify** dialog and the app will:

1. Extract the address — the `connect` keyword, port, and semicolon are
   all optional; it scans for whatever IP is in the text.
2. Look it up via RDAP (free HTTPS registration data, no API key) to find
   the hosting company that owns the IP block — game servers are rented
   dedicated machines, so that's the provider you got assigned to.
3. Show every tested location with that provider and its worst-case
   numbers, so you can see how that provider performed from your
   location.

Identification is high-confidence but not infallible: DDoS-fronted or
relayed connections report the scrubber, not the datacenter. The raw RDAP
evidence (registrant, netname, country) is always shown next to the guess.

## Regions and cities

Server lists are trimmed to FACEIT's currently offered locations (city
groups use FACEIT's names; endpoints are transcribed from the official
HiperZ test scripts). Providers are the hosting companies that own the
endpoints per RDAP registration (OVH, Psychz Networks, Hetzner, …).

- **NA** — San Jose, Denver, Dallas, Chicago, Virginia
- **EU** — France, UK, Germany, Netherlands, Sweden, Finland, Moscow
- **SA** — Lima, São Paulo, Santiago, Buenos Aires
- **SEA** — Singapore
- **OCE** — Sydney

Some FACEIT EU locations (Kazakhstan, Novosibirsk, Yekaterinburg,
Vladivostok) have no public HiperZ test endpoint and are not included.

Servers appear in a three-level tree: region → city → provider, with
tri-state checkboxes (partial/dash when only some children are selected).
Expand a level with the ▸ button. Your selections and expanded tree
sections are saved when you close the app.

## Custom server lists

**Edit Servers** (in the actions drawer — the up-arrow button next to
Test) opens the built-in list for customization. Pick a server on the
left; edit its city, provider, host, and probe overrides on the right.
**+** next to a city adds a server to it; **Delete Server** removes the
selected one.

Nothing changes until you press **Apply**, which stores the entire list
as your custom server list — the first edit starts from the built-ins,
so your copy is always complete. Applying a list that matches the
built-ins, or pressing **Drop Custom List**, returns the app to the
built-ins. Cancel discards every unsaved edit (including deletes). The
custom list lives in the app's preferences alongside your selections —
not next to the executable — so it survives updates. Power users can
find it as the `servers_override` key in the same preferences file the
`updates_disabled` key lives in.

Per-server probe overrides, exposed in the editor as fields:

- **Max PPS** — cap the send rate for providers with a known ICMP
  policer (blank = default 64 pps).
- **Probe size** — smaller ICMP payload for providers that filter large
  echo packets (blank = default 600 bytes).
- **Scrubbed** — loss/jitter are unmeasurable through a DDoS scrubber;
  report ping only.

## Auto-updates

Release builds check GitHub for a newer version on startup. If one
exists, a dialog offers:

- **Update** — downloads the new release, replaces the running app, and
  restarts it (your selections are saved first).
- **Later** — dismisses; the same offer reappears next launch.
- **Never** — dismisses and never checks again.

Update downloads are in-process (no external tools needed). On Windows
the new instance inherits the current elevation, so there's no second
UAC prompt. If anything fails — network, permissions, a locked file —
the current version keeps running and the error dialog links the manual
download page. An old file that couldn't be deleted at replace time is
cleaned up automatically on the next launch.

To re-enable checks after choosing **Never**, delete the
`updates_disabled` key from the app's preferences file (location varies
by OS; search your user config dir for `com.christianws.faceit-tester`).

## Platform notes

- **Windows:** requires administrator rights (raw ICMP sockets). On launch
  the app immediately requests elevation (UAC prompt) and relaunches itself
  elevated — you never see an unelevated main window. If you dismiss the
  prompt, the app opens unelevated with Test disabled and a **Restart as
  Admin** button (which explains why elevation is needed, saves your
  selections, and retries) to go through the flow again.

  The first run of a downloaded exe may also show "Windows protected your
  PC" (SmartScreen). The build is unsigned — click **More info → Run
  anyway**, or clear the mark before the first run:

      Unblock-File .\faceit-tester-windows-amd64.exe

  (Smart App Control — on by default on some Windows 11 installs — can
  block unsigned apps with no "Run anyway" option. If the exe won't start
  at all, check Windows Security → App & browser control.)
- **Linux:** works unprivileged when the kernel allows it
  (`net.ipv4.ping_group_range`). Otherwise run with
  `sudo setcap cap_net_raw=+ep ./faceit-tester` or as root.
- **macOS:** works unprivileged.

## Command-line options

`--help` prints usage and the `--test` grammar below.

`--test <spec>` selects servers with a JSON object (region → city →
provider) and starts the test automatically once the window is ready:

```sh
faceit-tester --test '{"NA":"all"}'                          # entire NA region
faceit-tester --test '{"NA":{"Virginia":"all"}}'             # one city
faceit-tester --test '{"NA":{"Virginia":"OVH"}}'             # one provider
faceit-tester --test '{"NA":{"Virginia":["OVH","Datacamp"]}}' # provider list
faceit-tester --test '{"NA":"all","EU":{"Germany":"all"}}'   # multiple regions
faceit-tester --test '{"all":"all"}'                         # every server
```

Names match case-insensitively; `all` is reserved at every level; unknown
region/city/provider names are errors (printed to the terminal when one
is attached, and shown in the app's status bar). The spec is validated
before the UAC prompt, so a bad spec fails fast without an elevation
round-trip. The spec's selection replaces the checkbox state and — like
manual selections — is saved when you close the app. The window stays
open on the results when the run finishes.

On Windows the flag survives the elevation relaunch: launch with `--test`,
approve UAC, and the test starts in the elevated instance. In PowerShell,
wrap the spec in single quotes so the inner double quotes survive:

```powershell
.\faceit-tester.exe --test '{"NA":"all"}'
```

Note: the packaged Windows exe is built with `-H windowsgui` (no console),
so terminal output — `--help`, flag errors, and invalid `--test` spec
errors — is lost there (invalid specs still appear in the app's status
bar). Run a plain `go build` console binary if you need the messages on
a terminal.

`--force-update-check` shows the update dialog even when no newer release
exists (and even on dev builds or after **Never**) — used for reviewing
the update UI without shipping a release.

## Building

```sh
go build
```

On Windows, build with the GUI subsystem flag so no console window
appears (including on the Restart as Admin relaunch — the elevated
instance can't inherit your terminal, so a console-subsystem exe would
open a blank one):

```sh
go build -ldflags "-H windowsgui"
```

For distributable builds with the app icon embedded (Windows exe icon,
macOS `.app` bundle, Linux `.desktop` + pixmap):

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
fyne package -release
```

CI packages all three platforms on every push with `fyne package` and
publishes them as workflow artifacts; semver tag pushes (`X.Y.Z`) stamp
the tag into the binary and produce the release assets (plain zips —
the in-app updater unpacks them itself).

## License

MIT — see [LICENSE](LICENSE).
