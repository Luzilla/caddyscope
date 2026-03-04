package caddyscope

import (
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

// VHost represents a virtual host extracted from Caddy's running configuration.
type VHost struct {
	Host      string     `json:"host"`
	Server    string     `json:"server"`
	Listen    []string   `json:"listen"`
	Upstreams []Upstream `json:"upstreams"`
}

// Upstream represents a reverse proxy upstream target.
type Upstream struct {
	Address string `json:"address"`
	Healthy bool   `json:"healthy"`
}

// enumerateVhosts walks the running caddyhttp.App configuration and extracts
// virtual hosts with their listen addresses and reverse proxy upstreams.
func enumerateVhosts(httpApp *caddyhttp.App) []VHost {
	if httpApp == nil {
		return nil
	}

	var vhosts []VHost
	for name, srv := range httpApp.Servers {
		for _, route := range srv.Routes {
			vhosts = append(vhosts, routeVhosts(name, srv, route)...)
		}
	}
	return vhosts
}

// routeVhosts returns one VHost per host matched by the route.
func routeVhosts(server string, srv *caddyhttp.Server, route caddyhttp.Route) []VHost {
	hosts := extractHosts(route)
	if len(hosts) == 0 {
		return nil
	}

	upstreams := upstreamsFromHandlers(route.Handlers)
	vhosts := make([]VHost, 0, len(hosts))
	for _, host := range hosts {
		vhosts = append(vhosts, VHost{
			Host:      host,
			Server:    server,
			Listen:    srv.Listen,
			Upstreams: upstreams,
		})
	}
	return vhosts
}

// extractHosts returns all host patterns from the route's matcher sets.
func extractHosts(route caddyhttp.Route) []string {
	var hosts []string
	for _, matcherSet := range route.MatcherSets {
		for _, matcher := range matcherSet {
			if hm, ok := matcher.(*caddyhttp.MatchHost); ok {
				hosts = append(hosts, []string(*hm)...)
			}
		}
	}
	return hosts
}

// upstreamsFromHandlers returns reverse proxy upstream info from the handlers,
// recursing into subroutes since Caddy wraps per-host routes in subroutes.
func upstreamsFromHandlers(handlers []caddyhttp.MiddlewareHandler) []Upstream {
	var upstreams []Upstream
	for _, handler := range handlers {
		switch h := handler.(type) {
		case *reverseproxy.Handler:
			upstreams = append(upstreams, proxyUpstreams(h)...)
		case *caddyhttp.Subroute:
			upstreams = append(upstreams, subrouteUpstreams(h)...)
		default:
			// Other handler types carry no upstreams.
		}
	}
	return upstreams
}

func subrouteUpstreams(sub *caddyhttp.Subroute) []Upstream {
	var upstreams []Upstream
	for _, route := range sub.Routes {
		upstreams = append(upstreams, upstreamsFromHandlers(route.Handlers)...)
	}
	return upstreams
}

func proxyUpstreams(h *reverseproxy.Handler) []Upstream {
	upstreams := make([]Upstream, 0, len(h.Upstreams))
	for _, u := range h.Upstreams {
		upstreams = append(upstreams, Upstream{
			Address: u.Dial,
			Healthy: isUpstreamHealthy(u),
		})
	}
	return upstreams
}

// isUpstreamHealthy checks if an upstream is healthy, handling the case
// where the Host field may be nil (e.g. in tests or before provisioning).
func isUpstreamHealthy(u *reverseproxy.Upstream) bool {
	if u.Host == nil {
		return true // assume healthy if health checking is not initialized
	}
	return u.Healthy()
}
