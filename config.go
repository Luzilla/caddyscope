package caddyscope

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyevents"
	"go.uber.org/zap"
)

// Provision sets up the module.
func (cs *CaddyScope) Provision(ctx caddy.Context) error {
	cs.logger = ctx.Logger()

	// Normalize path: strip trailing slash to ensure consistent routing.
	cs.Path = strings.TrimRight(cs.Path, "/")

	if cs.Refresh == 0 {
		cs.Refresh = caddy.Duration(5 * time.Second)
	}

	// Store the Caddy context for lazy httpApp lookup at request time.
	// The HTTP app is not yet fully provisioned during our Provision(),
	// so we defer the lookup to when the first request arrives.
	cs.ctx = ctx
	cs.httpAppOnce = &sync.Once{}

	// Use Caddy's metrics registry (not the default Prometheus one).
	// Caddy registers all HTTP metrics with its own registry.
	if cs.gatherer == nil {
		registry := ctx.GetMetricsRegistry()
		if registry != nil {
			cs.gatherer = registry.Gather
		}
	}

	tmpl, err := template.ParseFS(embeddedFS, "ui/templates/dashboard.html")
	if err != nil {
		return fmt.Errorf("caddyscope: failed to parse dashboard template: %v", err)
	}
	cs.tmpl = tmpl

	cs.snap = &snapshot{}

	cs.cancelCtx, cs.cancel = context.WithCancel(ctx)
	cs.done = make(chan struct{})
	cs.loopStarted = &atomic.Bool{}
	cs.stopping = make(chan struct{})
	cs.stopOnce = &sync.Once{}
	cs.hookedServers = &sync.Map{}
	cs.exiting = caddy.Exiting

	// Start the snapshot loop only after Caddy has started every app. Starting
	// it here would read the HTTP app while Caddy is still provisioning it.
	if err := onStarted(ctx, cs.startSnapshotLoop); err != nil {
		return fmt.Errorf("caddyscope: %v", err)
	}

	ctx.Logger().Info("caddyscope provisioned",
		zap.String("path", cs.Path),
		zap.Duration("refresh", time.Duration(cs.Refresh)),
	)

	return nil
}

// onStarted runs fn once Caddy emits its "started" event.
func onStarted(ctx caddy.Context, fn func()) error {
	eventsIface, err := ctx.App("events")
	if err != nil {
		return fmt.Errorf("getting events app: %v", err)
	}
	events, ok := eventsIface.(*caddyevents.App)
	if !ok {
		return fmt.Errorf("events app has unexpected type %T", eventsIface)
	}
	return events.On("started", eventFunc(fn))
}

// eventFunc adapts a plain func to caddyevents.Handler.
type eventFunc func()

func (f eventFunc) Handle(context.Context, caddy.Event) error {
	f()
	return nil
}

// startSnapshotLoop starts the background loop exactly once.
func (cs *CaddyScope) startSnapshotLoop() {
	if cs.loopStarted.Swap(true) {
		return
	}
	go cs.runSnapshotLoop(cs.cancelCtx)
}

// Validate ensures the configuration is correct.
func (cs *CaddyScope) Validate() error {
	if cs.Path == "" {
		return fmt.Errorf("caddyscope: path is required")
	}
	if !strings.HasPrefix(cs.Path, "/") {
		return fmt.Errorf("caddyscope: path must start with /")
	}
	if cs.Username == "" {
		return fmt.Errorf("caddyscope: username is required")
	}
	if cs.PasswordHash == "" {
		return fmt.Errorf("caddyscope: password is required")
	}
	if !strings.HasPrefix(cs.PasswordHash, "$2a$") && !strings.HasPrefix(cs.PasswordHash, "$2b$") {
		return fmt.Errorf("caddyscope: password must be a bcrypt hash (starting with $2a$ or $2b$)")
	}
	return nil
}

// Cleanup stops the background goroutine.
func (cs *CaddyScope) Cleanup() error {
	if cs.cancel != nil {
		cs.cancel()
	}
	if cs.loopStarted == nil || !cs.loopStarted.Load() {
		return nil
	}
	select {
	case <-cs.done:
	case <-time.After(5 * time.Second):
		cs.logWarn("caddyscope: timed out waiting for snapshot loop to exit")
	}
	return nil
}
