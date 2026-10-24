---
name: update-servers
description: Use when updating the built-in FACEIT server list in servers.go from official sources — e.g. "update the servers", "sync with hiperz", "new hiperz zips", "check if servers moved/died", "refresh embeddedServers". Covers parsing hiperz_test_*.zip connection-test scripts, RDAP provider resolution, and ICMP rate/size matrix probing to classify endpoints as dead, moved, rate-limited, size-filtered, or scrubbed.
---

# Update Built-in Server List

Refresh `embeddedServers` in `servers.go` using two official inputs:

1. **HiperZ connection-test zips** — https://www.hiperz.com/hiperz_latency.php
   (one zip per region: `hiperz_test_{NA,EU,SA,SEA,OCE}.zip`; AFRICA/DUBAI
   exist too — ignore unless the FACEIT list includes those regions).
   hiperz.com is Cloudflare-403'd to scripted fetches — **the user must
   download the zips manually** and drop them in the repo root (they are
   gitignored via `hiperz_test*`).
2. **Official FACEIT region/city list** —
   https://support.faceit.com/hc/en-us/articles/10605801463580-Server-FAQs
   — pasted by the user each run (FACEIT adds/removes locations over time).

Everything is package main; provider names are RDAP-resolved hosting
companies, cities use FACEIT client names. The HiperZ scripts are only
*proxies* — every host still needs live verification before it ships in the
list.

## Phase 1 — Gather inputs

Ask the user (question tool) for both inputs before doing anything:

1. The HiperZ zips — confirm which `hiperz_test_*.zip` files are present in
   the repo root; ask if any region is missing.
2. The current official FACEIT region/city list (pasted text).

Baseline snapshot (as of 2026-10-20) — use only to sanity-check against what
the user pastes; **the user's paste is authoritative**:

- **NA:** San Jose, Denver, Dallas, Chicago, Virginia
- **SA:** Lima, São Paulo, Santiago, Buenos Aires
- **EU:** France, UK, Germany, Netherlands, Sweden, Finland, Moscow,
  Kazakhstan, Novosibirsk, Yekaterinburg, Vladivostok
- **SEA:** Singapore
- **OCE:** Sydney

Kazakhstan, Novosibirsk, Yekaterinburg, and Vladivostok have **no public
HiperZ test endpoint** — they stay absent from our list by design. Moscow is
in the EU zip even though it's geographically east.

## Phase 2 — Parse the zips

Each region zip contains one `.bat` (plus `__MACOSX` junk on macOS — skip
it). The format pairs an echo label with a tracert target:

```bat
echo - testing chicago.PS - please wait...
tracert -4 108.181.62.197 2>&1 >> hiperz_test.txt 2>&1
```

Extract (label, host) pairs where label is `<city>.<codename>` (city may
itself contain dots — take the *first* segment as the city; some labels like
`finland` have no dot):

```sh
unzip -q hiperz_test_NA.zip -d /tmp/hiperz/NA
awk '/^echo - testing /{ label=$4 } /^tracert -4 /{ print label, $3 }' \
  /tmp/hiperz/NA/hiperz_test_NA.bat
```

Hosts may be bare IPs (`108.181.62.197`) or hostnames
(`mirrors-lax.webnx.com`, `fsn.icmp.hetzner.com`) — keep whatever the script
publishes; resolve hostnames to IPs only for the RDAP lookup.

## Phase 3 — Map to FACEIT cities

Match the label's first segment (case-insensitive) against this table; keep
only cities on the user's FACEIT list. **Drop everything else** — HiperZ
tests many locations FACEIT doesn't serve (miami, gcp.iowa, tr/Turkey,
hongkong, tokyo, mumbai, melbourne, johannesburg, capetown, dubai, bahrain,
`chime.aws`, …). Entire zips (AFRICA, DUBAI) drop out the same way when the
region isn't on the FACEIT list.

| HiperZ city | FACEIT city |
|---|---|
| chicago | Chicago |
| dallas | Dallas |
| denver | Denver |
| lax | San Jose |
| ny | Virginia |
| de | Germany |
| nl | Netherlands |
| se | Sweden |
| finland | Finland |
| mow | Moscow |
| uk | UK |
| fr | France |
| brazil | São Paulo |
| buenosaires | Buenos Aires |
| santiago | Santiago |
| lima | Lima |
| singapore | Singapore |
| sydney | Sydney |

