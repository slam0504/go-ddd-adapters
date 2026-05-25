// Package httpstdlib wraps Go's net/http server as a bootstrap.Module.
//
// The listener is bound synchronously inside Module().Start, so a
// port-in-use surfaces as a startup error rather than a goroutine-only
// log line. Module().Stop runs server.Shutdown under a configurable
// timeout (default 15s) and returns context.DeadlineExceeded if the
// shutdown does not complete in time.
//
// # Typical usage
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("GET /orders", listOrders)
//	app.Use(httpstdlib.Module(":8080", mux))
//
// # Observing the resolved address (":0" / ephemeral ports)
//
// When the configured addr ends in ":0", use the Server form so the
// OS-assigned port is observable after Start:
//
//	srv := httpstdlib.New("127.0.0.1:0", mux)
//	app.Use(srv.Module())
//	// after app.Start: srv.Addr() returns e.g. "127.0.0.1:54321".
//
// # Health probes
//
// For /healthz and /readyz handlers backed by ports/health.Check, see
// the sub-package transport/http/stdlib/health.
package httpstdlib
