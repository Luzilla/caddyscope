package caddyscope_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/caddytest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/luzilla/caddyscope"
)

const testCaddyfile = `
{
	skip_install_trust
	admin localhost:2999
	http_port     9080
	https_port    9443
	grace_period  1ns
	metrics {
		per_host
	}
}

localhost:9080 {
	caddyscope /dashboard {
		username admin
		# bcrypt("admin") at cost 4: a cost-14 hash takes ~1s per request,
		# and over 10s under -race, which is past the client timeout.
		password $2a$04$s2PTTX9iRzjehIN4R9xCCOq31.g5bNNacSESwAeYKKsPGY.y6Vo4O
		refresh 1s
	}

	route /hello {
		respond "Hello"
	}
}
`

func newTester(t *testing.T) *caddytest.Tester {
	t.Helper()
	tester := caddytest.NewTester(t)
	tester.InitServer(testCaddyfile, "caddyfile")
	return tester
}

// doRequest is a helper that executes a GET with optional basic auth and returns the response + body.
func doRequest(t *testing.T, client *http.Client, url string, auth bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err, "failed to create request")
	if auth {
		req.SetBasicAuth("admin", "admin")
	}
	resp, err := client.Do(req)
	require.NoError(t, err, "request to %s failed", url)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "failed to read body")
	return resp, string(b)
}

// getJSON fetches url with basic auth, requires a 200 JSON response, and
// decodes the body into v.
func getJSON(t *testing.T, client *http.Client, url string, v any) string {
	t.Helper()
	resp, body := doRequest(t, client, url, true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	require.NoError(t, json.Unmarshal([]byte(body), v), "invalid JSON")
	return body
}

// hasHost reports whether any vhost entry is for the given host.
func hasHost(vhosts []map[string]any, host string) bool {
	for _, v := range vhosts {
		if v["host"] == host {
			return true
		}
	}
	return false
}

// hasTraffic reports whether any host recorded at least one request.
func hasTraffic(stats []map[string]any) bool {
	for _, s := range stats {
		if total, ok := s["total_requests"].(float64); ok && total > 0 {
			return true
		}
	}
	return false
}

// firstSSEData reads lines until the first "data:" line and returns its payload.
func firstSSEData(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			return data
		}
	}
	return ""
}

func TestDashboard(t *testing.T) {
	t.Run("RequiresAuth", func(t *testing.T) {
		tester := newTester(t)
		resp, _ := doRequest(t, tester.Client, "http://localhost:9080/dashboard", false)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.NotEmpty(t, resp.Header.Get("WWW-Authenticate"), "expected WWW-Authenticate header on 401 response")
	})

	t.Run("WrongCredentials", func(t *testing.T) {
		tester := newTester(t)
		req, _ := http.NewRequest("GET", "http://localhost:9080/dashboard", nil)
		req.SetBasicAuth("admin", "wrongpassword")
		resp, err := tester.Client.Do(req)
		require.NoError(t, err, "request failed")
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("ReturnsHTML", func(t *testing.T) {
		tester := newTester(t)
		resp, body := doRequest(t, tester.Client, "http://localhost:9080/dashboard", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
		for _, want := range []string{"<title>", `base-path`, "<script", "observablehq/plot"} {
			assert.Contains(t, body, want)
		}
	})

	t.Run("TrailingSlash", func(t *testing.T) {
		tester := newTester(t)
		resp, _ := doRequest(t, tester.Client, "http://localhost:9080/dashboard/", true)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("UnknownPath_Returns404", func(t *testing.T) {
		tester := newTester(t)
		resp, _ := doRequest(t, tester.Client, "http://localhost:9080/dashboard/unknown", true)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestAPI(t *testing.T) {
	t.Run("Vhosts_ReturnsJSON", func(t *testing.T) {
		tester := newTester(t)
		var vhosts []map[string]any
		body := getJSON(t, tester.Client, "http://localhost:9080/dashboard/api/vhosts", &vhosts)
		assert.True(t, hasHost(vhosts, "localhost"), "expected localhost in vhosts, got %s", body)
	})

	t.Run("Stats_ReturnsJSON", func(t *testing.T) {
		tester := newTester(t)
		var stats []any
		getJSON(t, tester.Client, "http://localhost:9080/dashboard/api/stats", &stats)
	})

	t.Run("Stats_AfterTraffic", func(t *testing.T) {
		tester := newTester(t)

		// Generate some traffic.
		for range 5 {
			doRequest(t, tester.Client, "http://localhost:9080/hello", false)
		}

		// Give the snapshot loop time to pick up the new metrics.
		time.Sleep(2 * time.Second)

		var stats []map[string]any
		body := getJSON(t, tester.Client, "http://localhost:9080/dashboard/api/stats", &stats)
		require.NotEmpty(t, stats, "expected non-empty stats after traffic")
		assert.True(t, hasTraffic(stats), "expected at least one host with non-zero total_requests, got: %s", body)
	})

	t.Run("Stream_ReturnsSSE", func(t *testing.T) {
		tester := newTester(t)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:9080/dashboard/api/stream", nil)
		req.SetBasicAuth("admin", "admin")

		resp, err := tester.Client.Do(req)
		require.NoError(t, err, "request failed")
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

		dataLine := firstSSEData(resp.Body)
		require.NotEmpty(t, dataLine, "no data line received from SSE stream")

		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(dataLine), &payload), "invalid JSON in SSE data: %s", dataLine)
		assert.Contains(t, payload, "timestamp", "SSE payload missing 'timestamp' field")
	})

	t.Run("StaticCSS_Served", func(t *testing.T) {
		tester := newTester(t)
		resp, body := doRequest(t, tester.Client, "http://localhost:9080/dashboard/static/style.css", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.NotEmpty(t, body, "expected non-empty CSS body")
	})
}

func TestNonDashboardPath_PassesThrough(t *testing.T) {
	tester := newTester(t)
	resp, body := doRequest(t, tester.Client, "http://localhost:9080/hello", false)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "Hello", strings.TrimSpace(body))
}
