// Command samweb-agent-test runs the SamWeb Agent HTTP server against an
// in-memory mock backend, so the full API contract can be exercised
// without a real webview (e.g. in CI / headless environments).
//
// Usage:
//
//	samweb-agent-test [--addr 0.0.0.0:7777] [--token my-secret]
//
// Once running, point any HTTP client at http://<addr>/agent/* to drive
// the fake browser.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/samaidev/samweb/internal/agent"
)

func main() {
	var (
		addr  = flag.String("addr", "0.0.0.0:7777", "address the agent HTTP server binds to")
		token = flag.String("token", "", "optional bearer token; if set, requests must carry 'Authorization: Bearer <token>'")
	)
	flag.Parse()

	backend := agent.NewMockBackend()
	srv := agent.NewServer(*addr, *token, backend)

	// Graceful shutdown on SIGINT / SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Fprintln(os.Stderr, "\n[samweb-agent-test] shutting down...")
		_ = srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
		fmt.Fprintf(os.Stderr, "samweb-agent-test: %v\n", err)
		os.Exit(1)
	}
}
