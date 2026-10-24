package main

import (
	"context"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
)

func TestLivePingSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("network smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res := probeHost(ctx, "1.1.1.1", 0, 0, nil)
	t.Logf("sent=%d recv=%d loss=%.1f%% median=%v p1=%v p99=%v jitter=%v err=%v",
		res.Sent, res.Recv, res.LossPercent, res.Median, res.P1, res.P99, res.Jitter, res.Err)
	if res.Err != nil {
		t.Fatalf("probe error: %v", res.Err)
	}
	if res.Sent == 0 {
		t.Fatal("no probes sent")
	}
	if res.Recv == 0 {
		t.Fatal("no replies received — ICMP may be blocked locally")
	}
}

// TestLivePacedOVHUK probes OVH UK at its embedded MaxPPS (9). The provider's
// policer answers ~10.5pps regardless of send rate, so paced probing should
// be near loss-free while 64pps historically showed ~84% loss.
func TestLivePacedOVHUK(t *testing.T) {
	if testing.Short() {
		t.Skip("network smoke test")
	}
	s, ok := findEmbedded("UK.OVH")
	if !ok || s.MaxPPS == 0 {
		t.Fatal("UK.OVH missing or lacks MaxPPS override")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeWindow+replyGrace+2*time.Second)
	defer cancel()
	res := probeHost(ctx, s.Host, s.MaxPPS, s.ProbeSize, nil)
	if res.Err != nil {
		t.Fatalf("probe error: %v", res.Err)
	}
	t.Logf("UK.OVH @%dpps: sent=%d recv=%d loss=%.1f%%", s.MaxPPS, res.Sent, res.Recv, res.LossPercent)
	if res.Sent == 0 {
		t.Fatal("no probes sent")
	}
	if float64(res.Recv) < 0.8*float64(res.Sent) {
		t.Fatalf("paced probe still heavily lossy: sent=%d recv=%d", res.Sent, res.Recv)
	}
}

// TestLiveNFOChicago56B probes the NFO replacement host at the default rate
// with its 56-byte payload override (600-byte packets are filtered 100%).
func TestLiveNFOChicago56B(t *testing.T) {
	if testing.Short() {
		t.Skip("network smoke test")
	}
	s, ok := findEmbedded("Chicago.NFOservers")
	if !ok || s.ProbeSize == 0 {
		t.Fatal("Chicago.NFOservers missing or lacks ProbeSize override")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeWindow+replyGrace+2*time.Second)
	defer cancel()
	res := probeHost(ctx, s.Host, s.MaxPPS, s.ProbeSize, nil)
	if res.Err != nil {
		t.Fatalf("probe error: %v", res.Err)
	}
	t.Logf("Chicago.NFOservers @%dB: sent=%d recv=%d loss=%.1f%%", s.ProbeSize, res.Sent, res.Recv, res.LossPercent)
	if res.Sent == 0 {
		t.Fatal("no probes sent")
	}
	if float64(res.Recv) < 0.9*float64(res.Sent) {
		t.Fatalf("small-payload probe lossy: sent=%d recv=%d", res.Sent, res.Recv)
	}
}

func findEmbedded(id string) (Server, bool) {
	for _, region := range RegionOrder {
		for _, s := range embeddedServers[region] {
			if s.ID() == id {
				return s, true
			}
		}
	}
	return Server{}, false
}

// TestLiveIdentifyRDAP looks up a Hivelocity IP through the real RDAP
// bootstrap. The embedded host for Dallas.Hivelocity is a hostname, so
// the IP goes through the RDAP path (not the exact-list shortcut).
func TestLiveIdentifyRDAP(t *testing.T) {
	if testing.Short() {
		t.Skip("network smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	servers, err := loadServers(test.NewApp().Preferences())
	if err != nil {
		t.Fatalf("loadServers: %v", err)
	}
	res := identifyTarget(ctx, "connect 107.155.84.58:27015;", servers)
	if res.Err != nil {
		t.Fatalf("identify error: %v", res.Err)
	}
	t.Logf("registrant=%q netname=%q country=%q provider=%q ptr=%v",
		res.Registrant, res.Netname, res.Country, res.Provider, res.PTR)
	if res.Provider != "Hivelocity" {
		t.Errorf("provider = %q, want Hivelocity", res.Provider)
	}
}