Diff the mapped set against the current `embeddedServers` (match by
city + host): classify each as **unchanged**, **new**, **changed host** (same
city, different host — candidate moved), or **gone from script** (present in
our list, absent from the zip — candidate removed, but see Phase 4: only
remove with evidence it's dead; a script omission alone isn't proof).

## Phase 4 — RDAP every new/changed host

The codename after the city dot is a **hint only** — it is routinely wrong or
ambiguous (`GSL` resolves to three different companies; `m` is MyLoc in
Germany but M247 in Sweden; `hz` spans Dedicated.com/ReliableSite/NForce).
The `Provider` label must come from RDAP registrant/netname via
`providerFromRDAP` (`identify.go`), which substring-matches against
`providerKeys` longest-needle-first.

Write a temporary harness `zz_rdap_test.go` in the repo root (delete when
done — do not commit):

```go
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRDAPDump(t *testing.T) {
	targets := os.Getenv("RDAP_TARGETS")
	if targets == "" {
		t.Skip("RDAP_TARGETS not set")
	}
	c := newRDAPClient()
	for _, host := range strings.Split(targets, ",") {
		host = strings.TrimSpace(host)
		ip := host
		if net.ParseIP(host) == nil {
			ips, err := lookupHostIPs(context.Background(), host)
			if err != nil || len(ips) == 0 {
				fmt.Printf("RDAP %s: resolve failed: %v\n", host, err)
				continue
			}
			ip = ips[0].IP.String()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		reg, netname, cc, err := c.lookupIP(ctx, ip)
		cancel()
		if err != nil {
			fmt.Printf("RDAP %s (%s): error: %v\n", host, ip, err)
			continue
		}
		fmt.Printf("RDAP %s (%s): provider=%q registrant=%q netname=%q cc=%s\n",
			host, ip, providerFromRDAP(reg, netname), reg, netname, cc)
	}
}
```

```sh
RDAP_TARGETS="108.181.62.197,mirrors-lax.webnx.com" go test -run TestRDAPDump -v
```

- `provider=""` → the registrant is a hosting company we don't label yet:
  add a needle → label entry to `providerKeys` in `identify.go` (label =
  the company's public name, matching the style of existing entries).
- Registrant **doesn't match** the expected provider for that city (e.g. the
  IP moved to a different company) → treat as a changed host; the new RDAP
  company becomes the Provider label.
- RDAP country wildly off the city's continent is worth a note (Psychz
  published an Australian IP as their Singapore test target once), but the
  HiperZ script is still the source for *which* hosts FACEIT's tooling uses.

## Phase 5 — Matrix probe (dead / moved / rate-limited / filtered)

Every **new** and **changed** host gets a live ICMP matrix. Unchanged hosts
already in the list keep their existing MaxPPS/ProbeSize/Scrubbed overrides
— don't re-measure them without cause.

Write `zz_matrix_test.go` (temporary, delete when done):

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMatrixProbe(t *testing.T) {
	targets := os.Getenv("MATRIX_TARGETS")
	if targets == "" {
		t.Skip("MATRIX_TARGETS not set")
	}
	cells := []struct{ pps, size int }{
		{64, 600}, // CS2-like default
		{32, 600}, // rate curve
		{16, 600},
		{5, 600},  // below any policer quota
		{64, 56},  // small-payload filter check
	}
	for _, host := range strings.Split(targets, ",") {
		host = strings.TrimSpace(host)
		for _, c := range cells {
			ctx, cancel := context.WithTimeout(context.Background(), probeWindow+replyGrace+2*time.Second)
			res := probeHost(ctx, host, c.pps, c.size, nil)
			cancel()
			fmt.Printf("MATRIX %s %3dpps %3dB: sent=%3d recv=%3d loss=%6.2f%% med=%s p99=%s err=%v\n",
				host, c.pps, c.size, res.Sent, res.Recv, res.LossPercent,
				formatMS(res.Median), formatMS(res.P99), res.Err)
		}
	}
}
```

```sh
MATRIX_TARGETS="1.2.3.4,example.com" go test -run TestMatrixProbe -v -timeout 30m
```

≈51s per host (5 × ~10s windows). **Batch 5–10 hosts per run.** On Windows
the probes need admin (raw ICMP); on Linux they usually work unprivileged
(`probeHost` falls back to raw sockets itself). Classify each host by the
recv counts (loss % alone is misleading — see PACKET_LOSS_FINDINGS.md):

| Pattern | Verdict | Action |
|---|---|---|
| 100% loss in every cell | dead or fully filtered | Remove. Optionally confirm the box is really gone with a TCP probe (`curl -m 3 http://HOST/` or `timeout 3 bash -c '</dev/tcp/HOST/80'`) — open TCP + silent ICMP means unmeasurable, still remove. |
| recv **constant** across 16/32/64pps @600B, **clean** @5pps | ICMP policer (token quota) | Set `MaxPPS` = floor(recv ÷ 10s × 0.9), floor at 1. Re-probe at that rate and expect ≈0% loss. E.g. recv=105 every run → 10.5pps quota → MaxPPS 9. |
| 600B ≈100% loss @all rates, 56B clean @64pps | payload size filter | Set `ProbeSize: 56`. |
| ~1 reply/sec cap at *every* rate incl. 5pps (recv≈10 whenever send≥10) | DDoS scrubber, not a policer | Set `Scrubbed: true` (loss/jitter become n/a; default probe rate for RTT). Don't try MaxPPS=1 — that's the scrubber's stochastic zone. |
| loss % roughly constant across rates, small at 64pps | clean (real path loss only) | No override. |
| loss % constant but >10% at 64pps and >5% at 16pps | suspicious path | No override; flag in the report for the user. |

