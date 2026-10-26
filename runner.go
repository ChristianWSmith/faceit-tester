package main

import (
	"context"
	"time"
)

const probeWindow = 10 * time.Second

// ServerOutcome is one finished (or failed) server test, reported via
// onDone in sequential order.
type ServerOutcome struct {
	Server Server
	Result PingResult
}

// runTests probes each server sequentially for probeWindow each, honoring
// ctx cancellation. onStatus is called before each server starts (with the
// server itself so callers can key UI state by ID); onTick reports live
// counts for the current server; onDone delivers each finished result.
// Callbacks may be invoked from a background goroutine.
func runTests(
	ctx context.Context,
	servers []Server,
	onStatus func(s Server, index, total int),
	onTick func(sent, recv int),
	onDone func(outcome ServerOutcome),
) {
	total := len(servers)
	for i, srv := range servers {
		if ctx.Err() != nil {
			return
		}
		onStatus(srv, i+1, total)

		// Full budget: probeWindow for sending (bounded by the paced
		// probe count), then replyGrace of receive-only time for
		// in-flight replies.
		serverCtx, cancel := context.WithTimeout(ctx, probeWindow+replyGrace)
		result := probeHost(serverCtx, srv.Host, srv.MaxPPS, srv.ProbeSize, onTick)
		cancel()

		onDone(ServerOutcome{Server: srv, Result: result})
	}
}
