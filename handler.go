package caddyscope

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (cs *CaddyScope) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Only handle requests under our path prefix.
	if !strings.HasPrefix(r.URL.Path, cs.Path) {
		return next.ServeHTTP(w, r)
	}

	// Register the shutdown hook for every dashboard request, not just SSE.
	// Registering only when the SSE stream starts leaves a window where a
	// shutdown beginning between page load and stream start misses the hook.
	cs.registerShutdownHook(r)

	// Auth check for all dashboard routes.
	if !cs.checkAuth(r) {
		requireAuth(w)
		return nil
	}

	// Strip the path prefix for internal routing.
	trimmed := strings.TrimPrefix(r.URL.Path, cs.Path)
	if trimmed == "" || trimmed == "/" {
		cs.serveDashboard(w, r)
		return nil
	}

	switch {
	case trimmed == "/api/vhosts":
		cs.serveAPIVhosts(w, r)
	case trimmed == "/api/stats":
		cs.serveAPIStats(w, r)
	case trimmed == "/api/stream":
		cs.serveAPIStream(w, r)
	case strings.HasPrefix(trimmed, "/static/"):
		cs.serveStatic(w, r)
	default:
		http.NotFound(w, r)
	}

	return nil
}

// logWarn logs at warn level. The logger is nil in unit tests.
func (cs *CaddyScope) logWarn(msg string, fields ...zap.Field) {
	if cs.logger != nil {
		cs.logger.Warn(msg, fields...)
	}
}

// logError logs at error level. The logger is nil in unit tests.
func (cs *CaddyScope) logError(msg string, fields ...zap.Field) {
	if cs.logger != nil {
		cs.logger.Error(msg, fields...)
	}
}

func (cs *CaddyScope) serveDashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := cs.tmpl.Execute(w, map[string]any{
		"Path":    cs.Path,
		"Refresh": int(time.Duration(cs.Refresh) / time.Millisecond),
	})
	if err != nil {
		cs.logError("failed to execute dashboard template", zap.Error(err))
	}
}

// resolveHTTPApp looks up the running HTTP app once. The lookup is deferred to
// request time because the HTTP app is not provisioned yet during Provision().
func (cs *CaddyScope) resolveHTTPApp() {
	cs.httpAppOnce.Do(cs.loadHTTPApp)
}

func (cs *CaddyScope) loadHTTPApp() {
	app, err := httpAppFromContext(cs.ctx)
	if err != nil {
		cs.logWarn("could not obtain http app", zap.Error(err))
		return
	}
	cs.httpApp = app

	if app != nil && !perHostMetricsEnabled(app) {
		cs.logWarn("per-host metrics are disabled; enable the metrics global option with per_host for per-host stats")
	}
}

// httpAppFromContext returns the configured HTTP app, or nil if there is none.
func httpAppFromContext(ctx caddy.Context) (*caddyhttp.App, error) {
	appIface, err := ctx.AppIfConfigured("http")
	if err != nil {
		return nil, err
	}
	if appIface == nil {
		return nil, nil
	}
	httpApp, ok := appIface.(*caddyhttp.App)
	if !ok {
		return nil, fmt.Errorf("http app has unexpected type %T", appIface)
	}
	return httpApp, nil
}

// perHostMetricsEnabled reports whether Caddy records metrics per host.
func perHostMetricsEnabled(app *caddyhttp.App) bool {
	return app.Metrics != nil && app.Metrics.PerHost
}

// currentVhosts returns the virtual hosts of the running HTTP app, never nil.
func (cs *CaddyScope) currentVhosts() []VHost {
	vhosts := enumerateVhosts(cs.httpApp)
	if vhosts == nil {
		return []VHost{}
	}
	return vhosts
}

// currentStats returns per-host stats from the metrics registry, never nil.
func (cs *CaddyScope) currentStats() []HostStats {
	if cs.gatherer == nil {
		return []HostStats{}
	}
	stats := gatherStats(cs.gatherer)
	if stats == nil {
		return []HostStats{}
	}
	return stats
}

func (cs *CaddyScope) serveAPIVhosts(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cs.resolveHTTPApp()
	cs.writeJSON(w, cs.currentVhosts())
}

func (cs *CaddyScope) serveAPIStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cs.writeJSON(w, cs.currentStats())
}

// writeJSON encodes v to w and logs any failure. The response status has
// already been sent at this point, so logging is the only remaining option.
func (cs *CaddyScope) writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		cs.logError("failed to encode JSON response", zap.Error(err))
	}
}

func (cs *CaddyScope) serveStatic(w http.ResponseWriter, r *http.Request) {
	// Map the request path to the embedded filesystem.
	// Request: /dashboard/static/style.css → embedded: ui/static/style.css
	trimmed := strings.TrimPrefix(r.URL.Path, cs.Path)
	filePath := "ui" + trimmed // trimmed starts with /static/...

	http.ServeFileFS(w, r, embeddedFS, filePath)
}
