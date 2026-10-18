package main

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus-community/pro-bing"
)

const (
	// probeInterval is ~64 packets/sec, matching CS2's send rate.
	probeInterval = time.Second / 64
	// probeSize is the ICMP payload size. CS2 UDP payloads range 400-800
	// bytes; 600 sits mid-range for equivalent wire behavior.
	probeSize = 600
	// probeCount is the number of probes per server. pro-bing stops sending
	// once Count is reached but keeps receiving until the context expires,
	// so the runner's probeWindow+replyGrace budget gives a receive-only
	// window for late replies after the last send.
	probeCount = int(probeWindow / probeInterval)
	// resolveTimeout bounds DNS resolution per target.
	resolveTimeout = 5 * time.Second
	// replyGrace is the receive-only window after the last probe is sent
	// (probeWindow) so in-flight echo replies can still be counted.
	replyGrace = 250 * time.Millisecond
)

// PingResult holds the outcome of probing one server.
type PingResult struct {
	Sent        int
	Recv        int
	LossPercent float64
	Min         time.Duration
	Avg         time.Duration
	Max         time.Duration
	Jitter      time.Duration
	Err         error
}

// probeHost sends ICMP echo requests at CS2-like rates to host. ctx should
// carry a deadline of probeWindow+replyGrace (the runner supplies this) so
// sends stop after probeCount probes and the remaining time is receive-only
// for late replies. ctx cancellation aborts immediately. onTick, if non-nil,
// is called from the pinger goroutines with running sent/received counts;
// callers must marshal to the UI thread themselves.
func probeHost(ctx context.Context, host string, onTick func(sent, recv int)) PingResult {
	privileged := runtime.GOOS == "windows"
	result, err := probeOnce(ctx, host, privileged, onTick)
	if err != nil && runtime.GOOS != "windows" && isPermissionErr(err) {
		// Unprivileged datagram ICMP unavailable (ping_group_range etc.);
		// retry with raw sockets.
		result, err = probeOnce(ctx, host, true, onTick)
	}
	if err != nil && result.Sent == 0 {
		result.Err = err
	}
	return result
}

func probeOnce(ctx context.Context, host string, privileged bool, onTick func(sent, recv int)) (PingResult, error) {
	p, err := probing.NewPinger(host)
	if err != nil {
		return PingResult{}, err
	}
	p.SetPrivileged(privileged)
	p.Count = probeCount
	p.Interval = probeInterval
	p.Size = probeSize
	p.ResolveTimeout = resolveTimeout
	p.RecordRtts = true

	var sent, recv atomic.Int64
	if onTick != nil {
		p.OnSend = func(*probing.Packet) {
			onTick(int(sent.Add(1)), int(recv.Load()))
		}
		p.OnRecv = func(*probing.Packet) {
			r := int(recv.Add(1))
			onTick(int(sent.Load()), r)
		}
	} else {
		p.OnSend = func(*probing.Packet) { sent.Add(1) }
		p.OnRecv = func(*probing.Packet) { recv.Add(1) }
	}

	// The runner's context carries the full probeWindow+replyGrace budget;
	// supply a default when called without a deadline (e.g. tests).
	runCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		runCtx, cancel = context.WithTimeout(ctx, probeWindow+replyGrace)
		defer cancel()
	}

	runErr := p.RunWithContext(runCtx)
	stats := p.Statistics()
	result := buildResult(stats)
	if runErr != nil && result.Sent == 0 {
		return result, runErr
	}
	return result, nil
}

func buildResult(stats *probing.Statistics) PingResult {
	res := PingResult{
		Sent: stats.PacketsSent,
		Recv: stats.PacketsRecv,
	}
	if stats.PacketsSent > 0 {
		res.LossPercent = float64(stats.PacketsSent-stats.PacketsRecv) / float64(stats.PacketsSent) * 100
	}
	if len(stats.Rtts) > 0 {
		res.Min = stats.MinRtt
		res.Avg = stats.AvgRtt
		res.Max = stats.MaxRtt
		res.Jitter = computeJitter(stats.Rtts)
	}
	return res
}

// computeJitter returns the mean absolute difference between consecutive RTT
// samples (RFC 3550 style).
func computeJitter(rtts []time.Duration) time.Duration {
	if len(rtts) < 2 {
		return 0
	}
	var sum time.Duration
	for i := 1; i < len(rtts); i++ {
		d := rtts[i] - rtts[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / time.Duration(len(rtts)-1)
}

func isPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted") ||
		strings.Contains(msg, "Access is denied") ||
		strings.Contains(msg, "not been configured into the system") ||
		strings.Contains(msg, "not configured into the system")
}