A "moved" host (same city+provider, new IP) is probed as new; the old entry
is removed only when the matrix says it's dead — if the old IP still answers
cleanly, keep **both** (disambiguate Provider labels, see Phase 6) and let
the user decide.

## Phase 6 — Edit the list

Update `embeddedServers` in `servers.go`:

- Cities use the FACEIT names from Phase 3; hosts exactly as the HiperZ
  script publishes them.
- Preserve the house comment style: one-line comments above special entries
  explaining *why* a MaxPPS/ProbeSize/Scrubbed override exists (quota
  measurements, filter behavior) — see the OVH UK and NFOservers entries for
  the pattern.
- **ID uniqueness:** `City.Provider` must be unique per region
  (`TestEmbeddedServersCoverAllRegions` enforces it). When one provider has
  two endpoints in one city, disambiguate in the Provider label — existing
  pattern: `"Latitude.sh SP1"` / `"Latitude.sh SP2"`,
  `"Google Cloud (West4 Premium)"`.
- Don't resurrect previously retired endpoints (see the retired list in
  `TestServerListMatchesFindings`) without fresh matrix evidence they
  answer again.
- If a new provider label was added, it belongs in `providerKeys`
  (`identify.go`) too, so the Identify dialog agrees with the table.

Update the tests that pin the list:

- `TestEmbeddedServersCoverAllRegions` — total count + the per-region
  breakdown comment.
- `TestFaceitCityNames` — if city sets changed.
- `TestServerListMatchesFindings` — MaxPPS map, NFO host/ProbeSize,
  scrubbed entries, retired-endpoint list.
- `smoke_test.go` — if a referenced host (OVH UK, NFO Chicago) changed.

## Phase 7 — Verify and report

```sh
gofmt -w . && gofmt -l .
go vet ./...
go test -short ./...
```

`go test ./...` (without `-short`) additionally live-pings 1.1.1.1, paced
OVH UK, and NFO 56B — run it when the environment allows raw ICMP.

Delete `zz_rdap_test.go` / `zz_matrix_test.go`, then summarize for the user:
added / removed / changed hosts with the evidence per host (RDAP
registrant, matrix verdict), MaxPPS/ProbeSize/Scrubbed values chosen and
why, and any FACEIT locations that remain without a test endpoint. **Do not
commit** — the user reviews and commits.

## Gotchas

- Codenames lie; RDAP is the source of truth for Provider. Never copy a
  codename-derived provider label without a matching `providerKeys` hit.
- Constant *recv count* across rates = policer; constant *loss %* = real
  loss. Confusing the two is how a working paced host gets mislabeled dead
  (or vice versa) — always run the rate curve before setting MaxPPS.
- Scrubbed ≠ rate-limited: a scrubber caps at ~1 reply/sec at *any* send
  rate and its loss is unmeasurable; a policer answers loss-free below its
  quota. The 5pps cell is the discriminator.
- HiperZ label geography can lie (a "singapore" label pointed at a Psychz
  *Australian* IP). Trust RDAP country over the label when they disagree,
  and note it in the report.
- A custom server list lives in app preferences (`servers_override`) and
  replaces the whole list at runtime, but this skill edits the
  **embedded** table — the shipped default. Don't create or enable a
  custom list as part of this workflow (and if one is active when
  comparing live behavior, Drop Custom List first in the app).
- The zips and extracted files match `hiperz_test*` in `.gitignore`; the
  temp `zz_*_test.go` harnesses must be deleted, not committed.
