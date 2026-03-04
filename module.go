// Package caddyscope is a Caddy module that serves a live dashboard of
// virtual hosts, request metrics, and upstream health.
package caddyscope

import (
	"context"
	"embed"
	"html/template"
	"sync"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

//go:embed ui/templates/* ui/static/*
var embeddedFS embed.FS

func init() {
	caddy.RegisterModule(CaddyScope{})
	httpcaddyfile.RegisterDirective("caddyscope", parseCaddyfile)
	httpcaddyfile.RegisterDirectiveOrder("caddyscope", httpcaddyfile.After, "basicauth")
}

// CaddyScope implements a Caddy module that provides a real-time dashboard
// for monitoring virtual hosts, request metrics, and upstream health.
type CaddyScope struct {
	// Path is the URL path prefix where the dashboard is mounted.
	Path string `json:"path,omitempty"`

	// Username for basic auth access to the dashboard.
	Username string `json:"username,omitempty"`

	// PasswordHash is a bcrypt hash of the password for basic auth.
	PasswordHash string `json:"password_hash,omitempty"`

	// Refresh is the interval for SSE push updates.
	Refresh caddy.Duration `json:"refresh,omitempty"`

	// internal fields set during Provision
	logger      *zap.Logger
	cancel      func()
	cancelCtx   context.Context
	done        chan struct{}
	loopStarted *atomic.Bool // set when runSnapshotLoop has been started
	ctx         caddy.Context
	httpAppOnce *sync.Once
	httpApp     *caddyhttp.App
	gatherer    metricsGatherer
	snap        *snapshot
	tmpl        *template.Template

	// stopping is closed when any underlying HTTP server begins shutdown.
	// It lets long-lived SSE handlers exit before server.Shutdown() drains
	// connections, avoiding a shutdown deadlock. stopOnce guards the close;
	// hookedServers tracks which stdlib servers already have the hook, since
	// Caddy runs one stdlib *http.Server per server block and shutdown waits
	// for all of them to drain.
	stopping      chan struct{}
	stopOnce      *sync.Once
	hookedServers *sync.Map

	// exiting reports whether the process is shutting down. Set to
	// caddy.Exiting in Provision; tests override it.
	exiting func() bool
}

// CaddyModule returns the Caddy module information.
func (CaddyScope) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.caddyscope",
		New: func() caddy.Module { return new(CaddyScope) },
	}
}

func parseCaddyfile(h httpcaddyfile.Helper) ([]httpcaddyfile.ConfigValue, error) {
	var cs CaddyScope
	err := cs.UnmarshalCaddyfile(h.Dispenser)
	if err != nil {
		return nil, err
	}

	// Build a path matcher for the configured path prefix.
	matcherSet := caddy.ModuleMap{
		"path": caddyconfig.JSON(caddyhttp.MatchPath{cs.Path + "*"}, nil),
	}

	return h.NewRoute(matcherSet, &cs), nil
}

// UnmarshalCaddyfile parses the caddyscope directive. Syntax:
//
//	caddyscope <path> {
//	    username <name>
//	    password <bcrypt_hash>
//	    refresh  <duration>
//	}
func (cs *CaddyScope) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name

	if d.NextArg() {
		cs.Path = d.Val()
	}

	for d.NextBlock(0) {
		key := d.Val()
		if !d.NextArg() {
			return d.ArgErr()
		}
		if err := cs.setOption(d, key, d.Val()); err != nil {
			return err
		}
	}

	return nil
}

// setOption applies one sub-directive of the caddyscope block.
func (cs *CaddyScope) setOption(d *caddyfile.Dispenser, key, val string) error {
	switch key {
	case "username":
		cs.Username = val
	case "password":
		cs.PasswordHash = val
	case "refresh":
		dur, err := caddy.ParseDuration(val)
		if err != nil {
			return d.Errf("invalid refresh duration: %v", err)
		}
		cs.Refresh = caddy.Duration(dur)
	default:
		return d.Errf("unrecognized sub-directive: %s", key)
	}
	return nil
}

// Interface guards
var (
	_ caddy.Module                = (*CaddyScope)(nil)
	_ caddy.Provisioner           = (*CaddyScope)(nil)
	_ caddy.Validator             = (*CaddyScope)(nil)
	_ caddy.CleanerUpper          = (*CaddyScope)(nil)
	_ caddyhttp.MiddlewareHandler = (*CaddyScope)(nil)
	_ caddyfile.Unmarshaler       = (*CaddyScope)(nil)
)
