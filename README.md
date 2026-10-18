# faceit-tester

Quick pre-flight connection check for FACEIT CS2 players. Pick a region,
select the servers you care about, and measure ping and packet loss against
the official HiperZ test endpoints before you queue.

## What it measures

For each selected server the app sends ICMP echo requests for 10 seconds at
~64 packets/sec with 600-byte payloads — approximating CS2's UDP traffic
shape (~64 pps each way, 400–800 byte payloads). Results per server:

- **Avg / Min / Max** — round-trip time in milliseconds
- **Jitter** — mean variation between consecutive RTT samples (RFC 3550 style)
- **Loss** — round-trip packet loss percentage

Numbers are colored with CS2's telemetry HUD thresholds: green at-or-below
100ms ping / 10ms jitter / 2% loss, red above. After every selected provider
of a city finishes, a bold worst-case row summarizes the column-by-column
maximums — players cannot choose their provider within a city, so that row
is what they might actually get.

These endpoints are HiperZ mirrors/speedtest hosts used as proxies for
FACEIT locations, not live game servers, so only round-trip loss is
measurable (directional up/down loss would require a cooperating endpoint).
Treat results as a rough signal of what to expect.

## Regions and cities

Server lists are trimmed to FACEIT's currently offered locations (city
groups use FACEIT's names; endpoints are transcribed from the official
HiperZ test scripts):

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
sections are remembered between launches.

## Custom server lists

Place a `servers.json` next to the executable to replace the built-in lists
entirely:

```json
{
  "NA": [
    { "city": "Chicago", "provider": "PS", "host": "108.181.62.197" }
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
