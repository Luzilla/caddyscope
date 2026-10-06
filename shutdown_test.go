package caddyscope

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/stretchr/testify/require"
)

// requireClosed fails the test unless ch closes within timeout.
func requireClosed(t *testing.T, ch <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		require.FailNow(t, msg)
	}
}

// requireOpen fails the test if ch is already closed.
func requireOpen(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
		require.FailNow(t, msg)
	default:
	}
}

// TestRegisterShutdownHook_ClosesStoppingOnServerShutdown verifies the core of
// the shutdown fix: the hook registered on the stdlib *http.Server fires when
// server.Shutdown() begins draining, which closes cs.stopping. This is what
// releases a blocked SSE handler.
//
// Why this matters: on process exit (SIGINT/SIGTERM) Caddy blocks in
// server.Shutdown() waiting for connections to drain, and only cancels the
// provision context (and runs Cleanup) AFTER that. So cancelCtx cannot release
// an SSE handler in time — the stdlib RegisterOnShutdown callback can, because
// stdlib runs those callbacks at the start of Shutdown, before the drain wait.
func TestRegisterShutdownHook_ClosesStoppingOnServerShutdown(t *testing.T) {
	cs := &CaddyScope{
		stopping:      make(chan struct{}),
		stopOnce:      &sync.Once{},
		hookedServers: &sync.Map{},
	}

	// Spin up a real stdlib server so the request context carries a
	// *http.Server under http.ServerContextKey, exactly as in production.
	registered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.registerShutdownHook(r)
		close(registered)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err, "request failed")
	_ = resp.Body.Close()
	<-registered

	requireOpen(t, cs.stopping, "stopping closed before server shutdown")

	// Begin shutdown; the hook must close stopping.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Config.Shutdown(ctx), "server shutdown")

	requireClosed(t, cs.stopping, time.Second, "stopping was not closed by the shutdown hook")
}

// TestRegisterShutdownHook_MultipleServers verifies the hook is registered on
// every distinct stdlib server, not just the first one seen. Caddy runs one
// stdlib *http.Server per server block, and process-exit shutdown waits for
// ALL of them to drain — an SSE connection on an un-hooked server hangs for
// the full grace period.
func TestRegisterShutdownHook_MultipleServers(t *testing.T) {
	cs := &CaddyScope{
		stopping:      make(chan struct{}),
		stopOnce:      &sync.Once{},
		hookedServers: &sync.Map{},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.registerShutdownHook(r)
		w.WriteHeader(http.StatusOK)
	})
	srvA := httptest.NewServer(handler)
	defer srvA.Close()
	srvB := httptest.NewServer(handler)
	defer srvB.Close()

	for _, url := range []string{srvA.URL, srvB.URL} {
		resp, err := http.Get(url)
		require.NoError(t, err, "request failed")
		_ = resp.Body.Close()
	}

	// Shut down only the SECOND server. Before the per-server fix, only the
	// first server ever got a hook, so this would leave stopping open.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, srvB.Config.Shutdown(ctx), "server shutdown")

	requireClosed(t, cs.stopping, time.Second, "stopping was not closed by the second server's shutdown hook")

	// Shutting down the first server too must not double-close stopping.
	require.NoError(t, srvA.Config.Shutdown(ctx), "server shutdown")
}

// TestServeHTTP_RegistersShutdownHook verifies the hook is wired for every
// dashboard request, not just SSE. Registering only in serveAPIStream leaves
// a window where shutdown starts between the page load and the SSE request,
// and the hook is never registered.
func TestServeHTTP_RegistersShutdownHook(t *testing.T) {
	cs := &CaddyScope{
		Path:          "/dashboard",
		stopping:      make(chan struct{}),
		stopOnce:      &sync.Once{},
		hookedServers: &sync.Map{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unauthenticated non-SSE request; hook must register before dispatch.
		_ = cs.ServeHTTP(w, r, nil)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/dashboard/api/vhosts")
	require.NoError(t, err, "request failed")
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Config.Shutdown(ctx), "server shutdown")

	requireClosed(t, cs.stopping, time.Second, "ServeHTTP did not register the shutdown hook")
}

// TestServeAPIStream_ExitsWhenStoppingClosed verifies the SSE handler returns
// promptly when cs.stopping closes, which is what lets server.Shutdown() finish
// instead of hanging for the full grace period.
func TestServeAPIStream_ExitsWhenStoppingClosed(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cs := &CaddyScope{
		Refresh:       caddy.Duration(60 * time.Second), // long, so the ticker never fires
		snap:          &snapshot{},
		stopping:      make(chan struct{}),
		stopOnce:      &sync.Once{},
		hookedServers: &sync.Map{},
		cancelCtx:     cancelCtx,
	}

	req := httptest.NewRequest("GET", "/dashboard/api/stream", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		cs.serveAPIStream(rec, req)
		close(done)
	}()

	// Give the handler a moment to enter its select loop, then signal shutdown.
	time.Sleep(50 * time.Millisecond)
	close(cs.stopping)

	requireClosed(t, done, time.Second, "serveAPIStream did not exit after stopping was closed")
}

// TestRunSnapshotLoop_ClosesStoppingOnProcessExit verifies the exit poll.
// HTTP/3 streams never register a stdlib shutdown hook, so the snapshot loop
// must close cs.stopping once caddy.Exiting() reports true.
func TestRunSnapshotLoop_ClosesStoppingOnProcessExit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var exiting atomic.Bool
	cs := &CaddyScope{
		Refresh:       caddy.Duration(time.Hour), // never refresh during the test
		snap:          &snapshot{},
		httpAppOnce:   &sync.Once{},
		ctx:           caddy.Context{},
		done:          make(chan struct{}),
		stopping:      make(chan struct{}),
		stopOnce:      &sync.Once{},
		hookedServers: &sync.Map{},
		exiting:       exiting.Load,
	}

	go cs.runSnapshotLoop(ctx)

	select {
	case <-cs.stopping:
		require.FailNow(t, "stopping closed before the process started exiting")
	case <-time.After(2 * exitPollInterval):
	}

	exiting.Store(true)

	requireClosed(t, cs.stopping, time.Second, "exit poll did not close stopping")
	requireClosed(t, cs.done, time.Second, "snapshot loop did not return after exit")
}
