package caddyscope_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	exitCode := m.Run()

	// Stop the in-process Caddy server so our module's Cleanup() is called
	// and runSnapshotLoop exits cleanly.
	if err := caddy.Stop(); err != nil {
		fmt.Println("Error stopping Caddy: " + err.Error())
	}
	time.Sleep(100 * time.Millisecond)

	if exitCode == 0 {
		if err := goleak.Find(
			// Caddy's own goroutines that outlive caddy.Stop().
			goleak.IgnoreTopFunction("github.com/caddyserver/caddy/v2/cmd.cmdRun"),
			goleak.IgnoreTopFunction("github.com/caddyserver/caddy/v2.trapSignalsCrossPlatform.func1"),
			goleak.IgnoreTopFunction("github.com/caddyserver/caddy/v2.trapSignalsPosix.func1"),
			goleak.IgnoreTopFunction("go.uber.org/automaxprocs/maxprocs.Set.func1"),
			goleak.IgnoreTopFunction("github.com/caddyserver/certmagic.(*Cache).maintainAssets"),
			goleak.IgnoreAnyFunction("github.com/caddyserver/certmagic.keepLockfileFresh"),
			goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
			goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			exitCode = 1
		}
	}

	os.Exit(exitCode)
}
