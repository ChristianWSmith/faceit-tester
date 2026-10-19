# faceit-tester

Quick pre-flight connection check for FACEIT CS2 players. Pick a region,
select the servers you care about, and measure ping and packet loss against
the official HiperZ test endpoints before you queue.

## What it measures

For each selected server the app sends ICMP echo requests for 10 seconds at
~64 packets/sec with 600-byte payloads — approximating CS2's UDP traffic
shape (~64 pps each way, 400–800 byte payloads). Results per server:

- **Ping (med / p1 / p99)** — round-trip time percentiles in milliseconds
- **Jitter** — mean variation between consecutive RTT samples (RFC 3550 style)
- **Loss** — round-trip packet loss percentage

Numbers are colored with CS2's telemetry HUD thresholds: green at-or-below
100ms ping / 10ms jitter / 2% loss, red above.

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

Place a `servers.json` next to the executable to replace the built-in lists
entirely. Same city/provider/host schema, plus optional per-server probe
overrides: `max_pps` (cap the send rate, e.g. under a policer),
`probe_size` (smaller ICMP payload for providers that filter large echo
packets), and `scrubbed` (loss/jitter are unmeasurable through a DDoS
scrubber — report ping only):

```json
{
  "NA": [
    { "city": "Chicago", "provider": "Psychz Networks", "host": "108.181.62.197" }
  ]
}
```

## Platform notes

- **Windows:** requires running as administrator (raw ICMP sockets). The app
  will refuse to start tests otherwise.
- **Linux:** works unprivileged when the kernel allows it
  (`net.ipv4.ping_group_range`). Otherwise run with
  `sudo setcap cap_net_raw=+ep ./faceit-tester` or as root.
- **macOS:** works unprivileged.

## Building

```sh
go build
```

For distributable builds with the app icon embedded (Windows exe icon,
macOS `.app` bundle, Linux `.desktop` + pixmap):

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
fyne package -release
```

CI packages all three platforms on every push with `fyne package` and
publishes them as workflow artifacts.

## License

MIT — see [LICENSE](LICENSE).
