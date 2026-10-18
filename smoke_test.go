package main

import (
	"context"
	"testing"
	"time"
)

func TestLivePingSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("network smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res := probeHost(ctx, "1.1.1.1", nil)
	t.Logf("sent=%d recv=%d loss=%.1f%% min=%v avg=%v max=%v jitter=%v err=%v",
		res.Sent, res.Recv, res.LossPercent, res.Min, res.Avg, res.Max, res.Jitter, res.Err)
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
