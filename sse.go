package caddyscope

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ssePayload is the JSON structure sent to SSE clients.
type ssePayload struct {
	VHosts    []VHost     `json:"vhosts"`
	Stats     []HostStats `json:"stats"`
	Warnings  []string    `json:"warnings,omitempty"`
	Timestamp string      `json:"timestamp"`
}

// snapshot holds pre-marshaled JSON bytes for SSE clients to read.
type snapshot struct {
	mu        sync.RWMutex
	jsonBytes []byte
}

// update gathers vhosts and stats, marshals to JSON, and stores under lock.
func (s *snapshot) update(cs *CaddyScope) {
	cs.resolveHTTPApp()

	payload := ssePayload{
		VHosts:    cs.currentVhosts(),
		Stats:     cs.currentStats(),
		Warnings:  cs.metricsWarnings(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		cs.logError("failed to marshal snapshot", zap.Error(err))
		return
	}

	s.mu.Lock()
	s.jsonBytes = data
	s.mu.Unlock()
}

// metricsWarnings returns user-facing hints when stats are missing or partial.
func (cs *CaddyScope) metricsWarnings() []string {
	if cs.gatherer == nil {
		return []string{"Prometheus metrics are not available. Enable the metrics global option in your Caddyfile."}
	}
	if cs.httpApp != nil && !perHostMetricsEnabled(cs.httpApp) {
		return []string{"Per-host metrics are disabled. Enable the metrics global option with per_host for per-host stats."}
	}
	return nil
}

// get returns the latest snapshot bytes, or nil if no snapshot has been taken yet.
func (s *snapshot) get() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jsonBytes
}

// runSnapshotLoop runs the background snapshot loop until the context is cancelled.
func (cs *CaddyScope) runSnapshotLoop(ctx context.Context) {
	defer close(cs.done)

	// Initial gather immediately.
	cs.refreshSnapshot(ctx)

	ticker := time.NewTicker(time.Duration(cs.Refresh))
	defer ticker.Stop()

	// HTTP/3 connections get no shutdown hook (see registerShutdownHook), so
	// also poll for process exit. caddy.Exiting() flips before Stop() runs.
	exitPoll := time.NewTicker(exitPollInterval)
	defer exitPoll.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-cs.stopping:
			return
		case <-exitPoll.C:
			if cs.processExiting() {
				cs.signalStop()
				return
			}
		case <-ticker.C:
			cs.refreshSnapshot(ctx)
		}
	}
}

// refreshSnapshot takes a new snapshot unless the context is already cancelled.
func (cs *CaddyScope) refreshSnapshot(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	cs.snap.update(cs)
}

// processExiting reports whether Caddy is shutting down the whole process.
func (cs *CaddyScope) processExiting() bool {
	return cs.exiting != nil && cs.exiting()
}

// signalStop closes cs.stopping exactly once, releasing every SSE handler.
func (cs *CaddyScope) signalStop() {
	cs.stopOnce.Do(func() { close(cs.stopping) })
}

// exitPollInterval bounds how long an HTTP/3 SSE stream can hold up process
// exit. Caddy's stdlib servers run shutdown hooks; quic-go's http3.Server has
// none, and caddy.OnExit fires only after Stop() returns, so polling is the
// only option for h3.
const exitPollInterval = 250 * time.Millisecond

// registerShutdownHook wires cs.stopping to the underlying stdlib HTTP server's
// shutdown. Caddy cancels the provision context (which triggers Cleanup) only
// AFTER server.Shutdown() drains connections, so cancelCtx alone cannot release
// a blocking SSE handler. The stdlib server reads its RegisterOnShutdown
// callbacks at shutdown time, so registering here — lazily, per request —
// fires the hook when draining begins and lets the handler exit.
//
// Caddy runs one stdlib *http.Server per server block and shutdown waits for
// all of them, so the hook must be registered on every server that carries
// dashboard traffic — not just the first one seen.
//
// HTTP/3 requests carry quic-go's own context key, not http.ServerContextKey,
// and http3.Server has no shutdown hook. Those streams are released by the
// exit poll in runSnapshotLoop instead.
func (cs *CaddyScope) registerShutdownHook(r *http.Request) {
	srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server)
	if !ok || srv == nil {
		return
	}
	if _, loaded := cs.hookedServers.LoadOrStore(srv, struct{}{}); loaded {
		return
	}
	srv.RegisterOnShutdown(cs.signalStop)
}

// sseWriteTimeout caps how long a write to a stalled client can block the
// handler. Without it, a full client TCP buffer keeps the handler stuck in a
// write (not the select), where cs.stopping cannot release it during shutdown.
const sseWriteTimeout = 10 * time.Second

// serveAPIStream handles the SSE /api/stream endpoint.
func (cs *CaddyScope) serveAPIStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers before any writes or flushes.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)

	// Send immediate snapshot if available.
	if err := writeEvent(w, rc, cs.snap.get()); err != nil {
		return
	}

	ticker := time.NewTicker(time.Duration(cs.Refresh))
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-cs.cancelCtx.Done():
			return
		case <-cs.stopping:
			return
		case <-ticker.C:
			if err := writeEvent(w, rc, cs.snap.get()); err != nil {
				return
			}
		}
	}
}

// writeEvent sends one SSE stats event and flushes it. A nil snapshot (none
// taken yet) is skipped.
func writeEvent(w http.ResponseWriter, rc *http.ResponseController, data []byte) error {
	if data == nil {
		return nil
	}
	// Best-effort: not all writers support deadlines (e.g. tests).
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if _, err := fmt.Fprintf(w, "event: stats\ndata: %s\n\n", data); err != nil {
		return err
	}
	return rc.Flush()
}
